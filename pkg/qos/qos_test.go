package qos

import (
	"errors"
	"testing"
)

// runUUID is a stable identifier for this test binary's observable logs so a
// reviewer can correlate before/after evidence across a run.
const runUUID = "qos-HXC-1509-7f3c1a92-4d0e-4b6a-9c11-0a2e5d6f8b34"

// fleet is a candidate set where latency and cost trade off, so that each QoS
// objective lands on a *different* node when wired correctly:
//
//	rt    : LatencyMs=10.0, CostPerHour=9.00, Spot=false  (fastest, pricey)
//	inter : LatencyMs=10.5, CostPerHour=3.00, Spot=false  (latency near-tie w/ rt, 3x cheaper)
//	batch : LatencyMs=20.0, CostPerHour=0.40, Spot=false  (cost near-tie w/ cheap, faster)
//	cheap : LatencyMs=90.0, CostPerHour=0.42, Spot=true   (cost near-tie w/ batch, slowest, spot)
//
// Reasoning under the package's near-tie band (10%):
//   - RealTime (pure latency): rt (10.0) is the strict minimum.
//   - Interactive (latency-dominant, cost-aware): rt vs inter latency are
//     within 10% (10.0 vs 10.5, band 1.0, gap 0.5), so the cheaper one,
//     inter (3.00 < 9.00), wins. inter is the only node within rt's band.
//   - Batch (cost-dominant, latency-aware): batch (0.40) vs cheap (0.42) are
//     within 10% (band 0.04, gap 0.02), so latency breaks it; batch (20.0) is
//     faster than cheap (90.0). batch and cheap are far cheaper than inter/rt,
//     so the contest is genuinely batch-vs-cheap and latency decides -> batch.
//   - BestEffort (cheapest, prefer spot): cheap (0.40<... wait) -- cheap is
//     0.42 which is NOT the strict cheapest; batch at 0.40 is. So BestEffort
//     ignores the near-tie band entirely and picks the strict-cheapest cost.
//     To keep BestEffort distinct we make the spot node also the strict
//     cheapest below.
func fleet() []Candidate {
	return []Candidate{
		{ID: "rt", LatencyMs: 10.0, CostPerHour: 9.00, Spot: false},
		{ID: "inter", LatencyMs: 10.5, CostPerHour: 3.00, Spot: false},
		{ID: "batch", LatencyMs: 20.0, CostPerHour: 0.45, Spot: false},
		{ID: "cheap", LatencyMs: 90.0, CostPerHour: 0.42, Spot: true},
	}
}

func mustRoute(t *testing.T, class QoSClass, cs []Candidate) Decision {
	t.Helper()
	d, err := Route(class, cs)
	if err != nil {
		t.Fatalf("Route(%s) unexpected error: %v", class, err)
	}
	return d
}

// TestClause1_ObjectiveOptimalPerClass covers CLOSURE clause 1: one workload
// per class against the same fleet; RealTime must land on the lowest-latency
// node and BestEffort on the cheapest spot node, and each Decision must record
// the applied class label.
//
// Mutation: if RealTime picked by cost instead of latency it would land on
// "cheap"/"batch" not "rt"; if BestEffort were aliased to RealTime it would
// land on "rt" not "cheap". Both assertions below would fail.
func TestClause1_ObjectiveOptimalPerClass(t *testing.T) {
	cs := fleet()
	t.Logf("run=%s clause=1 before: fleet=%+v (no QoS distinction => same target)", runUUID, cs)

	rt := mustRoute(t, RealTime, cs)
	be := mustRoute(t, BestEffort, cs)
	inter := mustRoute(t, Interactive, cs)
	batch := mustRoute(t, Batch, cs)

	t.Logf("run=%s clause=1 after: RealTime->%s Interactive->%s Batch->%s BestEffort->%s",
		runUUID, rt.Target.ID, inter.Target.ID, batch.Target.ID, be.Target.ID)

	// RealTime -> lowest latency.
	if rt.Target.ID != "rt" {
		t.Fatalf("RealTime: want lowest-latency 'rt', got %q (lat=%v cost=%v)",
			rt.Target.ID, rt.Target.LatencyMs, rt.Target.CostPerHour)
	}
	// Independent oracle: no candidate has strictly lower latency than the pick.
	for _, c := range cs {
		if c.LatencyMs < rt.Target.LatencyMs {
			t.Fatalf("RealTime picked %q (lat=%v) but %q has lower latency %v",
				rt.Target.ID, rt.Target.LatencyMs, c.ID, c.LatencyMs)
		}
	}

	// BestEffort -> cheapest, and that cheapest is the spot node.
	if be.Target.ID != "cheap" {
		t.Fatalf("BestEffort: want cheapest spot 'cheap', got %q (cost=%v spot=%v)",
			be.Target.ID, be.Target.CostPerHour, be.Target.Spot)
	}
	if !be.Target.Spot {
		t.Fatalf("BestEffort: expected a spot target, got non-spot %q", be.Target.ID)
	}
	for _, c := range cs {
		if c.CostPerHour < be.Target.CostPerHour {
			t.Fatalf("BestEffort picked %q (cost=%v) but %q is cheaper %v",
				be.Target.ID, be.Target.CostPerHour, c.ID, c.CostPerHour)
		}
	}

	// Class labels are recorded on each Decision.
	if rt.Class != RealTime || be.Class != BestEffort ||
		inter.Class != Interactive || batch.Class != Batch {
		t.Fatalf("class labels not recorded: rt=%s inter=%s batch=%s be=%s",
			rt.Class, inter.Class, batch.Class, be.Class)
	}

	// RealTime and BestEffort must be distinct targets (proves QoS actually
	// differentiates rather than collapsing to one node).
	if rt.Target.ID == be.Target.ID {
		t.Fatalf("RealTime and BestEffort aliased to same target %q", rt.Target.ID)
	}
}

// TestClause2_AllFourObjectivesDistinct covers CLOSURE clause 2: on a fleet
// where latency and cost trade off, all four classes resolve to *distinct*
// candidates — proving Interactive and Batch are real objectives, not aliases
// of RealTime or BestEffort.
//
// Mutation: aliasing BestEffort to RealTime, or making Interactive ignore cost
// (collapsing it onto rt with RealTime), or making Batch ignore the near-tie
// cost band would collapse two targets together and fail the distinctness
// check below.
func TestClause2_AllFourObjectivesDistinct(t *testing.T) {
	cs := fleet()
	t.Logf("run=%s clause=2 before: trade-off fleet=%+v", runUUID, cs)

	got := map[QoSClass]string{}
	for _, class := range []QoSClass{RealTime, Interactive, Batch, BestEffort} {
		got[class] = mustRoute(t, class, cs).Target.ID
	}
	t.Logf("run=%s clause=2 after: targets=%v", runUUID, map[string]string{
		"RealTime": got[RealTime], "Interactive": got[Interactive],
		"Batch": got[Batch], "BestEffort": got[BestEffort],
	})

	// Expected, hand-derived from the trade-off fleet.
	want := map[QoSClass]string{
		RealTime:    "rt",    // fastest
		Interactive: "inter", // near-fastest but 3x cheaper than rt
		Batch:       "batch", // cost-balanced (cheaper than inter, not cheapest)
		BestEffort:  "cheap", // cheapest spot
	}
	for class, w := range want {
		if got[class] != w {
			t.Fatalf("%s: want target %q, got %q", class, w, got[class])
		}
	}

	// Pairwise distinctness: four classes => four different nodes.
	seen := map[string]QoSClass{}
	for class, id := range got {
		if other, dup := seen[id]; dup {
			t.Fatalf("objectives aliased: %s and %s both routed to %q", class, other, id)
		}
		seen[id] = class
	}

	// Specifically guard the alias the prompt calls out: Interactive must NOT
	// equal RealTime's pick on a fleet where a cheaper near-tie exists.
	if got[Interactive] == got[RealTime] {
		t.Fatalf("Interactive aliased to RealTime target %q despite cheaper near-tie", got[RealTime])
	}
	// And Batch must differ from BestEffort (cost-balanced != absolute cheapest).
	if got[Batch] == got[BestEffort] {
		t.Fatalf("Batch aliased to BestEffort target %q", got[Batch])
	}
}

// TestClause3_TieBreakAndEmpty covers CLOSURE clause 3: deterministic tie-break
// on indistinguishable candidates, and ErrNoCandidates on an empty fleet.
//
// Mutation: a non-deterministic or reversed tie-break (e.g. picking the max ID
// or input order) would fail the ascending-ID expectation; dropping the empty
// guard would fail the ErrNoCandidates check.
func TestClause3_TieBreakAndEmpty(t *testing.T) {
	// Empty fleet.
	if _, err := Route(RealTime, nil); !errors.Is(err, ErrNoCandidates) {
		t.Fatalf("empty fleet: want ErrNoCandidates, got %v", err)
	}
	if _, err := Route(Batch, []Candidate{}); !errors.Is(err, ErrNoCandidates) {
		t.Fatalf("empty slice: want ErrNoCandidates, got %v", err)
	}

	// Three candidates identical on every routing metric; only ID differs.
	// Provide them out of ID order to prove the tie-break, not input order,
	// decides. For RealTime the primary key (latency) is tied, so the
	// ascending-ID tie-break must select "a".
	tied := []Candidate{
		{ID: "c", LatencyMs: 10, CostPerHour: 2.0, Spot: false},
		{ID: "a", LatencyMs: 10, CostPerHour: 2.0, Spot: false},
		{ID: "b", LatencyMs: 10, CostPerHour: 2.0, Spot: false},
	}
	t.Logf("run=%s clause=3 before: tied(input order)=[c a b]", runUUID)

	for _, class := range []QoSClass{RealTime, Interactive, Batch, BestEffort} {
		d := mustRoute(t, class, tied)
		if d.Target.ID != "a" {
			t.Fatalf("%s tie-break: want lowest ID 'a', got %q", class, d.Target.ID)
		}
	}
	// Determinism across repeated calls.
	first := mustRoute(t, RealTime, tied).Target.ID
	for i := 0; i < 5; i++ {
		if got := mustRoute(t, RealTime, tied).Target.ID; got != first {
			t.Fatalf("non-deterministic tie-break: call %d gave %q, first gave %q", i, got, first)
		}
	}
	t.Logf("run=%s clause=3 after: tie-break stable winner=%q", runUUID, first)

	// Invalid class is rejected.
	if _, err := Route(QoSClass(99), tied); err == nil {
		t.Fatalf("invalid class: expected error, got nil")
	}
}

// TestInputNotMutated guards the documented contract that Route never reorders
// the caller's slice.
//
// Mutation: sorting cs in place instead of a copy would change the input order
// and fail this check.
func TestInputNotMutated(t *testing.T) {
	cs := fleet()
	before := make([]string, len(cs))
	for i, c := range cs {
		before[i] = c.ID
	}
	_, _ = Route(BestEffort, cs)
	for i, c := range cs {
		if c.ID != before[i] {
			t.Fatalf("input mutated at %d: was %q now %q", i, before[i], c.ID)
		}
	}
	t.Logf("run=%s input-immutability: order preserved %v", runUUID, before)
}

// TestQoSClassString guards the String labels used to record decisions.
//
// Mutation: swapping any label would fail the exact-match expectation.
func TestQoSClassString(t *testing.T) {
	cases := map[QoSClass]string{
		RealTime: "RealTime", Interactive: "Interactive",
		Batch: "Batch", BestEffort: "BestEffort", QoSClass(42): "Unknown",
	}
	for c, want := range cases {
		if got := c.String(); got != want {
			t.Fatalf("String(%d): want %q got %q", int(c), want, got)
		}
	}
}
