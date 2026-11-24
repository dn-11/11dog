package server_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/11dog/11dog/gen/agent/v1"
	"github.com/11dog/11dog/internal/agent"
	"github.com/11dog/11dog/internal/config"
	"github.com/11dog/11dog/internal/dn11"
	"github.com/11dog/11dog/internal/model"
	"github.com/11dog/11dog/internal/mq/memory"
	"github.com/11dog/11dog/internal/server"
	"github.com/11dog/11dog/internal/store"
	"google.golang.org/grpc"
)

type testStack struct {
	cfg     *config.ServerConfig
	store   *store.SQLStore
	broker  *memory.Broker
	hub     *server.Hub
	orch    *server.Orchestrator
	httpSrv *httptest.Server
	grpcSrv *grpc.Server
	grpcLis net.Listener
}

func startTestStack(t *testing.T, maxQueuePerNode int, dispatchRPS float64) *testStack {
	t.Helper()
	cfg := config.DefaultServerConfig()
	cfg.Auth.AdminToken = "test-admin-secret"
	cfg.MQ.MaxQueuePerNode = maxQueuePerNode
	cfg.MQ.TaskTTLSec = 30
	cfg.Limits.PerNodeDispatchRPS = dispatchRPS
	cfg.Limits.RateLimitRPM = 600
	cfg.Limits.RateLimitBurst = 100

	nodeStore, err := store.NewSQLStore("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	reg, err := dn11.NewRegistry(cfg.DN11Prefixes)
	if err != nil {
		t.Fatal(err)
	}
	broker := memory.NewBroker(cfg.MQ.MaxQueuePerNode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if testing.Verbose() {
		logger = slog.Default()
	}
	hub := server.NewHub(cfg, broker, nodeStore, reg, logger)
	orch, err := server.NewOrchestrator(cfg, broker, hub, reg)
	if err != nil {
		t.Fatal(err)
	}

	grpcLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcSrv := grpc.NewServer()
	agentv1.RegisterAgentHubServer(grpcSrv, hub)
	go func() {
		_ = grpcSrv.Serve(grpcLis)
	}()

	handler := server.NewHTTPHandler(cfg, hub, orch, nodeStore, server.NewIPRateLimiter(600, 100), logger)
	httpSrv := httptest.NewServer(handler)

	t.Cleanup(func() {
		hub.DrainAll("test done", 1)
		httpSrv.Close()
		grpcSrv.Stop()
		_ = broker.Close()
		_ = nodeStore.Close()
	})

	return &testStack{
		cfg:     cfg,
		store:   nodeStore,
		broker:  broker,
		hub:     hub,
		orch:    orch,
		httpSrv: httpSrv,
		grpcSrv: grpcSrv,
		grpcLis: grpcLis,
	}
}

func registerDBNodeAndStartAgent(
	t *testing.T,
	st *testStack,
	id, name, region, group string,
	asn, maxConc uint32,
	bogusLocalName string,
) *store.NodeRecord {
	t.Helper()
	rec, err := st.store.CreateNode(context.Background(), &store.NodeRecord{
		ID:             id,
		Name:           name,
		Region:         region,
		Group:          group,
		ASN:            asn,
		MaxConcurrency: maxConc,
		Enabled:        true,
	})
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.DefaultAgentConfig()
	cfg.ServerAddr = st.grpcLis.Addr().String()
	cfg.Token = rec.Token
	// Deliberately pass bogus local node metadata to ensure server uses DB authority.
	cfg.NodeID = "bogus-local-id"
	cfg.Name = bogusLocalName
	cfg.Region = "火星"
	cfg.MaxConcurrency = maxConc
	cfg.HeartbeatIntervalSec = 2

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	agentLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if testing.Verbose() {
		agentLogger = slog.Default()
	}
	c := agent.NewClient(cfg, agentLogger)
	go func() {
		_ = c.Run(ctx)
	}()
	return rec
}

func waitForOnlineNodes(t *testing.T, baseURL string, wantCount int) []model.PublicNodeInfo {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	var lastNodes []model.PublicNodeInfo
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/api/v1/nodes")
		if err != nil {
			lastErr = err
		} else {
			var body struct {
				Nodes []model.PublicNodeInfo `json:"nodes"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&body)
			_ = resp.Body.Close()
			lastNodes = body.Nodes
			if len(body.Nodes) >= wantCount {
				return body.Nodes
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d online nodes (got=%d, lastErr=%v)", wantCount, len(lastNodes), lastErr)
	return nil
}

func createTaskHTTP(t *testing.T, baseURL string, req model.CreateTaskRequest) model.TaskSnapshot {
	t.Helper()
	raw, _ := json.Marshal(req)
	resp, err := http.Post(baseURL+"/api/v1/tasks", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST /api/v1/tasks failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /api/v1/tasks status=%d body=%s", resp.StatusCode, b)
	}
	var snap model.TaskSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

func waitForSSEDone(t *testing.T, baseURL, taskID string) model.TaskSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/v1/tasks/"+taskID+"/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("SSE connect failed: %v", err)
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var curEvent string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			curEvent = strings.TrimPrefix(line, "event: ")
			continue
		}
		if strings.HasPrefix(line, "data: ") && curEvent == "done" {
			data := strings.TrimPrefix(line, "data: ")
			var snap model.TaskSnapshot
			if err := json.Unmarshal([]byte(data), &snap); err != nil {
				t.Fatalf("unmarshal done snapshot: %v", err)
			}
			return snap
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("SSE read error: %v", err)
	}
	t.Fatal("SSE stream ended without done event")
	return model.TaskSnapshot{}
}

func TestE2ETcpingAndHTTPViaReverseGRPCAndSSE(t *testing.T) {
	stack := startTestStack(t, 20, 50)

	registerDBNodeAndStartAgent(t, stack, "node-hk-1", "香港节点 01", "香港", "DN11-BGP", 4211110101, 4, "spoof-hk")
	registerDBNodeAndStartAgent(t, stack, "node-jp-1", "东京节点 01", "东京", "DN11-IX", 4211110202, 4, "spoof-jp")
	nodes := waitForOnlineNodes(t, stack.httpSrv.URL, 2)

	for _, n := range nodes {
		if n.Region == "火星" || n.ID == "bogus-local-id" {
			t.Fatalf("server accepted spoofed agent identity: %+v", n)
		}
	}

	// 1. Local TCP listener for TCPing verification.
	tcpLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcpLis.Close()
	go func() {
		for {
			c, err := tcpLis.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	_, portStr, _ := net.SplitHostPort(tcpLis.Addr().String())
	portNum, _ := strconv.Atoi(portStr)

	tcpSnap := createTaskHTTP(t, stack.httpSrv.URL, model.CreateTaskRequest{
		Type:   model.ProbeTcping,
		Target: "127.0.0.1",
		Params: model.ProbeParams{
			Tcping: &model.TcpingParams{
				Port:       uint32(portNum),
				Count:      2,
				IntervalMs: 100,
			},
		},
	})
	doneTCP := waitForSSEDone(t, stack.httpSrv.URL, tcpSnap.TaskID)
	if doneTCP.CompletedNodes != 2 {
		t.Fatalf("tcping CompletedNodes=%d, want 2", doneTCP.CompletedNodes)
	}
	for _, nodeID := range []string{"node-hk-1", "node-jp-1"} {
		res := doneTCP.Results[nodeID]
		if res == nil || res.Status != model.StatusCompleted || res.Tcping == nil {
			t.Fatalf("unexpected tcping result for %s: %+v", nodeID, res)
		}
		if res.Tcping.State != "open" || res.Tcping.Connected != 2 {
			t.Fatalf("node %s tcping = %+v, want open with 2 connected", nodeID, res.Tcping)
		}
	}

	// 2. Local HTTP server for HTTP probe verification.
	targetHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello dn11"))
	}))
	defer targetHTTP.Close()

	httpSnap := createTaskHTTP(t, stack.httpSrv.URL, model.CreateTaskRequest{
		Type:   model.ProbeHTTP,
		Target: targetHTTP.URL,
	})
	doneHTTP := waitForSSEDone(t, stack.httpSrv.URL, httpSnap.TaskID)
	if doneHTTP.CompletedNodes != 2 {
		t.Fatalf("http CompletedNodes=%d, want 2", doneHTTP.CompletedNodes)
	}
	for _, nodeID := range []string{"node-hk-1", "node-jp-1"} {
		res := doneHTTP.Results[nodeID]
		if res == nil || res.Status != model.StatusCompleted || res.HTTP == nil {
			t.Fatalf("unexpected http result for %s: %+v", nodeID, res)
		}
		if res.HTTP.StatusCode != 200 || res.HTTP.Timing == nil {
			t.Fatalf("node %s http = %+v, want status 200 with timing", nodeID, res.HTTP)
		}
	}
}

func TestE2EOverloadQueueBufferProtection(t *testing.T) {
	stack := startTestStack(t, 1, 100)
	registerDBNodeAndStartAgent(t, stack, "node-tiny", "边缘小节点", "测试区", "DN11-Edge", 4211110999, 1, "spoof")
	waitForOnlineNodes(t, stack.httpSrv.URL, 1)

	blockCh := make(chan struct{})
	defer close(blockCh)
	slowSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-blockCh:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer slowSrv.Close()

	sawOverload := false
	for i := 0; i < 8; i++ {
		snap := createTaskHTTP(t, stack.httpSrv.URL, model.CreateTaskRequest{
			Type:      model.ProbeHTTP,
			Target:    slowSrv.URL,
			TimeoutMs: 3000,
		})
		if snap.OverloadedNodes > 0 {
			sawOverload = true
			break
		}
		if res := snap.Results["node-tiny"]; res != nil && res.Status == model.StatusOverloaded {
			sawOverload = true
			break
		}
	}

	if !sawOverload {
		metricsResp, err := http.Get(stack.httpSrv.URL + "/metrics")
		if err == nil {
			b, _ := io.ReadAll(metricsResp.Body)
			_ = metricsResp.Body.Close()
			if strings.Contains(string(b), "dog_subtasks_overloaded_total ") &&
				!strings.Contains(string(b), "dog_subtasks_overloaded_total 0") {
				sawOverload = true
			}
		}
	}
	if !sawOverload {
		t.Fatal("expected MQ buffer overload protection to reject burst tasks with StatusOverloaded")
	}
}

func TestPublicAPISanitizationAndAdminAuth(t *testing.T) {
	stack := startTestStack(t, 20, 50)
	registerDBNodeAndStartAgent(t, stack, "node-pub-check", "检查节点", "华东", "DN11-IX", 4211110505, 8, "spoof")
	waitForOnlineNodes(t, stack.httpSrv.URL, 1)

	metaResp, err := http.Get(stack.httpSrv.URL + "/api/v1/meta")
	if err != nil {
		t.Fatal(err)
	}
	metaRaw, _ := io.ReadAll(metaResp.Body)
	_ = metaResp.Body.Close()
	for _, forbidden := range []string{"mq_driver", "max_queue_per_node", "version", "db_driver"} {
		if strings.Contains(string(metaRaw), forbidden) {
			t.Fatalf("public /api/v1/meta leaked internal field %q: %s", forbidden, metaRaw)
		}
	}

	nodesResp, err := http.Get(stack.httpSrv.URL + "/api/v1/nodes")
	if err != nil {
		t.Fatal(err)
	}
	nodesRaw, _ := io.ReadAll(nodesResp.Body)
	_ = nodesResp.Body.Close()
	for _, forbidden := range []string{"cpu_percent", "mem_percent", "queue_depth", "max_concurrency", "active_tasks", "ipv4", "version", "last_seen_unix_ms", "token"} {
		if strings.Contains(string(nodesRaw), forbidden) {
			t.Fatalf("public /api/v1/nodes leaked internal field %q: %s", forbidden, nodesRaw)
		}
	}

	for _, path := range []string{"/api/v1/admin/overview", "/api/v1/admin/nodes"} {
		unauthResp, err := http.Get(stack.httpSrv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = unauthResp.Body.Close()
		if unauthResp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("GET %s without token status=%d, want 401", path, unauthResp.StatusCode)
		}

		req, _ := http.NewRequest(http.MethodGet, stack.httpSrv.URL+path, nil)
		req.Header.Set("X-Admin-Token", "test-admin-secret")
		authResp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		authBody, _ := io.ReadAll(authResp.Body)
		_ = authResp.Body.Close()
		if authResp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s with token status=%d body=%s, want 200", path, authResp.StatusCode, authBody)
		}
		if path == "/api/v1/admin/overview" && !strings.Contains(string(authBody), `"db_driver":"sqlite"`) {
			t.Fatalf("admin overview missing db_driver: %s", authBody)
		}
		if path == "/api/v1/admin/nodes" && !strings.Contains(string(authBody), `"max_concurrency":8`) {
			t.Fatalf("admin nodes missing telemetry: %s", authBody)
		}
	}
}

func TestAuthoritativeNodeDBLifecycleAndInstallScript(t *testing.T) {
	stack := startTestStack(t, 20, 50)

	// 1. Create node via admin HTTP API.
	createReqBody := `{"id":"node-db-01","name":"数据库权威节点","region":"华南","group":"DN11-BGP","asn":4211110888,"max_concurrency":6}`
	req, _ := http.NewRequest(http.MethodPost, stack.httpSrv.URL+"/api/v1/admin/nodes", strings.NewReader(createReqBody))
	req.Header.Set("X-Admin-Token", "test-admin-secret")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var created model.NodeInfo
	_ = json.NewDecoder(resp.Body).Decode(&created)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || created.Token == "" {
		t.Fatalf("POST /api/v1/admin/nodes status=%d created=%+v", resp.StatusCode, created)
	}

	// 2. Verify hosted install script rendering & parameter validation.
	scriptResp, err := http.Get(stack.httpSrv.URL + "/scripts/install.sh?token=" + created.Token + "&grpc=10.0.0.1:9090")
	if err != nil {
		t.Fatal(err)
	}
	scriptBody, _ := io.ReadAll(scriptResp.Body)
	_ = scriptResp.Body.Close()
	if scriptResp.StatusCode != http.StatusOK ||
		!strings.Contains(string(scriptBody), created.Token) ||
		!strings.Contains(string(scriptBody), "CAP_NET_RAW") {
		t.Fatalf("unexpected hosted install script status=%d body=%s", scriptResp.StatusCode, scriptBody)
	}

	badScriptResp, err := http.Get(stack.httpSrv.URL + "/scripts/install.sh?token=bad;rm%20-rf")
	if err != nil {
		t.Fatal(err)
	}
	_ = badScriptResp.Body.Close()
	if badScriptResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 on invalid script token param, got %d", badScriptResp.StatusCode)
	}

	// 3. Connect agent with ONLY token & bogus local ID/Name; verify server enforces DB identity.
	agentCfg := config.DefaultAgentConfig()
	agentCfg.ServerAddr = stack.grpcLis.Addr().String()
	agentCfg.Token = created.Token
	agentCfg.NodeID = "evil-spoof-id"
	agentCfg.Name = "恶意伪造名称"
	agentCtx, agentCancel := context.WithCancel(context.Background())
	defer agentCancel()
	go func() {
		_ = agent.NewClient(agentCfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Run(agentCtx)
	}()

	online := waitForOnlineNodes(t, stack.httpSrv.URL, 1)
	if online[0].ID != "node-db-01" || online[0].Name != "数据库权威节点" {
		t.Fatalf("expected authoritative DB identity, got %+v", online[0])
	}

	// 4. Update node in DB via PUT and verify live reflection without reconnect.
	putReq, _ := http.NewRequest(
		http.MethodPut,
		stack.httpSrv.URL+"/api/v1/admin/nodes/node-db-01",
		strings.NewReader(`{"name":"已更新节点名","region":"西南"}`),
	)
	putReq.Header.Set("X-Admin-Token", "test-admin-secret")
	putReq.Header.Set("Content-Type", "application/json")
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT update status=%d, want 200", putResp.StatusCode)
	}

	afterPut := waitForOnlineNodes(t, stack.httpSrv.URL, 1)
	if afterPut[0].Name != "已更新节点名" || afterPut[0].Region != "西南" {
		t.Fatalf("expected live updated metadata, got %+v", afterPut[0])
	}

	// 5. Rotate token disconnects active session so public node goes offline.
	rotReq, _ := http.NewRequest(http.MethodPost, stack.httpSrv.URL+"/api/v1/admin/nodes/node-db-01/rotate-token", nil)
	rotReq.Header.Set("X-Admin-Token", "test-admin-secret")
	rotResp, err := http.DefaultClient.Do(rotReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = rotResp.Body.Close()
	if rotResp.StatusCode != http.StatusOK {
		t.Fatalf("rotate-token status=%d, want 200", rotResp.StatusCode)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if stack.hub.OnlineCount() == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if stack.hub.OnlineCount() != 0 {
		t.Fatalf("expected rotated token to disconnect session, online=%d", stack.hub.OnlineCount())
	}
}
