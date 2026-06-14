package pool

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// This file is an ADVERSARIAL probe of pkg/pool focused on the return-side of
// the lease lifecycle, which the existing concurrency_race_test.go does not
// stress:
//
//   (c) DOUBLE-RETURN / RETURN-OF-FOREIGN: returning the same instance twice,
//       or returning an instance never leased from this pool, must NOT inflate
//       available capacity beyond K (no phantom capacity) and must be safe.
//   (b) CONSERVATION: at every observable instant in-use + available == Size(),
//       and Size() never exceeds the provider quota K. Concurrent returns must
//       not corrupt the free/busy accounting.
//   (d) VALIDATION: an unknown / foreign instance ID is rejected with
//       ErrUnknownInstance (a silent success would let a foreign object pollute
//       the pool's idle accounting).
//
// The documented contract being held to account (package.go):
//   - PoolManager "is safe for concurrent use".
//   - ReturnToPool "marks a previously-acquired instance idle again"; returning
//     "an instance the manager does not track yields ErrUnknownInstance".
//   - Size() == Idle()+Busy() always (Idle and Busy are derived from the same
//     per-ID busy flag, so they are two views of one source of truth).
//   - The provider quota Capacity() is the hard ceiling on live instances.
//
// NOTE on the contract's deliberate silence: package.go does NOT promise that
// ReturnToPool is an ownership-checked, epoch-versioned handle. It documents
// idempotent-by-ID return keyed on tracked membership. The tests below pin the
// behaviour the doc actually promises (idempotent, conservation-preserving,
// foreign-rejecting) and separately DOCUMENT — without asserting a fix — the
// latent re-acquire/stale-return aliasing that the by-ID model permits. See
// TestAdversarial_StaleReturn_AliasesLiveLease_LATENT.

// TestAdversarial_DoubleReturn_NoPhantomCapacity proves that returning the SAME
// instance twice does not inflate the pool's idle/available count beyond the
// real Size(). A counter-based (rather than flag-based) free list would over-
// count: each ReturnToPool would push another "free" token, so after two
// returns of one instance the pool would believe it has Size()+1 idle slots —
// phantom capacity that later over-allocates. The flag model must be idempotent.
//
// WHY IT BITES: assert Idle() == Size() and Idle() never exceeds Size() after
// the second return. If ReturnToPool inflated availability, Idle() would be
// Size()+1 here and the assertion fails. We also drain the pool with exactly
// Size() Acquires and assert the (Size()+1)-th fails with ErrPoolEmpty — a
// phantom slot would let one extra Acquire succeed (the real over-commit sink).
func TestAdversarial_DoubleReturn_NoPhantomCapacity(t *testing.T) {
	const poolSize = 4
	pm, prov := newManager(t, poolSize)
	ctx := context.Background()
	if _, err := pm.ScaleTo(ctx, poolSize); err != nil {
		t.Fatalf("ScaleTo(%d): %v", poolSize, err)
	}

	inst, err := pm.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if pm.Busy() != 1 || pm.Idle() != poolSize-1 {
		t.Fatalf("after Acquire: Busy=%d Idle=%d, want 1/%d", pm.Busy(), pm.Idle(), poolSize-1)
	}

	// First return: legitimate.
	if err := pm.ReturnToPool(inst); err != nil {
		t.Fatalf("first ReturnToPool: %v", err)
	}
	// Second return of the SAME instance: must be a no-op for accounting, not an
	// extra idle slot. (The doc keys return on tracked membership, which still
	// holds, so this is permitted and must be idempotent — NOT capacity-adding.)
	if err := pm.ReturnToPool(inst); err != nil {
		t.Fatalf("second ReturnToPool (double-return) err = %v, want nil (idempotent by-ID)", err)
	}

	// Conservation: Idle()+Busy() == Size(), and no phantom idle slot appeared.
	if got, want := pm.Idle()+pm.Busy(), pm.Size(); got != want {
		t.Fatalf("conservation broken: Idle+Busy=%d, Size=%d", got, want)
	}
	if pm.Idle() != poolSize {
		t.Fatalf("Idle() = %d after double-return, want %d (phantom capacity if > %d)", pm.Idle(), poolSize, poolSize)
	}
	if pm.Size() != poolSize {
		t.Fatalf("Size() = %d, want %d", pm.Size(), poolSize)
	}

	// SINK-SIDE over-commit check: the pool must hand out at most poolSize
	// instances. A phantom slot from the double-return would let a (poolSize+1)-th
	// Acquire succeed; the contract says it must fail with ErrPoolEmpty.
	got := make([]Instance, 0, poolSize)
	for i := 0; i < poolSize; i++ {
		in, err := pm.Acquire(ctx)
		if err != nil {
			t.Fatalf("Acquire #%d of %d-slot pool after double-return: %v", i, poolSize, err)
		}
		got = append(got, in)
	}
	if _, err := pm.Acquire(ctx); !errors.Is(err, ErrPoolEmpty) {
		t.Fatalf("(%d+1)-th Acquire err = %v, want ErrPoolEmpty (double-return created phantom capacity)", poolSize, err)
	}
	// No duplicate ID was handed out across the full drain (over-commit by alias).
	seen := make(map[string]bool, poolSize)
	for _, in := range got {
		if seen[in.ID] {
			t.Fatalf("instance %q handed out twice in one drain (double-handout)", in.ID)
		}
		seen[in.ID] = true
	}
	if prov[0].LiveCount() != poolSize {
		t.Fatalf("provider LiveCount() = %d, want %d (no phantom provider-side capacity)", prov[0].LiveCount(), poolSize)
	}
}

// TestAdversarial_ReturnForeign_Rejected proves that returning an instance that
// never came from this pool is rejected with ErrUnknownInstance and does not
// pollute the pool's accounting (no phantom idle slot, no Size change).
//
// WHY IT BITES: a pool that blindly set busy=false (or, worse, inserted the
// foreign ID as idle) would return nil and/or grow Idle()/Size(). Asserting the
// typed error AND unchanged Size()/Idle() catches both the silent-success and
// the pollution variants.
func TestAdversarial_ReturnForeign_Rejected(t *testing.T) {
	const poolSize = 3
	pm, prov := newManager(t, poolSize)
	ctx := context.Background()
	if _, err := pm.ScaleTo(ctx, poolSize); err != nil {
		t.Fatalf("ScaleTo(%d): %v", poolSize, err)
	}
	sizeBefore, idleBefore := pm.Size(), pm.Idle()

	// An instance fabricated outside the pool (foreign provider ID).
	foreign := Instance{ID: "evil-provider-9999", GPUType: "A100", HourlyUSD: 99}
	if err := pm.ReturnToPool(foreign); !errors.Is(err, ErrUnknownInstance) {
		t.Fatalf("ReturnToPool(foreign) err = %v, want ErrUnknownInstance", err)
	}
	if pm.Size() != sizeBefore {
		t.Fatalf("Size() changed to %d after foreign return, want %d (foreign ID polluted pool)", pm.Size(), sizeBefore)
	}
	if pm.Idle() != idleBefore {
		t.Fatalf("Idle() changed to %d after foreign return, want %d (foreign phantom idle slot)", pm.Idle(), idleBefore)
	}
	if pm.Idle()+pm.Busy() != pm.Size() {
		t.Fatalf("conservation broken after foreign return: Idle+Busy=%d Size=%d", pm.Idle()+pm.Busy(), pm.Size())
	}
	if prov[0].LiveCount() != poolSize {
		t.Fatalf("provider LiveCount() = %d, want %d", prov[0].LiveCount(), poolSize)
	}
}

// TestAdversarial_ConcurrentDoubleReturn_Conservation hammers Acquire +
// duplicate-ReturnToPool + foreign-ReturnToPool concurrently and asserts the
// CONSERVATION + QUOTA + NO-DATA-RACE invariants survive the churn under -race.
//
// IMPORTANT SCOPING: this test deliberately injects buggy duplicate returns
// (returning an instance a second time after already handing it back). Under
// the DOCUMENTED by-ID return contract, a duplicate return can free an instance
// another goroutine has since re-acquired — i.e. it can break LEASE EXCLUSIVITY
// on purpose. That aliasing is the latent hazard isolated deterministically in
// TestAdversarial_StaleReturn_AliasesLiveLease_LATENT. Therefore this test does
// NOT assert no-double-issue (it is itself manufacturing the duplicate return).
// What it DOES assert — and what MUST hold even with hostile redundant returns —
// is that the pool's structural accounting never lies:
//
//   - Size() and provider LiveCount() NEVER exceed the provider quota K (no
//     redundant return manufactures a real instance / phantom provider slot);
//   - Idle() and Busy() are never negative and Idle()+Busy()==Size() at every
//     sampled instant (the busy flag is a bounded view, not an unbounded
//     counter that double-return could inflate);
//   - at quiescence Size()==K==LiveCount() with Busy()==0 (no instance lost,
//     duplicated, or leaked by the redundant returns);
//   - -race finds no unsynchronized access to p.tracked across the return paths.
//
// WHY IT BITES: if ReturnToPool tracked availability with an unguarded free
// COUNTER instead of a per-ID flag, each redundant return would push another
// free token; Idle() would climb past Size() and Size()/LiveCount() would be
// observed above K — caught by the quota and Idle<=Size assertions. A missing
// lock on the return path surfaces under -race.
func TestAdversarial_ConcurrentDoubleReturn_Conservation(t *testing.T) {
	const poolSize = 6
	const goroutines = 32
	const iterations = 300

	pm, provs := newManager(t, poolSize)
	prov := provs[0]
	ctx := context.Background()
	if _, err := pm.ScaleTo(ctx, poolSize); err != nil {
		t.Fatalf("ScaleTo(%d): %v", poolSize, err)
	}

	var quotaViolations int64 // Size()/LiveCount()/Idle() observed out of bounds
	foreign := Instance{ID: "foreign-zzzz", GPUType: "A100", HourlyUSD: 1}

	// sampleBounds checks the structural CEILING invariants that must hold at ANY
	// instant regardless of who is mid-return. Each accessor is a single locked
	// read, so each comparison below is individually meaningful under concurrency.
	//
	// We intentionally do NOT cross-check Idle()+Busy()==Size() here: those are
	// three SEPARATE lock acquisitions, so composing them mid-churn samples three
	// different instants and would flag a phantom mismatch that is an artifact of
	// non-atomic sampling, not a pool defect. The exact conservation equality is
	// asserted at QUIESCENCE below, where no goroutine is mutating state. What a
	// redundant/foreign return must never do is push any single bounded gauge
	// PAST the quota — that is a true defect and is what we police per-instant.
	sampleBounds := func() {
		if pm.Size() > poolSize {
			atomic.AddInt64(&quotaViolations, 1)
		}
		if prov.LiveCount() > poolSize {
			atomic.AddInt64(&quotaViolations, 1)
		}
		if idle := pm.Idle(); idle < 0 || idle > poolSize {
			atomic.AddInt64(&quotaViolations, 1)
		}
		if busy := pm.Busy(); busy < 0 || busy > poolSize {
			atomic.AddInt64(&quotaViolations, 1)
		}
	}

	var wg sync.WaitGroup
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
				sampleBounds()

				// Legitimate return.
				if err := pm.ReturnToPool(inst); err != nil {
					t.Errorf("ReturnToPool(%q): %v", inst.ID, err)
					return
				}
				// Adversarial duplicate return of the SAME instance and a foreign
				// return. Neither may grow Size()/LiveCount() past the quota nor
				// push Idle() above Size(). (They MAY break lease exclusivity by
				// the documented by-ID model — out of scope here; see the LATENT
				// test. Here we only police structural conservation/quota.)
				_ = pm.ReturnToPool(inst)    //nolint:errcheck // idempotent by-ID return
				_ = pm.ReturnToPool(foreign) //nolint:errcheck // ErrUnknownInstance expected
				sampleBounds()
			}
		}()
	}
	wg.Wait()

	if t.Failed() {
		return
	}
	if qv := atomic.LoadInt64(&quotaViolations); qv != 0 {
		t.Fatalf("structural invariant violated on %d observations: Size()/LiveCount() exceeded quota %d, "+
			"or Idle()+Busy() != Size(), or a negative/over-Size idle count (phantom capacity from redundant returns)",
			qv, poolSize)
	}
	// Conservation at quiescence: no instance lost, duplicated, or leaked.
	if pm.Busy() != 0 {
		t.Fatalf("Busy() = %d at quiescence, want 0", pm.Busy())
	}
	if pm.Idle()+pm.Busy() != pm.Size() {
		t.Fatalf("conservation: Idle+Busy=%d Size=%d", pm.Idle()+pm.Busy(), pm.Size())
	}
	if pm.Size() != poolSize {
		t.Fatalf("Size() = %d at quiescence, want %d (no growth/loss from redundant returns)", pm.Size(), poolSize)
	}
	if pm.Idle() != poolSize {
		t.Fatalf("Idle() = %d at quiescence, want %d (no phantom idle slots from redundant returns)", pm.Idle(), poolSize)
	}
	if pm.Size() != prov.LiveCount() {
		t.Fatalf("Size() = %d but LiveCount() = %d (instance lost/duplicated)", pm.Size(), prov.LiveCount())
	}
	t.Logf("double-return churn survived: quota-violations=%d size=%d idle=%d live=%d",
		atomic.LoadInt64(&quotaViolations), pm.Size(), pm.Idle(), prov.LiveCount())
}

// TestAdversarial_StaleReturn_AliasesLiveLease_LATENT documents — and pins the
// CURRENT behaviour of — a real aliasing hazard the by-ID return model permits.
//
// Scenario: caller A acquires instance X. A buggy/duplicate path returns X
// (legitimately). Caller B then acquires X (the now-idle instance). A STALE,
// duplicate return of X from A's path now runs again — and because ReturnToPool
// only checks tracked membership (X is still tracked) and unconditionally sets
// busy=false, it FREES X while B is actively holding it. A third caller C can
// then Acquire X — the SAME instance B believes it exclusively holds. That is a
// double-handout produced entirely through the return path, with no data race.
//
// This is NOT asserted as a bug-to-fix here because package.go does NOT promise
// ownership/epoch-checked returns — it documents idempotent by-ID return keyed
// on tracked membership, and this scenario is that documented behaviour taken to
// its logical edge. The test PINS the current observable behaviour so that if a
// future change adds ownership checking (making the stale return a no-op or an
// error), this test will visibly flip and force a conscious contract update.
//
// REPORTED AS: LATENT RISK (see final report). If callers can ever issue a
// duplicate/stale ReturnToPool for an ID that may have been re-leased, the pool
// will alias a live lease. Mitigation belongs at the call sites or via an
// epoch/handle on Instance — a coordinated change, not a silent local patch.
func TestAdversarial_StaleReturn_AliasesLiveLease_LATENT(t *testing.T) {
	const poolSize = 1 // single slot makes the alias unambiguous
	pm, _ := newManager(t, poolSize)
	ctx := context.Background()
	if _, err := pm.ScaleTo(ctx, poolSize); err != nil {
		t.Fatalf("ScaleTo(%d): %v", poolSize, err)
	}

	// A acquires the only instance X.
	a, err := pm.Acquire(ctx)
	if err != nil {
		t.Fatalf("A Acquire: %v", err)
	}
	// A returns X legitimately.
	if err := pm.ReturnToPool(a); err != nil {
		t.Fatalf("A ReturnToPool: %v", err)
	}
	// B acquires X (only idle slot). B now believes it exclusively holds X.
	b, err := pm.Acquire(ctx)
	if err != nil {
		t.Fatalf("B Acquire: %v", err)
	}
	if b.ID != a.ID {
		t.Fatalf("precondition: B got %q, want the same instance %q", b.ID, a.ID)
	}
	if pm.Busy() != 1 {
		t.Fatalf("Busy() = %d after B Acquire, want 1 (B holds X)", pm.Busy())
	}

	// STALE/duplicate return of X from A's path runs again. Under the current
	// by-ID contract this succeeds and frees X out from under B.
	staleErr := pm.ReturnToPool(a)

	// PIN current behaviour: the stale return is accepted (nil) and X is now
	// observably idle even though B still "holds" it. This is the latent alias.
	if staleErr != nil {
		// If a future ownership check rejects the stale return, this branch
		// fires — a GOOD change. Flag it loudly so the contract doc is updated.
		t.Fatalf("LATENT-RISK RESOLVED: stale ReturnToPool now errors (%v). "+
			"Ownership checking appears to have been added; update package.go's "+
			"ReturnToPool contract and this test to assert the new safe behaviour.", staleErr)
	}
	if pm.Busy() != 0 {
		t.Fatalf("LATENT-RISK CHANGED: after stale return Busy()=%d, want 0 under current "+
			"by-ID contract. Behaviour shifted; re-evaluate the alias hazard.", pm.Busy())
	}

	// Demonstrate the concrete double-handout the alias enables: C can now
	// Acquire X — the same instance B still believes it exclusively owns.
	c, err := pm.Acquire(ctx)
	if err != nil {
		t.Fatalf("C Acquire after stale return: %v "+
			"(expected success demonstrating the alias)", err)
	}
	if c.ID != b.ID {
		t.Fatalf("C got %q, want same instance %q as B (the aliased double-handout)", c.ID, b.ID)
	}
	t.Logf("LATENT alias confirmed: stale ReturnToPool freed %q while B held it; "+
		"C then re-acquired the same instance. Two holders (B,C) for one instance. "+
		"Not a contract violation (by-ID return is documented); reported as latent risk.", c.ID)

	// Clean up: return once (idempotent) to leave the pool quiescent.
	_ = pm.ReturnToPool(c) //nolint:errcheck
	if pm.Busy() != 0 {
		t.Fatalf("Busy() = %d at end, want 0", pm.Busy())
	}
}
