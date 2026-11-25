package server

import (
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

	"github.com/dn-11/11dog/internal/config"
	"github.com/dn-11/11dog/internal/store"
)

//go:embed scripts/*.sh
var staticScriptsFS embed.FS

var (
	tokenParamRe     = regexp.MustCompile(`^[a-zA-Z0-9_-]{8,128}$`)
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

// ServeStaticScript serves an embedded static shell script from scripts/<filename>.
func ServeStaticScript(filename string) http.HandlerFunc {
	data, err := staticScriptsFS.ReadFile("scripts/" + filename)
	if err != nil {
		panic(fmt.Sprintf("embedded static script %q not found: %v", filename, err))
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
		_, _ = w.Write(data)
	}
}

// ServeNodeCreds returns a node's NATS .creds file authenticated by its install token
// via "Authorization: Bearer <token>" or query parameter "?token=<token>".
func ServeNodeCreds(nodeStore store.NodeStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := extractNodeToken(r)
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
		creds := strings.TrimSpace(rec.Creds)
		if creds == "" {
			http.Error(w, "node NATS credentials not provisioned", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, creds+"\n")
	}
}

func extractNodeToken(r *http.Request) string {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) > 7 && strings.EqualFold(auth[:7], "Bearer ") {
		if tok := strings.TrimSpace(auth[7:]); tok != "" {
			return tok
		}
	}
	return strings.TrimSpace(r.URL.Query().Get("token"))
}
