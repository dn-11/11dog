package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/11dog/11dog/internal/config"
	"github.com/11dog/11dog/internal/dn11"
	"github.com/11dog/11dog/internal/model"
	"github.com/11dog/11dog/internal/mq"
	"github.com/11dog/11dog/internal/wsgrpc"
)

var (
	ErrInvalidRequest   = errors.New("invalid task request")
	ErrNoNodesAvailable = errors.New("no matching online nodes available")
	ErrTaskNotFound     = errors.New("task not found")
)

type taskRecord struct {
	mu       sync.Mutex
	snapshot *model.TaskSnapshot
	done     bool
	unsub    func()
}

// Orchestrator validates diagnostic tasks, dispatches them through the MQ buffer,
// aggregates live results into task snapshots, and exposes Prometheus metrics.
type Orchestrator struct {
	cfg     *config.ServerConfig
	broker  mq.Broker
	hub     *Hub
	dn11Reg *dn11.Registry

	allowedPrefixes []netip.Prefix
	deniedPrefixes  []netip.Prefix

	mu    sync.RWMutex
	tasks map[string]*taskRecord

	tasksCreatedTotal      atomic.Int64
	subtasksEnqueuedTotal  atomic.Int64
	subtasksOverloadTotal  atomic.Int64
	subtasksExpiredTotal   atomic.Int64
	subtasksCompletedTotal atomic.Int64
}

// NewOrchestrator constructs a task orchestrator wired to Hub and Broker.
func NewOrchestrator(cfg *config.ServerConfig, broker mq.Broker, hub *Hub, dn11Reg *dn11.Registry) (*Orchestrator, error) {
	o := &Orchestrator{
		cfg:     cfg,
		broker:  broker,
		hub:     hub,
		dn11Reg: dn11Reg,
		tasks:   make(map[string]*taskRecord),
	}
	for _, cidr := range cfg.Limits.AllowedCIDRs {
		p, err := netip.ParsePrefix(cidr)
		if err != nil {
			return nil, fmt.Errorf("invalid allowed_cidr %q: %w", cidr, err)
		}
		o.allowedPrefixes = append(o.allowedPrefixes, p)
	}
	for _, cidr := range cfg.Limits.DeniedCIDRs {
		p, err := netip.ParsePrefix(cidr)
		if err != nil {
			return nil, fmt.Errorf("invalid denied_cidr %q: %w", cidr, err)
		}
		o.deniedPrefixes = append(o.deniedPrefixes, p)
	}
	hub.SetMetricsCallbacks(HubMetricsCallbacks{
		OnExpired:   func() { o.subtasksExpiredTotal.Add(1) },
		OnCompleted: func() { o.subtasksCompletedTotal.Add(1) },
	})
	return o, nil
}

// StartReaper periodically prunes expired task snapshots from memory.
func (o *Orchestrator) StartReaper(ctx context.Context) {
	ttl := time.Duration(o.cfg.Limits.TaskHistoryTTLSec) * time.Second
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cutoffMs := time.Now().Add(-ttl).UnixMilli()
				o.mu.Lock()
				for id, rec := range o.tasks {
					rec.mu.Lock()
					expired := rec.done && rec.snapshot.CreatedUnixMs < cutoffMs
					unsub := rec.unsub
					rec.mu.Unlock()
					if expired {
						if unsub != nil {
							unsub()
						}
						delete(o.tasks, id)
					}
				}
				o.mu.Unlock()
			}
		}
	}()
}

func newID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (o *Orchestrator) validateAndNormalize(req *model.CreateTaskRequest) (uint32, error) {
	if !req.Type.IsValid() {
		return 0, fmt.Errorf("%w: unsupported probe type %q", ErrInvalidRequest, req.Type)
	}
	req.Target = strings.TrimSpace(req.Target)
	if req.Target == "" {
		return 0, fmt.Errorf("%w: target is required", ErrInvalidRequest)
	}
	if len(req.Target) > o.cfg.Limits.MaxTargetLen {
		return 0, fmt.Errorf("%w: target exceeds maximum length %d", ErrInvalidRequest, o.cfg.Limits.MaxTargetLen)
	}
	for _, r := range req.Target {
		if r < 0x20 || r == 0x7f {
			return 0, fmt.Errorf("%w: target contains invalid control characters", ErrInvalidRequest)
		}
	}

	timeoutMs := req.TimeoutMs
	if timeoutMs == 0 {
		timeoutMs = o.cfg.Limits.DefaultTimeoutMs
	}
	if timeoutMs > o.cfg.Limits.MaxTimeoutMs {
		timeoutMs = o.cfg.Limits.MaxTimeoutMs
	}

	switch req.Type {
	case model.ProbePing:
		if req.Params.Ping == nil {
			req.Params.Ping = &model.PingParams{}
		}
		if req.Params.Ping.Count == 0 {
			req.Params.Ping.Count = 4
		} else if req.Params.Ping.Count > 20 {
			req.Params.Ping.Count = 20
		}
		if req.Params.Ping.IntervalMs == 0 {
			req.Params.Ping.IntervalMs = 300
		} else if req.Params.Ping.IntervalMs < 100 {
			req.Params.Ping.IntervalMs = 100
		}
		if req.Params.Ping.PacketSize == 0 {
			req.Params.Ping.PacketSize = 56
		} else if req.Params.Ping.PacketSize > 1400 {
			req.Params.Ping.PacketSize = 1400
		}

	case model.ProbeTcping:
		if req.Params.Tcping == nil {
			req.Params.Tcping = &model.TcpingParams{}
		}
		if req.Params.Tcping.Port == 0 || req.Params.Tcping.Port > 65535 {
			return 0, fmt.Errorf("%w: tcping port must be between 1 and 65535", ErrInvalidRequest)
		}
		if req.Params.Tcping.Count == 0 {
			req.Params.Tcping.Count = 4
		} else if req.Params.Tcping.Count > 20 {
			req.Params.Tcping.Count = 20
		}
		if req.Params.Tcping.IntervalMs == 0 {
			req.Params.Tcping.IntervalMs = 300
		} else if req.Params.Tcping.IntervalMs < 100 {
			req.Params.Tcping.IntervalMs = 100
		}

	case model.ProbeHTTP:
		if !strings.HasPrefix(req.Target, "http://") && !strings.HasPrefix(req.Target, "https://") {
			req.Target = "http://" + req.Target
		}
		u, err := url.Parse(req.Target)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return 0, fmt.Errorf("%w: invalid http url %q", ErrInvalidRequest, req.Target)
		}
		if req.Params.HTTP == nil {
			req.Params.HTTP = &model.HTTPParams{FollowRedirects: true}
		}
		if req.Params.HTTP.Method == "" {
			req.Params.HTTP.Method = "GET"
		}
		req.Params.HTTP.Method = strings.ToUpper(req.Params.HTTP.Method)
		if req.Params.HTTP.Method != "GET" && req.Params.HTTP.Method != "HEAD" {
			return 0, fmt.Errorf("%w: http method must be GET or HEAD", ErrInvalidRequest)
		}

	case model.ProbeTraceroute:
		if req.Params.Traceroute == nil {
			req.Params.Traceroute = &model.TracerouteParams{}
		}
		if req.Params.Traceroute.MaxHops == 0 {
			req.Params.Traceroute.MaxHops = 30
		} else if req.Params.Traceroute.MaxHops > 64 {
			req.Params.Traceroute.MaxHops = 64
		}
		if req.Params.Traceroute.QueriesPerHop == 0 {
			req.Params.Traceroute.QueriesPerHop = 3
		} else if req.Params.Traceroute.QueriesPerHop > 5 {
			req.Params.Traceroute.QueriesPerHop = 5
		}
		if timeoutMs < 30000 && o.cfg.Limits.MaxTimeoutMs >= 30000 {
			timeoutMs = 30000
		}

	case model.ProbeDNS:
		if req.Params.DNS == nil {
			req.Params.DNS = &model.DNSParams{}
		}
		rt := strings.ToUpper(strings.TrimSpace(req.Params.DNS.RecordType))
		if rt == "" {
			rt = "A"
		}
		switch rt {
		case "A", "AAAA", "CNAME", "MX", "TXT", "NS", "PTR", "SOA":
		default:
			return 0, fmt.Errorf("%w: unsupported dns record type %q", ErrInvalidRequest, rt)
		}
		req.Params.DNS.RecordType = rt
	}

	// Enforce CIDR allow/deny rules when target is a literal IP.
	hostOnly := req.Target
	if u, err := url.Parse(req.Target); err == nil && u.Hostname() != "" {
		hostOnly = u.Hostname()
	}
	hostOnly = strings.Trim(hostOnly, "[]")
	if addr, err := netip.ParseAddr(hostOnly); err == nil {
		addr = addr.Unmap()
		for _, p := range o.deniedPrefixes {
			if p.Contains(addr) {
				return 0, fmt.Errorf("%w: target IP %s is in denied CIDR %s", ErrInvalidRequest, addr, p)
			}
		}
		if len(o.allowedPrefixes) > 0 {
			allowed := false
			for _, p := range o.allowedPrefixes {
				if p.Contains(addr) {
					allowed = true
					break
				}
			}
			if !allowed {
				return 0, fmt.Errorf("%w: target IP %s is outside allowed DN11 CIDRs", ErrInvalidRequest, addr)
			}
		}
	}

	return timeoutMs, nil
}

func (o *Orchestrator) selectTargetNodes(req model.CreateTaskRequest) []model.NodeInfo {
	allNodes := o.hub.ListNodes()
	nodeIDSet := make(map[string]struct{}, len(req.NodeIDs))
	for _, id := range req.NodeIDs {
		nodeIDSet[id] = struct{}{}
	}
	regionSet := make(map[string]struct{}, len(req.Regions))
	for _, r := range req.Regions {
		regionSet[r] = struct{}{}
	}
	groupSet := make(map[string]struct{}, len(req.Groups))
	for _, g := range req.Groups {
		groupSet[g] = struct{}{}
	}

	selected := make([]model.NodeInfo, 0, len(allNodes))
	for _, n := range allNodes {
		if !n.Enabled || n.Status != "online" {
			continue
		}
		if !nodeSupports(n.Capabilities, req.Type) {
			continue
		}
		if len(nodeIDSet) > 0 {
			if _, ok := nodeIDSet[n.ID]; !ok {
				continue
			}
		} else {
			if len(regionSet) > 0 {
				if _, ok := regionSet[n.Region]; !ok {
					continue
				}
			}
			if len(groupSet) > 0 {
				if _, ok := groupSet[n.Group]; !ok {
					continue
				}
			}
		}
		selected = append(selected, n)
	}
	return selected
}

func nodeSupports(caps []model.ProbeType, t model.ProbeType) bool {
	if len(caps) == 0 {
		return true
	}
	for _, c := range caps {
		if c == t {
			return true
		}
	}
	return false
}

// CreateTask creates a multi-node task and enqueues per-node subtasks into the MQ layer.
func (o *Orchestrator) CreateTask(ctx context.Context, req model.CreateTaskRequest) (*model.TaskSnapshot, error) {
	timeoutMs, err := o.validateAndNormalize(&req)
	if err != nil {
		return nil, err
	}

	nodes := o.selectTargetNodes(req)
	if len(nodes) == 0 {
		return nil, ErrNoNodesAvailable
	}

	taskID := newID()
	now := time.Now()
	queueTTL := time.Duration(o.cfg.MQ.TaskTTLSec) * time.Second
	if queueTTL < time.Duration(timeoutMs)*time.Millisecond+10*time.Second {
		queueTTL = time.Duration(timeoutMs)*time.Millisecond + 15*time.Second
	}
	deadline := now.Add(queueTTL)

	snapshot := &model.TaskSnapshot{
		TaskID:         taskID,
		Type:           req.Type,
		Target:         req.Target,
		Params:         req.Params,
		CreatedUnixMs:  now.UnixMilli(),
		DeadlineUnixMs: deadline.UnixMilli(),
		TotalNodes:     len(nodes),
		Results:        make(map[string]*model.NodeTaskResult, len(nodes)),
	}

	subtasks := make([]*mq.QueuedSubtask, 0, len(nodes))
	for _, n := range nodes {
		subtaskID := taskID + "_" + n.ID
		snapshot.Results[n.ID] = &model.NodeTaskResult{
			TaskID:    taskID,
			SubtaskID: subtaskID,
			NodeID:    n.ID,
			NodeName:  n.Name,
			Region:    n.Region,
			Group:     n.Group,
			NodeASN:   n.ASN,
			Type:      req.Type,
			Status:    model.StatusQueued,
		}
		subtasks = append(subtasks, &mq.QueuedSubtask{
			TaskID:    taskID,
			SubtaskID: subtaskID,
			NodeID:    n.ID,
			Type:      req.Type,
			Target:    req.Target,
			TimeoutMs: timeoutMs,
			Params:    req.Params,
			CreatedAt: now,
			Deadline:  deadline,
		})
	}

	ch, unsub, err := o.broker.SubscribeResult(context.Background(), taskID)
	if err != nil {
		return nil, fmt.Errorf("subscribe task results: %w", err)
	}

	rec := &taskRecord{
		snapshot: snapshot,
		unsub:    unsub,
	}
	o.mu.Lock()
	o.tasks[taskID] = rec
	o.mu.Unlock()
	o.tasksCreatedTotal.Add(1)

	go o.trackTaskEvents(rec, ch, deadline)

	// Enqueue per-node subtasks into MQ buffer.
	for _, st := range subtasks {
		if err := o.broker.EnqueueTask(ctx, st.NodeID, st); err != nil {
			status := model.StatusError
			errMsg := err.Error()
			if errors.Is(err, mq.ErrQueueFull) {
				status = model.StatusOverloaded
				errMsg = "节点繁忙，请稍后再试"
				o.subtasksOverloadTotal.Add(1)
			}
			n := snapshot.Results[st.NodeID]
			_ = o.broker.PublishResult(context.Background(), &model.TaskEvent{
				EventType: "result",
				TaskID:    taskID,
				SubtaskID: st.SubtaskID,
				NodeID:    st.NodeID,
				Type:      req.Type,
				Status:    status,
				Result: &model.NodeTaskResult{
					TaskID:         taskID,
					SubtaskID:      st.SubtaskID,
					NodeID:         st.NodeID,
					NodeName:       n.NodeName,
					Region:         n.Region,
					Group:          n.Group,
					NodeASN:        n.NodeASN,
					Type:           req.Type,
					Status:         status,
					ErrorMessage:   errMsg,
					FinishedUnixMs: time.Now().UnixMilli(),
				},
			})
			continue
		}
		o.subtasksEnqueuedTotal.Add(1)
	}

	return o.cloneSnapshot(rec), nil
}

func (o *Orchestrator) trackTaskEvents(rec *taskRecord, ch <-chan *model.TaskEvent, deadline time.Time) {
	waitDur := time.Until(deadline) + 10*time.Second
	if waitDur < 15*time.Second {
		waitDur = 15 * time.Second
	}
	timer := time.NewTimer(waitDur)
	defer timer.Stop()

	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if ev.EventType == "done" {
				return
			}
			allDone := o.applyEvent(rec, ev)
			if allDone {
				snap := o.cloneSnapshot(rec)
				_ = o.broker.PublishResult(context.Background(), &model.TaskEvent{
					EventType: "done",
					TaskID:    snap.TaskID,
					Snapshot:  snap,
				})
				rec.mu.Lock()
				rec.done = true
				unsub := rec.unsub
				rec.unsub = nil
				rec.mu.Unlock()
				if unsub != nil {
					unsub()
				}
				return
			}
		case <-timer.C:
			// Mark any still-unfinished nodes as timed out.
			nowMs := time.Now().UnixMilli()
			rec.mu.Lock()
			for _, res := range rec.snapshot.Results {
				if !res.Status.IsTerminal() {
					res.Status = model.StatusTimeout
					res.ErrorMessage = "任务全局等待超时"
					res.FinishedUnixMs = nowMs
				}
			}
			recomputeCounters(rec.snapshot)
			rec.done = true
			unsub := rec.unsub
			rec.unsub = nil
			rec.mu.Unlock()
			if unsub != nil {
				unsub()
			}
			snap := o.cloneSnapshot(rec)
			_ = o.broker.PublishResult(context.Background(), &model.TaskEvent{
				EventType: "done",
				TaskID:    snap.TaskID,
				Snapshot:  snap,
			})
			return
		}
	}
}

func (o *Orchestrator) applyEvent(rec *taskRecord, ev *model.TaskEvent) bool {
	rec.mu.Lock()
	defer rec.mu.Unlock()

	cur, ok := rec.snapshot.Results[ev.NodeID]
	if !ok {
		return false
	}

	switch ev.EventType {
	case "progress":
		if !cur.Status.IsTerminal() && ev.Status != "" {
			cur.Status = ev.Status
		}
		if ev.Progress != nil && ev.Progress.TracerouteHop != nil {
			if cur.Traceroute == nil {
				cur.Traceroute = &model.TracerouteResult{}
			}
			hop := *ev.Progress.TracerouteHop
			replaced := false
			for i := range cur.Traceroute.Hops {
				if cur.Traceroute.Hops[i].Hop == hop.Hop {
					cur.Traceroute.Hops[i] = hop
					replaced = true
					break
				}
			}
			if !replaced {
				cur.Traceroute.Hops = append(cur.Traceroute.Hops, hop)
				sort.Slice(cur.Traceroute.Hops, func(i, j int) bool {
					return cur.Traceroute.Hops[i].Hop < cur.Traceroute.Hops[j].Hop
				})
			}
		}
		if ev.Progress != nil && ev.Progress.PingPacket != nil {
			if cur.Ping == nil {
				cur.Ping = &model.PingResult{}
			}
			cur.Ping.Packets = append(cur.Ping.Packets, *ev.Progress.PingPacket)
		}

	case "result":
		if ev.Result != nil {
			copied := *ev.Result
			if copied.NodeName == "" {
				copied.NodeName = cur.NodeName
			}
			if copied.Region == "" {
				copied.Region = cur.Region
			}
			if copied.Group == "" {
				copied.Group = cur.Group
			}
			if copied.NodeASN == 0 {
				copied.NodeASN = cur.NodeASN
			}
			if copied.Type == "" {
				copied.Type = cur.Type
			}
			rec.snapshot.Results[ev.NodeID] = &copied
		} else {
			cur.Status = ev.Status
			cur.FinishedUnixMs = time.Now().UnixMilli()
		}
	}

	recomputeCounters(rec.snapshot)
	return rec.snapshot.CompletedNodes >= rec.snapshot.TotalNodes
}

func recomputeCounters(snap *model.TaskSnapshot) {
	completed := 0
	overloaded := 0
	for _, r := range snap.Results {
		if r.Status.IsTerminal() {
			completed++
		}
		if r.Status == model.StatusOverloaded {
			overloaded++
		}
	}
	snap.CompletedNodes = completed
	snap.OverloadedNodes = overloaded
}

func (o *Orchestrator) cloneSnapshot(rec *taskRecord) *model.TaskSnapshot {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	src := rec.snapshot
	cp := *src
	cp.Results = make(map[string]*model.NodeTaskResult, len(src.Results))
	for k, v := range src.Results {
		vCopy := *v
		cp.Results[k] = &vCopy
	}
	return &cp
}

// GetTask returns the current snapshot of taskID.
func (o *Orchestrator) GetTask(taskID string) (*model.TaskSnapshot, bool) {
	o.mu.RLock()
	rec, ok := o.tasks[taskID]
	o.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return o.cloneSnapshot(rec), true
}

// SubscribeTask atomically acquires the current snapshot and live event channel for SSE streaming.
func (o *Orchestrator) SubscribeTask(ctx context.Context, taskID string) (*model.TaskSnapshot, <-chan *model.TaskEvent, func(), error) {
	o.mu.RLock()
	rec, ok := o.tasks[taskID]
	o.mu.RUnlock()
	if !ok {
		return nil, nil, nil, ErrTaskNotFound
	}

	ch, unsub, err := o.broker.SubscribeResult(ctx, taskID)
	if err != nil {
		return nil, nil, nil, err
	}
	snap := o.cloneSnapshot(rec)
	return snap, ch, unsub, nil
}

// CancelTask attempts to cancel unfinished subtasks across online nodes.
func (o *Orchestrator) CancelTask(ctx context.Context, taskID string) (*model.TaskSnapshot, error) {
	o.mu.RLock()
	rec, ok := o.tasks[taskID]
	o.mu.RUnlock()
	if !ok {
		return nil, ErrTaskNotFound
	}

	rec.mu.Lock()
	rec.snapshot.Cancelled = true
	var pending []*model.NodeTaskResult
	for _, res := range rec.snapshot.Results {
		if !res.Status.IsTerminal() {
			resCopy := *res
			pending = append(pending, &resCopy)
		}
	}
	rec.mu.Unlock()

	for _, res := range pending {
		_ = o.hub.CancelTask(res.NodeID, taskID, res.SubtaskID, "cancelled by user")
		_ = o.broker.PublishResult(ctx, &model.TaskEvent{
			EventType: "result",
			TaskID:    taskID,
			SubtaskID: res.SubtaskID,
			NodeID:    res.NodeID,
			Type:      res.Type,
			Status:    model.StatusCancelled,
			Result: &model.NodeTaskResult{
				TaskID:         taskID,
				SubtaskID:      res.SubtaskID,
				NodeID:         res.NodeID,
				NodeName:       res.NodeName,
				Region:         res.Region,
				Group:          res.Group,
				NodeASN:        res.NodeASN,
				Type:           res.Type,
				Status:         model.StatusCancelled,
				ErrorMessage:   "用户已手动取消探测",
				FinishedUnixMs: time.Now().UnixMilli(),
			},
		})
	}
	return o.cloneSnapshot(rec), nil
}

// AdminOverview returns internal server state, MQ metrics, and node capacity telemetry.
func (o *Orchestrator) AdminOverview() model.AdminOverview {
	nodes := o.hub.ListNodes()
	var online, offline int
	var totalCap, active uint32
	var totalQueue int
	for _, n := range nodes {
		totalQueue += n.QueueDepth
		if n.Status == "online" {
			online++
			totalCap += n.MaxConcurrency
			active += n.ActiveTasks
		} else {
			offline++
		}
	}
	return model.AdminOverview{
		Version:                Version,
		DBDriver:               o.cfg.DB.Driver,
		MQDriver:               o.cfg.MQ.Driver,
		MaxQueuePerNode:        o.cfg.MQ.MaxQueuePerNode,
		WSPath:                 wsgrpc.DefaultPath,
		PublicWSAddr:           o.cfg.PublicWSAddr,
		AgentDownloadBaseURL:   o.cfg.AgentDownloadBaseURL,
		HostedBinaries:         ListHostedBinaries(o.cfg.AgentBinDir),
		OnlineNodes:            online,
		OfflineNodes:           offline,
		TotalNodes:             len(nodes),
		TotalCapacity:          totalCap,
		ActiveTasks:            active,
		TotalQueueDepth:        totalQueue,
		TasksCreatedTotal:      o.tasksCreatedTotal.Load(),
		SubtasksEnqueuedTotal:  o.subtasksEnqueuedTotal.Load(),
		SubtasksOverloadTotal:  o.subtasksOverloadTotal.Load(),
		SubtasksExpiredTotal:   o.subtasksExpiredTotal.Load(),
		SubtasksCompletedTotal: o.subtasksCompletedTotal.Load(),
	}
}

// MetricsText renders Prometheus exposition format metrics.
func (o *Orchestrator) MetricsText() string {
	nodes := o.hub.ListNodes()
	online := 0
	var b strings.Builder
	b.WriteString("# HELP dog_nodes_online Number of currently connected probe agents.\n")
	b.WriteString("# TYPE dog_nodes_online gauge\n")
	for _, n := range nodes {
		if n.Status == "online" {
			online++
		}
	}
	fmt.Fprintf(&b, "dog_nodes_online %d\n", online)
	b.WriteString("# HELP dog_nodes_total Total known probe agents.\n")
	b.WriteString("# TYPE dog_nodes_total gauge\n")
	fmt.Fprintf(&b, "dog_nodes_total %d\n", len(nodes))
	b.WriteString("# HELP dog_tasks_created_total Total tasks created.\n")
	b.WriteString("# TYPE dog_tasks_created_total counter\n")
	fmt.Fprintf(&b, "dog_tasks_created_total %d\n", o.tasksCreatedTotal.Load())
	b.WriteString("# HELP dog_subtasks_enqueued_total Total node subtasks enqueued.\n")
	b.WriteString("# TYPE dog_subtasks_enqueued_total counter\n")
	fmt.Fprintf(&b, "dog_subtasks_enqueued_total %d\n", o.subtasksEnqueuedTotal.Load())
	b.WriteString("# HELP dog_subtasks_overloaded_total Total subtasks rejected due to edge overload protection.\n")
	b.WriteString("# TYPE dog_subtasks_overloaded_total counter\n")
	fmt.Fprintf(&b, "dog_subtasks_overloaded_total %d\n", o.subtasksOverloadTotal.Load())
	b.WriteString("# HELP dog_subtasks_expired_total Total subtasks expired in buffer.\n")
	b.WriteString("# TYPE dog_subtasks_expired_total counter\n")
	fmt.Fprintf(&b, "dog_subtasks_expired_total %d\n", o.subtasksExpiredTotal.Load())
	b.WriteString("# HELP dog_subtasks_completed_total Total subtasks completed by agents.\n")
	b.WriteString("# TYPE dog_subtasks_completed_total counter\n")
	fmt.Fprintf(&b, "dog_subtasks_completed_total %d\n", o.subtasksCompletedTotal.Load())
	b.WriteString("# HELP dog_node_queue_depth Current buffered queue depth per node.\n")
	b.WriteString("# TYPE dog_node_queue_depth gauge\n")
	for _, n := range nodes {
		fmt.Fprintf(&b, "dog_node_queue_depth{node_id=%q,region=%q} %d\n", n.ID, n.Region, n.QueueDepth)
	}
	return b.String()
}
