package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/11dog/11dog/internal/model"
)

// SetUpdateHooks overrides executable resolution and process restart for testing.
func (c *Client) SetUpdateHooks(execPathFn func() (string, error), restartFn func(exePath string) error) {
	c.updateExecPathFn = execPathFn
	c.updateRestartFn = restartFn
}

func (c *Client) handleAgentUpdate(
	parentCtx context.Context,
	nodeID string,
	upd *model.AgentUpdateCommand,
	upg *processUpgrader,
	reportFn func(model.AgentUpdateState),
) {
	if upd == nil {
		return
	}
	targetVer := strings.TrimSpace(upd.Version)

	report := func(state, message string) {
		if reportFn == nil {
			return
		}
		reportFn(model.AgentUpdateState{
			TargetVersion: targetVer,
			State:         state,
			Message:       message,
			UpdatedAtMs:   time.Now().UnixMilli(),
		})
	}

	if !c.updating.CompareAndSwap(false, true) {
		report("failed", "节点正在执行更新中，请勿重复下发")
		return
	}
	defer c.updating.Store(false)

	if !upd.Force && targetVer != "" && targetVer == Version {
		report("up_to_date", fmt.Sprintf("当前已是目标版本 %s", Version))
		return
	}

	dlURL, err := validateUpdateURL(upd.DownloadURL)
	if err != nil {
		c.logger.Error("invalid agent update url", "node_id", nodeID, "url", upd.DownloadURL, "error", err)
		report("failed", "下载地址无效: "+err.Error())
		return
	}

	exePath, err := c.resolveExecutablePath()
	if err != nil {
		c.logger.Error("resolve current executable failed", "error", err)
		report("failed", "无法定位当前可执行文件: "+err.Error())
		return
	}

	c.logger.Info("starting agent self-update",
		"node_id", nodeID,
		"current_version", Version,
		"target_version", targetVer,
		"download_url", dlURL,
		"exe_path", exePath,
	)
	report("downloading", "正在下载新版本二进制...")

	dlCtx, dlCancel := context.WithTimeout(parentCtx, 5*time.Minute)
	defer dlCancel()

	tmpPath, gotSHA, err := c.downloadUpdateBinary(dlCtx, dlURL, filepath.Dir(exePath))
	if err != nil {
		c.logger.Error("download agent update failed", "url", dlURL, "error", err)
		report("failed", "下载失败: "+err.Error())
		return
	}
	cleanupTmp := true
	defer func() {
		if cleanupTmp && tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()

	report("verifying", "正在校验二进制完整性...")
	wantSHA := strings.ToLower(strings.TrimSpace(upd.SHA256))
	if wantSHA != "" && wantSHA != gotSHA {
		errMsg := fmt.Sprintf("SHA256 校验失败 (期望 %s, 实际 %s)", wantSHA, gotSHA)
		c.logger.Error("agent update checksum mismatch", "expected", wantSHA, "actual", gotSHA)
		report("failed", errMsg)
		return
	}

	report("applying", "正在等待活跃探测完成并替换二进制...")
	c.waitForActiveTasksDrain(10 * time.Second)

	if err := replaceBinaryFile(tmpPath, exePath); err != nil {
		c.logger.Error("replace agent binary failed", "exe_path", exePath, "error", err)
		report("failed", "替换二进制失败: "+err.Error())
		return
	}
	cleanupTmp = false

	report("restarting", "更新已应用，正在通过 tableflip 平滑切换进程...")
	c.logger.Info("agent binary replaced successfully; triggering tableflip upgrade", "exe_path", exePath, "target_version", targetVer)

	var upgradeErr error
	if c.updateRestartFn != nil {
		upgradeErr = c.updateRestartFn(exePath)
	} else {
		upgradeErr = upg.Upgrade()
	}
	if upgradeErr != nil {
		c.logger.Error("tableflip upgrade failed", "error", upgradeErr)
		report("failed", "平滑重启失败: "+upgradeErr.Error())
		return
	}
}

func validateUpdateURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("empty download_url")
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		return "", fmt.Errorf("download_url must use http:// or https://: %q", raw)
	}
	if _, err := url.ParseRequestURI(raw); err != nil {
		return "", err
	}
	return raw, nil
}

func (c *Client) resolveExecutablePath() (string, error) {
	if c.updateExecPathFn != nil {
		return c.updateExecPathFn()
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil && resolved != "" {
		return resolved, nil
	}
	return exe, nil
}

func (c *Client) downloadUpdateBinary(ctx context.Context, dlURL, targetDir string) (string, string, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if strings.HasPrefix(dlURL, "https://") {
		tlsCfg, err := c.tlsConfig()
		if err != nil {
			return "", "", err
		}
		if tlsCfg != nil {
			transport.TLSClientConfig = tlsCfg
		}
	}
	httpClient := &http.Client{Transport: transport}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", fmt.Sprintf("11dog-agent/%s (%s/%s)", Version, runtime.GOOS, runtime.GOARCH))

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	tmpFile, err := os.CreateTemp(targetDir, ".11dog-agent-update-*")
	if err != nil {
		return "", "", fmt.Errorf("create temp file in %s: %w", targetDir, err)
	}
	tmpPath := tmpFile.Name()

	hasher := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(tmpFile, hasher), resp.Body)
	closeErr := tmpFile.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return "", "", copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return "", "", closeErr
	}
	if n == 0 {
		_ = os.Remove(tmpPath)
		return "", "", errors.New("downloaded binary is empty")
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil && runtime.GOOS != "windows" {
		_ = os.Remove(tmpPath)
		return "", "", err
	}

	return tmpPath, hex.EncodeToString(hasher.Sum(nil)), nil
}

func (c *Client) waitForActiveTasksDrain(maxWait time.Duration) {
	deadline := time.Now().Add(maxWait)
	for c.activeTasks.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(150 * time.Millisecond)
	}
}

func replaceBinaryFile(tmpPath, exePath string) error {
	if runtime.GOOS == "windows" {
		backupPath := exePath + ".old"
		_ = os.Remove(backupPath)
		if _, err := os.Stat(exePath); err == nil {
			if err := os.Rename(exePath, backupPath); err != nil {
				return fmt.Errorf("backup existing binary: %w", err)
			}
		}
		if err := os.Rename(tmpPath, exePath); err != nil {
			_ = os.Rename(backupPath, exePath)
			return fmt.Errorf("install new binary: %w", err)
		}
		return nil
	}
	if err := os.Rename(tmpPath, exePath); err != nil {
		return fmt.Errorf("atomic rename to %s: %w", exePath, err)
	}
	return nil
}
