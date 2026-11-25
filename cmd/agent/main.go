package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/11dog/11dog/internal/agent"
	"github.com/11dog/11dog/internal/config"
	"gopkg.in/yaml.v3"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			fmt.Printf("11dog-agent %s\n", agent.Version)
			return
		case "init-config":
			data, _ := yaml.Marshal(config.DefaultAgentConfig())
			fmt.Print(string(data))
			return
		}
	}

	var (
		configPath     = flag.String("config", "", "path to agent YAML configuration")
		natsAddr       = flag.String("nats", "", "override NATS WebSocket URL (e.g. wss://nats.example.com)")
		credsFile      = flag.String("creds", "", "override path to NATS .creds file")
		maxConcurrency = flag.Uint("max-concurrency", 0, "override max concurrent probe tasks")
		tlsInsecure    = flag.Bool("insecure", false, "skip TLS certificate verification")
		showVer        = flag.Bool("version", false, "print agent version and exit")
	)
	flag.Parse()

	if *showVer {
		fmt.Printf("11dog-agent %s\n", agent.Version)
		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.LoadAgentConfig(*configPath)
	if err != nil {
		logger.Error("failed to load agent config", "error", err)
		os.Exit(1)
	}
	if *natsAddr != "" {
		cfg.NATSAddr = *natsAddr
	}
	if *credsFile != "" {
		cfg.CredsFile = *credsFile
	}
	if *maxConcurrency > 0 {
		cfg.MaxConcurrency = uint32(*maxConcurrency)
	}
	if *tlsInsecure {
		cfg.TLSInsecureSkipVerify = true
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	client := agent.NewClient(cfg, logger)
	if err := client.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("agent exited with error", "error", err)
		os.Exit(1)
	}
	logger.Info("11dog-agent stopped cleanly")
}
