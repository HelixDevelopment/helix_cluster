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

// --- Mutation tests ---

func TestChecker_DefaultHealthy_Mutation(t *testing.T) {
	// Mutation: NewChecker initializes to Unhealthy or empty string
	c := NewChecker()
	if c.GetStatus() != Healthy {
		t.Errorf("default status must be Healthy, got %s", c.GetStatus())
	}
}

func TestChecker_SetStatusPersists_Mutation(t *testing.T) {
	// Mutation: SetStatus is no-op → status never changes
	c := NewChecker()
	c.SetStatus(Unhealthy)
	if c.GetStatus() != Unhealthy {
		t.Errorf("status should be Unhealthy after SetStatus, got %s", c.GetStatus())
	}
	c.SetStatus(Degraded)
	if c.GetStatus() != Degraded {
		t.Errorf("status should be Degraded after second SetStatus, got %s", c.GetStatus())
	}
}

func TestChecker_ConcurrentAccess_Mutation(t *testing.T) {
	// Mutation: mutex removed → concurrent SetStatus/GetStatus races or corrupts state
	c := NewChecker()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			c.SetStatus(Healthy)
			c.SetStatus(Unhealthy)
		}
		close(done)
	}()
	for i := 0; i < 100; i++ {
		_ = c.GetStatus()
	}
	<-done
	// If we reach here without panic or data race, the mutex is doing its job
}
