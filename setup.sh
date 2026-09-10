#!/usr/bin/env bash
set -euo pipefail

# ECS Controller deployment wizard.
# Application credentials and Alibaba Cloud settings are configured after the first login.

RELEASE_REPO="${ECS_RELEASE_REPO:-elunez/ecs-controller}"
SETUP_REPO="${ECS_SETUP_REPO:-JudiLite/ecs-controller}"
DEPLOY_DIR="${ECS_DEPLOY_DIR:-/opt/ecs-controller}"
STATE_FILE="$DEPLOY_DIR/.setup_state"
COMPOSE_FILE="$DEPLOY_DIR/docker-compose.yml"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
RESET='\033[0m'

die() {
    echo -e "${RED}错误：$*${RESET}" >&2
    exit 1
}

require_root() {
    [[ "$(id -u)" -eq 0 ]] || die "请使用 root 权限运行。"
    [[ "$(uname -s)" == "Linux" ]] || die "当前部署向导仅支持 Linux。"
}

persist_self() {
    mkdir -p "$DEPLOY_DIR"
    if [[ -f "${BASH_SOURCE[0]:-}" ]]; then
        install -m 0755 "${BASH_SOURCE[0]}" "$DEPLOY_DIR/setup.sh"
    else
        local temp_setup
        temp_setup=$(mktemp)
        curl -fsSL "https://raw.githubusercontent.com/$SETUP_REPO/main/setup.sh" -o "$temp_setup"
        install -m 0755 "$temp_setup" "$DEPLOY_DIR/setup.sh"
        rm -f "$temp_setup"
    fi
}

ask() {
    local prompt="$1" default="$2" answer
    if [[ -n "$default" ]]; then
        read -r -p "$prompt [$default]: " answer </dev/tty || answer=""
        printf '%s' "${answer:-$default}"
    else
        read -r -p "$prompt: " answer </dev/tty || answer=""
        printf '%s' "$answer"
    fi
}

yes_no() {
    local prompt="$1" default="$2" answer
    read -r -p "$prompt [$default]: " answer </dev/tty || answer=""
    answer="${answer:-$default}"
    [[ "${answer,,}" == "y" || "${answer,,}" == "yes" ]]
}

load_state() {
    INSTALL_MODE="docker"
    ENABLE_HTTPS="n"
    USE_DOMAIN="n"
    DOMAIN=""
    HTTP_PORT="80"
    HTTPS_PORT="443"
    APP_PORT="43211"
    CONTAINER_NAME="ecs-controller"
    if [[ -f "$STATE_FILE" ]]; then
        # shellcheck source=/dev/null
        source "$STATE_FILE"
    fi
}

save_state() {
    mkdir -p "$DEPLOY_DIR"
    cat > "$STATE_FILE" <<EOF
INSTALL_MODE=$(printf '%q' "$INSTALL_MODE")
ENABLE_HTTPS=$(printf '%q' "$ENABLE_HTTPS")
USE_DOMAIN=$(printf '%q' "$USE_DOMAIN")
DOMAIN=$(printf '%q' "$DOMAIN")
HTTP_PORT=$(printf '%q' "$HTTP_PORT")
HTTPS_PORT=$(printf '%q' "$HTTPS_PORT")
APP_PORT=$(printf '%q' "$APP_PORT")
CONTAINER_NAME=$(printf '%q' "$CONTAINER_NAME")
EOF
    chmod 0600 "$STATE_FILE"
}

check_port() {
    [[ "$1" =~ ^[0-9]+$ ]] || die "端口必须是数字：$1"
    (( "$1" >= 1 && "$1" <= 65535 )) || die "端口范围必须是 1-65535：$1"
}

collect_config() {
    echo ""
    echo -e "${CYAN}${BOLD}部署配置${RESET}"
    echo "应用账号、密码、阿里云凭据等内容将在首次登录网页后配置。"
    echo ""

    INSTALL_MODE="docker"
    echo "安装方式："
    echo "  1) Docker Compose（推荐）"
    echo "  2) 原生 systemd/OpenRC"
    local mode
    mode=$(ask "请选择" "1")
    [[ "$mode" == "2" ]] && INSTALL_MODE="native"

    if [[ "$INSTALL_MODE" == "docker" ]]; then
        CONTAINER_NAME=$(ask "容器名称" "$CONTAINER_NAME")
        [[ "$CONTAINER_NAME" =~ ^[a-z0-9][a-z0-9_.-]*$ ]] ||
            die "容器名称只能包含小写字母、数字、点、下划线和短横线。"
    fi

    if yes_no "是否设置域名？(y/n)" "$USE_DOMAIN"; then
        USE_DOMAIN="y"
        DOMAIN=$(ask "域名（不含 http:// 或 https://）" "$DOMAIN")
        DOMAIN="${DOMAIN#https://}"
        DOMAIN="${DOMAIN#http://}"
        DOMAIN="${DOMAIN%%/*}"
        [[ -n "$DOMAIN" ]] || die "启用域名后不能留空。"
    else
        USE_DOMAIN="n"
        DOMAIN=""
    fi

    if yes_no "是否启用 HTTPS？(y/n)" "$ENABLE_HTTPS"; then
        ENABLE_HTTPS="y"
        [[ "$USE_DOMAIN" == "y" ]] || die "自动 HTTPS 需要域名，请先设置域名。"
        HTTPS_PORT=$(ask "HTTPS 对外端口" "$HTTPS_PORT")
        check_port "$HTTPS_PORT"
        HTTP_PORT=$(ask "HTTP 对外端口（用于证书申请和跳转）" "$HTTP_PORT")
        check_port "$HTTP_PORT"
    else
        ENABLE_HTTPS="n"
        HTTP_PORT=$(ask "HTTP 对外端口" "$HTTP_PORT")
        check_port "$HTTP_PORT"
    fi

    if [[ "$INSTALL_MODE" == "docker" && "$USE_DOMAIN" == "n" ]]; then
        APP_PORT=$(ask "应用对外端口" "$APP_PORT")
        check_port "$APP_PORT"
    fi

    save_state
}

ensure_docker() {
    if ! command -v docker >/dev/null 2>&1; then
        echo -e "${YELLOW}未检测到 Docker，正在自动安装...${RESET}"
        curl -fsSL https://get.docker.com | sh
    fi
    systemctl enable --now docker >/dev/null 2>&1 || true
    docker compose version >/dev/null 2>&1 || die "需要 Docker Compose v2（docker compose）。"
}

download_release() {
    local arch asset version work_dir
    case "$(uname -m)" in
        x86_64|amd64) arch="amd64" ;;
        aarch64|arm64) arch="arm64" ;;
        *) die "不支持的处理器架构：$(uname -m)" ;;
    esac

    version=$(curl -fsSL --retry 3 "https://api.github.com/repos/$RELEASE_REPO/releases/latest" |
        sed -n 's/^[[:space:]]*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)
    [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "无法获取有效的最新版本。"
    asset="ecs-controller-linux-$arch.tar.gz"
    work_dir=$(mktemp -d)
    trap 'rm -rf "$work_dir"' RETURN

    echo "正在下载 $RELEASE_REPO $version（Linux/$arch）..."
    curl -fsSL --retry 3 -o "$work_dir/$asset" \
        "https://github.com/$RELEASE_REPO/releases/download/$version/$asset"
    rm -rf "$DEPLOY_DIR/app"
    mkdir -p "$DEPLOY_DIR/app"
    tar -xzf "$work_dir/$asset" -C "$DEPLOY_DIR/app"
    [[ -x "$DEPLOY_DIR/app/ecs-controller" ]] || die "发布包缺少 ecs-controller。"
    echo "$version" > "$DEPLOY_DIR/.version"
}

write_docker_files() {
    cat > "$DEPLOY_DIR/app/Dockerfile" <<'EOF'
FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY ecs-controller template.html updater.sh ./
COPY static ./static
RUN chmod 0755 /app/ecs-controller /app/updater.sh
ENV ECS_APP_DIR=/app ECS_DATA_DIR=/data ECS_HTTP_ADDR=0.0.0.0:43211
EXPOSE 43211
CMD ["/app/ecs-controller"]
EOF

    cat > "$COMPOSE_FILE" <<EOF
services:
  ecs-controller:
    build: ./app
    image: ${CONTAINER_NAME}:latest
    container_name: ${CONTAINER_NAME}
    restart: unless-stopped
    environment:
      TZ: Asia/Shanghai
      ECS_APP_DIR: /app
      ECS_DATA_DIR: /data
      ECS_HTTP_ADDR: 0.0.0.0:43211
      ECS_COOKIE_SECURE: "$([[ "$ENABLE_HTTPS" == "y" ]] && echo 1 || echo 0)"
    volumes:
      - ./data:/data
EOF

    if [[ "$USE_DOMAIN" == "y" ]]; then
        cat >> "$COMPOSE_FILE" <<EOF
    expose:
      - "43211"
  caddy:
    image: caddy:2-alpine
    container_name: ${CONTAINER_NAME}-proxy
    restart: unless-stopped
    ports:
      - "${HTTP_PORT}:80"
      - "${HTTPS_PORT}:443"
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy_data:/data
      - caddy_config:/config
    depends_on:
      - ecs-controller
volumes:
  caddy_data:
  caddy_config:
EOF
        if [[ "$ENABLE_HTTPS" == "y" ]]; then
            cat > "$DEPLOY_DIR/Caddyfile" <<EOF
$DOMAIN {
    reverse_proxy ecs-controller:43211
}
EOF
        else
            cat > "$DEPLOY_DIR/Caddyfile" <<EOF
http://$DOMAIN {
    reverse_proxy ecs-controller:43211
}
EOF
        fi
    else
        cat >> "$COMPOSE_FILE" <<EOF
    ports:
      - "${APP_PORT}:43211"
EOF
    fi
}

install_docker() {
    ensure_docker
    download_release
    mkdir -p "$DEPLOY_DIR/data"
    write_docker_files
    (cd "$DEPLOY_DIR" && docker compose config >/dev/null && docker compose up -d --build)
    write_deploy_info
    echo -e "${GREEN}部署完成。${RESET}"
}

install_native() {
    local cookie_secure="0"
    [[ "$ENABLE_HTTPS" == "y" ]] && cookie_secure="1"
    curl -fsSL "https://raw.githubusercontent.com/${ECS_RELEASE_REPO:-$RELEASE_REPO}/main/install.sh" |
        ECS_NONINTERACTIVE=1 ECS_COOKIE_SECURE="$cookie_secure" sh
    write_deploy_info
    echo -e "${GREEN}原生部署完成。HTTPS 证书和反向代理请使用已有的 Nginx/Caddy 配置。${RESET}"
}

write_deploy_info() {
    local address
    if [[ "$USE_DOMAIN" == "y" ]]; then
        if [[ "$ENABLE_HTTPS" == "y" ]]; then
            address="https://$DOMAIN"
            [[ "$HTTPS_PORT" != "443" ]] && address="$address:$HTTPS_PORT"
        else
            address="http://$DOMAIN"
            [[ "$HTTP_PORT" != "80" ]] && address="$address:$HTTP_PORT"
        fi
    else
        address="http://服务器IP:${APP_PORT}"
    fi
    cat > "$DEPLOY_DIR/DEPLOY_INFO.txt" <<EOF
ECS Controller 部署信息
生成时间: $(date '+%Y-%m-%d %H:%M:%S')
访问地址: $address
部署方式: $INSTALL_MODE
容器名称: ${CONTAINER_NAME:-不适用}
首次登录后，请在网页中完成账号、阿里云凭据和其他应用配置。
EOF
    chmod 0600 "$DEPLOY_DIR/DEPLOY_INFO.txt"
    echo "访问信息已保存到 $DEPLOY_DIR/DEPLOY_INFO.txt"
}

show_status() {
    if [[ -f "$COMPOSE_FILE" ]]; then
        (cd "$DEPLOY_DIR" && docker compose ps)
    else
        systemctl status ecs-controller.service --no-pager || true
    fi
}

show_logs() {
    if [[ -f "$COMPOSE_FILE" ]]; then
        (cd "$DEPLOY_DIR" && docker compose logs --tail=100 -f)
    else
        journalctl -u ecs-controller.service -n 100 -f
    fi
}

uninstall() {
    if [[ -f "$COMPOSE_FILE" ]]; then
        (cd "$DEPLOY_DIR" && docker compose down)
    else
        curl -fsSL "https://raw.githubusercontent.com/$RELEASE_REPO/main/uninstall.sh" | sh
    fi
    echo "服务已停止。数据目录仍保留：$DEPLOY_DIR/data"
}

main_menu() {
    require_root
    persist_self
    load_state
    while true; do
        echo ""
        echo -e "${BOLD}ECS Controller 部署向导${RESET}"
        echo "  1) 安装 / 更新"
        echo "  2) 查看状态"
        echo "  3) 重启服务"
        echo "  4) 查看日志"
        echo "  5) 卸载服务（保留数据）"
        echo "  0) 退出"
        local choice
        choice=$(ask "请选择" "1")
        case "$choice" in
            1)
                collect_config
                [[ "$INSTALL_MODE" == "docker" ]] && install_docker || install_native
                ;;
            2) show_status ;;
            3)
                if [[ -f "$COMPOSE_FILE" ]]; then
                    (cd "$DEPLOY_DIR" && docker compose restart)
                else
                    systemctl restart ecs-controller.service
                fi
                ;;
            4) show_logs ;;
            5) uninstall ;;
            0) exit 0 ;;
            *) echo "无效选项。" ;;
        esac
    done
}

main_menu "$@"
