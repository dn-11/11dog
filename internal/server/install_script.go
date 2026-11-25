package server

import (
	"bytes"
	"crypto/sha256"
	"embed"
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
	"text/template"

	"github.com/dn-11/11dog/internal/config"
	"github.com/dn-11/11dog/internal/store"
)

//go:embed templates/*.tmpl
var scriptTemplatesFS embed.FS

var scriptTemplates = template.Must(template.ParseFS(scriptTemplatesFS, "templates/*.tmpl"))

var (
	tokenParamRe     = regexp.MustCompile(`^[a-zA-Z0-9_-]{8,128}$`)
	natsParamRe      = regexp.MustCompile(`^wss?://[a-zA-Z0-9._:\[\]-]+(?:/[a-zA-Z0-9._~/-]*)?$`)
	binaryFilenameRe = regexp.MustCompile(`^11dog-agent-[a-z0-9-]+(\.exe)?$`)
)

type installScriptData struct {
	DownloadBase   string
	NATSAddr       string
	MaxConcurrency uint32
	Creds          string
}

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

// ServeInstallScript renders a systemd install or uninstall bash script using Go text/template.
func ServeInstallScript(cfg *config.ServerConfig, nodeStore store.NodeStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		uninstall := q.Get("uninstall") == "1" || strings.EqualFold(q.Get("uninstall"), "true")
		if uninstall {
			var buf bytes.Buffer
			if err := scriptTemplates.ExecuteTemplate(&buf, "uninstall.sh.tmpl", nil); err != nil {
				http.Error(w, "failed to render uninstall script", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write(buf.Bytes())
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

		var buf bytes.Buffer
		if err := scriptTemplates.ExecuteTemplate(&buf, "install.sh.tmpl", installScriptData{
			DownloadBase:   downloadBase,
			NATSAddr:       natsAddr,
			MaxConcurrency: rec.MaxConcurrency,
			Creds:          strings.TrimSpace(rec.Creds),
		}); err != nil {
			http.Error(w, "failed to render install script", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(buf.Bytes())
	}
}
