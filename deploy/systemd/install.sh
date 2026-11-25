#!/usr/bin/env bash
set -euo pipefail

BIN_SRC="${1:-./11dog-agent-linux-amd64}"
INSTALL_DIR="/opt/11dog"
INSTALL_BIN="${INSTALL_DIR}/11dog-agent"
SYMLINK_BIN="/usr/local/bin/11dog-agent"
CONFIG_DIR="/etc/11dog"
CONFIG_FILE="${CONFIG_DIR}/agent.yaml"
SERVICE_FILE="/etc/systemd/system/11dog-agent.service"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [[ $EUID -ne 0 ]]; then
  echo "请使用 root 权限运行安装脚本 (sudo $0)" >&2
  exit 1
fi

if [[ ! -f "${BIN_SRC}" ]]; then
  echo "找不到 Agent 二进制文件: ${BIN_SRC}" >&2
  echo "用法: sudo $0 /path/to/11dog-agent" >&2
  exit 1
fi

if ! id -u 11dog >/dev/null 2>&1; then
  useradd --system --no-create-home --shell /usr/sbin/nologin 11dog
fi

mkdir -p "${INSTALL_DIR}"
install -m 0755 -o 11dog -g 11dog "${BIN_SRC}" "${INSTALL_BIN}"
chown 11dog:11dog "${INSTALL_DIR}"
chmod 0755 "${INSTALL_DIR}"
ln -sfn "${INSTALL_BIN}" "${SYMLINK_BIN}"
mkdir -p "${CONFIG_DIR}"
chown root:11dog "${CONFIG_DIR}"
chmod 0750 "${CONFIG_DIR}"

if [[ ! -f "${CONFIG_FILE}" ]]; then
  install -m 0640 -o root -g 11dog "${SCRIPT_DIR}/agent.yaml.example" "${CONFIG_FILE}"
  echo "已生成默认配置文件: ${CONFIG_FILE}，请先修改 server_addr / token / node_id 后重启服务"
else
  echo "保留现有配置文件: ${CONFIG_FILE}"
fi

install -m 0644 "${SCRIPT_DIR}/11dog-agent.service" "${SERVICE_FILE}"
systemctl daemon-reload
systemctl enable 11dog-agent.service
systemctl restart 11dog-agent.service

echo "11dog-agent 安装并启动完成！查看状态: systemctl status 11dog-agent"
