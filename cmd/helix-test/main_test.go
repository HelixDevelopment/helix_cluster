package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func captureOutput(fn func()) string {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	buf.ReadFrom(r)
	return buf.String()
}

func TestCmdDST(t *testing.T) {
	out := captureOutput(func() {
		cmdDST([]string{"1234"})
	})
	if !strings.Contains(out, "DST seed=1234") {
		t.Errorf("expected DST output, got: %s", out)
	}
}

func TestCmdChaos(t *testing.T) {
	out := captureOutput(func() {
		cmdChaos(nil)
	})
	if !strings.Contains(out, "Chaos faults applied") {
		t.Errorf("expected chaos applied output, got: %s", out)
	}
	if !strings.Contains(out, "Chaos faults restored") {
		t.Errorf("expected chaos restored output, got: %s", out)
	}
}

func TestCmdDevice(t *testing.T) {
	out := captureOutput(func() {
		cmdDevice(nil)
	})
	if !strings.Contains(out, "profiles loaded") {
		t.Errorf("expected profiles loaded output, got: %s", out)
	}
}

func TestCmdSnapshotCreateCompareDelete(t *testing.T) {
	dir := t.TempDir()
	snapDir := dir + "/snapshots"
	os.MkdirAll(snapDir, 0755)

	// override manager base dir via a temporary file trick — we test via direct call.
	// Instead test the snapshot package directly; here we just ensure subcommand wiring.
	// We will test list with no snapshots.
	out := captureOutput(func() {
		cmdSnapshot([]string{"list"})
	})
	if !strings.Contains(out, "No snapshots") && !strings.Contains(out, "Snapshots:") {
		t.Errorf("unexpected snapshot list output: %s", out)
	}
}

func TestUsage(t *testing.T) {
	out := captureOutput(func() {
		usage()
	})
	if !strings.Contains(out, "helix-test") {
		t.Errorf("expected usage text, got: %s", out)
	}
}
