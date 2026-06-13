package tieredcache

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ============================================================================
// Adversarial cross-tier coherence + concurrency-safety suite.
//
// MODEL (verified by reading tieredcache.go, NOT assumed):
//   - Three tiers: Hot (in-memory map + LRU), Warm, Cold (injected Tier).
//   - Put writes the Hot tier ONLY. Warm/Cold are populated exclusively by
//     demotion (RunMaintenance) and by enforceHotCap LRU eviction (Hot->Warm).
//   - Get probes Hot -> Warm -> Cold and PROMOTES a Warm/Cold hit back to Hot.
//   - There is NO public Cache.Delete: the cache is write-Hot-only. (The Tier
//     interface has Delete, but Cache exposes none.) So "Delete removes from all
//     tiers" is tested as the strongest available equivalent: a re-Put of a key
//     that already lives in a lower tier MUST shadow/override the stale lower
//     copy on every subsequent Get (no stale lower-tier read ever surfaces).
//   - All public methods serialize on a single sync.Mutex.
//
// GAP vs existing tests: hotcap_test.go hammers Put/Get concurrently but only
// asserts the SIZE bound and LRU/map sync — it never asserts VALUE coherence
// (a Get could return a wrong/stale value and that suite would still pass). The
// existing single-threaded tests check tier transitions for ONE key with fixed
// values and never re-Put a key that already lives in a lower tier. None assert
// "Get after Put never returns a stale lower-tier value" or "no key ever returns
// a value assigned to a different key" under concurrent promotion/eviction
// churn. This suite fills exactly that gap.
// ============================================================================

// ----------------------------------------------------------------------------
// TestCoherence_ReputShadowsStaleLowerTier (headline coherence, deterministic)
//
// Scenario that bites a broken coherence layer:
//  1. Put k=v1 -> Hot.
//  2. Idle + RunMaintenance -> k demoted Hot->Warm (Warm now holds v1, Hot empty).
//  3. Put k=v2 -> Hot (Warm STILL physically holds the stale v1).
//  4. Get k MUST return v2 (Hot shadows Warm). A Get that probed Warm first, or
//     a Put that failed to override the Hot entry, would surface stale v1.
//  5. Force the Hot entry out via cap pressure: enforceHotCap demotes k Hot->Warm
//     and MUST overwrite Warm with v2. A subsequent Get (now served from Warm)
//     MUST return v2 — never the stale v1 that physically sat in Warm at step 3.
//
// ANTI-BLUFF: the step-5 assertion is the load-bearing one. If eviction wrote
// the wrong value, or if Warm's stale v1 were not overwritten, the Warm-served
// Get returns v1 and this test FAILS. It directly bites "a stale value from a
// lower tier is returned after an update".
// ----------------------------------------------------------------------------
func TestCoherence_ReputShadowsStaleLowerTier(t *testing.T) {
	id := runID(t)
	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	clk := newClock(base)
	idle := 10 * time.Second

	// Cap = 1 so we can force LRU eviction with a single extra key.
	warm := newFakeTier(500 * time.Microsecond)
	cold := newFakeTier(2 * time.Millisecond)
	c, err := New(Config{
		Warm: warm, Cold: cold, IdleThreshold: idle, Now: clk.Now, MaxHotEntries: 1,
	})
	if err != nil {
		t.Fatalf("run=%s New: %v", id, err)
	}

	const key = "coh"
	v1 := []byte("VALUE-ONE")
	v2 := []byte("VALUE-TWO-updated")

	// (1) Put v1 -> Hot.
	if err := c.Put(key, v1); err != nil {
		t.Fatalf("run=%s Put v1: %v", id, err)
	}

	// (2) Idle past threshold + maintenance -> demote Hot->Warm.
	clk.Advance(idle + time.Second)
	c.RunMaintenance()
	if !warm.Has(key) {
		t.Fatalf("run=%s after maintenance: key not demoted to Warm", id)
	}
	if c.HotCount() != 0 {
		t.Fatalf("run=%s after maintenance: HotCount=%d, want 0", id, c.HotCount())
	}
	t.Logf("run=%s step2: Warm physically holds stale v1=%q", id, v1)

	// (3) Re-Put v2 -> Hot. Warm still physically holds v1.
	if err := c.Put(key, v2); err != nil {
		t.Fatalf("run=%s Put v2: %v", id, err)
	}

	// (4) Get MUST return v2 (Hot shadows the stale Warm copy).
	got, tier, err := c.Get(key)
	if err != nil {
		t.Fatalf("run=%s Get after re-Put: %v", id, err)
	}
	if tier != TierHot {
		t.Fatalf("run=%s Get after re-Put: tier=%q, want Hot", id, tier)
	}
	if string(got) != string(v2) {
		t.Fatalf("run=%s STALE READ: Get=%q, want v2=%q (Hot must shadow stale Warm v1)", id, got, v2)
	}

	// (5) Force k out of Hot via cap pressure: Put a different key -> enforceHotCap
	// demotes the LRU Hot key (k) to Warm, which MUST overwrite Warm's stale v1.
	if err := c.Put("evictor", []byte("evictor-val")); err != nil {
		t.Fatalf("run=%s Put evictor: %v", id, err)
	}
	// k must have left Hot (cap=1) and been written to Warm with v2.
	got2, tier2, err := c.Get(key)
	if err != nil {
		t.Fatalf("run=%s Get after eviction: %v", id, err)
	}
	if tier2 != TierWarm {
		t.Fatalf("run=%s Get after eviction: tier=%q, want Warm (k evicted from Hot)", id, tier2)
	}
	if string(got2) != string(v2) {
		t.Fatalf("run=%s STALE LOWER-TIER READ: Warm-served Get=%q, want v2=%q "+
			"(eviction must overwrite stale Warm v1)", id, got2, v2)
	}
	t.Logf("run=%s PASS: Warm-served value is the updated v2 (no stale lower-tier read)", id)
}

// ----------------------------------------------------------------------------
// TestCoherence_DemotionPreservesLatestValueThroughCold
//
// Verifies the full demotion cascade preserves the LATEST value, including a
// re-Put that occurs while a stale copy lives in Warm, all the way to Cold.
//
//  1. Put k=v1, idle, maintenance -> Warm holds v1.
//  2. Get k (Warm hit -> promotes to Hot), then re-Put k=v2 to Hot.
//  3. idle, maintenance -> warm.Get(k) succeeds (v1 present) so the cascade
//     pushes rec.Value (v2) to Cold and clears Warm.
//  4. Get k MUST return v2 from Cold — never v1.
//
// ANTI-BLUFF: if the cascade wrote the stale Warm value to Cold instead of the
// live Hot record value, step 4 returns v1 and FAILS.
// ----------------------------------------------------------------------------
func TestCoherence_DemotionPreservesLatestValueThroughCold(t *testing.T) {
	id := runID(t)
	clk := newClock(time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC))
	idle := 10 * time.Second
	c, warm, cold := newTestCache(t, clk, idle)

	const key = "casc"
	v1 := []byte("first-1")
	v2 := []byte("second-2-newer")

	if err := c.Put(key, v1); err != nil {
		t.Fatalf("run=%s Put v1: %v", id, err)
	}
	clk.Advance(idle + time.Second)
	c.RunMaintenance() // Hot->Warm; Warm=v1
	if !warm.Has(key) {
		t.Fatalf("run=%s key not in Warm after 1st maintenance", id)
	}

	// Promote back to Hot via Get, then re-Put v2.
	clk.Advance(time.Second)
	if _, tier, err := c.Get(key); err != nil || tier != TierWarm {
		t.Fatalf("run=%s Warm Get: tier=%q err=%v, want Warm", id, tier, err)
	}
	if err := c.Put(key, v2); err != nil { // Hot=v2; Warm STILL holds v1
		t.Fatalf("run=%s Put v2: %v", id, err)
	}

	// Idle again -> cascade Warm->Cold (warm.Get succeeds), Cold must get v2.
	clk.Advance(idle + time.Second)
	c.RunMaintenance()
	if !cold.Has(key) {
		t.Fatalf("run=%s key not in Cold after cascade", id)
	}
	if warm.Has(key) {
		t.Fatalf("run=%s key still in Warm after Warm->Cold cascade (stale residue)", id)
	}

	clk.Advance(time.Second)
	got, tier, err := c.Get(key)
	if err != nil {
		t.Fatalf("run=%s Cold Get: %v", id, err)
	}
	if tier != TierCold {
		t.Fatalf("run=%s Cold Get: tier=%q, want Cold", id, tier)
	}
	if string(got) != string(v2) {
		t.Fatalf("run=%s STALE CASCADE: Cold-served Get=%q, want v2=%q "+
			"(cascade must demote the live value, not the stale Warm copy)", id, got, v2)
	}
	t.Logf("run=%s PASS: latest value v2 survived the full Hot->Warm->Cold cascade", id)
}

// ----------------------------------------------------------------------------
// TestCoherence_ConcurrentChurnNoStaleNoContamination (-race headline)
//
// Many goroutines Put/Get concurrently across a small key space with a tiny Hot
// cap so keys constantly churn between tiers (promotion + LRU eviction), with a
// background RunMaintenance demotion sweep running in parallel.
//
// Per-key values are MONOTONIC and self-describing: the value written for key k
// at sequence s is "k|<padded-seq>". A reader records, per key, the highest
// sequence it ever READ. Invariants asserted live:
//
//	(A) NO CONTAMINATION: every value a Get returns for key k MUST decode to
//	    the SAME key k. A value belonging to another key (or a torn/garbage
//	    value) is a racy-promotion corruption bug. Bites "no key returns a
//	    value it was never assigned".
//	(B) NO STALE-BACKWARDS BEYOND TOLERANCE: because Get can be served from a
//	    lower tier that legitimately lags an in-flight Put, we do NOT require
//	    strict monotonicity per read. Instead, after the writers finish we
//	    drain: every key's FINAL Get MUST return the FINAL (highest) sequence
//	    that was Put for it — proving no permanent stale lower-tier value wins.
//	(C) -race: no data race, no panic.
//
// ANTI-BLUFF:
//   - (A) bites a racy promotion that copies one key's bytes under another key,
//     or returns an aliased/torn buffer.
//   - (B)'s drain bites a permanent stale lower-tier read (an update lost behind
//     a lower tier forever).
//   - -race bites the per-tier map race if the Hot map / LRU / Value copy were
//     touched outside the mutex.
//
// ----------------------------------------------------------------------------
func TestCoherence_ConcurrentChurnNoStaleNoContamination(t *testing.T) {
	id := runID(t)
	clk := newClock(time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC))
	// Long idle so the background maintenance only demotes when we advance the
	// clock; we drive churn primarily through the tiny Hot cap.
	idle := time.Hour
	warm := newFakeTier(500 * time.Microsecond)
	cold := newFakeTier(2 * time.Millisecond)
	c, err := New(Config{
		Warm: warm, Cold: cold, IdleThreshold: idle, Now: clk.Now, MaxHotEntries: 4,
	})
	if err != nil {
		t.Fatalf("run=%s New: %v", id, err)
	}

	const (
		writers   = 8
		nKeys     = writers * 2 // each key is owned by EXACTLY ONE writer
		readers   = 8
		perWriter = 1500
	)
	keyName := func(k int) string { return fmt.Sprintf("key%02d", k) }
	encode := func(k, seq int) []byte { return []byte(fmt.Sprintf("%s|%08d", keyName(k), seq)) }

	// highestPut[k] = highest sequence whose Put has RETURNED for key k.
	//
	// IMPORTANT (sound oracle): each key is written by exactly one writer, and
	// that writer Puts strictly increasing sequences and only then stores the
	// sequence here. So highestPut[k] equals the most-recent value the cache was
	// asked to store for k. (If two writers shared a key, atomic seq-assignment
	// would NOT match Put arrival order at the lock, and "final == max seq" would
	// be an UNSOUND oracle — last-write-wins would legitimately keep a lower seq.
	// Single-owner-per-key removes that ambiguity so a drain mismatch is a REAL
	// lost cross-tier update, not a writer-ordering artifact.)
	highestPut := make([]int64, nKeys)

	var stop atomic.Bool
	var workWG sync.WaitGroup  // writers + readers
	var sweepWG sync.WaitGroup // background maintenance sweeper

	// Background demotion sweeper: advances the clock and runs maintenance so a
	// real Hot->Warm->Cold demotion path runs concurrently with Get/Put. It runs
	// for the WHOLE concurrent phase and is stopped only after workers finish.
	sweepWG.Add(1)
	go func() {
		defer sweepWG.Done()
		for !stop.Load() {
			clk.Advance(2 * idle) // push everything past idle so maintenance demotes
			c.RunMaintenance()
		}
	}()

	// Writers: writer w OWNS keys {w, w+writers, ...}. For each owned key it Puts
	// strictly increasing sequences, so Put arrival order == sequence order.
	for w := 0; w < writers; w++ {
		workWG.Add(1)
		go func(w int) {
			defer workWG.Done()
			next := make([]int, writers*2) // per-owned-key next sequence
			for i := 0; i < perWriter; i++ {
				// Rotate among this writer's owned keys (two per writer: w, w+writers).
				owned := w + (i%2)*writers
				next[owned]++
				seq := next[owned]
				// PUBLISH-BEFORE-PUT: raise highestPut to this seq BEFORE the Put.
				// Because this writer is the sole owner of `owned` and increments
				// monotonically, the cache can hold AT MOST this seq for the key once
				// the Put lands — so "served seq <= highestPut" is a sound live
				// invariant with no publish race (a reader can never see a seq the
				// owner has not yet announced). The final Put for the key also makes
				// highestPut == that final seq, keeping the drain oracle exact.
				atomic.StoreInt64(&highestPut[owned], int64(seq))
				if err := c.Put(keyName(owned), encode(owned, seq)); err != nil {
					t.Errorf("run=%s Put %s: %v", id, keyName(owned), err)
					return
				}
			}
		}(w)
	}

	// Readers: Get keys across the whole space concurrently and assert live
	// invariants that hold REGARDLESS of which tier serves the value.
	var reads int64
	for r := 0; r < readers; r++ {
		workWG.Add(1)
		go func(r int) {
			defer workWG.Done()
			for i := 0; i < perWriter; i++ {
				k := (r*7 + i*3) % nKeys
				val, _, err := c.Get(keyName(k))
				if errors.Is(err, ErrNotFound) {
					continue // legitimately not yet written / in-flight
				}
				if err != nil {
					t.Errorf("run=%s Get %s: %v", id, keyName(k), err)
					return
				}
				atomic.AddInt64(&reads, 1)
				// (A) CONTAMINATION CHECK: value MUST belong to key k. A value
				// carrying another key's name (or a torn buffer) is a racy-promotion
				// corruption — a Get returning a value the key was never assigned.
				wantPrefix := keyName(k) + "|"
				if len(val) < len(wantPrefix) || string(val[:len(wantPrefix)]) != wantPrefix {
					t.Errorf("run=%s CONTAMINATION: Get(%s) returned %q (belongs to another key / torn)",
						id, keyName(k), val)
					return
				}
				// (A2) NO VALUE-FROM-THE-FUTURE: the served sequence may LAG (lower
				// tiers trail in-flight Puts) but must never EXCEED the highest Put
				// that has already returned for k. Exceeding it means a torn/garbage
				// read or cross-key contamination decoding to a too-high number.
				var seq int
				if _, e := fmt.Sscanf(string(val[len(wantPrefix):]), "%08d", &seq); e == nil {
					if hi := int(atomic.LoadInt64(&highestPut[k])); seq > hi {
						t.Errorf("run=%s FUTURE READ: Get(%s)=seq %d > highest Put %d (torn/contaminated)",
							id, keyName(k), seq, hi)
						return
					}
				}
			}
		}(r)
	}

	// Let writers+readers finish, THEN stop the sweeper so demotion churn runs
	// for the entire concurrent phase.
	workWG.Wait()
	stop.Store(true)
	sweepWG.Wait()

	t.Logf("run=%s concurrent phase done: reads-validated=%d", id, atomic.LoadInt64(&reads))

	// (B) DRAIN: every key's final Get MUST return the highest sequence Put.
	// Stop churn (no more clock advance) and read each key. The value may be
	// served from any tier, but it MUST be the latest — no permanent stale read.
	for k := 0; k < nKeys; k++ {
		want := int(atomic.LoadInt64(&highestPut[k]))
		if want == 0 {
			continue // never written
		}
		val, tier, err := c.Get(keyName(k))
		if err != nil {
			t.Fatalf("run=%s drain Get(%s): %v", id, keyName(k), err)
		}
		wantVal := string(encode(k, want))
		if string(val) != wantVal {
			t.Fatalf("run=%s PERMANENT STALE READ: drain Get(%s) from %s = %q, want latest %q",
				id, keyName(k), tier, val, wantVal)
		}
	}
	t.Logf("run=%s PASS: no contamination, no permanent stale read across %d keys", id, nKeys)
}
