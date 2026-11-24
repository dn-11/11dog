package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	agentv1 "github.com/11dog/11dog/gen/agent/v1"
	"github.com/11dog/11dog/internal/config"
	"github.com/11dog/11dog/internal/dn11"
	"github.com/11dog/11dog/internal/model"
	"github.com/11dog/11dog/internal/mq"
	"github.com/11dog/11dog/internal/store"
	"golang.org/x/time/rate"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type inFlightSubtask struct {
	taskID string
	nodeID string
	timer  *time.Timer
}

type agentSession struct {
	infoMu      sync.RWMutex
	info        model.NodeInfo
	stream      agentv1.AgentHub_ConnectServer
	sendMu      sync.Mutex
	ctx         context.Context
	cancel      context.CancelFunc
	sem         chan struct{}
	dispatchLim *rate.Limiter

	inflightMu sync.Mutex
	inflight   map[string]*inFlightSubtask
}

func (s *agentSession) getInfo() model.NodeInfo {
	s.infoMu.RLock()
	defer s.infoMu.RUnlock()
	cp := s.info
	cp.Capabilities = append([]model.ProbeType(nil), s.info.Capabilities...)
	return cp
}

func (s *agentSession) updateTelemetry(activeTasks uint32, cpu, mem float64, nowMs int64) {
	s.infoMu.Lock()
	s.info.ActiveTasks = activeTasks
	s.info.CPUPercent = cpu
	s.info.MemPercent = mem
	s.info.LastSeenUnixMs = nowMs
	s.infoMu.Unlock()
}

func (s *agentSession) applyRecord(rec *store.NodeRecord) {
	s.infoMu.Lock()
	s.info.Name = rec.Name
	s.info.Region = rec.Region
	s.info.Group = rec.Group
	s.info.ASN = rec.ASN
	s.info.MaxConcurrency = rec.MaxConcurrency
	s.info.Enabled = rec.Enabled
	s.info.Notes = rec.Notes
	s.info.Token = rec.Token
	s.info.CreatedAtMs = rec.CreatedAtMs
	s.info.UpdatedAtMs = rec.UpdatedAtMs
	s.infoMu.Unlock()
}

func (s *agentSession) send(msg *agentv1.ServerMessage) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.stream.Send(msg)
}

func (s *agentSession) releaseInflight(subtaskID string) bool {
	s.inflightMu.Lock()
	entry, ok := s.inflight[subtaskID]
	if ok {
		delete(s.inflight, subtaskID)
	}
	s.inflightMu.Unlock()
	if !ok {
		return false
	}
	if entry.timer != nil {
		entry.timer.Stop()
	}
	select {
	case <-s.sem:
	default:
	}
	return true
}

func (s *agentSession) releaseAllInflight() {
	s.inflightMu.Lock()
	entries := make([]*inFlightSubtask, 0, len(s.inflight))
	for k, v := range s.inflight {
		entries = append(entries, v)
		delete(s.inflight, k)
	}
	s.inflightMu.Unlock()
	for _, entry := range entries {
		if entry.timer != nil {
			entry.timer.Stop()
		}
		select {
		case <-s.sem:
		default:
		}
	}
}

// HubMetricsCallbacks lets the orchestrator observe hub task lifecycle counts.
type HubMetricsCallbacks struct {
	OnExpired   func()
	OnCompleted func()
}

// Hub implements agentv1.AgentHubServer for reverse-gRPC edge node sessions.
// Authoritative node identities & tokens live in NodeStore.
type Hub struct {
	agentv1.UnimplementedAgentHubServer

	cfg     *config.ServerConfig
	broker  mq.Broker
	store   store.NodeStore
	dn11Reg *dn11.Registry
	logger  *slog.Logger
	metrics HubMetricsCallbacks

	mu       sync.RWMutex
	sessions map[string]*agentSession
	// lastSeen tracks runtime host metadata (IPs, version, capabilities) between reconnects.
	lastRuntime map[string]model.NodeInfo
}

// NewHub creates a reverse-gRPC Hub backed by authoritative NodeStore.
func NewHub(cfg *config.ServerConfig, broker mq.Broker, nodeStore store.NodeStore, dn11Reg *dn11.Registry, logger *slog.Logger) *Hub {
	if logger == nil {
		logger = slog.Default()
	}
	return &Hub{
		cfg:         cfg,
		broker:      broker,
		store:       nodeStore,
		dn11Reg:     dn11Reg,
		logger:      logger,
		sessions:    make(map[string]*agentSession),
		lastRuntime: make(map[string]model.NodeInfo),
	}
}

// SetMetricsCallbacks registers optional lifecycle counter callbacks.
func (h *Hub) SetMetricsCallbacks(cb HubMetricsCallbacks) {
	h.metrics = cb
}

func extractAgentToken(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	if vals := md.Get("x-agent-token"); len(vals) > 0 {
		return strings.TrimSpace(vals[0])
	}
	if vals := md.Get("authorization"); len(vals) > 0 {
		raw := strings.TrimSpace(vals[0])
		if strings.HasPrefix(strings.ToLower(raw), "bearer ") {
			return strings.TrimSpace(raw[7:])
		}
		return raw
	}
	return ""
}

func (h *Hub) authenticateNode(ctx context.Context) (*store.NodeRecord, error) {
	token := extractAgentToken(ctx)
	if token == "" {
		return nil, status.Error(codes.Unauthenticated, "missing agent communication token")
	}
	rec, err := h.store.GetNodeByToken(ctx, token)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, status.Error(codes.Unauthenticated, "invalid agent communication token")
		}
		return nil, status.Errorf(codes.Internal, "lookup agent token: %v", err)
	}
	if !rec.Enabled {
		return nil, status.Error(codes.PermissionDenied, "node is disabled by administrator")
	}
	return rec, nil
}

// Connect handles an incoming reverse gRPC bi-directional stream from an edge agent.
func (h *Hub) Connect(stream agentv1.AgentHub_ConnectServer) error {
	ctx := stream.Context()
	rec, err := h.authenticateNode(ctx)
	if err != nil {
		return err
	}

	firstMsg, err := stream.Recv()
	if err != nil {
		return err
	}
	reg := firstMsg.GetRegister()
	if reg == nil {
		return status.Error(codes.InvalidArgument, "first message must be RegisterRequest")
	}

	maxConc := rec.MaxConcurrency
	if maxConc == 0 {
		maxConc = 8
	}
	if reg.MaxConcurrency > 0 && reg.MaxConcurrency < maxConc {
		maxConc = reg.MaxConcurrency
	}
	if maxConc > 128 {
		maxConc = 128
	}

	caps := make([]model.ProbeType, 0, len(reg.Capabilities))
	for _, c := range reg.Capabilities {
		if pt := model.ProbeTypeFromProto(c); pt.IsValid() {
			caps = append(caps, pt)
		}
	}
	if len(caps) == 0 {
		caps = []model.ProbeType{model.ProbePing, model.ProbeTcping, model.ProbeHTTP, model.ProbeTraceroute, model.ProbeDNS}
	}

	ipv4 := reg.Ipv4
	if ipv4 == "" {
		if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
			host := p.Addr.String()
			if idx := strings.LastIndex(host, ":"); idx > 0 {
				host = host[:idx]
			}
			host = strings.Trim(host, "[]")
			if strings.Contains(host, ".") {
				ipv4 = host
			}
		}
	}

	nowMs := time.Now().UnixMilli()
	rps := h.cfg.Limits.PerNodeDispatchRPS
	if rps <= 0 {
		rps = 10
	}
	burst := int(maxConc)
	if burst < 2 {
		burst = 2
	}

	sessCtx, sessCancel := context.WithCancel(ctx)
	session := &agentSession{
		info: model.NodeInfo{
			ID:             rec.ID,
			Name:           rec.Name,
			Region:         rec.Region,
			Group:          rec.Group,
			ASN:            rec.ASN,
			IPv4:           ipv4,
			IPv6:           reg.Ipv6,
			Capabilities:   caps,
			MaxConcurrency: maxConc,
			Version:        reg.Version,
			Status:         "online",
			Enabled:        rec.Enabled,
			Notes:          rec.Notes,
			Token:          rec.Token,
			CreatedAtMs:    rec.CreatedAtMs,
			UpdatedAtMs:    rec.UpdatedAtMs,
			LastSeenUnixMs: nowMs,
			ConnectedAtMs:  nowMs,
			Labels:         reg.Labels,
		},
		stream:      stream,
		ctx:         sessCtx,
		cancel:      sessCancel,
		sem:         make(chan struct{}, maxConc),
		dispatchLim: rate.NewLimiter(rate.Limit(rps), burst),
		inflight:    make(map[string]*inFlightSubtask),
	}

	h.registerSession(session)
	defer h.unregisterSession(session)

	if err := session.send(&agentv1.ServerMessage{
		Payload: &agentv1.ServerMessage_RegisterResp{
			RegisterResp: &agentv1.RegisterResponse{
				Accepted:               true,
				Message:                "registered",
				HeartbeatIntervalSec:   10,
				AssignedMaxConcurrency: maxConc,
				AssignedNodeId:         rec.ID,
				AssignedName:           rec.Name,
			},
		},
	}); err != nil {
		return err
	}

	h.logger.Info("agent authenticated and connected",
		"node_id", rec.ID,
		"name", rec.Name,
		"region", rec.Region,
		"group", rec.Group,
		"asn", rec.ASN,
		"max_concurrency", maxConc,
	)

	go h.runDispatcher(session)

	for {
		msg, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) || sessCtx.Err() != nil {
				return nil
			}
			return err
		}
		h.handleAgentMessage(session, msg)
	}
}

func (h *Hub) registerSession(session *agentSession) {
	info := session.getInfo()
	h.mu.Lock()
	old := h.sessions[info.ID]
	h.sessions[info.ID] = session
	h.lastRuntime[info.ID] = info
	h.mu.Unlock()

	if old != nil {
		old.cancel()
	}
}

func (h *Hub) unregisterSession(session *agentSession) {
	session.cancel()
	session.releaseAllInflight()

	info := session.getInfo()
	h.mu.Lock()
	if cur, ok := h.sessions[info.ID]; ok && cur == session {
		delete(h.sessions, info.ID)
		info.Status = "offline"
		info.ActiveTasks = 0
		info.LastSeenUnixMs = time.Now().UnixMilli()
		h.lastRuntime[info.ID] = info
	}
	h.mu.Unlock()

	h.logger.Info("agent disconnected", "node_id", info.ID)
}

func (h *Hub) runDispatcher(session *agentSession) {
	nodeID := session.getInfo().ID
	err := h.broker.ConsumeNodeQueue(session.ctx, nodeID, int(session.getInfo().MaxConcurrency), func(ctx context.Context, subtask *mq.QueuedSubtask) error {
		if !subtask.Deadline.IsZero() && time.Now().After(subtask.Deadline) {
			h.publishSyntheticResult(session, subtask, model.StatusExpired, "节点等待超时")
			if h.metrics.OnExpired != nil {
				h.metrics.OnExpired()
			}
			return nil
		}

		if err := session.dispatchLim.Wait(ctx); err != nil {
			return err
		}

		select {
		case session.sem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}

		if !subtask.Deadline.IsZero() && time.Now().After(subtask.Deadline) {
			select {
			case <-session.sem:
			default:
			}
			h.publishSyntheticResult(session, subtask, model.StatusExpired, "节点等待超时")
			if h.metrics.OnExpired != nil {
				h.metrics.OnExpired()
			}
			return nil
		}

		waitDur := time.Until(subtask.Deadline) + 5*time.Second
		if waitDur < 10*time.Second {
			waitDur = 10 * time.Second
		}
		timer := time.AfterFunc(waitDur, func() {
			if session.releaseInflight(subtask.SubtaskID) {
				h.publishSyntheticResult(session, subtask, model.StatusTimeout, "节点探测执行超时未回传结果")
			}
		})

		session.inflightMu.Lock()
		session.inflight[subtask.SubtaskID] = &inFlightSubtask{
			taskID: subtask.TaskID,
			nodeID: nodeID,
			timer:  timer,
		}
		session.inflightMu.Unlock()

		dispatchMsg := &agentv1.ServerMessage{
			Payload: &agentv1.ServerMessage_DispatchTask{
				DispatchTask: &agentv1.DispatchTask{
					TaskId:         subtask.TaskID,
					SubtaskId:      subtask.SubtaskID,
					Type:           model.ProbeTypeToProto(subtask.Type),
					Target:         subtask.Target,
					DeadlineUnixMs: subtask.Deadline.UnixMilli(),
					TimeoutMs:      subtask.TimeoutMs,
					Params:         model.ProbeParamsToProto(subtask.Type, subtask.Params),
				},
			},
		}

		if err := session.send(dispatchMsg); err != nil {
			session.releaseInflight(subtask.SubtaskID)
			return err
		}

		_ = h.broker.PublishResult(context.Background(), &model.TaskEvent{
			EventType: "progress",
			TaskID:    subtask.TaskID,
			SubtaskID: subtask.SubtaskID,
			NodeID:    nodeID,
			Type:      subtask.Type,
			Status:    model.StatusRunning,
		})
		return nil
	})
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, mq.ErrClosed) {
		h.logger.Warn("dispatcher stopped", "node_id", nodeID, "error", err)
	}
}

func (h *Hub) publishSyntheticResult(session *agentSession, subtask *mq.QueuedSubtask, st model.TaskStatus, errMsg string) {
	info := session.getInfo()
	res := &model.NodeTaskResult{
		TaskID:         subtask.TaskID,
		SubtaskID:      subtask.SubtaskID,
		NodeID:         info.ID,
		NodeName:       info.Name,
		Region:         info.Region,
		Group:          info.Group,
		NodeASN:        info.ASN,
		Type:           subtask.Type,
		Status:         st,
		ErrorMessage:   errMsg,
		FinishedUnixMs: time.Now().UnixMilli(),
	}
	_ = h.broker.PublishResult(context.Background(), &model.TaskEvent{
		EventType: "result",
		TaskID:    subtask.TaskID,
		SubtaskID: subtask.SubtaskID,
		NodeID:    info.ID,
		Type:      subtask.Type,
		Status:    st,
		Result:    res,
	})
}

func (h *Hub) handleAgentMessage(session *agentSession, msg *agentv1.AgentMessage) {
	info := session.getInfo()

	switch payload := msg.Payload.(type) {
	case *agentv1.AgentMessage_Heartbeat:
		hb := payload.Heartbeat
		if hb == nil {
			return
		}
		nowMs := time.Now().UnixMilli()
		session.updateTelemetry(hb.ActiveTasks, hb.CpuPercent, hb.MemPercent, nowMs)

		h.mu.Lock()
		h.lastRuntime[info.ID] = session.getInfo()
		h.mu.Unlock()

		_ = session.send(&agentv1.ServerMessage{
			Payload: &agentv1.ServerMessage_HeartbeatAck{
				HeartbeatAck: &agentv1.HeartbeatAck{
					ServerUnixMs: nowMs,
				},
			},
		})

	case *agentv1.AgentMessage_TaskAck:
		ack := payload.TaskAck
		if ack == nil {
			return
		}
		if !ack.Accepted {
			session.releaseInflight(ack.SubtaskId)
			reason := ack.RejectReason
			if reason == "" {
				reason = "节点繁忙，请稍后再试"
			}
			res := &model.NodeTaskResult{
				TaskID:         ack.TaskId,
				SubtaskID:      ack.SubtaskId,
				NodeID:         info.ID,
				NodeName:       info.Name,
				Region:         info.Region,
				Group:          info.Group,
				NodeASN:        info.ASN,
				Status:         model.StatusOverloaded,
				ErrorMessage:   reason,
				FinishedUnixMs: time.Now().UnixMilli(),
			}
			_ = h.broker.PublishResult(context.Background(), &model.TaskEvent{
				EventType: "result",
				TaskID:    ack.TaskId,
				SubtaskID: ack.SubtaskId,
				NodeID:    info.ID,
				Status:    model.StatusOverloaded,
				Result:    res,
			})
		}

	case *agentv1.AgentMessage_TaskProgress:
		prog := payload.TaskProgress
		if prog == nil {
			return
		}
		p := model.ProgressFromProto(prog)
		if p != nil && p.TracerouteHop != nil && p.TracerouteHop.IP != "" && h.dn11Reg != nil {
			p.TracerouteHop.DN11 = h.dn11Reg.Lookup(p.TracerouteHop.IP)
		}
		_ = h.broker.PublishResult(context.Background(), &model.TaskEvent{
			EventType: "progress",
			TaskID:    prog.TaskId,
			SubtaskID: prog.SubtaskId,
			NodeID:    info.ID,
			Type:      model.ProbeTypeFromProto(prog.Type),
			Status:    model.StatusRunning,
			Progress:  p,
		})

	case *agentv1.AgentMessage_TaskResult:
		tr := payload.TaskResult
		if tr == nil {
			return
		}
		session.releaseInflight(tr.SubtaskId)

		res := model.TaskResultFromProto(tr)
		res.NodeID = info.ID
		res.NodeName = info.Name
		res.Region = info.Region
		res.Group = info.Group
		res.NodeASN = info.ASN

		if h.dn11Reg != nil {
			if res.ResolvedIP != "" {
				res.ResolvedDN11 = h.dn11Reg.Lookup(res.ResolvedIP)
			}
			if res.Traceroute != nil {
				for i := range res.Traceroute.Hops {
					if res.Traceroute.Hops[i].IP != "" {
						res.Traceroute.Hops[i].DN11 = h.dn11Reg.Lookup(res.Traceroute.Hops[i].IP)
					}
				}
			}
		}

		if h.metrics.OnCompleted != nil {
			h.metrics.OnCompleted()
		}

		_ = h.broker.PublishResult(context.Background(), &model.TaskEvent{
			EventType: "result",
			TaskID:    res.TaskID,
			SubtaskID: res.SubtaskID,
			NodeID:    res.NodeID,
			Type:      res.Type,
			Status:    res.Status,
			Result:    res,
		})
	}
}

// CancelTask sends a cancellation signal to a specific online node.
func (h *Hub) CancelTask(nodeID, taskID, subtaskID, reason string) error {
	h.mu.RLock()
	session := h.sessions[nodeID]
	h.mu.RUnlock()
	if session == nil {
		return fmt.Errorf("node %q is offline", nodeID)
	}
	return session.send(&agentv1.ServerMessage{
		Payload: &agentv1.ServerMessage_CancelTask{
			CancelTask: &agentv1.CancelTask{
				TaskId:    taskID,
				SubtaskId: subtaskID,
				Reason:    reason,
			},
		},
	})
}

// SyncNode updates an active session's metadata from DB; disconnects it if disabled.
func (h *Hub) SyncNode(rec *store.NodeRecord) {
	if rec == nil {
		return
	}
	h.mu.RLock()
	session := h.sessions[rec.ID]
	h.mu.RUnlock()
	if session == nil {
		return
	}
	session.applyRecord(rec)
	if !rec.Enabled {
		_ = session.send(&agentv1.ServerMessage{
			Payload: &agentv1.ServerMessage_ServerDrain{
				ServerDrain: &agentv1.ServerDrain{
					Reason:            "node disabled by administrator",
					ReconnectAfterSec: 15,
				},
			},
		})
		session.cancel()
	}
}

// RevokeNodeSession disconnects an active session when its token is rotated or node is deleted.
func (h *Hub) RevokeNodeSession(nodeID string) {
	h.mu.RLock()
	session := h.sessions[nodeID]
	h.mu.RUnlock()
	if session != nil {
		_ = session.send(&agentv1.ServerMessage{
			Payload: &agentv1.ServerMessage_ServerDrain{
				ServerDrain: &agentv1.ServerDrain{
					Reason:            "credentials revoked by administrator",
					ReconnectAfterSec: 15,
				},
			},
		})
		session.cancel()
	}
	h.mu.Lock()
	delete(h.lastRuntime, nodeID)
	h.mu.Unlock()
}

// DisconnectNode gracefully drains and disconnects an active node session so it reconnects.
func (h *Hub) DisconnectNode(nodeID string) bool {
	h.mu.RLock()
	session := h.sessions[nodeID]
	h.mu.RUnlock()
	if session == nil {
		return false
	}
	_ = session.send(&agentv1.ServerMessage{
		Payload: &agentv1.ServerMessage_ServerDrain{
			ServerDrain: &agentv1.ServerDrain{
				Reason:            "disconnected by admin",
				ReconnectAfterSec: 3,
			},
		},
	})
	session.cancel()
	return true
}

// DrainAll notifies all connected agents to gracefully reconnect before server shutdown.
func (h *Hub) DrainAll(reason string, reconnectAfterSec uint32) {
	h.mu.RLock()
	sessions := make([]*agentSession, 0, len(h.sessions))
	for _, s := range h.sessions {
		sessions = append(sessions, s)
	}
	h.mu.RUnlock()

	for _, s := range sessions {
		_ = s.send(&agentv1.ServerMessage{
			Payload: &agentv1.ServerMessage_ServerDrain{
				ServerDrain: &agentv1.ServerDrain{
					Reason:            reason,
					ReconnectAfterSec: reconnectAfterSec,
				},
			},
		})
		s.cancel()
	}
}

// ListNodes returns all authoritative nodes from DB merged with live session telemetry.
func (h *Hub) ListNodes() []model.NodeInfo {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	recs, err := h.store.ListNodes(ctx)
	if err != nil {
		h.logger.Error("failed to list nodes from store", "error", err)
		return nil
	}

	h.mu.RLock()
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
			CreatedAtMs:    rec.CreatedAtMs,
			UpdatedAtMs:    rec.UpdatedAtMs,
			Status:         "offline",
			Capabilities:   []model.ProbeType{model.ProbePing, model.ProbeTcping, model.ProbeHTTP, model.ProbeTraceroute, model.ProbeDNS},
		}
		if sess, ok := h.sessions[rec.ID]; ok {
			live := sess.getInfo()
			info.Status = "online"
			info.IPv4 = live.IPv4
			info.IPv6 = live.IPv6
			info.Capabilities = live.Capabilities
			info.MaxConcurrency = live.MaxConcurrency
			info.ActiveTasks = live.ActiveTasks
			info.CPUPercent = live.CPUPercent
			info.MemPercent = live.MemPercent
			info.Version = live.Version
			info.LastSeenUnixMs = live.LastSeenUnixMs
			info.ConnectedAtMs = live.ConnectedAtMs
			info.Labels = live.Labels
		} else if rt, ok := h.lastRuntime[rec.ID]; ok {
			info.IPv4 = rt.IPv4
			info.IPv6 = rt.IPv6
			info.Capabilities = rt.Capabilities
			info.Version = rt.Version
			info.LastSeenUnixMs = rt.LastSeenUnixMs
		}
		nodes = append(nodes, info)
	}
	h.mu.RUnlock()

	for i := range nodes {
		nodes[i].QueueDepth = h.broker.QueueDepth(nodes[i].ID)
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

// OnlineCount returns the count of currently connected and enabled agents.
func (h *Hub) OnlineCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	count := 0
	for _, s := range h.sessions {
		if s.getInfo().Enabled {
			count++
		}
	}
	return count
}
