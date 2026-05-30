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
