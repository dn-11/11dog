package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// AgentConfig holds minimal configuration for an edge probe agent connecting to NATS over WebSocket.
type AgentConfig struct {
	NATSAddr              string `yaml:"nats_addr"`
	CredsFile             string `yaml:"creds_file"`
	Creds                 string `yaml:"-"`
	PIDFile               string `yaml:"pid_file,omitempty"`
	MaxConcurrency        uint32 `yaml:"max_concurrency,omitempty"`
	TLSCAFile             string `yaml:"tls_ca_file,omitempty"`
	TLSInsecureSkipVerify bool   `yaml:"tls_insecure_skip_verify,omitempty"`
}

// DefaultAgentConfig returns minimal defaults for an edge probe agent.
func DefaultAgentConfig() *AgentConfig {
	return &AgentConfig{
		NATSAddr:       "ws://127.0.0.1:8443",
		CredsFile:      "/etc/11dog/agent.creds",
		PIDFile:        "/opt/11dog/11dog-agent.pid",
		MaxConcurrency: 8,
	}
}

// LoadAgentConfig loads agent YAML config from path (if non-empty) and applies env overrides.
func LoadAgentConfig(path string) (*AgentConfig, error) {
	cfg := DefaultAgentConfig()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read agent config %q: %w", path, err)
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse agent config %q: %w", path, err)
		}
	}
	applyAgentEnvOverrides(cfg)
	if cfg.NATSAddr == "" {
		cfg.NATSAddr = "ws://127.0.0.1:8443"
	}
	if cfg.MaxConcurrency == 0 {
		cfg.MaxConcurrency = 8
	}
	return cfg, nil
}

func applyAgentEnvOverrides(cfg *AgentConfig) {
	if v := os.Getenv("DOG_NATS_ADDR"); v != "" {
		cfg.NATSAddr = strings.TrimSpace(v)
	}
	if v := os.Getenv("DOG_NATS_CREDS_FILE"); v != "" {
		cfg.CredsFile = strings.TrimSpace(v)
	}
	if v := os.Getenv("DOG_NATS_CREDS"); v != "" {
		cfg.Creds = v
	}
	if v := os.Getenv("DOG_PID_FILE"); v != "" {
		cfg.PIDFile = strings.TrimSpace(v)
	}
	if v := os.Getenv("DOG_MAX_CONCURRENCY"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 32); err == nil && n > 0 {
			cfg.MaxConcurrency = uint32(n)
		}
	}
	if v := os.Getenv("DOG_TLS_CA_FILE"); v != "" {
		cfg.TLSCAFile = strings.TrimSpace(v)
	}
	if v := os.Getenv("DOG_TLS_INSECURE"); v != "" {
		cfg.TLSInsecureSkipVerify = strings.EqualFold(v, "true") || v == "1"
	}
}
