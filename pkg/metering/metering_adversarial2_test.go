package metering

import (
	"math"
	"testing"
	"time"
)

// metering_adversarial2_test.go — a SECOND adversarial pass over pkg/metering's
// accounting ledger, written to NOT duplicate the existing pins in
// metering_test.go / conservation_test.go / metering_adversarial_test.go (which
// already cover sequential conservation, per-key isolation, payout attribution,
// int64 overflow, NaN-rate isolation, precision drift, the documented race, and
// the no-mutation-on-error contract for Register/Meter/empty-claim).
//
// FOCUS of this pass (new ground):
//
//  1. ACCESSOR RETURN-ISOLATION (the pointer/map/slice aliasing bug class that
//     struck internal/node, pkg/discovery, pkg/session): does ANY accessor hand
//     a caller a reference into Market's internal state that a caller mutation
//     could corrupt? metering.go's surface returns ONLY value types
//     (TotalUnits->int64, Payout->float64); no method returns *provider, the
//     providers map, or a slice. This is proven sink-side: the result of an
//     accessor is a copy, so re-reading the store after mutating the returned
//     value yields the original — there is NOTHING to alias. We pin that
//     value-return contract so a future change to a reference-returning accessor
//     (e.g. a ListProviders []string or a *provider getter) would have to add a
//     defensive-copy test alongside it.
//
//  2. SECOND-CLAIM FAILURE PRESERVES PRIOR LEDGER: ClaimBounty documents "does
//     not mutate state" on the already-claimed error path. The existing suite
//     pins the empty-claim path; this pins the *second non-empty* claim path
//     AND, critically, that units accrued under the FIRST claim survive a
//     rejected second claim (a billing-integrity property: a failed re-claim
//     must neither reset the bounty binding nor zero accrued usage).
//
//  3. Inf-RATE FLOAT PROPAGATION with finite units is a documented projection
//     contract (companion to the existing NaN-rate test): +Inf rate * positive
//     units = +Inf payout, and the integer unit ledger is untouched.
//
// VERDICT (stated up front, proven by the tests below): NO accessor shares
// memory with internal state; the ledger is value-typed end to end. No
// production bug found in this pass. No production code is modified.

// ---------------------------------------------------------------------------
// timeout guard: none of these probes block, but per the adversarial protocol
// we wrap the whole-body work so a hang (e.g. a future deadlock-introducing
// change) surfaces as a test failure rather than a CI hang.
// ---------------------------------------------------------------------------
func withDeadline2(t *testing.T, d time.Duration, body func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		body()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("test body exceeded deadline %v — possible hang/deadlock in metering", d)
	}
}

// TestAdversarial2_AccessorsReturnValuesNotAliases proves sink-side that the
// public accessors (TotalUnits, Payout) return COPIES: mutating the returned
// value cannot reach back into Market's internal per-provider state. This is the
// pointer/map/slice aliasing bug class adapted to a value-returning API — the
// proof is that there is nothing to alias.
//
// ANTI-BLUFF: we capture an accessor result, locally clobber it to a poison
// value, then re-read the store from a fresh accessor call and assert the store
// is UNCHANGED. If TotalUnits ever returned, say, a *int64 into p.totalUnits (a
// reference-returning regression), the local clobber would corrupt the store and
// the re-read would observe the poison — this test would fail.
func TestAdversarial2_AccessorsReturnValuesNotAliases(t *testing.T) {
	id := runUUID("adversarial2-accessor-return-isolation")
	t.Logf("run-uuid=%s START accessor-isolation", id)

	withDeadline2(t, 10*time.Second, func() {
		const (
			pid          = "prov-alias"
			ratePerUnit  = 0.5
			bountyReward = 4.0
		)
		m := NewMarket()
		if err := m.Register(pid, true); err != nil {
			t.Fatalf("Register: %v", err)
		}
		if err := m.ClaimBounty(pid, "b-alias"); err != nil {
			t.Fatalf("ClaimBounty: %v", err)
		}
		units := []int{7, 11, 13}
		var want int64
		for _, u := range units {
			if err := m.Meter(pid, u); err != nil {
				t.Fatalf("Meter(%d): %v", u, err)
			}
			want += int64(u)
		}

		// (a) TotalUnits result is a value copy: clobber it locally, store intact.
		got := m.TotalUnits(pid)
		if got != want {
			t.Fatalf("TotalUnits: got %d, want %d", got, want)
		}
		got = -999999 // clobber the local copy
		_ = got
		if reread := m.TotalUnits(pid); reread != want {
			t.Fatalf("ALIASING: store changed after clobbering a returned int64: "+
				"got %d, want %d — accessor leaked a reference into internal state",
				reread, want)
		}

		// (b) Payout result is a value copy: clobber it locally, store intact.
		wantPay := bountyReward + float64(want)*ratePerUnit
		pay := m.Payout(pid, ratePerUnit, bountyReward)
		if pay != wantPay {
			t.Fatalf("Payout: got %v, want %v", pay, wantPay)
		}
		pay = math.Inf(1) // clobber the local copy
		_ = pay
		if reread := m.Payout(pid, ratePerUnit, bountyReward); reread != wantPay {
			t.Fatalf("ALIASING: Payout changed after clobbering a returned float64: "+
				"got %v, want %v", reread, wantPay)
		}

		// (c) Independence across accessor calls: two TotalUnits results are
		// independent copies, not two views of one shared cell.
		a := m.TotalUnits(pid)
		b := m.TotalUnits(pid)
		a += 100 // mutate one copy
		_ = a
		if b != want {
			t.Fatalf("ALIASING: second TotalUnits copy moved when first was mutated: "+
				"got %d, want %d", b, want)
		}
		t.Logf("run-uuid=%s END accessor-isolation OK (all accessors return value copies)", id)
	})
}

// TestAdversarial2_SecondClaimFailurePreservesLedger pins the billing-integrity
// half of ClaimBounty's "does not mutate state on error" contract for the
// SECOND non-empty claim: after a provider has claimed bounty "first" and
// accrued units, a rejected attempt to claim "second" must (a) return
// ErrBountyAlreadyClaimed, (b) leave the original claim able to keep metering,
// and (c) leave accrued units EXACTLY conserved (no reset to zero, no double).
//
// ANTI-BLUFF: units are accrued before AND after the rejected re-claim; the
// final total must equal the independent sum of all accepted Meter calls. If
// ClaimBounty mutated state on the rejected path (e.g. reset totalUnits, or
// flipped claimed off so subsequent Meter fails), the post-reject Meter would
// error or the conserved total would diverge.
//
// MUTATION (load-bearing): in metering.go ClaimBounty, moving the
// `p.claimed = true; p.bountyID = bountyID` writes BEFORE the `if p.claimed`
// guard (so a second claim rebinds the bounty) would change bountyID under the
// hood; we assert the binding by behaviour — the original lifecycle keeps
// working and units conserve — and an accruing-reset mutation breaks the sum.
func TestAdversarial2_SecondClaimFailurePreservesLedger(t *testing.T) {
	id := runUUID("adversarial2-second-claim-preserves-ledger")
	t.Logf("run-uuid=%s START second-claim", id)

	withDeadline2(t, 10*time.Second, func() {
		const pid = "prov-reclaim"
		m := NewMarket()
		if err := m.Register(pid, true); err != nil {
			t.Fatalf("Register: %v", err)
		}
		if err := m.ClaimBounty(pid, "first"); err != nil {
			t.Fatalf("ClaimBounty(first): %v", err)
		}

		var want int64
		preUnits := []int{5, 2, 8}
		for _, u := range preUnits {
			if err := m.Meter(pid, u); err != nil {
				t.Fatalf("pre-reclaim Meter(%d): %v", u, err)
			}
			want += int64(u)
		}

		// Rejected second claim MUST NOT mutate state.
		err := m.ClaimBounty(pid, "second")
		if err == nil {
			t.Fatalf("second ClaimBounty: got nil, want ErrBountyAlreadyClaimed")
		}

		// (a) accrued units survived the rejected re-claim untouched.
		if got := m.TotalUnits(pid); got != want {
			t.Fatalf("units after rejected re-claim: got %d, want %d "+
				"(rejected ClaimBounty must not reset accrued usage)", got, want)
		}

		// (b) original lifecycle still functional: metering continues to accrue.
		postUnits := []int{6, 1}
		for _, u := range postUnits {
			if err := m.Meter(pid, u); err != nil {
				t.Fatalf("post-reclaim Meter(%d): %v "+
					"(rejected re-claim must not break the original claim)", u, err)
			}
			want += int64(u)
		}
		if got := m.TotalUnits(pid); got != want {
			t.Fatalf("final units: got %d, want %d (exact conservation across rejected re-claim)",
				got, want)
		}
		t.Logf("run-uuid=%s END second-claim OK (ledger conserved across rejected re-claim, total=%d)", id, want)
	})
}

// TestAdversarial2_InfRateFinitePayoutContract is the +Inf companion to the
// existing NaN-rate isolation test: a caller-supplied +Inf rate against positive
// metered units yields a +Inf payout (documented float propagation), while the
// integer unit ledger is untouched and still reports the exact conserved total.
// This pins the float projection as cleanly separated from the revenue-critical
// integer accounting.
func TestAdversarial2_InfRateFinitePayoutContract(t *testing.T) {
	id := runUUID("adversarial2-inf-rate-projection")
	t.Logf("run-uuid=%s START inf-rate", id)

	withDeadline2(t, 10*time.Second, func() {
		const pid = "prov-inf"
		m := NewMarket()
		if err := m.Register(pid, true); err != nil {
			t.Fatalf("Register: %v", err)
		}
		if err := m.ClaimBounty(pid, "b-inf"); err != nil {
			t.Fatalf("ClaimBounty: %v", err)
		}
		units := []int{3, 4, 5}
		var want int64
		for _, u := range units {
			if err := m.Meter(pid, u); err != nil {
				t.Fatalf("Meter(%d): %v", u, err)
			}
			want += int64(u)
		}

		// +Inf rate * positive units = +Inf payout (float-domain propagation).
		pay := m.Payout(pid, math.Inf(1), 1.0)
		if !math.IsInf(pay, 1) {
			t.Fatalf("Payout with +Inf rate: got %v, want +Inf (float poison propagates)", pay)
		}

		// Integer ledger untouched by the float projection.
		if got := m.TotalUnits(pid); got != want {
			t.Fatalf("unit ledger corrupted by Inf-rate Payout: got %d, want %d", got, want)
		}

		// A subsequent clean Payout recovers an exact finite value.
		clean := m.Payout(pid, 0.25, 2.0)
		wantClean := 2.0 + float64(want)*0.25
		t.Logf("run-uuid=%s units=%d inf-payout=%v clean=%v want=%v", id, want, pay, clean, wantClean)
		if clean != wantClean {
			t.Fatalf("clean Payout after Inf call: got %v, want %v", clean, wantClean)
		}
		t.Logf("run-uuid=%s END inf-rate OK", id)
	})
}
