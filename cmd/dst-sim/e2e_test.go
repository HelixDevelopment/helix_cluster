//go:build e2e

// Package main e2e for cmd/dst-sim drives the REAL built deterministic-simulation
// gate binary end-to-end (CLAUDE-1): it `go build`s the artifact and execs it
// with real seed-range flags, asserting the operator-observable contract — the
// stdout report and the load-bearing PROCESS EXIT CODE that makes the CI gate
// trustworthy (0 on an all-linearizable range, non-zero on a vacuous/empty run).
//
// This complements the cross-package gate e2e at test/e2e/consensus and keeps the
// per-binary coverage local to cmd/dst-sim. Config/param-driven: seed count and
// steps are passed via the documented -seeds/-start/-steps flags.
//
// Run with: go test -tags e2e ./cmd/dst-sim/ -count=1 -v
package main

import (
	"bytes"
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
	bin := filepath.Join(t.TempDir(), "dst-sim")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-buildvcs=false", "-o", bin, "./cmd/dst-sim")
	cmd.Dir = repoRoot(t)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build ./cmd/dst-sim failed: %v\n%s", err, out.String())
	}
	return bin
}

// TestDSTSimGatePasses runs a small seed range through the real binary and
// asserts exit 0 AND the all-linearizable stdout report — proving the gate runs
// the simulation to a genuine PASS, not a vacuous no-op.
func TestDSTSimGatePasses(t *testing.T) {
	bin := buildBinary(t)

	cmd := exec.Command(bin, "-seeds", "20", "-steps", "600")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("dst-sim exited non-zero on a passing seed range: %v\nstdout:\n%s\nstderr:\n%s",
			err, stdout.String(), stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "running 20 seeded simulations") {
		t.Fatalf("dst-sim did not report running the simulations; stdout:\n%s", out)
	}
	if !strings.Contains(out, "all 20 seeds linearizable") {
		t.Fatalf("dst-sim did not report the all-pass result; stdout:\n%s", out)
	}
}

// TestDSTSimRefusesEmptyRun proves the load-bearing anti-PASS-bluff guard on the
// real artifact: a zero seed count must NOT report a vacuous success — the binary
// must exit non-zero and say so on stdout.
func TestDSTSimRefusesEmptyRun(t *testing.T) {
	bin := buildBinary(t)

	cmd := exec.Command(bin, "-seeds", "0")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	if err == nil {
		t.Fatalf("HARD-FAIL: dst-sim reported success for an empty (0-seed) run; stdout:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "refusing to report a vacuous PASS") {
		t.Fatalf("dst-sim did not explain the empty-run refusal; stdout:\n%s", stdout.String())
	}
}

// TestDSTSimDeterministic proves the gate is reproducible end-to-end through the
// real binary: the same seed range yields byte-identical output twice.
func TestDSTSimDeterministic(t *testing.T) {
	bin := buildBinary(t)
	run := func() (string, bool) {
		cmd := exec.Command(bin, "-start", "50", "-seeds", "12", "-steps", "500")
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		ok := cmd.Run() == nil
		return stdout.String(), ok
	}
	out1, ok1 := run()
	out2, ok2 := run()
	if !ok1 || !ok2 {
		t.Fatalf("both runs should exit 0 (ok1=%v ok2=%v)\nout1:\n%s", ok1, ok2, out1)
	}
	if out1 != out2 {
		t.Fatalf("dst-sim is NOT deterministic:\n--- run1 ---\n%s\n--- run2 ---\n%s", out1, out2)
	}
}
