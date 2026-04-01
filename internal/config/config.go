package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listener ListenerConfig `yaml:"listener"`
	Backend  BackendConfig  `yaml:"backend"`
	Pool     PoolConfig     `yaml:"pool"`
	TLS      TLSConfig      `yaml:"tls"`
	Metrics  MetricsConfig  `yaml:"metrics"`
	Logging  LoggingConfig  `yaml:"logging"`
	RateLimit RateLimitConfig `yaml:"rate_limit"`
}

type ListenerConfig struct {
	Address        string        `yaml:"address"`
	MaxConnections int           `yaml:"max_connections"`
	ReadTimeout    time.Duration `yaml:"read_timeout"`
	WriteTimeout   time.Duration `yaml:"write_timeout"`
}

type BackendConfig struct {
	Primary  BackendNode   `yaml:"primary"`
	Replicas []BackendNode `yaml:"replicas"`
}

type BackendNode struct {
	Address string `yaml:"address"`
}

type PoolConfig struct {
	PrimarySize    int           `yaml:"primary_size"`
	ReplicaSize    int           `yaml:"replica_size"`
	IdleTimeout    time.Duration `yaml:"idle_timeout"`
	CleanupInterval time.Duration `yaml:"cleanup_interval"`
	QueryTimeout   time.Duration `yaml:"query_timeout"`
	MaxReplicaLag  int64         `yaml:"max_replica_lag"` // seconds; 0 disables lag check
}

type TLSConfig struct {
	Enabled  bool   `yaml:"enabled"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	CAFile   string `yaml:"ca_file"`
}

type MetricsConfig struct {
	Address string `yaml:"address"`
}

type LoggingConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

type RateLimitConfig struct {
	Enabled       bool    `yaml:"enabled"`
	RequestsPerSec float64 `yaml:"requests_per_sec"`
	Burst         int     `yaml:"burst"`
}

type ShutdownConfig struct {
	Timeout time.Duration `yaml:"timeout"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	cfg.applyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Listener.Address == "" {
		c.Listener.Address = ":5432"
	}
	if c.Listener.MaxConnections <= 0 {
		c.Listener.MaxConnections = 100
	}
	if c.Listener.ReadTimeout <= 0 {
		c.Listener.ReadTimeout = 30 * time.Second
	}
	if c.Listener.WriteTimeout <= 0 {
		c.Listener.WriteTimeout = 30 * time.Second
	}
	if c.Pool.PrimarySize <= 0 {
		c.Pool.PrimarySize = 10
	}
	if c.Pool.ReplicaSize <= 0 {
		c.Pool.ReplicaSize = 10
	}
	if c.Pool.IdleTimeout <= 0 {
		c.Pool.IdleTimeout = 60 * time.Second
	}
	if c.Pool.CleanupInterval <= 0 {
		c.Pool.CleanupInterval = 30 * time.Second
	}
	if c.Pool.QueryTimeout <= 0 {
		c.Pool.QueryTimeout = 30 * time.Second
	}
	if c.Metrics.Address == "" {
		c.Metrics.Address = ":8080"
	}
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	if c.Logging.Format == "" {
		c.Logging.Format = "json"
	}
	if c.RateLimit.RequestsPerSec <= 0 {
		c.RateLimit.RequestsPerSec = 1000
	}
	if c.RateLimit.Burst <= 0 {
		c.RateLimit.Burst = 100
	}
}

func (c *Config) Validate() error {
	if c.Backend.Primary.Address == "" {
		return fmt.Errorf("backend.primary.address is required")
	}
	if err := validateAddress(c.Backend.Primary.Address); err != nil {
		return fmt.Errorf("backend.primary.address invalid: %w", err)
	}
	for i, r := range c.Backend.Replicas {
		if r.Address == "" {
			return fmt.Errorf("backend.replicas[%d].address is required", i)
		}
		if err := validateAddress(r.Address); err != nil {
			return fmt.Errorf("backend.replicas[%d].address invalid: %w", i, err)
		}
	}
	if err := validateAddress(c.Listener.Address); err != nil {
		return fmt.Errorf("listener.address invalid: %w", err)
	}
	if c.Listener.MaxConnections < 1 || c.Listener.MaxConnections > 100000 {
		return fmt.Errorf("listener.max_connections must be between 1 and 100000")
	}
	if c.Pool.PrimarySize < 1 || c.Pool.PrimarySize > 10000 {
		return fmt.Errorf("pool.primary_size must be between 1 and 10000")
	}
	if c.Pool.ReplicaSize < 1 || c.Pool.ReplicaSize > 10000 {
		return fmt.Errorf("pool.replica_size must be between 1 and 10000")
	}
	if c.TLS.Enabled {
		if c.TLS.CertFile == "" {
			return fmt.Errorf("tls.cert_file is required when TLS is enabled")
		}
		if c.TLS.KeyFile == "" {
			return fmt.Errorf("tls.key_file is required when TLS is enabled")
		}
	}
	validLevels := map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
	if !validLevels[c.Logging.Level] {
		return fmt.Errorf("logging.level must be one of: debug, info, warn, error")
	}
	validFormats := map[string]bool{"json": true, "text": true}
	if !validFormats[c.Logging.Format] {
		return fmt.Errorf("logging.format must be one of: json, text")
	}
	return nil
}

func validateAddress(addr string) error {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid host:port format: %w", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return fmt.Errorf("port is not a number: %w", err)
	}
	if port < 0 || port > 65535 {
		return fmt.Errorf("port %d out of range (0-65535)", port)
	}
	if host != "" && net.ParseIP(host) == nil {
		// Could be a hostname, try to resolve
		if _, err := net.LookupHost(host); err != nil {
			return fmt.Errorf("cannot resolve host %q: %w", host, err)
		}
	}
	return nil
}
