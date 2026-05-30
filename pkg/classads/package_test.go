package classads

import "testing"

func TestClassAd(t *testing.T) {
	c := New()
	c.Set("name", "node-1")
	v, ok := c.Get("name")
	if !ok || v != "node-1" {
		t.Errorf("expected node-1, got %v", v)
	}
	_, ok = c.Get("missing")
	if ok {
		t.Error("expected missing key to be absent")
	}
}

// --- Mutation tests ---

func TestClassAd_GetMissingKey_Mutation(t *testing.T) {
	// Mutation: Get returns true for missing keys → breaks lookup semantics
	c := New()
	_, ok := c.Get("nonexistent")
	if ok {
		t.Error("missing key should return false")
	}
}

func TestClassAd_Overwrite_Mutation(t *testing.T) {
	// Mutation: Set no longer overwrites → second Set is ignored
	c := New()
	c.Set("key", "first")
	c.Set("key", "second")
	v, ok := c.Get("key")
	if !ok {
		t.Fatal("key should exist")
	}
	if v != "second" {
		t.Errorf("overwrite should store second value, got %v", v)
	}
}

func TestClassAd_NilMapSet_Mutation(t *testing.T) {
	// Mutation: New returns nil map → Set would panic on nil map assignment
	c := New()
	// Ensure New does not return nil
	if c == nil {
		t.Fatal("New() must not return nil")
	}
	// This should not panic
	c.Set("key", "value")
	v, ok := c.Get("key")
	if !ok || v != "value" {
		t.Errorf("expected value after Set, got %v, ok=%v", v, ok)
	}
}
