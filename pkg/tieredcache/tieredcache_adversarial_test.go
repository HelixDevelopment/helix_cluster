package tieredcache

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ============================================================================
// ADVERSARIAL SUITE — eviction-boundary & concurrency probes.
//
// MODEL (re-derived by reading tieredcache.go, not assumed):
//   - Hot tier = map[string]*Record + container/list LRU (Front = MRU, Back = LRU).
//   - Put writes Hot only; hotSet PushFronts then enforceHotCap demotes Back->Warm
//     until len(hot) <= maxHotEntries. Cap enforced on EVERY insert/promote.
//   - Get: Hot lookup MoveToFront on hit; Warm/Cold hit promotes (hotSet) to Hot.
//   - enforceHotCap evicts via c.lru.Back(); a demoted key is written to Warm
//     (never dropped). If Warm.Put fails the key stays Hot (best-effort).
//   - RunMaintenance demotes keys whose lastAccess.Before(now-idleThreshold).
//   - Single sync.Mutex guards all of the above.
//
// GAP vs existing tests:
//   - hotcap_test.go proves the SIZE bound and that the *bulk* tail demotes, but
//     it Puts 20 keys into cap-4 in one burst and never checks the EXACT N+1
//     boundary (does inserting the (N+1)-th key evict exactly ONE victim and is
//     that victim the true LRU, not a just-touched live entry?).
//   - No test pins that a Get-recency touch RESCUES an entry from being the
//     eviction victim (LRU-correctness on the read path, not just write path).
//   - No test pins the TTL boundary: an entry idle EXACTLY idleThreshold must NOT
//     demote (Before is strict); idle by one tick MUST.
//   - coherence_test hammers values; this suite additionally asserts the SIZE
//     bound is never momentarily violated and that key CONSERVATION holds (a Put
//     that returns is always retrievable) under -race.
// ============================================================================

// hotKeysSnapshot returns the set of keys currently resident in Hot (white-box).
func hotKeysSnapshot(c *Cache) map[string]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]bool, len(c.hot))
	for k := range c.hot {
		out[k] = true
	}
	return out
}

// lruOrderBackToFront returns Hot keys in LRU order (Back/LRU first) (white-box).
func lruOrderBackToFront(c *Cache) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for e := c.lru.Back(); e != nil; e = e.Prev() {
		out = append(out, e.Value.(string))
	}
	return out
}

// ----------------------------------------------------------------------------
// TestAdv_CapacityBound_ExactNPlusOneEvictsExactlyOneTrueLRU
//
// SINK-SIDE capacity-boundary probe. Cap = N. Insert k0..k_{N-1} (Hot full at
// exactly N, no eviction yet — proves NOT off-by-one low). Then insert ONE more
// key k_N:
//   (a) size MUST be exactly N (never N+1, never N-1),
//   (b) the SINGLE evicted victim MUST be k0 (the true LRU), demoted to Warm,
//   (c) every other original key k1..k_{N-1} and the new k_N MUST still be Hot.
//
// This bites: holds N+1 (cap off-by-one high), evicts two (over-evict), evicts a
// non-LRU/live entry (wrong victim), or evicts the just-inserted key.
// ----------------------------------------------------------------------------
func TestAdv_CapacityBound_ExactNPlusOneEvictsExactlyOneTrueLRU(t *testing.T) {
	id := runID(t)
	clk := newClock(time.Unix(1_700_000_000, 0))
	const N = 5
	warm := newFakeTier(500 * time.Microsecond)
	cold := newFakeTier(2 * time.Millisecond)
	c, err := New(Config{
		Warm: warm, Cold: cold, IdleThreshold: time.Hour, Now: clk.Now, MaxHotEntries: N,
	})
	if err != nil {
		t.Fatalf("run=%s New: %v", id, err)
	}

	// Fill to exactly N. Insertion order k0 (oldest) .. k4 (newest) => k0 is LRU.
	for i := 0; i < N; i++ {
		if err := c.Put(fmt.Sprintf("k%d", i), []byte(fmt.Sprintf("v%d", i))); err != nil {
			t.Fatalf("run=%s Put k%d: %v", id, i, err)
		}
	}
	// At exactly N: NO eviction yet (boundary not crossed) — proves not off-by-one low.
	if got := c.HotCount(); got != N {
		t.Fatalf("run=%s after filling to N: HotCount=%d, want %d (premature eviction at the boundary)", id, got, N)
	}
	if warm.PutCount() != 0 {
		t.Fatalf("run=%s after filling to N: warm.Puts=%d, want 0 (evicted a live entry before the cap was exceeded)", id, warm.PutCount())
	}

	// Insert the (N+1)-th distinct key: crosses the boundary by exactly one.
	if err := c.Put("kN", []byte("vN")); err != nil {
		t.Fatalf("run=%s Put kN: %v", id, err)
	}

	// (a) size is EXACTLY N — never N+1.
	if got := c.HotCount(); got != N {
		t.Fatalf("run=%s after N+1 insert: HotCount=%d, want %d (capacity off-by-one: cache holds N+1)", id, got, N)
	}
	// LRU list must stay in lock-step with the map.
	if l := lruLen(c); l != N {
		t.Fatalf("run=%s LRU len=%d, want %d (list desynced from map)", id, l, N)
	}

	// (b) EXACTLY ONE eviction, and it is the true LRU (k0).
	if warm.PutCount() != 1 {
		t.Fatalf("run=%s warm.Puts=%d, want exactly 1 (over- or under-eviction at N+1 boundary)", id, warm.PutCount())
	}
	if !warm.Has("k0") {
		t.Fatalf("run=%s wrong victim: k0 (true LRU) not demoted to Warm", id)
	}
	hot := hotKeysSnapshot(c)
	if hot["k0"] {
		t.Fatalf("run=%s LRU victim k0 still resident in Hot", id)
	}
	// (c) every other original key + the new key are still Hot, and none leaked to Warm.
	for i := 1; i < N; i++ {
		k := fmt.Sprintf("k%d", i)
		if !hot[k] {
			t.Fatalf("run=%s live key %s wrongly evicted (should outrank LRU k0)", id, k)
		}
		if warm.Has(k) {
			t.Fatalf("run=%s live key %s wrongly demoted to Warm", id, k)
		}
	}
	if !hot["kN"] {
		t.Fatalf("run=%s just-inserted key kN is not Hot (inserted key was chosen as victim)", id)
	}
	t.Logf("run=%s PASS: N+1 boundary evicts exactly one true-LRU victim (k0); size stays %d", id, N)
}

// ----------------------------------------------------------------------------
// TestAdv_GetRecencyRescuesEntryFromEviction
//
// LRU-correctness on the READ path. Cap = N, Hot full with k0..k_{N-1}
// (k0 is LRU). A Get(k0) MUST move k0 to MRU, so the NEXT inserted key must
// evict k1 (the new LRU), NOT the just-read k0.
//
// Bites a Get that does not refresh recency (touchHot/MoveToFront broken): then
// k0 would be evicted despite being the most-recently-USED entry — a live/just-
// used entry evicted early.
// ----------------------------------------------------------------------------
func TestAdv_GetRecencyRescuesEntryFromEviction(t *testing.T) {
	id := runID(t)
	clk := newClock(time.Unix(1_700_000_000, 0))
	const N = 4
	warm := newFakeTier(500 * time.Microsecond)
	cold := newFakeTier(2 * time.Millisecond)
	c, err := New(Config{
		Warm: warm, Cold: cold, IdleThreshold: time.Hour, Now: clk.Now, MaxHotEntries: N,
	})
	if err != nil {
		t.Fatalf("run=%s New: %v", id, err)
	}

	for i := 0; i < N; i++ {
		if err := c.Put(fmt.Sprintf("k%d", i), []byte(fmt.Sprintf("v%d", i))); err != nil {
			t.Fatalf("run=%s Put k%d: %v", id, i, err)
		}
	}
	// LRU order now: k0(LRU) k1 k2 k3(MRU).
	if order := lruOrderBackToFront(c); len(order) != N || order[0] != "k0" {
		t.Fatalf("run=%s unexpected initial LRU order %v, want k0 at back", id, order)
	}

	// READ k0: this MUST promote it to MRU, making k1 the new LRU.
	clk.Advance(time.Millisecond)
	if v, tier, err := c.Get("k0"); err != nil || tier != TierHot || string(v) != "v0" {
		t.Fatalf("run=%s Get(k0): v=%q tier=%q err=%v, want Hot hit v0", id, v, tier, err)
	}
	if order := lruOrderBackToFront(c); order[0] != "k1" {
		t.Fatalf("run=%s after Get(k0): LRU back=%q, want k1 (Get failed to refresh recency)", id, order[0])
	}

	// Insert a new key, crossing the cap: victim MUST be k1, NOT the just-read k0.
	if err := c.Put("kNew", []byte("vNew")); err != nil {
		t.Fatalf("run=%s Put kNew: %v", id, err)
	}
	if got := c.HotCount(); got != N {
		t.Fatalf("run=%s HotCount=%d, want %d", id, got, N)
	}
	hot := hotKeysSnapshot(c)
	if !hot["k0"] {
		t.Fatalf("run=%s LIVE-ENTRY EVICTED EARLY: just-read k0 was evicted (Get did not refresh recency)", id)
	}
	if hot["k1"] {
		t.Fatalf("run=%s wrong victim: k1 (new LRU after Get(k0)) still Hot", id)
	}
	if !warm.Has("k1") {
		t.Fatalf("run=%s expected k1 demoted to Warm", id)
	}
	t.Logf("run=%s PASS: Get refreshed recency; k1 evicted, just-read k0 survived", id)
}

// ----------------------------------------------------------------------------
// TestAdv_TTLBoundary_ExactThresholdKeptOneTickEvicts
//
// TTL/expiry boundary. deadline = now - idleThreshold; demotion fires iff
// rec.lastAccess.Before(deadline) (STRICT). So:
//   - idle EXACTLY idleThreshold  => lastAccess == deadline => NOT Before => KEEP.
//   - idle idleThreshold + 1 tick => lastAccess <  deadline => Before     => DEMOTE.
//
// Two single-key caches isolate the two sides so the boundary is unambiguous.
// Bites an off-by-one in the demotion predicate (Before<->BeforeOrEqual / After).
// ----------------------------------------------------------------------------
func TestAdv_TTLBoundary_ExactThresholdKeptOneTickEvicts(t *testing.T) {
	id := runID(t)
	const idle = 30 * time.Second

	// --- Side 1: idle EXACTLY idleThreshold => MUST be kept. ---
	clkA := newClock(time.Unix(1_700_000_000, 0))
	cA, warmA, _ := newTestCache(t, clkA, idle)
	if err := cA.Put("edge", []byte("p")); err != nil {
		t.Fatalf("run=%s Put A: %v", id, err)
	}
	clkA.Advance(idle) // lastAccess == now-idle == deadline exactly.
	cA.RunMaintenance()
	if got := cA.HotCount(); got != 1 {
		t.Fatalf("run=%s idle==threshold: HotCount=%d, want 1 (entry at the exact boundary must be KEPT)", id, got)
	}
	if warmA.Has("edge") {
		t.Fatalf("run=%s idle==threshold: entry demoted to Warm (boundary should keep, off-by-one)", id)
	}

	// --- Side 2: idle by ONE extra nanosecond => MUST demote. ---
	clkB := newClock(time.Unix(1_700_000_000, 0))
	cB, warmB, _ := newTestCache(t, clkB, idle)
	if err := cB.Put("edge", []byte("p")); err != nil {
		t.Fatalf("run=%s Put B: %v", id, err)
	}
	clkB.Advance(idle + time.Nanosecond) // lastAccess strictly Before deadline.
	cB.RunMaintenance()
	if got := cB.HotCount(); got != 0 {
		t.Fatalf("run=%s idle>threshold: HotCount=%d, want 0 (idle entry must demote)", id, got)
	}
	if !warmB.Has("edge") {
		t.Fatalf("run=%s idle>threshold: entry not demoted to Warm", id)
	}
	t.Logf("run=%s PASS: TTL boundary strict — exact-threshold kept, +1 tick demoted", id)
}

// ----------------------------------------------------------------------------
// TestAdv_ConcurrentConservation_PutThenGetNeverMisses (-race headline)
//
// CONSERVATION under concurrency. A bounded key space is hammered by concurrent
// Put+Get plus a background maintenance demotion sweep. Because a key, once Put,
// lives in SOME tier (Hot, or demoted to Warm/Cold — never dropped), a Get for a
// key whose Put has already returned MUST NOT return ErrNotFound.
//
// Each goroutine owns a disjoint key range, Puts the key, then immediately Gets
// it back and asserts presence + value-belongs-to-key. After the concurrent
// phase a drain Get over every Put key asserts none was lost.
//
// Bites: a lost update / dropped entry on the eviction or promotion path
// (enforceHotCap dropping a key, a Put racing a concurrent eviction), and any
// unguarded shared-map access (caught by -race). "No panic" is NOT the assertion;
// the assertions are sink-side presence + value identity + a clean race run.
// ----------------------------------------------------------------------------
func TestAdv_ConcurrentConservation_PutThenGetNeverMisses(t *testing.T) {
	id := runID(t)
	clk := newClock(time.Unix(1_700_000_000, 0))
	// Small cap so insertions constantly trigger Hot->Warm demotion churn.
	warm := newFakeTier(500 * time.Microsecond)
	cold := newFakeTier(2 * time.Millisecond)
	const cap = 4
	c, err := New(Config{
		Warm: warm, Cold: cold, IdleThreshold: time.Hour, Now: clk.Now, MaxHotEntries: cap,
	})
	if err != nil {
		t.Fatalf("run=%s New: %v", id, err)
	}

	const (
		workers   = 8
		perWorker = 600
		keysEach  = 16
	)
	keyName := func(w, k int) string { return fmt.Sprintf("w%02d-k%02d", w, k) }

	var stop atomic.Bool
	var sweepWG sync.WaitGroup
	sweepWG.Add(1)
	go func() {
		defer sweepWG.Done()
		for !stop.Load() {
			// Don't advance the clock (idle=1h) so maintenance only stresses the
			// lock/iteration path concurrently; eviction churn comes from the cap.
			c.RunMaintenance()
		}
	}()

	var miss int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				k := i % keysEach
				key := keyName(w, k)
				val := []byte(key) // self-describing
				if err := c.Put(key, val); err != nil {
					t.Errorf("run=%s Put %s: %v", id, key, err)
					return
				}
				// CONSERVATION: the key we just Put must be retrievable from SOME
				// tier. A concurrent eviction may have demoted it Hot->Warm, but it
				// must never have been dropped.
				got, _, gerr := c.Get(key)
				if gerr != nil {
					atomic.AddInt64(&miss, 1)
					t.Errorf("run=%s LOST ENTRY: Get(%s) after Put returned err=%v (entry dropped, not demoted)", id, key, gerr)
					return
				}
				if string(got) != string(val) {
					t.Errorf("run=%s CONTAMINATION: Get(%s)=%q, want %q", id, key, got, val)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	stop.Store(true)
	sweepWG.Wait()

	// Drain: every key any worker ever Put must still be retrievable with its own
	// value (last write for each (w,k) was the same self-describing bytes).
	for w := 0; w < workers; w++ {
		for k := 0; k < keysEach; k++ {
			key := keyName(w, k)
			got, _, err := c.Get(key)
			if err != nil {
				t.Fatalf("run=%s DRAIN LOST ENTRY: Get(%s): %v", id, key, err)
			}
			if string(got) != key {
				t.Fatalf("run=%s DRAIN CONTAMINATION: Get(%s)=%q, want %q", id, key, got, key)
			}
		}
	}
	if m := atomic.LoadInt64(&miss); m != 0 {
		t.Fatalf("run=%s %d conservation misses observed live", id, m)
	}
	t.Logf("run=%s PASS: conservation held under concurrent Put/Get/evict + maintenance sweep", id)
}
