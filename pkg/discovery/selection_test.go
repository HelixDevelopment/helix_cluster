package discovery

import (
	"context"
	"testing"
	"time"
)

// fakeClock is an injectable, manually-advanced clock for deterministic TTL tests.
type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

// --- Smooth Weighted Round-Robin selector ---

func TestSmoothWeightedSelector_Distribution(t *testing.T) {
	// Weights 5,1,1 over 7 picks must produce exactly that count distribution,
	// deterministically (no randomness, no time dependence).
	insts := []*Instance{
		{ID: "a", Service: "svc", Weight: 5, Healthy: true},
		{ID: "b", Service: "svc", Weight: 1, Healthy: true},
		{ID: "c", Service: "svc", Weight: 1, Healthy: true},
	}
	sel := NewWeightedSelector()
	counts := map[string]int{}
	for i := 0; i < 7; i++ {
		pick := sel.Pick(insts)
		if pick == nil {
			t.Fatalf("pick %d returned nil", i)
		}
		counts[pick.ID]++
	}
	if counts["a"] != 5 || counts["b"] != 1 || counts["c"] != 1 {
		t.Fatalf("expected a=5,b=1,c=1 over 7 picks, got %v", counts)
	}
}

func TestSmoothWeightedSelector_Deterministic(t *testing.T) {
	// Two independent selectors over the same input must produce the IDENTICAL
	// sequence of picks -> proves determinism (no time-based randomness).
	mk := func() []*Instance {
		return []*Instance{
			{ID: "a", Service: "svc", Weight: 3, Healthy: true},
			{ID: "b", Service: "svc", Weight: 2, Healthy: true},
			{ID: "c", Service: "svc", Weight: 1, Healthy: true},
		}
	}
	s1 := NewWeightedSelector()
	s2 := NewWeightedSelector()
	for i := 0; i < 12; i++ {
		p1 := s1.Pick(mk())
		p2 := s2.Pick(mk())
		if p1.ID != p2.ID {
			t.Fatalf("non-deterministic at step %d: %s vs %s", i, p1.ID, p2.ID)
		}
	}
}

func TestSmoothWeightedSelector_Distribution_Mutation(t *testing.T) {
	// Mutation: prove weights actually bias selection. If weights were ignored
	// (e.g. round-robin), a weight-10 vs weight-1 instance would NOT dominate.
	insts := []*Instance{
		{ID: "heavy", Service: "svc", Weight: 10, Healthy: true},
		{ID: "light", Service: "svc", Weight: 1, Healthy: true},
	}
	sel := NewWeightedSelector()
	counts := map[string]int{}
	for i := 0; i < 11; i++ {
		counts[sel.Pick(insts).ID]++
	}
	if counts["heavy"] != 10 || counts["light"] != 1 {
		t.Fatalf("weights not biasing selection: got %v (expected heavy=10, light=1)", counts)
	}
}

func TestWeightedSelector_DefaultWeight(t *testing.T) {
	// Weight <= 0 must be treated as 1 so unset weights still participate fairly.
	insts := []*Instance{
		{ID: "a", Service: "svc", Weight: 0, Healthy: true},
		{ID: "b", Service: "svc", Weight: 0, Healthy: true},
	}
	sel := NewWeightedSelector()
	counts := map[string]int{}
	for i := 0; i < 4; i++ {
		counts[sel.Pick(insts).ID]++
	}
	if counts["a"] != 2 || counts["b"] != 2 {
		t.Fatalf("expected even 2/2 split for default weights, got %v", counts)
	}
}

func TestWeightedSelector_Empty(t *testing.T) {
	sel := NewWeightedSelector()
	if got := sel.Pick(nil); got != nil {
		t.Fatalf("expected nil on empty input, got %v", got)
	}
	if got := sel.Pick([]*Instance{}); got != nil {
		t.Fatalf("expected nil on empty slice, got %v", got)
	}
}

// --- Registry.Select: health-aware + weighted + deterministic TTL expiry ---

func TestServiceRegistry_Select_SkipsUnhealthy(t *testing.T) {
	clk := &fakeClock{now: time.Unix(1000, 0)}
	r := NewStaticRegistry(WithClock(clk))

	_ = r.Register(context.Background(), &Instance{ID: "good", Service: "svc", Weight: 1, TTL: time.Hour})
	_ = r.Register(context.Background(), &Instance{ID: "bad", Service: "svc", Weight: 100, TTL: time.Hour})

	// Mark "bad" unhealthy directly in the backend.
	backend := r.backend.(*InMemoryBackend)
	_ = backend.Put(context.Background(), "svc/bad", &Instance{ID: "bad", Service: "svc", Weight: 100, Healthy: false, TTL: time.Hour})

	for i := 0; i < 5; i++ {
		pick, err := r.Select(context.Background(), "svc")
		if err != nil {
			t.Fatalf("select error: %v", err)
		}
		if pick == nil || pick.ID != "good" {
			t.Fatalf("expected to always pick healthy 'good', got %v", pick)
		}
	}
}

func TestServiceRegistry_Select_SkipsUnhealthy_Mutation(t *testing.T) {
	// Mutation: prove an unhealthy instance is skipped even with huge weight.
	clk := &fakeClock{now: time.Unix(1000, 0)}
	r := NewStaticRegistry(WithClock(clk))
	_ = r.Register(context.Background(), &Instance{ID: "good", Service: "svc", Weight: 1, TTL: time.Hour})
	backend := r.backend.(*InMemoryBackend)
	_ = backend.Put(context.Background(), "svc/bad", &Instance{ID: "bad", Service: "svc", Weight: 100, Healthy: false, TTL: time.Hour})

	pick, _ := r.Select(context.Background(), "svc")
	if pick != nil && pick.ID == "bad" {
		t.Fatal("unhealthy instance must never be selected")
	}
}

func TestServiceRegistry_Select_ExcludesExpired_DeterministicClock(t *testing.T) {
	clk := &fakeClock{now: time.Unix(1000, 0)}
	r := NewStaticRegistry(WithClock(clk))

	_ = r.Register(context.Background(), &Instance{ID: "live", Service: "svc", Weight: 1, TTL: 30 * time.Second})
	_ = r.Register(context.Background(), &Instance{ID: "stale", Service: "svc", Weight: 100, TTL: 10 * time.Second})

	// Advance past "stale" TTL but within "live" TTL: no real sleep involved.
	clk.advance(15 * time.Second)

	for i := 0; i < 5; i++ {
		pick, err := r.Select(context.Background(), "svc")
		if err != nil {
			t.Fatalf("select error: %v", err)
		}
		if pick == nil || pick.ID != "live" {
			t.Fatalf("expected only non-expired 'live', got %v", pick)
		}
	}
}

func TestServiceRegistry_Select_ExcludesExpired_Mutation(t *testing.T) {
	// Mutation: prove an expired instance is excluded. If expiry were ignored,
	// the heavy-weight "stale" instance would dominate selection.
	clk := &fakeClock{now: time.Unix(1000, 0)}
	r := NewStaticRegistry(WithClock(clk))
	_ = r.Register(context.Background(), &Instance{ID: "live", Service: "svc", Weight: 1, TTL: 30 * time.Second})
	_ = r.Register(context.Background(), &Instance{ID: "stale", Service: "svc", Weight: 100, TTL: 10 * time.Second})

	clk.advance(15 * time.Second)

	pick, _ := r.Select(context.Background(), "svc")
	if pick != nil && pick.ID == "stale" {
		t.Fatal("expired instance must never be selected")
	}
}

func TestServiceRegistry_Select_EmptyService(t *testing.T) {
	clk := &fakeClock{now: time.Unix(1000, 0)}
	r := NewStaticRegistry(WithClock(clk))
	pick, err := r.Select(context.Background(), "nope")
	if err == nil {
		t.Fatal("expected error when no instances available")
	}
	if pick != nil {
		t.Fatalf("expected nil pick, got %v", pick)
	}
}

func TestServiceRegistry_TTLEviction_InjectedClock(t *testing.T) {
	// Deterministic sweep using injected clock: SweepExpired evicts without sleeps.
	clk := &fakeClock{now: time.Unix(1000, 0)}
	r := NewStaticRegistry(WithClock(clk))

	_ = r.Register(context.Background(), &Instance{ID: "i1", Service: "svc", TTL: 200 * time.Millisecond})

	list, _ := r.Lookup(context.Background(), "svc")
	if len(list) != 1 {
		t.Fatalf("expected 1 instance initially, got %d", len(list))
	}

	clk.advance(500 * time.Millisecond)
	r.SweepExpired()

	list, _ = r.Lookup(context.Background(), "svc")
	if len(list) != 0 {
		t.Fatalf("expected 0 instances after deterministic sweep, got %d", len(list))
	}
}

func TestServiceRegistry_TTLEviction_InjectedClock_Mutation(t *testing.T) {
	// Mutation: prove the sweep actually uses the clock. Before advancing, the
	// instance must still be present (not evicted prematurely).
	clk := &fakeClock{now: time.Unix(1000, 0)}
	r := NewStaticRegistry(WithClock(clk))
	_ = r.Register(context.Background(), &Instance{ID: "i1", Service: "svc", TTL: 200 * time.Millisecond})

	// Sweep BEFORE TTL elapses: must NOT evict.
	clk.advance(50 * time.Millisecond)
	r.SweepExpired()
	list, _ := r.Lookup(context.Background(), "svc")
	if len(list) != 1 {
		t.Fatalf("instance evicted before TTL elapsed: got %d, want 1", len(list))
	}

	// Now advance past TTL and sweep: must evict.
	clk.advance(500 * time.Millisecond)
	r.SweepExpired()
	list, _ = r.Lookup(context.Background(), "svc")
	if len(list) != 0 {
		t.Fatalf("instance not evicted after TTL elapsed: got %d, want 0", len(list))
	}
}
