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
