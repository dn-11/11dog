package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/dn-11/11dog/internal/dn11"
	"gopkg.in/yaml.v3"
)

// ServerConfig holds all configuration for the 11dog control-plane server.
type ServerConfig struct {
	HTTPListenAddr       string             `yaml:"http_listen_addr"`
	AgentBinDir          string             `yaml:"agent_bin_dir"`
	AgentDownloadBaseURL string             `yaml:"agent_download_base_url"`
	StaticDir            string             `yaml:"static_dir"`
	SiteTitle            string             `yaml:"site_title"`
	NetworkName          string             `yaml:"network_name"`
	Auth                 ServerAuthConfig   `yaml:"auth"`
	DB                   DBConfig           `yaml:"db"`
	NATS                 NATSConfig         `yaml:"nats"`
	Limits               LimitsConfig       `yaml:"limits"`
	DN11Registry         DN11RegistryConfig `yaml:"dn11_registry"`
	DN11Prefixes         []dn11.PrefixEntry `yaml:"dn11_prefixes"`
}

// DN11RegistryConfig configures periodic in-memory synchronization from the remote DN11 registry.
type DN11RegistryConfig struct {
	Enabled            bool   `yaml:"enabled"`
	URL                string `yaml:"url"`
	RefreshIntervalSec int    `yaml:"refresh_interval_sec"`
}

type DBConfig struct {
	Driver string `yaml:"driver"` // sqlite | postgres
	DSN    string `yaml:"dsn"`
}

type ServerAuthConfig struct {
	AdminToken string `yaml:"admin_token"`
}

// NATSConfig configures the external NATS JetStream cluster and Account signing seed.
type NATSConfig struct {
	URL                   string `yaml:"url"`
	PublicURL             string `yaml:"public_url"`
	StreamName            string `yaml:"stream_name"`
	MaxQueuePerNode       int    `yaml:"max_queue_per_node"`
	TaskTTLSec            int    `yaml:"task_ttl_sec"`
	AccountSeed           string `yaml:"account_seed"`
	TLSCAFile             string `yaml:"tls_ca_file"`
	TLSInsecureSkipVerify bool   `yaml:"tls_insecure_skip_verify"`
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
		AgentBinDir:    "./bin",
		SiteTitle:      "11Dog - DN11 网络测速平台",
		NetworkName:    "DN11",
		DB: DBConfig{
			Driver: "sqlite",
			DSN:    "./data/11dog.db",
		},
		NATS: NATSConfig{
			URL:             "nats://127.0.0.1:4222",
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
		DN11Registry: DN11RegistryConfig{
			Enabled:            true,
			URL:                dn11.DefaultRegistryURL,
			RefreshIntervalSec: 1800,
		},
		DN11Prefixes: []dn11.PrefixEntry{
			{CIDR: "172.16.0.0/16", ASN: 4211110000, Owner: "DN11 Backbone", Region: "DN11"},
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
	if cfg.NATS.URL == "" {
		cfg.NATS.URL = "nats://127.0.0.1:4222"
	}
	if cfg.NATS.StreamName == "" {
		cfg.NATS.StreamName = "DOG_TASKS"
	}
	if cfg.NATS.MaxQueuePerNode <= 0 {
		cfg.NATS.MaxQueuePerNode = 20
	}
	if cfg.NATS.TaskTTLSec <= 0 {
		cfg.NATS.TaskTTLSec = 60
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
	if cfg.DN11Registry.URL == "" {
		cfg.DN11Registry.URL = dn11.DefaultRegistryURL
	}
	if cfg.DN11Registry.RefreshIntervalSec <= 0 {
		cfg.DN11Registry.RefreshIntervalSec = 1800
	}
	return cfg, nil
}

func applyServerEnvOverrides(cfg *ServerConfig) {
	if v := os.Getenv("DOG_HTTP_LISTEN"); v != "" {
		cfg.HTTPListenAddr = v
	}
	if v := os.Getenv("DOG_STATIC_DIR"); v != "" {
		cfg.StaticDir = v
	}
	if v := os.Getenv("DOG_ADMIN_TOKEN"); v != "" {
		cfg.Auth.AdminToken = strings.TrimSpace(v)
	}
	if v := os.Getenv("DOG_NATS_URL"); v != "" {
		cfg.NATS.URL = strings.TrimSpace(v)
	}
	if v := os.Getenv("DOG_PUBLIC_NATS_URL"); v != "" {
		cfg.NATS.PublicURL = strings.TrimSpace(v)
	}
	if v := os.Getenv("DOG_NATS_STREAM_NAME"); v != "" {
		cfg.NATS.StreamName = strings.TrimSpace(v)
	}
	if v := os.Getenv("DOG_NATS_MAX_QUEUE_PER_NODE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.NATS.MaxQueuePerNode = n
		}
	}
	if v := os.Getenv("DOG_NATS_TASK_TTL_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.NATS.TaskTTLSec = n
		}
	}
	if v := os.Getenv("DOG_NATS_ACCOUNT_SEED"); v != "" {
		cfg.NATS.AccountSeed = strings.TrimSpace(v)
	}
	if v := os.Getenv("DOG_NATS_TLS_CA_FILE"); v != "" {
		cfg.NATS.TLSCAFile = strings.TrimSpace(v)
	}
	if v := os.Getenv("DOG_NATS_TLS_INSECURE"); v != "" {
		cfg.NATS.TLSInsecureSkipVerify = strings.EqualFold(v, "true") || v == "1"
	}
	if v := os.Getenv("DOG_DB_DRIVER"); v != "" {
		cfg.DB.Driver = v
	}
	if v := os.Getenv("DOG_DB_DSN"); v != "" {
		cfg.DB.DSN = v
	}
	if v := os.Getenv("DOG_AGENT_BIN_DIR"); v != "" {
		cfg.AgentBinDir = strings.TrimSpace(v)
	}
	if v := os.Getenv("DOG_AGENT_DOWNLOAD_BASE_URL"); v != "" {
		cfg.AgentDownloadBaseURL = strings.TrimSpace(v)
	}
	if v := os.Getenv("DOG_DN11_REGISTRY_ENABLED"); v != "" {
		cfg.DN11Registry.Enabled = strings.EqualFold(v, "true") || v == "1"
	}
	if v := os.Getenv("DOG_DN11_REGISTRY_URL"); v != "" {
		cfg.DN11Registry.URL = strings.TrimSpace(v)
	}
	if v := os.Getenv("DOG_DN11_REGISTRY_REFRESH_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.DN11Registry.RefreshIntervalSec = n
		}
	}
}
