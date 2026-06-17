package costrouter

import (
	"errors"
	"math"
	"math/rand"
	"testing"
)

// This file targets the NaN/Inf objective-poison class. The production min-loop
// in Route compares float64 objective values with `<` and `==`. EVERY comparison
// against NaN is false, so a NaN-objective candidate placed at the head of the
// table seizes `best`/`bestScore` and is never displaced by any finite (truly
// cheaper / faster) candidate — a silent MIS-ROUTE. ±Inf is a sentinel that must
// likewise not slip through as a real value. The fix rejects any non-finite
// objective fail-closed with ErrNonFiniteObjective.
//
// Mutation proof: delete the `if !isFinite(...) { return ErrNonFiniteObjective }`
// guards in Route and these tests FAIL (the NaN candidate wins / no error).

// TestNaNObjectivePoison_HeadOfTable is the centerpiece. A NaN-objective provider
// sits at index 0 with a genuinely cheaper/faster finite provider behind it.
// Without the guard, Route returns the NaN provider (mis-route). With the guard,
// Route returns ErrNonFiniteObjective and never names a winner.
func TestNaNObjectivePoison_HeadOfTable(t *testing.T) {
	id := runUUID(t)

	for _, wl := range []WorkloadType{Inference, Training} {
		// Index 0 carries a NaN in the field that THIS workload minimizes.
		table := []Provider{
			{ID: "poison", CostPerHour: math.NaN(), LatencyMs: math.NaN()},
			{ID: "cheap-fast", CostPerHour: 1.0, LatencyMs: 5.0}, // the true optimum
			{ID: "mid", CostPerHour: 4.0, LatencyMs: 50.0},
		}
		t.Logf("run=%s wl=%s BEFORE table=%+v", id, wl, table)
		got, err := Route(wl, table)
		t.Logf("run=%s wl=%s AFTER got=%q err=%v", id, wl, got.ID, err)

		if !errors.Is(err, ErrNonFiniteObjective) {
			t.Fatalf("run=%s wl=%s: NaN objective at head not rejected: got winner=%q err=%v (want ErrNonFiniteObjective)",
				id, wl, got.ID, err)
		}
		// Sink-side: the poisoned candidate must NEVER be returned as the choice.
		if got.ID == "poison" {
			t.Fatalf("run=%s wl=%s: POISON — NaN candidate seized the best slot and was routed to", id, wl)
		}
		if got.ID != "" {
			t.Fatalf("run=%s wl=%s: rejected table must return zero Provider, got %q", id, wl, got.ID)
		}
	}
}

// TestNaNObjectivePoison_Interior covers a NaN that is NOT at the head. A correct
// fail-closed router still rejects it (a malformed candidate anywhere makes the
// table untrustworthy). This also guards a partial fix that only checks index 0.
func TestNaNObjectivePoison_Interior(t *testing.T) {
	id := runUUID(t)

	for _, wl := range []WorkloadType{Inference, Training} {
		table := []Provider{
			{ID: "a", CostPerHour: 3.0, LatencyMs: 30.0},
			{ID: "nan-mid", CostPerHour: math.NaN(), LatencyMs: math.NaN()}, // interior poison
			{ID: "b", CostPerHour: 1.0, LatencyMs: 5.0},
		}
		t.Logf("run=%s wl=%s BEFORE table=%+v", id, wl, table)
		got, err := Route(wl, table)
		t.Logf("run=%s wl=%s AFTER got=%q err=%v", id, wl, got.ID, err)
		if !errors.Is(err, ErrNonFiniteObjective) {
			t.Fatalf("run=%s wl=%s: interior NaN not rejected: got winner=%q err=%v", id, wl, got.ID, err)
		}
		if got.ID == "nan-mid" {
			t.Fatalf("run=%s wl=%s: interior NaN candidate was routed to", id, wl)
		}
	}
}

// TestInfObjectiveRejected pins that ±Inf objective values are rejected too. A
// +Inf cost is a sentinel "never use" value; routing must not treat it as a real
// orderable number. (Pre-fix, a -Inf at the head would even WIN, masquerading as
// "cheapest" — a cap/threshold bypass shape.)
func TestInfObjectiveRejected(t *testing.T) {
	id := runUUID(t)

	cases := []struct {
		name string
		wl   WorkloadType
		bad  Provider
	}{
		{"training+inf", Training, Provider{ID: "p", CostPerHour: math.Inf(1), LatencyMs: 5.0}},
		{"training-inf", Training, Provider{ID: "p", CostPerHour: math.Inf(-1), LatencyMs: 5.0}},
		{"inference+inf", Inference, Provider{ID: "p", CostPerHour: 5.0, LatencyMs: math.Inf(1)}},
		{"inference-inf", Inference, Provider{ID: "p", CostPerHour: 5.0, LatencyMs: math.Inf(-1)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Put the bad provider at the head, with a finite provider behind it.
			table := []Provider{tc.bad, {ID: "ok", CostPerHour: 2.0, LatencyMs: 20.0}}
			got, err := Route(tc.wl, table)
			t.Logf("run=%s case=%s AFTER got=%q err=%v", id, tc.name, got.ID, err)
			if !errors.Is(err, ErrNonFiniteObjective) {
				t.Fatalf("run=%s case=%s: non-finite objective not rejected: winner=%q err=%v", id, tc.name, got.ID, err)
			}
			if got.ID == "p" {
				t.Fatalf("run=%s case=%s: ±Inf candidate was routed to", id, tc.name)
			}
		})
	}
}

// TestNonObjectiveFieldNaNIsHarmless proves the guard is targeted, not blanket:
// a NaN in the field the workload does NOT minimize must NOT block routing.
// Training minimizes CostPerHour, so a NaN LatencyMs is irrelevant and the
// cheapest finite-cost provider must still be selected (and vice-versa for
// Inference). This stops the fix from over-rejecting valid tables.
func TestNonObjectiveFieldNaNIsHarmless(t *testing.T) {
	id := runUUID(t)

	// Training: NaN latency everywhere, but costs are finite -> route by cost.
	train := []Provider{
		{ID: "t-hi", CostPerHour: 9.0, LatencyMs: math.NaN()},
		{ID: "t-lo", CostPerHour: 1.0, LatencyMs: math.NaN()}, // cheapest
		{ID: "t-mid", CostPerHour: 4.0, LatencyMs: math.NaN()},
	}
	gotT, err := Route(Training, train)
	t.Logf("run=%s Training(NaN-latency) got=%q err=%v", id, gotT.ID, err)
	if err != nil {
		t.Fatalf("run=%s Training: NaN in NON-objective field wrongly rejected: %v", id, err)
	}
	if gotT.ID != "t-lo" {
		t.Fatalf("run=%s Training: got %q, want cheapest t-lo (NaN latency must be ignored)", id, gotT.ID)
	}

	// Inference: NaN cost everywhere, but latencies are finite -> route by latency.
	infer := []Provider{
		{ID: "i-slow", CostPerHour: math.NaN(), LatencyMs: 80.0},
		{ID: "i-fast", CostPerHour: math.NaN(), LatencyMs: 5.0}, // fastest
		{ID: "i-mid", CostPerHour: math.NaN(), LatencyMs: 40.0},
	}
	gotI, err := Route(Inference, infer)
	t.Logf("run=%s Inference(NaN-cost) got=%q err=%v", id, gotI.ID, err)
	if err != nil {
		t.Fatalf("run=%s Inference: NaN in NON-objective field wrongly rejected: %v", id, err)
	}
	if gotI.ID != "i-fast" {
		t.Fatalf("run=%s Inference: got %q, want fastest i-fast (NaN cost must be ignored)", id, gotI.ID)
	}
}

// TestFiniteTablesStillRouteCorrectly is a regression guard: across many random
// all-finite tables the guarded Route must behave identically to the oracle for
// both objectives. The fix must not perturb the happy path.
func TestFiniteTablesStillRouteCorrectly(t *testing.T) {
	id := runUUID(t)
	rng := rand.New(rand.NewSource(0x5A1AD))
	const trials = 1500
	for tr := 0; tr < trials; tr++ {
		n := 1 + rng.Intn(7)
		ps := make([]Provider, 0, n)
		seen := map[string]bool{}
		for len(ps) < n {
			pid := "q" + string(rune('A'+rng.Intn(26))) + string(rune('A'+rng.Intn(26))) + string(rune('0'+rng.Intn(10)))
			if seen[pid] {
				continue
			}
			seen[pid] = true
			ps = append(ps, Provider{
				ID:          pid,
				CostPerHour: float64(rng.Intn(6)),
				LatencyMs:   float64(rng.Intn(6)) * 10,
			})
		}
		for _, wl := range []WorkloadType{Inference, Training} {
			want := oracleMin(wl, ps)
			got, err := Route(wl, ps)
			if err != nil {
				t.Fatalf("run=%s trial=%d wl=%s finite table wrongly errored: %v table=%+v", id, tr, wl, err, ps)
			}
			if got.ID != want.ID {
				t.Fatalf("run=%s trial=%d wl=%s got %q want %q table=%+v", id, tr, wl, got.ID, want.ID, ps)
			}
		}
	}
	t.Logf("run=%s regression: %d finite tables x2 objectives still match oracle", id, trials)
}
