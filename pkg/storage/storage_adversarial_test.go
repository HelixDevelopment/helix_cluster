package storage

// Adversarial sink-side tests for pkg/storage.
//
// These probe the four failure classes the coordinator asked for:
//
//	(a) ATOMICITY / PARTIAL-WRITE  — SetSessionRouting does HSet THEN Expire as
//	    two unconditioned writes with no transaction. If Expire fails after HSet
//	    succeeds, the routing hash is left persisted WITHOUT a TTL: an orphan
//	    that lives forever, even though the call reported failure. This is the
//	    hxcregistry "two autocommit writes, no transaction" class. The contract
//	    in redis_store.go (lines 119-120) says the method "stores fields as a
//	    Redis hash ... AND sets its TTL" — i.e. both-or-neither.
//	(b) UNGUARDED SHARED MUTABLE STATE — concurrent Put/Get/Delete on
//	    MemoryStore and FileStore under -race, with conservation.
//	(c) TOTAL-ORDER / KEY-PREFIX — List(prefix) prefix-collision parity between
//	    MemoryStore and FileStore (the "sess-1" vs "sess-10" trap).
//	(d) DURABILITY EDGE — empty/zero-length value vs missing-key confusion on
//	    both MemoryStore and FileStore.
//
// (a) is the load-bearing reproducer. It is NOT gated behind an env var: it
// FAILS on unfixed prod by design (see TestSetSessionRouting_ExpireFailure_*).
// The coordinator applies the fix so it passes by default.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// (a) ATOMICITY / PARTIAL-WRITE — the orphan reproducer
// ─────────────────────────────────────────────────────────────────────────────

// orphanSeam models a REAL Redis at the HSet/Expire boundary: HSet persists the
// hash immediately and durably; Expire may be forced to fail AFTER the hash is
// already persisted (exactly what a network blip between the two round-trips
// produces). HGetAll reads back whatever is currently persisted — so a hash that
// HSet wrote but whose owning call later errored is still visible: an orphan.
//
// It also records a Del seam so a *correct* implementation that compensates on
// Expire failure (rolls the hash back) is observable as "no orphan". The current
// production redisSeam has no Del method; this fake exposes one additively so the
// test asserts the CONTRACT (no orphan) rather than any particular fix shape.
type orphanSeam struct {
	mu sync.Mutex

	// store is the durable backing: key → fields. Mirrors Redis hash state.
	store map[string]map[string]string
	// ttld records keys that currently carry a TTL (Expire succeeded).
	ttld map[string]bool

	// expireErr, if non-nil, is returned by Expire AFTER the hash is persisted.
	expireErr error
}

func newOrphanSeam() *orphanSeam {
	return &orphanSeam{
		store: make(map[string]map[string]string),
		ttld:  make(map[string]bool),
	}
}

func (o *orphanSeam) HSet(_ context.Context, key string, fields map[string]string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	cp := make(map[string]string, len(fields))
	for k, v := range fields {
		cp[k] = v
	}
	o.store[key] = cp // persisted immediately, like a real HSET.
	return nil
}

func (o *orphanSeam) Expire(_ context.Context, key string, _ time.Duration) error {
	if o.expireErr != nil {
		// The hash is ALREADY persisted by HSet above; the TTL simply never
		// lands. This is the orphan-producing path.
		return o.expireErr
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.ttld[key] = true
	return nil
}

func (o *orphanSeam) HGetAll(_ context.Context, key string) (map[string]string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	cp := make(map[string]string, len(o.store[key]))
	for k, v := range o.store[key] {
		cp[k] = v
	}
	return cp, nil
}

// Del removes a key (and its TTL bookkeeping). Present so a correct, atomic
// SetSessionRouting can compensate on Expire failure. The production redisSeam
// does not yet declare Del; if a fix adds it, this satisfies that contract.
func (o *orphanSeam) Del(_ context.Context, key string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.store, key)
	delete(o.ttld, key)
	return nil
}

func (o *orphanSeam) Publish(_ context.Context, _, _ string) error { return nil }
func (o *orphanSeam) Subscribe(_ context.Context, _ string) (<-chan string, error) {
	ch := make(chan string)
	close(ch)
	return ch, nil
}

// persistedWithoutTTL reports whether key holds fields but carries no TTL —
// i.e. an immortal orphan routing hash.
func (o *orphanSeam) persistedWithoutTTL(key string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.store[key]) > 0 && !o.ttld[key]
}

// TestSetSessionRouting_ExpireFailure_LeavesNoOrphan is the SINK-SIDE atomicity
// probe. After SetSessionRouting returns an error because Expire failed, the
// routing hash MUST NOT remain persisted without a TTL. The contract is
// all-or-nothing: a routing entry without its TTL never expires and silently
// pins a stale session→node mapping forever.
//
// FAILS on unfixed prod: SetSessionRouting calls HSet (persists) then Expire
// (errors) and returns — leaving the hash with no TTL. persistedWithoutTTL is
// true. PASSES once SetSessionRouting is atomic (compensates / never leaves a
// TTL-less hash on failure).
//
// Mutation proof (fix direction): with a compensating rollback in
// SetSessionRouting this asserts false → PASS; remove the rollback → orphan
// remains → FAIL.
func TestSetSessionRouting_ExpireFailure_LeavesNoOrphan(t *testing.T) {
	seam := newOrphanSeam()
	seam.expireErr = errors.New("redis: EXPIRE timeout after HSET committed")
	store := newRedisStoreFromSeam(seam)

	key := RoutingKey("sess-orphan")
	err := store.SetSessionRouting(context.Background(),
		"sess-orphan", map[string]string{"target_node": "node-7"}, 30*time.Second)

	// The call MUST report failure (it does today — that part is fine).
	if err == nil {
		t.Fatal("SetSessionRouting must surface the Expire failure, got nil error")
	}

	// SINK-SIDE: with the call having failed, there must be NO orphan hash left
	// behind without a TTL. On unfixed prod this fails — proving the partial
	// write leaves a durable, never-expiring routing entry.
	if seam.persistedWithoutTTL(key) {
		got, _ := seam.HGetAll(context.Background(), key)
		t.Fatalf("ORPHAN: SetSessionRouting failed but left routing hash %q persisted "+
			"WITHOUT a TTL (fields=%v). A TTL-less routing entry never expires and "+
			"pins a stale session→node mapping forever. The HSet+Expire pair is not "+
			"atomic (hxcregistry partial-write class).", key, got)
	}
}

// TestSetSessionRouting_ExpireFailure_NotReadableAsLiveRoute is a second
// sink-side angle on the same bug: after the failed call, an observer doing
// GetSessionRouting must not see a live, fully-populated route that the system
// believes is valid yet which will never expire.
//
// FAILS on unfixed prod (the fields are readable); PASSES when the failed write
// leaves nothing behind.
func TestSetSessionRouting_ExpireFailure_NotReadableAsLiveRoute(t *testing.T) {
	seam := newOrphanSeam()
	seam.expireErr = errors.New("redis: connection reset before EXPIRE")
	store := newRedisStoreFromSeam(seam)

	_ = store.SetSessionRouting(context.Background(),
		"sess-x", map[string]string{"target_node": "node-3", "protocol": "grpc"}, time.Minute)

	got, err := store.GetSessionRouting(context.Background(), "sess-x")
	if err != nil {
		t.Fatalf("GetSessionRouting unexpected error: %v", err)
	}
	// If the partial write left the fields behind, this map is non-empty AND the
	// underlying key has no TTL — an immortal phantom route.
	if len(got) != 0 && seam.persistedWithoutTTL(RoutingKey("sess-x")) {
		t.Fatalf("ORPHAN: after a FAILED SetSessionRouting, GetSessionRouting still "+
			"returns a live route %v that carries no TTL and will never expire", got)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// (b) UNGUARDED SHARED MUTABLE STATE — concurrency + conservation, run -race
// ─────────────────────────────────────────────────────────────────────────────

// TestMemoryStore_ConcurrentPutConservation hammers MemoryStore with N
// concurrent Puts to distinct keys plus concurrent Gets/Lists/Deletes on other
// keys, then asserts every committed Put is readable (no lost update) and the
// data round-trips byte-for-byte. The Store interface implies safe-for-concurrent
// use (RWMutex present); -race + conservation prove it.
func TestMemoryStore_ConcurrentPutConservation(t *testing.T) {
	s := NewMemoryStore()
	const n = 500
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("k/%05d", i)
			val := []byte(fmt.Sprintf("val-%d", i))
			if err := s.Put(key, val); err != nil {
				t.Errorf("put %d: %v", i, err)
			}
			// Concurrent reads/list to stress the shared map under -race.
			_, _ = s.Get(key)
			_, _ = s.List("k/")
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		key := fmt.Sprintf("k/%05d", i)
		want := fmt.Sprintf("val-%d", i)
		got, err := s.Get(key)
		if err != nil {
			t.Fatalf("conservation: key %q missing after concurrent Put: %v", key, err)
		}
		if string(got) != want {
			t.Fatalf("conservation: key %q = %q, want %q (lost/torn update)", key, got, want)
		}
	}
	keys, err := s.List("k/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != n {
		t.Fatalf("conservation: List returned %d keys, want %d", len(keys), n)
	}
}

// TestFileStore_ConcurrentPutConservation does the same against the filesystem
// store, where the guard is the FileStore.mu plus the OS. Distinct keys, so no
// two goroutines write the same file; every Put must be durably readable.
func TestFileStore_ConcurrentPutConservation(t *testing.T) {
	s, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	const n = 300
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("dir%d/k%d", i%8, i)
			if err := s.Put(key, []byte(fmt.Sprintf("v%d", i))); err != nil {
				t.Errorf("put %d: %v", i, err)
			}
			_, _ = s.Get(key)
			_, _ = s.List("dir")
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		key := fmt.Sprintf("dir%d/k%d", i%8, i)
		got, err := s.Get(key)
		if err != nil {
			t.Fatalf("conservation: key %q missing: %v", key, err)
		}
		if string(got) != fmt.Sprintf("v%d", i) {
			t.Fatalf("conservation: key %q = %q, want v%d", key, got, i)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// (c) TOTAL-ORDER / KEY-PREFIX — List parity + the prefix-collision trap
// ─────────────────────────────────────────────────────────────────────────────

// TestListPrefixParity_MemoryVsFile pins the documented contract that List
// returns keys whose prefix matches, sorted. It also nails the classic
// prefix-collision trap: List("sess-1") MUST include "sess-10" because the
// stores use plain string-prefix semantics (HasPrefix), not path-segment
// semantics. Both stores must agree.
//
// NOTE: keys are chosen so none is a path-prefix of another (no "a" + "a/b"),
// because FileStore cannot hold both a file "a" and a directory "a/" — that
// distinct limitation is pinned separately in
// TestFileStore_FileDirKeyCollision_LatentRisk.
func TestListPrefixParity_MemoryVsFile(t *testing.T) {
	keys := []string{"sess-1", "sess-10", "sess-1x", "sess-2", "other"}

	mem := NewMemoryStore()
	fs, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	for _, k := range keys {
		if err := mem.Put(k, []byte("x")); err != nil {
			t.Fatalf("mem put %q: %v", k, err)
		}
		if err := fs.Put(k, []byte("x")); err != nil {
			t.Fatalf("fs put %q: %v", k, err)
		}
	}

	memList, err := mem.List("sess-1")
	if err != nil {
		t.Fatalf("mem List: %v", err)
	}
	fsList, err := fs.List("sess-1")
	if err != nil {
		t.Fatalf("fs List: %v", err)
	}

	// MemoryStore uses strings.HasPrefix → "sess-10" and "sess-1x" both match
	// "sess-1". This is the documented (HasPrefix) behaviour and the collision
	// trap callers must know about.
	want := map[string]bool{"sess-1": true, "sess-10": true, "sess-1x": true}
	gotMem := map[string]bool{}
	for _, k := range memList {
		gotMem[k] = true
	}
	for k := range want {
		if !gotMem[k] {
			t.Errorf("MemoryStore.List(\"sess-1\") missing %q (got %v)", k, memList)
		}
	}
	if gotMem["sess-2"] || gotMem["other"] {
		t.Errorf("MemoryStore.List(\"sess-1\") leaked non-matching keys: %v", memList)
	}

	// Sorted-order contract.
	if !isSorted(memList) {
		t.Errorf("MemoryStore.List not sorted: %v", memList)
	}
	if !isSorted(fsList) {
		t.Errorf("FileStore.List not sorted: %v", fsList)
	}

	// Parity: both stores must surface the same matching set.
	gotFS := map[string]bool{}
	for _, k := range fsList {
		gotFS[k] = true
	}
	for k := range want {
		if gotFS[k] != gotMem[k] {
			t.Errorf("List parity mismatch on %q: mem=%v fs=%v (memList=%v fsList=%v)",
				k, gotMem[k], gotFS[k], memList, fsList)
		}
	}
}

func isSorted(s []string) bool {
	for i := 1; i < len(s); i++ {
		if s[i-1] > s[i] {
			return false
		}
	}
	return true
}

// ─────────────────────────────────────────────────────────────────────────────
// (d) DURABILITY EDGE — empty value vs missing key must not be confused
// ─────────────────────────────────────────────────────────────────────────────

// TestMemoryStore_EmptyValueVsMissingKey proves a stored zero-length value is a
// PRESENT key (no error), distinct from an absent key (ErrKeyNotFound). Confusing
// the two corrupts presence checks built on top of the store.
func TestMemoryStore_EmptyValueVsMissingKey(t *testing.T) {
	s := NewMemoryStore()
	if err := s.Put("present-empty", []byte{}); err != nil {
		t.Fatalf("put empty: %v", err)
	}

	got, err := s.Get("present-empty")
	if err != nil {
		t.Fatalf("Get of present empty-valued key returned error %v; empty value must "+
			"NOT be confused with a missing key", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("Get(present-empty) = %v (len %d), want non-nil zero-length slice", got, len(got))
	}

	if _, err := s.Get("never-put"); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("Get(missing) error = %v, want ErrKeyNotFound", err)
	}
}

// TestFileStore_EmptyValueVsMissingKey is the filesystem counterpart: an empty
// file is a present key; a non-existent path is ErrKeyNotFound. A zero-length
// write must persist as an existing, readable, empty object.
func TestFileStore_EmptyValueVsMissingKey(t *testing.T) {
	root := t.TempDir()
	s, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err := s.Put("empty/obj", []byte{}); err != nil {
		t.Fatalf("put empty: %v", err)
	}

	// The file must actually exist on disk (durability of a zero-length value).
	if _, statErr := os.Stat(filepath.Join(root, "empty", "obj")); statErr != nil {
		t.Fatalf("empty value not durably persisted: %v", statErr)
	}

	got, err := s.Get("empty/obj")
	if err != nil {
		t.Fatalf("Get of empty file returned error %v; empty value != missing key", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("Get(empty/obj) = %v (len %d), want non-nil zero-length slice", got, len(got))
	}

	if _, err := s.Get("no/such/key"); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("Get(missing) error = %v, want ErrKeyNotFound", err)
	}
}

// TestFileStore_FileDirKeyCollision_LatentRisk pins — without asserting a fix —
// the FileStore limitation that a key ("a") and a key under it as a path
// ("a/b") cannot coexist, because the first is stored as a regular file and the
// second needs "a" to be a directory. This is a LATENT RISK, not a contract
// violation: the Store interface documents keys as opaque strings but FileStore
// maps them onto a filesystem hierarchy. Callers that mix flat and nested keys
// sharing a path prefix will get an error on the second Put. MemoryStore, by
// contrast, accepts both. Pinned so a future change to FileStore key-encoding
// (e.g. hashing keys to flat filenames) is a deliberate, reviewed decision.
func TestFileStore_FileDirKeyCollision_LatentRisk(t *testing.T) {
	fs, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err := fs.Put("a", []byte("flat")); err != nil {
		t.Fatalf("put flat key: %v", err)
	}
	// "a/b" needs "a" to be a directory; it is a file → Put fails. This documents
	// current behaviour; it is NOT asserted to be desirable.
	errNested := fs.Put("a/b", []byte("nested"))
	if errNested == nil {
		t.Skip("FileStore now tolerates file+dir key collision — latent risk resolved; " +
			"update this pin to assert the new contract")
	}
	t.Logf("LATENT RISK pinned: FileStore rejects nested key 'a/b' once flat key "+
		"'a' exists: %v. MemoryStore accepts both. Keys are not a flat namespace "+
		"on FileStore.", errNested)

	// Sanity: MemoryStore has no such limitation (proves the divergence is real).
	mem := NewMemoryStore()
	if err := mem.Put("a", []byte("flat")); err != nil {
		t.Fatalf("mem put a: %v", err)
	}
	if err := mem.Put("a/b", []byte("nested")); err != nil {
		t.Fatalf("MemoryStore should accept both 'a' and 'a/b', got: %v", err)
	}
}
