package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/11dog/11dog/internal/config"
	"github.com/11dog/11dog/internal/store"
)

var (
	tokenParamRe     = regexp.MustCompile(`^[a-zA-Z0-9_-]{8,128}$`)
	natsParamRe      = regexp.MustCompile(`^wss?://[a-zA-Z0-9._:\[\]-]+(?:/[a-zA-Z0-9._~/-]*)?$`)
	binaryFilenameRe = regexp.MustCompile(`^11dog-agent-[a-z0-9-]+(\.exe)?$`)
)

// AgentBinaryFilename returns the canonical hosted binary filename for a given GOOS and GOARCH.
func AgentBinaryFilename(goos, goarch string) string {
	goos = strings.ToLower(strings.TrimSpace(goos))
	goarch = strings.ToLower(strings.TrimSpace(goarch))
	if goos == "" {
		goos = "linux"
	}
	if goarch == "" {
		goarch = "amd64"
	}
	name := fmt.Sprintf("11dog-agent-%s-%s", goos, goarch)
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

// ComputeHostedBinarySHA256 computes the SHA-256 hex digest of a hosted agent binary in binDir.
func ComputeHostedBinarySHA256(binDir, filename string) (string, error) {
	if binDir == "" || !binaryFilenameRe.MatchString(filename) {
		return "", os.ErrNotExist
	}
	fullPath := filepath.Join(binDir, filename)
	f, err := os.Open(fullPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

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

// ServeInstallScript dynamically generates a self-hosted systemd install (or uninstall) bash script
// embedding the node's signed NATS .creds file.
func ServeInstallScript(cfg *config.ServerConfig, nodeStore store.NodeStore) http.HandlerFunc {
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

		rec, err := nodeStore.GetNodeByToken(r.Context(), token)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "node token not found", http.StatusNotFound)
				return
			}
			http.Error(w, "failed to look up node", http.StatusInternalServerError)
			return
		}
		if strings.TrimSpace(rec.Creds) == "" {
			http.Error(w, "node NATS credentials not provisioned", http.StatusInternalServerError)
			return
		}

		natsAddr := strings.TrimSpace(q.Get("nats"))
		if natsAddr == "" {
			natsAddr = strings.TrimSpace(cfg.NATS.PublicURL)
		}
		if natsAddr == "" {
			wsScheme := "ws"
			if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
				wsScheme = "wss"
			}
			natsAddr = fmt.Sprintf("%s://%s", wsScheme, r.Host)
		}
		if !natsParamRe.MatchString(natsAddr) {
			http.Error(w, "invalid nats parameter (expected ws:// or wss://)", http.StatusBadRequest)
			return
		}

		downloadBase := strings.TrimRight(cfg.AgentDownloadBaseURL, "/")
		if downloadBase == "" {
			scheme := "http"
			if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
				scheme = "https"
			}
			downloadBase = fmt.Sprintf("%s://%s/downloads", scheme, r.Host)
		}

		script := fmt.Sprintf(installScriptTemplate, downloadBase, natsAddr, rec.MaxConcurrency, strings.TrimSpace(rec.Creds))
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
rm -rf /opt/11dog
rm -rf /etc/11dog
systemctl daemon-reload
echo "11dog-agent 已卸载完成。"
`

const installScriptTemplate = `#!/usr/bin/env bash
set -euo pipefail

DOWNLOAD_BASE=%q
NATS_ADDR=%q
MAX_CONCURRENCY=%d

INSTALL_DIR="/opt/11dog"
INSTALL_BIN="${INSTALL_DIR}/11dog-agent"
SYMLINK_BIN="/usr/local/bin/11dog-agent"
CONFIG_DIR="/etc/11dog"
CONFIG_FILE="${CONFIG_DIR}/agent.yaml"
CREDS_FILE="${CONFIG_DIR}/agent.creds"
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
cat > "${CREDS_FILE}" <<'CREDS_EOF'
%s
CREDS_EOF
chown root:11dog "${CREDS_FILE}"
chmod 0640 "${CREDS_FILE}"

cat > "${CONFIG_FILE}" <<EOF
nats_addr: "${NATS_ADDR}"
creds_file: "${CREDS_FILE}"
pid_file: "/opt/11dog/11dog-agent.pid"
max_concurrency: ${MAX_CONCURRENCY}
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
PIDFile=/opt/11dog/11dog-agent.pid
ExecStart=/opt/11dog/11dog-agent --config /etc/11dog/agent.yaml
ExecReload=/bin/kill -HUP $MAINPID
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
ReadOnlyPaths=/etc/11dog
ReadWritePaths=/opt/11dog
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable 11dog-agent.service
systemctl restart 11dog-agent.service

echo "11dog-agent 部署并启动完成！查看日志: journalctl -u 11dog-agent -f"
`
