#!/usr/bin/env bash
set -euo pipefail

SERVER_URL=""
DOWNLOAD_BASE=""
CREDS_URL=""
TOKEN=""
NATS_ADDR=""
MAX_CONCURRENCY="8"
PID_FILE="/opt/11dog/11dog-agent.pid"
SOURCE_CREDS_FILE=""
TLS_CA_FILE=""
TLS_INSECURE="0"
UNINSTALL="0"

INSTALL_DIR="/opt/11dog"
INSTALL_BIN="${INSTALL_DIR}/11dog-agent"
SYMLINK_BIN="/usr/local/bin/11dog-agent"
CONFIG_DIR="/etc/11dog"
CONFIG_FILE="${CONFIG_DIR}/agent.yaml"
CREDS_FILE="${CONFIG_DIR}/agent.creds"
SERVICE_FILE="/etc/systemd/system/11dog-agent.service"

usage() {
  cat <<'USAGE_EOF'
用法:
  install.sh --server <URL> --token <TOKEN> --nats <WSS_URL> [选项]
  install.sh --uninstall

必填参数 (安装时):
  -s, --server <URL>            11Dog 控制面地址 (例如 https://11dog.dn11.example)
  -t, --token <TOKEN>           节点专属安装 Token (用于拉取 .creds)
  -n, --nats <URL>              NATS WebSocket 地址 (例如 wss://nats.dn11.example)

可选参数:
  -d, --download-base <URL>     自定义 Agent 二进制下载基址 (默认: <server>/downloads)
      --creds-url <URL>         自定义 .creds 拉取地址 (默认: <server>/scripts/creds)
      --creds-file <PATH>       使用本地已有 .creds 文件 (未传 --token 时)
  -m, --max-concurrency <N>     最大并发探测数 (默认: 8)
  -p, --pid-file <PATH>         PID 文件路径 (默认: /opt/11dog/11dog-agent.pid)
      --tls-ca-file <PATH>      自定义 TLS CA 证书路径
  -k, --insecure                跳过 TLS 证书校验
      --uninstall               卸载 11dog-agent
  -h, --help                    显示此帮助信息
USAGE_EOF
}

require_arg() {
  if [[ $# -lt 2 || -z "$2" ]]; then
    echo "参数 $1 缺少值" >&2
    exit 1
  fi
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    -s|--server)
      require_arg "$@"
      SERVER_URL="$2"
      shift 2
      ;;
    --server=*)
      SERVER_URL="${1#*=}"
      shift
      ;;
    -d|--download-base)
      require_arg "$@"
      DOWNLOAD_BASE="$2"
      shift 2
      ;;
    --download-base=*)
      DOWNLOAD_BASE="${1#*=}"
      shift
      ;;
    --creds-url)
      require_arg "$@"
      CREDS_URL="$2"
      shift 2
      ;;
    --creds-url=*)
      CREDS_URL="${1#*=}"
      shift
      ;;
    -t|--token)
      require_arg "$@"
      TOKEN="$2"
      shift 2
      ;;
    --token=*)
      TOKEN="${1#*=}"
      shift
      ;;
    -n|--nats)
      require_arg "$@"
      NATS_ADDR="$2"
      shift 2
      ;;
    --nats=*)
      NATS_ADDR="${1#*=}"
      shift
      ;;
    -m|--max-concurrency)
      require_arg "$@"
      MAX_CONCURRENCY="$2"
      shift 2
      ;;
    --max-concurrency=*)
      MAX_CONCURRENCY="${1#*=}"
      shift
      ;;
    -p|--pid-file)
      require_arg "$@"
      PID_FILE="$2"
      shift 2
      ;;
    --pid-file=*)
      PID_FILE="${1#*=}"
      shift
      ;;
    --creds-file)
      require_arg "$@"
      SOURCE_CREDS_FILE="$2"
      shift 2
      ;;
    --creds-file=*)
      SOURCE_CREDS_FILE="${1#*=}"
      shift
      ;;
    --tls-ca-file)
      require_arg "$@"
      TLS_CA_FILE="$2"
      shift 2
      ;;
    --tls-ca-file=*)
      TLS_CA_FILE="${1#*=}"
      shift
      ;;
    -k|--insecure)
      TLS_INSECURE="1"
      shift
      ;;
    --uninstall)
      UNINSTALL="1"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "未知参数: $1" >&2
      usage >&2
      exit 1
      ;;
  esac
done

if [[ $EUID -ne 0 ]]; then
  echo "请使用 root 权限运行安装脚本 (例如 curl ... | sudo bash -s -- ...)" >&2
  exit 1
fi

if [[ "${UNINSTALL}" == "1" ]]; then
  systemctl disable --now 11dog-agent 2>/dev/null || true
  rm -f "${SERVICE_FILE}"
  rm -f "${SYMLINK_BIN}"
  rm -rf "${INSTALL_DIR}"
  rm -rf "${CONFIG_DIR}"
  systemctl daemon-reload
  echo "11dog-agent 已卸载完成。"
  exit 0
fi

SERVER_URL="${SERVER_URL%/}"
if [[ -z "${DOWNLOAD_BASE}" && -n "${SERVER_URL}" ]]; then
  DOWNLOAD_BASE="${SERVER_URL}/downloads"
fi
DOWNLOAD_BASE="${DOWNLOAD_BASE%/}"

if [[ -z "${CREDS_URL}" && -n "${SERVER_URL}" ]]; then
  CREDS_URL="${SERVER_URL}/scripts/creds"
fi

if [[ -z "${DOWNLOAD_BASE}" ]]; then
  echo "缺少 --server 或 --download-base 参数" >&2
  usage >&2
  exit 1
fi

if [[ -z "${NATS_ADDR}" ]]; then
  echo "缺少必填参数 --nats (例如 wss://nats.dn11.example)" >&2
  usage >&2
  exit 1
fi
if [[ ! "${NATS_ADDR}" =~ ^wss?://[^[:space:]\"\'\\]+$ ]]; then
  echo "无效的 --nats 参数: ${NATS_ADDR} (仅支持 ws:// 或 wss://)" >&2
  exit 1
fi

if [[ ! "${MAX_CONCURRENCY}" =~ ^[1-9][0-9]*$ ]]; then
  echo "无效的 --max-concurrency 参数: ${MAX_CONCURRENCY} (必须为正整数)" >&2
  exit 1
fi

if [[ -n "${TOKEN}" ]]; then
  if [[ -z "${CREDS_URL}" ]]; then
    echo "使用 --token 时必须提供 --server 或 --creds-url" >&2
    exit 1
  fi
elif [[ -n "${SOURCE_CREDS_FILE}" ]]; then
  if [[ ! -s "${SOURCE_CREDS_FILE}" ]]; then
    echo "找不到或空的 --creds-file: ${SOURCE_CREDS_FILE}" >&2
    exit 1
  fi
elif [[ ! -s "${CREDS_FILE}" ]]; then
  echo "缺少 --token（或本地已有的 ${CREDS_FILE} / --creds-file）" >&2
  usage >&2
  exit 1
fi

UNAME_M="$(uname -m)"
case "${UNAME_M}" in
  x86_64|amd64)
    ARCH="amd64"
    ;;
  aarch64|arm64)
    ARCH="arm64"
    ;;
  *)
    echo "暂不支持的 CPU 架构: ${UNAME_M} (仅支持 amd64 / arm64)" >&2
    exit 1
    ;;
esac

BIN_URL="${DOWNLOAD_BASE}/11dog-agent-linux-${ARCH}"
TMP_BIN="$(mktemp)"
TMP_CREDS="$(mktemp)"
trap 'rm -f "${TMP_BIN}" "${TMP_CREDS}"' EXIT

echo "[1/4] 正在从 ${BIN_URL} 下载 11dog-agent (${ARCH})..."
curl -fsSL "${BIN_URL}" -o "${TMP_BIN}"

echo "[2/4] 创建系统用户、安装目录与配置目录..."
if ! id -u 11dog >/dev/null 2>&1; then
  useradd --system --no-create-home --shell /usr/sbin/nologin 11dog
fi
mkdir -p "${INSTALL_DIR}"
install -m 0755 -o 11dog -g 11dog "${TMP_BIN}" "${INSTALL_BIN}"
chown 11dog:11dog "${INSTALL_DIR}"
chmod 0755 "${INSTALL_DIR}"
ln -sfn "${INSTALL_BIN}" "${SYMLINK_BIN}"

mkdir -p "${CONFIG_DIR}"
chown root:11dog "${CONFIG_DIR}"
chmod 0750 "${CONFIG_DIR}"

echo "[3/4] 写入 NATS JWT 凭据 ${CREDS_FILE} 与配置文件 ${CONFIG_FILE}..."
if [[ -n "${TOKEN}" ]]; then
  curl -fsSL -H "Authorization: Bearer ${TOKEN}" "${CREDS_URL}" -o "${TMP_CREDS}"
  if [[ ! -s "${TMP_CREDS}" ]] || ! grep -q "BEGIN NATS USER JWT" "${TMP_CREDS}"; then
    echo "拉取节点 .creds 凭据失败或内容无效" >&2
    exit 1
  fi
  install -m 0640 -o root -g 11dog "${TMP_CREDS}" "${CREDS_FILE}"
elif [[ -n "${SOURCE_CREDS_FILE}" && "${SOURCE_CREDS_FILE}" != "${CREDS_FILE}" ]]; then
  install -m 0640 -o root -g 11dog "${SOURCE_CREDS_FILE}" "${CREDS_FILE}"
else
  chown root:11dog "${CREDS_FILE}"
  chmod 0640 "${CREDS_FILE}"
fi

cat > "${CONFIG_FILE}" <<EOF
nats_addr: "${NATS_ADDR}"
creds_file: "${CREDS_FILE}"
pid_file: "${PID_FILE}"
max_concurrency: ${MAX_CONCURRENCY}
EOF
if [[ -n "${TLS_CA_FILE}" ]]; then
  cat >> "${CONFIG_FILE}" <<EOF
tls_ca_file: "${TLS_CA_FILE}"
EOF
fi
if [[ "${TLS_INSECURE}" == "1" ]]; then
  cat >> "${CONFIG_FILE}" <<EOF
tls_insecure_skip_verify: true
EOF
fi
chown root:11dog "${CONFIG_FILE}"
chmod 0640 "${CONFIG_FILE}"

echo "[4/4] 安装并启动 systemd 服务 11dog-agent..."
cat > "${SERVICE_FILE}" <<EOF
[Unit]
Description=11Dog DN11 Network Probe Agent
Documentation=https://github.com/dn-11/11dog
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=11dog
Group=11dog
PIDFile=${PID_FILE}
ExecStart=${INSTALL_BIN} --config ${CONFIG_FILE}
ExecReload=/bin/kill -HUP \$MAINPID
KillMode=process
Restart=always
RestartSec=5s
StartLimitIntervalSec=0

AmbientCapabilities=CAP_NET_RAW
CapabilityBoundingSet=CAP_NET_RAW

NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ProtectKernelTunables=true
ProtectControlGroups=true
RestrictSUIDSGID=true
ReadOnlyPaths=${CONFIG_DIR}
ReadWritePaths=${INSTALL_DIR}
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable 11dog-agent.service
systemctl restart 11dog-agent.service

echo "11dog-agent 部署并启动完成！查看状态: systemctl status 11dog-agent"
