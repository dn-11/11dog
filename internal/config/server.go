package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/11dog/11dog/internal/dn11"
	"gopkg.in/yaml.v3"
)

// ServerConfig holds all configuration for the 11dog control-plane server.
type ServerConfig struct {
	HTTPListenAddr string `yaml:"http_listen_addr"`
	GRPCListenAddr string `yaml:"grpc_listen_addr"`
	PublicGRPCAddr string `yaml:"public_grpc_addr"`
	// PublicGRPCTLS indicates agents must dial PublicGRPCAddr over TLS even when
	// the server itself listens in plaintext (e.g. TLS terminated by an Ingress).
	PublicGRPCTLS        bool               `yaml:"public_grpc_tls"`
	AgentBinDir          string             `yaml:"agent_bin_dir"`
	AgentDownloadBaseURL string             `yaml:"agent_download_base_url"`
	StaticDir            string             `yaml:"static_dir"`
	SiteTitle            string             `yaml:"site_title"`
	NetworkName          string             `yaml:"network_name"`
	TLS                  ServerTLSConfig    `yaml:"tls"`
	Auth                 ServerAuthConfig   `yaml:"auth"`
	DB                   DBConfig           `yaml:"db"`
	MQ                   MQConfig           `yaml:"mq"`
	Limits               LimitsConfig       `yaml:"limits"`
	DN11Prefixes         []dn11.PrefixEntry `yaml:"dn11_prefixes"`
}

type DBConfig struct {
	Driver string `yaml:"driver"` // sqlite | postgres
	DSN    string `yaml:"dsn"`
}

type ServerTLSConfig struct {
	Enabled  bool   `yaml:"enabled"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

type ServerAuthConfig struct {
	Tokens     []string `yaml:"tokens"`
	AdminToken string   `yaml:"admin_token"`
}

type MQConfig struct {
	Driver          string `yaml:"driver"` // memory | nats
	NATSURL         string `yaml:"nats_url"`
	StreamName      string `yaml:"stream_name"`
	MaxQueuePerNode int    `yaml:"max_queue_per_node"`
	TaskTTLSec      int    `yaml:"task_ttl_sec"`
}

type LimitsConfig struct {
	RateLimitRPM       int      `yaml:"rate_limit_rpm"`
	RateLimitBurst     int      `yaml:"rate_limit_burst"`
	MaxTargetLen       int      `yaml:"max_target_len"`
	DefaultTimeoutMs   uint32   `yaml:"default_timeout_ms"`
	MaxTimeoutMs       uint32   `yaml:"max_timeout_ms"`
	PerNodeDispatchRPS float64  `yaml:"per_node_dispatch_rps"`
	AllowedCIDRs       []string `yaml:"allowed_cidrs"`
	DeniedCIDRs        []string `yaml:"denied_cidrs"`
	TaskHistoryTTLSec  int      `yaml:"task_history_ttl_sec"`
}

// DefaultServerConfig returns production-safe defaults for 11dog-server.
func DefaultServerConfig() *ServerConfig {
	return &ServerConfig{
		HTTPListenAddr: ":8080",
		GRPCListenAddr: ":9090",
		AgentBinDir:    "./bin",
		SiteTitle:      "11Dog - DN11 网络测速平台",
		NetworkName:    "DN11",
		DB: DBConfig{
			Driver: "sqlite",
			DSN:    "./data/11dog.db",
		},
		MQ: MQConfig{
			Driver:          "memory",
			NATSURL:         "nats://127.0.0.1:4222",
			StreamName:      "DOG_TASKS",
			MaxQueuePerNode: 20,
			TaskTTLSec:      60,
		},
		Limits: LimitsConfig{
			RateLimitRPM:       180,
			RateLimitBurst:     30,
			MaxTargetLen:       512,
			DefaultTimeoutMs:   15000,
			MaxTimeoutMs:       60000,
			PerNodeDispatchRPS: 10,
			TaskHistoryTTLSec:  1800,
		},
		DN11Prefixes: []dn11.PrefixEntry{
			{CIDR: "172.16.0.0/16", ASN: 4211110000, Owner: "DN11 Backbone", Region: "DN11"},
			{CIDR: "fd00::/48", ASN: 4211110000, Owner: "DN11 IPv6 Backbone", Region: "DN11"},
		},
	}
}

// LoadServerConfig reads YAML configuration from path (if non-empty) and applies env overrides.
func LoadServerConfig(path string) (*ServerConfig, error) {
	cfg := DefaultServerConfig()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read server config %q: %w", path, err)
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse server config %q: %w", path, err)
		}
	}
	applyServerEnvOverrides(cfg)
	if cfg.DB.Driver == "" {
		cfg.DB.Driver = "sqlite"
	}
	if cfg.DB.Driver == "sqlite" && cfg.DB.DSN == "" {
		cfg.DB.DSN = "./data/11dog.db"
	}
	if cfg.AgentBinDir == "" {
		cfg.AgentBinDir = "./bin"
	}
	if cfg.MQ.Driver == "" {
		cfg.MQ.Driver = "memory"
	}
	if cfg.MQ.MaxQueuePerNode <= 0 {
		cfg.MQ.MaxQueuePerNode = 20
	}
	if cfg.MQ.TaskTTLSec <= 0 {
		cfg.MQ.TaskTTLSec = 60
	}
	if cfg.MQ.StreamName == "" {
		cfg.MQ.StreamName = "DOG_TASKS"
	}
	if cfg.Limits.DefaultTimeoutMs == 0 {
		cfg.Limits.DefaultTimeoutMs = 15000
	}
	if cfg.Limits.MaxTimeoutMs == 0 {
		cfg.Limits.MaxTimeoutMs = 60000
	}
	if cfg.Limits.MaxTargetLen == 0 {
		cfg.Limits.MaxTargetLen = 512
	}
	if cfg.Limits.TaskHistoryTTLSec <= 0 {
		cfg.Limits.TaskHistoryTTLSec = 1800
	}
	return cfg, nil
}

func applyServerEnvOverrides(cfg *ServerConfig) {
	if v := os.Getenv("DOG_HTTP_LISTEN"); v != "" {
		cfg.HTTPListenAddr = v
	}
	if v := os.Getenv("DOG_GRPC_LISTEN"); v != "" {
		cfg.GRPCListenAddr = v
	}
	if v := os.Getenv("DOG_STATIC_DIR"); v != "" {
		cfg.StaticDir = v
	}
	if v := os.Getenv("DOG_AGENT_TOKENS"); v != "" {
		parts := strings.Split(v, ",")
		tokens := make([]string, 0, len(parts))
		for _, p := range parts {
			if t := strings.TrimSpace(p); t != "" {
				tokens = append(tokens, t)
			}
		}
		cfg.Auth.Tokens = tokens
	}
	if v := os.Getenv("DOG_ADMIN_TOKEN"); v != "" {
		cfg.Auth.AdminToken = strings.TrimSpace(v)
	}
	if v := os.Getenv("DOG_MQ_DRIVER"); v != "" {
		cfg.MQ.Driver = v
	}
	if v := os.Getenv("DOG_NATS_URL"); v != "" {
		cfg.MQ.NATSURL = v
	}
	if v := os.Getenv("DOG_MQ_MAX_QUEUE_PER_NODE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MQ.MaxQueuePerNode = n
		}
	}
	if v := os.Getenv("DOG_MQ_TASK_TTL_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MQ.TaskTTLSec = n
		}
	}
	if v := os.Getenv("DOG_TLS_ENABLED"); v != "" {
		cfg.TLS.Enabled = strings.EqualFold(v, "true") || v == "1"
	}
	if v := os.Getenv("DOG_TLS_CERT_FILE"); v != "" {
		cfg.TLS.CertFile = v
	}
	if v := os.Getenv("DOG_TLS_KEY_FILE"); v != "" {
		cfg.TLS.KeyFile = v
	}
	if v := os.Getenv("DOG_DB_DRIVER"); v != "" {
		cfg.DB.Driver = v
	}
	if v := os.Getenv("DOG_DB_DSN"); v != "" {
		cfg.DB.DSN = v
	}
	if v := os.Getenv("DOG_PUBLIC_GRPC_ADDR"); v != "" {
		cfg.PublicGRPCAddr = strings.TrimSpace(v)
	}
	if v := os.Getenv("DOG_PUBLIC_GRPC_TLS"); v != "" {
		cfg.PublicGRPCTLS = strings.EqualFold(v, "true") || v == "1"
	}
	if v := os.Getenv("DOG_AGENT_BIN_DIR"); v != "" {
		cfg.AgentBinDir = strings.TrimSpace(v)
	}
	if v := os.Getenv("DOG_AGENT_DOWNLOAD_BASE_URL"); v != "" {
		cfg.AgentDownloadBaseURL = strings.TrimSpace(v)
	}
}
