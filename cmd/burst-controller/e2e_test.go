//go:build e2e

// Package main e2e for cmd/burst-controller drives the REAL built binary
// end-to-end (CLAUDE-1): it `go build`s the artifact, execs it with real
// utilization samples via the documented --samples / --run-id flags and via
// stdin, and asserts the observable contract — the SPILL/RECOVER transition
// lines on stdout (carrying the run-id) and the process exit code. This proves
// the shipped hysteresis controller produces real burst-allocation decisions
// for an operator, not just that the package compiles.
//
// Run with: go test -tags e2e ./cmd/burst-controller/ -count=1 -v
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
	bin := filepath.Join(t.TempDir(), "burst-controller")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-buildvcs=false", "-o", bin, "./cmd/burst-controller")
	cmd.Dir = repoRoot(t)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build ./cmd/burst-controller failed: %v\n%s", err, out.String())
	}
	return bin
}

// TestBurstControllerSpillRecover feeds a real utilization trace that crosses
// the spill-high then recovers below recover-low, and asserts the binary emits
// BOTH a SPILL "burst allocation request" line and a RECOVER line, each tagged
// with the supplied run-id. This is the end-user-observable decision surface.
func TestBurstControllerSpillRecover(t *testing.T) {
	bin := buildBinary(t)
	const runID = "e2e-burst-001"

	// 0.50 (monitor) -> 0.97 (spill-high crossed) -> 0.30 (recover-low crossed).
	cmd := exec.Command(bin, "--run-id", runID, "--samples", "0.50,0.97,0.30")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("burst-controller exited non-zero on a valid trace: %v\nstdout:\n%s\nstderr:\n%s",
			err, stdout.String(), stderr.String())
	}
	out := stdout.String()

	if !strings.Contains(out, "SPILL runID="+runID) || !strings.Contains(out, "burst allocation request") {
		t.Fatalf("expected a SPILL/burst-allocation line tagged with run-id; stdout:\n%s", out)
	}
	if !strings.Contains(out, "RECOVER runID="+runID) {
		t.Fatalf("expected a RECOVER line tagged with run-id after utilization dropped; stdout:\n%s", out)
	}
}

// TestBurstControllerStdin proves the documented stdin sample source works on the
// real artifact (the operator can pipe a trace), producing at least one SPILL.
func TestBurstControllerStdin(t *testing.T) {
	bin := buildBinary(t)
	const runID = "e2e-burst-stdin"

	cmd := exec.Command(bin, "--run-id", runID)
	cmd.Stdin = strings.NewReader("0.40 0.99 0.20\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("burst-controller (stdin) exited non-zero: %v\nstderr:\n%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "SPILL runID="+runID) {
		t.Fatalf("stdin trace did not produce a SPILL; stdout:\n%s", stdout.String())
	}
}

// TestBurstControllerRejectsBadSample proves the load-bearing input guard on the
// real artifact: a non-finite sample must NOT silently produce a bogus burst
// decision — the binary must exit non-zero and report the bad input on stderr.
func TestBurstControllerRejectsBadSample(t *testing.T) {
	bin := buildBinary(t)

	cmd := exec.Command(bin, "--run-id", "e2e-bad", "--samples", "0.5,NaN,0.3")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		t.Fatalf("HARD-FAIL: burst-controller accepted a NaN sample (exit 0); stdout:\n%s", stdout.String())
	}
	if strings.Contains(stdout.String(), "burst allocation request") {
		t.Fatalf("HARD-FAIL: a NaN sample produced a bogus burst-allocation decision; stdout:\n%s", stdout.String())
	}
}
