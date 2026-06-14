package jobadmit

// Adversarial probes for the cap-bypass / over-commit family that recurred
// across budgetcap (NaN), admissioncontrol (int64 reserve-sum overflow),
// marketplace (over-commit-under-race), and edge (fail-open).
//
// jobadmit's locking is already tight (one RWMutex guards every check-then-
// commit), and tier/priority/conservation are covered by the existing suites.
// The UNEXPLORED surface is the arithmetic in the capacity gate itself:
//
//	if m.used + j.Request <= m.quota.Total { ... }   // Admit
//
// j.Request is a plain int with NO documented upper bound (Submit only rejects
// Request < 0). The package doc promises, unconditionally, that "the sum of
// admitted Requests never exceeds Quota.Total" and that "Used() <= Quota.Total
// at all times". A single job submitted with a near-MaxInt Request makes
// used+Request overflow to a NEGATIVE value, which passes the <= Total gate,
// so the oversized job is ADMITTED and m.used is corrupted — the exact
// admissioncontrol "reserve-sum overflow flipped the reserve negative" class,
// here re-skinned as int overflow in the admit gate.
//
// These probes assert SINK-SIDE: the actual admit/reject verdict (job State)
// and the committed total (Used()) versus the quota — never merely "no panic".

import (
	"crypto/rand"
	"encoding/hex"
	"math"
	"testing"
)

func advID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("uuid: %v", err)
	}
	return hex.EncodeToString(b)
}

// TestAdversarial_OverflowAdmitGateBypassesQuota is the headline probe.
//
// Contract under test (package doc + Used() doc):
//   - "the sum of admitted Requests never exceeds Quota.Total"
//   - "It is an invariant that Used() <= Quota.Total at all times."
//   - a job whose Request does not fit the remaining quota stays SUSPENDED.
//
// Attack: with the quota PARTIALLY consumed (used > 0), submit one job whose
// Request is so large that used + Request overflows int back to a negative
// number. The gate
//
//	m.used + j.Request <= m.quota.Total
//
// then reads (negative) <= Total == true and ADMITS the job that cannot
// possibly fit, then does used += Request, committing a corrupted (overflowed,
// negative) Used().
//
// We seed used>0 by first admitting a small "seed" job (high priority so it is
// admitted first in the pass), then the huge job's gate overflows in the SAME
// admission pass.
//
// SINK-SIDE assertions:
//   - the oversized (MaxInt) job MUST remain Suspended (it cannot fit);
//   - Used() MUST stay within [0, Quota.Total].
//
// On unfixed prod this FAILS: the huge job is Admitted and Used() goes
// negative. This test is NOT env-gated; the coordinator's fix makes it pass.
func TestAdversarial_OverflowAdmitGateBypassesQuota(t *testing.T) {
	id := advID(t)
	t.Logf("run=%s overflow-admit-gate START", id)

	const quota = 10
	m := NewManager(Quota{Total: quota})

	// Seed: a small high-priority job consumes part of the quota first, so that
	// when the huge job is evaluated in the same pass, used>0 and used+MaxInt
	// overflows. Request 5 of quota 10 is legal.
	if !m.Submit(Job{ID: "seed", Tier: TierPaid, Priority: 100, Request: 5}) {
		t.Fatalf("Submit(seed) returned false")
	}
	// Legal, non-negative Request (Submit accepts it: only Request<0 is rejected).
	// Lower priority than seed so it is evaluated AFTER seed sets used=5.
	huge := math.MaxInt
	if !m.Submit(Job{ID: "overflow", Tier: TierPaid, Priority: 1, Request: huge}) {
		t.Fatalf("Submit(overflow) returned false; a non-negative Request must be accepted as Suspended")
	}

	admitted := m.Admit()

	st, _ := m.State("overflow")
	used := m.Used()
	t.Logf("run=%s admitted=%v overflow-state=%v used=%d quota=%d", id, admitted, st, used, quota)

	// SINK 1: a job that cannot fit (Request >> remaining quota) must NOT be admitted.
	if st == StateAdmitted {
		t.Fatalf("CAP BYPASS: job Request=%d admitted into quota=%d (used now %d); "+
			"used+Request overflowed and passed the <= Total gate", huge, quota, used)
	}
	for _, gotID := range admitted {
		if gotID == "overflow" {
			t.Fatalf("CAP BYPASS: Admit() returned oversized job %q (used=%d quota=%d)", gotID, used, quota)
		}
	}

	// SINK 2: committed total must never leave [0, quota].
	if used < 0 {
		t.Fatalf("OVERFLOW: Used()=%d went NEGATIVE after admitting Request=%d (quota=%d)", used, huge, quota)
	}
	if used > quota {
		t.Fatalf("OVER-ADMISSION: Used()=%d exceeds quota=%d", used, quota)
	}
	t.Logf("run=%s overflow-admit-gate held (state=%v used=%d) END", id, st, used)
}

// TestAdversarial_OverflowPoisonsRunningTotalThenAdmitsEverything proves the
// downstream "poison the running total, then admit everything" sink, mirroring
// the budgetcap NaN finding: after ONE overflow admit, the committed total is
// corrupted (driven negative), so a later request that MUST be rejected — its
// own Request alone exceeds the quota — slips through because the gate now
// compares against a poisoned, far-below-zero used.
//
// SINK-SIDE: after the poison, a job whose Request alone exceeds the quota is
// admitted, and Used() escapes [0, quota].
func TestAdversarial_OverflowPoisonsRunningTotalThenAdmitsEverything(t *testing.T) {
	id := advID(t)
	t.Logf("run=%s overflow-poison-total START", id)

	const quota = 10
	m := NewManager(Quota{Total: quota})

	// Seed: consume part of the quota first so used>0 when poison is evaluated.
	if !m.Submit(Job{ID: "seed", Tier: TierPaid, Priority: 200, Request: 5}) {
		t.Fatalf("Submit(seed) returned false")
	}
	// Poison job: huge Request overflows used negative on admit (evaluated after
	// seed, so used=5 and 5+MaxInt wraps negative, passing the gate).
	if !m.Submit(Job{ID: "poison", Tier: TierPaid, Priority: 100, Request: math.MaxInt}) {
		t.Fatalf("Submit(poison) returned false")
	}
	// Victim job: Request alone (50) exceeds the quota (10); it MUST be rejected
	// in an honest accounting, no matter what else is admitted. Lowest priority
	// so it is evaluated last, against the (poisoned) used.
	if !m.Submit(Job{ID: "victim", Tier: TierPaid, Priority: 1, Request: 50}) {
		t.Fatalf("Submit(victim) returned false")
	}

	admitted := m.Admit()
	stPoison, _ := m.State("poison")
	stVictim, _ := m.State("victim")
	used := m.Used()
	t.Logf("run=%s admitted=%v poison=%v victim=%v used=%d quota=%d",
		id, admitted, stPoison, stVictim, used, quota)

	// SINK: the victim's Request (50) alone blows the quota (10); honest
	// accounting can never admit it. If it is admitted, the running total was
	// poisoned by the overflow.
	if stVictim == StateAdmitted {
		t.Fatalf("POISONED TOTAL: victim Request=50 admitted into quota=%d after overflow poison "+
			"(used=%d); a request that must reject was admitted", quota, used)
	}
	if used < 0 || used > quota {
		t.Fatalf("POISONED TOTAL: Used()=%d outside [0,%d] after overflow admit", used, quota)
	}
	t.Logf("run=%s overflow-poison-total held END", id)
}

// TestAdversarial_ContractPin_ReleaseSymmetryAndZeroRequest pins the parts of
// the gate that ARE correct so a regression there bites too. These reflect the
// real, documented contract and PASS on current prod:
//   - a zero-Request job is admitted without moving Used() (legal no-cost job);
//   - Complete is symmetric and idempotent: it releases exactly Request once,
//     and a second Complete on the same id is a no-op (no negative drift).
func TestAdversarial_ContractPin_ReleaseSymmetryAndZeroRequest(t *testing.T) {
	id := advID(t)
	t.Logf("run=%s contract-pin START", id)

	m := NewManager(Quota{Total: 10})

	if !m.Submit(Job{ID: "zero", Tier: TierPaid, Priority: 1, Request: 0}) {
		t.Fatalf("Submit(zero) failed")
	}
	if !m.Submit(Job{ID: "five", Tier: TierPaid, Priority: 2, Request: 5}) {
		t.Fatalf("Submit(five) failed")
	}

	m.Admit()
	if st, _ := m.State("zero"); st != StateAdmitted {
		t.Fatalf("zero-cost job state=%v, want Admitted", st)
	}
	if st, _ := m.State("five"); st != StateAdmitted {
		t.Fatalf("five job state=%v, want Admitted", st)
	}
	if u := m.Used(); u != 5 {
		t.Fatalf("Used()=%d after admitting {0,5}, want 5", u)
	}

	// Release five: Used drops to 0.
	if !m.Complete("five") {
		t.Fatalf("Complete(five) failed")
	}
	if u := m.Used(); u != 0 {
		t.Fatalf("Used()=%d after Complete(five), want 0", u)
	}
	// Double-complete must be a no-op — no negative refund drift.
	if m.Complete("five") {
		t.Fatalf("second Complete(five) returned true; release must be idempotent")
	}
	if u := m.Used(); u != 0 {
		t.Fatalf("Used()=%d after double Complete, want 0 (refund drift)", u)
	}
	t.Logf("run=%s contract-pin held END", id)
}
