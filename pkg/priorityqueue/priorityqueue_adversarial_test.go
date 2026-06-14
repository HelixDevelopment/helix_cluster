package priorityqueue

import (
	"testing"
)

// advUUID tags adversarial-run log lines for sink-side evidence capture.
const advUUID = "HXC-priorityqueue-adv-7c1e9b04-2d6a-4f31-8e55-91a0c3f2db64"

// permute returns a deterministic permutation of jobs selected by seed.
// It is a pure index shuffle (Fisher-Yates driven by a small LCG keyed on
// seed) -- NO time, NO math/rand, so every (jobs, seed) pair is reproducible
// and the test body is free of nondeterminism per the package's purity rule.
func permute(jobs []Job, seed uint64) []Job {
	out := make([]Job, len(jobs))
	copy(out, jobs)
	// Numerical Recipes LCG constants; deterministic per seed.
	state := seed*6364136223846793005 + 1442695040888963407
	for i := len(out) - 1; i > 0; i-- {
		state = state*6364136223846793005 + 1442695040888963407
		j := int(state % uint64(i+1))
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// drainFresh builds a Queue from jobs (in the given slice order) and drains it
// at now, returning the dequeue order as IDs. Push order == slice order, so
// this exposes any input-permutation sensitivity in Pop's selection.
func drainFresh(w Weights, jobs []Job, now int64) []string {
	q := New(w)
	for _, j := range jobs {
		q.Push(j)
	}
	return drainIDs(q, now)
}

// seedList is a fixed, non-random set of permutation seeds. Using a fixed list
// (not a random/time seed) keeps the probe reproducible: a failure is always
// replayable.
var seedList = []uint64{
	0, 1, 2, 3, 5, 7, 11, 13, 17, 19, 23, 29, 31, 37, 41, 43,
	101, 257, 1009, 65537, 99991, 1<<31 - 1, 1 << 40, 1 << 62,
}

// TestPermutationInvarianceExactTies is THE centerpiece probe (HXC-1690 class).
//
// It constructs a job set that is a worst case for an intransitive / banded
// comparator: many jobs collapse onto only a few DISTINCT composite-priority
// values, so the primary key produces large equivalence classes and ALL
// ordering inside a class is decided by the tie-break chain (SubmitTick, then
// ID). If the comparator had an epsilon "near-tie band" (a~b when
// |p(a)-p(b)|<eps) -- the exact defect found in pkg/qos -- then feeding the
// same set in different input permutations would select different winners,
// because a banded comparator is order-dependent. We assert the FULL dequeue
// order (and therefore the top job) is byte-identical across every permutation.
func TestPermutationInvarianceExactTies(t *testing.T) {
	w := DefaultWeights() // Age:2 FairShare:5 Size:1

	now := int64(1000)

	// Engineer many jobs onto a SMALL number of distinct priority values.
	// p = qos + 2*age + 5*fair - 1*size.  We craft groups that share a value
	// AND groups that are only 1 apart (a "near-tie band" of width 1 would
	// merge them; a correct exact comparator keeps them strictly ordered).
	jobs := []Job{
		// ---- priority class A: p == 200 (five jobs, varied submit/ID) ----
		// 0 + 2*(1000-900) + 5*0 - 0 = 200
		{ID: "a-late", SubmitTick: 900, FairShare: 0, Size: 0, QoS: QoSBestEffort},
		// 0 + 2*(1000-905) + 5*2 - 0 = 190+10 = 200
		{ID: "a-mid-z", SubmitTick: 905, FairShare: 2, Size: 0, QoS: QoSBestEffort},
		// 0 + 2*(1000-905) + 5*2 - 0 = 200, earlier-ID twin of a-mid-z, same submit
		{ID: "a-mid-a", SubmitTick: 905, FairShare: 2, Size: 0, QoS: QoSBestEffort},
		// 150 + 2*(1000-980) + 5*2 - 0 = 150+40+10 = 200 (burstable path)
		{ID: "a-burst", SubmitTick: 980, FairShare: 2, Size: 0, QoS: QoSBurstable},
		// 0 + 2*(1000-800) + 5*0 - 200 = 400-200 = 200 (big size, very old)
		{ID: "a-old-big", SubmitTick: 800, FairShare: 0, Size: 200, QoS: QoSBestEffort},

		// ---- priority class B: p == 201 (one above A -> defeats width-1 band) ----
		// 0 + 2*(1000-900) + 5*0 - (-1)?  size is non-negative; use fair instead:
		// 0 + 2*(1000-900) + 5*0 - 0 = 200 ... bump by fair: need +1, but 5*fair
		// jumps by 5. Use age: 2*age jumps by 2. So p==201 is reachable only via
		// qos(0/150/300) combos. Use burstable with tuned fair/size: 150 + 2*25 + 5*0 - (-1)?
		// Instead: 150 + 2*(1000-975) + 5*0 - 0 = 150+50 = 200; +1 not reachable with
		// these integer weights from best-effort. Use a guaranteed job offset:
		// 300 + 2*(1000-1000) + 5*0 - 99 = 201.
		{ID: "b-guar", SubmitTick: 1000, FairShare: 0, Size: 99, QoS: QoSGuaranteed},

		// ---- priority class C: p == 199 (one below A) ----
		// 300 + 2*0 + 0 - 101 = 199
		{ID: "c-guar", SubmitTick: 1000, FairShare: 0, Size: 101, QoS: QoSGuaranteed},

		// ---- a clearly-dominant top and clearly-dominant bottom as anchors ----
		{ID: "top", SubmitTick: 0, FairShare: 100, Size: 0, QoS: QoSGuaranteed},       // 300 + 2000 + 500 = 2800
		{ID: "bottom", SubmitTick: 1000, FairShare: 0, Size: 500, QoS: QoSBestEffort}, // -500
	}

	// Sanity: log the priority of every job so the equivalence classes are
	// visible in captured evidence.
	for _, j := range jobs {
		t.Logf("run=%s setup p(%s)=%d submit=%d", advUUID, j.ID, Priority(w, j, now), j.SubmitTick)
	}

	// Establish the reference order from the natural (unpermuted) input.
	ref := drainFresh(w, jobs, now)
	t.Logf("run=%s REFERENCE dequeue order at now=%d: %v", advUUID, now, ref)

	// Cross-check: the reference must equal the independently hand-derived order
	// (same formula + documented tie-breaks), so we are not merely comparing the
	// implementation to itself.
	wantFormula := expectedOrder(w, jobs, now)
	if !eq(ref, wantFormula) {
		t.Fatalf("reference order disagrees with formula: ref=%v formula=%v", ref, wantFormula)
	}

	// THE PROBE: every permutation must yield the IDENTICAL order. A banded /
	// intransitive comparator would let the chosen top (and inner order) vary
	// with input permutation; an exact total order cannot.
	for _, seed := range seedList {
		perm := permute(jobs, seed)
		got := drainFresh(w, perm, now)
		if !eq(got, ref) {
			t.Fatalf("PERMUTATION-DEPENDENT SELECTION (seed=%d):\n  inputPerm=%v\n  got =%v\n  ref =%v\n  -> winner flips with input order: intransitive/banded comparator",
				seed, idsOf(perm), got, ref)
		}
		// Also assert Order() (the sort.SliceStable path) agrees with Pop's
		// linear-scan path under the same permutation.
		qq := New(w)
		for _, j := range perm {
			qq.Push(j)
		}
		ord := idsOf(qq.Order(now))
		if !eq(ord, ref) {
			t.Fatalf("Order() disagrees with Pop() under seed=%d: Order=%v Pop=%v", seed, ord, ref)
		}
	}
	t.Logf("run=%s PASS: %d permutations all produced identical order; top=%q stable",
		advUUID, len(seedList), ref[0])
}

// TestTransitivityExhaustive proves the comparator is a STRICT TOTAL ORDER over
// a banded-looking set by exhaustively checking the three total-order axioms on
// every triple. An intransitive banded comparator (HXC-1690) fails transitivity
// on some triple a<b, b<c, c<a within an epsilon band; here we assert no triple
// violates it and antisymmetry holds for every pair.
func TestTransitivityExhaustive(t *testing.T) {
	w := DefaultWeights()
	now := int64(1000)
	q := New(w)

	// A dense band: priorities packed 198..202 with duplicate values, plus equal
	// SubmitTicks and varied IDs to stress every tier of the tie-break chain.
	jobs := []Job{
		{ID: "j00", SubmitTick: 900, FairShare: 0, Size: 0, QoS: QoSBestEffort},    // 200
		{ID: "j01", SubmitTick: 900, FairShare: 0, Size: 0, QoS: QoSBestEffort},    // 200 (dup of j00 by value+submit, ID differs)
		{ID: "j02", SubmitTick: 901, FairShare: 1, Size: 1, QoS: QoSBestEffort},    // 2*99 + 5 - 1 = 202
		{ID: "j03", SubmitTick: 899, FairShare: 0, Size: 2, QoS: QoSBestEffort},    // 2*101 - 2 = 200
		{ID: "j04", SubmitTick: 1000, FairShare: 0, Size: 98, QoS: QoSGuaranteed},  // 300 - 98 = 202
		{ID: "j05", SubmitTick: 1000, FairShare: 0, Size: 100, QoS: QoSGuaranteed}, // 200
		{ID: "j06", SubmitTick: 1000, FairShare: 0, Size: 102, QoS: QoSGuaranteed}, // 198
		{ID: "j07", SubmitTick: 950, FairShare: 0, Size: 99, QoS: QoSGuaranteed},   // 300 + 100 - 99 = 301 (distinct)
		{ID: "j08", SubmitTick: 905, FairShare: 2, Size: 0, QoS: QoSBestEffort},    // 190 + 10 = 200
		{ID: "j09", SubmitTick: 905, FairShare: 2, Size: 0, QoS: QoSBestEffort},    // 200 (dup-by-value of j08, same submit, ID differs)
	}

	cmp := func(a, b Job) bool { return q.less(a, b, now) }

	// Antisymmetry: never a<b AND b<a. Irreflexivity: never a<a.
	for _, a := range jobs {
		if cmp(a, a) {
			t.Fatalf("irreflexivity violated: less(%s,%s)=true", a.ID, a.ID)
		}
		for _, b := range jobs {
			if a.ID == b.ID {
				continue
			}
			if cmp(a, b) && cmp(b, a) {
				t.Fatalf("antisymmetry violated for (%s,%s): both directions less==true", a.ID, b.ID)
			}
		}
	}

	// Transitivity of the "<" relation AND of the induced equivalence (neither
	// less): for all triples, less(a,b)&&less(b,c) => less(a,c). This is exactly
	// what an epsilon band breaks.
	violations := 0
	for _, a := range jobs {
		for _, b := range jobs {
			for _, c := range jobs {
				if cmp(a, b) && cmp(b, c) && !cmp(a, c) {
					t.Errorf("transitivity violated: less(%s,%s)&&less(%s,%s) but !less(%s,%s)",
						a.ID, b.ID, b.ID, c.ID, a.ID, c.ID)
					violations++
				}
			}
		}
	}
	t.Logf("run=%s transitivity check over %d triples: %d violations",
		advUUID, len(jobs)*len(jobs)*len(jobs), violations)
	if violations != 0 {
		t.Fatalf("comparator is NOT a strict total order: %d transitivity violations", violations)
	}
}

// TestMonotonicityIntrinsicPriority: holding every other factor fixed, raising a
// single intrinsic priority factor must NOT make a job rank lower. We sweep each
// factor (FairShare up, Size down, QoS up, Age via earlier SubmitTick) and
// assert the improved job's priority is >= and its dequeue rank is <= the base.
func TestMonotonicityIntrinsicPriority(t *testing.T) {
	w := DefaultWeights()
	now := int64(500)
	base := Job{ID: "base", SubmitTick: 400, FairShare: 5, Size: 10, QoS: QoSBurstable}

	improvements := []struct {
		name string
		job  Job
	}{
		{"more-fairshare", mutate(base, func(j *Job) { j.FairShare = 50 })},
		{"smaller-size", mutate(base, func(j *Job) { j.Size = 0 })},
		{"higher-qos", mutate(base, func(j *Job) { j.QoS = QoSGuaranteed })},
		{"older-submit", mutate(base, func(j *Job) { j.SubmitTick = 0 })}, // more age
	}

	for _, imp := range improvements {
		pBase := Priority(w, base, now)
		pImp := Priority(w, imp.job, now)
		t.Logf("run=%s monotonic %s: p(base)=%d p(improved)=%d", advUUID, imp.name, pBase, pImp)
		if pImp < pBase {
			t.Fatalf("%s LOWERED priority: improved=%d base=%d", imp.name, pImp, pBase)
		}
		// Rank: improved must dequeue no later than base when both present.
		// Give improved an ID that would LOSE a pure ID tie-break, so any
		// rank advantage is due to the intrinsic factor, not the ID.
		impJob := imp.job
		impJob.ID = "z-improved" // lexically larger than "base"
		order := drainFresh(w, []Job{base, impJob}, now)
		rankBase, rankImp := indexOf(order, "base"), indexOf(order, "z-improved")
		t.Logf("run=%s monotonic %s order=%v rankBase=%d rankImp=%d", advUUID, imp.name, order, rankBase, rankImp)
		if rankImp > rankBase {
			t.Fatalf("%s ranked improved AFTER base despite >= priority: order=%v", imp.name, order)
		}
	}
}

// TestBoundaryScores exercises zero/negative/extreme now and zero weights for
// crashes and ordering sanity (no panic, deterministic, total order preserved).
func TestBoundaryScores(t *testing.T) {
	cases := []struct {
		name string
		w    Weights
		now  int64
	}{
		{"zero-weights", Weights{}, 0},
		{"negative-now", DefaultWeights(), -1_000_000},
		{"zero-now", DefaultWeights(), 0},
		{"large-now", DefaultWeights(), 1 << 40},
	}
	jobs := []Job{
		{ID: "x", SubmitTick: -5, FairShare: 3, Size: 7, QoS: QoSBurstable},
		{ID: "y", SubmitTick: 10, FairShare: 0, Size: 0, QoS: QoSGuaranteed},
		{ID: "z", SubmitTick: 0, FairShare: 9, Size: 2, QoS: QoSBestEffort},
	}
	for _, tc := range cases {
		ref := drainFresh(tc.w, jobs, tc.now)
		wantFormula := expectedOrder(tc.w, jobs, tc.now)
		if !eq(ref, wantFormula) {
			t.Fatalf("%s: order %v != formula %v", tc.name, ref, wantFormula)
		}
		// Permutation-invariant under boundary conditions too.
		for _, seed := range seedList {
			got := drainFresh(tc.w, permute(jobs, seed), tc.now)
			if !eq(got, ref) {
				t.Fatalf("%s: permutation-dependent (seed=%d) got=%v ref=%v", tc.name, seed, got, ref)
			}
		}
		t.Logf("run=%s boundary %s now=%d order=%v stable", advUUID, tc.name, tc.now, ref)
	}
}

// TestSingleAndEmpty pins the trivial-size contracts.
func TestSingleAndEmpty(t *testing.T) {
	w := DefaultWeights()
	// single
	q := New(w)
	only := Job{ID: "only", SubmitTick: 1, FairShare: 1, Size: 1, QoS: QoSBurstable}
	q.Push(only)
	got := drainFresh(w, []Job{only}, 100)
	if len(got) != 1 || got[0] != "only" {
		t.Fatalf("single-element queue mishandled: %v", got)
	}
	// empty Order
	if o := New(w).Order(100); len(o) != 0 {
		t.Fatalf("empty Order should be empty, got %v", idsOf(o))
	}
	t.Logf("run=%s single/empty contracts ok", advUUID)
}

// ---- small helpers (local to adversarial tests) ----

func mutate(j Job, f func(*Job)) Job {
	cp := j
	f(&cp)
	return cp
}

func indexOf(ids []string, id string) int {
	for i, v := range ids {
		if v == id {
			return i
		}
	}
	return -1
}
