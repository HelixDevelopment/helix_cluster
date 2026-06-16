package health

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ----------------------------------------------------------------------------
// (a) FAIL-OPEN AGGREGATE — the pkg/health CompositeChecker class.
//
// getStatusLocked / rollupLocked aggregate with `overall := Healthy` and a
// switch that only matches Critical / Degraded / Unknown. Any Status value that
// is NOT one of those (a malformed / out-of-range / future enum value produced
// by a buggy or hostile check) hits NO case and is silently folded into the
// Healthy baseline. The documented contract is "Priority: Critical > Degraded >
// Unknown > Healthy" — it does NOT say "an unrecognised status counts as
// Healthy". A check that did not return a known-good status MUST NOT make the
// aggregate report HEALTHY/READY.
//
// These tests assert the SINK-SIDE verdict (GetStatus / Ready / Readiness /
// Liveness), not "no panic". They are written to PASS on a correct (fail-safe)
// implementation and FAIL on the current fail-open prod code, so they bite.
// ----------------------------------------------------------------------------

// malformedStatus is a Status outside the declared set {Unknown,Healthy,
// Degraded,Critical} == {0,1,2,3}. A check can return this through a plain bug
// (uninitialised/garbage int, arithmetic, a future enum constant the aggregator
// predates). It is exactly the "unknown status" that pkg/health's
// CompositeChecker folded to Healthy tonight.
const malformedStatus Status = 99

// TestAdversarial_GetStatus_UnknownStatusFoldsOpen proves the overall verdict.
// A single check returns an unrecognised status; the aggregate MUST NOT report
// Healthy (it never observed a healthy/serving signal from that check). A
// fail-safe implementation reports Unknown (or worse). Prod folds it to Healthy.
func TestAdversarial_GetStatus_UnknownStatusFoldsOpen(t *testing.T) {
	agg := NewAggregator()
	agg.RegisterCheck("garbage", &mockCheck{status: malformedStatus})
	agg.RunChecks(context.Background())

	got := agg.GetStatus()
	t.Logf("overall status for a single malformed check = %v (%d)", got, got)
	assert.NotEqual(t, Healthy, got,
		"FAIL-OPEN: a check that returned an unrecognised status (%d) was folded into overall HEALTHY; "+
			"an aggregate must never report healthy on a status it cannot interpret", malformedStatus)
}

// TestAdversarial_GetStatus_HealthyPlusUnknownFoldsOpen is the more dangerous
// real-world shape: most deps are healthy and ONE returns a malformed status.
// The malformed one is lost and the cluster reports HEALTHY.
func TestAdversarial_GetStatus_HealthyPlusUnknownFoldsOpen(t *testing.T) {
	agg := NewAggregator()
	agg.RegisterCheck("ok", &mockCheck{status: Healthy})
	agg.RegisterCheck("garbage", &mockCheck{status: malformedStatus})
	agg.RunChecks(context.Background())

	got := agg.GetStatus()
	t.Logf("overall status for {Healthy, malformed} = %v (%d)", got, got)
	assert.NotEqual(t, Healthy, got,
		"FAIL-OPEN: one malformed check among healthy ones disappeared and the cluster reported HEALTHY")
}

// TestAdversarial_Ready_UnknownStatusFoldsOpenReady is the readiness sink — the
// one wired to /readyz in cmd/helix-health. A readiness check that returns a
// malformed status must NOT leave the node READY (it would keep taking traffic
// it cannot serve). Ready() requires the readiness rollup == Healthy, and the
// fold-open makes rollupLocked return Healthy, so Ready() is true.
func TestAdversarial_Ready_UnknownStatusFoldsOpenReady(t *testing.T) {
	agg := NewAggregator()
	agg.RegisterCheckKind("db", &mockCheck{status: malformedStatus}, KindReadiness)
	agg.RunChecks(context.Background())

	ready := agg.Ready()
	readiness := agg.Readiness()
	t.Logf("Ready()=%v Readiness()=%v for a malformed readiness check", ready, readiness)
	assert.False(t, ready,
		"FAIL-OPEN READINESS: node reported READY while its only readiness check returned an "+
			"uninterpretable status (%d); /readyz would keep the node in rotation", malformedStatus)
	assert.NotEqual(t, Healthy, readiness,
		"readiness rollup folded a malformed status into Healthy")
}

// TestAdversarial_Liveness_UnknownStatusFoldsOpen is the liveness sink wired to
// /livez. A wedged process whose liveness check returns a malformed status
// should NOT be reported Healthy (alive); it must surface as not-Healthy so the
// supervisor can act. (Note: the "no liveness checks => Healthy" default is a
// DOCUMENTED contract and is NOT what this probes — here a liveness check exists
// and produced a bad value.)
func TestAdversarial_Liveness_UnknownStatusFoldsOpen(t *testing.T) {
	agg := NewAggregator()
	agg.RegisterCheckKind("self", &mockCheck{status: malformedStatus}, KindLiveness)
	agg.RunChecks(context.Background())

	live := agg.Liveness()
	t.Logf("Liveness()=%v for a malformed liveness check", live)
	assert.NotEqual(t, Healthy, live,
		"FAIL-OPEN LIVENESS: a liveness check returning an uninterpretable status (%d) reported Healthy/alive",
		malformedStatus)
}

// ----------------------------------------------------------------------------
// (b) THRESHOLD / AND-SEMANTICS — these PASS on prod and pin the good contract.
// A single Critical (or Degraded) check must fail the whole aggregate; one
// failing check is never averaged/voted away. Kept as anti-regression guards.
// ----------------------------------------------------------------------------

// TestAdversarial_OneCriticalFailsAggregate proves a lone Critical among many
// Healthy checks drives overall Critical (no any-pass / majority fold). Mutation
// that makes Critical return only when ALL fail would flip this.
func TestAdversarial_OneCriticalFailsAggregate(t *testing.T) {
	agg := NewAggregator()
	for i := 0; i < 7; i++ {
		agg.RegisterCheck(fmt.Sprintf("ok-%d", i), &mockCheck{status: Healthy})
	}
	agg.RegisterCheck("bad", &mockCheck{status: Critical})
	agg.RunChecks(context.Background())

	assert.Equal(t, Critical, agg.GetStatus(),
		"one Critical among healthy checks must fail the whole aggregate (AND-semantics)")
}

// TestAdversarial_OneReadinessFailureDrainsNode proves one Critical readiness
// check makes the node not-ready even with healthy liveness/other readiness.
func TestAdversarial_OneReadinessFailureDrainsNode(t *testing.T) {
	agg := NewAggregator()
	agg.RegisterCheckKind("self", &mockCheck{status: Healthy}, KindLiveness)
	agg.RegisterCheckKind("cache", &mockCheck{status: Healthy}, KindReadiness)
	agg.RegisterCheckKind("db", &mockCheck{status: Critical}, KindReadiness)
	agg.RunChecks(context.Background())

	assert.False(t, agg.Ready(),
		"one failing readiness dependency must drain the node even when others are healthy")
}

// ----------------------------------------------------------------------------
// (c) UNGUARDED SHARED STATE / CONSERVATION — run under -race.
// Concurrent Register / Unregister / RunChecks / read. Capped goroutines.
// The aggregator copies the checks map under RLock before fanning out, so this
// is expected to be race-clean; this test is the sink-side proof of that claim
// and a conservation check (final state is internally consistent).
// ----------------------------------------------------------------------------

func TestAdversarial_ConcurrentRegisterReadRace(t *testing.T) {
	agg := NewAggregator()
	const workers = 8 // ANTI-HANG cap.
	const iters = 60

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			name := fmt.Sprintf("svc-%d", id)
			for i := 0; i < iters; i++ {
				switch i % 4 {
				case 0:
					agg.RegisterCheck(name, &mockCheck{status: Healthy})
				case 1:
					agg.RegisterCheckKind(name, &mockCheck{status: Degraded}, KindBoth)
				case 2:
					agg.RunChecks(context.Background())
				case 3:
					_ = agg.GetStatus()
					_ = agg.Ready()
					_ = agg.GetAllStatuses()
					agg.UnregisterCheck(name)
				}
			}
		}(w)
	}
	wg.Wait()

	// Conservation: after the storm, results only contain currently-registered
	// names, and a final deterministic run yields a self-consistent verdict.
	agg.RunChecks(context.Background())
	all := agg.GetAllStatuses()
	st := agg.GetStatus()
	t.Logf("post-storm: %d results, overall=%v", len(all), st)
	// GetStatus must agree with a recomputation over the same snapshot.
	st2, _ := agg.StatusWithDetails()
	assert.Equal(t, st, st2, "GetStatus and StatusWithDetails must agree on the same snapshot")
}

// ----------------------------------------------------------------------------
// (d) TOTAL-ORDER / DETERMINISM and duplicate-name clobbering — PASS on prod.
// ----------------------------------------------------------------------------

// TestAdversarial_DeterministicVerdictAcrossRuns proves the overall verdict is
// order-independent: rollups are commutative (max-severity), so iterating the
// results map in random order yields the same Status every time.
func TestAdversarial_DeterministicVerdictAcrossRuns(t *testing.T) {
	build := func() *Aggregator {
		agg := NewAggregator()
		agg.RegisterCheck("a", &mockCheck{status: Healthy})
		agg.RegisterCheck("b", &mockCheck{status: Degraded})
		agg.RegisterCheck("c", &mockCheck{status: Unknown})
		agg.RegisterCheck("d", &mockCheck{status: Critical})
		agg.RunChecks(context.Background())
		return agg
	}
	want := build().GetStatus()
	for i := 0; i < 64; i++ {
		assert.Equal(t, want, build().GetStatus(),
			"overall verdict must be deterministic regardless of map iteration order")
	}
	assert.Equal(t, Critical, want, "max-severity over {Healthy,Degraded,Unknown,Critical} is Critical")
}

// TestAdversarial_DuplicateNameClobbers proves a second RegisterCheck with the
// same name replaces the first (last-writer-wins) — it must not silently keep a
// stale healthy result alongside the new one, which would let an old PASS mask a
// new FAIL.
func TestAdversarial_DuplicateNameClobbers(t *testing.T) {
	agg := NewAggregator()
	agg.RegisterCheck("svc", &mockCheck{status: Healthy})
	agg.RegisterCheck("svc", &mockCheck{status: Critical}) // clobber
	agg.RunChecks(context.Background())

	assert.Len(t, agg.GetAllStatuses(), 1, "duplicate name must collapse to one entry")
	assert.Equal(t, Critical, agg.GetStatus(),
		"last registration wins; the new Critical must not be masked by the old Healthy")
}
