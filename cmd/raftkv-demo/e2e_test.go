//go:build e2e

// Package main e2e for cmd/raftkv-demo drives the REAL built binary end-to-end
// (CLAUDE-1): it `go build`s the artifact and execs it, then asserts the
// operator-observable contract of the scripted demo — exit 0 plus the stdout
// evidence that a genuine 3-node embedded-Raft cluster elected a leader,
// replicated Put'd keys to ALL nodes' FSMs, rejected a follower write, and kept
// committed data readable after a leader kill + re-election. Every Put goes
// through real Raft consensus and every Get reads a replica's own FSM, so the
// asserted stdout reflects real replicated-store behaviour, not a local map.
//
// Run with: go test -tags e2e ./cmd/raftkv-demo/ -count=1 -v
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
	bin := filepath.Join(t.TempDir(), "raftkv-demo")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-buildvcs=false", "-o", bin, "./cmd/raftkv-demo")
	cmd.Dir = repoRoot(t)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build ./cmd/raftkv-demo failed: %v\n%s", err, out.String())
	}
	return bin
}

// TestRaftKVDemoRoundTrip runs the real demo binary and asserts the full
// consensus story is visible on stdout and the process exits 0.
func TestRaftKVDemoRoundTrip(t *testing.T) {
	bin := buildBinary(t)

	cmd := exec.Command(bin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("raftkv-demo exited non-zero: %v\nstdout:\n%s\nstderr:\n%s",
			err, stdout.String(), stderr.String())
	}
	out := stdout.String()

	// Each phase the demo proves must be observable on stdout.
	mustContain := []string{
		"leader elected:",                 // [1] real election
		"committed by majority",           // [2] Put through the Raft log
		"replication proof",               // [3] Get from all 3 FSMs
		"rejected as expected",            // [4] follower write rejected (ErrNotLeader)
		"new leader elected:",             // [5] failover re-election after kill
		"written-after-failover",          // [7] fresh write replicates post-failover
		"demo complete",                   // overall success banner
	}
	for _, want := range mustContain {
		if !strings.Contains(out, want) {
			t.Fatalf("raftkv-demo stdout missing expected evidence %q; full stdout:\n%s", want, out)
		}
	}

	// SINK-SIDE: a replicated key must be visible (ok=true) on a node's own FSM.
	if !strings.Contains(out, "service/db/host") || !strings.Contains(out, "ok=true") {
		t.Fatalf("raftkv-demo did not show a replicated key readable from a replica FSM; stdout:\n%s", out)
	}
}
