package healthprobe

import (
	"testing"
	"time"
)

// advRunUUID tags adversarial-suite logs so a -race interleave can be attributed
// to this file's closure proof, distinct from the existing suite's runUUID.
const advRunUUID = "HXC-1398-healthprobe-adversarial-9b8a7c6d"

// advBase is a fixed wall-clock anchor; only ManualClock advancement matters.
var advBase = time.Date(2026, 6, 14, 9, 0, 0, 0, time.UTC)

// newFailingProber builds a Prober at advBase with the given grace window and a
// single dependency "dep" whose health is governed by the returned pointer
// (start failing). Flip *healthy to model recovery/failure mid-test.
func newFailingProber(grace time.Duration) (*Prober, *ManualClock, *bool) {
	clock := NewManualClock(advBase)
	p := NewProber(clock, grace)
	healthy := false
	p.RegisterDependency("dep", func() bool { return healthy })
	return p, clock, &healthy
}

// findDep is a terse helper around ProbeResult.Find that fails on absence.
func findDep(t *testing.T, r ProbeResult, name string) DependencyResult {
	t.Helper()
	d, ok := r.Find(name)
	if !ok {
		t.Fatalf("[%s] dependency %q missing from result", advRunUUID, name)
	}
	return d
}

// TestGraceBoundaryTriple is the CENTERPIECE. With a permanently-failing startup
// dependency and a 30s grace window, it pins the inclusive/exclusive edge at the
// three adjacent clock instants around the boundary:
//
//	grace-1ns : elapsed strictly < grace  => still IN grace  => startup tolerated => Healthy
//	grace+0   : elapsed == grace          => grace OVER (half-open) => startup Unhealthy
//	grace+1ns : elapsed strictly > grace  => grace OVER          => startup Unhealthy
//
// Each instant is probed on a FRESH prober so a stale evaluation cannot leak.
// The contract is the half-open window [startedAt, startedAt+gracePeriod):
// `inGrace()` is `elapsed < gracePeriod`.
//
// Mutation that bites: flipping `elapsed < gracePeriod` to `elapsed <=
// gracePeriod` keeps startup Healthy at grace+0 (and only there), failing the
// grace+0 sub-assertion while grace-1ns and grace+1ns still pass — isolating the
// exact boundary cell.
func TestGraceBoundaryTriple(t *testing.T) {
	const grace = 30 * time.Second

	cases := []struct {
		name    string
		elapsed time.Duration
		wantAgg Status
		wantTol bool // expected Tolerated on the dep
		inGrace bool
	}{
		{"grace-1ns(strictly inside)", grace - 1, Healthy, true, true},
		{"grace+0(exact boundary, half-open => out)", grace, Unhealthy, false, false},
		{"grace+1ns(strictly outside)", grace + 1, Unhealthy, false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, clock, _ := newFailingProber(grace)
			clock.Advance(tc.elapsed)

			res := p.CheckStartup()
			dep := findDep(t, res, "dep")

			t.Logf("[%s] elapsed=%v grace=%v: raw=%s startup.aggregate=%s reported=%s tolerated=%v",
				advRunUUID, tc.elapsed, grace, dep.Raw, res.Aggregate, dep.Reported, dep.Tolerated)

			if dep.Raw != Unhealthy {
				t.Fatalf("[%s] precondition: raw must be Unhealthy, got %s", advRunUUID, dep.Raw)
			}
			if res.Aggregate != tc.wantAgg {
				t.Errorf("[%s] elapsed=%v: startup aggregate want %s, got %s",
					advRunUUID, tc.elapsed, tc.wantAgg, res.Aggregate)
			}
			if dep.Tolerated != tc.wantTol {
				t.Errorf("[%s] elapsed=%v: tolerated want %v, got %v",
					advRunUUID, tc.elapsed, tc.wantTol, dep.Tolerated)
			}
			// Reported mirrors aggregate for the single-dep case.
			if dep.Reported != tc.wantAgg {
				t.Errorf("[%s] elapsed=%v: reported want %s, got %s",
					advRunUUID, tc.elapsed, tc.wantAgg, dep.Reported)
			}
		})
	}
}

// TestReadinessNeverGetsStartupGrace is the second CENTERPIECE. At the SAME clock
// instant well inside the grace window, a failing dependency must be:
//   - startup:   tolerated => Healthy   (startup grace masks it)
//   - readiness: NOT tolerated => Unhealthy (the documented distinction)
//   - liveness:  NOT tolerated => Unhealthy (reflects raw health)
//
// Probing all three at one instant proves readiness/liveness do not borrow the
// startup grace. The grace-1ns instant is the hardest case: it is the LAST
// instant startup tolerates, so readiness diverging here is maximal pressure.
//
// Mutation that bites: giving readiness the startup policy (return Healthy,true
// when raw==Unhealthy && inGrace) would flip readiness to Healthy here and fail.
func TestReadinessNeverGetsStartupGrace(t *testing.T) {
	const grace = 30 * time.Second
	p, clock, _ := newFailingProber(grace)

	// One nanosecond before the boundary: the last instant startup still tolerates.
	clock.Advance(grace - 1)

	startup := p.CheckStartup()
	readiness := p.CheckReadiness()
	liveness := p.CheckLiveness()

	sDep := findDep(t, startup, "dep")
	rDep := findDep(t, readiness, "dep")
	lDep := findDep(t, liveness, "dep")

	t.Logf("[%s] at grace-1ns (deepest tolerance): startup.agg=%s(tol=%v) readiness.agg=%s(tol=%v) liveness.agg=%s(tol=%v)",
		advRunUUID, startup.Aggregate, sDep.Tolerated, readiness.Aggregate, rDep.Tolerated, liveness.Aggregate, lDep.Tolerated)

	if startup.Aggregate != Healthy || !sDep.Tolerated {
		t.Errorf("[%s] startup at grace-1ns: want Healthy/tolerated, got %s/tol=%v",
			advRunUUID, startup.Aggregate, sDep.Tolerated)
	}
	if readiness.Aggregate != Unhealthy || rDep.Tolerated {
		t.Errorf("[%s] readiness at grace-1ns: want Unhealthy/not-tolerated (no startup grace), got %s/tol=%v",
			advRunUUID, readiness.Aggregate, rDep.Tolerated)
	}
	if liveness.Aggregate != Unhealthy || lDep.Tolerated {
		t.Errorf("[%s] liveness at grace-1ns: want Unhealthy/not-tolerated (no startup grace), got %s/tol=%v",
			advRunUUID, liveness.Aggregate, lDep.Tolerated)
	}
	// Cross-tier divergence at the same instant is the load-bearing fact.
	if startup.Aggregate == readiness.Aggregate {
		t.Errorf("[%s] startup and readiness must DIVERGE in grace; both = %s",
			advRunUUID, startup.Aggregate)
	}
}

// TestStartupRecoveryFlipsBackHealthy proves a failing dependency that recovers
// is reflected immediately (and is not Tolerated once raw is Healthy), then a
// re-failure inside grace is once again tolerated by startup. Recovery is not
// sticky in either direction; each probe reflects the live check.
//
// Mutation that bites: if the policy stuck a prior Unhealthy/Tolerated verdict
// instead of re-reading the check, post-recovery would stay tolerated/old.
func TestStartupRecoveryFlipsBackHealthy(t *testing.T) {
	const grace = 60 * time.Second
	p, clock, healthy := newFailingProber(grace)

	clock.Advance(5 * time.Second) // in grace, failing
	d1 := findDep(t, p.CheckStartup(), "dep")
	if d1.Raw != Unhealthy || d1.Reported != Healthy || !d1.Tolerated {
		t.Errorf("[%s] phase1 (in-grace failing): want raw=Unhealthy reported=Healthy tolerated=true, got raw=%s reported=%s tol=%v",
			advRunUUID, d1.Raw, d1.Reported, d1.Tolerated)
	}

	*healthy = true                // recover
	clock.Advance(5 * time.Second) // t=+10s, still in grace
	d2 := findDep(t, p.CheckStartup(), "dep")
	if d2.Raw != Healthy || d2.Reported != Healthy || d2.Tolerated {
		t.Errorf("[%s] phase2 (recovered in-grace): want raw=Healthy reported=Healthy tolerated=false, got raw=%s reported=%s tol=%v",
			advRunUUID, d2.Raw, d2.Reported, d2.Tolerated)
	}

	*healthy = false               // re-fail, still in grace
	clock.Advance(5 * time.Second) // t=+15s
	d3 := findDep(t, p.CheckStartup(), "dep")
	if d3.Raw != Unhealthy || d3.Reported != Healthy || !d3.Tolerated {
		t.Errorf("[%s] phase3 (re-failed in-grace): want raw=Unhealthy reported=Healthy tolerated=true, got raw=%s reported=%s tol=%v",
			advRunUUID, d3.Raw, d3.Reported, d3.Tolerated)
	}

	// Past grace, the same failure is no longer tolerated.
	clock.Advance(grace) // well past boundary
	d4 := findDep(t, p.CheckStartup(), "dep")
	t.Logf("[%s] recovery path: p1(in,fail)=%s/tol%v p2(in,ok)=%s/tol%v p3(in,fail)=%s/tol%v p4(out,fail)=%s/tol%v",
		advRunUUID, d1.Reported, d1.Tolerated, d2.Reported, d2.Tolerated, d3.Reported, d3.Tolerated, d4.Reported, d4.Tolerated)
	if d4.Reported != Unhealthy || d4.Tolerated {
		t.Errorf("[%s] phase4 (failing post-grace): want reported=Unhealthy tolerated=false, got reported=%s tol=%v",
			advRunUUID, d4.Reported, d4.Tolerated)
	}
}

// TestTierIndependenceMultiDep proves the three tiers do not bleed into each
// other when evaluated over the same dependency set at the same instant: with a
// failing "slow" dep IN grace and a healthy "fast" dep, startup tolerates slow
// (aggregate Healthy) while readiness and liveness reject it (aggregate
// Unhealthy), and the healthy dep is never wrongly marked tolerated anywhere.
//
// Mutation that bites: coupling liveness/readiness to startup (sharing the
// tolerate policy) would flip their aggregate to Healthy and fail.
func TestTierIndependenceMultiDep(t *testing.T) {
	const grace = 30 * time.Second
	clock := NewManualClock(advBase)
	p := NewProber(clock, grace)
	p.RegisterDependency("fast", func() bool { return true })  // always healthy
	p.RegisterDependency("slow", func() bool { return false }) // never healthy

	clock.Advance(10 * time.Second) // in grace

	startup := p.CheckStartup()
	readiness := p.CheckReadiness()
	liveness := p.CheckLiveness()

	t.Logf("[%s] multi-dep in-grace: startup.agg=%s readiness.agg=%s liveness.agg=%s",
		advRunUUID, startup.Aggregate, readiness.Aggregate, liveness.Aggregate)

	if startup.Aggregate != Healthy {
		t.Errorf("[%s] startup: want Healthy (slow tolerated in grace), got %s", advRunUUID, startup.Aggregate)
	}
	if readiness.Aggregate != Unhealthy {
		t.Errorf("[%s] readiness: want Unhealthy (slow rejected), got %s", advRunUUID, readiness.Aggregate)
	}
	if liveness.Aggregate != Unhealthy {
		t.Errorf("[%s] liveness: want Unhealthy (slow rejected), got %s", advRunUUID, liveness.Aggregate)
	}

	// The healthy "fast" dep must be Healthy/not-tolerated in every tier.
	for _, res := range []struct {
		tier string
		r    ProbeResult
	}{{"startup", startup}, {"readiness", readiness}, {"liveness", liveness}} {
		fast := findDep(t, res.r, "fast")
		if fast.Raw != Healthy || fast.Reported != Healthy || fast.Tolerated {
			t.Errorf("[%s] %s.fast: want Healthy/not-tolerated, got raw=%s reported=%s tol=%v",
				advRunUUID, res.tier, fast.Raw, fast.Reported, fast.Tolerated)
		}
	}
	// startup.slow is the only place "slow" is tolerated.
	sSlow := findDep(t, startup, "slow")
	if !sSlow.Tolerated || sSlow.Reported != Healthy {
		t.Errorf("[%s] startup.slow: want tolerated/Healthy, got tol=%v reported=%s", advRunUUID, sSlow.Tolerated, sSlow.Reported)
	}
	rSlow := findDep(t, readiness, "slow")
	if rSlow.Tolerated || rSlow.Reported != Unhealthy {
		t.Errorf("[%s] readiness.slow: want not-tolerated/Unhealthy, got tol=%v reported=%s", advRunUUID, rSlow.Tolerated, rSlow.Reported)
	}
}

// TestZeroGraceBoundaryNeverTolerates is an edge: a zero-length grace window
// means elapsed (0) < grace (0) is false at t+0 — there is no instant at which a
// failing startup dependency is tolerated. Pins the degenerate boundary.
//
// Mutation that bites: `<=` here would make 0 <= 0 true and wrongly tolerate at
// t+0, failing this test (complements the grace+0 cell of the triple).
func TestZeroGraceBoundaryNeverTolerates(t *testing.T) {
	p, _, _ := newFailingProber(0)

	res := p.CheckStartup() // no advance: elapsed == 0 == grace
	dep := findDep(t, res, "dep")

	t.Logf("[%s] zero-grace at t+0: startup.aggregate=%s tolerated=%v", advRunUUID, res.Aggregate, dep.Tolerated)
	if res.Aggregate != Unhealthy || dep.Tolerated {
		t.Errorf("[%s] zero grace: want Unhealthy/not-tolerated at t+0, got %s/tol=%v",
			advRunUUID, res.Aggregate, dep.Tolerated)
	}
}

// TestNoDependenciesIsVacuouslyHealthy is an edge: a prober with zero registered
// dependencies reports Healthy aggregate (empty AND is true) across all tiers and
// does not panic, even past the grace boundary.
func TestNoDependenciesIsVacuouslyHealthy(t *testing.T) {
	clock := NewManualClock(advBase)
	p := NewProber(clock, 10*time.Second)

	clock.Advance(20 * time.Second) // past grace, still no deps

	for _, tc := range []struct {
		tier string
		r    ProbeResult
	}{{"startup", p.CheckStartup()}, {"readiness", p.CheckReadiness()}, {"liveness", p.CheckLiveness()}} {
		if tc.r.Aggregate != Healthy {
			t.Errorf("[%s] %s with no deps: want Healthy (vacuous), got %s", advRunUUID, tc.tier, tc.r.Aggregate)
		}
		if len(tc.r.Dependencies) != 0 {
			t.Errorf("[%s] %s with no deps: want 0 dependency results, got %d", advRunUUID, tc.tier, len(tc.r.Dependencies))
		}
	}
	t.Logf("[%s] no-deps past grace: all tiers vacuously Healthy, no panic", advRunUUID)
}
