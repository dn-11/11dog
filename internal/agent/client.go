package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dn-11/11dog/internal/agent/probe"
	"github.com/dn-11/11dog/internal/config"
	"github.com/dn-11/11dog/internal/model"
	"github.com/dn-11/11dog/internal/mq"
	"github.com/dn-11/11dog/internal/natsauth"
	"github.com/google/uuid"
	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	defaultStreamName = "DOG_TASKS"
	heartbeatInterval = 10 * time.Second
)

var builtinCapabilities = []model.ProbeType{
	model.ProbePing,
	model.ProbeTcping,
	model.ProbeHTTP,
	model.ProbeTraceroute,
	model.ProbeDNS,
}

// Version is set via ldflags at build time.
var Version = "dev"

var lastConnectedAtMs atomic.Int64

func nextConnectedAtMs() int64 {
	now := time.Now().UnixMilli()
	for {
		prev := lastConnectedAtMs.Load()
		next := now
		if next <= prev {
			next = prev + 1
		}
		if lastConnectedAtMs.CompareAndSwap(prev, next) {
			return next
		}
	}
}

// Client manages the direct NATS JetStream connection, edge concurrency guard,
// task execution, control commands, and heartbeat reporting.
type Client struct {
	cfg    *config.AgentConfig
	logger *slog.Logger

	sem         chan struct{}
	activeTasks atomic.Int32
	updating    atomic.Bool

	cancelMu sync.Mutex
	cancels  map[string]context.CancelFunc

	// Optional hooks for testing self-update without modifying the test binary.
	updateExecPathFn func() (string, error)
	updateRestartFn  func(exePath string) error
}

// NewClient creates a NATS-native edge agent client.
func NewClient(cfg *config.AgentConfig, logger *slog.Logger) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	maxConc := int(cfg.MaxConcurrency)
	if maxConc <= 0 {
		maxConc = 8
	}
	return &Client{
		cfg:     cfg,
		logger:  logger,
		sem:     make(chan struct{}, maxConc),
		cancels: make(map[string]context.CancelFunc),
	}
}

func (c *Client) loadCredsBytes() ([]byte, error) {
	if strings.TrimSpace(c.cfg.Creds) != "" {
		return []byte(c.cfg.Creds), nil
	}
	if strings.TrimSpace(c.cfg.CredsFile) == "" {
		return nil, errors.New("either creds_file or creds must be configured")
	}
	data, err := os.ReadFile(c.cfg.CredsFile)
	if err != nil {
		return nil, fmt.Errorf("read creds_file %q: %w", c.cfg.CredsFile, err)
	}
	return data, nil
}

func (c *Client) tlsConfig() (*tls.Config, error) {
	if c.cfg.TLSCAFile == "" && !c.cfg.TLSInsecureSkipVerify {
		return nil, nil
	}
	tlsCfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: c.cfg.TLSInsecureSkipVerify, //nolint:gosec
	}
	if c.cfg.TLSCAFile != "" {
		pemData, err := os.ReadFile(c.cfg.TLSCAFile)
		if err != nil {
			return nil, fmt.Errorf("read tls_ca_file %q: %w", c.cfg.TLSCAFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pemData) {
			return nil, fmt.Errorf("parse tls_ca_file %q: no valid PEM certificates", c.cfg.TLSCAFile)
		}
		tlsCfg.RootCAs = pool
	}
	return tlsCfg, nil
}

// Run connects to NATS over WebSocket using JWT/NKey .creds, starts heartbeats, control listeners,
// and the JetStream pull consumer until ctx is cancelled or tableflip completes a graceful upgrade.
func (c *Client) Run(ctx context.Context) error {
	upg, err := newProcessUpgrader(c.cfg.PIDFile)
	if err != nil {
		return fmt.Errorf("init tableflip upgrader: %w", err)
	}
	defer upg.Stop()

	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	go func() {
		select {
		case <-upg.Exit():
			c.logger.Info("tableflip upgrade ready; draining old agent process")
			runCancel()
		case <-runCtx.Done():
		}
	}()

	credsBytes, err := c.loadCredsBytes()
	if err != nil {
		return err
	}

	nodeID, err := natsauth.ExtractNodeIDFromCreds(credsBytes)
	if err != nil {
		return fmt.Errorf("extract node_id from creds: %w", err)
	}
	idStr := nodeID.String()

	tlsCfg, err := c.tlsConfig()
	if err != nil {
		return fmt.Errorf("load tls config: %w", err)
	}

	opts := []natsgo.Option{
		natsgo.Name("11dog-agent-" + idStr),
		natsgo.UserCredentialBytes(credsBytes),
		natsgo.MaxReconnects(-1),
		natsgo.ReconnectWait(2 * time.Second),
		natsgo.DisconnectErrHandler(func(_ *natsgo.Conn, err error) {
			c.logger.Warn("disconnected from NATS", "node_id", nodeID, "error", err)
		}),
		natsgo.ReconnectHandler(func(nc *natsgo.Conn) {
			c.logger.Info("reconnected to NATS", "node_id", nodeID, "url", nc.ConnectedUrl())
		}),
	}
	if tlsCfg != nil {
		opts = append(opts, natsgo.Secure(tlsCfg))
	}

	nc, err := natsgo.Connect(c.cfg.NATSAddr, opts...)
	if err != nil {
		return fmt.Errorf("connect nats %q: %w", c.cfg.NATSAddr, err)
	}
	defer func() {
		_ = nc.Drain()
		nc.Close()
	}()

	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("init jetstream: %w", err)
	}

	connectedAtMs := nextConnectedAtMs()
	c.logger.Info("connected to NATS with JWT/NKey credentials",
		"nats_addr", c.cfg.NATSAddr,
		"node_id", nodeID,
		"stream", defaultStreamName,
		"max_concurrency", cap(c.sem),
		"version", Version,
	)

	// Control subscriptions: cancel, update, reconnect.
	cancelSub, err := nc.Subscribe("ctrl.node."+idStr+".cancel", func(msg *natsgo.Msg) {
		var cmd model.CancelTaskCommand
		if err := json.Unmarshal(msg.Data, &cmd); err != nil {
			return
		}
		c.cancelMu.Lock()
		cancelFn := c.cancels[cmd.SubtaskID]
		c.cancelMu.Unlock()
		if cancelFn != nil {
			cancelFn()
		}
	})
	if err != nil {
		return fmt.Errorf("subscribe cancel ctrl: %w", err)
	}
	defer func() { _ = cancelSub.Unsubscribe() }()

	updateSub, err := nc.Subscribe("ctrl.node."+idStr+".update", func(msg *natsgo.Msg) {
		var cmd model.AgentUpdateCommand
		if err := json.Unmarshal(msg.Data, &cmd); err != nil {
			return
		}
		go c.handleAgentUpdate(runCtx, nodeID, connectedAtMs, &cmd, upg, func(st model.AgentUpdateState) {
			if payload, err := json.Marshal(st); err == nil {
				_ = nc.Publish("agent.update."+idStr, payload)
				_ = nc.Flush()
			}
		})
	})
	if err != nil {
		return fmt.Errorf("subscribe update ctrl: %w", err)
	}
	defer func() { _ = updateSub.Unsubscribe() }()

	reconnectSub, err := nc.Subscribe("ctrl.node."+idStr+".reconnect", func(msg *natsgo.Msg) {
		var cmd model.ReconnectCommand
		_ = json.Unmarshal(msg.Data, &cmd)
		c.logger.Info("received reconnect command from control plane", "node_id", nodeID, "reason", cmd.Reason)
		_ = nc.ForceReconnect()
	})
	if err != nil {
		return fmt.Errorf("subscribe reconnect ctrl: %w", err)
	}
	defer func() { _ = reconnectSub.Unsubscribe() }()

	// Heartbeat loop (publish immediately on startup, then every 10s).
	publishHB := func() {
		hb := model.AgentHeartbeat{
			NodeID:          nodeID,
			Capabilities:    builtinCapabilities,
			MaxConcurrency:  uint32(cap(c.sem)),
			ActiveTasks:     uint32(c.activeTasks.Load()),
			Version:         Version,
			OS:              runtime.GOOS,
			Arch:            runtime.GOARCH,
			TimestampUnixMs: time.Now().UnixMilli(),
			ConnectedAtMs:   connectedAtMs,
		}
		if payload, err := json.Marshal(hb); err == nil {
			_ = nc.Publish("agent.hb."+idStr, payload)
		}
	}
	publishHB()

	// Signal tableflip that this (potentially newly upgraded) process is connected and ready.
	if err := upg.Ready(); err != nil {
		return fmt.Errorf("tableflip ready: %w", err)
	}
	if upg.HasParent() {
		restartedState := model.AgentUpdateState{
			TargetVersion: Version,
			State:         "restarted",
			Message:       fmt.Sprintf("平滑重启完成，当前运行版本 %s", Version),
			UpdatedAtMs:   time.Now().UnixMilli(),
		}
		if payload, err := json.Marshal(restartedState); err == nil {
			_ = nc.Publish("agent.update."+idStr, payload)
		}
		_ = nc.Flush()
	}

	go func() {
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				publishHB()
			}
		}
	}()

	// JetStream Pull Consumer loop for this node's task subject.
	consumerName := "node_" + idStr
	filterSubj := "tasks.node." + idStr

	for {
		if runCtx.Err() != nil {
			return runCtx.Err()
		}

		consCtx, consCancel := context.WithTimeout(runCtx, 5*time.Second)
		consumer, err := js.CreateOrUpdateConsumer(consCtx, defaultStreamName, jetstream.ConsumerConfig{
			Durable:       consumerName,
			FilterSubject: filterSubj,
			AckPolicy:     jetstream.AckExplicitPolicy,
			AckWait:       90 * time.Second,
			MaxAckPending: cap(c.sem),
			DeliverPolicy: jetstream.DeliverAllPolicy,
		})
		consCancel()
		if err != nil {
			c.logger.Warn("waiting for JetStream task stream/consumer", "stream", defaultStreamName, "consumer", consumerName, "error", err)
			select {
			case <-runCtx.Done():
				return runCtx.Err()
			case <-time.After(2 * time.Second):
				continue
			}
		}

		if err := c.consumeLoop(runCtx, nc, consumer, nodeID); err != nil {
			if runCtx.Err() != nil {
				return runCtx.Err()
			}
			c.logger.Warn("JetStream consume loop interrupted; retrying", "error", err)
			select {
			case <-runCtx.Done():
				return runCtx.Err()
			case <-time.After(time.Second):
			}
		}
	}
}

func (c *Client) consumeLoop(ctx context.Context, nc *natsgo.Conn, consumer jetstream.Consumer, nodeID uuid.UUID) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		msgs, err := consumer.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, natsgo.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
				continue
			}
			return err
		}
		for msg := range msgs.Messages() {
			var subtask mq.QueuedSubtask
			if err := json.Unmarshal(msg.Data(), &subtask); err != nil {
				_ = msg.Term()
				continue
			}
			c.handleSubtask(ctx, nc, nodeID, &subtask, msg)
		}
	}
}

func publishTaskEvent(nc *natsgo.Conn, ev *model.TaskEvent) {
	if payload, err := json.Marshal(ev); err == nil {
		_ = nc.Publish("results.task."+ev.TaskID, payload)
	}
}

func (c *Client) handleSubtask(
	parentCtx context.Context,
	nc *natsgo.Conn,
	nodeID uuid.UUID,
	subtask *mq.QueuedSubtask,
	msg jetstream.Msg,
) {
	if !subtask.Deadline.IsZero() && time.Now().After(subtask.Deadline) {
		publishTaskEvent(nc, &model.TaskEvent{
			EventType: "result",
			TaskID:    subtask.TaskID,
			SubtaskID: subtask.SubtaskID,
			NodeID:    nodeID,
			Type:      subtask.Type,
			Status:    model.StatusExpired,
			Result: &model.NodeTaskResult{
				TaskID:         subtask.TaskID,
				SubtaskID:      subtask.SubtaskID,
				NodeID:         nodeID,
				Type:           subtask.Type,
				Status:         model.StatusExpired,
				ErrorMessage:   "task deadline expired before execution",
				FinishedUnixMs: time.Now().UnixMilli(),
			},
		})
		_ = msg.Ack()
		return
	}

	// Hard local overload guard: never exceed edge node's configured max_concurrency.
	select {
	case c.sem <- struct{}{}:
	default:
		publishTaskEvent(nc, &model.TaskEvent{
			EventType: "result",
			TaskID:    subtask.TaskID,
			SubtaskID: subtask.SubtaskID,
			NodeID:    nodeID,
			Type:      subtask.Type,
			Status:    model.StatusOverloaded,
			Result: &model.NodeTaskResult{
				TaskID:         subtask.TaskID,
				SubtaskID:      subtask.SubtaskID,
				NodeID:         nodeID,
				Type:           subtask.Type,
				Status:         model.StatusOverloaded,
				ErrorMessage:   "节点繁忙，请稍后再试",
				FinishedUnixMs: time.Now().UnixMilli(),
			},
		})
		_ = msg.Ack()
		return
	}

	publishTaskEvent(nc, &model.TaskEvent{
		EventType: "progress",
		TaskID:    subtask.TaskID,
		SubtaskID: subtask.SubtaskID,
		NodeID:    nodeID,
		Type:      subtask.Type,
		Status:    model.StatusRunning,
	})

	taskCtx, taskCancel := context.WithCancel(parentCtx)
	c.cancelMu.Lock()
	c.cancels[subtask.SubtaskID] = taskCancel
	c.cancelMu.Unlock()
	c.activeTasks.Add(1)

	go func() {
		defer func() {
			taskCancel()
			c.cancelMu.Lock()
			delete(c.cancels, subtask.SubtaskID)
			c.cancelMu.Unlock()
			c.activeTasks.Add(-1)
			<-c.sem
			_ = msg.Ack()
		}()

		onProgress := func(p *model.ProgressPayload) {
			if p == nil {
				return
			}
			publishTaskEvent(nc, &model.TaskEvent{
				EventType: "progress",
				TaskID:    subtask.TaskID,
				SubtaskID: subtask.SubtaskID,
				NodeID:    nodeID,
				Type:      subtask.Type,
				Status:    model.StatusRunning,
				Progress:  p,
			})
		}

		res := probe.Run(
			taskCtx,
			subtask.TaskID,
			subtask.SubtaskID,
			nodeID,
			subtask.Type,
			subtask.Target,
			subtask.TimeoutMs,
			subtask.Params,
			onProgress,
		)

		publishTaskEvent(nc, &model.TaskEvent{
			EventType: "result",
			TaskID:    subtask.TaskID,
			SubtaskID: subtask.SubtaskID,
			NodeID:    nodeID,
			Type:      subtask.Type,
			Status:    res.Status,
			Result:    res,
		})
	}()
}
