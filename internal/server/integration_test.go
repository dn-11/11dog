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
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/11dog/11dog/internal/agent"
	"github.com/11dog/11dog/internal/config"
	"github.com/11dog/11dog/internal/dn11"
	"github.com/11dog/11dog/internal/model"
	natsmq "github.com/11dog/11dog/internal/mq/nats"
	"github.com/11dog/11dog/internal/natsauth"
	"github.com/11dog/11dog/internal/server"
	"github.com/11dog/11dog/internal/store"
	"github.com/nats-io/jwt/v2"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nkeys"
)

type testStack struct {
	cfg       *config.ServerConfig
	store     *store.SQLStore
	authMgr   *natsauth.Manager
	natsSrv   *natsserver.Server
	natsWSURL string
	broker    *natsmq.Broker
	hub       *server.Hub
	orch      *server.Orchestrator
	httpSrv   *httptest.Server
}

func startTestNATSServer(t *testing.T) (*natsserver.Server, string, string) {
	t.Helper()
	opKP, err := nkeys.CreateOperator()
	if err != nil {
		t.Fatal(err)
	}
	opPub, _ := opKP.PublicKey()

	sysKP, err := nkeys.CreateAccount()
	if err != nil {
		t.Fatal(err)
	}
	sysPub, _ := sysKP.PublicKey()
	sysClaims := jwt.NewAccountClaims(sysPub)
	sysClaims.Name = "SYS"
	sysJWT, err := sysClaims.Encode(opKP)
	if err != nil {
		t.Fatal(err)
	}

	opClaims := jwt.NewOperatorClaims(opPub)
	opClaims.Name = "11dog-test-operator"
	opClaims.SystemAccount = sysPub
	opJWT, err := opClaims.Encode(opKP)
	if err != nil {
		t.Fatal(err)
	}

	accKP, err := nkeys.CreateAccount()
	if err != nil {
		t.Fatal(err)
	}
	accPub, _ := accKP.PublicKey()
	accSeed, _ := accKP.Seed()
	accClaims := jwt.NewAccountClaims(accPub)
	accClaims.Name = "11dog"
	accClaims.Limits.JetStreamLimits = jwt.JetStreamLimits{
		MemoryStorage:        jwt.NoLimit,
		DiskStorage:          jwt.NoLimit,
		Streams:              jwt.NoLimit,
		Consumer:             jwt.NoLimit,
		MemoryMaxStreamBytes: jwt.NoLimit,
		DiskMaxStreamBytes:   jwt.NoLimit,
		MaxAckPending:        jwt.NoLimit,
	}
	accJWT, err := accClaims.Encode(opKP)
	if err != nil {
		t.Fatal(err)
	}

	// Pick a free local port for NATS WebSocket listener.
	wsLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	wsPort := wsLis.Addr().(*net.TCPAddr).Port
	_ = wsLis.Close()

	storeDir := t.TempDir()
	confPath := filepath.Join(t.TempDir(), "nats-server.conf")
	confContent := "host: 127.0.0.1\nport: -1\njetstream {\n  store_dir: " + strconv.Quote(storeDir) + "\n}\n" +
		"websocket {\n  host: 127.0.0.1\n  port: " + strconv.Itoa(wsPort) + "\n  no_tls: true\n}\n" +
		"operator: " + opJWT + "\n" +
		"system_account: " + sysPub + "\n" +
		"resolver: MEMORY\nresolver_preload: {\n  " + sysPub + ": " + sysJWT + "\n  " + accPub + ": " + accJWT + "\n}\n"
	if err := os.WriteFile(confPath, []byte(confContent), 0o600); err != nil {
		t.Fatal(err)
	}

	opts := &natsserver.Options{}
	if err := opts.ProcessConfigFile(confPath); err != nil {
		t.Fatalf("parse test nats-server.conf: %v\n%s", err, confContent)
	}
	ns, err := natsserver.NewServer(opts)
	if err != nil {
		t.Fatalf("create test nats server: %v", err)
	}
	if testing.Verbose() {
		ns.ConfigureLogger()
	}
	go ns.Start()
	if !ns.ReadyForConnections(5 * time.Second) {
		t.Fatal("test nats server did not become ready in time")
	}
	t.Cleanup(func() {
		ns.Shutdown()
	})
	return ns, "ws://127.0.0.1:" + strconv.Itoa(wsPort), string(accSeed)
}

func startTestStack(t *testing.T, maxQueuePerNode int) *testStack {
	t.Helper()
	cfg := config.DefaultServerConfig()
	cfg.Auth.AdminToken = "test-admin-secret"
	cfg.NATS.MaxQueuePerNode = maxQueuePerNode
	cfg.NATS.TaskTTLSec = 30
	cfg.Limits.RateLimitRPM = 600
	cfg.Limits.RateLimitBurst = 100

	ns, natsWSURL, accSeed := startTestNATSServer(t)
	cfg.NATS.URL = ns.ClientURL()
	cfg.NATS.PublicURL = natsWSURL
	cfg.NATS.AccountSeed = accSeed

	authMgr, err := natsauth.NewManager(accSeed, cfg.NATS.StreamName)
	if err != nil {
		t.Fatal(err)
	}

	nodeStore, err := store.NewSQLStore("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	reg, err := dn11.NewRegistry(cfg.DN11Prefixes)
	if err != nil {
		t.Fatal(err)
	}

	srvJWT, srvSeed, _, err := authMgr.IssueServerCreds()
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	broker, err := natsmq.NewBroker(ctx, natsmq.Options{
		URL:             cfg.NATS.URL,
		UserJWT:         srvJWT,
		UserSeed:        srvSeed,
		StreamName:      cfg.NATS.StreamName,
		MaxQueuePerNode: cfg.NATS.MaxQueuePerNode,
		TaskTTL:         time.Duration(cfg.NATS.TaskTTLSec) * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if testing.Verbose() {
		logger = slog.Default()
	}
	hub, err := server.NewHub(ctx, cfg, broker, nodeStore, authMgr, logger)
	if err != nil {
		t.Fatal(err)
	}
	orch, err := server.NewOrchestrator(cfg, broker, hub, reg)
	if err != nil {
		t.Fatal(err)
	}

	handler := server.NewHTTPHandler(cfg, hub, orch, nodeStore, server.NewIPRateLimiter(600, 100), logger)
	httpSrv := httptest.NewServer(handler)

	t.Cleanup(func() {
		httpSrv.Close()
		hub.Close()
		_ = broker.Close()
		_ = nodeStore.Close()
	})

	return &testStack{
		cfg:       cfg,
		store:     nodeStore,
		authMgr:   authMgr,
		natsSrv:   ns,
		natsWSURL: natsWSURL,
		broker:    broker,
		hub:       hub,
		orch:      orch,
		httpSrv:   httpSrv,
	}
}

func registerDBNodeAndStartAgent(
	t *testing.T,
	st *testStack,
	id, name, region, group string,
	asn, maxConc uint32,
) (*store.NodeRecord, *agent.Client) {
	t.Helper()
	creds, err := st.authMgr.IssueAgentCreds(id)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := st.store.CreateNode(context.Background(), &store.NodeRecord{
		ID:             id,
		Name:           name,
		Region:         region,
		Group:          group,
		ASN:            asn,
		MaxConcurrency: maxConc,
		Creds:          creds,
		Enabled:        true,
	})
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.DefaultAgentConfig()
	cfg.NATSAddr = st.natsWSURL
	cfg.Creds = creds
	cfg.CredsFile = ""
	cfg.PIDFile = ""
	cfg.MaxConcurrency = maxConc

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
	return rec, c
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

func TestE2ETcpingAndHTTPViaNativeNATSAndSSE(t *testing.T) {
	stack := startTestStack(t, 20)

	registerDBNodeAndStartAgent(t, stack, "node-hk-1", "香港节点 01", "香港", "DN11-BGP", 4211110101, 4)
	registerDBNodeAndStartAgent(t, stack, "node-jp-1", "东京节点 01", "东京", "DN11-IX", 4211110202, 4)
	nodes := waitForOnlineNodes(t, stack.httpSrv.URL, 2)

	for _, n := range nodes {
		if n.ID == "bogus-local-id" {
			t.Fatalf("agent used spoofed local node_id instead of JWT name: %+v", n)
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
		_, _ = w.Write([]byte("hello dn11 via nats"))
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

func TestPublicAPISanitizationAndAdminAuthAndNATSConfig(t *testing.T) {
	stack := startTestStack(t, 20)
	registerDBNodeAndStartAgent(t, stack, "node-pub-check", "检查节点", "华东", "DN11-IX", 4211110505, 8)
	waitForOnlineNodes(t, stack.httpSrv.URL, 1)

	metaResp, err := http.Get(stack.httpSrv.URL + "/api/v1/meta")
	if err != nil {
		t.Fatal(err)
	}
	metaRaw, _ := io.ReadAll(metaResp.Body)
	_ = metaResp.Body.Close()
	for _, forbidden := range []string{"nats_url", "max_queue_per_node", "version", "db_driver"} {
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
	for _, forbidden := range []string{"cpu_percent", "mem_percent", "queue_depth", "max_concurrency", "active_tasks", "ipv4", "version", "last_seen_unix_ms", "token", "creds"} {
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
	}
}

func TestAuthoritativeNodeCredsAndInstallScript(t *testing.T) {
	stack := startTestStack(t, 20)

	// 1. Create node via admin HTTP API (auto-mints .creds).
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
	if resp.StatusCode != http.StatusCreated || created.Token == "" || !strings.Contains(created.Creds, "BEGIN NATS USER JWT") {
		t.Fatalf("POST /api/v1/admin/nodes status=%d created=%+v", resp.StatusCode, created)
	}

	// 2. Verify hosted install script embeds .creds and NATS WebSocket URL.
	scriptResp, err := http.Get(stack.httpSrv.URL + "/scripts/install.sh?token=" + created.Token + "&nats=wss://nats.dn11.example")
	if err != nil {
		t.Fatal(err)
	}
	scriptBody, _ := io.ReadAll(scriptResp.Body)
	_ = scriptResp.Body.Close()
	if scriptResp.StatusCode != http.StatusOK ||
		!strings.Contains(string(scriptBody), "BEGIN NATS USER JWT") ||
		!strings.Contains(string(scriptBody), "wss://nats.dn11.example") ||
		!strings.Contains(string(scriptBody), "ReadWritePaths=/opt/11dog") {
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
}

func TestAdminTargetPolicyEnforcement(t *testing.T) {
	stack := startTestStack(t, 20)
	registerDBNodeAndStartAgent(t, stack, "node-pol-1", "策略测试节点", "华东", "DN11-BGP", 4211110401, 4)
	waitForOnlineNodes(t, stack.httpSrv.URL, 1)

	policyPayload := `{"enabled":true,"allow_rules":["127.0.0.0/8","*.allowed.dn11"],"deny_rules":["blocked.allowed.dn11"]}`
	putReq, _ := http.NewRequest(http.MethodPut, stack.httpSrv.URL+"/api/v1/admin/target-policy", strings.NewReader(policyPayload))
	putReq.Header.Set("X-Admin-Token", "test-admin-secret")
	putReq.Header.Set("Content-Type", "application/json")
	putResp, err := http.DefaultClient.Do(putReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = putResp.Body.Close()
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT target-policy status=%d, want 200", putResp.StatusCode)
	}

	for _, badTarget := range []string{"1.1.1.1", "blocked.allowed.dn11", "evil.example.com"} {
		raw, _ := json.Marshal(model.CreateTaskRequest{
			Type:   model.ProbePing,
			Target: badTarget,
		})
		resp, err := http.Post(stack.httpSrv.URL+"/api/v1/tasks", "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("expected 400 for restricted target %q, got %d", badTarget, resp.StatusCode)
		}
	}

	rawOK, _ := json.Marshal(model.CreateTaskRequest{
		Type:   model.ProbeDNS,
		Target: "ok.allowed.dn11",
	})
	respOK, err := http.Post(stack.httpSrv.URL+"/api/v1/tasks", "application/json", bytes.NewReader(rawOK))
	if err != nil {
		t.Fatal(err)
	}
	_ = respOK.Body.Close()
	if respOK.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 for allowed wildcard target, got %d", respOK.StatusCode)
	}
}

func TestAgentSelfUpdateDispatchAndApplyOverNATS(t *testing.T) {
	stack := startTestStack(t, 20)

	binDir := t.TempDir()
	stack.cfg.AgentBinDir = binDir
	hostedFilename := server.AgentBinaryFilename(runtime.GOOS, runtime.GOARCH)
	newBinaryPayload := []byte("fake-new-11dog-agent-binary-v2.0.0-nats")
	if err := os.WriteFile(filepath.Join(binDir, hostedFilename), newBinaryPayload, 0o755); err != nil {
		t.Fatal(err)
	}

	agentDir := t.TempDir()
	fakeExePath := filepath.Join(agentDir, "11dog-agent")
	if runtime.GOOS == "windows" {
		fakeExePath += ".exe"
	}
	if err := os.WriteFile(fakeExePath, []byte("old-agent-binary-v1.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, client := registerDBNodeAndStartAgent(t, stack, "node-upd-1", "自更新测试节点", "香港", "DN11-BGP", 4211110777, 4)

	var restarted atomic.Bool
	client.SetUpdateHooks(
		func() (string, error) { return fakeExePath, nil },
		func(_ string) error {
			restarted.Store(true)
			return nil
		},
	)

	waitForOnlineNodes(t, stack.httpSrv.URL, 1)

	updReq, _ := http.NewRequest(
		http.MethodPost,
		stack.httpSrv.URL+"/api/v1/admin/nodes/node-upd-1/update",
		strings.NewReader(`{"version":"2.0.0","force":true}`),
	)
	updReq.Header.Set("X-Admin-Token", "test-admin-secret")
	updReq.Header.Set("Content-Type", "application/json")
	updResp, err := http.DefaultClient.Do(updReq)
	if err != nil {
		t.Fatal(err)
	}
	updBody, _ := io.ReadAll(updResp.Body)
	_ = updResp.Body.Close()
	if updResp.StatusCode != http.StatusOK {
		t.Fatalf("POST update status=%d body=%s, want 200", updResp.StatusCode, updBody)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if restarted.Load() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !restarted.Load() {
		t.Fatal("timed out waiting for agent self-update restart hook")
	}

	replacedBytes, err := os.ReadFile(fakeExePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(replacedBytes, newBinaryPayload) {
		t.Fatalf("replaced binary content = %q, want %q", string(replacedBytes), string(newBinaryPayload))
	}

	// Verify admin node list reflects the reported update status from NATS KV.
	deadlineStatus := time.Now().Add(3 * time.Second)
	var lastNodes []model.NodeInfo
	for time.Now().Before(deadlineStatus) {
		nodesReq, _ := http.NewRequest(http.MethodGet, stack.httpSrv.URL+"/api/v1/admin/nodes", nil)
		nodesReq.Header.Set("X-Admin-Token", "test-admin-secret")
		nodesResp, err := http.DefaultClient.Do(nodesReq)
		if err == nil {
			var nodesBody struct {
				Nodes []model.NodeInfo `json:"nodes"`
			}
			_ = json.NewDecoder(nodesResp.Body).Decode(&nodesBody)
			_ = nodesResp.Body.Close()
			lastNodes = nodesBody.Nodes
			if len(lastNodes) == 1 && lastNodes[0].UpdateStatus != nil && lastNodes[0].UpdateStatus.State == "restarting" {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("expected node update_status=restarting in NATS KV, got %+v", lastNodes)
}
