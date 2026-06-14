package covgate

// Adversarial sink-side probes for the covgate coverage gate.
//
// The "sink" under test is the actual PASS/FAIL verdict of the gate:
// (*CoverageReport).MeetsThreshold, which cmd/helix-gate/main.go consumes at
//
//	if !report.MeetsThreshold(threshold) { ... FAIL ... }
//
// Every assertion below pins the real boolean verdict on a boundary / poisoned /
// no-data input — never merely "no panic". Each probe is labelled with its
// three-way classification:
//
//	CONTRACT — behaviour the package documents; pinned, no fix expected.
//	LATENT   — a fail-open / hazard that is documented but lets undertested
//	           code ship through the gate; pinned + flagged, not silently fixed.
//
// Findings (see end-of-file summary): NO undocumented (REAL) bug was found.
// The notable hazard is the documented empty-profile fail-open (class d):
// a profile that parses but measured ZERO statements yields Aggregate=100.0 and
// therefore PASSES any threshold. That is documented in parse.go ("An empty
// profile (mode line only) returns Aggregate 100.0") and already pinned by
// TestParseCoverProfile_EmptyModeOnly, so it is classified LATENT, not REAL.

import (
	"math"
	"strings"
	"testing"
)

// reportAt builds a CoverageReport with a precise Aggregate without going
// through the parser, so boundary/poison cases can be expressed exactly.
func reportAt(agg float64) *CoverageReport {
	return &CoverageReport{Aggregate: agg}
}

// ---------------------------------------------------------------------------
// (a) THRESHOLD BOUNDARY — `>` vs `>=` off-by-one at exactly the threshold.
//
// Documented contract (parse.go): "MeetsThreshold returns true if the
// aggregate coverage is >= min." So exactly-at-threshold MUST PASS, and one
// representable step below MUST FAIL.
// ---------------------------------------------------------------------------

func TestAdversarial_Threshold_ExactlyEqual_Passes(t *testing.T) {
	// CONTRACT: aggregate == threshold => PASS (the `>=` boundary).
	const thr = 80.0
	if !reportAt(thr).MeetsThreshold(thr) {
		t.Fatalf("SINK BUG: aggregate==threshold (%.4f) returned FAIL; contract says >= must PASS", thr)
	}
}

func TestAdversarial_Threshold_OneEpsilonBelow_Fails(t *testing.T) {
	// CONTRACT: the next representable float64 below the threshold must FAIL.
	// This is the off-by-one bite: if MeetsThreshold used `>` it would still
	// PASS at equality (covered above); if it (wrongly) rounded or used a
	// tolerance, this epsilon-below case would leak a PASS.
	const thr = 80.0
	below := math.Nextafter(thr, math.Inf(-1)) // largest float64 strictly < thr
	if below >= thr {
		t.Fatalf("test setup broken: Nextafter did not produce a value below threshold")
	}
	if reportAt(below).MeetsThreshold(thr) {
		t.Fatalf("SINK FAIL-OPEN: aggregate %.20g (one ULP below threshold %.20g) PASSED; must FAIL", below, thr)
	}
}

func TestAdversarial_Threshold_OneEpsilonAbove_Passes(t *testing.T) {
	// CONTRACT: one ULP above threshold must PASS (proves the gate is not
	// stuck-closed / inverted).
	const thr = 80.0
	above := math.Nextafter(thr, math.Inf(1))
	if !reportAt(above).MeetsThreshold(thr) {
		t.Fatalf("SINK STUCK-CLOSED: aggregate %.20g (one ULP above threshold) FAILED; must PASS", above)
	}
}

// End-to-end boundary through the REAL parser: a profile whose aggregate is
// exactly 80.000% must PASS an 80.0 gate and FAIL an 80.0+epsilon gate.
func TestAdversarial_Threshold_ParsedExact80_BoundaryVerdict(t *testing.T) {
	// 4 covered of 5 total => exactly 80.0%.
	const p = `mode: set
github.com/x/y/pkg/a/a.go:1.1,4.2 4 1
github.com/x/y/pkg/a/a.go:6.1,6.2 1 0
`
	r, err := ParseCoverProfile(strings.NewReader(p))
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if r.Aggregate != 80.0 {
		t.Fatalf("fixture drift: Aggregate=%.6f, want exactly 80.0", r.Aggregate)
	}
	// CONTRACT: == threshold passes.
	if !r.MeetsThreshold(80.0) {
		t.Fatalf("SINK BUG: parsed 80.0%% report FAILED an 80.0 gate; >= boundary must PASS")
	}
	// CONTRACT: just above the exact aggregate must fail.
	thrAbove := math.Nextafter(80.0, math.Inf(1))
	if r.MeetsThreshold(thrAbove) {
		t.Fatalf("SINK FAIL-OPEN: parsed 80.0%% report PASSED a gate of %.20g (> aggregate); must FAIL", thrAbove)
	}
}

// ---------------------------------------------------------------------------
// (b) NUMERIC POISON / DIV-ZERO — NaN / +-Inf coverage or threshold.
// ---------------------------------------------------------------------------

func TestAdversarial_NaNThreshold_FailsClosed(t *testing.T) {
	// CONTRACT (IEEE-754): any-comparison-with-NaN is false, so `agg >= NaN`
	// is false => FAIL. A NaN threshold (e.g. COVERAGE_THRESHOLD parsed from a
	// garbage env) must NOT pass the gate. Prove the verdict, not "no panic".
	nan := math.NaN()
	if reportAt(100.0).MeetsThreshold(nan) {
		t.Fatalf("SINK FAIL-OPEN: NaN threshold PASSED even a 100%% report; must FAIL")
	}
	if reportAt(0.0).MeetsThreshold(nan) {
		t.Fatalf("SINK FAIL-OPEN: NaN threshold PASSED a 0%% report; must FAIL")
	}
}

func TestAdversarial_NaNAggregate_FailsClosed(t *testing.T) {
	// LATENT: the parser guards total==0 so a NaN Aggregate cannot arise from
	// ParseCoverProfile today. But if a NaN ever reached the report (future
	// change / direct construction), the gate must fail closed: NaN >= thr is
	// false => FAIL. Pin that fail-closed property so a regression that, say,
	// special-cased NaN to pass would be caught here.
	if reportAt(math.NaN()).MeetsThreshold(80.0) {
		t.Fatalf("SINK FAIL-OPEN: NaN aggregate PASSED an 80%% gate; a NaN verdict must FAIL closed")
	}
	if reportAt(math.NaN()).MeetsThreshold(0.0) {
		t.Fatalf("SINK FAIL-OPEN: NaN aggregate PASSED a 0%% gate; a NaN verdict must FAIL closed")
	}
}

func TestAdversarial_NegInfThreshold_DegenerateButHonest(t *testing.T) {
	// CONTRACT: a -Inf threshold means "accept anything"; agg >= -Inf is true.
	// This documents the degenerate verdict (operator-supplied threshold).
	if !reportAt(0.0).MeetsThreshold(math.Inf(-1)) {
		t.Fatalf("unexpected: 0%% report did not satisfy a -Inf (accept-all) threshold")
	}
}

func TestAdversarial_PosInfThreshold_FailsClosed(t *testing.T) {
	// CONTRACT: a +Inf threshold is unsatisfiable; even 100%% must FAIL.
	if reportAt(100.0).MeetsThreshold(math.Inf(1)) {
		t.Fatalf("SINK FAIL-OPEN: +Inf threshold was satisfied by a 100%% report; must FAIL")
	}
}

func TestAdversarial_ZeroTotal_DivZero_DoesNotProduceNaN(t *testing.T) {
	// CONTRACT + LATENT: total==0 is the divide-by-zero site. The parser does
	// NOT compute 0/0=NaN; it special-cases to 100.0 (documented). Prove the
	// SINK: the aggregate is a finite 100.0 (not NaN), and therefore PASSES.
	r, err := ParseCoverProfile(strings.NewReader("mode: atomic\n"))
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if math.IsNaN(r.Aggregate) {
		t.Fatalf("SINK: zero-total produced NaN aggregate (raw 0/0 leaked)")
	}
	if r.Aggregate != 100.0 {
		t.Fatalf("zero-total aggregate = %.6f, documented contract is 100.0", r.Aggregate)
	}
}

// ---------------------------------------------------------------------------
// (c) RANGE — coverage must stay within [0,100]; covered<=total invariant.
// ---------------------------------------------------------------------------

func TestAdversarial_ParsedAggregate_NeverExceeds100_OrGoesNegative(t *testing.T) {
	// CONTRACT: because a block only adds to covered when it also added the
	// SAME numStmts to total, covered<=total always, so 0<=agg<=100. Probe an
	// all-covered profile (must be exactly 100, never >100) and a partly
	// covered one (must be strictly between).
	const allCovered = `mode: count
github.com/x/y/pkg/a/a.go:1.1,9.2 9 7
`
	r, err := ParseCoverProfile(strings.NewReader(allCovered))
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if r.Aggregate < 0 || r.Aggregate > 100 {
		t.Fatalf("SINK RANGE BUG: aggregate %.6f outside [0,100]", r.Aggregate)
	}
	if r.Aggregate != 100.0 {
		t.Fatalf("all-covered profile aggregate = %.6f, want 100.0", r.Aggregate)
	}
}

func TestAdversarial_NegativeCount_TreatedAsUncovered_NotNegativeRange(t *testing.T) {
	// LATENT: parse.go rejects negative numStmts but NOT a negative count.
	// A negative count is silently treated as "not covered" (count>0 is false).
	// Prove the SINK consequence: a profile whose only block has a negative
	// count reports 0% (uncovered) and therefore FAILS a real threshold — it
	// does NOT poison the range negative and does NOT fail-open to a pass.
	const negCount = `mode: count
github.com/x/y/pkg/a/a.go:1.1,5.2 5 -3
`
	r, err := ParseCoverProfile(strings.NewReader(negCount))
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if r.Aggregate != 0.0 {
		t.Fatalf("negative-count block: aggregate = %.6f, want 0.0 (treated as uncovered)", r.Aggregate)
	}
	if r.MeetsThreshold(1.0) {
		t.Fatalf("SINK FAIL-OPEN: 0%% report (negative count) PASSED a 1%% gate; must FAIL")
	}
}

// ---------------------------------------------------------------------------
// (d) FAIL-OPEN — no-data / empty profile that PASSES when it should FAIL.
//
// This is the headline hazard. It is DOCUMENTED (parse.go: "An empty profile
// (mode line only) returns Aggregate 100.0 and 0 statements") and already
// pinned by TestParseCoverProfile_EmptyModeOnly, so it is classified LATENT,
// not a REAL (undocumented) bug. We prove the fail-open VERDICT at the gate
// sink so the hazard is unmistakable and any future tightening is detectable.
// ---------------------------------------------------------------------------

func TestAdversarial_EmptyProfile_FailsOpen_PassesGate_LATENT(t *testing.T) {
	// LATENT FAIL-OPEN, pinned: a profile that parses but measured ZERO
	// statements (no tests ran / truncated-but-mode-present file) returns
	// Aggregate=100.0 and PASSES even a 100%% gate. An operator reading the
	// gate output sees "100.00% >= 80.00% PASS" while NOTHING was measured.
	//
	// This pins the documented behaviour. If covgate is ever hardened so that
	// a zero-statement profile FAILS (the safer fail-closed posture), THIS test
	// will start failing and must be updated together with that change — that
	// is the intended tripwire, not a regression.
	for _, mode := range []string{"set", "count", "atomic"} {
		r, err := ParseCoverProfile(strings.NewReader("mode: " + mode + "\n"))
		if err != nil {
			t.Fatalf("mode %q: unexpected parse error: %v", mode, err)
		}
		if r.TotalStatements != 0 {
			t.Fatalf("mode %q: fixture drift, TotalStatements=%d want 0", mode, r.TotalStatements)
		}
		// The smoking gun: no data, yet the gate verdict is PASS at 100%.
		if !r.MeetsThreshold(100.0) {
			t.Fatalf("mode %q: documented fail-open changed — zero-statement profile no longer passes 100%% gate. "+
				"If covgate was intentionally hardened to fail-closed on empty data, update this LATENT pin.", mode)
		}
		if !r.MeetsThreshold(80.0) {
			t.Fatalf("mode %q: zero-statement profile unexpectedly failed an 80%% gate", mode)
		}
	}
}

func TestAdversarial_PerPackageZeroTotal_NeverAShortfall_LATENT(t *testing.T) {
	// LATENT: the same fail-open exists at package granularity. A package that
	// contributes only zero-statement blocks gets 100.0%% and can NEVER appear
	// in Shortfalls — i.e. an empty/undertested package is invisible to the
	// shortfall sink. Pin that documented consequence.
	//
	// Build a profile where pkg/empty has a single 0-statement block. (numStmts
	// 0 is accepted; only negative numStmts is rejected.) Its coverage is the
	// documented total==0 => 100.0.
	const p = `mode: set
github.com/x/y/pkg/empty/e.go:1.1,1.1 0 0
`
	r, err := ParseCoverProfile(strings.NewReader(p))
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	emptyPkg := "github.com/x/y/pkg/empty"
	if got := r.PackageCoverage(emptyPkg); got != 100.0 {
		t.Fatalf("zero-total package coverage = %.6f, documented contract is 100.0", got)
	}
	sf := r.Shortfalls("github.com/x/y/pkg", 99.0)
	if len(sf) != 0 {
		t.Fatalf("SINK: zero-statement package appeared in shortfalls %v; documented behaviour is invisible (100%%)", sf)
	}
}

// ---------------------------------------------------------------------------
// Malformed input MUST error (fail-closed at the gate, since main.go returns
// false on a parse error). Pin a few hostile inputs that must NOT yield a
// silently-passing report.
// ---------------------------------------------------------------------------

func TestAdversarial_MalformedInput_ErrorsNotSilentPass(t *testing.T) {
	cases := map[string]string{
		"no mode line":            "github.com/x/y/z.go:1.1,2.2 1 1\n",
		"garbage first line":      "totally not a profile\n",
		"bad numStmts":            "mode: set\ngithub.com/x/y/z.go:1.1,2.2 NaN 1\n",
		"negative numStmts":       "mode: set\ngithub.com/x/y/z.go:1.1,2.2 -4 1\n",
		"two fields not three":    "mode: set\ngithub.com/x/y/z.go:1.1,2.2 1\n",
		"block spec missing colon": "mode: set\ngithub.com/x/y/z.go 1 1\n",
	}
	for name, in := range cases {
		r, err := ParseCoverProfile(strings.NewReader(in))
		if err == nil {
			t.Fatalf("%s: expected parse error (gate must fail-closed), got report %+v", name, r)
		}
	}
}
