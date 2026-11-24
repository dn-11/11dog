package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/11dog/11dog/internal/model"
	"gopkg.in/yaml.v3"
)

// AgentConfig holds all configuration for a pure-binary 11dog edge agent.
type AgentConfig struct {
	ServerAddr            string            `yaml:"server_addr"`
	Token                 string            `yaml:"token"`
	TLSInsecureSkipVerify bool              `yaml:"tls_insecure_skip_verify"`
	TLSCAFile             string            `yaml:"tls_ca_file"`
	NodeID                string            `yaml:"node_id"`
	Name                  string            `yaml:"name"`
	Region                string            `yaml:"region"`
	Group                 string            `yaml:"group"`
	ASN                   uint32            `yaml:"asn"`
	IPv4                  string            `yaml:"ipv4"`
	IPv6                  string            `yaml:"ipv6"`
	MaxConcurrency        uint32            `yaml:"max_concurrency"`
	HeartbeatIntervalSec  uint32            `yaml:"heartbeat_interval_sec"`
	Capabilities          []model.ProbeType `yaml:"capabilities"`
	Labels                map[string]string `yaml:"labels"`
}

// DefaultAgentConfig returns sensible defaults for an edge probe agent.
func DefaultAgentConfig() *AgentConfig {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "dn11-node"
	}
	return &AgentConfig{
		ServerAddr:           "ws://127.0.0.1:8080/api/v1/agent/ws",
		NodeID:               hostname,
		Name:                 hostname,
		Region:               "DN11",
		Group:                "Default",
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
	if cfg.NodeID == "" {
		hostname, _ := os.Hostname()
		if hostname == "" {
			hostname = "dn11-node"
		}
		cfg.NodeID = hostname
	}
	if cfg.Name == "" {
		cfg.Name = cfg.NodeID
	}
	if cfg.Region == "" {
		cfg.Region = "DN11"
	}
	if cfg.Group == "" {
		cfg.Group = "Default"
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
	if v := os.Getenv("DOG_SERVER_ADDR"); v != "" {
		cfg.ServerAddr = v
	}
	if v := os.Getenv("DOG_TOKEN"); v != "" {
		cfg.Token = v
	}
	if v := os.Getenv("DOG_TLS_INSECURE"); v != "" {
		cfg.TLSInsecureSkipVerify = strings.EqualFold(v, "true") || v == "1"
	}
	if v := os.Getenv("DOG_TLS_CA_FILE"); v != "" {
		cfg.TLSCAFile = v
	}
	if v := os.Getenv("DOG_NODE_ID"); v != "" {
		cfg.NodeID = v
	}
	if v := os.Getenv("DOG_NODE_NAME"); v != "" {
		cfg.Name = v
	}
	if v := os.Getenv("DOG_REGION"); v != "" {
		cfg.Region = v
	}
	if v := os.Getenv("DOG_GROUP"); v != "" {
		cfg.Group = v
	}
	if v := os.Getenv("DOG_ASN"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 32); err == nil {
			cfg.ASN = uint32(n)
		}
	}
	if v := os.Getenv("DOG_MAX_CONCURRENCY"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 32); err == nil && n > 0 {
			cfg.MaxConcurrency = uint32(n)
		}
	}
}
