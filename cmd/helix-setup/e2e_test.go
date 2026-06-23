//go:build e2e

// Package main e2e for cmd/helix-setup drives the REAL built setup-wizard
// binary end-to-end (CLAUDE-1): it `go build`s the artifact and execs it the
// way an operator provisioning a node would — pointing it at a fresh data
// directory via the documented -data-dir flag — then asserts the observable
// contract: exit code 0, the "Setup complete!" stdout banner, and the actual
// on-disk artifacts (config.yaml + node-config.yaml) the wizard is supposed to
// generate. Hardware detection runs for real on the host. Config/param-driven:
// the only input is a per-test t.TempDir(); nothing touches the real $HOME.
//
// Run with: go test -tags e2e ./cmd/helix-setup/ -count=1 -v
package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve caller path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
}

func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "helix-setup")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-buildvcs=false", "-o", bin, "./cmd/helix-setup")
	cmd.Dir = repoRoot(t)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build ./cmd/helix-setup failed: %v\n%s", err, out.String())
	}
	return bin
}

// TestSetupWizardProvisionsNode runs the real wizard against a fresh data dir
// (with -skip-checks so it does not depend on docker/git being installed in the
// CI sandbox; hardware detection still runs for real) and proves it generates
// the cluster config and node config on disk and reports completion.
func TestSetupWizardProvisionsNode(t *testing.T) {
	bin := buildBinary(t)
	dataDir := filepath.Join(t.TempDir(), "helix-data")

	cmd := exec.Command(bin,
		"-skip-checks",
		"-data-dir", dataDir,
		"-cluster-name", "e2e-cluster",
		"-node-id", "e2e-node-0",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("helix-setup exited non-zero: %v\nstdout:\n%s\nstderr:\n%s",
			err, stdout.String(), stderr.String())
	}
	out := stdout.String()

	// SINK-SIDE 1: observable completion banner.
	if !strings.Contains(out, "Setup complete!") {
		t.Fatalf("setup did not report completion; stdout:\n%s", out)
	}
	// SINK-SIDE 2: cluster config.yaml really written with expected content.
	cfgPath := filepath.Join(dataDir, "config.yaml")
	cfgBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("config.yaml not generated at %s: %v", cfgPath, err)
	}
	if !strings.Contains(string(cfgBytes), "name: helix-local") ||
		!strings.Contains(string(cfgBytes), "endpoints:") {
		t.Fatalf("config.yaml missing expected cluster/service content:\n%s", cfgBytes)
	}
	// SINK-SIDE 3: node-config.yaml produced by real hardware detection.
	nodeCfg := filepath.Join(dataDir, "node-config.yaml")
	nb, err := os.ReadFile(nodeCfg)
	if err != nil {
		t.Fatalf("node-config.yaml not generated at %s: %v", nodeCfg, err)
	}
	if len(bytes.TrimSpace(nb)) == 0 {
		t.Fatal("node-config.yaml was generated but is empty (hardware detection produced no output)")
	}
	if !strings.Contains(string(nb), "e2e-node-0") {
		t.Fatalf("node-config.yaml does not carry the supplied node id; got:\n%s", nb)
	}
}

// TestSetupWizardIdempotent proves the operator can safely re-run the wizard:
// a second run against the same data dir must also succeed (exit 0) and leave
// byte-identical config.yaml.
func TestSetupWizardIdempotent(t *testing.T) {
	bin := buildBinary(t)
	dataDir := filepath.Join(t.TempDir(), "helix-data")
	args := []string{"-skip-checks", "-data-dir", dataDir, "-cluster-name", "e2e", "-node-id", "n0"}

	for i := 0; i < 2; i++ {
		cmd := exec.Command(bin, args...)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		if err := cmd.Run(); err != nil {
			t.Fatalf("run %d failed: %v\n%s", i, err, out.String())
		}
	}
	if _, err := os.Stat(filepath.Join(dataDir, "config.yaml")); err != nil {
		t.Fatalf("config.yaml missing after idempotent re-run: %v", err)
	}
}
