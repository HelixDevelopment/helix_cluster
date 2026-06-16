package lru

import (
	"fmt"
	"testing"
)

// ---------------------------------------------------------------------------
// Second adversarial LRU suite — complements adversarial_test.go by probing
// edges that file does not isolate:
//
//   - the off-by-one eviction boundary at the SINGLE step that flips when the
//     guard is `> cap` instead of `>= cap` (adversarial_test.go proves it under
//     bulk overflow; here we pin the precise one-insert transition);
//   - re-Put of an EXISTING key while the cache is FULL must NOT evict any
//     sibling (the `>=cap` guard must be gated behind the new-key path);
//   - re-adding a previously-EVICTED key behaves like a fresh insert and does
//     not resurrect stale state or grow past cap;
//   - the head==tail MoveToFront no-op (single-element / repeated Get on the
//     same key) does not corrupt the backing list;
//   - the `back != nil` defensive branch under cap<=0 (evicting from an empty
//     list is a no-op, never a panic).
//
// Contract pinned (white-box, in-package) from package.go:
//   NewCache(cap), Get(k)(interface{},bool), Put(k,v). No Len/Remove/TTL/mutex.
//   Eviction victim = order.Back() (true LRU). Guard `order.Len() >= cap` fires
//   ONLY on the new-key branch of Put.
// ---------------------------------------------------------------------------

// listLen reads the backing list length directly (no exported Len()).
func listLen(c *Cache) int { return c.order.Len() }

// frontToBackKeys returns live keys MRU->LRU by walking the backing list.
func frontToBackKeys(c *Cache) []string {
	ks := make([]string, 0, c.order.Len())
	for e := c.order.Front(); e != nil; e = e.Next() {
		ks = append(ks, e.Value.(*entry).key)
	}
	return ks
}

// TestLRU_EvictionBoundary_ExactlyAtCap pins the precise insert that triggers
// the FIRST eviction. With cap=C we insert C keys (occupancy must reach exactly
// C, no eviction yet), then ONE more (occupancy stays C, exactly one victim).
//
// ANTI-BLUFF (`>` vs `>=` off-by-one): if the guard were `order.Len() > cap`,
// the C+1-th insert would let the list reach C+1 before any trim, so occupancy
// would be C+1 here and the oldest key would NOT yet be gone. We assert
// occupancy is C at the saturation insert AND C (not C+1) immediately after the
// overflow insert, AND that the oldest key is evicted on exactly that insert.
func TestLRU_EvictionBoundary_ExactlyAtCap(t *testing.T) {
	const C = 4
	c := NewCache(C)

	for i := 0; i < C; i++ {
		c.Put(fmt.Sprintf("k%d", i), i)
	}
	// Saturated but NOT yet over: exactly C, all present, nothing evicted.
	if got := listLen(c); got != C {
		t.Fatalf("at saturation: occupancy=%d, want %d (premature eviction or growth)", got, C)
	}
	if _, ok := c.Get("k0"); !ok {
		t.Fatalf("at saturation (len==cap, no new insert yet): k0 must still be present")
	}
	// Note: that Get promoted k0; rebuild to keep k0 as the genuine LRU victim.
	c = NewCache(C)
	for i := 0; i < C; i++ {
		c.Put(fmt.Sprintf("k%d", i), i)
	}

	// The C+1-th distinct insert: exactly one eviction, occupancy stays C.
	c.Put("overflow", 999)
	if got := listLen(c); got != C {
		t.Fatalf("immediately after first overflow insert: occupancy=%d, want exactly %d (>cap off-by-one?)", got, C)
	}
	if _, ok := c.Get("k0"); ok {
		t.Fatalf("first overflow insert must evict the LRU (k0); it is still present")
	}
	if _, ok := c.Get("overflow"); !ok {
		t.Fatalf("freshly inserted overflow key must be present")
	}
}

// TestLRU_UpdateWhenFull_DoesNotEvictSibling proves the `>=cap` guard is gated
// behind the NEW-key path: re-Put of an existing key in a FULL cache updates in
// place and evicts NOTHING.
//
// ANTI-BLUFF: if the eviction guard ran unconditionally (i.e. also on the
// update path), re-Putting an existing key while full would wrongly evict the
// current LRU sibling. We fill cap=C, re-Put an existing MID key, then assert
// occupancy is still C AND every original key (including the LRU) survives.
func TestLRU_UpdateWhenFull_DoesNotEvictSibling(t *testing.T) {
	const C = 4
	c := NewCache(C)
	for i := 0; i < C; i++ {
		c.Put(fmt.Sprintf("k%d", i), i) // k0 LRU ... k3 MRU
	}

	c.Put("k2", 2222) // update an existing, non-LRU key while FULL

	if got := listLen(c); got != C {
		t.Fatalf("update-while-full must not change occupancy: got %d, want %d", got, C)
	}
	// EVERY original key must survive — nothing was evicted by the update.
	for i := 0; i < C; i++ {
		k := fmt.Sprintf("k%d", i)
		if _, ok := peek(c, k); !ok {
			t.Errorf("update-while-full wrongly evicted sibling %q", k)
		}
	}
	// And the value was actually updated.
	if v, ok := peek(c, "k2"); !ok || v != 2222 {
		t.Errorf("update-while-full must update value: got v=%v ok=%v, want 2222/true", v, ok)
	}
}

// peek reads a key's value WITHOUT promoting recency, by scanning the backing
// list. Used where a Get would taint the recency ordering under test.
func peek(c *Cache, key string) (interface{}, bool) {
	if el, ok := c.items[key]; ok {
		return el.Value.(*entry).value, true
	}
	return nil, false
}

// TestLRU_ReAddEvictedKey_FreshInsert proves a key that was evicted can be
// re-added cleanly: it returns as a present, MRU entry with the NEW value, the
// map and list stay consistent, and occupancy never exceeds cap.
func TestLRU_ReAddEvictedKey_FreshInsert(t *testing.T) {
	const C = 2
	c := NewCache(C)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Put("c", 3) // evicts a (LRU)

	if _, ok := peek(c, "a"); ok {
		t.Fatalf("precondition: 'a' must have been evicted")
	}

	// Re-add the evicted key with a new value.
	c.Put("a", 111)

	if got := listLen(c); got != C {
		t.Fatalf("re-add evicted key must keep occupancy==cap: got %d, want %d", got, C)
	}
	if v, ok := peek(c, "a"); !ok || v != 111 {
		t.Fatalf("re-added key must hold NEW value: got v=%v ok=%v, want 111/true", v, ok)
	}
	// 'a' is freshly inserted => MRU (front); inserting it evicted the LRU 'b'.
	front := frontToBackKeys(c)
	if len(front) == 0 || front[0] != "a" {
		t.Fatalf("re-added key must be MRU (front): order=%v", front)
	}
	if _, ok := peek(c, "b"); ok {
		t.Errorf("re-adding 'a' should have evicted the LRU 'b'; 'b' still present (order=%v)", front)
	}
	// map and list must agree in size: no orphaned map entry from the churn.
	if len(c.items) != listLen(c) {
		t.Errorf("map/list size mismatch after evict+re-add: map=%d list=%d", len(c.items), listLen(c))
	}
}

// TestLRU_RepeatedGetSameKey_HeadEqualsTailNoOp exercises the MoveToFront no-op
// when the touched element is already the front, and when it is the SOLE
// element (head==tail). The backing list must stay well-formed (no nil-splice,
// no length drift) and values must remain retrievable.
func TestLRU_RepeatedGetSameKey_HeadEqualsTailNoOp(t *testing.T) {
	c := NewCache(3)

	// Single element: head==tail. Repeated Get must be a stable no-op.
	c.Put("solo", 7)
	for i := 0; i < 5; i++ {
		if v, ok := c.Get("solo"); !ok || v != 7 {
			t.Fatalf("repeated Get on sole element: got v=%v ok=%v, want 7/true (iter %d)", v, ok, i)
		}
		if got := listLen(c); got != 1 {
			t.Fatalf("repeated Get must not change length: got %d, want 1 (iter %d)", got, i)
		}
	}

	// Multi-element: repeatedly Get the FRONT (already MRU) — also a no-op that
	// must not corrupt ordering or length.
	c.Put("x", 1)
	c.Put("y", 2) // y is now front
	for i := 0; i < 5; i++ {
		c.Get("y")
		order := frontToBackKeys(c)
		if len(order) != 3 || order[0] != "y" {
			t.Fatalf("repeated Get of front must keep it front & length stable: order=%v (iter %d)", order, i)
		}
	}
}

// TestLRU_ZeroAndNegativeCap_EvictEmptyListNoPanic drives the `back != nil`
// defensive branch (package.go:45). With cap<=0 the guard `len >= cap` is always
// true, so each Put first attempts an eviction; the FIRST Put attempts to evict
// from an EMPTY list, which must be a silent no-op (back==nil), never a panic.
func TestLRU_ZeroAndNegativeCap_EvictEmptyListNoPanic(t *testing.T) {
	for _, cap := range []int{0, -1, -5} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("cap=%d: Put on empty cache panicked (back!=nil guard missing?): %v", cap, r)
				}
			}()
			c := NewCache(cap)
			c.Put("first", 1) // evicts from empty list -> must be no-op, no panic
			if got := listLen(c); got != 1 {
				t.Fatalf("cap=%d: after first Put occupancy=%d, want 1", cap, got)
			}
			c.Put("second", 2) // now evicts "first"; at most one item retained
			if got := listLen(c); got != 1 {
				t.Fatalf("cap=%d: cap<=0 must retain at most 1 item, got %d", cap, got)
			}
			if _, ok := peek(c, "first"); ok {
				t.Errorf("cap=%d: 'first' must be evicted when 'second' inserted", cap)
			}
			if v, ok := peek(c, "second"); !ok || v != 2 {
				t.Errorf("cap=%d: 'second' must be the retained MRU item, got v=%v ok=%v", cap, v, ok)
			}
		}()
	}
}
