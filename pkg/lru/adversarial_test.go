package lru

import (
	"fmt"
	"testing"
)

// ---------------------------------------------------------------------------
// Adversarial LRU correctness suite.
//
// Contract under test (from package.go, white-box / in-package):
//   - API: NewCache(capacity), Get(key) (interface{}, bool), Put(key, value).
//     No exported Len/Keys; internal length is c.order.Len().
//   - Capacity model: eviction guard `order.Len() >= cap` runs only when a NEW
//     key is inserted; for positive cap the invariant is order.Len() <= cap.
//   - Recency: BOTH Get and Put(existing key) call MoveToFront. Eviction removes
//     order.Back(), i.e. the least-recently-USED entry. This is true LRU, not
//     FIFO. The tests below are specifically constructed so a FIFO impl
//     (one that does NOT promote on Get/update) FAILS them.
//   - Thread-safety: *Cache has NO mutex and is NOT goroutine-safe (Get mutates
//     the list, Put mutates list+map). The honest contract is single-threaded;
//     callers must serialize. We therefore do NOT add a racing concurrent test
//     here — see TestCache_ConcurrencyContract_SerializedIsSafe for the honest,
//     non-bluffing treatment of concurrency.
// ---------------------------------------------------------------------------

// internalLen reports the cache's current occupancy. The cache exposes no Len(),
// so we read the backing list directly (white-box, in-package). This is the
// load-bearing capacity oracle: a cache that keeps cap+1 entries is caught here,
// not by indirect Get-miss probing that could mask an off-by-one.
func internalLen(c *Cache) int { return c.order.Len() }

// orderedKeysMRUtoLRU returns the live keys from most- to least-recently-used by
// walking the backing list front->back. Used to assert the EXACT surviving set
// and its recency order, which a Get-miss probe cannot do.
func orderedKeysMRUtoLRU(c *Cache) []string {
	keys := make([]string, 0, c.order.Len())
	for e := c.order.Front(); e != nil; e = e.Next() {
		keys = append(keys, e.Value.(*entry).key)
	}
	return keys
}

// TestCache_CapacityBound_BulkOverflow proves the hard capacity bound under bulk
// overflow at a non-trivial capacity.
//
// ANTI-BLUFF (off-by-one bite): we insert cap+K distinct keys into a cap=C cache
// and assert the occupancy is EXACTLY C at every step past saturation and at the
// end — never C+1. An off-by-one eviction guard (e.g. `> cap` instead of
// `>= cap`, which would let the list reach cap+1 before trimming) is caught here
// because we read the real backing-list length, not a Get-miss heuristic. We
// also assert the survivors are exactly the last C keys inserted (the MRU set),
// which a cache that evicted the WRONG entry would fail.
func TestCache_CapacityBound_BulkOverflow(t *testing.T) {
	const cap = 8
	const extra = 25 // insert cap+extra distinct keys
	c := NewCache(cap)

	for i := 0; i < cap+extra; i++ {
		c.Put(fmt.Sprintf("k%02d", i), i)
		got := internalLen(c)
		want := i + 1
		if want > cap {
			want = cap
		}
		if got != want {
			t.Fatalf("after inserting %d distinct keys (cap=%d): occupancy=%d, want=%d (capacity bound violated)",
				i+1, cap, got, want)
		}
	}

	// Exactly `cap` entries remain, never cap+1.
	if got := internalLen(c); got != cap {
		t.Fatalf("final occupancy=%d, want exactly cap=%d", got, cap)
	}

	// Survivors must be exactly the last `cap` keys inserted, in MRU->LRU order:
	// the most-recently inserted (highest index) is at the front.
	got := orderedKeysMRUtoLRU(c)
	if len(got) != cap {
		t.Fatalf("survivor count=%d, want %d", len(got), cap)
	}
	for pos := 0; pos < cap; pos++ {
		idx := (cap + extra - 1) - pos // front=newest
		want := fmt.Sprintf("k%02d", idx)
		if got[pos] != want {
			t.Fatalf("survivor at MRU position %d = %q, want %q (full ordered survivors: %v)",
				pos, got[pos], want, got)
		}
	}

	// The first `extra` keys must all be gone.
	for i := 0; i < extra; i++ {
		k := fmt.Sprintf("k%02d", i)
		if _, ok := c.Get(k); ok {
			t.Fatalf("key %q should have been evicted (only last %d keys survive)", k, cap)
		}
	}
}

// TestCache_GetPromotesRecency_SecondOldestEvicted is the core LRU-vs-FIFO test.
//
// ANTI-BLUFF (FIFO masquerade bite): With capacity C we insert C keys k0..k{C-1}
// (k0 oldest). We then Get(k0) — under true LRU this promotes k0 to MRU and the
// SECOND-oldest, k1, becomes the eviction victim. We insert one new key and
// assert:
//   - k1 (second-oldest) was evicted, AND
//   - k0 (the one we Got) SURVIVED.
//
// A FIFO cache, or any "LRU" whose Get does NOT actually reorder recency, would
// evict k0 (the genuinely-oldest-by-insertion) and keep k1 — the exact opposite.
// This single assertion distinguishes real recency tracking from insertion-order
// eviction, so a Get-doesn't-promote regression cannot pass.
func TestCache_GetPromotesRecency_SecondOldestEvicted(t *testing.T) {
	const C = 5
	c := NewCache(C)
	for i := 0; i < C; i++ {
		c.Put(fmt.Sprintf("k%d", i), i) // k0 oldest ... k4 newest
	}

	// Touch the OLDEST key. Under LRU this makes k0 most-recently-used.
	if v, ok := c.Get("k0"); !ok || v != 0 {
		t.Fatalf("precondition: Get(k0) should hit with value 0, got v=%v ok=%v", v, ok)
	}

	// Insert a new key -> exactly one eviction. Victim must be SECOND-oldest (k1).
	c.Put("new", 99)

	if _, ok := c.Get("k1"); ok {
		t.Errorf("k1 (second-oldest) must be evicted: Get promoted k0, so k1 is now LRU")
	}
	if v, ok := c.Get("k0"); !ok || v != 0 {
		t.Errorf("k0 must SURVIVE because Get promoted it to MRU (FIFO would have evicted it); got v=%v ok=%v", v, ok)
	}
	// Sanity: the other originals and the new key are present; occupancy is C.
	for _, k := range []string{"k2", "k3", "k4", "new"} {
		if _, ok := c.Get(k); !ok {
			t.Errorf("expected %q to be present", k)
		}
	}
	if got := internalLen(c); got != C {
		t.Errorf("occupancy after one overflow insert=%d, want %d", got, C)
	}
}

// TestCache_EvictionOrder_ExactSurvivors asserts the full eviction order across a
// scripted access pattern, not just a single victim.
//
// ANTI-BLUFF: a deterministic interleave of Put and Get is replayed, then the
// exact ordered survivor list (MRU->LRU) is compared against a hand-computed
// expectation. A cache that promotes on the wrong operation, fails to promote on
// Get, or evicts from the wrong end produces a different ordered set and fails.
func TestCache_EvictionOrder_ExactSurvivors(t *testing.T) {
	const C = 3
	c := NewCache(C)

	c.Put("a", 1) // order MRU->LRU: a
	c.Put("b", 2) // b, a
	c.Put("c", 3) // c, b, a   (full)
	c.Get("a")    // a, c, b   (a promoted)
	c.Put("d", 4) // evicts b (LRU) -> d, a, c

	want := []string{"d", "a", "c"}
	got := orderedKeysMRUtoLRU(c)
	if len(got) != len(want) {
		t.Fatalf("survivor count=%d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("survivor order mismatch at %d: got %v, want %v", i, got, want)
		}
	}
	if _, ok := c.Get("b"); ok {
		t.Errorf("b should have been evicted as LRU after a was promoted by Get")
	}
}

// TestCache_UpdateInPlace_PromotesAndNoGrowth proves Put on an EXISTING key:
//  1. updates the value,
//  2. does NOT grow occupancy (no duplicate element), and
//  3. promotes the key to MRU (re-Put counts as a use).
//
// ANTI-BLUFF: After filling cap=C, we re-Put the OLDEST key with a new value and
// then insert a fresh key forcing one eviction. If update-in-place promotes
// recency (correct), the re-Put key SURVIVES and the next-oldest is evicted. A
// broken impl that updates the value but does NOT promote (or that appends a
// duplicate and lets occupancy grow to C+1) is caught: it would evict the re-Put
// key and/or show occupancy != C.
func TestCache_UpdateInPlace_PromotesAndNoGrowth(t *testing.T) {
	const C = 4
	c := NewCache(C)
	for i := 0; i < C; i++ {
		c.Put(fmt.Sprintf("k%d", i), i) // k0 oldest ... k3 newest
	}

	// Re-Put the OLDEST key with a new value: must update value, must NOT grow,
	// must promote k0 to MRU.
	c.Put("k0", 100)

	if got := internalLen(c); got != C {
		t.Fatalf("update-in-place must not change occupancy: got %d, want %d (duplicate-element bug?)", got, C)
	}
	if v, ok := c.Get("k0"); !ok || v != 100 {
		t.Fatalf("update-in-place must update value: got v=%v ok=%v, want 100/true", v, ok)
	}
	// Note: the Get above re-promotes k0 too, but the load-bearing promotion is
	// from the Put. To isolate Put-promotion from Get-promotion, rebuild and use
	// only Put before the eviction trigger.

	c2 := NewCache(C)
	for i := 0; i < C; i++ {
		c2.Put(fmt.Sprintf("k%d", i), i) // k0 oldest ... k3 newest
	}
	c2.Put("k0", 100)  // Put-only promotion of the oldest; now k1 is LRU.
	c2.Put("fresh", 7) // one eviction; victim must be k1 (new LRU), NOT k0.

	if got := internalLen(c2); got != C {
		t.Fatalf("occupancy after re-Put + insert = %d, want %d", got, C)
	}
	if _, ok := c2.Get("k1"); ok {
		t.Errorf("k1 must be evicted: re-Put(k0) promoted k0, making k1 the LRU victim")
	}
	if v, ok := c2.Get("k0"); !ok || v != 100 {
		t.Errorf("k0 must SURVIVE (re-Put promoted it to MRU) and hold updated value 100; got v=%v ok=%v", v, ok)
	}
}

// TestCache_ConcurrencyContract_SerializedIsSafe documents the HONEST
// thread-safety contract and proves it positively.
//
// *Cache has no mutex and is NOT goroutine-safe. Rather than run a concurrent
// test that would merely demonstrate (or, worse, luckily hide) the documented
// race — a PASS-bluff — we assert the real contract: when callers serialize all
// access behind their OWN lock, the cache behaves correctly and the capacity
// bound holds under high churn. The -race detector is clean here precisely
// because every access is serialized, which is what the contract requires of
// callers. This is the strongest property we can PROVE without misrepresenting
// the type as goroutine-safe.
func TestCache_ConcurrencyContract_SerializedIsSafe(t *testing.T) {
	const cap = 16
	const goroutines = 8
	const opsPerG = 2000

	c := NewCache(cap)

	// A single shared lock the "callers" hold — modeling the required external
	// synchronization. With this, concurrent use is safe and -race is clean.
	var mu = make(chan struct{}, 1)
	mu <- struct{}{} // token available
	lock := func() { <-mu }
	unlock := func() { mu <- struct{}{} }

	done := make(chan struct{})
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < opsPerG; i++ {
				k := fmt.Sprintf("g%d-k%d", g, i%cap)
				lock()
				if i%3 == 0 {
					c.Get(k)
				} else {
					c.Put(k, i)
				}
				// Invariant must hold at every serialized step.
				if l := internalLen(c); l > cap {
					unlock()
					t.Errorf("capacity bound violated under serialized churn: len=%d > cap=%d", l, cap)
					return
				}
				unlock()
			}
		}(g)
	}
	for g := 0; g < goroutines; g++ {
		<-done
	}

	if got := internalLen(c); got > cap {
		t.Fatalf("final occupancy=%d exceeds cap=%d", got, cap)
	}
}
