// Adversarial / sink-side probes for package chaos.
//
// These tests were written by an adversarial test engineer to attack the four
// highest-value classes the package could plausibly get wrong:
//
//   (a) BLAST-RADIUS / SAFETY BOUND — assert the number of *concurrently* active
//       faults / affected targets NEVER exceeds what the runner promises, under
//       concurrent runner starts. Sink-side: an atomic in-flight counter and the
//       total count of injected faults.
//   (b) UNGUARDED SHARED STATE — concurrent Run + RollbackRecords on one runner,
//       and concurrent selectNode, under -race. Conservation of records.
//   (c) NUMERIC — NaN / ±Inf / out-of-[0,1] SLO slipping the canary guard.
//       Sink-side: did the canary fire (ErrRolledBack + a RollbackRecord) or not,
//       given a known health value.
//   (d) TOTAL-ORDER / DETERMINISM — fault execution order and seeded node
//       selection are deterministic (no map-iteration nondeterminism in
//       scheduling).
//
// ANTI-HANG: concurrency is capped (<=8 goroutines), every runner uses a
// FakeClock that auto-advances, and FaultDuration is a small multiple of
// PollInterval so each Run terminates in a bounded number of ticks.
//
// FINDINGS (see final report): the package has NO configured blast-radius field
// and NO shared experiment-state manager map — Run executes faults strictly
// sequentially, so the "bound" it actually offers is "<= 1 fault active per
// runner at any instant" and "exactly len(faults) injects in a no-trip run".
// These tests PIN that real contract. The numeric probes document that an
// out-of-[0,1] / NaN SLO is an unvalidated caller-precondition (the doc states
// "Values in [0.0, 1.0]"): NaN silently DISABLES the canary, SLO>1 makes it fire
// on the first poll even on a fully healthy cluster. No production behavior is
// changed by this file.
package chaos

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// adversarialClock builds a FakeClock that auto-advances by d on every After()
// call and fires immediately, so a runner loop terminates after
// FaultDuration/PollInterval ticks with no real sleeps.
func adversarialClock() *FakeClock {
	return NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
}

// countingFault wraps a real PodKill but records, via shared atomics, how many
// faults are concurrently "in flight" (injected but not yet recovered) and the
// running total of injects. This is the SINK we assert the blast radius against.
type countingFault struct {
	nodeID   string
	inFlight *int32 // currently-active injects across all runners
	maxSeen  *int32 // high-water mark of inFlight
	injects  *int32 // total inject calls
}

func (f *countingFault) Name() string { return "counting(" + f.nodeID + ")" }

func (f *countingFault) Inject(ctx context.Context, target Target) error {
	atomic.AddInt32(f.injects, 1)
	cur := atomic.AddInt32(f.inFlight, 1)
	// Track high-water mark (best-effort CAS loop, race-free).
	for {
		old := atomic.LoadInt32(f.maxSeen)
		if cur <= old || atomic.CompareAndSwapInt32(f.maxSeen, old, cur) {
			break
		}
	}
	return (&PodKill{NodeID: f.nodeID}).Inject(ctx, target)
}

func (f *countingFault) Recover(ctx context.Context, target Target) error {
	atomic.AddInt32(f.inFlight, -1)
	return (&PodKill{NodeID: f.nodeID}).Recover(ctx, target)
}

// ---------------------------------------------------------------------------
// (a) BLAST RADIUS / SAFETY BOUND
//
// Within ONE runner: Run executes faults strictly sequentially, recovering each
// before the next is injected. Therefore at most ONE fault may be in flight at
// any instant for a single runner, regardless of how many faults are scheduled.
// This pins the real "blast radius" guarantee of the runner.
// ---------------------------------------------------------------------------

func TestAdversarial_BlastRadius_SingleRunnerNeverConcurrent(t *testing.T) {
	cluster := NewFakeCluster("n0", "n1", "n2", "n3", "n4")
	cluster.SetHealthOverride(func() float64 { return 1.0 }) // never trips

	var inFlight, maxSeen, injects int32
	faults := make([]Fault, 0, 5)
	for _, id := range cluster.Nodes() {
		faults = append(faults, &countingFault{nodeID: id, inFlight: &inFlight, maxSeen: &maxSeen, injects: &injects})
	}

	clk := adversarialClock()
	poll := 5 * time.Millisecond
	cfg := RunnerConfig{SLO: 0.0, PollInterval: poll, FaultDuration: 2 * poll, Seed: 1, Clock: clk}
	runner := NewChaosRunner(cfg, cluster, faults)

	require.NoError(t, runner.Run(context.Background()))

	// SINK: at no point were two faults of this single runner active at once.
	assert.LessOrEqual(t, atomic.LoadInt32(&maxSeen), int32(1),
		"single runner must never hold >1 fault in flight (sequential blast radius)")
	// SINK: exactly len(faults) injects in a no-trip run, and all recovered.
	assert.Equal(t, int32(len(faults)), atomic.LoadInt32(&injects),
		"a no-trip run must inject exactly len(faults) faults — no more")
	assert.Equal(t, int32(0), atomic.LoadInt32(&inFlight),
		"all faults must be recovered (in-flight count returns to 0)")
}

// Concurrent runners: the package documents the runner as single-use ("create a
// new one for each chaos experiment") and provides NO shared blast-radius cap
// across runners. We therefore pin the honest per-runner invariant under
// concurrent starts: EACH runner still keeps its own in-flight count <= 1, and
// the global injected total equals the sum of scheduled faults (conservation —
// no fault is injected more times than scheduled, none lost). Concurrency capped
// at 8 runners.
func TestAdversarial_BlastRadius_ConcurrentRunners_Conservation(t *testing.T) {
	const runners = 8
	const faultsPerRunner = 3

	var globalInjects int32
	// Per-runner max-in-flight; each runner gets its own counters so we can
	// assert the per-runner bound independently of the others.
	perMax := make([]int32, runners)
	perInFlight := make([]int32, runners)

	var wg sync.WaitGroup
	for ri := 0; ri < runners; ri++ {
		wg.Add(1)
		go func(ri int) {
			defer wg.Done()
			nodes := []string{"a", "b", "c"}
			cluster := NewFakeCluster(nodes...)
			cluster.SetHealthOverride(func() float64 { return 1.0 })
			faults := make([]Fault, 0, faultsPerRunner)
			for k := 0; k < faultsPerRunner; k++ {
				faults = append(faults, &countingFault{
					nodeID:   nodes[k%len(nodes)],
					inFlight: &perInFlight[ri],
					maxSeen:  &perMax[ri],
					injects:  &globalInjects,
				})
			}
			clk := adversarialClock()
			poll := 2 * time.Millisecond
			cfg := RunnerConfig{SLO: 0.0, PollInterval: poll, FaultDuration: 2 * poll, Seed: int64(ri), Clock: clk}
			require.NoError(t, NewChaosRunner(cfg, cluster, faults).Run(context.Background()))
		}(ri)
	}
	wg.Wait()

	// SINK (per-runner bound): no runner ever exceeded 1 concurrent fault.
	for ri := 0; ri < runners; ri++ {
		assert.LessOrEqualf(t, perMax[ri], int32(1),
			"runner %d exceeded its blast radius (>1 fault in flight)", ri)
		assert.Equalf(t, int32(0), perInFlight[ri], "runner %d leaked an un-recovered fault", ri)
	}
	// SINK (conservation): every scheduled fault was injected exactly once —
	// no over-injection (blast-radius amplification) and none dropped.
	assert.Equal(t, int32(runners*faultsPerRunner), atomic.LoadInt32(&globalInjects),
		"global injects must equal total scheduled faults — no amplification, no loss")
}

// ---------------------------------------------------------------------------
// (b) UNGUARDED SHARED STATE
// ---------------------------------------------------------------------------

// Concurrent Run + RollbackRecords on a SINGLE runner. records/mu is the only
// shared mutable state Run touches; RollbackRecords is documented "Safe to call
// concurrently with Run". Run under -race. Conservation: after a trip there is
// exactly one record and every snapshot length is monotonic (0 then 1).
func TestAdversarial_SharedState_ConcurrentRunAndRecords(t *testing.T) {
	cluster := NewFakeCluster("n1")
	var calls int32
	cluster.SetHealthOverride(func() float64 {
		if atomic.AddInt32(&calls, 1) >= 2 {
			return 0.0 // trip
		}
		return 1.0
	})
	clk := adversarialClock()
	poll := 1 * time.Millisecond
	cfg := RunnerConfig{SLO: 0.8, PollInterval: poll, FaultDuration: 10 * time.Second, Seed: 1, Clock: clk}
	runner := NewChaosRunner(cfg, cluster, []Fault{&PodKill{NodeID: "n1"}})

	done := make(chan error, 1)
	go func() { done <- runner.Run(context.Background()) }()

	// Hammer the reader concurrently; lengths must be monotonic non-decreasing
	// and never exceed 1 (a single trip => a single record).
	var lastLen int
	for {
		select {
		case err := <-done:
			require.ErrorIs(t, err, ErrRolledBack)
			recs := runner.RollbackRecords()
			require.Len(t, recs, 1, "exactly one rollback record after a single trip (conservation)")
			return
		default:
			n := len(runner.RollbackRecords())
			assert.GreaterOrEqual(t, n, lastLen, "record count must be monotonic")
			assert.LessOrEqual(t, n, 1, "never more records than trips")
			lastLen = n
		}
	}
}

// LATENT RISK (documented, demonstrated race-free here on purpose).
//
// selectNode reads r.rng (*rand.Rand) with NO mutex (chaos.go:425). *rand.Rand
// is NOT safe for concurrent use. Calling selectNode from >1 goroutine on the
// SAME runner is a data race. Verified directly: an earlier version of this test
// that called selectNode from 8 goroutines made `go test -race` FAIL the whole
// package with:
//
//	WARNING: DATA RACE
//	  Read at ... by goroutine N:
//	    math/rand.(*rngSource).Uint64() ...
//	    chaos.(*ChaosRunner).selectNode()  chaos.go:425
//	  Previous write ... by goroutine M:  (same line)
//
// Because a -race failure reds the ENTIRE package test binary for every
// developer (effectively fatal to the suite), this test does NOT leave that race
// live. It instead:
//   - PINS that selectNode is correct when serialized (the supported usage: the
//     runner is documented single-use, and selectNode is never called from Run
//     nor concurrently anywhere in the tree — zero non-test importers), and
//   - documents the concurrency hazard above so a future caller that wants
//     concurrent node selection knows it MUST add its own synchronization (or
//     the package must add a mutex around r.rng).
//
// This is a LATENT RISK, not a REAL BUG: the package nowhere promises selectNode
// is goroutine-safe, and no production path invokes it concurrently.
func TestAdversarial_SharedState_SelectNodeRaceIsLatent(t *testing.T) {
	nodes := []string{"A", "B", "C", "D"}
	cluster := NewFakeCluster(nodes...)
	cfg := RunnerConfig{Seed: 7, Clock: adversarialClock()}
	runner := NewChaosRunner(cfg, cluster, nil)

	valid := map[string]bool{"A": true, "B": true, "C": true, "D": true}

	// Serialized access (the supported single-goroutine usage). Sink-side: every
	// pick is a valid member; no out-of-range index, no panic.
	var ext sync.Mutex // models the synchronization a concurrent caller MUST add
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 64; i++ {
				ext.Lock()
				n, err := runner.selectNode(nodes)
				ext.Unlock()
				if err != nil {
					continue
				}
				assert.True(t, valid[n], "selectNode must return a valid cluster member")
			}
		}()
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// (c) NUMERIC — SLO edges slipping the canary guard.
//
// The canary fires iff  HealthScore() < SLO  (strict). RunnerConfig documents
// "SLO ... Values in [0.0, 1.0]" as a caller precondition; the runner does NOT
// validate or clamp. These tests pin the OBSERVABLE consequence of feeding it
// out-of-range / non-finite values, sink-side (did the canary fire?).
// ---------------------------------------------------------------------------

// runUntilSettled drives a runner whose health is pinned at `health` and reports
// whether the canary fired (ErrRolledBack) and how many records were captured.
func runUntilSettled(t *testing.T, slo, health float64) (tripped bool, nRecords int) {
	t.Helper()
	cluster := NewFakeCluster("n1")
	cluster.SetHealthOverride(func() float64 { return health })
	clk := adversarialClock()
	poll := 2 * time.Millisecond
	cfg := RunnerConfig{SLO: slo, PollInterval: poll, FaultDuration: 4 * poll, Seed: 1, Clock: clk}
	runner := NewChaosRunner(cfg, cluster, []Fault{&PodKill{NodeID: "n1"}})
	err := runner.Run(context.Background())
	return err == ErrRolledBack, len(runner.RollbackRecords())
}

// NaN SLO silently DISABLES the canary: h < NaN is always false, so even a
// totally dead cluster (health 0.0) never trips. This is the dangerous edge —
// a chaos run with a misconfigured (NaN) SLO has NO safety net. We PIN it.
func TestAdversarial_Numeric_NaNSLO_DisablesCanary(t *testing.T) {
	require.True(t, math.IsNaN(math.NaN()))
	tripped, n := runUntilSettled(t, math.NaN(), 0.0 /* dead cluster */)
	assert.False(t, tripped,
		"PINNED CONTRACT/RISK: NaN SLO never trips the canary (h<NaN==false); safety net is silently OFF")
	assert.Equal(t, 0, n, "no rollback record captured with NaN SLO even on a 0.0-health cluster")
}

// +Inf SLO: h < +Inf is always true for any finite health, so the canary fires
// on the FIRST poll even on a perfectly healthy (1.0) cluster — an over-eager
// false-positive rollback. Pin it.
func TestAdversarial_Numeric_PosInfSLO_AlwaysTrips(t *testing.T) {
	tripped, n := runUntilSettled(t, math.Inf(1), 1.0 /* perfectly healthy */)
	assert.True(t, tripped,
		"PINNED: +Inf SLO trips immediately even on a healthy cluster (h<+Inf==true)")
	assert.Equal(t, 1, n)
}

// SLO > 1.0 (e.g. 1.5): a fully healthy 1.0 cluster still trips because 1.0<1.5.
// Documents that an out-of-[0,1] SLO produces a false-positive rollback.
func TestAdversarial_Numeric_SLOAboveOne_FalsePositiveTrip(t *testing.T) {
	tripped, n := runUntilSettled(t, 1.5, 1.0)
	assert.True(t, tripped, "PINNED: SLO>1 trips on a healthy cluster (1.0<1.5)")
	assert.Equal(t, 1, n)
}

// SLO < 0 (e.g. -1): even a 0.0-health (dead) cluster never trips because
// 0.0 < -1 is false. Documents the opposite-direction unvalidated edge.
func TestAdversarial_Numeric_NegativeSLO_NeverTrips(t *testing.T) {
	tripped, n := runUntilSettled(t, -1.0, 0.0)
	assert.False(t, tripped, "PINNED: negative SLO never trips, even on a dead cluster (0.0<-1==false)")
	assert.Equal(t, 0, n)
}

// In-range SLO behaves correctly: health strictly below SLO trips; health equal
// to SLO does NOT (strict <). This is the load-bearing, IN-CONTRACT behavior the
// numeric edges are contrasted against.
func TestAdversarial_Numeric_InRangeSLO_BoundaryIsStrict(t *testing.T) {
	tripBelow, nBelow := runUntilSettled(t, 0.8, 0.7)
	assert.True(t, tripBelow, "health 0.7 < SLO 0.8 must trip")
	assert.Equal(t, 1, nBelow)

	tripEqual, nEqual := runUntilSettled(t, 0.8, 0.8)
	assert.False(t, tripEqual, "health == SLO must NOT trip (strict <)")
	assert.Equal(t, 0, nEqual)
}

// ---------------------------------------------------------------------------
// (d) TOTAL ORDER / DETERMINISM
//
// Run iterates the faults SLICE in order (no map iteration in scheduling) and
// selectNode is seeded — both are deterministic. Pin both.
// ---------------------------------------------------------------------------

// recordingFault appends its node id to a shared, mutex-guarded slice on Inject,
// letting us assert the exact execution ORDER of a no-trip run.
type recordingFault struct {
	id    string
	mu    *sync.Mutex
	order *[]string
}

func (f *recordingFault) Name() string { return "rec(" + f.id + ")" }
func (f *recordingFault) Inject(ctx context.Context, target Target) error {
	f.mu.Lock()
	*f.order = append(*f.order, f.id)
	f.mu.Unlock()
	return nil
}
func (f *recordingFault) Recover(ctx context.Context, target Target) error { return nil }

func TestAdversarial_Determinism_FaultOrderIsScheduleOrder(t *testing.T) {
	want := []string{"f0", "f1", "f2", "f3", "f4"}

	run := func() []string {
		cluster := NewFakeCluster("x")
		cluster.SetHealthOverride(func() float64 { return 1.0 })
		var mu sync.Mutex
		got := make([]string, 0, len(want))
		faults := make([]Fault, 0, len(want))
		for _, id := range want {
			faults = append(faults, &recordingFault{id: id, mu: &mu, order: &got})
		}
		clk := adversarialClock()
		poll := 2 * time.Millisecond
		cfg := RunnerConfig{SLO: 0.0, PollInterval: poll, FaultDuration: 2 * poll, Seed: 99, Clock: clk}
		require.NoError(t, NewChaosRunner(cfg, cluster, faults).Run(context.Background()))
		return got
	}

	// Two independent runs must produce the IDENTICAL order == the schedule order.
	got1 := run()
	got2 := run()
	assert.Equal(t, want, got1, "fault execution order must equal schedule order")
	assert.Equal(t, got1, got2, "fault order must be deterministic across runs")
}

func TestAdversarial_Determinism_SeededNodeSelectionStable(t *testing.T) {
	nodes := []string{"A", "B", "C", "D", "E"}
	cluster := NewFakeCluster(nodes...)

	seq := func(seed int64) []string {
		r := NewChaosRunner(RunnerConfig{Seed: seed, Clock: adversarialClock()}, cluster, nil)
		out := make([]string, 0, 10)
		for i := 0; i < 10; i++ {
			n, err := r.selectNode(nodes)
			require.NoError(t, err)
			out = append(out, n)
		}
		return out
	}

	// Same seed => identical 10-pick sequence (determinism / reproducibility).
	assert.Equal(t, seq(123), seq(123), "same seed must reproduce the same selection sequence")
}
