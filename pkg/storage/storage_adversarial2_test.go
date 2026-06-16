package storage

// Second-pass adversarial tests for pkg/storage.
//
// The first pass (storage_adversarial_test.go) covered the SetSessionRouting
// HSet+Expire orphan, MemoryStore/FileStore concurrency conservation, List
// prefix parity, and empty-vs-missing. This pass probes OTHER correctness and
// boundary properties the first pass did not:
//
//	(A) PATH TRAVERSAL / STORAGE-BOUNDARY ESCAPE — REAL BUG (now fixed).
//	    FileStore maps opaque keys onto a filesystem path via filepath.Join,
//	    which cleans "../" segments but does NOT confine the result to the root.
//	    A key like "../../etc/passwd" (or a malformed session id) resolved to a
//	    path OUTSIDE the store root, so Put/Get/Delete could write, read, or
//	    delete arbitrary files outside the configured store directory. That
//	    breaks the storage boundary that every higher layer assumes. Fixed in
//	    keyPath (returns ErrUnsafeKey). These tests mutation-prove the fix.
//
//	(B) DELETE ROUND-TRIP — a deleted key must be FULLY gone: Get→ErrKeyNotFound
//	    and List must not surface it. (no leak, no dangling visibility.)
//
//	(C) OVERWRITE EXACTNESS — Put over an existing (longer) value replaces it
//	    byte-exactly with no stale trailing bytes (no torn record).
//
//	(D) NO-ALIASING — MemoryStore must copy on both Put and Get so a caller
//	    mutating the input or the returned slice cannot corrupt stored state.
//	    Pinned as a regression guard (currently correct).
//
//	(E) COMPENSATION ACCOUNTING — when SetSessionRouting SUCCEEDS, it must NOT
//	    fire the Del compensation; the just-written route must survive with its
//	    TTL. (Guards against an over-eager rollback deleting good state.)
//
//	(F) EMPTY-DIR LEAK — LATENT RISK pin (not a fix): FileStore.Delete removes
//	    the file but leaves now-empty parent directories. It does not corrupt
//	    List or round-trips, so it is recorded, not asserted-against.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// (A) PATH TRAVERSAL — REAL BUG, mutation-proven
// ─────────────────────────────────────────────────────────────────────────────

// traversalKeys are keys that, under the old filepath.Join-only mapping, would
// resolve OUTSIDE the store root.
var traversalKeys = []string{
	"../escape",
	"../../escape2",
	"a/../../escape3",
	"sub/../../../escape4",
}

// TestFileStore_Put_RejectsTraversal_NoEscape is the load-bearing sink-side
// probe: Put of a traversing key MUST be rejected (ErrUnsafeKey) AND MUST NOT
// create any file outside the store root.
//
// Mutation proof: revert keyPath to `return filepath.Join(s.root, ...)` (no
// confinement check) and Put returns nil while a file appears at
// <parent>/escape — this test FAILS on both the error check and the sink-side
// os.Stat. With the fix it PASSES.
func TestFileStore_Put_RejectsTraversal_NoEscape(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Dir(root)
	s, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	for _, key := range traversalKeys {
		// The escaped path is whatever the unsafe Join would have produced.
		escaped := filepath.Join(root, filepath.FromSlash(key))
		_ = os.Remove(escaped) // ensure clean slate

		err := s.Put(key, []byte("PWNED"))
		if !errors.Is(err, ErrUnsafeKey) {
			t.Errorf("Put(%q): err = %v, want ErrUnsafeKey", key, err)
		}
		// SINK-SIDE: no file may exist outside the root as a result.
		if fi, statErr := os.Stat(escaped); statErr == nil && !fi.IsDir() {
			content, _ := os.ReadFile(escaped)
			_ = os.Remove(escaped)
			t.Errorf("BOUNDARY ESCAPE: Put(%q) wrote a file outside the store root at %q (content=%q)",
				key, escaped, content)
		}
		// And nothing leaked directly under the parent either.
		if _, statErr := os.Stat(filepath.Join(parent, "escape")); statErr == nil {
			t.Errorf("BOUNDARY ESCAPE: Put(%q) created %q outside root", key, filepath.Join(parent, "escape"))
		}
	}
}

// TestFileStore_Get_RejectsTraversal proves Get of a traversing key cannot read
// a file outside the store root: it returns ErrUnsafeKey rather than the
// contents of the escaped path.
//
// Mutation proof: without the confinement check, Get("../secret") reads the
// planted out-of-root file and returns its bytes → this test FAILS (it would
// get the secret bytes and a nil error). With the fix it PASSES.
func TestFileStore_Get_RejectsTraversal(t *testing.T) {
	root := t.TempDir()
	s, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	// Plant a secret in the parent dir, the exact path "../secret" would target.
	secretPath := filepath.Join(filepath.Dir(root), "secret")
	if err := os.WriteFile(secretPath, []byte("TOP-SECRET"), 0600); err != nil {
		t.Fatalf("plant secret: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(secretPath) })

	got, err := s.Get("../secret")
	if !errors.Is(err, ErrUnsafeKey) {
		t.Fatalf("Get(\"../secret\"): err = %v, want ErrUnsafeKey", err)
	}
	if got != nil {
		t.Fatalf("BOUNDARY ESCAPE: Get(\"../secret\") returned out-of-root data %q", got)
	}
}

// TestFileStore_Delete_RejectsTraversal proves Delete of a traversing key cannot
// remove a file outside the store root.
//
// Mutation proof: without the check, Delete("../victim") os.Remove's the planted
// out-of-root file → this test FAILS (the victim is gone). With the fix the
// victim survives and Delete returns ErrUnsafeKey → PASS.
func TestFileStore_Delete_RejectsTraversal(t *testing.T) {
	root := t.TempDir()
	s, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	victimPath := filepath.Join(filepath.Dir(root), "victim")
	if err := os.WriteFile(victimPath, []byte("important"), 0600); err != nil {
		t.Fatalf("plant victim: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(victimPath) })

	err = s.Delete("../victim")
	if !errors.Is(err, ErrUnsafeKey) {
		t.Fatalf("Delete(\"../victim\"): err = %v, want ErrUnsafeKey", err)
	}
	// SINK-SIDE: the out-of-root victim must still exist.
	if _, statErr := os.Stat(victimPath); statErr != nil {
		t.Fatalf("BOUNDARY ESCAPE: Delete(\"../victim\") removed out-of-root file %q: %v",
			victimPath, statErr)
	}
}

// TestFileStore_SafeKeysStillWork guards the fix from over-rejecting: ordinary
// nested keys, keys with internal (non-escaping) "." segments, and keys that
// merely share a textual prefix with the root name must all still round-trip.
func TestFileStore_SafeKeysStillWork(t *testing.T) {
	root := t.TempDir()
	s, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	safe := map[string]string{
		"flat":             "1",
		"a/b/c":            "2",
		"sess-1/node":      "3",
		"p/./q":            "4", // internal "." cleans to p/q, stays inside root
		"deep/x/../y":      "5", // cleans to deep/y, stays inside root
		"normalwith..dots": "6", // ".." not as a path segment — must be allowed
	}
	for k, v := range safe {
		if err := s.Put(k, []byte(v)); err != nil {
			t.Fatalf("Put(%q) unexpectedly rejected a safe key: %v", k, err)
		}
		got, err := s.Get(k)
		if err != nil {
			t.Fatalf("Get(%q) after Put: %v", k, err)
		}
		if string(got) != v {
			t.Fatalf("Get(%q) = %q, want %q", k, got, v)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// (B) DELETE ROUND-TRIP — fully removes, no dangling visibility
// ─────────────────────────────────────────────────────────────────────────────

// TestFileStore_DeleteFullyRemoves asserts a deleted key is gone from BOTH
// access paths: Get returns ErrKeyNotFound and List no longer surfaces it.
// A delete that removed the file but left it List-visible (or vice versa) would
// be a dangling-mapping leak.
func TestFileStore_DeleteFullyRemoves(t *testing.T) {
	s, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err := s.Put("ns/key", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Precondition: visible both ways.
	if _, err := s.Get("ns/key"); err != nil {
		t.Fatalf("pre-delete Get: %v", err)
	}
	list, _ := s.List("ns")
	if len(list) != 1 || list[0] != "ns/key" {
		t.Fatalf("pre-delete List = %v, want [ns/key]", list)
	}

	if err := s.Delete("ns/key"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := s.Get("ns/key"); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("post-delete Get err = %v, want ErrKeyNotFound (key not fully removed)", err)
	}
	list, _ = s.List("ns")
	if len(list) != 0 {
		t.Fatalf("post-delete List = %v, want empty (dangling List visibility)", list)
	}
}

// TestMemoryStore_DeleteFullyRemoves is the in-memory counterpart.
func TestMemoryStore_DeleteFullyRemoves(t *testing.T) {
	s := NewMemoryStore()
	if err := s.Put("ns/key", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Delete("ns/key"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get("ns/key"); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("post-delete Get err = %v, want ErrKeyNotFound", err)
	}
	if list, _ := s.List("ns"); len(list) != 0 {
		t.Fatalf("post-delete List = %v, want empty", list)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// (C) OVERWRITE EXACTNESS — no stale trailing bytes
// ─────────────────────────────────────────────────────────────────────────────

// TestFileStore_OverwriteShorterValue_Exact proves that overwriting a longer
// stored value with a shorter one yields EXACTLY the new bytes — no leftover
// tail from the previous write (a torn record). os.WriteFile truncates, but
// this pins it as a contract because a naive append/seek implementation would
// regress here.
func TestFileStore_OverwriteShorterValue_Exact(t *testing.T) {
	s, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err := s.Put("k", []byte("a-very-long-original-value")); err != nil {
		t.Fatalf("Put long: %v", err)
	}
	if err := s.Put("k", []byte("short")); err != nil {
		t.Fatalf("Put short: %v", err)
	}
	got, err := s.Get("k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "short" {
		t.Fatalf("overwrite not exact: Get = %q, want %q (stale trailing bytes)", got, "short")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// (D) NO-ALIASING — MemoryStore must copy in and out (regression guard)
// ─────────────────────────────────────────────────────────────────────────────

// TestMemoryStore_NoAliasing proves the stored bytes are isolated from both the
// caller's input slice (after Put) and the caller's returned slice (after Get).
// Aliasing here would let one goroutine's later mutation silently corrupt
// another's stored state — a torn read/lost update under concurrency.
func TestMemoryStore_NoAliasing(t *testing.T) {
	s := NewMemoryStore()

	in := []byte("world")
	if err := s.Put("k", in); err != nil {
		t.Fatalf("Put: %v", err)
	}
	in[0] = 'Z' // mutate the caller's slice AFTER Put
	got, _ := s.Get("k")
	if string(got) != "world" {
		t.Fatalf("input aliasing: stored value mutated to %q via caller's input slice", got)
	}

	got2, _ := s.Get("k")
	got2[0] = 'X' // mutate the returned slice
	got3, _ := s.Get("k")
	if string(got3) != "world" {
		t.Fatalf("output aliasing: stored value mutated to %q via a returned slice", got3)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// (E) COMPENSATION ACCOUNTING — success path must NOT roll back
// ─────────────────────────────────────────────────────────────────────────────

// countingSeam records how many times Del is invoked so we can assert the
// SetSessionRouting compensation fires ONLY on the Expire-failure path and
// never on the happy path (which would delete the route it just wrote).
type countingSeam struct {
	mu      sync.Mutex
	store   map[string]map[string]string
	ttld    map[string]bool
	delHits int
}

func newCountingSeam() *countingSeam {
	return &countingSeam{store: map[string]map[string]string{}, ttld: map[string]bool{}}
}

func (c *countingSeam) HSet(_ context.Context, key string, fields map[string]string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make(map[string]string, len(fields))
	for k, v := range fields {
		cp[k] = v
	}
	c.store[key] = cp
	return nil
}

func (c *countingSeam) Expire(_ context.Context, key string, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ttld[key] = true
	return nil
}

func (c *countingSeam) Del(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.delHits++
	delete(c.store, key)
	delete(c.ttld, key)
	return nil
}

func (c *countingSeam) HGetAll(_ context.Context, key string) (map[string]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make(map[string]string, len(c.store[key]))
	for k, v := range c.store[key] {
		cp[k] = v
	}
	return cp, nil
}

func (c *countingSeam) Publish(_ context.Context, _, _ string) error { return nil }
func (c *countingSeam) Subscribe(_ context.Context, _ string) (<-chan string, error) {
	ch := make(chan string)
	close(ch)
	return ch, nil
}

// TestSetSessionRouting_Success_NoCompensation asserts the happy path persists
// the route WITH a TTL and does NOT call Del. An over-eager rollback that fired
// on success would silently drop a freshly-written, valid session route.
func TestSetSessionRouting_Success_NoCompensation(t *testing.T) {
	seam := newCountingSeam()
	store := newRedisStoreFromSeam(seam)

	err := store.SetSessionRouting(context.Background(), "sess-ok",
		map[string]string{"target_node": "node-1"}, 30*time.Second)
	if err != nil {
		t.Fatalf("SetSessionRouting (happy path): %v", err)
	}
	if seam.delHits != 0 {
		t.Fatalf("compensation Del fired %d time(s) on the SUCCESS path; it must only "+
			"compensate on Expire failure", seam.delHits)
	}
	key := RoutingKey("sess-ok")
	if !seam.ttld[key] {
		t.Fatalf("route persisted without a TTL on the success path")
	}
	got, _ := store.GetSessionRouting(context.Background(), "sess-ok")
	if got["target_node"] != "node-1" {
		t.Fatalf("route not readable after successful SetSessionRouting: %v", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// (F) EMPTY-DIR LEAK — LATENT RISK pin (no fix asserted)
// ─────────────────────────────────────────────────────────────────────────────

// TestFileStore_DeleteLeavesEmptyDirs_LatentRisk pins — without asserting a fix —
// that FileStore.Delete removes the file but leaves now-empty parent directories
// behind. This does NOT corrupt List (which skips directories) or any round-trip;
// it is a benign resource-accumulation quirk recorded so a future change to prune
// empty dirs (or to keep them deliberately) is a reviewed decision.
func TestFileStore_DeleteLeavesEmptyDirs_LatentRisk(t *testing.T) {
	root := t.TempDir()
	s, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err := s.Put("nested/deep/key", []byte("x")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Delete("nested/deep/key"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	dir := filepath.Join(root, "nested", "deep")
	fi, statErr := os.Stat(dir)
	if statErr != nil || !fi.IsDir() {
		t.Skip("FileStore now prunes empty dirs after delete — latent risk resolved; " +
			"update this pin to assert the new contract")
	}
	t.Logf("LATENT RISK pinned: FileStore.Delete leaves empty dir %q after removing the "+
		"only file under it. Harmless to List/round-trips but accumulates empty dirs.", dir)

	// Confirm the harmlessness claim: the deleted key is gone from List.
	if list, _ := s.List("nested"); len(list) != 0 {
		t.Fatalf("empty-dir leak is NOT harmless: List still surfaces %v after delete", list)
	}
}
