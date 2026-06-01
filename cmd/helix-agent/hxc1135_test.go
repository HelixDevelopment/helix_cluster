package main

import (
	"bytes"
	"os"
	"testing"
)

// TestBuildConfig_EtcdEndpoints_FlagParsed proves that --etcd-endpoints is
// split on comma and each endpoint is trimmed and set into cfg.EtcdEndpoints.
//
// §1.1 mutation: change `cfg.EtcdEndpoints = endpoints` to
// `cfg.EtcdEndpoints = nil` in buildConfig. The assertion
// `cfg.EtcdEndpoints[0] == "a:2379"` then fails with index out of range /
// length 0 instead of 2.
func TestBuildConfig_EtcdEndpoints_FlagParsed(t *testing.T) {
	var stderr bytes.Buffer
	cfg, err := buildConfig([]string{
		"--id", "hxc1135-unit",
		"--etcd-endpoints", "a:2379,b:2379",
	}, &stderr)
	if err != nil {
		t.Fatalf("buildConfig: %v (stderr=%q)", err, stderr.String())
	}
	if len(cfg.EtcdEndpoints) != 2 {
		t.Fatalf("EtcdEndpoints len = %d, want 2; got %v", len(cfg.EtcdEndpoints), cfg.EtcdEndpoints)
	}
	if cfg.EtcdEndpoints[0] != "a:2379" {
		t.Errorf("EtcdEndpoints[0] = %q, want %q", cfg.EtcdEndpoints[0], "a:2379")
	}
	if cfg.EtcdEndpoints[1] != "b:2379" {
		t.Errorf("EtcdEndpoints[1] = %q, want %q", cfg.EtcdEndpoints[1], "b:2379")
	}
}

// TestBuildConfig_EtcdEndpoints_EnvFallback proves that when --etcd-endpoints
// flag is absent, HELIX_ETCD_ENDPOINTS env var is read as a fallback.
//
// §1.1 mutation: remove the env-var fallback block so the env var is never
// consulted. cfg.EtcdEndpoints stays nil/empty and the len==1 assertion fails.
func TestBuildConfig_EtcdEndpoints_EnvFallback(t *testing.T) {
	if err := os.Setenv("HELIX_ETCD_ENDPOINTS", "envhost:2379"); err != nil {
		t.Fatalf("Setenv: %v", err)
	}
	t.Cleanup(func() { os.Unsetenv("HELIX_ETCD_ENDPOINTS") })

	var stderr bytes.Buffer
	cfg, err := buildConfig([]string{"--id", "hxc1135-env"}, &stderr)
	if err != nil {
		t.Fatalf("buildConfig: %v (stderr=%q)", err, stderr.String())
	}
	if len(cfg.EtcdEndpoints) != 1 {
		t.Fatalf("EtcdEndpoints len = %d, want 1 (from env); got %v", len(cfg.EtcdEndpoints), cfg.EtcdEndpoints)
	}
	if cfg.EtcdEndpoints[0] != "envhost:2379" {
		t.Errorf("EtcdEndpoints[0] = %q, want %q", cfg.EtcdEndpoints[0], "envhost:2379")
	}
}

// TestBuildConfig_EtcdEndpoints_NeitherFlagNorEnv proves that when neither
// --etcd-endpoints nor HELIX_ETCD_ENDPOINTS is set, cfg.EtcdEndpoints is
// nil or empty (in-memory path is used).
//
// §1.1 mutation: unconditionally set cfg.EtcdEndpoints = []string{"x:2379"}.
// The len==0 assertion then fails.
func TestBuildConfig_EtcdEndpoints_NeitherFlagNorEnv(t *testing.T) {
	// Ensure the env var is not set in the test environment.
	os.Unsetenv("HELIX_ETCD_ENDPOINTS")

	var stderr bytes.Buffer
	cfg, err := buildConfig([]string{"--id", "hxc1135-neither"}, &stderr)
	if err != nil {
		t.Fatalf("buildConfig: %v (stderr=%q)", err, stderr.String())
	}
	if len(cfg.EtcdEndpoints) != 0 {
		t.Fatalf("EtcdEndpoints = %v, want nil/empty when neither flag nor env set", cfg.EtcdEndpoints)
	}
}

// TestBuildConfig_EtcdEndpoints_FlagTakesPrecedenceOverEnv proves that the
// --etcd-endpoints flag wins when both the flag and the env var are present.
//
// §1.1 mutation: remove the flag parsing entirely (or always use env even when
// flag is set). The assertion that flag value "flag:2379" is present fails
// because only the env value is loaded.
func TestBuildConfig_EtcdEndpoints_FlagTakesPrecedenceOverEnv(t *testing.T) {
	if err := os.Setenv("HELIX_ETCD_ENDPOINTS", "envhost:2379"); err != nil {
		t.Fatalf("Setenv: %v", err)
	}
	t.Cleanup(func() { os.Unsetenv("HELIX_ETCD_ENDPOINTS") })

	var stderr bytes.Buffer
	cfg, err := buildConfig([]string{
		"--id", "hxc1135-flagwin",
		"--etcd-endpoints", "flag:2379",
	}, &stderr)
	if err != nil {
		t.Fatalf("buildConfig: %v (stderr=%q)", err, stderr.String())
	}
	if len(cfg.EtcdEndpoints) != 1 {
		t.Fatalf("EtcdEndpoints len = %d, want 1; got %v", len(cfg.EtcdEndpoints), cfg.EtcdEndpoints)
	}
	if cfg.EtcdEndpoints[0] != "flag:2379" {
		t.Errorf("EtcdEndpoints[0] = %q, want %q (flag must win over env)", cfg.EtcdEndpoints[0], "flag:2379")
	}
}

// TestBuildConfig_EtcdEndpoints_EmptiesDropped proves that empty entries
// produced by a trailing comma or adjacent commas are silently dropped.
//
// §1.1 mutation: remove the `if ep != ""` guard so empty strings pass through.
// The len==2 assertion then fails because len==3 (one extra empty string).
func TestBuildConfig_EtcdEndpoints_EmptiesDropped(t *testing.T) {
	os.Unsetenv("HELIX_ETCD_ENDPOINTS")
	var stderr bytes.Buffer
	cfg, err := buildConfig([]string{
		"--id", "hxc1135-empty",
		"--etcd-endpoints", "a:2379,,b:2379,",
	}, &stderr)
	if err != nil {
		t.Fatalf("buildConfig: %v (stderr=%q)", err, stderr.String())
	}
	if len(cfg.EtcdEndpoints) != 2 {
		t.Fatalf("EtcdEndpoints len = %d, want 2 (empties dropped); got %v", len(cfg.EtcdEndpoints), cfg.EtcdEndpoints)
	}
}
