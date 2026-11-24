package server

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/11dog/11dog/internal/config"
)

var (
	tokenParamRe    = regexp.MustCompile(`^[a-zA-Z0-9_-]{8,128}$`)
	grpcParamRe     = regexp.MustCompile(`^[a-zA-Z0-9._:\[\]-]+$`)
	binaryFilenameRe = regexp.MustCompile(`^11dog-agent-[a-z0-9-]+(\.exe)?$`)
)

// ListHostedBinaries returns sorted agent binary filenames available in AgentBinDir.
func ListHostedBinaries(binDir string) []string {
	if binDir == "" {
		return nil
	}
	entries, err := os.ReadDir(binDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if binaryFilenameRe.MatchString(name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// ServeAgentBinary serves compiled edge agent binaries from cfg.AgentBinDir under /downloads/{filename}.
func ServeAgentBinary(cfg *config.ServerConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filename := r.PathValue("filename")
		if !binaryFilenameRe.MatchString(filename) {
			writeError(w, http.StatusNotFound, "binary not found")
			return
		}
		fullPath := filepath.Join(cfg.AgentBinDir, filename)
		info, err := os.Stat(fullPath)
		if err != nil || info.IsDir() {
			writeError(w, http.StatusNotFound, "agent binary not hosted on this server")
			return
		}
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
		http.ServeFile(w, r, fullPath)
	}
}

// ServeInstallScript dynamically generates a self-hosted systemd install (or uninstall) bash script.
func ServeInstallScript(cfg *config.ServerConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		uninstall := q.Get("uninstall") == "1" || strings.EqualFold(q.Get("uninstall"), "true")
		if uninstall {
			w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write([]byte(uninstallScript))
			return
		}

		token := strings.TrimSpace(q.Get("token"))
		if !tokenParamRe.MatchString(token) {
			http.Error(w, "invalid or missing token parameter", http.StatusBadRequest)
			return
		}

		grpcAddr := strings.TrimSpace(q.Get("grpc"))
		if grpcAddr == "" {
			grpcAddr = strings.TrimSpace(cfg.PublicGRPCAddr)
		}
		if grpcAddr == "" {
			host := r.Host
			if h, _, err := net.SplitHostPort(r.Host); err == nil && h != "" {
				host = h
			}
			grpcPort := "9090"
			if _, p, err := net.SplitHostPort(cfg.GRPCListenAddr); err == nil && p != "" {
				grpcPort = p
			}
			if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
				host = "[" + host + "]"
			}
			grpcAddr = host + ":" + grpcPort
		}
		if !grpcParamRe.MatchString(grpcAddr) {
			http.Error(w, "invalid grpc parameter", http.StatusBadRequest)
			return
		}

		tlsEnabled := cfg.TLS.Enabled
		if v := strings.TrimSpace(q.Get("tls")); v != "" {
			tlsEnabled = v == "1" || strings.EqualFold(v, "true")
		}

		downloadBase := strings.TrimRight(cfg.AgentDownloadBaseURL, "/")
		if downloadBase == "" {
			scheme := "http"
			if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
				scheme = "https"
			}
			downloadBase = fmt.Sprintf("%s://%s/downloads", scheme, r.Host)
		}

		script := fmt.Sprintf(installScriptTemplate, downloadBase, grpcAddr, token, tlsEnabled)
		w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(script))
	}
}

const uninstallScript = `#!/usr/bin/env bash
set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "请使用 root 权限运行卸载脚本 (sudo)" >&2
  exit 1
fi

systemctl disable --now 11dog-agent 2>/dev/null || true
rm -f /etc/systemd/system/11dog-agent.service
rm -f /usr/local/bin/11dog-agent
rm -rf /etc/11dog
systemctl daemon-reload
echo "11dog-agent 已卸载完成。"
`

const installScriptTemplate = `#!/usr/bin/env bash
set -euo pipefail

DOWNLOAD_BASE=%q
SERVER_ADDR=%q
AGENT_TOKEN=%q
TLS_ENABLED=%t

INSTALL_BIN="/usr/local/bin/11dog-agent"
CONFIG_DIR="/etc/11dog"
CONFIG_FILE="${CONFIG_DIR}/agent.yaml"
SERVICE_FILE="/etc/systemd/system/11dog-agent.service"

if [[ $EUID -ne 0 ]]; then
  echo "请使用 root 权限运行安装脚本 (例如 curl ... | sudo bash)" >&2
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
trap 'rm -f "${TMP_BIN}"' EXIT

echo "[1/4] 正在从 ${BIN_URL} 下载 11dog-agent (${ARCH})..."
curl -fsSL "${BIN_URL}" -o "${TMP_BIN}"
install -m 0755 "${TMP_BIN}" "${INSTALL_BIN}"

echo "[2/4] 创建系统用户与配置目录..."
if ! id -u 11dog >/dev/null 2>&1; then
  useradd --system --no-create-home --shell /usr/sbin/nologin 11dog
fi
mkdir -p "${CONFIG_DIR}"
chown root:11dog "${CONFIG_DIR}"
chmod 0750 "${CONFIG_DIR}"

echo "[3/4] 写入通信凭据配置 ${CONFIG_FILE} (节点身份由服务端数据库统一下发)..."
cat > "${CONFIG_FILE}" <<EOF
server_addr: "${SERVER_ADDR}"
token: "${AGENT_TOKEN}"
tls_enabled: ${TLS_ENABLED}
heartbeat_interval_sec: 10
EOF
chown root:11dog "${CONFIG_FILE}"
chmod 0640 "${CONFIG_FILE}"

echo "[4/4] 安装并启动 systemd 服务 11dog-agent..."
cat > "${SERVICE_FILE}" <<'EOF'
[Unit]
Description=11Dog DN11 Network Probe Agent
Documentation=https://github.com/11dog/11dog
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=11dog
Group=11dog
ExecStart=/usr/local/bin/11dog-agent --config /etc/11dog/agent.yaml
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
ReadOnlyPaths=/etc/11dog
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable 11dog-agent.service
systemctl restart 11dog-agent.service

echo "11dog-agent 部署并启动完成！查看日志: journalctl -u 11dog-agent -f"
`
