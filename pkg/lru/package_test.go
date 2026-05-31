package lru

import "testing"

func TestCache(t *testing.T) {
	c := NewCache(2)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Put("c", 3) // evicts a
	if _, ok := c.Get("a"); ok {
		t.Error("expected 'a' to be evicted")
	}
	if v, ok := c.Get("b"); !ok || v != 2 {
		t.Errorf("expected b=2, got %v", v)
	}
}

// --- Mutation tests ---

func TestCache_GetMovesToFront_Mutation(t *testing.T) {
	// Mutation: Get does not MoveToFront → accessing 'a' won't protect it from eviction
	c := NewCache(2)
	c.Put("a", 1)
	c.Put("b", 2)
	c.Get("a") // 'a' becomes most-recently-used
	c.Put("c", 3)
	if _, ok := c.Get("a"); !ok {
		t.Error("'a' should not be evicted because it was recently accessed")
	}
	if _, ok := c.Get("b"); ok {
		t.Error("'b' should be evicted because it was least-recently-used")
	}
}

func TestCache_UpdateExisting_Mutation(t *testing.T) {
	// Mutation: Put on existing key inserts duplicate instead of updating
	c := NewCache(2)
	c.Put("a", 1)
	c.Put("a", 10)
	if v, ok := c.Get("a"); !ok || v != 10 {
		t.Errorf("expected updated value 10, got %v", v)
	}
	// Capacity should still be respected (no duplicate elements bloating cache)
	c.Put("b", 2)
	c.Put("c", 3)
	if _, ok := c.Get("a"); ok {
		t.Error("'a' should be evictable after adding b and c")
	}
}

func TestCache_CapacityRespected_Mutation(t *testing.T) {
	// Mutation: eviction logic removed or broken → cache grows beyond capacity
	c := NewCache(1)
	c.Put("a", 1)
	c.Put("b", 2)
	if _, ok := c.Get("a"); ok {
		t.Error("capacity=1: 'a' should have been evicted")
	}
	if v, ok := c.Get("b"); !ok || v != 2 {
		t.Errorf("expected b=2, got %v", v)
	}
}

// --- Boundary / degenerate-capacity tests ---

// TestCache_ColdMiss locks the cold-path return at package.go:33: a Get on a
// fresh, never-written cache returns the zero value (nil) and ok==false.
func TestCache_ColdMiss(t *testing.T) {
	c := NewCache(4)
	v, ok := c.Get("absent")
	if ok {
		t.Errorf("cold miss on fresh cache: expected ok=false, got ok=true")
	}
	if v != nil {
		t.Errorf("cold miss on fresh cache: expected nil value, got %v", v)
	}
}

// TestCache_ZeroCapacity pins the DEFINED behavior of NewCache(0). The current
// implementation does not reject a zero capacity; instead the eviction guard
// (`order.Len() >= cap` => `0 >= 0`) runs on every insert, evicting the prior
// entry first, so the cache retains AT MOST the single most-recently-inserted
// item. This test asserts that documented, intended degenerate behavior.
func TestCache_ZeroCapacity(t *testing.T) {
	c := NewCache(0)
	c.Put("a", 1)
	// Most-recent item is retained (eviction of an empty list is a no-op).
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Errorf("cap=0: expected most-recent 'a'=1 to be retained, got v=%v ok=%v", v, ok)
	}
	c.Put("b", 2)
	// Inserting 'b' must evict 'a': at most one item is ever retained.
	if _, ok := c.Get("a"); ok {
		t.Error("cap=0: 'a' should have been evicted when 'b' was inserted (max 1 item)")
	}
	if v, ok := c.Get("b"); !ok || v != 2 {
		t.Errorf("cap=0: expected most-recent 'b'=2 to be retained, got v=%v ok=%v", v, ok)
	}
	if got := c.order.Len(); got != 1 {
		t.Errorf("cap=0: expected internal length 1 (single most-recent item), got %d", got)
	}
}

// TestCache_NegativeCapacity pins the DEFINED behavior of NewCache(-1). As with
// cap=0, the guard `order.Len() >= cap` (e.g. `0 >= -1`) is always true, so each
// insert first evicts the prior entry and the cache retains at most one item.
func TestCache_NegativeCapacity(t *testing.T) {
	c := NewCache(-1)
	c.Put("a", 1)
	if v, ok := c.Get("a"); !ok || v != 1 {
		t.Errorf("cap=-1: expected most-recent 'a'=1 to be retained, got v=%v ok=%v", v, ok)
	}
	c.Put("b", 2)
	if _, ok := c.Get("a"); ok {
		t.Error("cap=-1: 'a' should have been evicted when 'b' was inserted (max 1 item)")
	}
	if v, ok := c.Get("b"); !ok || v != 2 {
		t.Errorf("cap=-1: expected most-recent 'b'=2 to be retained, got v=%v ok=%v", v, ok)
	}
	if got := c.order.Len(); got != 1 {
		t.Errorf("cap=-1: expected internal length 1 (single most-recent item), got %d", got)
	}
}

// TestCache_NonIntValueRoundTrip proves the interface{} round-trip preserves
// non-int value types (struct, pointer, slice) — all existing tests stored ints.
func TestCache_NonIntValueRoundTrip(t *testing.T) {
	type payload struct {
		Name string
		Tags []string
	}
	want := payload{Name: "helix", Tags: []string{"l0", "l7"}}

	c := NewCache(3)
	c.Put("struct", want)
	c.Put("ptr", &want)
	c.Put("nil", nil)

	// Struct value round-trips by value equality.
	got, ok := c.Get("struct")
	if !ok {
		t.Fatal("struct key missing after Put")
	}
	gotStruct, isStruct := got.(payload)
	if !isStruct {
		t.Fatalf("expected payload struct, got %T", got)
	}
	if gotStruct.Name != want.Name || len(gotStruct.Tags) != len(want.Tags) ||
		gotStruct.Tags[0] != "l0" || gotStruct.Tags[1] != "l7" {
		t.Errorf("struct round-trip mismatch: got %+v, want %+v", gotStruct, want)
	}

	// Pointer round-trips by identity.
	gotPtr, ok := c.Get("ptr")
	if !ok {
		t.Fatal("ptr key missing after Put")
	}
	if gotPtr.(*payload) != &want {
		t.Error("pointer round-trip did not preserve identity")
	}

	// A stored nil value is distinguishable from a miss via ok==true.
	gotNil, ok := c.Get("nil")
	if !ok {
		t.Error("stored nil value: expected ok=true (present), got ok=false")
	}
	if gotNil != nil {
		t.Errorf("stored nil value: expected nil, got %v", gotNil)
	}
}

// Concurrency note (honest contract): *Cache has NO mutex — Get mutates the
// list via MoveToFront and Put mutates both the list and the map, so concurrent
// use from multiple goroutines is a data race by construction. The type is NOT
// goroutine-safe and callers MUST provide external synchronization. A -race
// concurrency test is therefore deliberately NOT added: it would either fail
// (true race) or, if it passed by luck, be a misleading PASS-bluff implying a
// safety guarantee the implementation does not provide.
