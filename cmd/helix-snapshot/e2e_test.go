//go:build e2e

// Package main e2e for cmd/helix-snapshot drives the REAL built binary
// end-to-end (CLAUDE-1): it `go build`s the artifact, then execs it as a real
// operator would against a config-driven HELIX_SNAPSHOT_DIR (no hardcoded
// paths) and asserts the observable contract — real stdout, the process exit
// code, AND the on-disk `.golden` snapshot file. It proves the shipped golden
// snapshot manager actually creates, lists, restores, and compares snapshots
// for an end user, exercising BOTH the match (exit 0) and the deliberate
// mismatch (exit 1) branches of `compare` — not merely that the package
// compiles.
//
// Run with: go test -tags e2e ./cmd/helix-snapshot/ -count=1 -v
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

// e2eSnapRepoRoot resolves the repository root from this test file's location so
// the build is independent of the caller's working directory.
func e2eSnapRepoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve caller path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
}

// e2eSnapBuildBinary builds the real helix-snapshot artifact with
// -buildvcs=false (REQUIRED for in-test / worktree builds where VCS stamping
// fails) and returns its path.
func e2eSnapBuildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "helix-snapshot")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-buildvcs=false", "-o", bin, "./cmd/helix-snapshot")
	cmd.Dir = e2eSnapRepoRoot(t)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build ./cmd/helix-snapshot failed: %v\n%s", err, out.String())
	}
	return bin
}

// e2eSnapRun execs the built binary with HELIX_SNAPSHOT_DIR pointing at the
// supplied config-driven snapshot directory, optionally piping stdin, and
// returns exit code, stdout, and stderr.
func e2eSnapRun(t *testing.T, bin, snapDir, stdin string, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "HELIX_SNAPSHOT_DIR="+snapDir)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running %v failed to start: %v\nstderr:\n%s", args, err, stderr.String())
		}
		code = ee.ExitCode()
	}
	return code, stdout.String(), stderr.String()
}

// TestE2ESnapshotFullLifecycle drives the real binary through the documented
// end-user flow against a config-driven HELIX_SNAPSHOT_DIR: create a snapshot
// from an input file, list it, restore it (byte-exact), and confirm the
// `.golden` artifact actually exists on disk. This is the operator-visible
// happy path.
func TestE2ESnapshotFullLifecycle(t *testing.T) {
	bin := e2eSnapBuildBinary(t)

	// Config-driven snapshot dir + input file (no hardcoded paths).
	snapDir := filepath.Join(t.TempDir(), "snapshots")
	payload := []byte("helix golden payload\nline2: ünïçödé 😀\n")
	inFile := filepath.Join(t.TempDir(), "config.txt")
	if err := os.WriteFile(inFile, payload, 0o644); err != nil {
		t.Fatalf("write input file: %v", err)
	}

	const name = "cluster-config"

	// create: must print the stored .golden path to stdout and exit 0.
	code, stdout, stderr := e2eSnapRun(t, bin, snapDir, "", "create", name, inFile)
	if code != 0 {
		t.Fatalf("create exit %d (want 0); stderr=%q", code, stderr)
	}
	wantPath := filepath.Join(snapDir, name+".golden")
	if strings.TrimSpace(stdout) != wantPath {
		t.Fatalf("create stdout = %q; want printed path %q", strings.TrimSpace(stdout), wantPath)
	}

	// Sink-side evidence: the .golden file exists on disk with the exact bytes.
	onDisk, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("expected snapshot file at %s: %v", wantPath, err)
	}
	if !bytes.Equal(onDisk, payload) {
		t.Fatalf("on-disk snapshot bytes mismatch: got %q want %q", onDisk, payload)
	}

	// list: must report the snapshot name on stdout, exit 0.
	code, stdout, stderr = e2eSnapRun(t, bin, snapDir, "", "list")
	if code != 0 {
		t.Fatalf("list exit %d (want 0); stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, name) {
		t.Fatalf("list stdout %q does not contain %q", stdout, name)
	}

	// restore: must write the stored bytes verbatim to stdout, exit 0.
	code, stdout, stderr = e2eSnapRun(t, bin, snapDir, "", "restore", name)
	if code != 0 {
		t.Fatalf("restore exit %d (want 0); stderr=%q", code, stderr)
	}
	if stdout != string(payload) {
		t.Fatalf("restore stdout mismatch: got %q want %q", stdout, payload)
	}
}

// TestE2ESnapshotCompareBranches proves BOTH branches of the real compare tool:
// an identical file matches (exit 0, no stderr) and a deliberately mutated file
// is detected as a mismatch (exit 1 + mismatch text on stderr). A compare that
// returns 0 on a tampered file would be a PASS-bluff (CLAUDE-1).
func TestE2ESnapshotCompareBranches(t *testing.T) {
	bin := e2eSnapBuildBinary(t)

	snapDir := filepath.Join(t.TempDir(), "snapshots")
	baseline := []byte("the quick brown fox jumps over the lazy dog")
	baseFile := filepath.Join(t.TempDir(), "baseline.bin")
	if err := os.WriteFile(baseFile, baseline, 0o644); err != nil {
		t.Fatalf("write baseline: %v", err)
	}

	const name = "compare-target"
	if code, _, stderr := e2eSnapRun(t, bin, snapDir, "", "create", name, baseFile); code != 0 {
		t.Fatalf("create exit %d (want 0); stderr=%q", code, stderr)
	}

	// MATCH branch: identical bytes -> exit 0.
	code, _, stderr := e2eSnapRun(t, bin, snapDir, "", "compare", name, baseFile)
	if code != 0 {
		t.Fatalf("compare(identical) exit %d (want 0); stderr=%q", code, stderr)
	}

	// MISMATCH branch: equal-length single-byte flip -> exit 1 + stderr.
	tampered := append([]byte(nil), baseline...)
	tampered[10] ^= 0xFF
	badFile := filepath.Join(t.TempDir(), "tampered.bin")
	if err := os.WriteFile(badFile, tampered, 0o644); err != nil {
		t.Fatalf("write tampered: %v", err)
	}
	code, stdout, stderr := e2eSnapRun(t, bin, snapDir, "", "compare", name, badFile)
	if code != 1 {
		t.Fatalf("compare(tampered) exit %d (want 1); stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "mismatch") {
		t.Fatalf("compare(tampered) expected 'mismatch' on stderr, got %q", stderr)
	}
}

// TestE2ESnapshotListEmptyAndUnknown proves operator-facing edge contracts on
// the real binary: an empty (config-driven) snapshot dir reports "No snapshots"
// at exit 0, and an unknown subcommand fails non-zero with usage on stderr.
func TestE2ESnapshotListEmptyAndUnknown(t *testing.T) {
	bin := e2eSnapBuildBinary(t)
	// An existing-but-empty snapshot dir is the real end-user state after a
	// snapshot is created then all entries are deleted. (A wholly non-existent
	// dir is correctly reported as a walk error with exit 1 by the binary.)
	snapDir := filepath.Join(t.TempDir(), "empty-snapshots")
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		t.Fatalf("mkdir snapshot dir: %v", err)
	}

	code, stdout, stderr := e2eSnapRun(t, bin, snapDir, "", "list")
	if code != 0 {
		t.Fatalf("list(empty) exit %d (want 0); stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "No snapshots") {
		t.Fatalf("list(empty) stdout = %q; want 'No snapshots'", stdout)
	}

	code, _, stderr = e2eSnapRun(t, bin, snapDir, "", "bogus-subcommand")
	if code == 0 {
		t.Fatalf("unknown subcommand returned exit 0; want non-zero")
	}
	if !strings.Contains(stderr, "unknown subcommand") {
		t.Fatalf("unknown subcommand stderr = %q; want 'unknown subcommand'", stderr)
	}
}
