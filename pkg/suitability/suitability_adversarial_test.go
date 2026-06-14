package suitability

import (
	"errors"
	"fmt"
	"testing"
)

// runUUIDAdv is a distinct forensic anchor for the adversarial suite so its
// log lines are attributable independently of the original suite.
const runUUIDAdv = "c41d9e7a-2f06-4b8e-a153-suitability-adv-hxc1529"

// ----------------------------------------------------------------------------
// Independent oracle.
//
// These oracle functions re-derive the DOCUMENTED semantics by hand, with NO
// reference to the production helpers (Classify / eligible / Route). If the
// production code and the oracle disagree, that is a real defect — the oracle
// encodes the package doc:
//
//   - Classify: HPC iff (TightlyCoupled OR InterconnectSensitive); else Inference.
//     Stateless is IRRELEVANT to the kind (coupling/interconnect dominate).
//   - eligible(HPC):       Local AND InfiniBand (low-latency). Nothing else.
//   - eligible(Inference): always true.
//   - Route: FIRST eligible candidate in slice order; else ErrNoSuitablePlacement.
//
// The hard "never" invariant is encoded separately in oracleViolatesHardRule so
// it can bite independently of the argmax/first-match logic.
// ----------------------------------------------------------------------------

func oracleClassify(w Workload) WorkloadKind {
	if w.TightlyCoupled || w.InterconnectSensitive {
		return KindHPC
	}
	return KindInference
}

func oracleEligible(kind WorkloadKind, c Candidate) bool {
	if kind == KindHPC {
		return c.Locality == Local && c.Interconnect == InterconnectInfiniBand
	}
	// Inference accepts everything.
	return true
}

// oracleRoute independently re-derives the expected placement: index of the
// first eligible candidate, or -1 if none. Returning the index (not the
// Candidate) lets the test assert order-deterministic "first match" precisely.
func oracleRoute(kind WorkloadKind, candidates []Candidate) int {
	for i, c := range candidates {
		if oracleEligible(kind, c) {
			return i
		}
	}
	return -1
}

// oracleViolatesHardRule encodes the absolute "never" rules from the package
// doc, independent of the routing logic: an HPC workload must NEVER be placed
// on a Remote candidate, and NEVER on a non-low-latency (Ethernet) fabric.
// Any production placement that trips this is a CLAUDE-1-class correctness
// defect (a hard-rule violation), not merely a ranking nit.
func oracleViolatesHardRule(kind WorkloadKind, placed Candidate) bool {
	if kind != KindHPC {
		return false
	}
	if placed.Locality == Remote {
		return true
	}
	if placed.Interconnect != InterconnectInfiniBand {
		return true
	}
	return false
}

// allCandidates is the full cartesian product of the candidate state space:
// {Local,Remote} x {Ethernet,InfiniBand} = 4 distinct candidate shapes, each
// given a deterministic NodeID so failures name the exact node.
func allCandidates() []Candidate {
	var out []Candidate
	for _, loc := range []Locality{Local, Remote} {
		for _, ic := range []Interconnect{InterconnectEthernet, InterconnectInfiniBand} {
			out = append(out, Candidate{
				NodeID:       fmt.Sprintf("%s-%s", loc, ic),
				Locality:     loc,
				Interconnect: ic,
			})
		}
	}
	return out
}

// allWorkloads enumerates every combination of the three boolean signals
// (TightlyCoupled, InterconnectSensitive, Stateless) = 8 workloads. This
// exercises every classifier branch, including the adversarial Stateless+coupled
// corners where a Stateless-keyed bug would misclassify.
func allWorkloads() []Workload {
	var out []Workload
	for _, tc := range []bool{false, true} {
		for _, is := range []bool{false, true} {
			for _, st := range []bool{false, true} {
				out = append(out, Workload{
					Name:                  fmt.Sprintf("w-tc%v-is%v-st%v", tc, is, st),
					TightlyCoupled:        tc,
					InterconnectSensitive: is,
					Stateless:             st,
				})
			}
		}
	}
	return out
}

// ----------------------------------------------------------------------------
// 1. Classifier correctness across the FULL 8-workload space vs oracle.
//
// Anti-bluff: a Classify mutated to key on Stateless (or to drop either the
// TightlyCoupled or InterconnectSensitive arm) disagrees with the oracle on at
// least one of these 8 rows and fails.
// ----------------------------------------------------------------------------
func TestAdversarialClassifyExhaustiveVsOracle(t *testing.T) {
	for _, w := range allWorkloads() {
		got := Classify(w)
		want := oracleClassify(w)
		t.Logf("run=%s classify workload=%q tc=%v is=%v st=%v => got=%s want=%s",
			runUUIDAdv, w.Name, w.TightlyCoupled, w.InterconnectSensitive, w.Stateless, got, want)
		if got != want {
			t.Fatalf("run=%s classify mismatch for %q (tc=%v is=%v st=%v): got=%s want=%s",
				runUUIDAdv, w.Name, w.TightlyCoupled, w.InterconnectSensitive, w.Stateless, got, want)
		}
	}
}

// permutations returns every ordering of the input candidate slice (n<=4 here,
// so at most 24 orderings) via Heap's algorithm. Order matters because Route is
// documented to return the FIRST eligible candidate; permuting the input is how
// we prove the "first eligible in slice order" + order-independence-of-the-rule
// guarantees simultaneously.
func permutations(in []Candidate) [][]Candidate {
	var res [][]Candidate
	n := len(in)
	a := make([]Candidate, n)
	copy(a, in)
	c := make([]int, n)
	snapshot := func() {
		cp := make([]Candidate, n)
		copy(cp, a)
		res = append(res, cp)
	}
	snapshot()
	i := 0
	for i < n {
		if c[i] < i {
			if i%2 == 0 {
				a[0], a[i] = a[i], a[0]
			} else {
				a[c[i]], a[i] = a[i], a[c[i]]
			}
			snapshot()
			c[i]++
			i = 0
		} else {
			c[i] = 0
			i++
		}
	}
	return res
}

// subsetsNonEmpty returns every non-empty subset of the 4 canonical candidate
// shapes (2^4 - 1 = 15 subsets). Combined with permutations, this sweeps every
// reachable candidate-list shape and ordering.
func subsetsNonEmpty(all []Candidate) [][]Candidate {
	var res [][]Candidate
	n := len(all)
	for mask := 1; mask < (1 << n); mask++ {
		var sub []Candidate
		for j := 0; j < n; j++ {
			if mask&(1<<j) != 0 {
				sub = append(sub, all[j])
			}
		}
		res = append(res, sub)
	}
	return res
}

// ----------------------------------------------------------------------------
// 2 + 3 + 4 combined: exhaustive Route correctness over
// (8 workloads) x (15 non-empty candidate subsets) x (all orderings).
//
// For EACH configuration we assert four things against the independent oracle:
//
//	(a) Decision correctness: the placed candidate == the oracle's first-eligible
//	    candidate (argmax of "eligible, earliest index"); on no-match, a typed
//	    ErrNoSuitablePlacement and a zero Candidate (edge case: reject, no panic).
//	(b) Hard-rule invariant: the placement NEVER violates the HPC "never remote /
//	    never non-low-latency" rule — checked directly, independent of (a), so a
//	    wrong-but-still-forbidden placement is caught even if (a)'s oracle were
//	    somehow also wrong.
//	(c) Order-independence of the RULE: across every permutation of the same
//	    candidate set, the *chosen shape* (Locality+Interconnect) is identical —
//	    i.e. ties are resolved deterministically and the rule does not depend on
//	    input ordering for WHICH KIND of node it picks.
//	(d) No panic on any input (covered implicitly by running to completion).
//
// Anti-bluff teeth:
//   - Removing the HPC arm of eligible (so HPC accepts a remote/Ethernet node):
//     for an HPC workload whose subset has a remote-IB or local-Eth node listed
//     before (or instead of) a local-IB node, the placement becomes forbidden ->
//     (b) fires AND (a) disagrees with the oracle.
//   - Flipping Route to return the LAST eligible (or skipping the first match):
//     for subsets with >=2 eligible candidates the placed index != oracle index
//     -> (a) fires.
//   - Making Route fall back to remote when no local-IB exists (silent fallback):
//     (b) fires on the HPC-no-local-IB subsets.
//
// ----------------------------------------------------------------------------
func TestAdversarialRouteExhaustiveVsOracle(t *testing.T) {
	base := allCandidates()
	workloads := allWorkloads()
	subsets := subsetsNonEmpty(base)

	var (
		configs       int
		hpcRejections int
		hpcPlacements int
	)

	for _, w := range workloads {
		kind := Classify(w)
		for _, sub := range subsets {
			// (c) reference shape from the canonical (unpermuted) ordering.
			refIdx := oracleRoute(kind, sub)
			var refShape string
			if refIdx >= 0 {
				refShape = fmt.Sprintf("%s/%s", sub[refIdx].Locality, sub[refIdx].Interconnect)
			} else {
				refShape = "<none>"
			}

			for _, perm := range permutations(sub) {
				configs++
				wantIdx := oracleRoute(kind, perm)
				got, err := Route(kind, perm)

				if wantIdx < 0 {
					// (a) no-match edge case: typed error + zero Candidate, no panic.
					if err == nil {
						t.Fatalf("run=%s want ErrNoSuitablePlacement workload=%q kind=%s perm=%v but got placement=%+v",
							runUUIDAdv, w.Name, kind, shapesOf(perm), got)
					}
					if !errors.Is(err, ErrNoSuitablePlacement) {
						t.Fatalf("run=%s want ErrNoSuitablePlacement type, got err=%v (workload=%q kind=%s perm=%v)",
							runUUIDAdv, err, w.Name, kind, shapesOf(perm))
					}
					if got != (Candidate{}) {
						t.Fatalf("run=%s want zero Candidate on no-match, got=%+v (workload=%q kind=%s perm=%v)",
							runUUIDAdv, got, w.Name, kind, shapesOf(perm))
					}
					if kind == KindHPC {
						hpcRejections++
					}
					continue
				}

				// (a) decision correctness: exact first-eligible match.
				if err != nil {
					t.Fatalf("run=%s unexpected error workload=%q kind=%s perm=%v: %v (oracle wanted idx=%d shape=%s/%s)",
						runUUIDAdv, w.Name, kind, shapesOf(perm), err,
						wantIdx, perm[wantIdx].Locality, perm[wantIdx].Interconnect)
				}
				want := perm[wantIdx]
				if got != want {
					t.Fatalf("run=%s placement mismatch workload=%q kind=%s perm=%v: got=%+v want=%+v (first-eligible idx=%d)",
						runUUIDAdv, w.Name, kind, shapesOf(perm), got, want, wantIdx)
				}

				// (b) hard-rule invariant: independent of the oracle's argmax.
				if oracleViolatesHardRule(kind, got) {
					t.Fatalf("run=%s HARD-RULE VIOLATION: %s workload placed on %s/%s (node=%q) — forbidden (never remote, never non-low-latency). workload=%q perm=%v",
						runUUIDAdv, kind, got.Locality, got.Interconnect, got.NodeID, w.Name, shapesOf(perm))
				}

				// (c) order-independence of the chosen SHAPE — HPC ONLY.
				//
				// For HPC exactly one shape (Local/InfiniBand) is ever eligible,
				// so the chosen shape MUST be identical across every ordering of
				// the same set; a varying shape would mean the hard rule depended
				// on input order (a real defect). For Inference EVERY candidate is
				// eligible, so the documented "first eligible in slice order"
				// legitimately yields different shapes under different orderings —
				// that exact placement is already pinned by assertion (a) above, so
				// applying a shape-stability claim to Inference would be a FALSE
				// invariant, not a real one. Scope (c) to HPC where it is real.
				if kind == KindHPC {
					gotShape := fmt.Sprintf("%s/%s", got.Locality, got.Interconnect)
					if gotShape != refShape {
						t.Fatalf("run=%s order-dependent HPC rule: same candidate set placed shape=%s under one ordering but shape=%s under another (workload=%q perm=%v)",
							runUUIDAdv, gotShape, refShape, w.Name, shapesOf(perm))
					}
				}

				if kind == KindHPC {
					hpcPlacements++
				}
			}
		}
	}

	t.Logf("run=%s exhaustive route: configs=%d hpcPlacements=%d hpcRejections=%d",
		runUUIDAdv, configs, hpcPlacements, hpcRejections)

	// Coverage guards: prove the sweep actually exercised BOTH outcomes for HPC
	// (a real placement AND a typed rejection). A zero here would mean the loop
	// never reached the interesting branches — i.e. the anti-bluff teeth never bit.
	if hpcPlacements == 0 {
		t.Fatalf("run=%s coverage: no HPC placement ever exercised — sweep is vacuous", runUUIDAdv)
	}
	if hpcRejections == 0 {
		t.Fatalf("run=%s coverage: no HPC rejection ever exercised — 'never remote fallback' path untested", runUUIDAdv)
	}
}

// shapesOf renders a candidate slice compactly for failure messages.
func shapesOf(cs []Candidate) string {
	out := "["
	for i, c := range cs {
		if i > 0 {
			out += " "
		}
		out += fmt.Sprintf("%s/%s", c.Locality, c.Interconnect)
	}
	return out + "]"
}

// ----------------------------------------------------------------------------
// Focused hard-rule bite: for EVERY HPC workload, over every candidate subset
// that contains at least one tempting-but-forbidden node (remote-IB and/or
// local-Eth) AND NO local-IB node, Route MUST refuse — never place on the
// forbidden node. This is the sharpest anti-bluff: a soft "best available"
// router would happily take the remote-IB proxy (it has the fast fabric!) or
// the local-Eth node (it's local!). The hard rule must win over that soft
// preference.
//
// Mutation that bites: drop the Local/low-latency check in eligible -> these
// subsets start returning a placement instead of ErrNoSuitablePlacement -> fail.
// ----------------------------------------------------------------------------
func TestAdversarialHPCNeverForbiddenWhenSoftWouldFavorIt(t *testing.T) {
	remoteIB := Candidate{NodeID: "remote-ib", Locality: Remote, Interconnect: InterconnectInfiniBand}
	localEth := Candidate{NodeID: "local-eth", Locality: Local, Interconnect: InterconnectEthernet}
	remoteEth := Candidate{NodeID: "remote-eth", Locality: Remote, Interconnect: InterconnectEthernet}

	// Every non-empty combination of forbidden-for-HPC nodes (no local-IB).
	forbiddenSets := [][]Candidate{
		{remoteIB},
		{localEth},
		{remoteEth},
		{remoteIB, localEth},
		{remoteIB, remoteEth},
		{localEth, remoteEth},
		{remoteIB, localEth, remoteEth},
	}

	hpcWorkloads := []Workload{
		{Name: "mpi-coupled", TightlyCoupled: true},
		{Name: "mpi-icsens", InterconnectSensitive: true},
		{Name: "mpi-both", TightlyCoupled: true, InterconnectSensitive: true},
		{Name: "mpi-stateless-coupled", Stateless: true, TightlyCoupled: true},
	}

	checked := 0
	for _, w := range hpcWorkloads {
		kind := Classify(w)
		if kind != KindHPC {
			t.Fatalf("run=%s precondition: %q must classify HPC, got %s", runUUIDAdv, w.Name, kind)
		}
		for _, set := range forbiddenSets {
			for _, perm := range permutations(set) {
				checked++
				got, err := Route(kind, perm)
				t.Logf("run=%s HPC=%q forbiddenSet=%v => err=%v placed=%q",
					runUUIDAdv, w.Name, shapesOf(perm), err, got.NodeID)
				if err == nil {
					t.Fatalf("run=%s HARD-RULE VIOLATION: HPC %q placed on forbidden node %s/%s (node=%q) — soft preference must NOT override 'never remote / never non-low-latency'. set=%v",
						runUUIDAdv, w.Name, got.Locality, got.Interconnect, got.NodeID, shapesOf(perm))
				}
				if !errors.Is(err, ErrNoSuitablePlacement) {
					t.Fatalf("run=%s HPC %q forbidden set: want ErrNoSuitablePlacement, got %v (set=%v)",
						runUUIDAdv, w.Name, err, shapesOf(perm))
				}
				if got != (Candidate{}) {
					t.Fatalf("run=%s HPC %q forbidden set: want zero Candidate, got %+v (set=%v)",
						runUUIDAdv, w.Name, got, shapesOf(perm))
				}
			}
		}
	}
	if checked == 0 {
		t.Fatalf("run=%s coverage: forbidden-set sweep was vacuous", runUUIDAdv)
	}
	t.Logf("run=%s forbidden-set sweep: %d (workload,ordering) configs all correctly refused", runUUIDAdv, checked)
}

// ----------------------------------------------------------------------------
// Hard rule wins even when a local-IB node EXISTS but is buried behind tempting
// forbidden nodes: prove HPC picks the local-IB regardless of position, and a
// remote-IB / local-Eth listed earlier never steals the placement. This is the
// ranking-correctness bite: a soft scorer that ranked "remote InfiniBand" above
// "local InfiniBand" (fast fabric, ignoring locality) would mis-place here.
//
// Mutation that bites: flip eligible's locality test, or make Route prefer the
// earliest candidate without the Local+IB gate -> the leading remote-IB or
// local-Eth node is chosen and the local-IB assertion fails.
// ----------------------------------------------------------------------------
func TestAdversarialHPCPicksLocalIBRegardlessOfPosition(t *testing.T) {
	localIB := Candidate{NodeID: "local-ib", Locality: Local, Interconnect: InterconnectInfiniBand}
	remoteIB := Candidate{NodeID: "remote-ib", Locality: Remote, Interconnect: InterconnectInfiniBand}
	localEth := Candidate{NodeID: "local-eth", Locality: Local, Interconnect: InterconnectEthernet}
	remoteEth := Candidate{NodeID: "remote-eth", Locality: Remote, Interconnect: InterconnectEthernet}

	w := Workload{Name: "mpi-cfd", TightlyCoupled: true, InterconnectSensitive: true}
	kind := Classify(w)
	if kind != KindHPC {
		t.Fatalf("run=%s precondition: want HPC, got %s", runUUIDAdv, kind)
	}

	// Exactly one local-IB plus the three forbidden shapes; permute all 24
	// orderings. The placement must be the local-IB node every single time.
	set := []Candidate{localIB, remoteIB, localEth, remoteEth}
	perms := permutations(set)
	for _, perm := range perms {
		got, err := Route(kind, perm)
		if err != nil {
			t.Fatalf("run=%s unexpected error with a local-IB present: %v (perm=%v)", runUUIDAdv, err, shapesOf(perm))
		}
		if got.NodeID != "local-ib" {
			t.Fatalf("run=%s HPC placed on %q (%s/%s) but a local-IB node was present — hard rule + locality must win over remote-IB/local-Eth. perm=%v",
				runUUIDAdv, got.NodeID, got.Locality, got.Interconnect, shapesOf(perm))
		}
		if oracleViolatesHardRule(kind, got) {
			t.Fatalf("run=%s HARD-RULE VIOLATION on %+v", runUUIDAdv, got)
		}
	}
	t.Logf("run=%s local-IB chosen across all %d orderings regardless of forbidden nodes' positions", runUUIDAdv, len(perms))
}

// ----------------------------------------------------------------------------
// ClassifyAndRoute parity: the convenience wrapper must agree with the
// composition Classify+Route on the full workload x subset x ordering sweep,
// including the kind it reports. Guards against the wrapper drifting from its
// parts (e.g. classifying with a different rule).
// ----------------------------------------------------------------------------
func TestAdversarialClassifyAndRouteParity(t *testing.T) {
	base := allCandidates()
	for _, w := range allWorkloads() {
		for _, sub := range subsetsNonEmpty(base) {
			for _, perm := range permutations(sub) {
				wantKind := Classify(w)
				wantC, wantErr := Route(wantKind, perm)

				gotKind, gotC, gotErr := ClassifyAndRoute(w, perm)

				if gotKind != wantKind {
					t.Fatalf("run=%s ClassifyAndRoute kind=%s != Classify kind=%s (workload=%q)",
						runUUIDAdv, gotKind, wantKind, w.Name)
				}
				if (gotErr == nil) != (wantErr == nil) {
					t.Fatalf("run=%s ClassifyAndRoute err disagreement: got=%v want=%v (workload=%q perm=%v)",
						runUUIDAdv, gotErr, wantErr, w.Name, shapesOf(perm))
				}
				if gotErr == nil && gotC != wantC {
					t.Fatalf("run=%s ClassifyAndRoute placement=%+v != Route placement=%+v (workload=%q perm=%v)",
						runUUIDAdv, gotC, wantC, w.Name, shapesOf(perm))
				}
				// Independent hard-rule check on the wrapper's own output.
				if gotErr == nil && oracleViolatesHardRule(gotKind, gotC) {
					t.Fatalf("run=%s HARD-RULE VIOLATION via ClassifyAndRoute: %s on %s/%s (workload=%q)",
						runUUIDAdv, gotKind, gotC.Locality, gotC.Interconnect, w.Name)
				}
			}
		}
	}
}
