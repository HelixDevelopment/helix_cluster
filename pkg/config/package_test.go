package config

import (
	"os"
	"testing"
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
}

func TestLoadFromEnv(t *testing.T) {
	os.Setenv("HELIX_APP_NAME", "custom-app")
	os.Setenv("HELIX_VERSION", "2.0.0")
	os.Setenv("HELIX_DEBUG", "true")
	defer func() {
		os.Unsetenv("HELIX_APP_NAME")
		os.Unsetenv("HELIX_VERSION")
		os.Unsetenv("HELIX_DEBUG")
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
}

func TestLoadInvalidBoolFallsBack(t *testing.T) {
	os.Setenv("HELIX_DEBUG", "not-a-bool")
	defer os.Unsetenv("HELIX_DEBUG")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Debug != false {
		t.Error("expected debug fallback to false for invalid bool")
	}
}

func TestValidateEmptyAppName(t *testing.T) {
	cfg := &Config{AppName: "", Version: "1.0.0"}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for empty app name")
	}
}

func TestValidateWhitespaceAppName(t *testing.T) {
	cfg := &Config{AppName: "   ", Version: "1.0.0"}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for whitespace-only app name")
	}
}

func TestValidateValidConfig(t *testing.T) {
	cfg := &Config{AppName: "helix", Version: "1.0.0"}
	if err := cfg.Validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- Mutation Tests ---

func TestMutationEnvOverrideEmptyString(t *testing.T) {
	// Empty string env var should fall back to default
	os.Setenv("HELIX_APP_NAME", "")
	defer os.Unsetenv("HELIX_APP_NAME")

	cfg, _ := Load()
	if cfg.AppName != "helix-cluster" {
		t.Error("mutation: empty env var should fall back to default")
	}
}

func TestMutationValidateDoesNotCheckVersion(t *testing.T) {
	// Mutation: Validate should NOT reject empty version (only app name matters)
	cfg := &Config{AppName: "helix", Version: ""}
	if err := cfg.Validate(); err != nil {
		t.Error("mutation: expected no error for empty version")
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
