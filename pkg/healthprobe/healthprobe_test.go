package healthprobe

import (
	"testing"
	"time"
)

// runUUID is a fixed per-suite identifier so that interleaved test logs from a
// -race run can be attributed unambiguously to this package's closure proof.
const runUUID = "HXC-1398-healthprobe-1f2c3d4e"

// baseInstant is an arbitrary fixed wall-clock anchor; nothing in the logic
// depends on its absolute value, only on advancement of the ManualClock.
var baseInstant = time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)

// mustFind fails the test if the named dependency is absent from the result.
func mustFind(t *testing.T, r ProbeResult, name string) DependencyResult {
	t.Helper()
	d, ok := r.Find(name)
	if !ok {
		t.Fatalf("[%s] dependency %q missing from result", runUUID, name)
	}
	return d
}

// Closure clause 1: a dependency failing WITHIN the grace window reports
// Healthy under CheckStartup (tolerated) but Unhealthy under CheckReadiness,
// captured at the SAME clock instant.
//
// Mutation: if the in-grace tolerance is removed (CheckStartup returns raw),
// startup flips to Unhealthy inside grace and this test fails — proving the
// tolerance is real, not structurally guaranteed.
func TestStartupToleratesFailureInGraceButReadinessDoesNot(t *testing.T) {
	clock := NewManualClock(baseInstant)
	p := NewProber(clock, 30*time.Second)

	// "gpu" is warming up: its check fails throughout this test.
	healthy := false
	p.RegisterDependency("gpu", func() bool { return healthy })

	// Same clock instant: 10s into a 30s grace window — still in grace.
	clock.Advance(10 * time.Second)

	startup := p.CheckStartup()
	readiness := p.CheckReadiness()

	sDep := mustFind(t, startup, "gpu")
	rDep := mustFind(t, readiness, "gpu")

	t.Logf("[%s] in-grace(t=+10s/30s): raw=%s startup.aggregate=%s startup.gpu.reported=%s tolerated=%v readiness.aggregate=%s readiness.gpu.reported=%s",
		runUUID, sDep.Raw, startup.Aggregate, sDep.Reported, sDep.Tolerated, readiness.Aggregate, rDep.Reported)

	if sDep.Raw != Unhealthy {
		t.Fatalf("[%s] precondition: raw gpu health must be Unhealthy, got %s", runUUID, sDep.Raw)
	}
	if startup.Aggregate != Healthy {
		t.Errorf("[%s] startup aggregate: want Healthy (tolerated in grace), got %s", runUUID, startup.Aggregate)
	}
	if sDep.Reported != Healthy || !sDep.Tolerated {
		t.Errorf("[%s] startup gpu: want reported=Healthy tolerated=true, got reported=%s tolerated=%v", runUUID, sDep.Reported, sDep.Tolerated)
	}
	if readiness.Aggregate != Unhealthy {
		t.Errorf("[%s] readiness aggregate: want Unhealthy (never tolerate), got %s", runUUID, readiness.Aggregate)
	}
	if rDep.Reported != Unhealthy || rDep.Tolerated {
		t.Errorf("[%s] readiness gpu: want reported=Unhealthy tolerated=false, got reported=%s tolerated=%v", runUUID, rDep.Reported, rDep.Tolerated)
	}
}

// Closure clause 2: after the grace window elapses, the SAME still-failing
// dependency flips CheckStartup from Healthy (in-grace) to Unhealthy (post-grace).
//
// Mutation: if the grace window never expires (inGrace always true / elapsed
// never compared against gracePeriod), startup stays Healthy forever and the
// post-grace assertion fails — proving expiry actually fires.
func TestStartupFlipsToUnhealthyAfterGraceElapses(t *testing.T) {
	clock := NewManualClock(baseInstant)
	p := NewProber(clock, 30*time.Second)

	healthy := false // never recovers
	p.RegisterDependency("gpu", func() bool { return healthy })

	// Before: 10s in — within grace.
	clock.Advance(10 * time.Second)
	before := p.CheckStartup()
	beforeDep := mustFind(t, before, "gpu")

	// After: advance past the 30s grace boundary (10s + 25s = 35s total).
	clock.Advance(25 * time.Second)
	after := p.CheckStartup()
	afterDep := mustFind(t, after, "gpu")

	t.Logf("[%s] startup before(t=+10s)=%s/reported=%s tolerated=%v ; after(t=+35s>30s grace)=%s/reported=%s tolerated=%v",
		runUUID, before.Aggregate, beforeDep.Reported, beforeDep.Tolerated,
		after.Aggregate, afterDep.Reported, afterDep.Tolerated)

	if before.Aggregate != Healthy || beforeDep.Reported != Healthy || !beforeDep.Tolerated {
		t.Errorf("[%s] before: want startup Healthy/tolerated in grace, got aggregate=%s reported=%s tolerated=%v",
			runUUID, before.Aggregate, beforeDep.Reported, beforeDep.Tolerated)
	}
	if after.Aggregate != Unhealthy || afterDep.Reported != Unhealthy || afterDep.Tolerated {
		t.Errorf("[%s] after: want startup Unhealthy/not-tolerated post-grace, got aggregate=%s reported=%s tolerated=%v",
			runUUID, after.Aggregate, afterDep.Reported, afterDep.Tolerated)
	}
}

// Closure clause 3: a dependency that becomes healthy before grace ends is
// Healthy/Ready and stays so; liveness reflects current health independent of
// the startup grace.
//
// Mutation: if liveness erroneously applied the startup grace tolerance, the
// post-recovery failure injected below would be masked under liveness and the
// final liveness=Unhealthy assertion would fail — proving liveness ignores grace.
func TestRecoveredDependencyReadyAndLivenessIgnoresGrace(t *testing.T) {
	clock := NewManualClock(baseInstant)
	p := NewProber(clock, 30*time.Second)

	healthy := false
	p.RegisterDependency("db", func() bool { return healthy })

	// t=+5s, in grace, still failing: startup tolerates, readiness rejects.
	clock.Advance(5 * time.Second)
	preStartup := p.CheckStartup()
	preReady := p.CheckReadiness()

	// Recover before grace ends (still at t=+5s window remaining).
	healthy = true
	clock.Advance(10 * time.Second) // t=+15s, still inside 30s grace

	postStartup := p.CheckStartup()
	postReady := p.CheckReadiness()
	postLive := p.CheckLiveness()

	t.Logf("[%s] pre-recover(t=+5s): startup=%s readiness=%s ; post-recover(t=+15s): startup=%s readiness=%s liveness=%s",
		runUUID, preStartup.Aggregate, preReady.Aggregate,
		postStartup.Aggregate, postReady.Aggregate, postLive.Aggregate)

	if preStartup.Aggregate != Healthy {
		t.Errorf("[%s] pre-recover startup: want Healthy (tolerated), got %s", runUUID, preStartup.Aggregate)
	}
	if preReady.Aggregate != Unhealthy {
		t.Errorf("[%s] pre-recover readiness: want Unhealthy, got %s", runUUID, preReady.Aggregate)
	}
	if postStartup.Aggregate != Healthy || postReady.Aggregate != Healthy || postLive.Aggregate != Healthy {
		t.Errorf("[%s] post-recover: want all Healthy, got startup=%s readiness=%s liveness=%s",
			runUUID, postStartup.Aggregate, postReady.Aggregate, postLive.Aggregate)
	}

	// Liveness must track CURRENT health independent of grace: inject a fresh
	// failure while still inside the grace window and confirm liveness reports
	// Unhealthy (grace does not mask liveness), while startup still tolerates it.
	healthy = false
	clock.Advance(5 * time.Second) // t=+20s, still inside 30s grace
	liveAfterFail := p.CheckLiveness()
	startupAfterFail := p.CheckStartup()

	t.Logf("[%s] inject-fail(t=+20s, still in grace): liveness=%s startup=%s",
		runUUID, liveAfterFail.Aggregate, startupAfterFail.Aggregate)

	if liveAfterFail.Aggregate != Unhealthy {
		t.Errorf("[%s] liveness after fresh failure in-grace: want Unhealthy (grace ignored), got %s",
			runUUID, liveAfterFail.Aggregate)
	}
	if startupAfterFail.Aggregate != Healthy {
		t.Errorf("[%s] startup after fresh failure in-grace: want Healthy (still tolerated), got %s",
			runUUID, startupAfterFail.Aggregate)
	}
}

// TestGraceBoundaryIsHalfOpen proves the boundary semantics: at exactly
// startedAt+gracePeriod the window is OVER (elapsed is not strictly less than
// gracePeriod), so a failing dependency is no longer tolerated.
//
// Mutation: changing `elapsed < gracePeriod` to `elapsed <= gracePeriod` would
// keep startup Healthy at the exact boundary and fail this test.
func TestGraceBoundaryIsHalfOpen(t *testing.T) {
	clock := NewManualClock(baseInstant)
	p := NewProber(clock, 30*time.Second)
	p.RegisterDependency("x", func() bool { return false })

	clock.Advance(30 * time.Second) // exactly at the boundary
	res := p.CheckStartup()

	t.Logf("[%s] at-boundary(t=+30s == grace): startup=%s", runUUID, res.Aggregate)
	if res.Aggregate != Unhealthy {
		t.Errorf("[%s] at exact grace boundary: want Unhealthy (half-open), got %s", runUUID, res.Aggregate)
	}
}

// TestAggregateAcrossMultipleDependencies proves the aggregate is the AND of
// per-dependency reported statuses, not a fixed value.
//
// Mutation: if evaluate ignored individual failures (always Healthy aggregate),
// the one-bad-dependency case below would wrongly report Healthy and fail.
func TestAggregateAcrossMultipleDependencies(t *testing.T) {
	clock := NewManualClock(baseInstant)
	p := NewProber(clock, 1*time.Second)
	p.RegisterDependency("good", func() bool { return true })
	p.RegisterDependency("bad", func() bool { return false })

	clock.Advance(5 * time.Second) // past grace so no tolerance masks "bad"
	live := p.CheckLiveness()

	t.Logf("[%s] mixed deps post-grace: liveness.aggregate=%s deps=%d", runUUID, live.Aggregate, len(live.Dependencies))
	if live.Aggregate != Unhealthy {
		t.Errorf("[%s] one failing dep must make aggregate Unhealthy, got %s", runUUID, live.Aggregate)
	}
	if g := mustFind(t, live, "good"); g.Reported != Healthy {
		t.Errorf("[%s] good dep: want Healthy, got %s", runUUID, g.Reported)
	}
	if b := mustFind(t, live, "bad"); b.Reported != Unhealthy {
		t.Errorf("[%s] bad dep: want Unhealthy, got %s", runUUID, b.Reported)
	}
}
