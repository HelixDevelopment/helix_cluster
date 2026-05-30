package metrics

import "testing"

func TestCounter(t *testing.T) {
	c := &Counter{}
	c.Inc()
	c.Inc()
	c.Add(3)
	if c.Value() != 5 {
		t.Errorf("expected 5, got %d", c.Value())
	}
}
