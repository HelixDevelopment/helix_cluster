package metrics

import (
	"sync"
	"testing"
)

func TestCounter(t *testing.T) {
	c := &Counter{}
	c.Inc()
	c.Inc()
	c.Add(3)
	if c.Value() != 5 {
		t.Errorf("expected 5, got %d", c.Value())
	}
}

// --- Mutation tests ---

func TestCounter_ConcurrentInc_Mutation(t *testing.T) {
	// Mutation: Inc not atomic → concurrent increments lose updates
	c := &Counter{}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Inc()
		}()
	}
	wg.Wait()
	if c.Value() != 100 {
		t.Errorf("expected 100 after 100 concurrent increments, got %d", c.Value())
	}
}

func TestCounter_AddNegative_Mutation(t *testing.T) {
	// Mutation: Add ignores negative values or uses unsigned arithmetic
	c := &Counter{}
	c.Add(10)
	c.Add(-3)
	if c.Value() != 7 {
		t.Errorf("expected 7 after Add(10) then Add(-3), got %d", c.Value())
	}
}

func TestCounter_ValueIsolation_Mutation(t *testing.T) {
	// Mutation: Counter shares internal state across instances
	c1 := &Counter{}
	c2 := &Counter{}
	c1.Inc()
	if c2.Value() != 0 {
		t.Error("separate Counter instances must not share state")
	}
}
