package cache

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// These adversarial tests probe content-integrity and safety guarantees of the
// content-addressable cache. Each test documents whether it pins a REAL BUG, a
// DOCUMENTED CONTRACT, or a LATENT RISK.

// --- HXC-1908 REGRESSION GUARD 1: DiskCache must NOT let a path-traversal key
// collide with / overwrite a distinct legitimate key.
//
// Before the fix, digestPath joined root, digest[:2], digest WITHOUT sanitizing
// the key, so two DISTINCT key strings could resolve to the SAME on-disk file
// (the cache returned the WRONG artifact — a content-integrity violation). The
// fix (validDigest) rejects any non-hex key, so a traversal key like "ab/../ab"
// is refused by Put and can never clobber the legitimate "ab" entry.
// This test FAILS if validDigest is removed (mutation proof of load-bearing).
func TestDiskCache_DistinctKeysDoNotCollide_HXC1908(t *testing.T) {
	root := t.TempDir()
	c, err := NewDiskCache(root)
	if err != nil {
		t.Fatalf("new disk cache: %v", err)
	}

	keyA := "ab"       // valid lowercase-hex content key
	keyB := "ab/../ab" // distinct string that previously cleaned to keyA's path

	if err := c.Put(keyA, []byte("ARTIFACT-A")); err != nil {
		t.Fatalf("put A (valid hex key) must succeed: %v", err)
	}
	// The traversal key must be REJECTED, not silently routed onto keyA's file.
	if err := c.Put(keyB, []byte("ARTIFACT-B")); err == nil {
		t.Fatalf("Put(%q) must be rejected as an invalid digest (path traversal); got nil error", keyB)
	}

	got, err := c.Get(keyA)
	if err != nil {
		t.Fatalf("get A: %v", err)
	}
	// keyA's artifact must be intact — never overwritten by the rejected keyB.
	if !bytes.Equal(got, []byte("ARTIFACT-A")) {
		t.Fatalf("content-integrity violation: Get(%q)=%q, want ARTIFACT-A "+
			"(a distinct traversal key collided onto keyA — validDigest guard removed?)", keyA, got)
	}
}

// --- HXC-1908 REGRESSION GUARD 2: DiskCache.Put must NOT escape the cache root ---
//
// Before the fix, a key containing ".." escaped the cache root and Put reported
// success, silently overwriting a file OUTSIDE the cache. The fix (validDigest)
// rejects any non-hex key, so Put("../victim") is refused and the out-of-root
// sentinel is never touched. FAILS if validDigest is removed.
func TestDiskCache_PutDoesNotEscapeRoot_HXC1908(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "cacheroot")
	c, err := NewDiskCache(root)
	if err != nil {
		t.Fatalf("new disk cache: %v", err)
	}

	key := "../victim"

	// Seed a sentinel exactly where the UNSANITIZED key would have landed (one
	// level above root), so a regression that re-enables traversal is observable.
	dest := filepath.Clean(filepath.Join(root, key[:2], key))
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		t.Fatalf("mkdir dest parent: %v", err)
	}
	if err := os.WriteFile(dest, []byte("ORIGINAL"), 0644); err != nil {
		t.Fatalf("seed victim: %v", err)
	}
	defer os.Remove(dest)

	// The traversal key must be REJECTED by Put.
	if err := c.Put(key, []byte("OVERWRITTEN")); err == nil {
		t.Fatalf("Put(%q) must be rejected as an invalid digest (path traversal); got nil error", key)
	}
	// The out-of-root sentinel must be untouched.
	stored, rerr := os.ReadFile(dest)
	if rerr != nil {
		t.Fatalf("read sentinel: %v", rerr)
	}
	if !bytes.Equal(stored, []byte("ORIGINAL")) {
		t.Fatalf("path traversal: Put(%q) escaped root and overwrote %s (got %q, want ORIGINAL) "+
			"— validDigest guard removed?", key, dest, stored)
	}
}

// --- REAL BUG 3: empty digest is rejected by Put but silently mishandled by
// Has/Delete on DiskCache, operating on the cache ROOT itself.
//
// Put("") correctly errors ("digest cannot be empty"), proving "" is an invalid
// key by the author's own contract. But digestPath("") == root, so:
//   - Has("") reports a phantom hit (root dir always exists)
//   - Delete("") targets the cache root directory itself (destructive intent)
// A consistent cache must treat "" as not-present and a no-op to delete.
func TestDiskCache_EmptyDigestIsNotAPhantomHit(t *testing.T) {
	root := t.TempDir()
	c, err := NewDiskCache(root)
	if err != nil {
		t.Fatalf("new disk cache: %v", err)
	}
	// HXC-1908 REGRESSION GUARD: Has("") must be false. Put("") is rejected, so
	// "" is an invalid key that can never be stored. Before the fix, digestPath("")
	// resolved to the cache root and os.Stat(root) succeeded → a phantom hit. The
	// validDigest guard makes Has("") return false. FAILS if the guard is removed.
	if c.Has("") {
		t.Fatalf("empty-digest phantom hit: Has(\"\")==true (digestPath(\"\") resolves to cache root %s) "+
			"— validDigest guard removed?", root)
	}
}

func TestDiskCache_EmptyDigestDeleteDoesNotTargetRoot(t *testing.T) {
	root := t.TempDir()
	c, err := NewDiskCache(root)
	if err != nil {
		t.Fatalf("new disk cache: %v", err)
	}
	// Seed a real entry so the root is non-empty (observable if root is targeted).
	d := Digest([]byte("real"))
	if err := c.Put(d, []byte("real")); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Delete("") must be a no-op on a non-existent empty key, NOT an attempt to
	// remove the cache root. We assert (a) the seeded entry survives and (b) the
	// root still exists.
	_ = c.Delete("")

	if _, statErr := os.Stat(root); statErr != nil {
		t.Fatalf("destructive: Delete(\"\") removed/altered the cache root %s: %v", root, statErr)
	}
	if !c.Has(d) {
		t.Fatalf("destructive: Delete(\"\") affected unrelated entry %s", d)
	}
}

// --- REAL BUG 4 candidate / verification: empty-digest symmetry on MemoryCache.
// MemoryCache.Put("") errors, and Get/Has/Delete on "" are harmless (plain map
// miss). This documents that the MemoryCache is SAFE here, so the divergence is
// specific to DiskCache's path mapping. (Pins current correct MemoryCache behavior.)
func TestMemoryCache_EmptyDigestIsClean(t *testing.T) {
	c := NewMemoryCache()
	if err := c.Put("", []byte("x")); err == nil {
		t.Fatal("MemoryCache.Put(\"\") must error")
	}
	if c.Has("") {
		t.Fatal("MemoryCache.Has(\"\") must be false")
	}
	if _, err := c.Get(""); err == nil {
		t.Fatal("MemoryCache.Get(\"\") must error")
	}
	if err := c.Delete(""); err != nil {
		t.Fatalf("MemoryCache.Delete(\"\") must be a clean no-op: %v", err)
	}
}

// --- Content-addressing integrity: Put stores a COPY (MemoryCache) so later
// mutation of the caller's slice cannot corrupt the cached artifact. Pins the
// existing defensive copy (already covered, reinforced adversarially here under
// concurrency below).

// --- Concurrency: concurrent Put/Get on MemoryCache must not lose writes, tear
// the map, or hand back a partially-written slice. Run with -race once.
func TestMemoryCache_ConcurrentPutGetNoTornData(t *testing.T) {
	c := NewMemoryCache()
	const writers = 16
	const iters = 500

	// Pre-compute distinct keys and their canonical payloads.
	payload := func(i int) []byte {
		return bytes.Repeat([]byte{byte(i)}, 64+i)
	}
	keys := make([]string, writers)
	for i := 0; i < writers; i++ {
		keys[i] = Digest(payload(i))
	}

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := payload(i)
			k := keys[i]
			for j := 0; j < iters; j++ {
				if err := c.Put(k, p); err != nil {
					t.Errorf("put: %v", err)
					return
				}
				got, err := c.Get(k)
				if err != nil {
					t.Errorf("get: %v", err)
					return
				}
				// The returned slice must equal the canonical payload exactly —
				// never a torn/partial copy.
				if !bytes.Equal(got, p) {
					t.Errorf("torn read for key %d: len(got)=%d want=%d", i, len(got), len(p))
					return
				}
			}
		}(i)
	}
	wg.Wait()

	// Final state: every key present with its exact payload.
	for i := 0; i < writers; i++ {
		got, err := c.Get(keys[i])
		if err != nil {
			t.Fatalf("final get key %d: %v", i, err)
		}
		if !bytes.Equal(got, payload(i)) {
			t.Fatalf("final mismatch key %d", i)
		}
	}
}

// --- Concurrency: independent keys written concurrently are all retained (no
// lost writes / no map corruption under concurrent Put).
func TestMemoryCache_ConcurrentDistinctPutsAllRetained(t *testing.T) {
	c := NewMemoryCache()
	const n = 1000
	var wg sync.WaitGroup
	keys := make([]string, n)
	vals := make([][]byte, n)
	for i := 0; i < n; i++ {
		vals[i] = []byte(fmt.Sprintf("value-%d", i))
		keys[i] = Digest(vals[i])
	}
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = c.Put(keys[i], vals[i])
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		got, err := c.Get(keys[i])
		if err != nil {
			t.Fatalf("lost write: key %d (%s) missing: %v", i, keys[i], err)
		}
		if !bytes.Equal(got, vals[i]) {
			t.Fatalf("corrupted value for key %d", i)
		}
	}
}
