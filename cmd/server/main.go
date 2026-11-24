package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	agentv1 "github.com/11dog/11dog/gen/agent/v1"
	"github.com/11dog/11dog/internal/config"
	"github.com/11dog/11dog/internal/dn11"
	"github.com/11dog/11dog/internal/mq"
	"github.com/11dog/11dog/internal/mq/memory"
	natsmq "github.com/11dog/11dog/internal/mq/nats"
	"github.com/11dog/11dog/internal/server"
	"github.com/11dog/11dog/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
)

func main() {
	var (
		configPath = flag.String("config", "", "path to server YAML configuration")
		httpAddr   = flag.String("http-listen", "", "override HTTP listen address (e.g. :8080)")
		grpcAddr   = flag.String("grpc-listen", "", "override gRPC listen address (e.g. :9090)")
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
	if *grpcAddr != "" {
		cfg.GRPCListenAddr = *grpcAddr
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
	logger.Info("initialized node database", "driver", cfg.DB.Driver)

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

	var broker mq.Broker
	switch cfg.MQ.Driver {
	case "memory":
		broker = memory.NewBroker(cfg.MQ.MaxQueuePerNode)
		logger.Info("initialized in-memory MQ broker", "max_queue_per_node", cfg.MQ.MaxQueuePerNode)
	case "nats":
		for attempt := 1; attempt <= 15; attempt++ {
			initCtx, initCancel := context.WithTimeout(rootCtx, 5*time.Second)
			broker, err = natsmq.NewBroker(
				initCtx,
				cfg.MQ.NATSURL,
				cfg.MQ.StreamName,
				cfg.MQ.MaxQueuePerNode,
				time.Duration(cfg.MQ.TaskTTLSec)*time.Second,
			)
			initCancel()
			if err == nil {
				break
			}
			logger.Warn("waiting for NATS JetStream broker...", "attempt", attempt, "error", err)
			select {
			case <-rootCtx.Done():
				os.Exit(1)
			case <-time.After(2 * time.Second):
			}
		}
		if err != nil {
			logger.Error("failed to initialize NATS JetStream broker", "error", err)
			os.Exit(1)
		}
		logger.Info("initialized NATS JetStream MQ broker",
			"nats_url", cfg.MQ.NATSURL,
			"stream", cfg.MQ.StreamName,
			"max_queue_per_node", cfg.MQ.MaxQueuePerNode,
		)
	default:
		logger.Error("unknown mq.driver", "driver", cfg.MQ.Driver)
		os.Exit(1)
	}
	defer func() { _ = broker.Close() }()

	hub := server.NewHub(cfg, broker, nodeStore, dn11Reg, logger)
	orch, err := server.NewOrchestrator(cfg, broker, hub, dn11Reg)
	if err != nil {
		logger.Error("failed to initialize orchestrator", "error", err)
		os.Exit(1)
	}
	orch.StartReaper(rootCtx)

	limiter := server.NewIPRateLimiter(cfg.Limits.RateLimitRPM, cfg.Limits.RateLimitBurst)
	limiter.StartCleanup(rootCtx)

	grpcOpts := []grpc.ServerOption{
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second,
			PermitWithoutStream: true,
		}),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    25 * time.Second,
			Timeout: 10 * time.Second,
		}),
	}
	if cfg.TLS.Enabled {
		creds, err := credentials.NewServerTLSFromFile(cfg.TLS.CertFile, cfg.TLS.KeyFile)
		if err != nil {
			logger.Error("failed to load gRPC TLS credentials", "error", err)
			os.Exit(1)
		}
		grpcOpts = append(grpcOpts, grpc.Creds(creds))
	}

	grpcServer := grpc.NewServer(grpcOpts...)
	agentv1.RegisterAgentHubServer(grpcServer, hub)

	grpcLis, err := net.Listen("tcp", cfg.GRPCListenAddr)
	if err != nil {
		logger.Error("failed to listen gRPC", "addr", cfg.GRPCListenAddr, "error", err)
		os.Exit(1)
	}

	httpHandler := server.NewHTTPHandler(cfg, hub, orch, nodeStore, limiter, logger)
	httpSrv := &http.Server{
		Addr:              cfg.HTTPListenAddr,
		Handler:           httpHandler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		logger.Info("starting reverse-gRPC hub", "addr", cfg.GRPCListenAddr, "tls", cfg.TLS.Enabled)
		if err := grpcServer.Serve(grpcLis); err != nil {
			logger.Error("gRPC server exited", "error", err)
		}
	}()

	go func() {
		logger.Info("starting HTTP API & SPA server", "addr", cfg.HTTPListenAddr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server exited", "error", err)
			cancel()
		}
	}()

	<-rootCtx.Done()
	logger.Info("shutting down 11dog-server...")

	hub.DrainAll("server shutting down", 5)

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	_ = httpSrv.Shutdown(shutCtx)
	grpcServer.GracefulStop()
	logger.Info("11dog-server stopped")
}
