package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"time"

	agentv1 "github.com/11dog/11dog/gen/agent/v1"
	"github.com/11dog/11dog/internal/agent/probe"
	"github.com/11dog/11dog/internal/config"
	"github.com/11dog/11dog/internal/model"
	"github.com/11dog/11dog/internal/wsgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
)

// Version is set via ldflags at build time.
var Version = "dev"

// Client manages the reverse-gRPC stream to 11dog-server, edge concurrency guard,
// task execution, and heartbeat reporting.
type Client struct {
	cfg    *config.AgentConfig
	logger *slog.Logger

	sem         chan struct{}
	activeTasks atomic.Int32

	cancelMu sync.Mutex
	cancels  map[string]context.CancelFunc
}

// NewClient creates a reverse-gRPC edge agent client.
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

// Run maintains the reverse-gRPC connection with exponential backoff until ctx is cancelled.
func (c *Client) Run(ctx context.Context) error {
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		drainDelay, err := c.runOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}

		wait := backoff + time.Duration(rand.Intn(500))*time.Millisecond
		if drainDelay > 0 {
			wait = drainDelay
			backoff = time.Second
		} else {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}

		c.logger.Warn("gRPC session ended; reconnecting", "error", err, "retry_in", wait.String())
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

func (c *Client) wsTLSConfig() (*tls.Config, error) {
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

func (c *Client) runOnce(ctx context.Context) (time.Duration, error) {
	wsURL, err := wsgrpc.NormalizeWSURL(c.cfg.ServerAddr)
	if err != nil {
		return 0, fmt.Errorf("invalid server_addr: %w", err)
	}
	u, err := url.Parse(wsURL)
	if err != nil {
		return 0, fmt.Errorf("parse server_addr: %w", err)
	}
	var tlsCfg *tls.Config
	if u.Scheme == "wss" {
		tlsCfg, err = c.wsTLSConfig()
		if err != nil {
			return 0, fmt.Errorf("load ws tls config: %w", err)
		}
	}

	// TLS (wss://) is terminated by the WebSocket transport layer; gRPC runs plaintext inside the WS tunnel.
	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(dialCtx context.Context, _ string) (net.Conn, error) {
			return wsgrpc.DialContext(dialCtx, wsURL, tlsCfg)
		}),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                20 * time.Second,
			Timeout:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	}

	conn, err := grpc.NewClient("passthrough:///"+u.Host, dialOpts...)
	if err != nil {
		return 0, fmt.Errorf("grpc new client: %w", err)
	}
	defer conn.Close()

	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()

	if c.cfg.Token != "" {
		streamCtx = metadata.AppendToOutgoingContext(streamCtx, "x-agent-token", c.cfg.Token)
	}

	client := agentv1.NewAgentHubClient(conn)
	stream, err := client.Connect(streamCtx)
	if err != nil {
		return 0, fmt.Errorf("open reverse stream: %w", err)
	}

	var sendMu sync.Mutex
	sendMsg := func(msg *agentv1.AgentMessage) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(msg)
	}

	caps := make([]agentv1.ProbeType, 0, len(c.cfg.Capabilities))
	for _, capType := range c.cfg.Capabilities {
		if pt := model.ProbeTypeToProto(capType); pt != agentv1.ProbeType_PROBE_TYPE_UNSPECIFIED {
			caps = append(caps, pt)
		}
	}

	if err := sendMsg(&agentv1.AgentMessage{
		Payload: &agentv1.AgentMessage_Register{
			Register: &agentv1.RegisterRequest{
				NodeId:         c.cfg.NodeID,
				Name:           c.cfg.Name,
				Region:         c.cfg.Region,
				Group:          c.cfg.Group,
				Asn:            c.cfg.ASN,
				Ipv4:           c.cfg.IPv4,
				Ipv6:           c.cfg.IPv6,
				Capabilities:   caps,
				MaxConcurrency: c.cfg.MaxConcurrency,
				Version:        Version,
				Labels:         c.cfg.Labels,
			},
		},
	}); err != nil {
		return 0, fmt.Errorf("send register: %w", err)
	}

	firstResp, err := stream.Recv()
	if err != nil {
		return 0, fmt.Errorf("recv register response: %w", err)
	}
	regResp := firstResp.GetRegisterResp()
	if regResp == nil || !regResp.Accepted {
		msg := "rejected by server"
		if regResp != nil && regResp.Message != "" {
			msg = regResp.Message
		}
		return 0, errors.New(msg)
	}

	hbInterval := time.Duration(c.cfg.HeartbeatIntervalSec) * time.Second
	if regResp.HeartbeatIntervalSec > 0 {
		hbInterval = time.Duration(regResp.HeartbeatIntervalSec) * time.Second
	}
	if hbInterval < 2*time.Second {
		hbInterval = 10 * time.Second
	}

	assignedNodeID := regResp.AssignedNodeId
	if assignedNodeID == "" {
		assignedNodeID = c.cfg.NodeID
	}

	c.logger.Info("connected to 11dog-server via reverse gRPC over WebSocket",
		"server", wsURL,
		"node_id", assignedNodeID,
		"assigned_name", regResp.AssignedName,
		"max_concurrency", regResp.AssignedMaxConcurrency,
	)

	// Heartbeat loop
	go func() {
		ticker := time.NewTicker(hbInterval)
		defer ticker.Stop()
		for {
			select {
			case <-streamCtx.Done():
				return
			case <-ticker.C:
				cpuPct, memPct := readSysMetrics()
				_ = sendMsg(&agentv1.AgentMessage{
					Payload: &agentv1.AgentMessage_Heartbeat{
						Heartbeat: &agentv1.Heartbeat{
							NodeId:          assignedNodeID,
							TimestampUnixMs: time.Now().UnixMilli(),
							ActiveTasks:     uint32(c.activeTasks.Load()),
							CpuPercent:      cpuPct,
							MemPercent:      memPct,
						},
					},
				})
			}
		}
	}()

	for {
		srvMsg, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return 0, errors.New("server closed stream")
			}
			return 0, err
		}

		switch payload := srvMsg.Payload.(type) {
		case *agentv1.ServerMessage_DispatchTask:
			c.handleDispatch(streamCtx, assignedNodeID, payload.DispatchTask, sendMsg)

		case *agentv1.ServerMessage_CancelTask:
			if payload.CancelTask != nil {
				c.cancelMu.Lock()
				cancelFn := c.cancels[payload.CancelTask.SubtaskId]
				c.cancelMu.Unlock()
				if cancelFn != nil {
					cancelFn()
				}
			}

		case *agentv1.ServerMessage_ServerDrain:
			delay := 3 * time.Second
			if payload.ServerDrain != nil && payload.ServerDrain.ReconnectAfterSec > 0 {
				delay = time.Duration(payload.ServerDrain.ReconnectAfterSec) * time.Second
			}
			return delay, errors.New("server requested graceful drain")
		}
	}
}

func (c *Client) handleDispatch(
	streamCtx context.Context,
	assignedNodeID string,
	task *agentv1.DispatchTask,
	sendMsg func(*agentv1.AgentMessage) error,
) {
	if task == nil {
		return
	}
	if task.DeadlineUnixMs > 0 && time.Now().UnixMilli() > task.DeadlineUnixMs {
		_ = sendMsg(&agentv1.AgentMessage{
			Payload: &agentv1.AgentMessage_TaskResult{
				TaskResult: &agentv1.TaskResult{
					TaskId:         task.TaskId,
					SubtaskId:      task.SubtaskId,
					NodeId:         assignedNodeID,
					Type:           task.Type,
					Status:         agentv1.TaskStatus_TASK_STATUS_EXPIRED,
					ErrorMessage:   "task deadline expired before execution",
					FinishedUnixMs: time.Now().UnixMilli(),
				},
			},
		})
		return
	}

	// Hard local overload guard: never exceed edge node's configured max_concurrency.
	select {
	case c.sem <- struct{}{}:
	default:
		_ = sendMsg(&agentv1.AgentMessage{
			Payload: &agentv1.AgentMessage_TaskAck{
				TaskAck: &agentv1.TaskAck{
					TaskId:       task.TaskId,
					SubtaskId:    task.SubtaskId,
					Accepted:     false,
					RejectReason: "节点繁忙，请稍后再试",
				},
			},
		})
		return
	}

	_ = sendMsg(&agentv1.AgentMessage{
		Payload: &agentv1.AgentMessage_TaskAck{
			TaskAck: &agentv1.TaskAck{
				TaskId:    task.TaskId,
				SubtaskId: task.SubtaskId,
				Accepted:  true,
			},
		},
	})

	taskCtx, taskCancel := context.WithCancel(streamCtx)
	c.cancelMu.Lock()
	c.cancels[task.SubtaskId] = taskCancel
	c.cancelMu.Unlock()
	c.activeTasks.Add(1)

	go func() {
		defer func() {
			taskCancel()
			c.cancelMu.Lock()
			delete(c.cancels, task.SubtaskId)
			c.cancelMu.Unlock()
			c.activeTasks.Add(-1)
			<-c.sem
		}()

		probeType := model.ProbeTypeFromProto(task.Type)
		params := model.ProbeParamsFromProto(task.Params)

		onProgress := func(p *model.ProgressPayload) {
			if p == nil {
				return
			}
			pb := &agentv1.TaskProgress{
				TaskId:    task.TaskId,
				SubtaskId: task.SubtaskId,
				NodeId:    assignedNodeID,
				Type:      task.Type,
			}
			if p.PingPacket != nil {
				pb.Progress = &agentv1.TaskProgress_PingPacket{
					PingPacket: &agentv1.PingPacket{
						Seq:     p.PingPacket.Seq,
						RttMs:   p.PingPacket.RTTMs,
						Ttl:     p.PingPacket.TTL,
						Timeout: p.PingPacket.Timeout,
						ReplyIp: p.PingPacket.ReplyIP,
					},
				}
			} else if p.TracerouteHop != nil {
				pb.Progress = &agentv1.TaskProgress_TracerouteHop{
					TracerouteHop: &agentv1.TracerouteHop{
						Hop:         p.TracerouteHop.Hop,
						Ip:          p.TracerouteHop.IP,
						Hostname:    p.TracerouteHop.Hostname,
						RttsMs:      p.TracerouteHop.RTTsMs,
						AvgRttMs:    p.TracerouteHop.AvgRTTMs,
						LossPercent: p.TracerouteHop.LossPercent,
						Timeout:     p.TracerouteHop.Timeout,
					},
				}
			}
			_ = sendMsg(&agentv1.AgentMessage{
				Payload: &agentv1.AgentMessage_TaskProgress{
					TaskProgress: pb,
				},
			})
		}

		res := probe.Run(
			taskCtx,
			task.TaskId,
			task.SubtaskId,
			assignedNodeID,
			probeType,
			task.Target,
			task.TimeoutMs,
			params,
			onProgress,
		)

		_ = sendMsg(&agentv1.AgentMessage{
			Payload: &agentv1.AgentMessage_TaskResult{
				TaskResult: model.TaskResultToProto(res),
			},
		})
	}()
}
