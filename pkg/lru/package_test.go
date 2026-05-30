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
