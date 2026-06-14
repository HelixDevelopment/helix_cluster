package latencysched

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"
)

// runUUIDAdv tags this adversarial suite's forensic log lines.
const runUUIDAdv = "latencysched-adv-3c8d51a2-6e07-4f19-b3aa-c0ffeebada55"

// -----------------------------------------------------------------------------
// Independent oracle.
//
// oracleSelect re-implements the DOCUMENTED routing policy from first
// principles, deliberately NOT sharing code with the production sort:
//
//  1. Discard unavailable providers.
//  2. If ANY available LOCAL exists, the winner is the available local with the
//     minimum LatencyMs; exact-latency ties broken by ascending ID.
//  3. Otherwise the winner is the available REMOTE with the minimum LatencyMs;
//     exact-latency ties broken by ascending ID.
//  4. If nothing is available, there is no winner.
//
// It computes the winner by an O(n) argmin scan (no sort), so an intransitive
// or order-dependent PRODUCTION comparator cannot "agree" with the oracle by
// construction — the oracle is itself order-independent (argmin over a set is
// commutative), giving us a fixed ground truth to compare every permutation
// against. Returns (id, latency, locality, ok).
func oracleSelect(ps []Provider) (string, int, Locality, bool) {
	pick := func(loc Locality) (Provider, bool) {
		var best Provider
		found := false
		for _, p := range ps {
			if !p.Available || p.Locality != loc {
				continue
			}
			if !found {
				best, found = p, true
				continue
			}
			if p.LatencyMs < best.LatencyMs ||
				(p.LatencyMs == best.LatencyMs && p.ID < best.ID) {
				best = p
			}
		}
		return best, found
	}
	if b, ok := pick(Local); ok {
		return b.ID, b.LatencyMs, b.Locality, true
	}
	if b, ok := pick(Remote); ok {
		return b.ID, b.LatencyMs, b.Locality, true
	}
	return "", 0, 0, false
}

// permutations yields every ordering of ps (n! of them). n stays small in the
// caller (<= 6) so n! is bounded.
func permutations(ps []Provider) [][]Provider {
	var out [][]Provider
	var rec func(prefix, rest []Provider)
	rec = func(prefix, rest []Provider) {
		if len(rest) == 0 {
			cp := make([]Provider, len(prefix))
			copy(cp, prefix)
			out = append(out, cp)
			return
		}
		for i := range rest {
			nextRest := make([]Provider, 0, len(rest)-1)
			nextRest = append(nextRest, rest[:i]...)
			nextRest = append(nextRest, rest[i+1:]...)
			rec(append(prefix, rest[i]), nextRest)
		}
	}
	rec(nil, ps)
	return out
}

// -----------------------------------------------------------------------------
// TEST 1 — Order-independence across ALL permutations, including near-tie
// chains designed to expose an intransitive comparator.
//
// WHY THIS BITES: an intransitive `less` (e.g. a pairwise "within X% of each
// other -> compare on secondary key" band) fed to sort.SliceStable produces a
// winner that depends on the INPUT ORDER. We feed each fleet through EVERY
// permutation and assert the chosen provider is identical every time. The
// fleets include near-tie chains (a@27,b@26,c@29 within ~12% of each other but
// NOT all within 12% of one endpoint) that are the canonical intransitivity
// trigger: if someone replaced the (latency,ID) total order with a pairwise
// band, some permutation would elect a different winner and this test FAILS.
// Against the current transitive (latency,ID) comparator it passes.
func TestSelect_OrderIndependenceAllPermutations(t *testing.T) {
	fleets := map[string][]Provider{
		// Classic intransitivity bait among REMOTES: 26,27,29 are pairwise
		// "close" but a pairwise band would tie a~b, b~c yet a !~ c.
		"near-tie-chain-remotes": {
			{ID: "b26", Locality: Remote, LatencyMs: 26, Available: true},
			{ID: "a27", Locality: Remote, LatencyMs: 27, Available: true},
			{ID: "c29", Locality: Remote, LatencyMs: 29, Available: true},
		},
		// Same chain but with a cheaper/secondary axis that a band comparator
		// would use to break "near ties" — ordering must still be irrelevant.
		"near-tie-chain-with-id-bait": {
			{ID: "zzz", Locality: Remote, LatencyMs: 27, Available: true},
			{ID: "aaa", Locality: Remote, LatencyMs: 29, Available: true},
			{ID: "mmm", Locality: Remote, LatencyMs: 26, Available: true},
		},
		// Locals present: local-preference must hold regardless of order, and
		// the local near-tie chain must resolve identically every permutation.
		"local-chain-beats-fast-remote": {
			{ID: "rfast", Locality: Remote, LatencyMs: 1, Available: true},
			{ID: "lb", Locality: Local, LatencyMs: 26, Available: true},
			{ID: "la", Locality: Local, LatencyMs: 27, Available: true},
			{ID: "lc", Locality: Local, LatencyMs: 29, Available: true},
		},
		// Mixed availability + a 5-wide near-tie ladder (exposes any band that
		// chains across more than two hops).
		"five-wide-ladder": {
			{ID: "p24", Locality: Remote, LatencyMs: 24, Available: true},
			{ID: "p25", Locality: Remote, LatencyMs: 25, Available: true},
			{ID: "p26", Locality: Remote, LatencyMs: 26, Available: true},
			{ID: "down", Locality: Remote, LatencyMs: 1, Available: false},
			{ID: "p27", Locality: Remote, LatencyMs: 27, Available: true},
		},
		// Exact-latency ties (the tie-break axis) under permutation.
		"exact-ties": {
			{ID: "z", Locality: Remote, LatencyMs: 30, Available: true},
			{ID: "a", Locality: Remote, LatencyMs: 30, Available: true},
			{ID: "m", Locality: Remote, LatencyMs: 30, Available: true},
		},
	}

	for name, fleet := range fleets {
		fleet := fleet
		t.Run(name, func(t *testing.T) {
			wantID, _, _, ok := oracleSelect(fleet)
			if !ok {
				t.Fatalf("[%s] fleet %q has no available provider per oracle", runUUIDAdv, name)
			}
			perms := permutations(fleet)
			t.Logf("[%s] fleet=%q size=%d permutations=%d oracleWinner=%s",
				runUUIDAdv, name, len(fleet), len(perms), wantID)

			seen := map[string]int{}
			for idx, perm := range perms {
				s := New()
				d, err := s.Select(perm)
				if err != nil {
					t.Fatalf("[%s] %q perm#%d unexpected err: %v", runUUIDAdv, name, idx, err)
				}
				seen[d.ChosenID]++
				if d.ChosenID != wantID {
					t.Fatalf("[%s] ORDER-DEPENDENT WINNER in %q: perm#%d %v chose %q, oracle/other perms chose %q",
						runUUIDAdv, name, idx, idsOf(perm), d.ChosenID, wantID)
				}
			}
			if len(seen) != 1 {
				t.Fatalf("[%s] %q produced %d distinct winners across permutations: %v (intransitive comparator)",
					runUUIDAdv, name, len(seen), seen)
			}
		})
	}
}

// idsOf renders the ID order of a fleet for forensic failure messages.
func idsOf(ps []Provider) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.ID
	}
	return out
}

// -----------------------------------------------------------------------------
// TEST 2 — Objective correctness against the independent oracle over many
// randomized fleets and many permutations of each.
//
// WHY THIS BITES: this catches a WRONG RANK. If the production comparator
// flipped min<->max, or dropped the local-preference branch, or broke the ID
// tie-break, the chosen provider would diverge from the argmin oracle on some
// config. Randomized fleets (varied sizes, localities, availability, tight
// latency clusters) plus per-fleet permutation give broad coverage; every
// (fleet, permutation) pair must equal the oracle's fixed answer.
func TestSelect_ObjectiveVsOracleRandomized(t *testing.T) {
	rng := rand.New(rand.NewSource(0x0FFEE0DDF00D))
	const fleetCount = 400

	mismatches := 0
	emptyChecked := 0
	for f := 0; f < fleetCount; f++ {
		n := 1 + rng.Intn(6) // 1..6 providers
		fleet := make([]Provider, n)
		for i := 0; i < n; i++ {
			loc := Local
			if rng.Intn(2) == 0 {
				loc = Remote
			}
			fleet[i] = Provider{
				ID:        fmt.Sprintf("p%02d-%d", i, rng.Intn(1000)),
				Locality:  loc,
				LatencyMs: rng.Intn(40), // tight range -> frequent near-ties
				Available: rng.Intn(4) != 0,
			}
		}
		// De-dup IDs so the (latency,ID) total order is well-defined and the
		// oracle's ID tie-break is unambiguous.
		fleet = dedupIDs(fleet)

		wantID, wantLat, wantLoc, wantOK := oracleSelect(fleet)

		// A handful of permutations per fleet (cap factorial blowup at n<=6).
		perms := permutations(fleet)
		if len(perms) > 24 {
			perms = perms[:24]
		}
		for _, perm := range perms {
			s := New()
			d, err := s.Select(perm)
			if !wantOK {
				emptyChecked++
				if err == nil {
					t.Fatalf("[%s] fleet#%d: oracle says no winner but Select returned %+v", runUUIDAdv, f, d)
				}
				continue
			}
			if err != nil {
				t.Fatalf("[%s] fleet#%d: unexpected err %v for %v", runUUIDAdv, f, err, idsOf(perm))
			}
			if d.ChosenID != wantID || d.LatencyMs != wantLat || d.Locality != wantLoc {
				mismatches++
				t.Fatalf("[%s] WRONG RANK fleet#%d perm %v: got {%s,%dms,%s} want {%s,%dms,%s}",
					runUUIDAdv, f, idsOf(perm),
					d.ChosenID, d.LatencyMs, d.Locality, wantID, wantLat, wantLoc)
			}
		}
	}
	t.Logf("[%s] randomized objective check: %d fleets, 0 mismatches, %d empty-fleet checks",
		runUUIDAdv, fleetCount, emptyChecked)
}

func dedupIDs(ps []Provider) []Provider {
	seen := map[string]bool{}
	out := ps[:0]
	i := 0
	for _, p := range ps {
		if seen[p.ID] {
			p.ID = fmt.Sprintf("%s-dup%d", p.ID, i)
			i++
		}
		seen[p.ID] = true
		out = append(out, p)
	}
	return out
}

// -----------------------------------------------------------------------------
// TEST 3 — Edge cases: single provider, all-equal, empty, all-unavailable.
//
// WHY THIS BITES: documented behavior must hold with no panic. Empty and
// all-unavailable must return ErrNoProvider; a single available provider must
// be chosen; an all-equal fleet must resolve deterministically to the min-ID.
func TestSelect_EdgeCases(t *testing.T) {
	s := New()

	if _, err := s.Select(nil); err != ErrNoProvider {
		t.Fatalf("[%s] empty: want ErrNoProvider, got %v", runUUIDAdv, err)
	}
	if _, err := s.Select([]Provider{}); err != ErrNoProvider {
		t.Fatalf("[%s] empty slice: want ErrNoProvider, got %v", runUUIDAdv, err)
	}
	if _, err := s.Select([]Provider{
		{ID: "x", Locality: Remote, LatencyMs: 5, Available: false},
		{ID: "y", Locality: Local, LatencyMs: 5, Available: false},
	}); err != ErrNoProvider {
		t.Fatalf("[%s] all-unavailable: want ErrNoProvider, got %v", runUUIDAdv, err)
	}

	d, err := s.Select([]Provider{{ID: "solo", Locality: Remote, LatencyMs: 7, Available: true}})
	if err != nil || d.ChosenID != "solo" || d.LatencyMs != 7 {
		t.Fatalf("[%s] single provider: got %+v err=%v", runUUIDAdv, d, err)
	}

	// All-equal latency remotes -> min ID, order independent.
	allEqual := []Provider{
		{ID: "c", Locality: Remote, LatencyMs: 10, Available: true},
		{ID: "a", Locality: Remote, LatencyMs: 10, Available: true},
		{ID: "b", Locality: Remote, LatencyMs: 10, Available: true},
	}
	wantID, _, _, _ := oracleSelect(allEqual)
	for _, perm := range permutations(allEqual) {
		s2 := New()
		dd, err := s2.Select(perm)
		if err != nil {
			t.Fatalf("[%s] all-equal perm err: %v", runUUIDAdv, err)
		}
		if dd.ChosenID != wantID || dd.ChosenID != "a" {
			t.Fatalf("[%s] all-equal: perm %v chose %q want %q", runUUIDAdv, idsOf(perm), dd.ChosenID, wantID)
		}
	}
}

// -----------------------------------------------------------------------------
// TEST 4 — Self-validation of the suite's bite (anti-bluff meta-test).
//
// This proves the order-independence assertion in TEST 1 is LOAD-BEARING: it
// stands up an INTRANSITIVE pairwise-band comparator (the exact bug class qos
// had) over the same near-tie chain, runs it through every permutation via the
// production-shaped sort.SliceStable path, and asserts that THIS comparator DOES
// produce order-dependent winners. If a hypothetical regression replaced
// minByLatencyThenID's total order with such a pairwise band, TEST 1 would fail
// exactly the way this meta-test demonstrates here. The production Select is NOT
// modified — this only exercises a local bad comparator to show the trap is real.
func TestSelect_IntransitivePairwiseBandIsOrderDependent(t *testing.T) {
	// near-tie chain: 26,27,29 — pairwise within ~12% of each other, but 26 and
	// 29 are > the band measured from either endpoint in the asymmetric sense a
	// naive band uses.
	chain := []Provider{
		{ID: "b26", Locality: Remote, LatencyMs: 26, Available: true},
		{ID: "a27", Locality: Remote, LatencyMs: 27, Available: true},
		{ID: "c29", Locality: Remote, LatencyMs: 29, Available: true},
	}

	// INTRANSITIVE pairwise-band less: if |a-b| within 10% of the smaller, treat
	// as a near-tie and prefer the LARGER latency (a deliberately perverse but
	// self-consistent secondary key to surface the cycle); else smaller wins.
	// This is the SAME shape as a qos pairwise within() bug.
	pairwiseBandLess := func(a, b Provider) bool {
		lo := a.LatencyMs
		if b.LatencyMs < lo {
			lo = b.LatencyMs
		}
		band := lo / 10 // ~10%
		if abs(a.LatencyMs-b.LatencyMs) <= band {
			// near-tie -> prefer larger latency (perverse secondary)
			return a.LatencyMs > b.LatencyMs
		}
		return a.LatencyMs < b.LatencyMs
	}

	winners := map[string]int{}
	for _, perm := range permutations(chain) {
		cp := make([]Provider, len(perm))
		copy(cp, perm)
		sort.SliceStable(cp, func(i, j int) bool { return pairwiseBandLess(cp[i], cp[j]) })
		winners[cp[0].ID]++
	}
	t.Logf("[%s] intransitive pairwise-band winners across permutations: %v", runUUIDAdv, winners)
	if len(winners) < 2 {
		t.Fatalf("[%s] meta-test FAILED to demonstrate intransitivity: only winner(s) %v; "+
			"the order-independence assertion in TEST 1 would not bite", runUUIDAdv, winners)
	}

	// And prove the PRODUCTION comparator does NOT have this defect on the very
	// same chain: one stable winner across all permutations.
	prodWinners := map[string]int{}
	for _, perm := range permutations(chain) {
		s := New()
		d, _ := s.Select(perm)
		prodWinners[d.ChosenID]++
	}
	if len(prodWinners) != 1 {
		t.Fatalf("[%s] PRODUCTION comparator is order-dependent on near-tie chain: %v (REAL BUG)",
			runUUIDAdv, prodWinners)
	}
	t.Logf("[%s] production (latency,ID) comparator: single stable winner %v on the same chain",
		runUUIDAdv, prodWinners)
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
