package health

import "testing"

func TestChecker(t *testing.T) {
	c := NewChecker()
	if c.GetStatus() != Healthy {
		t.Errorf("expected healthy, got %s", c.GetStatus())
	}
	c.SetStatus(Degraded)
	if c.GetStatus() != Degraded {
		t.Errorf("expected degraded, got %s", c.GetStatus())
	}
}
