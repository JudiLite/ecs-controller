#!/usr/bin/env bash
set -euo pipefail

# ECS Controller deployment wizard.
# Application credentials and Alibaba Cloud settings are configured after the first login.

RELEASE_REPO="${ECS_RELEASE_REPO:-JudiLite/ecs-controller}"
SETUP_REPO="${ECS_SETUP_REPO:-JudiLite/ecs-controller}"
DEPLOY_DIR="${ECS_DEPLOY_DIR:-/opt/ecs-controller}"
STATE_FILE="$DEPLOY_DIR/.setup_state"
COMPOSE_FILE="$DEPLOY_DIR/docker-compose.yml"
UNINSTALL_SCRIPT="/usr/local/bin/ecs-uninstall"
DOWNLOAD_WORK_DIR=""

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
RESET='\033[0m'

cleanup() {
    if [[ -n "$DOWNLOAD_WORK_DIR" && -d "$DOWNLOAD_WORK_DIR" ]]; then
        rm -rf "$DOWNLOAD_WORK_DIR"
    fi
}
trap cleanup EXIT

die() {
    echo -e "${RED}错误：$*${RESET}" >&2
    exit 1
}

install_if_changed() {
    local mode="$1" source="$2" destination="$3"
    if [[ -f "$destination" ]] && cmp -s "$source" "$destination"; then
        chmod "$mode" "$destination"
        return 0
    fi
    install -m "$mode" "$source" "$destination"
}

require_root() {
    [[ "$(id -u)" -eq 0 ]] || die "请使用 root 权限运行。"
    [[ "$(uname -s)" == "Linux" ]] || die "当前部署向导仅支持 Linux。"
}

persist_self() {
    local script_source="${BASH_SOURCE[0]:-}"
    local source_dir temp_asset asset
    source_dir=$(dirname "$script_source")
    mkdir -p "$DEPLOY_DIR"
    if [[ -f "$script_source" && "$script_source" != /dev/stdin && "$script_source" != /proc/* ]]; then
        install_if_changed 0755 "$script_source" "$DEPLOY_DIR/setup.sh"
    else
        local temp_setup
        temp_setup=$(mktemp)
        curl -fsSL "https://raw.githubusercontent.com/$SETUP_REPO/main/setup.sh" -o "$temp_setup"
        install_if_changed 0755 "$temp_setup" "$DEPLOY_DIR/setup.sh"
        rm -f "$temp_setup"
    fi
    if [[ -f "$source_dir/ecs" ]]; then
        install_if_changed 0755 "$source_dir/ecs" /usr/local/bin/ecs
    else
        temp_asset=$(mktemp)
        curl -fsSL "https://raw.githubusercontent.com/$SETUP_REPO/main/ecs" -o "$temp_asset"
        install_if_changed 0755 "$temp_asset" /usr/local/bin/ecs
        rm -f "$temp_asset"
    fi
    for asset in Dockerfile.updater docker-updater.sh release-public-key.pem uninstall.sh; do
        if [[ -f "$source_dir/$asset" ]]; then
            asset_mode=0644
            [[ "$asset" == "uninstall.sh" ]] && asset_mode=0755
            install_if_changed "$asset_mode" "$source_dir/$asset" "$DEPLOY_DIR/$asset"
        else
            temp_asset=$(mktemp)
            asset_url="$asset"
            if [[ "$asset" == "release-public-key.pem" ]]; then
                asset_url="internal/app/release-public-key.pem"
            fi
            curl -fsSL "https://raw.githubusercontent.com/$SETUP_REPO/main/$asset_url" -o "$temp_asset"
            asset_mode=0644
            [[ "$asset" == "uninstall.sh" ]] && asset_mode=0755
            install_if_changed "$asset_mode" "$temp_asset" "$DEPLOY_DIR/$asset"
            rm -f "$temp_asset"
        fi
    done
    install_if_changed 0755 "$DEPLOY_DIR/uninstall.sh" "$UNINSTALL_SCRIPT"
    rm -f /usr/local/bin/aliyun
}

init_terminal() {
    if ! { exec 3<>/dev/tty; } 2>/dev/null; then
        die "未检测到交互终端，请通过 SSH 终端运行安装命令。"
    fi
}

ask() {
    local prompt="$1" default="$2" answer
    if [[ -n "$default" ]]; then
        printf '%s [默认 %s，回车使用]: ' "$prompt" "$default" >&2
    else
        printf '%s: ' "$prompt" >&2
    fi
    if ! IFS= read -r -u 3 answer; then
        printf '\n输入已结束，已取消操作。\n' >&2
        return 130
    fi
    answer="${answer#"${answer%%[![:space:]]*}"}"
    answer="${answer%"${answer##*[![:space:]]}"}"
    printf '%s' "${answer:-$default}"
}

ask_choice() {
    local prompt="$1" default="$2" answer option
    shift 2
    while true; do
        answer=$(ask "$prompt" "$default") || exit 130
        for option in "$@"; do
            if [[ "$answer" == "$option" ]]; then
                printf '%s' "$answer"
                return 0
            fi
        done
        printf '无效选项，请输入 %s。\n' "$*" >&2
    done
}

yes_no() {
    local prompt="$1" default="$2" answer hint
    if [[ "$default" == "y" ]]; then hint="Y/n"; else hint="y/N"; fi
    while true; do
        answer=$(ask "$prompt（$hint）" "$default") || exit 130
        case "${answer,,}" in
            y|yes|1) return 0 ;;
            n|no|2) return 1 ;;
            *) printf '请输入 y 或 n，也可直接回车使用默认值。\n' >&2 ;;
        esac
    done
}

ask_port() {
    local prompt="$1" default="$2" answer
    while true; do
        answer=$(ask "$prompt" "$default") || exit 130
        if [[ "$answer" =~ ^[0-9]{1,5}$ ]] && (( 10#$answer >= 1 && 10#$answer <= 65535 )); then
            printf '%s' "$((10#$answer))"
            return 0
        fi
        printf '端口请输入 1–65535 之间的数字。\n' >&2
    done
}

ask_domain() {
    local answer
    while true; do
        answer=$(ask "域名（需已解析到本机，可粘贴网址）" "$DOMAIN") || exit 130
        answer="${answer#https://}"
        answer="${answer#http://}"
        answer="${answer%%/*}"
        if [[ ${#answer} -le 253 && "$answer" == *.* && "$answer" =~ ^[a-zA-Z0-9]([a-zA-Z0-9.-]*[a-zA-Z0-9])?$ && "$answer" != *..* && "$answer" != *.-* && "$answer" != *-.* ]]; then
            printf '%s' "${answer,,}"
            return 0
        fi
        printf '请输入有效域名，例如 ecs.example.com。\n' >&2
    done
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

collect_config() {
    local mode
    echo ""
    echo -e "${CYAN}${BOLD}第 1 步：选择安装方式${RESET}"
    echo "  1) Docker Compose（推荐，回车默认）"
    echo "  2) 原生 systemd/OpenRC"
    mode=$(ask_choice "请选择安装方式" "1" 1 2)
    if [[ "$mode" == "2" ]]; then INSTALL_MODE="native"; else INSTALL_MODE="docker"; fi

    if [[ "$INSTALL_MODE" == "docker" ]]; then
        while true; do
            CONTAINER_NAME=$(ask "容器名称" "$CONTAINER_NAME")
            if [[ "$CONTAINER_NAME" =~ ^[a-z0-9][a-z0-9_.-]*$ ]]; then break; fi
            echo "容器名称只能包含小写字母、数字、点、下划线和短横线。" >&2
        done
    fi

    echo ""
    echo -e "${CYAN}${BOLD}第 2 步：设置访问方式${RESET}"
    if yes_no "是否使用域名" "$USE_DOMAIN"; then
        USE_DOMAIN="y"
        DOMAIN=$(ask_domain)
        if [[ "$INSTALL_MODE" == "native" ]]; then
            echo "原生安装不配置反向代理，请将域名转发到本机 127.0.0.1 的应用端口。"
            if yes_no "已有 HTTPS 反向代理" "$ENABLE_HTTPS"; then ENABLE_HTTPS="y"; else ENABLE_HTTPS="n"; fi
        else
            if yes_no "是否自动申请 HTTPS 证书" "$ENABLE_HTTPS"; then ENABLE_HTTPS="y"; else ENABLE_HTTPS="n"; fi
        fi
        HTTP_PORT=$(ask_port "HTTP 对外端口" "$HTTP_PORT")
        if [[ "$ENABLE_HTTPS" == "y" ]]; then
            while true; do
                HTTPS_PORT=$(ask_port "HTTPS 对外端口" "$HTTPS_PORT")
                if [[ "$HTTPS_PORT" != "$HTTP_PORT" ]]; then break; fi
                echo "HTTP 和 HTTPS 端口不能相同，请重新输入。" >&2
            done
        fi
    else
        USE_DOMAIN="n"
        DOMAIN=""
        ENABLE_HTTPS="n"
    fi

    if [[ "$INSTALL_MODE" == "native" ]]; then
        APP_PORT=$(ask_port "应用监听端口（仅本机，供反向代理使用）" "$APP_PORT")
    elif [[ "$USE_DOMAIN" == "n" ]]; then
        APP_PORT=$(ask_port "应用访问端口" "$APP_PORT")
    fi
}

show_config_summary() {
    echo ""
    echo -e "${CYAN}${BOLD}安装配置确认${RESET}"
    if [[ "$INSTALL_MODE" == "docker" ]]; then
        printf '安装方式：Docker Compose\n容器名称：%s\n' "$CONTAINER_NAME"
    else
        printf '安装方式：原生 systemd/OpenRC\n本机监听：127.0.0.1:%s\n' "$APP_PORT"
    fi
    if [[ "$USE_DOMAIN" == "y" ]]; then
        if [[ "$ENABLE_HTTPS" == "y" ]]; then
            printf '访问地址：https://%s:%s\n' "$DOMAIN" "$HTTPS_PORT"
        else
            printf '访问地址：http://%s:%s\n' "$DOMAIN" "$HTTP_PORT"
        fi
    elif [[ "$INSTALL_MODE" == "docker" ]]; then
        printf '访问地址：http://服务器IP:%s\n' "$APP_PORT"
    else
        printf '访问地址：http://127.0.0.1:%s（请配置反向代理后访问）\n' "$APP_PORT"
    fi
    printf '部署目录：%s\n' "$DEPLOY_DIR"
    echo "应用账号、密码和阿里云凭据将在首次打开网页时设置。"
}

prepare_installation() {
    local choice
    if [[ -f "$STATE_FILE" ]]; then
        echo "检测到已保存的安装配置，直接确认即可沿用。"
    else
        collect_config
    fi
    while true; do
        show_config_summary
        echo "  1) 确认并开始安装（回车默认）"
        echo "  2) 修改配置"
        echo "  0) 取消并退出"
        choice=$(ask_choice "请选择" "1" 1 2 0)
        case "$choice" in
            1)
                save_state
                if [[ "$INSTALL_MODE" == "docker" ]]; then
                    install_docker
                else
                    ECS_RECONFIGURE=1 install_native
                fi
                return
                ;;
            2) collect_config ;;
            0) return ;;
        esac
    done
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
    local arch asset version
    case "$(uname -m)" in
        x86_64|amd64) arch="amd64" ;;
        aarch64|arm64) arch="arm64" ;;
        *) die "不支持的处理器架构：$(uname -m)" ;;
    esac

    version=$(curl -fsSL --retry 3 "https://api.github.com/repos/$RELEASE_REPO/releases/latest" |
        sed -n 's/^[[:space:]]*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n 1)
    [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "无法获取有效的最新版本。"
    asset="ecs-controller-linux-$arch.tar.gz"
    DOWNLOAD_WORK_DIR=$(mktemp -d)

    echo "正在下载 $RELEASE_REPO $version（Linux/$arch）..."
    curl -fsSL --retry 3 -o "$DOWNLOAD_WORK_DIR/$asset" \
        "https://github.com/$RELEASE_REPO/releases/download/$version/$asset"
    rm -rf "$DEPLOY_DIR/app"
    mkdir -p "$DEPLOY_DIR/app"
    tar -xzf "$DOWNLOAD_WORK_DIR/$asset" -C "$DEPLOY_DIR/app"
    [[ -x "$DEPLOY_DIR/app/ecs-controller" ]] || die "发布包缺少 ecs-controller。"
    echo "$version" > "$DEPLOY_DIR/.version"
    rm -rf "$DOWNLOAD_WORK_DIR"
    DOWNLOAD_WORK_DIR=""
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
      ECS_UPDATE_DIR: /data/update
      ECS_HTTP_ADDR: 0.0.0.0:43211
      ECS_COOKIE_SECURE: "$([[ "$ENABLE_HTTPS" == "y" ]] && echo 1 || echo 0)"
    volumes:
      - ./data:/data
EOF

    if [[ "$USE_DOMAIN" == "y" ]]; then
        cat >> "$COMPOSE_FILE" <<'EOF'
    expose:
      - "43211"
EOF
    else
        cat >> "$COMPOSE_FILE" <<EOF
    ports:
      - "${APP_PORT}:43211"
EOF
    fi

    cat >> "$COMPOSE_FILE" <<EOF
  ecs-controller-updater:
    build:
      context: .
      dockerfile: Dockerfile.updater
    image: ${CONTAINER_NAME}:updater
    container_name: ${CONTAINER_NAME}-updater
    restart: unless-stopped
    environment:
      ECS_UPDATE_REPO: $RELEASE_REPO
      ECS_UPDATE_DIR: /data/update
      ECS_DEPLOY_DIR: $DEPLOY_DIR
      ECS_HEALTH_URL: http://ecs-controller:43211/healthz
    volumes:
      - ./data:/data
      - ${DEPLOY_DIR}:${DEPLOY_DIR}
      - /var/run/docker.sock:/var/run/docker.sock
    depends_on:
      - ecs-controller
EOF

    if [[ "$USE_DOMAIN" == "y" ]]; then
        cat >> "$COMPOSE_FILE" <<EOF
  caddy:
    image: caddy:2-alpine
    container_name: ${CONTAINER_NAME}-proxy
    restart: unless-stopped
    ports:
      - "${HTTP_PORT}:80"
EOF
        if [[ "$ENABLE_HTTPS" == "y" ]]; then
            printf '      - "%s:443"\n' "$HTTPS_PORT" >> "$COMPOSE_FILE"
        fi
        cat >> "$COMPOSE_FILE" <<EOF
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
    curl -fsSL "https://raw.githubusercontent.com/$SETUP_REPO/main/install.sh" |
        ECS_NONINTERACTIVE=1 ECS_COOKIE_SECURE="$cookie_secure" \
        ECS_HTTP_ADDR="127.0.0.1:$APP_PORT" ECS_UPDATE_REPO="$RELEASE_REPO" \
        ECS_RECONFIGURE="${ECS_RECONFIGURE:-0}" sh
    write_deploy_info
    echo -e "${GREEN}原生部署完成。HTTPS 证书和反向代理请使用已有的 Nginx/Caddy 配置。${RESET}"
}

update_existing() {
    load_state
    if [[ -f "$COMPOSE_FILE" ]]; then
        INSTALL_MODE="docker"
        echo -e "${CYAN}检测到 Docker 部署，正在按现有配置更新服务...${RESET}"
        install_docker
        return
    fi

    if [[ -e "$DEPLOY_DIR/current" || -f /etc/systemd/system/ecs-controller.service ||
        -f /etc/init.d/ecs-controller ]]; then
        INSTALL_MODE="native"
        echo -e "${CYAN}检测到原生部署，正在更新服务...${RESET}"
        install_native
        return
    fi

    die "未检测到已安装的 ECS Controller，请先运行安装向导。"
}

write_deploy_info() {
    local address public_ip
    public_ip=$(curl -fsS --max-time 5 https://api.ipify.org 2>/dev/null ||
        curl -fsS --max-time 5 https://ifconfig.me 2>/dev/null ||
        hostname -I 2>/dev/null | awk '{print $1}' ||
        printf '%s' '服务器IP')
    if [[ "$USE_DOMAIN" == "y" ]]; then
        if [[ "$ENABLE_HTTPS" == "y" ]]; then
            address="https://$DOMAIN"
            [[ "$HTTPS_PORT" != "443" ]] && address="$address:$HTTPS_PORT"
        else
            address="http://$DOMAIN"
            [[ "$HTTP_PORT" != "80" ]] && address="$address:$HTTP_PORT"
        fi
    elif [[ "$INSTALL_MODE" == "native" ]]; then
        address="http://127.0.0.1:$APP_PORT"
    else
        address="http://$public_ip:$APP_PORT"
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
    echo ""
    echo "============================================================"
    echo "ECS Controller 部署完成，以下内容可直接复制"
    echo "============================================================"
    printf '访问地址: %s\n' "$address"
    printf '部署方式: %s\n' "$INSTALL_MODE"
    if [[ "$INSTALL_MODE" == "docker" ]]; then
        printf '容器名称: %s\n' "$CONTAINER_NAME"
        printf '部署目录: %s\n' "$DEPLOY_DIR"
        printf '查看状态: ecs status\n'
        printf '服务菜单: ecs\n'
    else
        printf '服务管理: ecs\n'
        printf '应用仅监听本机，请通过已配置的反向代理访问。\n'
    fi
    echo "------------------------------------------------------------"
    echo "访问信息已保存到: $DEPLOY_DIR/DEPLOY_INFO.txt"
    echo "首次登录后，请在网页中完成账号、阿里云凭据和其他应用配置。"
    echo "============================================================"
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
    if [[ -x "$UNINSTALL_SCRIPT" ]]; then
        exec "$UNINSTALL_SCRIPT"
    fi
    if [[ -f "$DEPLOY_DIR/uninstall.sh" ]]; then
        exec "$DEPLOY_DIR/uninstall.sh"
    fi
    die "未找到卸载脚本，请重新运行最新安装脚本后再卸载。"
}

main_menu() {
    require_root
    init_terminal
    persist_self
    load_state
    while true; do
        echo ""
        echo -e "${BOLD}ECS Controller 部署向导${RESET}"
        echo "  1) 安装 / 重新部署（回车默认）"
        echo "  2) 查看状态"
        echo "  3) 重启服务"
        echo "  4) 查看日志"
        echo "  5) 卸载服务（保留数据）"
        echo "  0) 退出"
        local choice
        choice=$(ask_choice "请选择" "1" 1 2 3 4 5 0)
        case "$choice" in
            1)
                prepare_installation
                return
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

if [[ "${1:-}" == "--update" ]]; then
    require_root
    persist_self
    update_existing
    exit 0
fi

main_menu "$@"
