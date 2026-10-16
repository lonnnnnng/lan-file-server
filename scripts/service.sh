#!/usr/bin/env bash
set -euo pipefail

unit="lan-file-server.service"
action="${1:-status}"
case "$action" in
  start|stop|restart|enable|disable)
    sudo systemctl "$action" "$unit"
    ;;
  status)
    systemctl status --no-pager "$unit"
    ;;
  logs)
    journalctl -u "$unit" -f
    ;;
  *)
    printf '用法: %s {start|stop|restart|enable|disable|status|logs}\n' "$0" >&2
    exit 2
    ;;
esac
