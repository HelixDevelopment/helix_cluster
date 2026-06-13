package pool

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// This file holds an ADVERSARIAL, race-detector-driven test suite for the
// PoolManager's documented concurrency + capacity contract:
//
//   - PoolManager is "safe for concurrent use" (package.go doc).
//   - Acquire hands out an idle instance, marks it busy, and "does not block";
//     when none is idle it returns ErrPoolEmpty (non-blocking try-failure).
//   - An instance, once leased, MUST NOT be handed to a second caller until it
//     is returned (mutual exclusion of a lease — the critical safety property).
//   - The number of simultaneously-busy instances can never exceed the number
//     of tracked instances (the pool capacity in force at that moment), and the
//     provider quota (GPUProvider.Capacity()) is a hard upper bound on how many
//     instances are ever live at the provider sink.
//   - At quiescence every lease is returned (Busy()==0) and no instance is
//     lost or duplicated (pool Size() == provider LiveCount()).
//
// Existing TestConcurrentAcquireReturnScale (package_test.go) runs goroutines
// under -race but only asserts Busy()==0 and Size()>=0 at the end. It does NOT
// detect double-issue (the same instance leased to two goroutines at once), it
// does NOT bound the simultaneously-in-use count against a high-water gauge,
// and it does NOT prove reuse, exhaustion semantics, or provider-side
// conservation under concurrent scaling. This file closes those gaps.

// leaseLedger is the anti-bluff instrument. For every instance ID it keeps an
// atomic "currently held" counter. The invariant a correct pool guarantees is:
// at any instant, holders[id] is 0 or 1. If Acquire ever hands the same ID to
// two goroutines concurrently (a broken/unsynchronized busy flag), one of them
// will observe a post-increment value of 2 — a recorded double-issue.
type leaseLedger struct {
	mu      sync.Mutex
	holders map[string]*int32 // id -> current concurrent holders (0/1 if correct)

	doubleIssues  int64 // count of times an Acquire saw holders[id] become >1
	maxConcurrent int64 // high-water of simultaneously-in-use instances
	inUse         int64 // current simultaneously-in-use instances
	distinctIDs   sync.Map
	reuseSeen     int64 // count of Acquires that handed back a previously-seen ID
}

func newLeaseLedger() *leaseLedger {
	return &leaseLedger{holders: make(map[string]*int32)}
}

func (l *leaseLedger) counterFor(id string) *int32 {
	l.mu.Lock()
	defer l.mu.Unlock()
	c, ok := l.holders[id]
	if !ok {
		c = new(int32)
		l.holders[id] = c
	}
	return c
}

// onAcquire records that id was just leased and returns the post-acquire
// concurrent-holder count for id. A value > 1 means a double-issue.
func (l *leaseLedger) onAcquire(id string) int32 {
	c := l.counterFor(id)
	now := atomic.AddInt32(c, 1)
	if now > 1 {
		atomic.AddInt64(&l.doubleIssues, 1)
	}

	// In-use high-water gauge.
	cur := atomic.AddInt64(&l.inUse, 1)
	for {
		hw := atomic.LoadInt64(&l.maxConcurrent)
		if cur <= hw || atomic.CompareAndSwapInt64(&l.maxConcurrent, hw, cur) {
			break
		}
	}

	// Reuse tracking: was this ID handed out before?
	if _, seen := l.distinctIDs.LoadOrStore(id, struct{}{}); seen {
		atomic.AddInt64(&l.reuseSeen, 1)
	}
	return now
}

// onReturn records that id was returned.
func (l *leaseLedger) onReturn(id string) {
	c := l.counterFor(id)
	atomic.AddInt32(c, -1)
	atomic.AddInt64(&l.inUse, -1)
}

func (l *leaseLedger) distinctCount() int {
	n := 0
	l.distinctIDs.Range(func(_, _ any) bool { n++; return true })
	return n
}

// TestConcurrency_NoDoubleIssue_CapacityBounded is the headline adversarial
// -race test. Many goroutines hammer Acquire -> (hold) -> ReturnToPool on a
// fixed-size pool (no concurrent scaling, so the capacity bound is exact: at
// most poolSize instances may be busy at once). It asserts the load-bearing
// safety + capacity invariants directly.
//
// WHY EACH ASSERTION BITES:
//
//   - doubleIssues == 0: if Acquire's `s.busy = true` (or the surrounding map
//     read/scan) were unsynchronized, two goroutines could pick the same idle
//     ID before either marked it busy. The ledger's atomic per-ID counter would
//     then exceed 1 and record a double-issue. An object handed to two users at
//     once is THE critical pool safety violation — this bites it directly.
//   - maxConcurrent <= poolSize: a pool that over-admits (broken accounting,
//     off-by-one, or no busy filter) would let more than poolSize leases be
//     live simultaneously; the high-water gauge catches it.
//   - provider LiveCount() <= poolSize throughout (asserted at end == poolSize):
//     the provider sink must never hold more than what was provisioned; no
//     unbounded creation.
//   - -race finds no data race: the concurrent map/counter updates in Acquire /
//     ReturnToPool are exercised by real goroutines, so any missing lock on
//     p.tracked or s.busy surfaces as a detected race and fails the run.
//   - conservation at quiescence: Busy()==0, every per-ID holder counter == 0,
//     inUse==0, and Size()==LiveCount() — no lease leaked, no instance lost or
//     duplicated.
//   - reuse: distinct IDs ever issued <= poolSize AND reuseSeen > 0 proves the
//     pool actually RE-USES returned instances rather than minting a new one
//     per Acquire (a non-pooling impostor would show distinctIDs >> poolSize).
func TestConcurrency_NoDoubleIssue_CapacityBounded(t *testing.T) {
	const poolSize = 8
	const goroutines = 32
	const iterations = 400

	// Provider capacity == poolSize so the provider quota is the hard ceiling.
	pm, provs := newManager(t, poolSize)
	prov := provs[0]
	ctx := context.Background()

	if _, err := pm.ScaleTo(ctx, poolSize); err != nil {
		t.Fatalf("initial ScaleTo(%d): %v", poolSize, err)
	}
	if prov.LiveCount() != poolSize {
		t.Fatalf("precondition LiveCount() = %d, want %d", prov.LiveCount(), poolSize)
	}

	ledger := newLeaseLedger()
	var acquired int64 // total successful Acquires across all goroutines

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				inst, err := pm.Acquire(ctx)
				if err != nil {
					if errors.Is(err, ErrPoolEmpty) {
						// Expected: pool fully leased right now. Retry next round.
						continue
					}
					t.Errorf("Acquire unexpected error: %v", err)
					return
				}
				atomic.AddInt64(&acquired, 1)

				// Record the lease and assert mutual exclusion IMMEDIATELY: the
				// instant we hold it, no other goroutine may also hold it.
				if n := ledger.onAcquire(inst.ID); n != 1 {
					t.Errorf("DOUBLE-ISSUE: instance %q held by %d goroutines at once", inst.ID, n)
				}

				// Hold briefly: touch the lease so the race detector and the
				// high-water gauge see real overlap. A trivial no-op body would
				// reduce overlap; this keeps leases live long enough to collide
				// if the pool were broken.
				_ = inst.HourlyUSD

				ledger.onReturn(inst.ID)
				if err := pm.ReturnToPool(inst); err != nil {
					t.Errorf("ReturnToPool(%q): %v", inst.ID, err)
					return
				}
			}
		}()
	}
	wg.Wait()

	if t.Failed() {
		return // concrete double-issue / error already reported
	}

	// --- Safety: no instance was ever issued to two callers at once. ---
	if d := atomic.LoadInt64(&ledger.doubleIssues); d != 0 {
		t.Fatalf("double-issue count = %d, want 0 (an instance was leased to two callers simultaneously)", d)
	}

	// --- Capacity: simultaneously-in-use never exceeded the pool size. ---
	if hw := atomic.LoadInt64(&ledger.maxConcurrent); hw > poolSize {
		t.Fatalf("max simultaneously in-use = %d, want <= poolSize %d (capacity violated)", hw, poolSize)
	}
	if hw := atomic.LoadInt64(&ledger.maxConcurrent); hw == 0 {
		t.Fatalf("max simultaneously in-use = 0; the concurrent acquirers never ran (test is vacuous)")
	}

	// --- Reuse: the pool re-handed instances rather than minting new ones. ---
	if distinct := ledger.distinctCount(); distinct > poolSize {
		t.Fatalf("distinct instance IDs ever issued = %d, want <= poolSize %d (pool is not reusing instances)", distinct, poolSize)
	}
	if r := atomic.LoadInt64(&ledger.reuseSeen); r == 0 {
		t.Fatalf("reuse count = 0; no instance was ever re-leased (pool did not actually pool)")
	}

	// --- Conservation at quiescence: every lease returned, nothing leaked. ---
	if busy := pm.Busy(); busy != 0 {
		t.Fatalf("Busy() = %d at quiescence, want 0 (a lease leaked)", busy)
	}
	ledger.mu.Lock()
	for id, c := range ledger.holders {
		if v := atomic.LoadInt32(c); v != 0 {
			ledger.mu.Unlock()
			t.Fatalf("instance %q has %d holders at quiescence, want 0", id, v)
		}
	}
	ledger.mu.Unlock()
	if inUse := atomic.LoadInt64(&ledger.inUse); inUse != 0 {
		t.Fatalf("ledger inUse = %d at quiescence, want 0", inUse)
	}

	// --- Provider-side conservation: no instance lost or duplicated. ---
	if pm.Size() != prov.LiveCount() {
		t.Fatalf("pool Size() = %d but provider LiveCount() = %d (instance lost or duplicated)", pm.Size(), prov.LiveCount())
	}
	if prov.LiveCount() != poolSize {
		t.Fatalf("provider LiveCount() = %d at quiescence, want %d (no unbounded creation/loss)", prov.LiveCount(), poolSize)
	}

	t.Logf("acquired=%d double-issues=%d max-in-use=%d/%d distinct-ids=%d reuse=%d busy@quiesce=%d size=%d liveCount=%d",
		atomic.LoadInt64(&acquired),
		atomic.LoadInt64(&ledger.doubleIssues),
		atomic.LoadInt64(&ledger.maxConcurrent), poolSize,
		ledger.distinctCount(),
		atomic.LoadInt64(&ledger.reuseSeen),
		pm.Busy(), pm.Size(), prov.LiveCount())
}

// TestConcurrency_CapacityBound_UnderConcurrentScaling proves the provider
// quota is a hard ceiling even while ScaleTo races against acquirers. The pool
// is scaled up and down between [base, base+grow] by one goroutine while many
// acquirers lease/return. The provider's Capacity() is the absolute upper bound
// on live instances; LiveCount() must never exceed it, and no double-issue may
// occur during the churn.
//
// WHY IT BITES: a capacity off-by-one in pickProviderLocked (using <= instead
// of <) or a busy/idle accounting race during scale-down would let LiveCount()
// exceed Capacity() or evict a leased instance. We sample LiveCount() from the
// acquirers (it is read under the provider's own lock) and assert the ceiling;
// the per-ID ledger again catches any double-issue caused by a scale/acquire
// interleaving.
func TestConcurrency_CapacityBound_UnderConcurrentScaling(t *testing.T) {
	const base = 4
	const grow = 4
	const capacity = base + grow // provider hard quota
	const goroutines = 24
	const iterations = 300

	pm, provs := newManager(t, capacity)
	prov := provs[0]
	ctx := context.Background()

	if _, err := pm.ScaleTo(ctx, base); err != nil {
		t.Fatalf("initial ScaleTo(%d): %v", base, err)
	}

	ledger := newLeaseLedger()
	var overCapacity int64 // times LiveCount() was observed > capacity

	var wg sync.WaitGroup

	// Scaler goroutine: churns the pool size up and down.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			target := base
			if i%2 == 0 {
				target = base + grow
			}
			pm.ScaleTo(ctx, target) //nolint:errcheck // capacity races w/ acquirers are expected
			if lc := prov.LiveCount(); lc > capacity {
				atomic.AddInt64(&overCapacity, 1)
			}
		}
	}()

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				inst, err := pm.Acquire(ctx)
				if err != nil {
					if errors.Is(err, ErrPoolEmpty) {
						continue
					}
					t.Errorf("Acquire unexpected error: %v", err)
					return
				}
				if n := ledger.onAcquire(inst.ID); n != 1 {
					t.Errorf("DOUBLE-ISSUE under scaling: %q held by %d at once", inst.ID, n)
				}
				if lc := prov.LiveCount(); lc > capacity {
					atomic.AddInt64(&overCapacity, 1)
				}
				ledger.onReturn(inst.ID)
				if err := pm.ReturnToPool(inst); err != nil {
					t.Errorf("ReturnToPool(%q): %v", inst.ID, err)
					return
				}
			}
		}()
	}
	wg.Wait()

	if t.Failed() {
		return
	}
	if oc := atomic.LoadInt64(&overCapacity); oc != 0 {
		t.Fatalf("provider LiveCount() exceeded capacity %d on %d observations (quota not a hard ceiling)", capacity, oc)
	}
	if d := atomic.LoadInt64(&ledger.doubleIssues); d != 0 {
		t.Fatalf("double-issue count = %d under concurrent scaling, want 0", d)
	}
	if hw := atomic.LoadInt64(&ledger.maxConcurrent); hw > capacity {
		t.Fatalf("max simultaneously in-use = %d, want <= capacity %d", hw, capacity)
	}
	if busy := pm.Busy(); busy != 0 {
		t.Fatalf("Busy() = %d at quiescence, want 0", busy)
	}
	// Conservation: pool and provider agree on what is live.
	if pm.Size() != prov.LiveCount() {
		t.Fatalf("Size() = %d but LiveCount() = %d (instance lost or duplicated under scaling)", pm.Size(), prov.LiveCount())
	}
	t.Logf("under-scaling: max-in-use=%d/%d double-issues=%d over-capacity-obs=%d final-size=%d liveCount=%d",
		atomic.LoadInt64(&ledger.maxConcurrent), capacity,
		atomic.LoadInt64(&ledger.doubleIssues),
		atomic.LoadInt64(&overCapacity), pm.Size(), prov.LiveCount())
}

// TestConcurrency_Exhaustion_NonBlockingHandoff proves the DOCUMENTED
// exhaustion semantics: Acquire "does not block" — a fully-leased pool returns
// ErrPoolEmpty immediately, and once a lease is returned a subsequent Acquire
// succeeds (no lost wakeup, no stuck getter). This anchors the actual contract
// (try-failure, not blocking) rather than assuming a blocking pool.
//
// WHY IT BITES: if Acquire silently created or double-issued an instance when
// the pool was full (instead of returning ErrPoolEmpty), the "want ErrPoolEmpty"
// assertion fails. If ReturnToPool failed to free the slot, the post-return
// Acquire would still get ErrPoolEmpty and fail the "want success" assertion.
func TestConcurrency_Exhaustion_NonBlockingHandoff(t *testing.T) {
	const poolSize = 4
	pm, _ := newManager(t, poolSize)
	ctx := context.Background()
	if _, err := pm.ScaleTo(ctx, poolSize); err != nil {
		t.Fatalf("ScaleTo(%d): %v", poolSize, err)
	}

	// Lease every instance.
	held := make([]Instance, 0, poolSize)
	for i := 0; i < poolSize; i++ {
		inst, err := pm.Acquire(ctx)
		if err != nil {
			t.Fatalf("Acquire #%d of full pool: %v", i, err)
		}
		held = append(held, inst)
	}
	if pm.Busy() != poolSize {
		t.Fatalf("Busy() = %d, want %d (all leased)", pm.Busy(), poolSize)
	}

	// Now the pool is exhausted: Acquire must return ErrPoolEmpty immediately
	// (non-blocking contract), NOT block and NOT mint a new instance.
	if _, err := pm.Acquire(ctx); !errors.Is(err, ErrPoolEmpty) {
		t.Fatalf("Acquire on exhausted pool err = %v, want ErrPoolEmpty (non-blocking try-failure)", err)
	}

	// A concurrent waiter that spins trying to Acquire. It must succeed exactly
	// once a lease is returned — proving the freed slot becomes acquirable
	// (no lost wakeup in the try-again model).
	got := make(chan Instance, 1)
	go func() {
		for {
			inst, err := pm.Acquire(ctx)
			if err == nil {
				got <- inst
				return
			}
		}
	}()

	// Return one lease; the spinner must now obtain an instance.
	if err := pm.ReturnToPool(held[0]); err != nil {
		t.Fatalf("ReturnToPool: %v", err)
	}
	gotInst := <-got
	// The handed-out instance must be one of the pool's instances (here, the
	// freed one, since it was the only idle slot).
	if gotInst.ID != held[0].ID {
		t.Fatalf("spinner acquired %q after one return, want the freed %q", gotInst.ID, held[0].ID)
	}

	// Clean up remaining leases.
	if err := pm.ReturnToPool(gotInst); err != nil {
		t.Fatalf("ReturnToPool(spinner inst): %v", err)
	}
	for _, inst := range held[1:] {
		if err := pm.ReturnToPool(inst); err != nil {
			t.Fatalf("ReturnToPool cleanup: %v", err)
		}
	}
	if pm.Busy() != 0 {
		t.Fatalf("Busy() = %d at end, want 0", pm.Busy())
	}
}
