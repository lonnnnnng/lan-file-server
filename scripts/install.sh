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
storage_dir="$app_dir/storage"

if [[ "$EUID" -ne 0 ]]; then
  exec sudo "$0" "$@"
fi

install -d -o "$service_user" -g "$service_user" -m 0750 "$app_dir/bin"
if [[ ! -d "$storage_dir" ]]; then
  install -d -o "$service_user" -g "$service_user" -m 0750 "$storage_dir"
fi
install -o "$service_user" -g "$service_user" -m 0755 "$bundle_dir/bin/lan-file-server" "$app_dir/bin/lan-file-server"
install -o "$service_user" -g "$service_user" -m 0755 "$bundle_dir/scripts/service.sh" "$app_dir/service.sh"
install -o root -g root -m 0644 "$bundle_dir/deploy/systemd/lan-file-server.service" /etc/systemd/system/lan-file-server.service

# long: Basic Auth 已移除，清掉该服务早期版本生成的唯一认证文件，不影响其他用户配置。
old_auth_config="$service_home/.config/lan-file-server/server.env"
rm -f -- "$old_auth_config"
rmdir "$service_home/.config/lan-file-server" 2>/dev/null || true

systemctl daemon-reload
systemctl enable --now lan-file-server.service
systemctl restart lan-file-server.service
printf '服务已更新并设为开机自启。\n'
printf '存储目录保持独立：%s\n' "$storage_dir"
