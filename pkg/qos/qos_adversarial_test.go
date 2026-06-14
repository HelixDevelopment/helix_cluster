package qos

import (
	"errors"
	"fmt"
	"math/rand"
	"testing"
)

// advUUID identifies this adversarial binary's observable logs for evidence
// correlation across a run.
const advUUID = "qos-adv-HXC-1509-b71d3e4a-6c20-4f55-8a0e-2d9f7c4e1a66"

// oracleTarget is an INDEPENDENT reference implementation of each class
// objective, deliberately written in a different style from production Route
// (explicit single-pass argmin, no sort) so it cannot share a bug with it.
//
// It encodes the DOCUMENTED semantics with the near-tie band ANCHORED to the
// fleet-global best of the primary metric — the only interpretation that is a
// total, order-independent function and that reproduces every winner the
// existing (single-scenario) test asserts on the documented fleet. A *pairwise*
// band (the pre-fix production code) is intransitive and is exactly what this
// oracle exists to catch.
func oracleTarget(class QoSClass, cs []Candidate) Candidate {
	const frac = nearTieFrac
	const floor = nearTieFloor

	// argmin over cs using a strict "better than current best" predicate.
	pick := func(better func(cand, best Candidate) bool) Candidate {
		best := cs[0]
		for _, c := range cs[1:] {
			if better(c, best) {
				best = c
			}
		}
		return best
	}

	switch class {
	case RealTime:
		return pick(func(c, b Candidate) bool {
			if c.LatencyMs != b.LatencyMs {
				return c.LatencyMs < b.LatencyMs
			}
			return c.ID < b.ID
		})

	case BestEffort:
		return pick(func(c, b Candidate) bool {
			if c.CostPerHour != b.CostPerHour {
				return c.CostPerHour < b.CostPerHour
			}
			if c.Spot != b.Spot {
				return c.Spot // spot preferred at equal cost
			}
			return c.ID < b.ID
		})

	case Interactive:
		// Anchor band to global-min latency.
		minLat := cs[0].LatencyMs
		for _, c := range cs {
			if c.LatencyMs < minLat {
				minLat = c.LatencyMs
			}
		}
		band := minLat*frac + floor
		near := func(c Candidate) bool { return c.LatencyMs <= minLat+band }
		return pick(func(c, b Candidate) bool {
			cn, bn := near(c), near(b)
			if cn != bn {
				return cn
			}
			if !cn { // neither near-best: pure latency, then ID
				if c.LatencyMs != b.LatencyMs {
					return c.LatencyMs < b.LatencyMs
				}
				return c.ID < b.ID
			}
			if c.CostPerHour != b.CostPerHour {
				return c.CostPerHour < b.CostPerHour
			}
			if c.LatencyMs != b.LatencyMs {
				return c.LatencyMs < b.LatencyMs
			}
			return c.ID < b.ID
		})

	case Batch:
		// Anchor band to global-min cost.
		minCost := cs[0].CostPerHour
		for _, c := range cs {
			if c.CostPerHour < minCost {
				minCost = c.CostPerHour
			}
		}
		band := minCost*frac + floor
		near := func(c Candidate) bool { return c.CostPerHour <= minCost+band }
		return pick(func(c, b Candidate) bool {
			cn, bn := near(c), near(b)
			if cn != bn {
				return cn
			}
			if !cn { // neither near-cheapest: pure cost, then ID
				if c.CostPerHour != b.CostPerHour {
					return c.CostPerHour < b.CostPerHour
				}
				return c.ID < b.ID
			}
			if c.LatencyMs != b.LatencyMs {
				return c.LatencyMs < b.LatencyMs
			}
			if c.CostPerHour != b.CostPerHour {
				return c.CostPerHour < b.CostPerHour
			}
			return c.ID < b.ID
		})
	}
	panic("oracle: invalid class")
}

func shuffled(r *rand.Rand, cs []Candidate) []Candidate {
	out := make([]Candidate, len(cs))
	copy(out, cs)
	r.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// TestAdv_ObjectiveMatchesOracle_AllClasses is the core correctness invariant:
// for thousands of random fleets, Route's target MUST equal the independent
// anchored-argmin oracle for every class.
//
// ANTI-BLUFF — why this bites:
//   - It pins the *exact* documented winner via a second, structurally
//     different implementation (argmin, not sort). A wrong objective (e.g.
//     Interactive ignoring cost, Batch ignoring latency, BestEffort losing the
//     spot preference) makes Route disagree with the oracle on some fleet and
//     fails here. Random small-integer metrics manufacture many near-ties so
//     the band logic is genuinely exercised, not bypassed.
func TestAdv_ObjectiveMatchesOracle_AllClasses(t *testing.T) {
	r := rand.New(rand.NewSource(0xC0FFEE))
	classes := []QoSClass{RealTime, Interactive, Batch, BestEffort}
	const iters = 60000
	checked := 0
	for it := 0; it < iters; it++ {
		n := 1 + r.Intn(6)
		cs := make([]Candidate, n)
		for i := range cs {
			cs[i] = Candidate{
				ID:          fmt.Sprintf("n%02d", i),
				LatencyMs:   float64(r.Intn(40)),
				CostPerHour: float64(r.Intn(20)) / 10.0,
				Spot:        r.Intn(2) == 0,
			}
		}
		for _, class := range classes {
			got, err := Route(class, cs)
			if err != nil {
				t.Fatalf("Route(%s) unexpected error on %+v: %v", class, cs, err)
			}
			want := oracleTarget(class, cs)
			if got.Target.ID != want.ID {
				t.Fatalf("OBJECTIVE MISMATCH class=%s\n got=%q (lat=%v cost=%v spot=%v)\n want=%q (lat=%v cost=%v spot=%v)\n fleet=%+v",
					class, got.Target.ID, got.Target.LatencyMs, got.Target.CostPerHour, got.Target.Spot,
					want.ID, want.LatencyMs, want.CostPerHour, want.Spot, cs)
			}
			checked++
		}
	}
	t.Logf("run=%s TestAdv_ObjectiveMatchesOracle_AllClasses: %d (fleet,class) checks agreed with oracle", advUUID, checked)
}

// TestAdv_OrderIndependence is the regression bite for the intransitive-band
// bug: routing a fleet and any permutation of it MUST yield the same target.
//
// ANTI-BLUFF — why this bites:
//   - The pre-fix Interactive/Batch comparators used a *pairwise* near-tie
//     band, which is intransitive; sort.SliceStable on an intransitive less
//     returns a winner that depends on input order. This test shuffles each
//     fleet many times and asserts a single stable target. Against the buggy
//     code it FAILS (empirically ~hundreds of flips over this corpus); against
//     the fixed (anchored) comparator it passes. It cannot pass vacuously: the
//     small-integer metrics guarantee dense near-tie chains.
func TestAdv_OrderIndependence(t *testing.T) {
	r := rand.New(rand.NewSource(0xABCDEF))
	classes := []QoSClass{RealTime, Interactive, Batch, BestEffort}
	const iters = 40000
	perms := 0
	for it := 0; it < iters; it++ {
		n := 2 + r.Intn(5)
		cs := make([]Candidate, n)
		for i := range cs {
			cs[i] = Candidate{
				ID:          fmt.Sprintf("n%02d", i),
				LatencyMs:   float64(r.Intn(30)),
				CostPerHour: float64(r.Intn(12)) / 10.0,
				Spot:        r.Intn(2) == 0,
			}
		}
		for _, class := range classes {
			base, _ := Route(class, cs)
			for s := 0; s < 6; s++ {
				sh := shuffled(r, cs)
				d, _ := Route(class, sh)
				if d.Target.ID != base.Target.ID {
					t.Fatalf("ORDER-DEPENDENT SELECTION class=%s base=%q perm=%q\n fleet=%+v\n perm =%+v",
						class, base.Target.ID, d.Target.ID, cs, sh)
				}
				perms++
			}
		}
	}
	t.Logf("run=%s TestAdv_OrderIndependence: %d permutations all stable", advUUID, perms)
}

// TestAdv_IntransitivityReproducer pins the EXACT minimal fleet that exposed
// the intransitive pairwise band, with the deterministic documented winner.
//
// Fleet (Interactive): n00 lat=27 cost=0.8, n01 lat=26 cost=0.9, n02 lat=29 cost=0.3.
//
//	global-min latency = 26 (n01); band = 26*0.10 = 2.6 -> near-best window
//	[26, 28.6] contains n01(26) and n00(27); n02(29) is OUTSIDE the window.
//	Among the near-best set the cheaper wins: n00(0.8) < n01(0.9) -> WINNER n00.
//	n02 is cheapest overall (0.3) but its latency 29 is outside the near-best
//	window, so it must NOT be chosen — this is the "cheapest-but-violates"
//	bite for the latency-dominant class.
//
// ANTI-BLUFF — why this bites:
//   - Under the buggy pairwise band, pairwise relations form a cycle
//     (n00<n01, n02<n00, n01<n02), so the target flips with input order; one of
//     those orders yields n01 or n02, failing the want=n00 assertion. Under the
//     fix it is deterministically n00 for every permutation.
//   - Mutation: deleting the anchored near() filter (so cost alone decides among
//     all nodes) makes n02 win -> fails. Flipping the cost comparison among
//     near-best makes n01 win -> fails.
func TestAdv_IntransitivityReproducer(t *testing.T) {
	base := []Candidate{
		{ID: "n00", LatencyMs: 27, CostPerHour: 0.8, Spot: true},
		{ID: "n01", LatencyMs: 26, CostPerHour: 0.9, Spot: true},
		{ID: "n02", LatencyMs: 29, CostPerHour: 0.3, Spot: true},
	}
	t.Logf("run=%s reproducer fleet=%+v", advUUID, base)

	// Every one of the 3! input orders must yield n00.
	perms := [][]int{
		{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0},
	}
	for _, p := range perms {
		in := []Candidate{base[p[0]], base[p[1]], base[p[2]]}
		d := mustRoute(t, Interactive, in)
		if d.Target.ID != "n00" {
			t.Fatalf("Interactive intransitivity: order %v gave %q, want deterministic n00 (cheapest n02 lat=29 is outside near-best window and must not win)",
				p, d.Target.ID)
		}
	}

	// Oracle agreement, belt-and-suspenders.
	if w := oracleTarget(Interactive, base).ID; w != "n00" {
		t.Fatalf("oracle disagrees: want n00 got %q", w)
	}
	t.Logf("run=%s reproducer: all 6 input orders -> n00 (deterministic)", advUUID)
}

// TestAdv_CheapestViolatesConstraint_NotChosen is the hard-constraint bite,
// adapted to this package's "near-best window" semantics: a node that is best
// on the SECONDARY axis but falls OUTSIDE the primary near-best window must
// never be chosen; a pricier in-window node wins.
//
// ANTI-BLUFF — why this bites:
//   - Interactive: the cheapest node has latency far outside the latency
//     near-best window; if Route skipped the window filter it would wrongly
//     pick that cheapest node. We assert the in-window pricier node wins AND
//     that the chosen node is actually inside the latency window.
//   - Batch (dual): the lowest-latency node has cost far outside the cost
//     near-best window; the chosen node must be inside the cost window.
func TestAdv_CheapestViolatesConstraint_NotChosen(t *testing.T) {
	// Interactive: fast in-window cluster vs an ultra-cheap but slow node.
	inter := []Candidate{
		{ID: "fast_pricey", LatencyMs: 10.0, CostPerHour: 5.0},
		{ID: "fast_cheap", LatencyMs: 10.5, CostPerHour: 2.0},    // in window, cheaper -> winner
		{ID: "slow_cheapest", LatencyMs: 80.0, CostPerHour: 0.1}, // cheapest overall, OUT of window
	}
	d := mustRoute(t, Interactive, inter)
	if d.Target.ID != "fast_cheap" {
		t.Fatalf("Interactive: want in-window cheaper 'fast_cheap', got %q (slow_cheapest must be rejected for latency)", d.Target.ID)
	}
	// Independent assertion: chosen latency is inside the near-best window.
	minLat := 10.0
	band := minLat*nearTieFrac + nearTieFloor
	if d.Target.LatencyMs > minLat+band {
		t.Fatalf("Interactive chose %q with latency %v outside near-best window [.. %v]",
			d.Target.ID, d.Target.LatencyMs, minLat+band)
	}

	// Batch dual: cheap in-window cluster vs an ultra-fast but expensive node.
	batch := []Candidate{
		{ID: "cheap_slow", LatencyMs: 200.0, CostPerHour: 1.00},
		{ID: "cheap_fast", LatencyMs: 50.0, CostPerHour: 1.05},   // in cost window, faster -> winner
		{ID: "fastest_pricey", LatencyMs: 1.0, CostPerHour: 9.0}, // lowest latency, OUT of cost window
	}
	db := mustRoute(t, Batch, batch)
	if db.Target.ID != "cheap_fast" {
		t.Fatalf("Batch: want in-window faster 'cheap_fast', got %q (fastest_pricey must be rejected for cost)", db.Target.ID)
	}
	minCost := 1.00
	cband := minCost*nearTieFrac + nearTieFloor
	if db.Target.CostPerHour > minCost+cband {
		t.Fatalf("Batch chose %q with cost %v outside near-best window [.. %v]",
			db.Target.ID, db.Target.CostPerHour, minCost+cband)
	}
	t.Logf("run=%s constraint bite: Interactive->fast_cheap, Batch->cheap_fast (out-of-window optima rejected)", advUUID)
}

// TestAdv_EdgeCases covers single node, all-equal, empty, and invalid class.
//
// ANTI-BLUFF: a single-node fleet must return that node for every class (no
// panic, no index error); all-equal must fall to the ID tie-break; empty and
// invalid class must surface the documented errors, not a zero Decision that
// looks like a success.
func TestAdv_EdgeCases(t *testing.T) {
	classes := []QoSClass{RealTime, Interactive, Batch, BestEffort}

	// Single node -> that node, every class.
	one := []Candidate{{ID: "solo", LatencyMs: 5, CostPerHour: 1, Spot: false}}
	for _, c := range classes {
		d := mustRoute(t, c, one)
		if d.Target.ID != "solo" {
			t.Fatalf("%s single-node: want solo got %q", c, d.Target.ID)
		}
	}

	// All-equal on every metric -> ascending-ID tie-break, every class.
	eq := []Candidate{
		{ID: "z", LatencyMs: 7, CostPerHour: 2, Spot: true},
		{ID: "m", LatencyMs: 7, CostPerHour: 2, Spot: true},
		{ID: "a", LatencyMs: 7, CostPerHour: 2, Spot: true},
	}
	for _, c := range classes {
		// Across permutations too, to bind determinism.
		want := "a"
		for _, perm := range [][]int{{0, 1, 2}, {2, 1, 0}, {1, 0, 2}} {
			in := []Candidate{eq[perm[0]], eq[perm[1]], eq[perm[2]]}
			if got := mustRoute(t, c, in).Target.ID; got != want {
				t.Fatalf("%s all-equal: want %q got %q (perm %v)", c, want, got, perm)
			}
		}
	}

	// Empty fleet -> ErrNoCandidates.
	if _, err := Route(RealTime, nil); !errors.Is(err, ErrNoCandidates) {
		t.Fatalf("empty: want ErrNoCandidates got %v", err)
	}
	// Invalid class -> error (not a silent zero Decision).
	if _, err := Route(QoSClass(123), one); err == nil {
		t.Fatalf("invalid class: want error got nil")
	}
	t.Logf("run=%s edge cases: single/all-equal/empty/invalid all correct", advUUID)
}
