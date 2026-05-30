package backoff

import (
	"testing"
	"time"
)

func TestDefault(t *testing.T) {
	c := Default()
	if c.Base == 0 || c.Max == 0 {
		t.Error("expected non-zero base and max")
	}
}

func TestDuration(t *testing.T) {
	c := Config{Base: 100 * time.Millisecond, Max: 1 * time.Second, Factor: 2}
	if d := c.Duration(0); d != 100*time.Millisecond {
		t.Errorf("unexpected duration: %v", d)
	}
	if d := c.Duration(10); d != c.Max {
		t.Errorf("expected capped duration, got %v", d)
	}
}
