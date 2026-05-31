package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AppName != "helix-cluster" {
		t.Errorf("unexpected app name: %s", cfg.AppName)
	}
	if cfg.Version != "0.1.0" {
		t.Errorf("unexpected version: %s", cfg.Version)
	}
	if cfg.Debug != false {
		t.Error("expected debug to be false by default")
	}
	if cfg.BindAddr != "0.0.0.0" {
		t.Errorf("unexpected bind addr: %s", cfg.BindAddr)
	}
	if cfg.HTTPPort != 8080 {
		t.Errorf("unexpected http port: %d", cfg.HTTPPort)
	}
	if cfg.GRPCPort != 9090 {
		t.Errorf("unexpected grpc port: %d", cfg.GRPCPort)
	}
	if len(cfg.EtcdEndpoints) != 1 || cfg.EtcdEndpoints[0] != "localhost:2379" {
		t.Errorf("unexpected etcd endpoints: %v", cfg.EtcdEndpoints)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("unexpected log level: %s", cfg.LogLevel)
	}
	if cfg.ReadTimeout != 30*time.Second {
		t.Errorf("unexpected read timeout: %v", cfg.ReadTimeout)
	}
	if cfg.MaxConns != 1000 {
		t.Errorf("unexpected max conns: %d", cfg.MaxConns)
	}
}

func TestLoadFromEnv(t *testing.T) {
	os.Setenv("HELIX_APP_NAME", "custom-app")
	os.Setenv("HELIX_VERSION", "2.0.0")
	os.Setenv("HELIX_DEBUG", "true")
	os.Setenv("HELIX_BIND_ADDR", "127.0.0.1")
	os.Setenv("HELIX_HTTP_PORT", "3000")
	os.Setenv("HELIX_GRPC_PORT", "4000")
	os.Setenv("HELIX_ETCD_ENDPOINTS", "etcd1:2379,etcd2:2379")
	os.Setenv("HELIX_LOG_LEVEL", "debug")
	os.Setenv("HELIX_READ_TIMEOUT", "10s")
	os.Setenv("HELIX_MAX_CONNS", "500")
	defer func() {
		os.Unsetenv("HELIX_APP_NAME")
		os.Unsetenv("HELIX_VERSION")
		os.Unsetenv("HELIX_DEBUG")
		os.Unsetenv("HELIX_BIND_ADDR")
		os.Unsetenv("HELIX_HTTP_PORT")
		os.Unsetenv("HELIX_GRPC_PORT")
		os.Unsetenv("HELIX_ETCD_ENDPOINTS")
		os.Unsetenv("HELIX_LOG_LEVEL")
		os.Unsetenv("HELIX_READ_TIMEOUT")
		os.Unsetenv("HELIX_MAX_CONNS")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AppName != "custom-app" {
		t.Errorf("expected app name custom-app, got %s", cfg.AppName)
	}
	if cfg.Version != "2.0.0" {
		t.Errorf("expected version 2.0.0, got %s", cfg.Version)
	}
	if cfg.Debug != true {
		t.Error("expected debug to be true")
	}
	if cfg.BindAddr != "127.0.0.1" {
		t.Errorf("expected bind addr 127.0.0.1, got %s", cfg.BindAddr)
	}
	if cfg.HTTPPort != 3000 {
		t.Errorf("expected http port 3000, got %d", cfg.HTTPPort)
	}
	if cfg.GRPCPort != 4000 {
		t.Errorf("expected grpc port 4000, got %d", cfg.GRPCPort)
	}
	if len(cfg.EtcdEndpoints) != 2 || cfg.EtcdEndpoints[0] != "etcd1:2379" {
		t.Errorf("unexpected etcd endpoints: %v", cfg.EtcdEndpoints)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("expected log level debug, got %s", cfg.LogLevel)
	}
	if cfg.ReadTimeout != 10*time.Second {
		t.Errorf("expected read timeout 10s, got %v", cfg.ReadTimeout)
	}
	if cfg.MaxConns != 500 {
		t.Errorf("expected max conns 500, got %d", cfg.MaxConns)
	}
}

func TestLoadFromYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
app_name: yaml-app
version: 1.2.3
bind_addr: 0.0.0.0
http_port: 7000
grpc_port: 7001
etcd_endpoints:
  - e1:2379
  - e2:2379
log_level: warn
read_timeout: 5s
max_conns: 200
labels:
  env: test
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	cfg, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AppName != "yaml-app" {
		t.Errorf("expected yaml-app, got %s", cfg.AppName)
	}
	if cfg.HTTPPort != 7000 {
		t.Errorf("expected http port 7000, got %d", cfg.HTTPPort)
	}
	if cfg.LogLevel != "warn" {
		t.Errorf("expected log level warn, got %s", cfg.LogLevel)
	}
	if cfg.Labels["env"] != "test" {
		t.Errorf("expected label env=test, got %v", cfg.Labels)
	}
}

func TestLoadFromJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	content := `{"app_name":"json-app","http_port":9000,"grpc_port":9001,"log_level":"error","max_conns":50}`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	cfg, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AppName != "json-app" {
		t.Errorf("expected json-app, got %s", cfg.AppName)
	}
	if cfg.HTTPPort != 9000 {
		t.Errorf("expected http port 9000, got %d", cfg.HTTPPort)
	}
}

func TestLoadFromUnsupportedFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("x=1"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	_, err := LoadFromFile(path)
	if err == nil {
		t.Fatal("expected error for unsupported format")
	}
}

func TestValidateEmptyAppName(t *testing.T) {
	cfg := &Config{AppName: "", Version: "1.0.0"}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for empty app name")
	}
}

func TestValidateInvalidPort(t *testing.T) {
	cfg := &Config{AppName: "test", HTTPPort: 99999, GRPCPort: 9090}
	cfg.SetDefaults()
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for invalid port")
	}
}

func TestValidateInvalidLogLevel(t *testing.T) {
	cfg := &Config{AppName: "test", LogLevel: "verbose"}
	cfg.SetDefaults()
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for invalid log level")
	}
}

func TestValidateTLSPartial(t *testing.T) {
	cfg := &Config{AppName: "test", TLSCertPath: "/cert.pem", TLSKeyPath: ""}
	cfg.SetDefaults()
	if err := cfg.Validate(); err == nil {
		t.Error("expected error when only cert path is set")
	}
}

func TestValidateValidConfig(t *testing.T) {
	cfg := &Config{AppName: "helix"}
	cfg.SetDefaults()
	if err := cfg.Validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestMerge(t *testing.T) {
	base := &Config{AppName: "base", HTTPPort: 8080, Labels: map[string]string{"a": "1"}}
	base.SetDefaults()
	other := &Config{AppName: "other", HTTPPort: 9090, Labels: map[string]string{"b": "2"}}
	base.Merge(other)
	if base.AppName != "other" {
		t.Errorf("expected app name other, got %s", base.AppName)
	}
	if base.HTTPPort != 9090 {
		t.Errorf("expected http port 9090, got %d", base.HTTPPort)
	}
	if base.Labels["a"] != "1" || base.Labels["b"] != "2" {
		t.Errorf("unexpected labels: %v", base.Labels)
	}
}

func TestMergeNil(t *testing.T) {
	base := &Config{AppName: "base"}
	base.Merge(nil)
	if base.AppName != "base" {
		t.Error("merge with nil should not change config")
	}
}

func TestSetDefaultsIdempotent(t *testing.T) {
	cfg := &Config{}
	cfg.SetDefaults()
	cfg.SetDefaults()
	if cfg.HTTPPort != 8080 {
		t.Errorf("expected port 8080, got %d", cfg.HTTPPort)
	}
}

// --- Mutation Tests ---

func TestMutationEnvOverrideEmptyString(t *testing.T) {
	os.Setenv("HELIX_APP_NAME", "")
	defer os.Unsetenv("HELIX_APP_NAME")
	cfg, _ := Load()
	if cfg.AppName != "helix-cluster" {
		t.Error("mutation: empty env var should fall back to default")
	}
}

func TestMutationCaseInsensitiveBool(t *testing.T) {
	os.Setenv("HELIX_DEBUG", "TRUE")
	defer os.Unsetenv("HELIX_DEBUG")
	cfg, _ := Load()
	if cfg.Debug != true {
		t.Error("mutation: expected TRUE (uppercase) to parse as true")
	}
}

func TestMutationMergeDoesNotOverwriteZeroValues(t *testing.T) {
	base := &Config{AppName: "base", HTTPPort: 8080}
	base.SetDefaults()
	other := &Config{AppName: ""}
	base.Merge(other)
	if base.AppName != "base" {
		t.Error("mutation: merge should not overwrite with empty string")
	}
}

func TestMutationValidateChecksMultipleFields(t *testing.T) {
	cfg := &Config{AppName: "", HTTPPort: 99999, LogLevel: "bad"}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
	msg := err.Error()
	if !contains(msg, "app name") || !contains(msg, "port") || !contains(msg, "log level") {
		t.Error("mutation: validation should report multiple errors")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
