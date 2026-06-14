package scan

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// adversarialClock is a trivial fixed clock for adversarial tests.
func adversarialClock() func() time.Time {
	t0 := time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC)
	return func() time.Time { return t0 }
}

// ---------------------------------------------------------------------------
// TestAdversarial_LeastLoadedMinNotFirstNotLast (CENTERPIECE)
//
// Hand-verified least-loaded selection where the winner is neither the
// first-added nor the last-added backend, AND a backend with a
// lexicographically SMALLER id but HIGHER load is present. This proves the
// selection is keyed on Load (primary) and only falls back to ID for ties —
// not the other way around. If the production comparison were ID-primary or
// inverted to pick the max load, this assertion bites.
//
// Loads (insertion order): "m-busy"=90, "a-heavy"=70, "z-light"=3, "k-mid"=40
// Smallest load = 3 -> "z-light". Note "a-heavy" has the smallest id ('a')
// but is NOT chosen, because its load (70) is far from minimal.
// ---------------------------------------------------------------------------
func TestAdversarial_LeastLoadedMinNotFirstNotLast(t *testing.T) {
	reg := New("helix-adv-leastloaded", adversarialClock())

	reg.AddBackend(Backend{ID: "m-busy", Load: 90, Healthy: true})  // first added
	reg.AddBackend(Backend{ID: "a-heavy", Load: 70, Healthy: true}) // smallest id, high load
	reg.AddBackend(Backend{ID: "z-light", Load: 3, Healthy: true})  // the true minimum (middle)
	reg.AddBackend(Backend{ID: "k-mid", Load: 40, Healthy: true})   // last added

	chosen, err := reg.Route("adv-ll-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chosen != "z-light" {
		t.Fatalf("least-loaded selection wrong: want z-light (Load=3), got %q", chosen)
	}

	// Sink-side: history recorded the same winner.
	hist := reg.History()
	if len(hist) != 1 || hist[0].NodeID != "z-light" || hist[0].RunID != "adv-ll-001" {
		t.Fatalf("history mismatch: %+v", hist)
	}
}

// ---------------------------------------------------------------------------
// TestAdversarial_StabilityUnrelatedMembershipChange (CENTERPIECE)
//
// The chosen endpoint MUST NOT move when an UNRELATED backend (one that is not
// and would not become the least-loaded) joins or leaves. Only a membership
// change touching the winner — or introducing a strictly-cheaper node — may
// move the route. A full reshuffle on an unrelated change is a bug.
//
// Winner is "win" (Load=5). We then:
//   - add an unrelated heavier node       -> winner unchanged
//   - add another unrelated heavier node  -> winner unchanged
//   - remove an unrelated heavier node    -> winner unchanged
//
// Across all of these, Route must keep returning "win".
// ---------------------------------------------------------------------------
func TestAdversarial_StabilityUnrelatedMembershipChange(t *testing.T) {
	reg := New("helix-adv-stability", adversarialClock())

	reg.AddBackend(Backend{ID: "win", Load: 5, Healthy: true})
	reg.AddBackend(Backend{ID: "other-a", Load: 50, Healthy: true})

	base, err := reg.Route("adv-stab-base")
	if err != nil {
		t.Fatalf("base route error: %v", err)
	}
	if base != "win" {
		t.Fatalf("base winner: want win, got %q", base)
	}

	// Unrelated JOIN (heavier than winner) — winner must not move.
	reg.AddBackend(Backend{ID: "other-b", Load: 80, Healthy: true})
	if got, err := reg.Route("adv-stab-join1"); err != nil || got != "win" {
		t.Fatalf("after unrelated heavy join: want win, got %q (err=%v)", got, err)
	}

	// Another unrelated JOIN with an id that sorts BEFORE the winner but heavier
	// load. If the selection ever leaked to id-ordering this would steal the
	// route; it must not.
	reg.AddBackend(Backend{ID: "aaa-but-heavy", Load: 99, Healthy: true})
	if got, err := reg.Route("adv-stab-join2"); err != nil || got != "win" {
		t.Fatalf("after low-id heavy join: want win, got %q (err=%v)", got, err)
	}

	// Unrelated LEAVE (remove a non-winner) — winner must not move.
	reg.RemoveBackend("other-a")
	if got, err := reg.Route("adv-stab-leave"); err != nil || got != "win" {
		t.Fatalf("after unrelated leave: want win, got %q (err=%v)", got, err)
	}

	// Endpoint name stable throughout.
	if reg.EndpointName() != "helix-adv-stability" {
		t.Fatalf("endpoint name drifted: %q", reg.EndpointName())
	}
}

// ---------------------------------------------------------------------------
// TestAdversarial_DeterminismAcrossFreshRegistries
//
// The real map-iteration-order leak probe. Build the SAME backend set many
// times in DIFFERENT insertion orders and across independent Registry values
// (each has its own freshly-seeded map), and confirm Route always returns the
// identical winner. Equal inputs -> equal route, regardless of map seeding.
// A map-order-dependent tie-break would eventually disagree here.
// ---------------------------------------------------------------------------
func TestAdversarial_DeterminismAcrossFreshRegistries(t *testing.T) {
	// Two backends tied at the minimum load (7); winner must be the smaller id.
	type spec struct {
		id      string
		load    int
		healthy bool
	}
	// Multiple distinct insertion orders of the SAME logical set.
	orders := [][]spec{
		{{"d", 7, true}, {"b", 7, true}, {"c", 30, true}, {"a", 99, true}},
		{{"a", 99, true}, {"c", 30, true}, {"b", 7, true}, {"d", 7, true}},
		{{"c", 30, true}, {"d", 7, true}, {"a", 99, true}, {"b", 7, true}},
		{{"b", 7, true}, {"a", 99, true}, {"d", 7, true}, {"c", 30, true}},
	}

	const want = "b" // min load 7 tied between b and d -> smaller id b

	for trial := 0; trial < 200; trial++ {
		order := orders[trial%len(orders)]
		reg := New("helix-adv-determinism", adversarialClock())
		for _, s := range order {
			reg.AddBackend(Backend{ID: s.id, Load: s.load, Healthy: s.healthy})
		}
		got, err := reg.Route(fmt.Sprintf("adv-det-%d", trial))
		if err != nil {
			t.Fatalf("trial %d: unexpected error: %v", trial, err)
		}
		if got != want {
			t.Fatalf("trial %d (order %v): nondeterministic/wrong winner: want %q, got %q",
				trial, order, want, got)
		}
	}
}

// ---------------------------------------------------------------------------
// TestAdversarial_RepeatedRouteSameRegistryStable
//
// Within a single registry, repeated Route calls with no membership change
// must keep returning the same winner (the choice does not drift between calls
// because of internal map iteration).
// ---------------------------------------------------------------------------
func TestAdversarial_RepeatedRouteSameRegistryStable(t *testing.T) {
	reg := New("helix-adv-repeat", adversarialClock())
	reg.AddBackend(Backend{ID: "p", Load: 12, Healthy: true})
	reg.AddBackend(Backend{ID: "q", Load: 4, Healthy: true})
	reg.AddBackend(Backend{ID: "r", Load: 4, Healthy: true}) // tie with q at min; q wins by id
	reg.AddBackend(Backend{ID: "s", Load: 8, Healthy: true})

	const want = "q"
	for i := 0; i < 1000; i++ {
		got, err := reg.Route(fmt.Sprintf("adv-rep-%d", i))
		if err != nil {
			t.Fatalf("iter %d: error: %v", i, err)
		}
		if got != want {
			t.Fatalf("iter %d: route drifted: want %q, got %q", i, want, got)
		}
	}
}

// ---------------------------------------------------------------------------
// TestAdversarial_UnhealthyLowestLoadExcluded
//
// The strictly-lowest-load node is UNHEALTHY and must be excluded; routing
// must fall to the cheapest HEALTHY node even though a cheaper (down) node
// exists. Removing the health filter in production would make this bite.
// ---------------------------------------------------------------------------
func TestAdversarial_UnhealthyLowestLoadExcluded(t *testing.T) {
	reg := New("helix-adv-health", adversarialClock())

	reg.AddBackend(Backend{ID: "down-cheapest", Load: 1, Healthy: false}) // lowest load, DOWN
	reg.AddBackend(Backend{ID: "up-mid", Load: 25, Healthy: true})
	reg.AddBackend(Backend{ID: "up-cheap", Load: 9, Healthy: true}) // cheapest healthy
	reg.AddBackend(Backend{ID: "down-also", Load: 2, Healthy: false})

	chosen, err := reg.Route("adv-health-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chosen != "up-cheap" {
		t.Fatalf("unhealthy exclusion failed: want up-cheap (Load=9), got %q", chosen)
	}
	if chosen == "down-cheapest" || chosen == "down-also" {
		t.Fatalf("routed to an UNHEALTHY backend: %q", chosen)
	}
}

// ---------------------------------------------------------------------------
// TestAdversarial_TieMinLoadLowerIdHigherLoadPresent
//
// A node with a globally-smaller id exists but carries a HIGHER load; it must
// NOT win. The tie-break by id applies ONLY among the equal-minimum-load set.
//
// Loads: "aaa"=50 (smallest id, NOT min load), "ggg"=5, "ddd"=5 (tie at min).
// Min load = 5, tied between ddd and ggg -> smaller id "ddd" wins; "aaa" loses.
// ---------------------------------------------------------------------------
func TestAdversarial_TieMinLoadLowerIdHigherLoadPresent(t *testing.T) {
	reg := New("helix-adv-tie", adversarialClock())

	reg.AddBackend(Backend{ID: "aaa", Load: 50, Healthy: true}) // smallest id, but heavy
	reg.AddBackend(Backend{ID: "ggg", Load: 5, Healthy: true})
	reg.AddBackend(Backend{ID: "ddd", Load: 5, Healthy: true}) // ties ggg at the min

	chosen, err := reg.Route("adv-tie-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chosen != "ddd" {
		t.Fatalf("tie-break among min-load set wrong: want ddd, got %q", chosen)
	}
	if chosen == "aaa" {
		t.Fatalf("id tie-break wrongly overrode load primary: got aaa")
	}
}

// ---------------------------------------------------------------------------
// TestAdversarial_EdgeCases
//
// Single backend, all-equal-load, and empty registry behaviour.
// ---------------------------------------------------------------------------
func TestAdversarial_EdgeCases(t *testing.T) {
	t.Run("single backend", func(t *testing.T) {
		reg := New("helix-adv-single", adversarialClock())
		reg.AddBackend(Backend{ID: "solo", Load: 42, Healthy: true})
		got, err := reg.Route("adv-edge-single")
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		if got != "solo" {
			t.Fatalf("single backend: want solo, got %q", got)
		}
	})

	t.Run("all equal load -> smallest id", func(t *testing.T) {
		reg := New("helix-adv-equal", adversarialClock())
		reg.AddBackend(Backend{ID: "yankee", Load: 17, Healthy: true})
		reg.AddBackend(Backend{ID: "alpha", Load: 17, Healthy: true})
		reg.AddBackend(Backend{ID: "mike", Load: 17, Healthy: true})
		got, err := reg.Route("adv-edge-equal")
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		if got != "alpha" {
			t.Fatalf("all-equal-load: want alpha (smallest id), got %q", got)
		}
	})

	t.Run("empty registry returns ErrNoHealthyBackend", func(t *testing.T) {
		reg := New("helix-adv-empty", adversarialClock())
		got, err := reg.Route("adv-edge-empty")
		if !errors.Is(err, ErrNoHealthyBackend) {
			t.Fatalf("empty: want ErrNoHealthyBackend, got err=%v", err)
		}
		if got != "" {
			t.Fatalf("empty: want empty node id, got %q", got)
		}
	})

	t.Run("single unhealthy returns error not panic", func(t *testing.T) {
		reg := New("helix-adv-single-down", adversarialClock())
		reg.AddBackend(Backend{ID: "down", Load: 0, Healthy: false})
		got, err := reg.Route("adv-edge-single-down")
		if !errors.Is(err, ErrNoHealthyBackend) {
			t.Fatalf("single unhealthy: want ErrNoHealthyBackend, got err=%v", err)
		}
		if got != "" {
			t.Fatalf("single unhealthy: want empty node id, got %q", got)
		}
	})
}
