package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runCapture invokes run with the given args and returns (exitCode, stdout, stderr).
// It uses injected buffers — no global os.Stdout swap — so it is race-safe.
func runCapture(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

// --- dispatch / usage -------------------------------------------------------

// Mutation: in run(), change `return 1` for the no-args branch to `return 0`.
func TestRunNoArgsExits1(t *testing.T) {
	code, out, _ := runCapture()
	if code != 1 {
		t.Fatalf("no-args exit code = %d, want 1", code)
	}
	if !strings.Contains(out, "Usage: helix-test") {
		t.Fatalf("no-args output missing usage, got: %q", out)
	}
}

// Mutation: in run(), change the default-case `return 1` to `return 0`.
func TestRunUnknownCommandExits1(t *testing.T) {
	code, _, errb := runCapture("bogus")
	if code != 1 {
		t.Fatalf("unknown cmd exit code = %d, want 1", code)
	}
	if !strings.Contains(errb, "Unknown command: bogus") {
		t.Fatalf("stderr missing unknown-command diagnostic, got: %q", errb)
	}
}

// Mutation: in run(), change the help-case `return 0` to `return 1`.
func TestRunHelpExits0(t *testing.T) {
	for _, flag := range []string{"help", "-h", "--help"} {
		code, out, _ := runCapture(flag)
		if code != 0 {
			t.Fatalf("%q exit code = %d, want 0", flag, code)
		}
		if !strings.Contains(out, "Manage golden snapshots") {
			t.Fatalf("%q usage missing snapshot line, got: %q", flag, out)
		}
	}
}

// --- dst --------------------------------------------------------------------

// Mutation: in cmdDST, register the handler for "message" (the old bug) instead
// of "heartbeat" — messages would then be 0, failing the messages=3 assertion.
func TestCmdDSTCountsHeartbeats(t *testing.T) {
	code, out, _ := runCapture("dst", "1234")
	if code != 0 {
		t.Fatalf("dst exit code = %d, want 0", code)
	}
	// 3 heartbeats scheduled within the maxEvents budget; all dispatched.
	want := "DST seed=1234 nodes=3 events=3 messages=3"
	if !strings.Contains(out, want) {
		t.Fatalf("dst output = %q, want substring %q", out, want)
	}
}

// Mutation: in cmdDST, drop the seed-parse error check (always default 42) —
// then a bad seed would not yield exit 1.
func TestCmdDSTInvalidSeedExits1(t *testing.T) {
	code, _, errb := runCapture("dst", "not-a-number")
	if code != 1 {
		t.Fatalf("bad-seed exit code = %d, want 1", code)
	}
	if !strings.Contains(errb, "invalid seed") {
		t.Fatalf("stderr missing invalid-seed diagnostic, got: %q", errb)
	}
}

// Mutation: in cmdDST, default seed from 42 to any other value.
func TestCmdDSTDefaultSeed(t *testing.T) {
	_, out, _ := runCapture("dst")
	if !strings.Contains(out, "DST seed=42 ") {
		t.Fatalf("default seed output = %q, want seed=42", out)
	}
}

// --- chaos ------------------------------------------------------------------

// Mutation: in cmdChaos, remove `runner.AddFault(crash)` — ListFaults would no
// longer include "node-crash", failing this assertion.
func TestCmdChaosAppliesAndRestores(t *testing.T) {
	code, out, _ := runCapture("chaos")
	if code != 0 {
		t.Fatalf("chaos exit code = %d, want 0", code)
	}
	if !strings.Contains(out, "Chaos faults applied: [network-partition node-crash]") {
		t.Fatalf("chaos applied line wrong: %q", out)
	}
	if !strings.Contains(out, "Chaos faults restored") {
		t.Fatalf("chaos restored line missing: %q", out)
	}
}

// --- device -----------------------------------------------------------------

// Mutation: in cmdDevice, print len(profiles)-1 (or a constant) instead of the
// real count — the "8 profiles" + T1/T8 spec assertions would fail.
func TestCmdDeviceListsAllTiers(t *testing.T) {
	code, out, _ := runCapture("device")
	if code != 0 {
		t.Fatalf("device exit code = %d, want 0", code)
	}
	if !strings.Contains(out, "Device simulation: 8 profiles loaded") {
		t.Fatalf("device count line wrong: %q", out)
	}
	// Sink-side: real seeded specs for the smallest and largest tier.
	if !strings.Contains(out, "T1: 1 cores, 1 GB RAM, 0 GPU(s)") {
		t.Fatalf("device T1 spec wrong: %q", out)
	}
	if !strings.Contains(out, "T8: 128 cores, 512 GB RAM, 8 GPU(s)") {
		t.Fatalf("device T8 spec wrong: %q", out)
	}
}

// --- snapshot ---------------------------------------------------------------

// withSnapshotDir points the snapshot base dir at a temp dir for the duration
// of the test, restoring the prior env afterward.
func withSnapshotDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HELIX_TEST_SNAPSHOT_DIR", dir)
	return dir
}

// Mutation: in cmdSnapshot create, pass `false` for the update flag AND also
// change the success message — simplest kill: change the printed path format so
// it no longer references the real on-disk .golden path.
func TestCmdSnapshotCreateThenCompareRoundTrip(t *testing.T) {
	dir := withSnapshotDir(t)

	// Source payload file the CLI reads.
	src := filepath.Join(t.TempDir(), "payload.txt")
	if err := os.WriteFile(src, []byte("golden-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, out, errb := runCapture("snapshot", "create", "demo", src)
	if code != 0 {
		t.Fatalf("create exit = %d, stderr=%q", code, errb)
	}
	wantPath := filepath.Join(dir, "demo.golden")
	if !strings.Contains(out, "Snapshot created: "+wantPath) {
		t.Fatalf("create output = %q, want path %q", out, wantPath)
	}
	// Real sink-side proof: the golden file exists with the exact payload.
	got, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("golden file not written: %v", err)
	}
	if string(got) != "golden-bytes" {
		t.Fatalf("golden contents = %q, want %q", got, "golden-bytes")
	}

	// Compare against identical bytes -> match, exit 0.
	code, out, _ = runCapture("snapshot", "compare", "demo", src)
	if code != 0 || !strings.Contains(out, "Snapshot matches") {
		t.Fatalf("compare(match) code=%d out=%q", code, out)
	}
}

// Mutation: in cmdSnapshot compare, change `return 1` on mgr.Compare error to
// `return 0` — a real mismatch would then be reported as success.
func TestCmdSnapshotCompareMismatchExits1(t *testing.T) {
	dir := withSnapshotDir(t)

	src := filepath.Join(t.TempDir(), "payload.txt")
	if err := os.WriteFile(src, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errb := runCapture("snapshot", "create", "k", src); code != 0 {
		t.Fatalf("setup create failed: code=%d err=%q", code, errb)
	}

	// Different bytes -> mismatch.
	other := filepath.Join(t.TempDir(), "other.txt")
	if err := os.WriteFile(other, []byte("CHANGED!!"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errb := runCapture("snapshot", "compare", "k", other)
	if code != 1 {
		t.Fatalf("mismatch compare exit = %d, want 1", code)
	}
	if !strings.Contains(errb, "compare failed") {
		t.Fatalf("stderr missing compare-failed diagnostic: %q", errb)
	}
	_ = dir
}

// Mutation: in cmdSnapshot list, return "Snapshots:" header even when empty —
// this empty-dir case must print exactly "No snapshots".
func TestCmdSnapshotListEmpty(t *testing.T) {
	withSnapshotDir(t)
	code, out, _ := runCapture("snapshot", "list")
	if code != 0 {
		t.Fatalf("list exit = %d, want 0", code)
	}
	if strings.TrimSpace(out) != "No snapshots" {
		t.Fatalf("empty list output = %q, want \"No snapshots\"", out)
	}
}

// Mutation: in cmdSnapshot list, stop printing created names — the "demo" line
// would vanish.
func TestCmdSnapshotListShowsNames(t *testing.T) {
	withSnapshotDir(t)
	src := filepath.Join(t.TempDir(), "p.txt")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errb := runCapture("snapshot", "create", "demo", src); code != 0 {
		t.Fatalf("setup create failed: %q", errb)
	}
	code, out, _ := runCapture("snapshot", "list")
	if code != 0 {
		t.Fatalf("list exit = %d, want 0", code)
	}
	if !strings.Contains(out, "Snapshots:") || !strings.Contains(out, "  demo") {
		t.Fatalf("list output = %q, want header + demo", out)
	}
}

// Mutation: in cmdSnapshot delete, change `return 1` on Delete error to 0 — a
// delete of a non-existent snapshot would then wrongly report success.
func TestCmdSnapshotDeleteMissingExits1(t *testing.T) {
	withSnapshotDir(t)
	code, _, errb := runCapture("snapshot", "delete", "nope")
	if code != 1 {
		t.Fatalf("delete-missing exit = %d, want 1", code)
	}
	if !strings.Contains(errb, "delete:") {
		t.Fatalf("stderr missing delete diagnostic: %q", errb)
	}
}

// Mutation: in cmdSnapshot delete, skip the actual mgr.Delete call — the file
// would survive and the post-delete list would still show it.
func TestCmdSnapshotDeleteRemovesFile(t *testing.T) {
	withSnapshotDir(t)
	src := filepath.Join(t.TempDir(), "p.txt")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errb := runCapture("snapshot", "create", "gone", src); code != 0 {
		t.Fatalf("setup create failed: %q", errb)
	}
	if code, _, errb := runCapture("snapshot", "delete", "gone"); code != 0 {
		t.Fatalf("delete exit nonzero: %q", errb)
	}
	code, out, _ := runCapture("snapshot", "list")
	if code != 0 || strings.TrimSpace(out) != "No snapshots" {
		t.Fatalf("after delete, list = %q (code %d), want \"No snapshots\"", out, code)
	}
}

// Mutation: in cmdSnapshot, drop the `len(args) < 1` guard — running snapshot
// with no subcommand would no longer exit 1 with the usage line.
func TestCmdSnapshotNoSubExits1(t *testing.T) {
	code, out, _ := runCapture("snapshot")
	if code != 1 {
		t.Fatalf("snapshot no-sub exit = %d, want 1", code)
	}
	if !strings.Contains(out, "snapshot <create|compare|list|delete>") {
		t.Fatalf("snapshot no-sub usage wrong: %q", out)
	}
}

// Mutation: in cmdSnapshot create, drop the missing-file error return — reading
// a non-existent file would not surface exit 1.
func TestCmdSnapshotCreateMissingFileExits1(t *testing.T) {
	withSnapshotDir(t)
	code, _, errb := runCapture("snapshot", "create", "n", "/no/such/file/here")
	if code != 1 {
		t.Fatalf("create missing-file exit = %d, want 1", code)
	}
	if !strings.Contains(errb, "read file:") {
		t.Fatalf("stderr missing read-file diagnostic: %q", errb)
	}
}
