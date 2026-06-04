package tieredcache

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// hotLen returns len(c.hot) under the cache lock (white-box).
func hotLen(c *Cache) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.hot)
}

// lruLen returns the LRU recency-list length under the cache lock (white-box).
func lruLen(c *Cache) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.Len()
}

// TestHotCap_BoundsAndDemotesLRU is the F9 regression guard: the Hot tier must
// never exceed Config.MaxHotEntries even WITHOUT a RunMaintenance pass, and the
// evicted least-recently-used keys must be DEMOTED to Warm (never dropped).
//
// Before the cap was wired into the insert path, Put did a raw c.hot[key]=...
// and the map grew unbounded (the helpers existed but were never called). A
// mutation that reverts Put/Get to a raw map write makes len(hot) == 20 here and
// fails this test.
func TestHotCap_BoundsAndDemotesLRU(t *testing.T) {
	clk := newClock(time.Unix(1_700_000_000, 0))
	warm := newFakeTier(500 * time.Microsecond)
	cold := newFakeTier(2 * time.Millisecond)
	c, err := New(Config{
		Warm:          warm,
		Cold:          cold,
		IdleThreshold: time.Hour, // long, so RunMaintenance is irrelevant here
		Now:           clk.Now,
		MaxHotEntries: 4,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const n = 20
	for i := 0; i < n; i++ {
		if err := c.Put(fmt.Sprintf("k%02d", i), []byte(fmt.Sprintf("v%02d", i))); err != nil {
			t.Fatalf("Put k%02d: %v", i, err)
		}
	}

	// Bound holds with NO RunMaintenance call.
	if got := hotLen(c); got != 4 {
		t.Fatalf("Hot tier size = %d, want 4 (cap not enforced on insert — F9 regression)", got)
	}
	if got := lruLen(c); got != 4 {
		t.Fatalf("LRU list length = %d, want 4 (LRU list desynced from hot map)", got)
	}

	// The 16 least-recently-used keys must have been DEMOTED to Warm (not lost),
	// and must no longer be in Hot.
	for i := 0; i < n-4; i++ {
		key := fmt.Sprintf("k%02d", i)
		if !warm.Has(key) {
			t.Errorf("evicted key %s was DROPPED, not demoted to Warm (data loss)", key)
		}
	}
	// The 4 most-recently-used keys must still be in Hot and not yet in Warm.
	for i := n - 4; i < n; i++ {
		key := fmt.Sprintf("k%02d", i)
		if warm.Has(key) {
			t.Errorf("most-recent key %s was demoted to Warm too early", key)
		}
		if _, tier, err := c.Get(key); err != nil || tier != TierHot {
			t.Errorf("Get(%s) tier=%q err=%v, want TierHot hit", key, tier, err)
		}
	}
}

// TestHotCap_DefaultBoundApplied verifies the defensive default is used when the
// caller does not set MaxHotEntries, so Hot growth is bounded out of the box.
func TestHotCap_DefaultBoundApplied(t *testing.T) {
	clk := newClock(time.Unix(1_700_000_000, 0))
	c, err := New(Config{
		Warm:          newFakeTier(500 * time.Microsecond),
		Cold:          newFakeTier(2 * time.Millisecond),
		IdleThreshold: time.Hour,
		Now:           clk.Now,
		// MaxHotEntries intentionally unset.
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.maxHotEntries != DefaultMaxHotEntries {
		t.Fatalf("maxHotEntries = %d, want default %d", c.maxHotEntries, DefaultMaxHotEntries)
	}
}

// TestHotCap_ConcurrentBoundHolds hammers Put/Get from many goroutines and
// asserts the Hot bound holds and the run is race-clean (go test -race).
func TestHotCap_ConcurrentBoundHolds(t *testing.T) {
	clk := newClock(time.Unix(1_700_000_000, 0))
	const cap = 8
	c, err := New(Config{
		Warm:          newFakeTier(500 * time.Microsecond),
		Cold:          newFakeTier(2 * time.Millisecond),
		IdleThreshold: time.Hour,
		Now:           clk.Now,
		MaxHotEntries: cap,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var wg sync.WaitGroup
	const workers, perWorker = 8, 200
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				key := fmt.Sprintf("w%d-k%d", w, i%32)
				_ = c.Put(key, []byte(key))
				_, _, _ = c.Get(key)
			}
		}(w)
	}
	wg.Wait()

	if got := hotLen(c); got > cap {
		t.Fatalf("Hot tier size = %d exceeds cap %d under concurrency (F9 bound violated)", got, cap)
	}
	if l, h := lruLen(c), hotLen(c); l != h {
		t.Fatalf("LRU list (%d) desynced from hot map (%d)", l, h)
	}
}
