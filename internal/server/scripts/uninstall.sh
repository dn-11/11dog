#!/usr/bin/env bash
set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "请使用 root 权限运行卸载脚本 (sudo)" >&2
  exit 1
fi

systemctl disable --now 11dog-agent 2>/dev/null || true
rm -f /etc/systemd/system/11dog-agent.service
rm -f /usr/local/bin/11dog-agent
rm -rf /opt/11dog
rm -rf /etc/11dog
systemctl daemon-reload
echo "11dog-agent 已卸载完成。"
