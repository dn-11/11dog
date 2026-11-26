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

	updatePendingTimeout    = 60 * time.Second
	updateDownloadTimeout   = 6 * time.Minute
	updateApplyingTimeout   = 90 * time.Second
	updateRestartingTimeout = 2 * time.Minute
	restartTimeoutMsgPrefix = "等待节点重启上线超时"
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

func isUpdateInProgress(state string) bool {
	switch state {
	case "pending", "downloading", "verifying", "applying", "restarting":
		return true
	default:
		return false
	}
}

func updateStageTimeout(state string) (time.Duration, string) {
	switch state {
	case "pending":
		return updatePendingTimeout, "等待节点响应更新指令超时"
	case "downloading", "verifying":
		return updateDownloadTimeout, "节点下载或校验更新包超时"
	case "applying":
		return updateApplyingTimeout, "节点替换二进制超时"
	case "restarting":
		return updateRestartingTimeout, restartTimeoutMsgPrefix
	default:
		return 0, ""
	}
}

func isRestartTimeoutFailure(st *model.AgentUpdateState) bool {
	return st != nil && st.State == "failed" && strings.HasPrefix(st.Message, restartTimeoutMsgPrefix)
}

func isTargetVersionMatch(targetVer, currentVer string) bool {
	targetVer = strings.TrimSpace(targetVer)
	currentVer = strings.TrimSpace(currentVer)
	if targetVer == "" || targetVer == "dev" {
		return true
	}
	return currentVer == targetVer
}

func isNewAgentSession(st *model.AgentUpdateState, hb *model.AgentHeartbeat) bool {
	if st == nil || hb == nil || hb.ConnectedAtMs <= 0 {
		return false
	}
	if st.PrevConnectedAtMs > 0 {
		return hb.ConnectedAtMs != st.PrevConnectedAtMs
	}
	return st.State == "restarting" && st.UpdatedAtMs > 0 && hb.ConnectedAtMs >= st.UpdatedAtMs
}

func resolvePostRestartUpdateState(prev *model.AgentUpdateState, currentVersion string, nowMs int64) model.AgentUpdateState {
	targetVer := ""
	prevVer := ""
	prevState := "restarting"
	if prev != nil {
		targetVer = strings.TrimSpace(prev.TargetVersion)
		prevVer = strings.TrimSpace(prev.PreviousVersion)
		prevState = prev.State
	}
	currVer := strings.TrimSpace(currentVersion)
	dispVer := currVer
	if dispVer == "" {
		dispVer = "unknown"
	}

	switch prevState {
	case "applying", "restarting", "failed":
		if isTargetVersionMatch(targetVer, currVer) {
			resolvedTarget := targetVer
			if resolvedTarget == "" || resolvedTarget == "dev" {
				resolvedTarget = currVer
			}
			return model.AgentUpdateState{
				TargetVersion: resolvedTarget,
				State:         "up_to_date",
				Message:       fmt.Sprintf("更新已完成，当前运行版本 %s", dispVer),
				UpdatedAtMs:   nowMs,
			}
		}
		return model.AgentUpdateState{
			TargetVersion: targetVer,
			State:         "failed",
			Message:       fmt.Sprintf("节点重启后版本 (%s) 与目标版本 (%s) 不一致", dispVer, targetVer),
			UpdatedAtMs:   nowMs,
		}
	default:
		if targetVer != "" && targetVer != "dev" && currVer == targetVer && currVer != prevVer {
			return model.AgentUpdateState{
				TargetVersion: targetVer,
				State:         "up_to_date",
				Message:       fmt.Sprintf("更新已完成，当前运行版本 %s", dispVer),
				UpdatedAtMs:   nowMs,
			}
		}
		return model.AgentUpdateState{
			TargetVersion: targetVer,
			State:         "failed",
			Message:       fmt.Sprintf("节点在更新完成前发生重启（当前版本 %s）", dispVer),
			UpdatedAtMs:   nowMs,
		}
	}
}

func (h *Hub) getRawNodeUpdateStatus(ctx context.Context, nodeID uuid.UUID) *model.AgentUpdateState {
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

func (h *Hub) putNodeUpdateStatus(ctx context.Context, nodeID uuid.UUID, st model.AgentUpdateState) bool {
	data, err := json.Marshal(st)
	if err != nil {
		return false
	}
	if _, err := h.updatesKV.Put(ctx, nodeID.String(), data); err != nil {
		h.logger.Warn("failed to store agent update status in NATS KV", "node_id", nodeID, "error", err)
		return false
	}
	h.logger.Info("agent update status reported",
		"node_id", nodeID,
		"target_version", st.TargetVersion,
		"state", st.State,
		"message", st.Message,
	)
	return true
}

func (h *Hub) reconcileNodeUpdateStatus(ctx context.Context, nodeID uuid.UUID, st *model.AgentUpdateState, hb *model.AgentHeartbeat) *model.AgentUpdateState {
	if st == nil {
		return nil
	}
	nowMs := time.Now().UnixMilli()

	if isUpdateInProgress(st.State) {
		if isNewAgentSession(st, hb) {
			resolved := resolvePostRestartUpdateState(st, hb.Version, nowMs)
			h.putNodeUpdateStatus(ctx, nodeID, resolved)
			return &resolved
		}
		if timeout, reason := updateStageTimeout(st.State); timeout > 0 && st.UpdatedAtMs > 0 && nowMs-st.UpdatedAtMs > timeout.Milliseconds() {
			timedOut := model.AgentUpdateState{
				TargetVersion:     st.TargetVersion,
				PreviousVersion:   st.PreviousVersion,
				PrevConnectedAtMs: st.PrevConnectedAtMs,
				State:             "failed",
				Message:           reason,
				UpdatedAtMs:       nowMs,
			}
			h.putNodeUpdateStatus(ctx, nodeID, timedOut)
			return &timedOut
		}
		return st
	}

	// Allow late-reconnecting upgraded process to clear a prior restart-wait timeout.
	if isRestartTimeoutFailure(st) && isNewAgentSession(st, hb) && isTargetVersionMatch(st.TargetVersion, hb.Version) {
		resolved := resolvePostRestartUpdateState(st, hb.Version, nowMs)
		h.putNodeUpdateStatus(ctx, nodeID, resolved)
		return &resolved
	}

	return st
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
	if prev := h.getRawNodeUpdateStatus(ctx, nodeID); prev != nil {
		_ = h.reconcileNodeUpdateStatus(ctx, nodeID, prev, &hb)
	}
}

func (h *Hub) handleUpdateStatusMsg(msg *natsgo.Msg) {
	var st model.AgentUpdateState
	if err := json.Unmarshal(msg.Data, &st); err != nil {
		return
	}
	nodeID, err := uuid.Parse(strings.TrimPrefix(msg.Subject, "agent.update."))
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	nowMs := time.Now().UnixMilli()
	prev := h.getRawNodeUpdateStatus(ctx, nodeID)

	if st.State == "restarted" {
		reportedVer := strings.TrimSpace(st.TargetVersion)
		if prev == nil {
			return
		}
		if isUpdateInProgress(prev.State) || (isRestartTimeoutFailure(prev) && isTargetVersionMatch(prev.TargetVersion, reportedVer)) {
			resolved := resolvePostRestartUpdateState(prev, reportedVer, nowMs)
			h.putNodeUpdateStatus(ctx, nodeID, resolved)
		}
		return
	}

	st.UpdatedAtMs = nowMs
	if prev != nil {
		if st.TargetVersion == "" {
			st.TargetVersion = prev.TargetVersion
		}
		if st.PreviousVersion == "" {
			st.PreviousVersion = prev.PreviousVersion
		}
		if st.PrevConnectedAtMs == 0 {
			st.PrevConnectedAtMs = prev.PrevConnectedAtMs
		}
	}

	if isUpdateInProgress(st.State) {
		if hb, ok := h.getNodeHeartbeat(ctx, nodeID); ok && isNewAgentSession(&st, hb) {
			resolved := resolvePostRestartUpdateState(&st, hb.Version, nowMs)
			h.putNodeUpdateStatus(ctx, nodeID, resolved)
			return
		}
	}

	h.putNodeUpdateStatus(ctx, nodeID, st)
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

func (h *Hub) getNodeUpdateStatus(ctx context.Context, nodeID uuid.UUID, liveHB *model.AgentHeartbeat) *model.AgentUpdateState {
	st := h.getRawNodeUpdateStatus(ctx, nodeID)
	if st == nil {
		return nil
	}
	return h.reconcileNodeUpdateStatus(ctx, nodeID, st, liveHB)
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
		TargetVersion:     targetVersion,
		PreviousVersion:   hb.Version,
		PrevConnectedAtMs: hb.ConnectedAtMs,
		State:             "pending",
		Message:           "已下发更新指令",
		UpdatedAtMs:       time.Now().UnixMilli(),
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
		var liveHB *model.AgentHeartbeat
		if hb, ok := h.getNodeHeartbeat(ctx, rec.ID); ok {
			liveHB = hb
			info.Status = "online"
			info.IPv4 = hb.IPv4
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
		info.UpdateStatus = h.getNodeUpdateStatus(ctx, rec.ID, liveHB)
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
