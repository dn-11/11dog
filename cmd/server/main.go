package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dn-11/11dog/internal/config"
	"github.com/dn-11/11dog/internal/dn11"
	natsmq "github.com/dn-11/11dog/internal/mq/nats"
	"github.com/dn-11/11dog/internal/natsauth"
	"github.com/dn-11/11dog/internal/server"
	"github.com/dn-11/11dog/internal/store"
)

func main() {
	var (
		configPath = flag.String("config", "", "path to server YAML configuration")
		httpAddr   = flag.String("http-listen", "", "override HTTP listen address (e.g. :8080)")
		staticDir  = flag.String("static-dir", "", "optional filesystem directory for Vue SPA assets")
		showVer    = flag.Bool("version", false, "print server version and exit")
	)
	flag.Parse()

	if *showVer {
		fmt.Printf("11dog-server %s\n", server.Version)
		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.LoadServerConfig(*configPath)
	if err != nil {
		logger.Error("failed to load server config", "error", err)
		os.Exit(1)
	}
	if *httpAddr != "" {
		cfg.HTTPListenAddr = *httpAddr
	}
	if *staticDir != "" {
		cfg.StaticDir = *staticDir
	}

	nodeStore, err := store.NewSQLStore(cfg.DB.Driver, cfg.DB.DSN)
	if err != nil {
		logger.Error("failed to initialize node database", "driver", cfg.DB.Driver, "error", err)
		os.Exit(1)
	}
	defer func() { _ = nodeStore.Close() }()

	authMgr, err := natsauth.NewManager(cfg.NATS.AccountSeed, cfg.NATS.StreamName)
	if err != nil {
		logger.Error("failed to initialize NATS account signer (ensure nats.account_seed / DOG_NATS_ACCOUNT_SEED is configured)", "error", err)
		os.Exit(1)
	}

	logger.Info("initialized node database & NATS account signer",
		"driver", cfg.DB.Driver,
		"account_pub", authMgr.AccountPub(),
	)

	if strings.TrimSpace(cfg.Auth.AdminToken) == "" {
		logger.Warn("auth.admin_token is empty; /admin APIs will return 503 until configured")
	}

	dn11Reg, err := dn11.NewRegistry(cfg.DN11Prefixes)
	if err != nil {
		logger.Error("failed to initialize DN11 prefix registry", "error", err)
		os.Exit(1)
	}

	rootCtx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if cfg.DN11Registry.Enabled {
		dn11Reg.StartSync(rootCtx, dn11.SyncOptions{
			URL:      cfg.DN11Registry.URL,
			Interval: time.Duration(cfg.DN11Registry.RefreshIntervalSec) * time.Second,
			Logger:   logger,
		})
	}

	srvJWT, srvSeed, _, err := authMgr.IssueServerCreds()
	if err != nil {
		logger.Error("failed to mint server NATS credentials", "error", err)
		os.Exit(1)
	}

	tlsCfg, err := buildNATSTLSConfig(cfg.NATS.TLSCAFile, cfg.NATS.TLSInsecureSkipVerify)
	if err != nil {
		logger.Error("failed to load NATS TLS config", "error", err)
		os.Exit(1)
	}

	var broker *natsmq.Broker
	for attempt := 1; attempt <= 15; attempt++ {
		initCtx, initCancel := context.WithTimeout(rootCtx, 5*time.Second)
		broker, err = natsmq.NewBroker(initCtx, natsmq.Options{
			URL:             cfg.NATS.URL,
			UserJWT:         srvJWT,
			UserSeed:        srvSeed,
			TLSConfig:       tlsCfg,
			StreamName:      cfg.NATS.StreamName,
			MaxQueuePerNode: cfg.NATS.MaxQueuePerNode,
			TaskTTL:         time.Duration(cfg.NATS.TaskTTLSec) * time.Second,
		})
		initCancel()
		if err == nil {
			break
		}
		logger.Warn("waiting for external NATS JetStream server...", "attempt", attempt, "nats_url", cfg.NATS.URL, "error", err)
		select {
		case <-rootCtx.Done():
			os.Exit(1)
		case <-time.After(2 * time.Second):
		}
	}
	if err != nil {
		logger.Error("failed to connect to external NATS JetStream", "nats_url", cfg.NATS.URL, "error", err)
		os.Exit(1)
	}
	defer func() { _ = broker.Close() }()
	logger.Info("connected to external NATS JetStream",
		"nats_url", cfg.NATS.URL,
		"stream", cfg.NATS.StreamName,
		"max_queue_per_node", cfg.NATS.MaxQueuePerNode,
	)

	hub, err := server.NewHub(rootCtx, cfg, broker, nodeStore, authMgr, logger)
	if err != nil {
		logger.Error("failed to initialize NATS hub", "error", err)
		os.Exit(1)
	}
	defer hub.Close()

	if err := hub.EnsureNodeCredsBackfilled(rootCtx); err != nil {
		logger.Warn("failed to backfill missing node .creds", "error", err)
	}

	orch, err := server.NewOrchestrator(cfg, broker, hub, dn11Reg)
	if err != nil {
		logger.Error("failed to initialize orchestrator", "error", err)
		os.Exit(1)
	}
	orch.StartReaper(rootCtx)

	limiter := server.NewIPRateLimiter(cfg.Limits.RateLimitRPM, cfg.Limits.RateLimitBurst)
	limiter.StartCleanup(rootCtx)

	httpHandler := server.NewHTTPHandler(cfg, hub, orch, nodeStore, limiter, logger)
	httpSrv := &http.Server{
		Addr:              cfg.HTTPListenAddr,
		Handler:           httpHandler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("starting HTTP API & embedded SPA server", "addr", cfg.HTTPListenAddr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server exited", "error", err)
			cancel()
		}
	}()

	<-rootCtx.Done()
	logger.Info("shutting down 11dog-server...")

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	_ = httpSrv.Shutdown(shutCtx)
	logger.Info("11dog-server stopped")
}

func buildNATSTLSConfig(caFile string, insecureSkip bool) (*tls.Config, error) {
	if caFile == "" && !insecureSkip {
		return nil, nil
	}
	tlsCfg := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: insecureSkip, //nolint:gosec
	}
	if caFile != "" {
		pemData, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read tls_ca_file %q: %w", caFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pemData) {
			return nil, fmt.Errorf("parse tls_ca_file %q: no valid PEM certificates", caFile)
		}
		tlsCfg.RootCAs = pool
	}
	return tlsCfg, nil
}
