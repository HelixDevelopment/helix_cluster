package thermalwarm

// Adversarial sink-side probes for the thermal pre-warm threshold surface.
//
// thermalwarm's "threshold" is a DURATION/TIME comparison, not a float
// temperature: the warm/serve verdict fires iff
//
//	now >= warmStartedAt.Add(coldStart)        (currentState: !now.Before(boundary))
//
// time.Duration / time.Time are int64-backed, so classic float NaN/±Inf cannot
// occur on this surface (those are float64-only). The int64 analogue — a
// silent time.Time.Add/Sub overflow flipping the verdict — is NOT reachable
// within the package's valid domain (a normal-era clock + any coldStart
// <= math.MaxInt64 ns lands in year ~2318, far below the overflow point near
// year 2.9e11; see THE NUMERIC-POISON probe below which proves the verdict is
// honest there).
//
// Every assertion below checks the SINK: the actual State()/Dispatch() verdict
// (Hot vs Warming, ServedHot, the cold-start charged), never merely "no panic".
//
// FINDING (see final report): NO REAL BUG on the threshold/numeric surface
// within the documented valid domain. The coldStart<=0 cases are DOCUMENTED
// PRECONDITION VIOLATIONS ("coldStart must be > 0", NewController doc) and are
// pinned here as contract characterisations, not bug reproducers — they are
// explicitly marked and are NOT fixed in prod.

import (
	"math"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// (b) THRESHOLD BOUNDARY — exact-boundary + one-ns triplet, sink-side verdict.
// ---------------------------------------------------------------------------

// TestThresholdBoundaryTriplet_StateVerdict pins the `<` vs `<=` choice at the
// warm-up boundary from BOTH sides with single-nanosecond resolution.
//
// Contract (currentState): Hot iff now >= warmStartedAt+coldStart, i.e. the
// boundary instant ITSELF reads Hot (>=, not >). One ns before reads Warming.
//
// Mutation A (flip `!now.Before(...)` -> `now.After(...)`, i.e. > not >=):
//
//	the exact-boundary case would read Warming -> the `elapsed==coldStart`
//	sub-assertion (want Hot) fails.
//
// Mutation B (flip to `!now.After(...)`, i.e. classify boundary+anything Hot
// too eagerly / off-by-one the other way): the `elapsed==coldStart-1ns` case
// would read Hot -> the (want Warming) sub-assertion fails.
func TestThresholdBoundaryTriplet_StateVerdict(t *testing.T) {
	const coldStart = 1000 * time.Millisecond
	cases := []struct {
		name    string
		elapsed time.Duration
		want    State
	}{
		{"one-ns-before-boundary", coldStart - 1, Warming},
		{"exactly-at-boundary", coldStart, Hot},
		{"one-ns-after-boundary", coldStart + 1, Hot},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clk := NewManualClock(anchor)
			c := NewController(clk, coldStart)
			id := runUUID(t)
			c.PreWarm(id) // Cold -> Warming, warmStartedAt = anchor
			c.Advance(tc.elapsed)

			got := c.State(id)
			t.Logf("elapsed=%s boundary=%s now=%s verdict=%s want=%s",
				tc.elapsed,
				anchor.Add(coldStart).Format(time.RFC3339Nano),
				clk.Now().Format(time.RFC3339Nano), got, tc.want)
			if got != tc.want {
				t.Fatalf("boundary verdict: elapsed=%s state=%s want=%s", tc.elapsed, got, tc.want)
			}
		})
	}
}

// TestThresholdBoundary_DispatchRemainderVerdict pins the SAME boundary on the
// Dispatch (serve) side: dispatching exactly one ns before the boundary must
// charge a 1ns remainder (not the full coldStart, not zero), and dispatching at
// the boundary must serve Hot with zero charge.
//
// This is the throttle/serve sink: a too-early serve that wrongly reports
// ServedHot (admitting a not-yet-warm backend as if hot) is the failure we hunt.
func TestThresholdBoundary_DispatchRemainderVerdict(t *testing.T) {
	const coldStart = 1000 * time.Millisecond

	t.Run("one-ns-before-boundary-charges-1ns-remainder", func(t *testing.T) {
		clk := NewManualClock(anchor)
		c := NewController(clk, coldStart)
		id := runUUID(t)
		c.PreWarm(id)
		c.Advance(coldStart - 1) // 1ns short of warm

		res := c.Dispatch(id)
		t.Logf("servedHot=%v coldStart=%s final=%s", res.ServedHot, res.ColdStart, res.FinalState)
		if res.ServedHot {
			t.Fatalf("1ns-before-boundary dispatch: ServedHot=true want false (backend NOT yet warm — wrongly admitted)")
		}
		if res.ColdStart != 1 {
			t.Fatalf("1ns-before-boundary dispatch: remainder charged=%s want 1ns", res.ColdStart)
		}
		if res.FinalState != Hot {
			t.Fatalf("post-dispatch state=%s want Hot", res.FinalState)
		}
	})

	t.Run("exactly-at-boundary-serves-hot-zero-charge", func(t *testing.T) {
		clk := NewManualClock(anchor)
		c := NewController(clk, coldStart)
		id := runUUID(t)
		c.PreWarm(id)
		c.Advance(coldStart) // exactly warm

		before := clk.Now()
		res := c.Dispatch(id)
		t.Logf("servedHot=%v coldStart=%s clockAdvanced=%s", res.ServedHot, res.ColdStart, clk.Now().Sub(before))
		if !res.ServedHot {
			t.Fatalf("at-boundary dispatch: ServedHot=false want true (warm backend wrongly throttled)")
		}
		if res.ColdStart != 0 {
			t.Fatalf("at-boundary dispatch: coldStart=%s want 0", res.ColdStart)
		}
		if !clk.Now().Equal(before) {
			t.Fatalf("at-boundary dispatch advanced clock by %s want 0", clk.Now().Sub(before))
		}
	})
}

// ---------------------------------------------------------------------------
// (a) NUMERIC POISON — int64 time overflow analogue of NaN/±Inf.
//     Proves the verdict is HONEST at the extreme of the valid domain: the
//     largest constructible coldStart (math.MaxInt64 ns) does NOT wrap a
//     normal-era clock into a wrong Hot verdict. A pre-warmed backend with a
//     ~292-year coldStart must read Warming (NOT instantly admitted Hot) and a
//     mid-warm dispatch must NOT report ServedHot.
// ---------------------------------------------------------------------------

func TestNumericPoison_MaxColdStartDoesNotWrapToHot(t *testing.T) {
	const coldStart = time.Duration(math.MaxInt64) // ~292 years, largest valid Duration
	clk := NewManualClock(anchor)                  // year 2026 — normal era
	c := NewController(clk, coldStart)
	id := runUUID(t)

	c.PreWarm(id) // Cold -> Warming at anchor
	boundary := anchor.Add(coldStart)
	t.Logf("anchor=%s coldStart=%s boundary=%s before(boundary)=%v",
		anchor.Format(time.RFC3339Nano), coldStart,
		boundary.Format(time.RFC3339Nano), anchor.Before(boundary))

	// Sink: with essentially no time elapsed, a 292-year warm-up is nowhere near
	// done. If Add had silently overflowed (boundary < now), the verdict would
	// flip to Hot — a too-hot device admitted. It must stay Warming.
	if got := c.State(id); got != Warming {
		t.Fatalf("max-coldStart pre-warm with ~0 elapsed: state=%s want Warming "+
			"(silent time overflow would wrongly admit as Hot)", got)
	}

	// Advance a year — still astronomically short of 292 years. Still Warming.
	c.Advance(365 * 24 * time.Hour)
	if got := c.State(id); got != Warming {
		t.Fatalf("max-coldStart after +1yr: state=%s want Warming", got)
	}

	// Serve sink: a dispatch mid-(292yr)-warm must NOT claim ServedHot, and must
	// charge a positive multi-century remainder — not a wrapped negative/zero.
	res := c.Dispatch(id)
	t.Logf("mid-warm dispatch: servedHot=%v remainder=%s final=%s", res.ServedHot, res.ColdStart, res.FinalState)
	if res.ServedHot {
		t.Fatalf("max-coldStart mid-warm dispatch: ServedHot=true want false (wrongly admitted not-yet-warm backend)")
	}
	if res.ColdStart <= 0 {
		t.Fatalf("max-coldStart mid-warm dispatch: remainder charged=%s want > 0 (a wrapped/overflowed remainder)", res.ColdStart)
	}
}

// ---------------------------------------------------------------------------
// (b') ZERO / NEGATIVE coldStart — DOCUMENTED PRECONDITION VIOLATION.
//      NewController doc: "coldStart must be > 0". The constructor does NOT
//      validate it. These tests CHARACTERISE (pin) the resulting behaviour so a
//      future change is noticed; they are NOT bug reproducers and the prod code
//      is intentionally left unchanged (the contract disclaims coldStart<=0).
//      Reported separately as a LATENT RISK (unvalidated precondition).
// ---------------------------------------------------------------------------

// TestContract_ZeroColdStart_PinsInstantHot documents that coldStart==0 (a
// precondition violation) makes a pre-warmed backend read Hot with zero elapsed
// time, and a Cold dispatch charges zero. This is the natural consequence of
// the >= boundary at boundary==warmStartedAt. PINNED, not fixed.
func TestContract_ZeroColdStart_PinsInstantHot(t *testing.T) {
	clk := NewManualClock(anchor)
	c := NewController(clk, 0) // VIOLATES "coldStart must be > 0"
	id := runUUID(t)

	c.PreWarm(id)
	// boundary == warmStartedAt == now, and now>=boundary -> Hot immediately.
	if got := c.State(id); got != Hot {
		t.Fatalf("DOC-PIN coldStart==0: state=%s want Hot (boundary==warmStartedAt, >= is true)", got)
	}

	// Cold dispatch on a fresh backend charges zero (Tick(0) is a no-op).
	id2 := runUUID(t)
	before := clk.Now()
	res := c.Dispatch(id2)
	t.Logf("DOC-PIN coldStart==0 cold dispatch: servedHot=%v coldStart=%s clockAdvanced=%s",
		res.ServedHot, res.ColdStart, clk.Now().Sub(before))
	if res.ColdStart != 0 {
		t.Fatalf("DOC-PIN coldStart==0 cold dispatch: charge=%s want 0", res.ColdStart)
	}
	if res.FinalState != Hot {
		t.Fatalf("DOC-PIN coldStart==0 cold dispatch: final=%s want Hot", res.FinalState)
	}
}

// TestContract_NegativeColdStart_PinsBackwardClockAndNegativeCharge documents
// the (precondition-violating) coldStart<0 behaviour: a Cold dispatch charges a
// NEGATIVE cold-start and runs the ManualClock BACKWARDS. This is surfaced as a
// LATENT RISK: NewController silently accepts coldStart<=0 instead of rejecting
// it. PINNED to make the unvalidated-precondition consequence explicit; prod is
// deliberately NOT changed (the documented contract forbids coldStart<=0).
func TestContract_NegativeColdStart_PinsBackwardClockAndNegativeCharge(t *testing.T) {
	const neg = -500 * time.Millisecond
	clk := NewManualClock(anchor)
	c := NewController(clk, neg) // VIOLATES "coldStart must be > 0"
	id := runUUID(t)

	before := clk.Now()
	res := c.Dispatch(id) // Cold path: Tick(neg) moves clock back, charge=neg
	advanced := clk.Now().Sub(before)
	t.Logf("DOC-PIN coldStart<0 cold dispatch: servedHot=%v coldStart=%s clockDelta=%s final=%s",
		res.ServedHot, res.ColdStart, advanced, res.FinalState)

	// Pin the (pathological) observable consequences so a future change trips here.
	if res.ColdStart != neg {
		t.Fatalf("DOC-PIN coldStart<0: charge=%s want %s (negative charge — unvalidated precondition)", res.ColdStart, neg)
	}
	if advanced != neg {
		t.Fatalf("DOC-PIN coldStart<0: clock moved by %s want %s (clock ran backward)", advanced, neg)
	}
	if res.FinalState != Hot {
		t.Fatalf("DOC-PIN coldStart<0: final=%s want Hot", res.FinalState)
	}
}

// ---------------------------------------------------------------------------
// (b'') ZERO / NEGATIVE elapsed & sticky-Hot — verdict monotonicity.
//        Once Hot is persisted it must never regress to Warming/Cold even if the
//        clock is rolled backwards underneath the controller.
// ---------------------------------------------------------------------------

func TestStickyHot_DoesNotRegressOnClockRewind(t *testing.T) {
	const coldStart = 100 * time.Millisecond
	clk := NewManualClock(anchor)
	c := NewController(clk, coldStart)
	id := runUUID(t)

	c.PreWarm(id)
	c.Advance(coldStart) // -> Hot
	if got := c.State(id); got != Hot {
		t.Fatalf("setup: state=%s want Hot", got)
	}

	// Roll the clock far back BELOW warmStartedAt. The sticky persist (b.state=Hot
	// written on the first post-boundary State) means currentState short-circuits
	// on b.state!=Warming and returns Hot regardless of `now`. Verdict must hold.
	clk.t = anchor.Add(-time.Hour)
	if got := c.State(id); got != Hot {
		t.Fatalf("after clock rewind: state=%s want Hot (Hot must be sticky/monotonic)", got)
	}
	res := c.Dispatch(id)
	if !res.ServedHot || res.ColdStart != 0 {
		t.Fatalf("post-rewind dispatch: servedHot=%v coldStart=%s want true/0", res.ServedHot, res.ColdStart)
	}
}

// ---------------------------------------------------------------------------
// (d) UNGUARDED SHARED STATE — independent -race storm (corroborates the
//     existing concurrent tests). Mixed Register/PreWarm/State/Dispatch against
//     ONE controller + ONE shared backend; -race must stay clean.
// ---------------------------------------------------------------------------

func TestAdversarialConcurrentSingleBackend_NoRace(t *testing.T) {
	const coldStart = 2 * time.Millisecond
	clk := newRaceClock(anchor) // concurrency-safe non-ManualClock; Tick is a no-op
	c := NewController(clk, coldStart)
	id := runUUID(t)
	c.Register(id)

	var wg sync.WaitGroup
	start := make(chan struct{})
	const goroutines, iters = 96, 300

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			<-start
			for i := 0; i < iters; i++ {
				switch (g + i) % 4 {
				case 0:
					c.PreWarm(id)
				case 1:
					_ = c.State(id) // read-looking, writes sticky Hot
				case 2:
					_ = c.Dispatch(id)
				case 3:
					c.Register(id)
				}
			}
		}(g)
	}
	close(start)
	wg.Wait()

	if s := c.State(id); s != Hot {
		t.Fatalf("after single-backend storm: state=%s want Hot", s)
	}
}
