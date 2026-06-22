//go:build integration

package qcow2

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

// requireQemuImg returns a real Manager or honestly skips with the exact
// reason when qemu-img is not installed (per CLAUDE-1: no fake PASS on a
// missing tool — an honest SKIP-with-reason instead).
func requireQemuImg(t *testing.T) *Manager {
	t.Helper()
	if !QemuImgAvailable() {
		t.Skip("SKIP: qemu-img not found on PATH; install qemu (brew install qemu) to run qcow2 integration tests")
	}
	m, err := NewManager()
	if err != nil {
		t.Skipf("SKIP: qemu-img unavailable: %v", err)
	}
	return m
}

// TestRealChainInfoAndDepth builds a base + a stack of overlays with real
// qemu-img and asserts the reported backing chain and depth match, verified
// independently against `qemu-img info --backing-chain`.
func TestRealChainInfoAndDepth(t *testing.T) {
	m := requireQemuImg(t)
	ctx := t.Context()
	dir := t.TempDir()

	base := filepath.Join(dir, "base.qcow2")
	if err := m.CreateBase(ctx, base, "64M"); err != nil {
		t.Fatalf("CreateBase: %v", err)
	}

	// Base must be depth 0.
	if d, err := m.ChainDepth(ctx, base); err != nil || d != 0 {
		t.Fatalf("base ChainDepth = %d, err=%v; want 0", d, err)
	}

	// Build a chain of 5 overlays: each backs the previous.
	prev := base
	const n = 5
	overlays := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		ov := filepath.Join(dir, "ov"+strconv.Itoa(i)+".qcow2")
		if err := m.CreateOverlay(ctx, ov, prev); err != nil {
			t.Fatalf("CreateOverlay #%d: %v", i, err)
		}
		overlays = append(overlays, ov)

		// Depth of this overlay must be i.
		if d, err := m.ChainDepth(ctx, ov); err != nil || d != i {
			t.Fatalf("overlay #%d ChainDepth = %d, err=%v; want %d", i, d, err, i)
		}
		prev = ov
	}

	// ChainInfo on the top overlay must list n+1 entries (overlay..base),
	// top-first, base-last.
	top := overlays[n-1]
	entries, err := m.ChainInfo(ctx, top)
	if err != nil {
		t.Fatalf("ChainInfo: %v", err)
	}
	if len(entries) != n+1 {
		t.Fatalf("ChainInfo len = %d; want %d", len(entries), n+1)
	}
	if entries[0].Filename != top {
		t.Errorf("ChainInfo[0].Filename = %q; want %q", entries[0].Filename, top)
	}
	if entries[len(entries)-1].Filename != base {
		t.Errorf("ChainInfo[last].Filename = %q; want base %q", entries[len(entries)-1].Filename, base)
	}
	if entries[len(entries)-1].BackingFilename != "" {
		t.Errorf("base BackingFilename = %q; want empty", entries[len(entries)-1].BackingFilename)
	}

	// Independent oracle: parse `qemu-img info --backing-chain` text output and
	// count the "backing file:" lines + 1 image == n+1.
	oracle := backingChainCount(t, top)
	if oracle != n+1 {
		t.Errorf("independent oracle chain length = %d; want %d", oracle, n+1)
	}
}

// TestRealDepthLimit creates overlays up to MaxChainDepth (10), which must all
// succeed, then asserts the 11th is rejected with a typed *ChainTooDeepError.
func TestRealDepthLimit(t *testing.T) {
	m := requireQemuImg(t)
	ctx := t.Context()
	dir := t.TempDir()

	base := filepath.Join(dir, "base.qcow2")
	if err := m.CreateBase(ctx, base, "32M"); err != nil {
		t.Fatalf("CreateBase: %v", err)
	}

	prev := base
	// Depths 1..10 must all succeed.
	for i := 1; i <= MaxChainDepth; i++ {
		ov := filepath.Join(dir, "d"+strconv.Itoa(i)+".qcow2")
		if err := m.CreateOverlay(ctx, ov, prev); err != nil {
			t.Fatalf("overlay at depth %d should succeed, got: %v", i, err)
		}
		prev = ov
	}

	// Confirm the top is exactly at the limit.
	if d, err := m.ChainDepth(ctx, prev); err != nil || d != MaxChainDepth {
		t.Fatalf("top depth = %d, err=%v; want %d", d, err, MaxChainDepth)
	}

	// The 11th overlay (depth 11) MUST be rejected.
	eleventh := filepath.Join(dir, "d11.qcow2")
	err := m.CreateOverlay(ctx, eleventh, prev)
	if err == nil {
		t.Fatal("creating the 11th overlay (depth 11) succeeded; depth limit NOT enforced")
	}
	if !errors.Is(err, ErrChainTooDeep) {
		t.Fatalf("11th overlay error = %v; want errors.Is ErrChainTooDeep", err)
	}
	var ctd *ChainTooDeepError
	if !errors.As(err, &ctd) {
		t.Fatalf("11th overlay error = %v; want *ChainTooDeepError", err)
	}
	if ctd.AttemptedDepth != 11 || ctd.MaxDepth != MaxChainDepth {
		t.Errorf("ChainTooDeepError = %+v; want AttemptedDepth=11 MaxDepth=%d", ctd, MaxChainDepth)
	}

	// And the rejected overlay file must NOT exist (no partial creation).
	if fileExists(eleventh) {
		t.Errorf("rejected overlay %q was created on disk; want absent", eleventh)
	}
}

// TestRealCommit commits a top overlay into its backing image and asserts the
// backing image's chain is unchanged in length (its own backing remains) but
// the committed data is collapsed down; then commits the single-overlay-on-base
// case and asserts the base chain shortens to depth 0 content-wise.
func TestRealCommit(t *testing.T) {
	m := requireQemuImg(t)
	ctx := t.Context()
	dir := t.TempDir()

	base := filepath.Join(dir, "base.qcow2")
	if err := m.CreateBase(ctx, base, "64M"); err != nil {
		t.Fatalf("CreateBase: %v", err)
	}
	ov1 := filepath.Join(dir, "ov1.qcow2")
	if err := m.CreateOverlay(ctx, ov1, base); err != nil {
		t.Fatalf("CreateOverlay ov1: %v", err)
	}
	ov2 := filepath.Join(dir, "ov2.qcow2")
	if err := m.CreateOverlay(ctx, ov2, ov1); err != nil {
		t.Fatalf("CreateOverlay ov2: %v", err)
	}

	// Before commit: ov2 has depth 2 (ov2 -> ov1 -> base).
	if d, err := m.ChainDepth(ctx, ov2); err != nil || d != 2 {
		t.Fatalf("ov2 depth before commit = %d, err=%v; want 2", d, err)
	}

	// Commit ov2 into ov1. ov2's deltas move into ov1; the live chain we now
	// use is ov1 -> base, which is depth 1 (shorter than ov2's depth 2).
	if err := m.Commit(ctx, ov2); err != nil {
		t.Fatalf("Commit ov2: %v", err)
	}
	if d, err := m.ChainDepth(ctx, ov1); err != nil || d != 1 {
		t.Fatalf("ov1 depth after committing ov2 = %d, err=%v; want 1", d, err)
	}

	// Now commit ov1 into base; the live image becomes base alone, depth 0.
	if err := m.Commit(ctx, ov1); err != nil {
		t.Fatalf("Commit ov1: %v", err)
	}
	if d, err := m.ChainDepth(ctx, base); err != nil || d != 0 {
		t.Fatalf("base depth after committing ov1 = %d, err=%v; want 0", d, err)
	}

	// Independent oracle: base now has no backing chain.
	if c := backingChainCount(t, base); c != 1 {
		t.Errorf("oracle base chain length = %d; want 1 (base only)", c)
	}

	// Committing a base (no backing) must be rejected.
	if err := m.Commit(ctx, base); err == nil {
		t.Error("Commit on base with no backing file should fail")
	}
}

// backingChainCount independently counts the number of images in path's
// backing chain using a fresh `qemu-img info --backing-chain --output=json`
// invocation parsed independently of the code under test (the production code
// uses typed structs; here we decode into a generic []map to avoid sharing
// logic).
func backingChainCount(t *testing.T, path string) int {
	t.Helper()
	out, err := exec.Command("qemu-img", "info", "--backing-chain", "--output=json", path).Output()
	if err != nil {
		t.Fatalf("oracle qemu-img info failed for %q: %v", path, err)
	}
	var chain []map[string]any
	if err := json.Unmarshal(out, &chain); err != nil {
		t.Fatalf("oracle JSON parse for %q: %v\n%s", path, err, out)
	}
	return len(chain)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
