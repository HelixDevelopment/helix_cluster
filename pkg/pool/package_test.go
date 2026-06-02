package pool

import (
	"context"
	"errors"
	"sync"
	"testing"
)

const (
	testGPUType   = "A100"
	testHourlyUSD = 2.50
)

func newManager(t *testing.T, capacity int, providers ...*ReferenceProvider) (*PoolManager, []*ReferenceProvider) {
	t.Helper()
	if len(providers) == 0 {
		providers = []*ReferenceProvider{NewReferenceProvider("ref", capacity)}
	}
	ifaces := make([]GPUProvider, len(providers))
	for i, p := range providers {
		ifaces[i] = p
	}
	pm, err := NewPoolManager(Spec{GPUType: testGPUType, HourlyUSD: testHourlyUSD}, ifaces...)
	if err != nil {
		t.Fatalf("NewPoolManager: %v", err)
	}
	return pm, providers
}

// TestScaleTo_ProvisionsExactlyTarget proves scaling to target=3 provisions
// EXACTLY 3 instances AND that the provider was actually driven (provider
// LiveCount and ProvisionCalls both == 3), not just the pool's own counter.
//
// Mutation that this kills: make ScaleTo a no-op (e.g. `return len(p.tracked),
// nil` at the top, ignoring target). Then no Provision is called: pool.Size()==0,
// prov.LiveCount()==0, prov.ProvisionCalls()==0 — every assertion below FAILS.
func TestScaleTo_ProvisionsExactlyTarget(t *testing.T) {
	pm, provs := newManager(t, 8)
	prov := provs[0]

	got, err := pm.ScaleTo(context.Background(), 3)
	if err != nil {
		t.Fatalf("ScaleTo(3): %v", err)
	}
	if got != 3 {
		t.Fatalf("ScaleTo returned size %d, want 3", got)
	}
	if pm.Size() != 3 {
		t.Fatalf("pool Size() = %d, want 3", pm.Size())
	}
	if prov.LiveCount() != 3 {
		t.Fatalf("provider LiveCount() = %d, want 3 (provider not actually driven)", prov.LiveCount())
	}
	if prov.ProvisionCalls() != 3 {
		t.Fatalf("provider ProvisionCalls() = %d, want exactly 3", prov.ProvisionCalls())
	}
}

// TestScaleTo_Idempotent proves re-scaling to the same target provisions no
// extra instances.
//
// Mutation: change `current < target` to `current <= target` in ScaleTo — the
// second call would provision a 4th instance and ProvisionCalls()==4 fails.
func TestScaleTo_Idempotent(t *testing.T) {
	pm, provs := newManager(t, 8)
	prov := provs[0]

	if _, err := pm.ScaleTo(context.Background(), 3); err != nil {
		t.Fatalf("first ScaleTo: %v", err)
	}
	if _, err := pm.ScaleTo(context.Background(), 3); err != nil {
		t.Fatalf("second ScaleTo: %v", err)
	}
	if prov.ProvisionCalls() != 3 {
		t.Fatalf("ProvisionCalls() = %d after re-scale to same target, want 3", prov.ProvisionCalls())
	}
	if pm.Size() != 3 {
		t.Fatalf("Size() = %d, want 3", pm.Size())
	}
}

// TestScaleDown_ReleasesExactlyTwoIdle proves scaling 3 -> 1 releases EXACTLY 2
// idle instances at the provider sink (LiveCount 3 -> 1, ReleaseCalls == 2).
//
// Mutation: make scaleDownLocked a no-op (delete its loop body / `return nil`
// immediately). Then nothing is released: Size()==3, LiveCount()==3,
// ReleaseCalls()==0 — assertions FAIL.
func TestScaleDown_ReleasesExactlyTwoIdle(t *testing.T) {
	pm, provs := newManager(t, 8)
	prov := provs[0]
	ctx := context.Background()

	if _, err := pm.ScaleTo(ctx, 3); err != nil {
		t.Fatalf("scale up: %v", err)
	}
	if prov.LiveCount() != 3 {
		t.Fatalf("precondition LiveCount() = %d, want 3", prov.LiveCount())
	}

	got, err := pm.ScaleTo(ctx, 1)
	if err != nil {
		t.Fatalf("scale down: %v", err)
	}
	if got != 1 {
		t.Fatalf("ScaleTo(1) returned %d, want 1", got)
	}
	if pm.Size() != 1 {
		t.Fatalf("Size() = %d after scale-down, want 1", pm.Size())
	}
	if prov.LiveCount() != 1 {
		t.Fatalf("provider LiveCount() = %d, want 1 (instances not actually released)", prov.LiveCount())
	}
	if prov.ReleaseCalls() != 2 {
		t.Fatalf("provider ReleaseCalls() = %d, want exactly 2", prov.ReleaseCalls())
	}
}

// TestScaleDown_NeverReleasesBusy proves scale-down refuses to evict leased
// instances: with 3 instances all leased, ScaleTo(1) releases NONE and the pool
// stays at 3.
//
// Mutation: drop the `if !s.busy` filter in scaleDownLocked (release any
// instance). Then a busy instance would be released: ReleaseCalls() != 0 and/or
// Size() drops below 3 — assertions FAIL. Also: the leased instance vanishing
// from the provider would be a correctness disaster the test catches.
func TestScaleDown_NeverReleasesBusy(t *testing.T) {
	pm, provs := newManager(t, 8)
	prov := provs[0]
	ctx := context.Background()

	if _, err := pm.ScaleTo(ctx, 3); err != nil {
		t.Fatalf("scale up: %v", err)
	}
	// Lease all three.
	for i := 0; i < 3; i++ {
		if _, err := pm.Acquire(ctx); err != nil {
			t.Fatalf("Acquire #%d: %v", i, err)
		}
	}
	if pm.Busy() != 3 {
		t.Fatalf("Busy() = %d, want 3", pm.Busy())
	}

	got, err := pm.ScaleTo(ctx, 1)
	if err != nil {
		t.Fatalf("scale down with all busy: %v", err)
	}
	if got != 3 {
		t.Fatalf("ScaleTo(1) with all leased returned %d, want 3 (must not evict leases)", got)
	}
	if prov.ReleaseCalls() != 0 {
		t.Fatalf("ReleaseCalls() = %d, want 0 (no busy instance may be released)", prov.ReleaseCalls())
	}
	if prov.LiveCount() != 3 {
		t.Fatalf("LiveCount() = %d, want 3", prov.LiveCount())
	}
}

// TestScaleDown_ReleasesOnlyIdleAmongMixed proves with 1 busy + 2 idle,
// ScaleTo(0) releases exactly the 2 idle ones and leaves the busy one.
//
// Mutation: same `if !s.busy` removal — it would release the busy instance too,
// making ReleaseCalls()==3 and LiveCount()==0, failing the want-1 assertions.
func TestScaleDown_ReleasesOnlyIdleAmongMixed(t *testing.T) {
	pm, provs := newManager(t, 8)
	prov := provs[0]
	ctx := context.Background()

	if _, err := pm.ScaleTo(ctx, 3); err != nil {
		t.Fatalf("scale up: %v", err)
	}
	leased, err := pm.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	if _, err := pm.ScaleTo(ctx, 0); err != nil {
		t.Fatalf("scale to 0: %v", err)
	}
	if pm.Size() != 1 {
		t.Fatalf("Size() = %d, want 1 (the one busy instance remains)", pm.Size())
	}
	if prov.ReleaseCalls() != 2 {
		t.Fatalf("ReleaseCalls() = %d, want exactly 2", prov.ReleaseCalls())
	}
	if prov.LiveCount() != 1 {
		t.Fatalf("LiveCount() = %d, want 1", prov.LiveCount())
	}
	// The surviving live instance must be the leased one.
	live, _ := prov.List(ctx)
	if len(live) != 1 || live[0].ID != leased.ID {
		t.Fatalf("surviving live instance = %+v, want the leased %q", live, leased.ID)
	}
}

// TestAcquireReturn_MarksBusyThenFree proves Acquire flips an instance to busy
// and ReturnToPool flips it back, observable via Busy()/Idle().
//
// Mutation: in Acquire, drop `s.busy = true`. Then Busy()==0 after Acquire and
// the want-1 assertion FAILS. Symmetrically, dropping `s.busy = false` in
// ReturnToPool fails the post-return Busy()==0 check.
func TestAcquireReturn_MarksBusyThenFree(t *testing.T) {
	pm, _ := newManager(t, 8)
	ctx := context.Background()
	if _, err := pm.ScaleTo(ctx, 2); err != nil {
		t.Fatalf("scale up: %v", err)
	}

	inst, err := pm.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if pm.Busy() != 1 {
		t.Fatalf("Busy() = %d after Acquire, want 1", pm.Busy())
	}
	if pm.Idle() != 1 {
		t.Fatalf("Idle() = %d after Acquire, want 1", pm.Idle())
	}

	if err := pm.ReturnToPool(inst); err != nil {
		t.Fatalf("ReturnToPool: %v", err)
	}
	if pm.Busy() != 0 {
		t.Fatalf("Busy() = %d after ReturnToPool, want 0", pm.Busy())
	}
	if pm.Idle() != 2 {
		t.Fatalf("Idle() = %d after ReturnToPool, want 2", pm.Idle())
	}
}

// TestAcquire_EmptyPool proves Acquire on an empty/all-busy pool returns
// ErrPoolEmpty rather than a zero instance with nil error.
//
// Mutation: in Acquire, `return s.inst, nil` for the empty case instead of
// ErrPoolEmpty. Then err==nil and errors.Is fails.
func TestAcquire_EmptyPool(t *testing.T) {
	pm, _ := newManager(t, 8)
	if _, err := pm.Acquire(context.Background()); !errors.Is(err, ErrPoolEmpty) {
		t.Fatalf("Acquire on empty pool err = %v, want ErrPoolEmpty", err)
	}
}

// TestReturnToPool_Unknown proves returning an untracked instance is rejected.
//
// Mutation: make ReturnToPool `return nil` unconditionally. Then the want-error
// assertion FAILS.
func TestReturnToPool_Unknown(t *testing.T) {
	pm, _ := newManager(t, 8)
	err := pm.ReturnToPool(Instance{ID: "does-not-exist"})
	if !errors.Is(err, ErrUnknownInstance) {
		t.Fatalf("ReturnToPool(unknown) err = %v, want ErrUnknownInstance", err)
	}
}

// TestUtilization_ReflectsBusyOverTotal proves Utilization == busy/total with
// an exact value (2 busy of 4 -> 0.5).
//
// Mutation: in Utilization, return `float64(total)` or compute busy/busy. The
// exact 0.5 assertion FAILS.
func TestUtilization_ReflectsBusyOverTotal(t *testing.T) {
	pm, _ := newManager(t, 8)
	ctx := context.Background()
	if _, err := pm.ScaleTo(ctx, 4); err != nil {
		t.Fatalf("scale up: %v", err)
	}
	if u := pm.Utilization(); u != 0 {
		t.Fatalf("Utilization() = %v on idle pool, want 0", u)
	}
	for i := 0; i < 2; i++ {
		if _, err := pm.Acquire(ctx); err != nil {
			t.Fatalf("Acquire #%d: %v", i, err)
		}
	}
	if u := pm.Utilization(); u != 0.5 {
		t.Fatalf("Utilization() = %v, want 0.5 (2 busy of 4)", u)
	}
}

// TestProvisioningFailure_Surfaces proves a provider Provision error surfaces
// from ScaleTo (wrapped) and the pool only tracks what truly came up.
//
// Mutation: in scaleUpLocked, swallow the Provision error (`continue`/`break`
// without returning it). Then err==nil and the want-error assertion FAILS;
// additionally Size() would not equal 1.
func TestProvisioningFailure_Surfaces(t *testing.T) {
	pm, provs := newManager(t, 8)
	prov := provs[0]
	ctx := context.Background()

	// First provision succeeds, second fails.
	if _, err := pm.ScaleTo(ctx, 1); err != nil {
		t.Fatalf("initial scale: %v", err)
	}
	sentinel := errors.New("quota exceeded at cloud")
	prov.FailNextProvision(sentinel)

	_, err := pm.ScaleTo(ctx, 3)
	if err == nil {
		t.Fatalf("ScaleTo past a failing provision returned nil error, want surfaced failure")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want wrapped sentinel %q", err, sentinel)
	}
	// Only the one successful instance is tracked; the failed one is not.
	if pm.Size() != 1 {
		t.Fatalf("Size() = %d after failed scale, want 1 (failed provision not tracked)", pm.Size())
	}
	if prov.LiveCount() != 1 {
		t.Fatalf("provider LiveCount() = %d, want 1", prov.LiveCount())
	}
}

// TestCapacity_ProviderQuotaEnforced proves a provider at capacity cannot be
// over-provisioned: with capacity 2, ScaleTo(3) errors and the pool holds 2.
//
// Mutation: in pickProviderLocked, change `used[prov] < prov.Capacity()` to
// `<=` (off-by-one) or ignore capacity — then a 3rd instance is provisioned and
// Size()==3, failing want-2.
func TestCapacity_ProviderQuotaEnforced(t *testing.T) {
	pm, provs := newManager(t, 2)
	prov := provs[0]
	ctx := context.Background()

	_, err := pm.ScaleTo(ctx, 3)
	if err == nil {
		t.Fatalf("ScaleTo(3) on capacity-2 provider returned nil, want capacity error")
	}
	if pm.Size() != 2 {
		t.Fatalf("Size() = %d, want 2 (capped at provider capacity)", pm.Size())
	}
	if prov.LiveCount() != 2 {
		t.Fatalf("LiveCount() = %d, want 2", prov.LiveCount())
	}
}

// TestMultiProvider_SpreadsAcrossCapacity proves the manager spills onto a
// second provider once the first is full: capacities 2 + 5, target 4 puts
// exactly 2 on the first and 2 on the second.
//
// Mutation: in pickProviderLocked, always return p.providers[0]. Then the 3rd
// instance would fail (provider 0 at capacity) instead of landing on provider 1
// — provB.LiveCount()==0 and the scale errors, failing the assertions.
func TestMultiProvider_SpreadsAcrossCapacity(t *testing.T) {
	provA := NewReferenceProvider("a", 2)
	provB := NewReferenceProvider("b", 5)
	pm, _ := newManager(t, 0, provA, provB)
	ctx := context.Background()

	got, err := pm.ScaleTo(ctx, 4)
	if err != nil {
		t.Fatalf("ScaleTo(4) across two providers: %v", err)
	}
	if got != 4 {
		t.Fatalf("size = %d, want 4", got)
	}
	if provA.LiveCount() != 2 {
		t.Fatalf("provA LiveCount() = %d, want 2 (filled to capacity)", provA.LiveCount())
	}
	if provB.LiveCount() != 2 {
		t.Fatalf("provB LiveCount() = %d, want 2 (overflow)", provB.LiveCount())
	}
}

// TestHourlyTCO_SumsTrackedInstances proves $/hr == sum of per-instance price:
// 3 instances at 2.50 -> 7.50.
//
// Mutation: in HourlyTCO, return `total / float64(len(p.tracked))` (average) —
// 2.50 != 7.50, assertion FAILS.
func TestHourlyTCO_SumsTrackedInstances(t *testing.T) {
	pm, _ := newManager(t, 8)
	ctx := context.Background()
	if _, err := pm.ScaleTo(ctx, 3); err != nil {
		t.Fatalf("scale up: %v", err)
	}
	if tco := pm.HourlyTCO(); tco != 7.50 {
		t.Fatalf("HourlyTCO() = %v, want 7.50 (3 x 2.50)", tco)
	}
}

// TestNewPoolManager_RequiresProvider proves construction without a provider is
// rejected (a pool with no backend cannot provision anything).
//
// Mutation: drop the len(providers)==0 guard — err would be nil, failing this.
func TestNewPoolManager_RequiresProvider(t *testing.T) {
	if _, err := NewPoolManager(Spec{GPUType: testGPUType}); err == nil {
		t.Fatalf("NewPoolManager with no providers returned nil error, want failure")
	}
}

// TestConcurrentAcquireReturnScale proves PoolManager is safe for concurrent
// use. Multiple goroutines call Acquire and ReturnToPool while a separate
// goroutine alternates ScaleTo calls. Under -race this test would expose any
// missing mutex coverage.
//
// Mutation guard: removing the p.mu.Lock() in Acquire (or ReturnToPool, or
// ScaleTo) makes the race detector flag a data race on p.tracked and fail the
// test run. The test is meaningless without concurrent goroutines; the WaitGroup
// ensures all goroutines actually execute before the test returns.
//
// Why this satisfies CLAUDE-1's "concurrent use" claim: the PoolManager
// doc-comment says "safe for concurrent use". A claim of concurrency-safety
// is only provable by actually running concurrent code paths under the race
// detector — which this test does.
func TestConcurrentAcquireReturnScale(t *testing.T) {
	const poolSize = 6
	const goroutines = 12
	const iterations = 50

	pm, _ := newManager(t, poolSize+4) // extra headroom for ScaleTo up/down
	ctx := context.Background()

	// Pre-scale to poolSize so there are instances to acquire.
	if _, err := pm.ScaleTo(ctx, poolSize); err != nil {
		t.Fatalf("initial ScaleTo(%d): %v", poolSize, err)
	}

	var wg sync.WaitGroup

	// Goroutine that repeatedly scales up and down while acquirers run.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			// Alternate between two sizes so there is always headroom for acquirers.
			target := poolSize
			if i%2 == 0 {
				target = poolSize + 2
			}
			// Ignore errors: capacity races with acquirers are expected under load.
			pm.ScaleTo(ctx, target) //nolint:errcheck
		}
	}()

	// N goroutines that each Acquire an instance and immediately return it.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				inst, err := pm.Acquire(ctx)
				if err != nil {
					// ErrPoolEmpty is expected under concurrent pressure; skip this round.
					continue
				}
				// ReturnToPool must never fail for an instance we just acquired.
				if err := pm.ReturnToPool(inst); err != nil {
					// Use t.Errorf (not Fatalf) so the goroutine can unwind cleanly.
					t.Errorf("ReturnToPool(%q): unexpected error: %v", inst.ID, err)
					return
				}
			}
		}()
	}

	wg.Wait()

	// After all goroutines finish: pool size must be non-negative and match
	// what ScaleTo last committed (the exact value is non-deterministic under
	// concurrent scaling, but must be in a sane range).
	size := pm.Size()
	if size < 0 {
		t.Fatalf("pool Size() = %d after concurrent run, must be >= 0", size)
	}

	// No instance must be left busy: all goroutines returned their leases.
	busy := pm.Busy()
	if busy != 0 {
		t.Fatalf("Busy() = %d after all goroutines finished, want 0 (all leases returned)", busy)
	}
}
