package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/11dog/11dog/internal/model"
	"gopkg.in/yaml.v3"
)

// AgentConfig holds all configuration for a pure-binary 11dog edge agent connecting directly to NATS.
type AgentConfig struct {
	NATSAddr              string            `yaml:"nats_addr"`
	CredsFile             string            `yaml:"creds_file"`
	Creds                 string            `yaml:"creds,omitempty"`
	StreamName            string            `yaml:"stream_name"`
	TLSInsecureSkipVerify bool              `yaml:"tls_insecure_skip_verify"`
	TLSCAFile             string            `yaml:"tls_ca_file"`
	PIDFile               string            `yaml:"pid_file,omitempty"`
	NodeID                string            `yaml:"node_id,omitempty"`
	IPv4                  string            `yaml:"ipv4,omitempty"`
	IPv6                  string            `yaml:"ipv6,omitempty"`
	MaxConcurrency        uint32            `yaml:"max_concurrency"`
	HeartbeatIntervalSec  uint32            `yaml:"heartbeat_interval_sec"`
	Capabilities          []model.ProbeType `yaml:"capabilities"`
	Labels                map[string]string `yaml:"labels,omitempty"`
}

// DefaultAgentConfig returns sensible defaults for an edge probe agent.
func DefaultAgentConfig() *AgentConfig {
	return &AgentConfig{
		NATSAddr:             "ws://127.0.0.1:8443",
		CredsFile:            "/etc/11dog/agent.creds",
		StreamName:           "DOG_TASKS",
		MaxConcurrency:       8,
		HeartbeatIntervalSec: 10,
		Capabilities: []model.ProbeType{
			model.ProbePing,
			model.ProbeTcping,
			model.ProbeHTTP,
			model.ProbeTraceroute,
			model.ProbeDNS,
		},
		Labels: map[string]string{},
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
	if cfg.StreamName == "" {
		cfg.StreamName = "DOG_TASKS"
	}
	if cfg.MaxConcurrency == 0 {
		cfg.MaxConcurrency = 8
	}
	if cfg.HeartbeatIntervalSec == 0 {
		cfg.HeartbeatIntervalSec = 10
	}
	if len(cfg.Capabilities) == 0 {
		cfg.Capabilities = DefaultAgentConfig().Capabilities
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
	if v := os.Getenv("DOG_NATS_STREAM_NAME"); v != "" {
		cfg.StreamName = strings.TrimSpace(v)
	}
	if v := os.Getenv("DOG_TLS_INSECURE"); v != "" {
		cfg.TLSInsecureSkipVerify = strings.EqualFold(v, "true") || v == "1"
	}
	if v := os.Getenv("DOG_TLS_CA_FILE"); v != "" {
		cfg.TLSCAFile = strings.TrimSpace(v)
	}
	if v := os.Getenv("DOG_PID_FILE"); v != "" {
		cfg.PIDFile = strings.TrimSpace(v)
	}
	if v := os.Getenv("DOG_NODE_ID"); v != "" {
		cfg.NodeID = strings.TrimSpace(v)
	}
	if v := os.Getenv("DOG_MAX_CONCURRENCY"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 32); err == nil && n > 0 {
			cfg.MaxConcurrency = uint32(n)
		}
	}
}
