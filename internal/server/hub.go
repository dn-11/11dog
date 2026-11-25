package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dn-11/11dog/internal/config"
	"github.com/dn-11/11dog/internal/model"
	"github.com/dn-11/11dog/internal/mq/nats"
	"github.com/dn-11/11dog/internal/natsauth"
	"github.com/dn-11/11dog/internal/store"
	"github.com/google/uuid"
	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	kvNodesBucket   = "DOG_NODES"
	kvUpdatesBucket = "DOG_UPDATES"
	nodeOnlineTTL   = 35 * time.Second
)

// Hub manages stateless edge node telemetry (via NATS KV) and control commands (via Core NATS).
// Authoritative node identities & .creds live in NodeStore; any server replica shares the same live view.
type Hub struct {
	cfg     *config.ServerConfig
	broker  *nats.Broker
	store   store.NodeStore
	authMgr *natsauth.Manager
	logger  *slog.Logger

	nodesKV   jetstream.KeyValue
	updatesKV jetstream.KeyValue

	mu   sync.Mutex
	subs []*natsgo.Subscription
}

// NewHub initializes NATS KV buckets for runtime node telemetry and subscribes to agent heartbeat/update subjects.
func NewHub(ctx context.Context, cfg *config.ServerConfig, broker *nats.Broker, nodeStore store.NodeStore, authMgr *natsauth.Manager, logger *slog.Logger) (*Hub, error) {
	if logger == nil {
		logger = slog.Default()
	}
	js := broker.JetStream()

	nodesKV, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:  kvNodesBucket,
		TTL:     nodeOnlineTTL,
		Storage: jetstream.MemoryStorage,
	})
	if err != nil {
		return nil, fmt.Errorf("init %s kv bucket: %w", kvNodesBucket, err)
	}

	updatesKV, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:  kvUpdatesBucket,
		TTL:     24 * time.Hour,
		Storage: jetstream.MemoryStorage,
	})
	if err != nil {
		return nil, fmt.Errorf("init %s kv bucket: %w", kvUpdatesBucket, err)
	}

	h := &Hub{
		cfg:       cfg,
		broker:    broker,
		store:     nodeStore,
		authMgr:   authMgr,
		logger:    logger,
		nodesKV:   nodesKV,
		updatesKV: updatesKV,
	}

	nc := broker.Conn()
	hbSub, err := nc.QueueSubscribe("agent.hb.*", "11dog-servers", h.handleHeartbeatMsg)
	if err != nil {
		return nil, fmt.Errorf("subscribe agent.hb.*: %w", err)
	}
	updSub, err := nc.QueueSubscribe("agent.update.*", "11dog-servers", h.handleUpdateStatusMsg)
	if err != nil {
		_ = hbSub.Unsubscribe()
		return nil, fmt.Errorf("subscribe agent.update.*: %w", err)
	}
	h.subs = []*natsgo.Subscription{hbSub, updSub}

	return h, nil
}

// Close unsubscribes NATS listeners held by Hub.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, sub := range h.subs {
		_ = sub.Unsubscribe()
	}
	h.subs = nil
}

// AuthManager returns the NATS JWT/NKey manager used by this Hub.
func (h *Hub) AuthManager() *natsauth.Manager {
	return h.authMgr
}

func (h *Hub) handleHeartbeatMsg(msg *natsgo.Msg) {
	var hb model.AgentHeartbeat
	if err := json.Unmarshal(msg.Data, &hb); err != nil {
		return
	}
	if hb.TimestampUnixMs == 0 {
		hb.TimestampUnixMs = time.Now().UnixMilli()
	}
	nodeID, err := uuid.Parse(strings.TrimPrefix(msg.Subject, "agent.hb."))
	if err != nil {
		nodeID = hb.NodeID
	}
	if nodeID == uuid.Nil {
		return
	}
	hb.NodeID = nodeID
	data, err := json.Marshal(hb)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := h.nodesKV.Put(ctx, nodeID.String(), data); err != nil {
		h.logger.Warn("failed to store agent heartbeat in NATS KV", "node_id", nodeID, "error", err)
	}
}

func (h *Hub) handleUpdateStatusMsg(msg *natsgo.Msg) {
	var st model.AgentUpdateState
	if err := json.Unmarshal(msg.Data, &st); err != nil {
		return
	}
	if st.UpdatedAtMs == 0 {
		st.UpdatedAtMs = time.Now().UnixMilli()
	}
	nodeID, err := uuid.Parse(strings.TrimPrefix(msg.Subject, "agent.update."))
	if err != nil {
		return
	}
	data, err := json.Marshal(st)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := h.updatesKV.Put(ctx, nodeID.String(), data); err != nil {
		h.logger.Warn("failed to store agent update status in NATS KV", "node_id", nodeID, "error", err)
		return
	}
	h.logger.Info("agent update status reported",
		"node_id", nodeID,
		"target_version", st.TargetVersion,
		"state", st.State,
		"message", st.Message,
	)
}

func (h *Hub) getNodeHeartbeat(ctx context.Context, nodeID uuid.UUID) (*model.AgentHeartbeat, bool) {
	entry, err := h.nodesKV.Get(ctx, nodeID.String())
	if err != nil {
		return nil, false
	}
	var hb model.AgentHeartbeat
	if err := json.Unmarshal(entry.Value(), &hb); err != nil {
		return nil, false
	}
	return &hb, true
}

func (h *Hub) getNodeUpdateStatus(ctx context.Context, nodeID uuid.UUID) *model.AgentUpdateState {
	entry, err := h.updatesKV.Get(ctx, nodeID.String())
	if err != nil {
		return nil
	}
	var st model.AgentUpdateState
	if err := json.Unmarshal(entry.Value(), &st); err != nil {
		return nil
	}
	return &st
}

// CancelTask publishes a cancellation command to a specific node over Core NATS.
func (h *Hub) CancelTask(nodeID uuid.UUID, taskID, subtaskID, reason string) error {
	payload, err := json.Marshal(model.CancelTaskCommand{
		TaskID:    taskID,
		SubtaskID: subtaskID,
		Reason:    reason,
	})
	if err != nil {
		return err
	}
	return h.broker.Conn().Publish("ctrl.node."+nodeID.String()+".cancel", payload)
}

// DisconnectNode sends a reconnect command to an online node over Core NATS.
func (h *Hub) DisconnectNode(nodeID uuid.UUID) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, ok := h.getNodeHeartbeat(ctx, nodeID); !ok {
		return false
	}
	payload, _ := json.Marshal(model.ReconnectCommand{
		Reason:            "reconnect requested by admin",
		ReconnectAfterSec: 2,
	})
	return h.broker.Conn().Publish("ctrl.node."+nodeID.String()+".reconnect", payload) == nil
}

// TriggerAgentUpdate pushes a self-update command to a single online node over Core NATS.
func (h *Hub) TriggerAgentUpdate(nodeID uuid.UUID, req model.AdminTriggerUpdateRequest, fallbackDownloadBase string) model.AdminUpdateNodeResult {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	hb, online := h.getNodeHeartbeat(ctx, nodeID)
	if !online {
		return model.AdminUpdateNodeResult{
			NodeID:  nodeID,
			Sent:    false,
			Message: "节点不在线",
		}
	}

	targetVersion := strings.TrimSpace(req.Version)
	if targetVersion == "" {
		targetVersion = Version
	}

	downloadURL := strings.TrimSpace(req.DownloadURL)
	shaHex := strings.TrimSpace(req.SHA256)

	if downloadURL == "" {
		filename := AgentBinaryFilename(hb.OS, hb.Arch)
		base := strings.TrimRight(strings.TrimSpace(h.cfg.AgentDownloadBaseURL), "/")
		if base == "" {
			base = strings.TrimRight(strings.TrimSpace(fallbackDownloadBase), "/")
		}
		computedSHA, err := ComputeHostedBinarySHA256(h.cfg.AgentBinDir, filename)
		if err == nil {
			if shaHex == "" {
				shaHex = computedSHA
			}
			if base == "" {
				return model.AdminUpdateNodeResult{
					NodeID:  nodeID,
					Sent:    false,
					Message: "无法确定 Agent 下载地址，请配置 agent_download_base_url 或自定义下载地址",
				}
			}
			downloadURL = base + "/" + filename
		} else if strings.TrimSpace(h.cfg.AgentDownloadBaseURL) != "" {
			downloadURL = base + "/" + filename
		} else {
			return model.AdminUpdateNodeResult{
				NodeID:  nodeID,
				Sent:    false,
				Message: fmt.Sprintf("服务端未托管对应平台二进制文件: %s", filename),
			}
		}
	}

	pending := model.AgentUpdateState{
		TargetVersion: targetVersion,
		State:         "pending",
		Message:       "已下发更新指令",
		UpdatedAtMs:   time.Now().UnixMilli(),
	}
	if pendingBytes, err := json.Marshal(pending); err == nil {
		_, _ = h.updatesKV.Put(ctx, nodeID.String(), pendingBytes)
	}

	cmdPayload, err := json.Marshal(model.AgentUpdateCommand{
		Version:     targetVersion,
		DownloadURL: downloadURL,
		SHA256:      shaHex,
		Force:       req.Force,
	})
	if err != nil {
		return model.AdminUpdateNodeResult{
			NodeID:  nodeID,
			Sent:    false,
			Message: err.Error(),
		}
	}

	if err := h.broker.Conn().Publish("ctrl.node."+nodeID.String()+".update", cmdPayload); err != nil {
		return model.AdminUpdateNodeResult{
			NodeID:  nodeID,
			Sent:    false,
			Message: "下发更新指令失败: " + err.Error(),
		}
	}

	h.logger.Info("dispatched agent self-update over NATS",
		"node_id", nodeID,
		"target_version", targetVersion,
		"download_url", downloadURL,
		"force", req.Force,
	)
	return model.AdminUpdateNodeResult{
		NodeID:  nodeID,
		Sent:    true,
		Message: "已下发更新指令",
	}
}

// TriggerBatchAgentUpdate pushes a self-update command to selected node UUIDs or all online nodes.
func (h *Hub) TriggerBatchAgentUpdate(req model.AdminTriggerUpdateRequest, fallbackDownloadBase string) []model.AdminUpdateNodeResult {
	targetIDs := req.NodeIDs
	if len(targetIDs) == 0 {
		for _, n := range h.ListNodes() {
			if n.Status == "online" {
				targetIDs = append(targetIDs, n.ID)
			}
		}
		sort.Slice(targetIDs, func(i, j int) bool {
			return targetIDs[i].String() < targetIDs[j].String()
		})
	}

	results := make([]model.AdminUpdateNodeResult, 0, len(targetIDs))
	for _, id := range targetIDs {
		if id == uuid.Nil {
			continue
		}
		results = append(results, h.TriggerAgentUpdate(id, req, fallbackDownloadBase))
	}
	return results
}

// ListNodes returns all authoritative nodes from DB merged with live NATS KV telemetry.
func (h *Hub) ListNodes() []model.NodeInfo {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	recs, err := h.store.ListNodes(ctx)
	if err != nil {
		h.logger.Error("failed to list nodes from store", "error", err)
		return nil
	}

	nodes := make([]model.NodeInfo, 0, len(recs))
	for _, rec := range recs {
		info := model.NodeInfo{
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
			Capabilities:   []model.ProbeType{model.ProbePing, model.ProbeTcping, model.ProbeHTTP, model.ProbeTraceroute, model.ProbeDNS},
		}
		if hb, ok := h.getNodeHeartbeat(ctx, rec.ID); ok {
			info.Status = "online"
			info.IPv4 = hb.IPv4
			info.IPv6 = hb.IPv6
			if len(hb.Capabilities) > 0 {
				info.Capabilities = hb.Capabilities
			}
			if hb.MaxConcurrency > 0 && (info.MaxConcurrency == 0 || hb.MaxConcurrency < info.MaxConcurrency) {
				info.MaxConcurrency = hb.MaxConcurrency
			}
			info.ActiveTasks = hb.ActiveTasks
			info.CPUPercent = hb.CPUPercent
			info.MemPercent = hb.MemPercent
			info.Version = hb.Version
			info.OS = hb.OS
			info.Arch = hb.Arch
			info.LastSeenUnixMs = hb.TimestampUnixMs
			info.ConnectedAtMs = hb.ConnectedAtMs
			info.Labels = hb.Labels
		}
		info.UpdateStatus = h.getNodeUpdateStatus(ctx, rec.ID)
		info.QueueDepth = h.broker.QueueDepth(rec.ID)
		nodes = append(nodes, info)
	}

	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Status != nodes[j].Status {
			return nodes[i].Status == "online"
		}
		if nodes[i].Region != nodes[j].Region {
			return nodes[i].Region < nodes[j].Region
		}
		if nodes[i].Group != nodes[j].Group {
			return nodes[i].Group < nodes[j].Group
		}
		return nodes[i].Name < nodes[j].Name
	})
	return nodes
}

// OnlineCount returns the count of currently online nodes based on NATS KV heartbeats.
func (h *Hub) OnlineCount() int {
	count := 0
	for _, n := range h.ListNodes() {
		if n.Status == "online" {
			count++
		}
	}
	return count
}

// EnsureNodeCredsBackfilled ensures any existing DB node without .creds gets one issued.
func (h *Hub) EnsureNodeCredsBackfilled(ctx context.Context) error {
	if h.authMgr == nil {
		return nil
	}
	recs, err := h.store.ListNodes(ctx)
	if err != nil {
		return err
	}
	for _, rec := range recs {
		if strings.TrimSpace(rec.Creds) != "" {
			continue
		}
		creds, err := h.authMgr.IssueAgentCreds(rec.ID)
		if err != nil {
			return fmt.Errorf("backfill creds for %s: %w", rec.ID, err)
		}
		if _, err := h.store.UpdateCreds(ctx, rec.ID, creds); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	return nil
}
