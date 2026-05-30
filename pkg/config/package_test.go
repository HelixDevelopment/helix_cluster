package config

import "testing"

func TestLoad(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AppName != "helix-cluster" {
		t.Errorf("unexpected app name: %s", cfg.AppName)
	}
}

func TestValidate(t *testing.T) {
	cfg := &Config{AppName: "", Version: "1.0.0"}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for empty app name")
	}
}
