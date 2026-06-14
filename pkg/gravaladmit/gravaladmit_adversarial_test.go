package gravaladmit

// Adversarial cap-bypass probes for the GraVal admission gate.
//
// CONTEXT: tonight the SAME cap-bypass family was found four times across
// admission/budget gates (budgetcap NaN poison, admissioncontrol int64 reserve
// overflow, marketplace over-commit-under-race). gravaladmit is an admission
// gate, so this file hunts that exact family here.
//
// IMPORTANT — what the "cap" actually is in THIS gate:
//
// gravaladmit is a CRYPTOGRAPHIC admission gate, not a numeric budget/capacity
// gate. It holds no float running total, no int64 reserve sum, and no
// refund/release ledger. Its scarce, capacity-bounded resource is the
// SINGLE-USE NONCE: IssueChallenge mints exactly one pending token per provider;
// Admit must CONSUME-AND-REMOVE that token atomically so that, for one issued
// challenge, AT MOST ONE Admit call can ever be admitted. That is a capacity of
// K=1 per issued nonce. The cap-bypass analogues therefore map as:
//
//	(a) OVER-COMMIT UNDER RACE  -> N concurrent Admit calls against ONE issued
//	    challenge; the documented single-use invariant means admitted count MUST
//	    be exactly 1, never >1. A TOCTOU gap between "find pending nonce" and
//	    "delete pending nonce" would let two goroutines both admit (double-spend
//	    of one token == over-commit beyond capacity K=1). Probed SINK-SIDE:
//	    we count admitted Decisions, not "no panic".
//	(b) NUMERIC POISON          -> N/A: no float/weight running total exists to
//	    poison. Pinned by inspection note below; no manufactured bug.
//	(c) INTEGER OVERFLOW        -> N/A: no int64/uint sum or MaxInt64 capacity.
//	(d) RELEASE/REFUND / DOUBLE-COUNT -> the pending map IS the ledger. Duplicate
//	    consumption of one nonce == a double-count leak. Concurrent IssueChallenge
//	    for one provider must also mint AT MOST ONE pending token (no duplicate
//	    insert under race). Both probed SINK-SIDE against the observable schedulable
//	    set / admitted count.
//
// FINDING (see bottom of file and the agent report): NO BUG — the consume path
// does find+delete under ONE mutex hold (gravaladmit.go ~L239-251), so the
// check-then-commit is atomic. These tests PIN that contract; they pass against
// unmodified prod and FAIL if the atomicity is broken (mutation-proof note in
// TestAdmit_SingleUseNonce_NoOverCommitUnderRace).

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

const advRunID = "gravaladmit-adv-HXC-graval-7c1d9a44-4b6d-11ef-9c2a-0050568a77b4"

var advSecret = []byte("helix-gravaladmit-adversarial-secret-v1")

// -----------------------------------------------------------------------------
// (a) OVER-COMMIT UNDER RACE — the capacity here is K=1 per issued nonce.
//
// One provider, ONE IssueChallenge, then N goroutines all call Admit with a
// CORRECT prover concurrently. The single-use contract (gravaladmit.go L21-23,
// L208-238) demands that exactly ONE of them is admitted; the nonce is consumed
// by the first and is gone for the rest. If the find/delete were not atomic
// under one lock (a TOCTOU gap), two or more goroutines could both observe the
// pending nonce before either deletes it, and both would be admitted —
// double-spending a single token == admitting beyond capacity K=1.
//
// SINK-SIDE assertion: we count Decisions with Admitted==true. It MUST equal 1.
// We do NOT merely assert "no panic" and we do NOT trust the -race flag alone.
//
// MUTATION-PROOF (stated, since prod is correct as-is): if the `delete` on
// gravaladmit.go L247 were hoisted out of the mutex (or the whole find/delete
// block were split into a read-lock find then a separate write-lock delete),
// this test would observe admittedCount > 1 under -race and FAIL.
// -----------------------------------------------------------------------------

func TestAdmit_SingleUseNonce_NoOverCommitUnderRace(t *testing.T) {
	const goroutines = 256

	a, err := NewAdmitter(advSecret)
	if err != nil {
		t.Fatalf("%s: NewAdmitter: %v", advRunID, err)
	}

	providerID := "gpu-provider-race-K1"
	ch, err := a.IssueChallenge(providerID)
	if err != nil {
		t.Fatalf("%s: IssueChallenge: %v", advRunID, err)
	}
	t.Logf("%s: BEFORE race — capacity K=1 (one issued nonce=%q)", advRunID, ch.Nonce)

	prover := &correctProver{key: advSecret}

	var admitted int64
	var rejected int64
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	done.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer done.Done()
			start.Wait() // release all goroutines simultaneously to maximise the race window
			d := a.Admit(advRunID, providerID, prover)
			if d.Admitted {
				atomic.AddInt64(&admitted, 1)
				// SINK-SIDE: an admitted decision MUST carry the verified label.
				if d.Labels["graval.verified"] != "true" {
					t.Errorf("%s: admitted decision missing graval.verified label: %v", advRunID, d.Labels)
				}
			} else {
				atomic.AddInt64(&rejected, 1)
			}
		}()
	}

	start.Done()
	done.Wait()

	gotAdmitted := atomic.LoadInt64(&admitted)
	gotRejected := atomic.LoadInt64(&rejected)
	t.Logf("%s: AFTER race — admitted=%d rejected=%d (capacity K=1)", advRunID, gotAdmitted, gotRejected)

	// SINK-SIDE cap invariant: committed (admitted) count MUST NOT exceed K=1.
	if gotAdmitted != 1 {
		t.Fatalf("%s: OVER-COMMIT: capacity K=1 but admitted=%d (want exactly 1); single-use nonce was double-spent under race",
			advRunID, gotAdmitted)
	}
	if gotAdmitted+gotRejected != goroutines {
		t.Fatalf("%s: accounting drift: admitted(%d)+rejected(%d) != goroutines(%d)",
			advRunID, gotAdmitted, gotRejected, goroutines)
	}

	// SINK-SIDE follow-up: the token is fully consumed — a fresh Admit (no
	// re-issue) for the same provider must now be rejected (no leak / refund).
	dAfter := a.Admit(advRunID, providerID, prover)
	if dAfter.Admitted {
		t.Fatalf("%s: REFUND/LEAK: nonce survived the race; post-race Admit wrongly Admitted=true", advRunID)
	}
}

// -----------------------------------------------------------------------------
// (a)/(d) CROSS-PROVIDER CAPACITY UNDER RACE.
//
// Mint N distinct single-use nonces (one per provider), then fire 2*N concurrent
// Admit calls — exactly one correct attempt and one duplicate attempt per
// provider. The committed total MUST equal N (exactly one admit per issued
// nonce), never more. This is the multi-token form of the cap invariant:
// total admitted across the whole gate must never exceed the number of tokens
// minted. A double-spend on ANY single nonce would push the count past N.
// -----------------------------------------------------------------------------

func TestAdmit_CrossProvider_CommittedNeverExceedsTokensMinted(t *testing.T) {
	const providers = 200

	a, err := NewAdmitter(advSecret)
	if err != nil {
		t.Fatalf("%s: NewAdmitter: %v", advRunID, err)
	}

	ids := make([]string, providers)
	for i := 0; i < providers; i++ {
		ids[i] = fmt.Sprintf("gpu-provider-cap-%04d", i)
		if _, e := a.IssueChallenge(ids[i]); e != nil {
			t.Fatalf("%s: IssueChallenge(%q): %v", advRunID, ids[i], e)
		}
	}
	t.Logf("%s: minted %d single-use tokens (capacity == %d)", advRunID, providers, providers)

	prover := &correctProver{key: advSecret}

	var admitted int64
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	// two contenders per provider — both try to spend the same single token.
	done.Add(providers * 2)

	for i := 0; i < providers; i++ {
		id := ids[i]
		for c := 0; c < 2; c++ {
			go func() {
				defer done.Done()
				start.Wait()
				if a.Admit(advRunID, id, prover).Admitted {
					atomic.AddInt64(&admitted, 1)
				}
			}()
		}
	}

	start.Done()
	done.Wait()

	got := atomic.LoadInt64(&admitted)
	t.Logf("%s: AFTER race — admitted=%d, tokens minted=%d", advRunID, got, providers)

	// SINK-SIDE cap invariant: committed total MUST NOT exceed capacity.
	if got > int64(providers) {
		t.Fatalf("%s: OVER-COMMIT: admitted=%d exceeds capacity=%d — a single-use token was double-spent",
			advRunID, got, providers)
	}
	// And with a correct prover for every token, exactly N must commit.
	if got != int64(providers) {
		t.Fatalf("%s: UNDER-COMMIT/LEAK: admitted=%d want exactly %d (one per minted token)",
			advRunID, got, providers)
	}
}

// -----------------------------------------------------------------------------
// (d) DUPLICATE-MINT UNDER RACE — the mint side of the ledger.
//
// N goroutines concurrently call IssueChallenge for the SAME provider. The
// duplicate-pending guard (gravaladmit.go L196-201) demands AT MOST ONE
// outstanding token per provider; under race, exactly one IssueChallenge must
// succeed and the rest must return ErrNonceReused. If two succeeded, the gate
// would hold two live tokens for one provider — a capacity leak that lets the
// provider be admitted twice. SINK-SIDE: count successes, then prove the gate
// only admits ONCE.
// -----------------------------------------------------------------------------

func TestIssueChallenge_DuplicateMintUnderRace_AtMostOneToken(t *testing.T) {
	const goroutines = 128

	a, err := NewAdmitter(advSecret)
	if err != nil {
		t.Fatalf("%s: NewAdmitter: %v", advRunID, err)
	}

	providerID := "gpu-provider-dupmint"

	var minted int64
	var reused int64
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	done.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer done.Done()
			start.Wait()
			_, e := a.IssueChallenge(providerID)
			switch {
			case e == nil:
				atomic.AddInt64(&minted, 1)
			case isErrNonceReused(e):
				atomic.AddInt64(&reused, 1)
			default:
				t.Errorf("%s: unexpected IssueChallenge error: %v", advRunID, e)
			}
		}()
	}

	start.Done()
	done.Wait()

	gotMinted := atomic.LoadInt64(&minted)
	t.Logf("%s: concurrent IssueChallenge — minted=%d reused=%d", advRunID, gotMinted, atomic.LoadInt64(&reused))

	if gotMinted != 1 {
		t.Fatalf("%s: DUPLICATE-MINT: capacity is 1 pending token per provider but minted=%d (want exactly 1)",
			advRunID, gotMinted)
	}

	// SINK-SIDE: with exactly one token live, the provider can be admitted at
	// most once. Fire two concurrent Admits and assert admitted==1.
	prover := &correctProver{key: advSecret}
	var admitted int64
	var d2 sync.WaitGroup
	d2.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer d2.Done()
			if a.Admit(advRunID, providerID, prover).Admitted {
				atomic.AddInt64(&admitted, 1)
			}
		}()
	}
	d2.Wait()
	if atomic.LoadInt64(&admitted) != 1 {
		t.Fatalf("%s: single live token admitted %d providers (want 1)", advRunID, atomic.LoadInt64(&admitted))
	}
}

// -----------------------------------------------------------------------------
// (a)+(b) NONCE-CONSUMED-FIRST: a forged/nil attempt must STILL burn the token
// and must NOT poison the gate into admitting a later request.
//
// This is the gravaladmit analogue of the "one poisoned admit thereafter admits
// EVERYTHING" sink probe. We interleave, under race, a forged Admit and a
// correct Admit for the same single token. Whoever wins, the token is consumed
// exactly once; the OTHER must be rejected. Crucially, after the dust settles a
// brand-new request for that provider (no re-issue) MUST be rejected — proving a
// failed/forged attempt did not leave the gate in a state that admits everything.
// -----------------------------------------------------------------------------

func TestAdmit_ForgedAndCorrectRaceOneToken_NoPoison(t *testing.T) {
	const rounds = 300

	for r := 0; r < rounds; r++ {
		a, err := NewAdmitter(advSecret)
		if err != nil {
			t.Fatalf("%s: NewAdmitter: %v", advRunID, err)
		}
		providerID := "gpu-provider-poison-probe"
		if _, e := a.IssueChallenge(providerID); e != nil {
			t.Fatalf("%s: IssueChallenge: %v", advRunID, e)
		}

		correct := &correctProver{key: advSecret}
		forged := &forgedProver{}

		var admitted int64
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			if a.Admit(advRunID, providerID, correct).Admitted {
				atomic.AddInt64(&admitted, 1)
			}
		}()
		go func() {
			defer wg.Done()
			if a.Admit(advRunID, providerID, forged).Admitted {
				atomic.AddInt64(&admitted, 1)
			}
		}()
		wg.Wait()

		got := atomic.LoadInt64(&admitted)
		// At most one admit (only the correct prover can ever be admitted, and
		// only if it won the single token). Never more than one.
		if got > 1 {
			t.Fatalf("%s: round %d OVER-COMMIT: admitted=%d for one token", advRunID, r, got)
		}

		// SINK: a fresh request with no re-issued nonce must be rejected. If a
		// forged/raced attempt had poisoned the gate, this would wrongly admit.
		if a.Admit(advRunID, providerID, correct).Admitted {
			t.Fatalf("%s: round %d POISON: post-race Admit wrongly Admitted=true (token leaked/poisoned)", advRunID, r)
		}
	}
	t.Logf("%s: %d forged/correct race rounds — no over-commit, no poison", advRunID, rounds)
}

// -----------------------------------------------------------------------------
// (b)/(c) CONTRACT-PIN: no numeric running total / no int64 sum exists to poison
// or overflow. This is an honest documentation of the THREE-WAY discrimination:
// classes (b) and (c) are NOT APPLICABLE to this gate because the admission
// decision is purely cryptographic (HMAC equality + single-use map membership),
// with zero arithmetic accumulation. There is therefore no manufactured bug for
// those classes. This test asserts the only "numeric-ish" sink — the schedulable
// set size — stays bounded by tokens minted, reinforcing that admission cannot
// be inflated by any value-based poison.
// -----------------------------------------------------------------------------

func TestSchedulableSet_SizeBoundedByTokens_NoArithmeticInflation(t *testing.T) {
	a, err := NewAdmitter(advSecret)
	if err != nil {
		t.Fatalf("%s: NewAdmitter: %v", advRunID, err)
	}

	ids := []string{"n1", "n2", "n3", "n4", "n5"}
	provers := make(map[string]Prover, len(ids))
	// Mint tokens for only the first three; the last two get NO token.
	minted := 0
	for i, id := range ids {
		if i < 3 {
			if _, e := a.IssueChallenge(id); e != nil {
				t.Fatalf("%s: IssueChallenge(%q): %v", advRunID, id, e)
			}
			minted++
		}
		provers[id] = &correctProver{key: advSecret}
	}

	schedulable, decisions := a.SchedulableSet(advRunID, ids, provers)
	t.Logf("%s: minted=%d schedulable=%v", advRunID, minted, schedulable)

	// SINK-SIDE: admitted count can never exceed tokens minted, regardless of how
	// many providerIDs (or correct provers) are presented. Presenting more
	// providers than tokens MUST NOT inflate the admitted set.
	if len(schedulable) > minted {
		t.Fatalf("%s: INFLATION: schedulable=%d exceeds tokens minted=%d", advRunID, len(schedulable), minted)
	}
	if len(schedulable) != minted {
		t.Fatalf("%s: want exactly %d admitted (one per token), got %d", advRunID, minted, len(schedulable))
	}
	// The two token-less providers must be rejected with non-empty Reason.
	rejected := 0
	for _, d := range decisions {
		if !d.Admitted {
			rejected++
			if d.Reason == "" {
				t.Fatalf("%s: rejected provider %q must carry non-empty Reason", advRunID, d.ProviderID)
			}
		}
	}
	if rejected != len(ids)-minted {
		t.Fatalf("%s: want %d rejected (token-less), got %d", advRunID, len(ids)-minted, rejected)
	}
}
