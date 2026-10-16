#!/usr/bin/env bash
set -euo pipefail

bundle_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
service_user="lee"
service_home="$(getent passwd "$service_user" | cut -d: -f6)"
if [[ -z "$service_home" ]]; then
  printf '服务用户不存在: %s\n' "$service_user" >&2
  exit 1
fi
app_dir="$service_home/lan-file-server"
config_dir="$service_home/.config/lan-file-server"
storage_dir="$app_dir/storage"

if [[ "$EUID" -ne 0 ]]; then
  exec sudo "$0" "$@"
fi

install -d -o "$service_user" -g "$service_user" -m 0750 "$app_dir/bin" "$config_dir"
if [[ ! -d "$storage_dir" ]]; then
  install -d -o "$service_user" -g "$service_user" -m 0750 "$storage_dir"
fi
install -o "$service_user" -g "$service_user" -m 0755 "$bundle_dir/bin/lan-file-server" "$app_dir/bin/lan-file-server"
install -o "$service_user" -g "$service_user" -m 0755 "$bundle_dir/scripts/service.sh" "$app_dir/service.sh"
install -o root -g root -m 0644 "$bundle_dir/deploy/systemd/lan-file-server.service" /etc/systemd/system/lan-file-server.service

if [[ ! -e "$config_dir/server.env" ]]; then
  password="$(openssl rand -hex 32)"
  umask 077
  printf 'HOST=0.0.0.0\nPORT=3000\nSTORAGE_ROOT=%s\nMAX_UPLOAD_BYTES=10737418240\nAUTH_USER=%s\nAUTH_PASSWORD=%s\n' \
    "$storage_dir" "$service_user" "$password" > "$config_dir/server.env"
  chown "$service_user:$service_user" "$config_dir/server.env"
  chmod 0600 "$config_dir/server.env"
fi

systemctl daemon-reload
systemctl enable --now lan-file-server.service
printf '服务已安装并设为开机自启。认证配置仅保存在 %s/server.env（权限 0600）。\n' "$config_dir"
printf '存储目录保持独立：%s\n' "$storage_dir"
