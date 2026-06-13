package forecast

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
)

// runUUID returns a fresh random hex id so each test run is traceable in logs.
func runUUID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}

// firstCross returns the first tick at which a rising trace reaches/exceeds thr.
// Derived from the trace itself — not hardcoded.
func firstCross(trace [][2]float64, thr float64) (int, bool) {
	for _, p := range trace {
		if p[1] >= thr {
			return int(p[0]), true
		}
	}
	return 0, false
}

// risingTrace builds a deterministic monotonically rising trace:
// util(tick) = base + step*tick for tick in [0,n). Caller picks base/step so a
// crossing genuinely occurs within the trace.
func risingTrace(n int, base, step float64) [][2]float64 {
	tr := make([][2]float64, n)
	for i := 0; i < n; i++ {
		tr[i] = [2]float64{float64(i), base + step*float64(i)}
	}
	return tr
}

// TestClause1_PreemptiveFireBeforeCross replays a RISING trace that actually
// crosses Threshold at tick Tcross and asserts PreWarm fires at Tfire < Tcross
// with projection >= Threshold at Tfire.
//
// Mutation: a purely reactive forecaster (projected = current, ignoring slope)
// would only fire AT the crossing, i.e. Tfire == Tcross, failing Tfire < Tcross.
func TestClause1_PreemptiveFireBeforeCross(t *testing.T) {
	id := runUUID(t)
	// Trace: util = 0.10 + 0.05*tick. Threshold 0.80 => crosses when
	// 0.10+0.05*tick >= 0.80 => tick >= 14. So Tcross is DERIVED below, not assumed.
	const base, step, thr = 0.10, 0.05, 0.80
	const lead, window = 4, 3
	trace := risingTrace(30, base, step)

	tcross, ok := firstCross(trace, thr)
	if !ok {
		t.Fatalf("test setup invalid: trace never crosses thr=%.2f", thr)
	}
	// Sanity: derived Tcross matches the closed-form ceil((thr-base)/step).
	wantCross := 14 // ceil((0.80-0.10)/0.05) = ceil(14.0) = 14
	if tcross != wantCross {
		t.Fatalf("derived Tcross=%d != expected %d (formula mismatch)", tcross, wantCross)
	}

	f := &Forecaster{Window: window, Threshold: thr, Lead: lead}
	t.Logf("run=%s BEFORE: window=%d thr=%.2f lead=%d base=%.2f step=%.2f Tcross=%d",
		id, window, thr, lead, base, step, tcross)

	tfire := -1
	var projAtFire float64
	for _, p := range trace {
		tick := int(p[0])
		pre, proj := f.Observe(tick, p[1])
		if pre && tfire == -1 {
			tfire = tick
			projAtFire = proj
		}
	}

	ft, fok := f.FiredTick()
	if !fok {
		t.Fatalf("run=%s expected a PreWarm to fire, none did", id)
	}
	if ft != tfire {
		t.Fatalf("run=%s FiredTick()=%d disagrees with first observed fire %d", id, ft, tfire)
	}
	if tfire < 0 {
		t.Fatalf("run=%s PreWarm never fired", id)
	}
	// Core pre-emptive assertion: fire strictly BEFORE the real crossing.
	if !(tfire < tcross) {
		t.Fatalf("run=%s NOT pre-emptive: Tfire=%d must be < Tcross=%d", id, tfire, tcross)
	}
	// Projection at the firing tick must justify the pre-warm.
	if projAtFire < thr {
		t.Fatalf("run=%s projection at fire %.4f < threshold %.2f", id, projAtFire, thr)
	}
	// Derived check on lead horizon: with slope=step, projection at tfire is
	// util(tfire)+step*lead. The first tick where that reaches thr is the
	// smallest tick t with base+step*t+step*lead >= thr => t >= 14-lead = 10.
	wantFire := wantCross - lead // 14 - 4 = 10
	if tfire != wantFire {
		t.Fatalf("run=%s Tfire=%d != derived %d (=Tcross-Lead)", id, tfire, wantFire)
	}
	t.Logf("run=%s AFTER: Tfire=%d (<Tcross=%d, lead=%d) projAtFire=%.4f>=thr=%.2f",
		id, tfire, tcross, lead, projAtFire, thr)
}

// TestClause2_FlatNoFalsePositive feeds a flat (and a declining) trace that
// never approaches Threshold and asserts PreWarm never fires.
//
// Mutation: a forecaster that fires regardless of trend would fire here,
// failing this clause.
func TestClause2_FlatNoFalsePositive(t *testing.T) {
	id := runUUID(t)
	const thr = 0.80
	const lead, window = 4, 3

	cases := []struct {
		name string
		// above reports whether this trace's utilization is AT/above threshold
		// (true) — used to make the non-fire meaningful in different ways per
		// case — or stays below (false).
		above bool
		trace [][2]float64
	}{
		{"flat", false, risingTrace(30, 0.30, 0.0)},        // constant 0.30, slope 0
		{"declining", false, risingTrace(30, 0.50, -0.01)}, // 0.50 -> falling, slope < 0
		// high-but-flat: sits AT/above threshold yet slope==0. Only the
		// rising-trend guard prevents a fire here. A "fire regardless of
		// trend" mutant WOULD fire on this case (false positive), so this
		// case makes Clause 2 mutation-sensitive to dropping the slope>0 gate.
		{"high-flat", true, risingTrace(30, thr+0.05, 0.0)},
		// high-declining: starts above threshold and falls — capacity is being
		// shed, must NOT pre-warm. The reactive/no-trend mutant fires while
		// still above threshold; correct code does not.
		{"high-declining", true, risingTrace(30, thr+0.10, -0.02)},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &Forecaster{Window: window, Threshold: thr, Lead: lead}
			t.Logf("run=%s BEFORE: case=%s thr=%.2f lead=%d above=%v", id, c.name, thr, lead, c.above)
			fired := false
			var maxProj float64 = -1
			var maxUtil float64 = -1
			for _, p := range c.trace {
				pre, proj := f.Observe(int(p[0]), p[1])
				if proj > maxProj {
					maxProj = proj
				}
				if p[1] > maxUtil {
					maxUtil = p[1]
				}
				if pre {
					fired = true
				}
			}
			if _, ok := f.FiredTick(); ok {
				t.Fatalf("run=%s case=%s false positive: PreWarm fired on non-rising trace", id, c.name)
			}
			if fired {
				t.Fatalf("run=%s case=%s false positive: PreWarm signalled", id, c.name)
			}
			// Guard the test itself so a non-fire is meaningful, not vacuous:
			//  - "below" cases: projection genuinely stayed under threshold, so
			//    the non-fire could be due to either guard; still a valid no-FP.
			//  - "above" cases: utilization is AT/above threshold the whole time,
			//    so ONLY the rising-trend (slope>0) gate suppresses the fire.
			//    A mutant that fires regardless of trend WOULD fire here.
			if c.above {
				if maxUtil < thr {
					t.Fatalf("run=%s case=%s test invalid: maxUtil %.4f never reached thr %.2f (should be high)", id, c.name, maxUtil, thr)
				}
				t.Logf("run=%s AFTER: case=%s no fire despite maxUtil=%.4f>=thr=%.2f (suppressed by non-rising trend)", id, c.name, maxUtil, thr)
			} else {
				if maxProj >= thr {
					t.Fatalf("run=%s case=%s test invalid: maxProj %.4f reached thr %.2f", id, c.name, maxProj, thr)
				}
				t.Logf("run=%s AFTER: case=%s no fire, maxProj=%.4f < thr=%.2f", id, c.name, maxProj, thr)
			}
		})
	}
}

// TestClause3_Determinism replays identical traces through two independent
// Forecaster instances and asserts identical Tfire (and projections).
//
// Mutation: any nondeterminism (e.g. map iteration, time.Now()) would diverge.
func TestClause3_Determinism(t *testing.T) {
	id := runUUID(t)
	const base, step, thr = 0.10, 0.05, 0.80
	const lead, window = 4, 3
	trace := risingTrace(30, base, step)

	run := func() (int, bool, []float64) {
		f := &Forecaster{Window: window, Threshold: thr, Lead: lead}
		projs := make([]float64, 0, len(trace))
		for _, p := range trace {
			_, proj := f.Observe(int(p[0]), p[1])
			projs = append(projs, proj)
		}
		ft, ok := f.FiredTick()
		return ft, ok, projs
	}

	t.Logf("run=%s BEFORE: two independent instances over identical trace", id)
	t1, ok1, p1 := run()
	t2, ok2, p2 := run()

	if ok1 != ok2 || t1 != t2 {
		t.Fatalf("run=%s nondeterministic Tfire: (%d,%v) vs (%d,%v)", id, t1, ok1, t2, ok2)
	}
	if !ok1 {
		t.Fatalf("run=%s expected a fire to make determinism meaningful", id)
	}
	if len(p1) != len(p2) {
		t.Fatalf("run=%s projection length mismatch %d vs %d", id, len(p1), len(p2))
	}
	for i := range p1 {
		if p1[i] != p2[i] {
			t.Fatalf("run=%s projection diverged at i=%d: %.17g vs %.17g", id, i, p1[i], p2[i])
		}
	}
	t.Logf("run=%s AFTER: identical Tfire=%d and %d projections matched bit-for-bit", id, t1, len(p1))
}
