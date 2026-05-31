// Package config provides configuration loading and validation for Helix Cluster OS.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds application configuration for Helix Cluster services.
type Config struct {
	AppName      string        `yaml:"app_name" json:"app_name"`
	Version      string        `yaml:"version" json:"version"`
	Debug        bool          `yaml:"debug" json:"debug"`
	BindAddr     string        `yaml:"bind_addr" json:"bind_addr"`
	HTTPPort     int           `yaml:"http_port" json:"http_port"`
	GRPCPort     int           `yaml:"grpc_port" json:"grpc_port"`
	EtcdEndpoints []string     `yaml:"etcd_endpoints" json:"etcd_endpoints"`
	LogLevel     string        `yaml:"log_level" json:"log_level"`
	DataDir      string        `yaml:"data_dir" json:"data_dir"`
	TLSCertPath  string        `yaml:"tls_cert_path" json:"tls_cert_path"`
	TLSKeyPath   string        `yaml:"tls_key_path" json:"tls_key_path"`
	TLSCAPath    string        `yaml:"tls_ca_path" json:"tls_ca_path"`
	MetricsAddr  string        `yaml:"metrics_addr" json:"metrics_addr"`
	ReadTimeout  time.Duration `yaml:"read_timeout" json:"read_timeout"`
	WriteTimeout time.Duration `yaml:"write_timeout" json:"write_timeout"`
	MaxConns     int           `yaml:"max_conns" json:"max_conns"`
	MaxBodyBytes int64         `yaml:"max_body_bytes" json:"max_body_bytes"`
	Labels       map[string]string `yaml:"labels" json:"labels"`
}

// Load loads configuration from environment variables with sensible defaults.
func Load() (*Config, error) {
	cfg := &Config{}
	cfg.SetDefaults()
	cfg.AppName = getEnv(envVarName("app-name"), cfg.AppName)
	cfg.Version = getEnv(envVarName("version"), cfg.Version)
	cfg.Debug = getEnvBool(envVarName("debug"), cfg.Debug)
	cfg.BindAddr = getEnv(envVarName("bind-addr"), cfg.BindAddr)
	cfg.HTTPPort = getEnvInt(envVarName("http-port"), cfg.HTTPPort)
	cfg.GRPCPort = getEnvInt(envVarName("grpc-port"), cfg.GRPCPort)
	if v := os.Getenv(envVarName("etcd-endpoints")); v != "" {
		cfg.EtcdEndpoints = strings.Split(v, ",")
	}
	cfg.LogLevel = getEnv(envVarName("log-level"), cfg.LogLevel)
	cfg.DataDir = getEnv(envVarName("data-dir"), cfg.DataDir)
	cfg.TLSCertPath = getEnv(envVarName("tls-cert-path"), cfg.TLSCertPath)
	cfg.TLSKeyPath = getEnv(envVarName("tls-key-path"), cfg.TLSKeyPath)
	cfg.TLSCAPath = getEnv(envVarName("tls-ca-path"), cfg.TLSCAPath)
	cfg.MetricsAddr = getEnv(envVarName("metrics-addr"), cfg.MetricsAddr)
	cfg.ReadTimeout = getEnvDuration(envVarName("read-timeout"), cfg.ReadTimeout)
	cfg.WriteTimeout = getEnvDuration(envVarName("write-timeout"), cfg.WriteTimeout)
	cfg.MaxConns = getEnvInt(envVarName("max-conns"), cfg.MaxConns)
	return cfg, nil
}

// LoadFromFile loads configuration from a YAML or JSON file.
func LoadFromFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}
	cfg := &Config{}
	cfg.SetDefaults()
	switch {
	case strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml"):
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("unmarshal yaml: %w", err)
		}
	case strings.HasSuffix(path, ".json"):
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("unmarshal json: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported config format: %s", path)
	}
	return cfg, nil
}

// SetDefaults sets sensible defaults for all configuration fields.
func (c *Config) SetDefaults() {
	if c.AppName == "" {
		c.AppName = "helix-cluster"
	}
	if c.Version == "" {
		c.Version = "0.1.0"
	}
	if c.BindAddr == "" {
		c.BindAddr = "0.0.0.0"
	}
	if c.HTTPPort == 0 {
		c.HTTPPort = 8080
	}
	if c.GRPCPort == 0 {
		c.GRPCPort = 9090
	}
	if len(c.EtcdEndpoints) == 0 {
		c.EtcdEndpoints = []string{"localhost:2379"}
	}
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	if c.DataDir == "" {
		c.DataDir = "./data"
	}
	if c.MetricsAddr == "" {
		c.MetricsAddr = ":9091"
	}
	if c.ReadTimeout == 0 {
		c.ReadTimeout = 30 * time.Second
	}
	if c.WriteTimeout == 0 {
		c.WriteTimeout = 30 * time.Second
	}
	if c.MaxConns == 0 {
		c.MaxConns = 1000
	}
	if c.Labels == nil {
		c.Labels = make(map[string]string)
	}
}

// Validate checks the configuration for errors and returns detailed errors.
func (c *Config) Validate() error {
	var errs []string
	if strings.TrimSpace(c.AppName) == "" {
		errs = append(errs, "app name is required")
	}
	if c.HTTPPort <= 0 || c.HTTPPort > 65535 {
		errs = append(errs, fmt.Sprintf("invalid http port: %d", c.HTTPPort))
	}
	if c.GRPCPort <= 0 || c.GRPCPort > 65535 {
		errs = append(errs, fmt.Sprintf("invalid grpc port: %d", c.GRPCPort))
	}
	if len(c.EtcdEndpoints) == 0 {
		errs = append(errs, "at least one etcd endpoint is required")
	}
	for _, ep := range c.EtcdEndpoints {
		if strings.TrimSpace(ep) == "" {
			errs = append(errs, "etcd endpoint cannot be empty")
		}
	}
	validLevels := map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
	if !validLevels[strings.ToLower(c.LogLevel)] {
		errs = append(errs, fmt.Sprintf("invalid log level: %s", c.LogLevel))
	}
	if c.ReadTimeout <= 0 {
		errs = append(errs, "read timeout must be positive")
	}
	if c.WriteTimeout <= 0 {
		errs = append(errs, "write timeout must be positive")
	}
	if c.MaxConns <= 0 {
		errs = append(errs, "max connections must be positive")
	}
	if (c.TLSCertPath != "" || c.TLSKeyPath != "") && (c.TLSCertPath == "" || c.TLSKeyPath == "") {
		errs = append(errs, "both tls_cert_path and tls_key_path must be set when using TLS")
	}
	if len(errs) > 0 {
		return fmt.Errorf("config validation failed: %s", strings.Join(errs, "; "))
	}
	return nil
}

// Merge merges another config into this one. Non-zero values from other take precedence.
func (c *Config) Merge(other *Config) {
	if other == nil {
		return
	}
	if other.AppName != "" {
		c.AppName = other.AppName
	}
	if other.Version != "" {
		c.Version = other.Version
	}
	c.Debug = other.Debug
	if other.BindAddr != "" {
		c.BindAddr = other.BindAddr
	}
	if other.HTTPPort != 0 {
		c.HTTPPort = other.HTTPPort
	}
	if other.GRPCPort != 0 {
		c.GRPCPort = other.GRPCPort
	}
	if len(other.EtcdEndpoints) > 0 {
		c.EtcdEndpoints = append([]string(nil), other.EtcdEndpoints...)
	}
	if other.LogLevel != "" {
		c.LogLevel = other.LogLevel
	}
	if other.DataDir != "" {
		c.DataDir = other.DataDir
	}
	if other.TLSCertPath != "" {
		c.TLSCertPath = other.TLSCertPath
	}
	if other.TLSKeyPath != "" {
		c.TLSKeyPath = other.TLSKeyPath
	}
	if other.TLSCAPath != "" {
		c.TLSCAPath = other.TLSCAPath
	}
	if other.MetricsAddr != "" {
		c.MetricsAddr = other.MetricsAddr
	}
	if other.ReadTimeout != 0 {
		c.ReadTimeout = other.ReadTimeout
	}
	if other.WriteTimeout != 0 {
		c.WriteTimeout = other.WriteTimeout
	}
	if other.MaxConns != 0 {
		c.MaxConns = other.MaxConns
	}
	if other.Labels != nil {
		if c.Labels == nil {
			c.Labels = make(map[string]string)
		}
		for k, v := range other.Labels {
			c.Labels[k] = v
		}
	}
}

func envVarName(key string) string {
	return "HELIX_" + strings.ToUpper(strings.ReplaceAll(key, "-", "_"))
}

func getEnv(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func getEnvBool(key string, defaultValue bool) bool {
	if v := os.Getenv(key); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return defaultValue
		}
		return b
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if v := os.Getenv(key); v != "" {
		i, err := strconv.Atoi(v)
		if err != nil {
			return defaultValue
		}
		return i
	}
	return defaultValue
}

func getEnvDuration(key string, defaultValue time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return defaultValue
		}
		return d
	}
	return defaultValue
}
