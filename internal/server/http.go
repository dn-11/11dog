package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/dn-11/11dog/internal/config"
	"github.com/dn-11/11dog/internal/model"
	"github.com/dn-11/11dog/internal/store"
	"github.com/dn-11/11dog/web"
	"github.com/google/uuid"
)

// Version is set via ldflags at build time.
var Version = "dev"

// NewHTTPHandler constructs the complete HTTP API + SSE + hosted script + embedded SPA router.
func NewHTTPHandler(cfg *config.ServerConfig, hub *Hub, orch *Orchestrator, nodeStore store.NodeStore, limiter *IPRateLimiter, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":       "ready",
			"online_nodes": hub.OnlineCount(),
		})
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = io.WriteString(w, orch.MetricsText())
	})

	mux.HandleFunc("GET /scripts/install.sh", ServeStaticScript("install.sh"))
	mux.HandleFunc("GET /scripts/uninstall.sh", ServeStaticScript("uninstall.sh"))
	mux.HandleFunc("GET /scripts/creds", ServeNodeCreds(nodeStore))
	mux.HandleFunc("GET /downloads/{filename}", ServeAgentBinary(cfg))

	mux.HandleFunc("GET /api/v1/meta", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"site_title":   cfg.SiteTitle,
			"network_name": cfg.NetworkName,
			"online_nodes": hub.OnlineCount(),
		})
	})

	mux.HandleFunc("GET /api/v1/nodes", func(w http.ResponseWriter, _ *http.Request) {
		nodes := hub.ListNodes()
		publicNodes := make([]model.PublicNodeInfo, 0, len(nodes))
		for _, n := range nodes {
			if !n.Enabled || n.Status != "online" {
				continue
			}
			publicNodes = append(publicNodes, model.PublicNodeInfo{
				ID:           n.ID,
				Name:         n.Name,
				Region:       n.Region,
				Group:        n.Group,
				ASN:          n.ASN,
				Capabilities: n.Capabilities,
				Status:       n.Status,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"nodes": publicNodes,
			"total": len(publicNodes),
		})
	})

	mux.HandleFunc("GET /api/v1/admin/overview", func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(cfg, w, r) {
			return
		}
		writeJSON(w, http.StatusOK, orch.AdminOverview())
	})

	mux.HandleFunc("GET /api/v1/admin/nodes", func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(cfg, w, r) {
			return
		}
		nodes := hub.ListNodes()
		writeJSON(w, http.StatusOK, map[string]any{
			"nodes": nodes,
			"total": len(nodes),
		})
	})

	mux.HandleFunc("POST /api/v1/admin/nodes", func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(cfg, w, r) {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var req model.AdminCreateNodeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "请求参数解析失败: "+err.Error())
			return
		}
		if strings.TrimSpace(req.Name) == "" {
			writeError(w, http.StatusBadRequest, "节点名称不能为空")
			return
		}
		nodeID := store.GenerateNodeID()
		creds, err := hub.AuthManager().IssueAgentCreds(nodeID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "签发 NATS .creds 失败: "+err.Error())
			return
		}
		rec, err := nodeStore.CreateNode(r.Context(), &store.NodeRecord{
			ID:             nodeID,
			Name:           strings.TrimSpace(req.Name),
			Region:         strings.TrimSpace(req.Region),
			Group:          strings.TrimSpace(req.Group),
			ASN:            req.ASN,
			MaxConcurrency: req.MaxConcurrency,
			Creds:          creds,
			Enabled:        true,
			Notes:          strings.TrimSpace(req.Notes),
		})
		if err != nil {
			if errors.Is(err, store.ErrConflict) {
				writeError(w, http.StatusConflict, "节点 ID 已存在")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, nodeRecordToInfo(rec))
	})

	mux.HandleFunc("PUT /api/v1/admin/nodes/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(cfg, w, r) {
			return
		}
		nodeID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "无效的节点 UUID")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var req model.AdminUpdateNodeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "请求参数解析失败: "+err.Error())
			return
		}
		rec, err := nodeStore.UpdateNode(r.Context(), nodeID, store.NodePatch{
			Name:           req.Name,
			Region:         req.Region,
			Group:          req.Group,
			ASN:            req.ASN,
			MaxConcurrency: req.MaxConcurrency,
			Enabled:        req.Enabled,
			Notes:          req.Notes,
		})
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "节点不存在")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, nodeRecordToInfo(rec))
	})

	mux.HandleFunc("POST /api/v1/admin/nodes/{id}/rotate-token", func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(cfg, w, r) {
			return
		}
		nodeID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "无效的节点 UUID")
			return
		}
		creds, err := hub.AuthManager().IssueAgentCreds(nodeID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "重新签发 NATS .creds 失败: "+err.Error())
			return
		}
		rec, err := nodeStore.UpdateCreds(r.Context(), nodeID, creds)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "节点不存在")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, nodeRecordToInfo(rec))
	})

	mux.HandleFunc("DELETE /api/v1/admin/nodes/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(cfg, w, r) {
			return
		}
		nodeID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "无效的节点 UUID")
			return
		}
		if err := nodeStore.DeleteNode(r.Context(), nodeID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "节点不存在")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "node_id": nodeID})
	})

	mux.HandleFunc("POST /api/v1/admin/nodes/{id}/disconnect", func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(cfg, w, r) {
			return
		}
		nodeID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "无效的节点 UUID")
			return
		}
		if !hub.DisconnectNode(nodeID) {
			writeError(w, http.StatusNotFound, "节点不存在或已离线")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"disconnected": true, "node_id": nodeID})
	})

	mux.HandleFunc("POST /api/v1/admin/nodes/update", func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(cfg, w, r) {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var req model.AdminTriggerUpdateRequest
		if r.Body != nil {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
				writeError(w, http.StatusBadRequest, "请求参数解析失败: "+err.Error())
				return
			}
		}
		results := hub.TriggerBatchAgentUpdate(req, requestDownloadBase(r))
		sentCount := 0
		for _, res := range results {
			if res.Sent {
				sentCount++
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"results": results,
			"total":   len(results),
			"sent":    sentCount,
		})
	})

	mux.HandleFunc("POST /api/v1/admin/nodes/{id}/update", func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(cfg, w, r) {
			return
		}
		nodeID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "无效的节点 UUID")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var req model.AdminTriggerUpdateRequest
		if r.Body != nil {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
				writeError(w, http.StatusBadRequest, "请求参数解析失败: "+err.Error())
				return
			}
		}
		res := hub.TriggerAgentUpdate(nodeID, req, requestDownloadBase(r))
		if !res.Sent {
			statusCode := http.StatusBadRequest
			if strings.Contains(res.Message, "不在线") {
				statusCode = http.StatusNotFound
			}
			writeError(w, statusCode, res.Message)
			return
		}
		writeJSON(w, http.StatusOK, res)
	})

	mux.HandleFunc("GET /api/v1/admin/target-policy", func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(cfg, w, r) {
			return
		}
		if orch.TargetPolicy() == nil {
			writeJSON(w, http.StatusOK, model.TargetPolicy{AllowRules: []string{}, DenyRules: []string{}})
			return
		}
		writeJSON(w, http.StatusOK, orch.TargetPolicy().Get())
	})

	mux.HandleFunc("PUT /api/v1/admin/target-policy", func(w http.ResponseWriter, r *http.Request) {
		if !requireAdmin(cfg, w, r) {
			return
		}
		if orch.TargetPolicy() == nil {
			writeError(w, http.StatusServiceUnavailable, "目标限制策略模块未初始化")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var req model.TargetPolicy
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "请求参数解析失败: "+err.Error())
			return
		}
		updated, err := orch.TargetPolicy().Update(r.Context(), req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, updated)
	})

	mux.HandleFunc("POST /api/v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		if limiter != nil && !limiter.Allow(ClientIP(r)) {
			writeError(w, http.StatusTooManyRequests, "请求过于频繁，请稍后再试")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var req model.CreateTaskRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "请求参数 JSON 解析失败: "+err.Error())
			return
		}
		snap, err := orch.CreateTask(r.Context(), req)
		if err != nil {
			switch {
			case errors.Is(err, ErrInvalidRequest):
				writeError(w, http.StatusBadRequest, err.Error())
			case errors.Is(err, ErrNoNodesAvailable):
				writeError(w, http.StatusServiceUnavailable, "当前没有可用的在线探测节点")
			default:
				logger.Error("create task failed", "error", err)
				writeError(w, http.StatusInternalServerError, "创建探测任务失败")
			}
			return
		}
		writeJSON(w, http.StatusCreated, snap)
	})

	mux.HandleFunc("GET /api/v1/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		taskID := r.PathValue("id")
		snap, ok := orch.GetTask(taskID)
		if !ok {
			writeError(w, http.StatusNotFound, "任务不存在或已过期")
			return
		}
		writeJSON(w, http.StatusOK, snap)
	})

	mux.HandleFunc("POST /api/v1/tasks/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		taskID := r.PathValue("id")
		snap, err := orch.CancelTask(r.Context(), taskID)
		if err != nil {
			if errors.Is(err, ErrTaskNotFound) {
				writeError(w, http.StatusNotFound, "任务不存在")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, snap)
	})

	mux.HandleFunc("GET /api/v1/tasks/{id}/stream", func(w http.ResponseWriter, r *http.Request) {
		taskID := r.PathValue("id")
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, http.StatusInternalServerError, "streaming unsupported")
			return
		}

		snap, ch, unsub, err := orch.SubscribeTask(r.Context(), taskID)
		if err != nil {
			if errors.Is(err, ErrTaskNotFound) {
				writeError(w, http.StatusNotFound, "任务不存在或已过期")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer unsub()

		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		if err := writeSSE(w, flusher, "snapshot", snap); err != nil {
			return
		}
		if snap.TotalNodes > 0 && snap.CompletedNodes >= snap.TotalNodes {
			_ = writeSSE(w, flusher, "done", snap)
			return
		}

		keepalive := time.NewTicker(15 * time.Second)
		defer keepalive.Stop()

		for {
			select {
			case <-r.Context().Done():
				return
			case <-keepalive.C:
				if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
					return
				}
				flusher.Flush()
			case ev, open := <-ch:
				if !open {
					return
				}
				switch ev.EventType {
				case "done":
					payload := any(ev)
					if ev.Snapshot != nil {
						payload = ev.Snapshot
					}
					_ = writeSSE(w, flusher, "done", payload)
					return
				case "progress", "result":
					if err := writeSSE(w, flusher, ev.EventType, ev); err != nil {
						return
					}
				}
			}
		}
	})

	// Static SPA handler (either external static_dir or embedded web/dist).
	var staticFS fs.FS
	if cfg.StaticDir != "" {
		staticFS = os.DirFS(cfg.StaticDir)
	} else {
		sub, err := fs.Sub(web.DistFS, "dist")
		if err == nil {
			staticFS = sub
		}
	}
	if staticFS != nil {
		mux.Handle("/", newSPAHandler(staticFS))
	}

	return withCORS(mux)
}

func requestDownloadBase(r *http.Request) string {
	if r == nil || r.Host == "" {
		return ""
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s/downloads", scheme, r.Host)
}

func newSPAHandler(fsys fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusNotFound, "api route not found")
			return
		}
		cleanPath := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if cleanPath == "" {
			cleanPath = "index.html"
		}
		if info, err := fs.Stat(fsys, cleanPath); err == nil && !info.IsDir() {
			fileServer.ServeHTTP(w, r)
			return
		}
		// Fallback to index.html for Vue Router history mode.
		data, err := fs.ReadFile(fsys, "index.html")
		if err != nil {
			http.Error(w, "11Dog frontend assets not built yet (run npm run build inside web/)", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	})
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Admin-Token")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func nodeRecordToInfo(rec *store.NodeRecord) model.NodeInfo {
	return model.NodeInfo{
		ID:             rec.ID,
		Name:           rec.Name,
		Region:         rec.Region,
		Group:          rec.Group,
		ASN:            rec.ASN,
		MaxConcurrency: rec.MaxConcurrency,
		Enabled:        rec.Enabled,
		Notes:          rec.Notes,
		Token:          rec.Token,
		Creds:          rec.Creds,
		CreatedAtMs:    rec.CreatedAtMs,
		UpdatedAtMs:    rec.UpdatedAtMs,
		Status:         "offline",
	}
}

func requireAdmin(cfg *config.ServerConfig, w http.ResponseWriter, r *http.Request) bool {
	expected := strings.TrimSpace(cfg.Auth.AdminToken)
	if expected == "" {
		writeError(w, http.StatusServiceUnavailable, "后台管理未启用（未配置 admin_token）")
		return false
	}
	provided := strings.TrimSpace(r.Header.Get("X-Admin-Token"))
	if provided == "" {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			provided = strings.TrimSpace(auth[7:])
		}
	}
	if provided == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
		writeError(w, http.StatusUnauthorized, "管理员认证失败")
		return false
	}
	return true
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{
		"error": message,
	})
}
