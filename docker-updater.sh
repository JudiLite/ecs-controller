#!/bin/sh
set -u

repo=${ECS_UPDATE_REPO:-JudiLite/ecs-controller}
update_dir=${ECS_UPDATE_DIR:-/data/update}
deploy_dir=${ECS_DEPLOY_DIR:-/deploy}
health_url=${ECS_HEALTH_URL:-http://ecs-controller:43211/healthz}
request_file=$update_dir/request.json
processing_file=$update_dir/request.processing.json
status_file=$update_dir/status.json
lock_dir=$update_dir/.docker-lock
public_key=/etc/ecs-controller/release-public-key.pem

mkdir -p "$update_dir"
umask 077

json_escape() {
    printf '%s' "$1" | awk 'BEGIN { ORS="" } { gsub(/\\/, "\\\\"); gsub(/"/, "\\\""); gsub(/\r/, ""); printf "%s", $0 }'
}

read_field() {
    field=$1
    file=$2
    sed -n "s/.*\"$field\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" "$file" | head -n 1
}

write_status() {
    status=$1
    phase=$2
    message=$3
    progress=${4:-0}
    target=${5:-}
    current=${6:-}
    version=${7:-}
    request_id=${8:-}
    now=$(date -u +%s)
    temporary=$status_file.tmp
    printf '{"status":"%s","phase":"%s","message":"%s","progress":%s,"target_commit":"%s","current_commit":"%s","target_version":"%s","request_id":"%s","updated_at":%s}\n' \
        "$(json_escape "$status")" "$(json_escape "$phase")" "$(json_escape "$message")" "$progress" \
        "$(json_escape "$target")" "$(json_escape "$current")" "$(json_escape "$version")" \
        "$(json_escape "$request_id")" "$now" > "$temporary"
    chmod 0644 "$temporary"
    mv -f "$temporary" "$status_file"
}

cleanup() {
    if [ -n "${work_dir:-}" ] && [ -d "$work_dir" ]; then
        rm -rf "$work_dir"
    fi
    rmdir "$lock_dir" 2>/dev/null || true
}

fail_unexpected() {
    write_status error failed "Docker 更新器发生异常，请查看 ecs-controller-updater 日志" 0 \
        "${target:-}" "${current:-}" "${version:-}" "${request_id:-}"
    cleanup
}
trap fail_unexpected HUP INT TERM

health_check() {
    attempt=0
    while [ "$attempt" -lt 30 ]; do
        attempt=$((attempt + 1))
        if curl -fsS --max-time 3 "$health_url" >/dev/null 2>&1; then
            return 0
        fi
        sleep 2
    done
    return 1
}

run_update() {
    target=$(read_field target_sha "$processing_file")
    version=$(read_field target_version "$processing_file")
    request_id=$(read_field request_id "$processing_file")
    current=""

    case "$target" in
        [0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F][0-9a-fA-F]) ;;
        *) write_status error failed "更新请求中的提交版本无效" 0 "$target" "$current" "$version" "$request_id"; return ;;
    esac
    if ! printf '%s' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
        write_status error failed "更新请求中的发布版本无效" 0 "$target" "$current" "$version" "$request_id"
        return
    fi

    case "$(uname -m)" in
        x86_64|amd64) arch=amd64 ;;
        aarch64|arm64) arch=arm64 ;;
        *) write_status error failed "当前 Linux 架构不受支持" 0 "$target" "$current" "$version" "$request_id"; return ;;
    esac

    work_dir=$(mktemp -d "$update_dir/.docker-download.XXXXXX") || {
        write_status error failed "无法创建更新临时目录" 0 "$target" "$current" "$version" "$request_id"
        return
    }
    asset=ecs-controller-linux-$arch.tar.gz
    base_url=https://github.com/$repo/releases/download/$version
    archive=$work_dir/$asset
    checksums=$work_dir/checksums.txt
    signature=$work_dir/checksums.txt.sig
    extracted=$work_dir/release

    write_status running downloading "正在下载 GitHub Release" 20 "$target" "$current" "$version" "$request_id"
    if ! curl -fL --retry 3 --connect-timeout 10 --max-time 300 -o "$archive" "$base_url/$asset" ||
       ! curl -fL --retry 3 --connect-timeout 10 --max-time 60 -o "$checksums" "$base_url/checksums.txt" ||
       ! curl -fL --retry 3 --connect-timeout 10 --max-time 60 -o "$signature" "$base_url/checksums.txt.sig"; then
        write_status error failed "GitHub Release 下载失败，请检查网络后重试" 0 "$target" "$current" "$version" "$request_id"
        return
    fi

    if ! openssl pkeyutl -verify -pubin -inkey "$public_key" -rawin -in "$checksums" -sigfile "$signature" >/dev/null 2>&1; then
        write_status error failed "更新清单签名校验失败，已停止更新" 0 "$target" "$current" "$version" "$request_id"
        return
    fi
    expected=$(awk -v name="$asset" '$2 == name || $2 == "*" name { print $1; exit }' "$checksums")
    actual=$(sha256sum "$archive" | awk '{print $1}')
    if [ -z "$expected" ] || [ "$expected" != "$actual" ]; then
        write_status error failed "更新包 SHA256 校验失败，已停止更新" 0 "$target" "$current" "$version" "$request_id"
        return
    fi

    mkdir -p "$extracted"
    if ! tar -xzf "$archive" -C "$extracted" ||
       [ ! -x "$extracted/ecs-controller" ] ||
       [ ! -f "$extracted/template.html" ] ||
       [ ! -d "$extracted/static" ] ||
       [ ! -f "$extracted/updater.sh" ]; then
        write_status error failed "更新包内容不完整，已停止更新" 0 "$target" "$current" "$version" "$request_id"
        return
    fi
    packaged_commit=$(ECS_APP_DIR="$extracted" "$extracted/ecs-controller" --version 2>/dev/null | sed -n 's/^commit=//p' | head -n 1)
    if [ "$packaged_commit" != "$target" ]; then
        write_status error failed "更新包提交版本与目标版本不一致" 0 "$target" "$current" "$version" "$request_id"
        return
    fi

    backup_dir=$deploy_dir/.app-backup-$target
    rm -rf "$backup_dir"
    mv "$deploy_dir/app" "$backup_dir"
    mv "$extracted" "$deploy_dir/app"
    write_status running restarting "新版本已就绪，正在重建 Docker 容器" 72 "$target" "$current" "$version" "$request_id"
    if docker compose -f "$deploy_dir/docker-compose.yml" --project-directory "$deploy_dir" up -d --build ecs-controller &&
       health_check; then
        rm -rf "$backup_dir"
        write_status success completed "Docker 更新完成，当前已运行最新版本" 100 "$target" "$target" "$version" "$request_id"
        return
    fi

    write_status running rollback "新版本启动或健康检查失败，正在回滚" 88 "$target" "$current" "$version" "$request_id"
    rm -rf "$deploy_dir/app"
    mv "$backup_dir" "$deploy_dir/app"
    docker compose -f "$deploy_dir/docker-compose.yml" --project-directory "$deploy_dir" up -d --build ecs-controller >/dev/null 2>&1 || true
    write_status error rolled_back "新版本健康检查失败，已恢复更新前版本" 0 "$target" "$current" "$version" "$request_id"
}

while :; do
    if [ ! -f "$request_file" ] && [ -f "$processing_file" ]; then
        mv -f "$processing_file" "$request_file"
    fi
    if [ -f "$request_file" ] && mkdir "$lock_dir" 2>/dev/null; then
        mv -f "$request_file" "$processing_file"
        run_update
        rm -f "$processing_file"
        cleanup
    fi
    sleep 2
done
