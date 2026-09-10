#!/bin/sh
set -eu

install_root=${ECS_INSTALL_ROOT:-/opt/ecs-controller}
config_dir=${ECS_CONFIG_DIR:-/etc/ecs-controller}
env_file=$config_dir/ecs-controller.env
data_dir=${ECS_DATA_DIR:-}
service_user=ecs-controller
purge=${ECS_PURGE:-0}
global_commands="/usr/local/bin/ecs /usr/local/bin/ecs-uninstall /usr/local/bin/aliyun"

# 优先从已有的环境配置文件中读取实际数据目录
if [ -z "$data_dir" ] && [ -f "$env_file" ]; then
    configured_data_dir=$(sed -n 's/^ECS_DATA_DIR=//p' "$env_file" | head -n 1)
    if [ -n "$configured_data_dir" ]; then
        data_dir=$configured_data_dir
    fi
fi
data_dir=${data_dir:-/var/lib/ecs-controller}
docker_data_dir=
if [ -d "$install_root/data" ] && [ -f "$install_root/docker-compose.yml" ]; then
    docker_data_dir="$install_root/data"
    data_dir="$docker_data_dir"
fi

# 解析命令行参数
for arg in "$@"; do
    case "$arg" in
        --purge|-p)
            purge=1
            ;;
        --help|-h)
            echo "用法: $0 [--purge]"
            echo "  --purge, -p    彻底卸载，同时删除数据目录 ($data_dir) 与配置文件 ($config_dir)"
            exit 0
            ;;
    esac
done

if [ "$(id -u)" -ne 0 ]; then
    echo "请使用 root 权限运行，例如：sudo $0" >&2
    exit 1
fi

case $(uname -s) in
    Linux) ;;
    *) echo "当前卸载脚本仅支持 Linux。" >&2; exit 1 ;;
esac

# 检查服务管理器
service_manager=""
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    service_manager=systemd
elif command -v rc-service >/dev/null 2>&1 && command -v rc-update >/dev/null 2>&1; then
    service_manager=openrc
fi

# 交互式终端下，如果未指定 purge 则询问确认
if [ "$purge" -eq 0 ] && [ -t 0 ] && [ -c /dev/tty ]; then
    printf "是否同时删除数据目录（包含数据库和凭据密钥）与配置文件？[y/N]: "
    if read -r response </dev/tty; then
        case "$response" in
            [yY]|[yY][eE][sS]) purge=1 ;;
            *) purge=0 ;;
        esac
    fi
fi

echo "正在停止并清理 ECS Controller 服务..."

# Docker Compose 服务不受 systemd/OpenRC 管理，必须单独停止并删除容器。
stop_docker_services() {
    command -v docker >/dev/null 2>&1 || return 0

    if [ -f "$install_root/docker-compose.yml" ]; then
        (cd "$install_root" && docker compose down --remove-orphans) >/dev/null 2>&1 || true
        return 0
    fi

    # 即使安装目录已被删除，也按 Compose 工作目录标签清理残留容器。
    container_ids=$(docker ps -aq \
        --filter "label=com.docker.compose.project.working_dir=$install_root" 2>/dev/null || true)
    if [ -n "$container_ids" ]; then
        docker rm -f $container_ids >/dev/null 2>&1 || true
    fi
}

stop_docker_services

# 停止并移除服务
if [ "$service_manager" = "systemd" ]; then
    for s in ecs-controller ecs-controller-updater; do
        if systemctl is-active --quiet "$s.service" 2>/dev/null; then
            systemctl stop "$s.service" || true
        fi
        if systemctl is-enabled --quiet "$s.service" 2>/dev/null; then
            systemctl disable "$s.service" >/dev/null 2>&1 || true
        fi
        rm -f "/etc/systemd/system/$s.service"
    done
    systemctl daemon-reload
    systemctl reset-failed >/dev/null 2>&1 || true
elif [ "$service_manager" = "openrc" ]; then
    for s in ecs-controller ecs-controller-updater; do
        rc-service "$s" stop >/dev/null 2>&1 || true
        rc-update del "$s" default >/dev/null 2>&1 || true
        rm -f "/etc/init.d/$s"
        rm -f "/run/$s.pid"
    done
fi

# 确保无残留进程运行，避免清理用户时被占用
if id "$service_user" >/dev/null 2>&1; then
    pkill -u "$service_user" 2>/dev/null || true
fi

# 移除日志与运行时文件
rm -f /var/log/ecs-controller.log /var/log/ecs-controller-updater.log

# 移除程序安装目录；普通卸载保留 Docker 的 data 目录。
if [ -d "$install_root" ] || [ -L "$install_root" ]; then
    echo "正在删除程序目录：$install_root ..."
    if [ "$purge" -eq 1 ] || [ -z "$docker_data_dir" ]; then
        rm -rf "$install_root"
    else
        find "$install_root" -mindepth 1 -maxdepth 1 ! -name data -exec rm -rf {} +
        rmdir "$install_root" 2>/dev/null || true
    fi
fi

# 清理配置与数据目录
if [ "$purge" -eq 1 ]; then
    echo "正在删除配置文件与数据目录..."
    rm -rf "$config_dir"
    rm -rf "$data_dir"
else
    echo "已保留数据与配置（如需彻底删除，请手动执行或带 --purge 参数运行）："
    [ -d "$config_dir" ] && echo "  - 配置文件：$config_dir"
    [ -d "$data_dir" ] && echo "  - 数据目录：$data_dir"
fi

# 移除系统用户与用户组
if id "$service_user" >/dev/null 2>&1; then
    echo "正在清理服务账号：$service_user ..."
    if command -v userdel >/dev/null 2>&1; then
        userdel "$service_user" 2>/dev/null || true
    elif command -v deluser >/dev/null 2>&1; then
        deluser "$service_user" 2>/dev/null || true
    fi
fi

if command -v groupdel >/dev/null 2>&1; then
    groupdel "$service_user" 2>/dev/null || true
fi

# 最后移除全局命令。当前脚本即使删除自身也可以继续执行到结束。
for command_path in $global_commands; do
    rm -f "$command_path"
done

echo "ECS 控制台卸载完成。"
