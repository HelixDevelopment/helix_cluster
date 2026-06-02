package jobadmit

import (
	"crypto/rand"
	"encoding/hex"
	"testing"
)

func runUUID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("uuid: %v", err)
	}
	return hex.EncodeToString(b)
}

// Clause 1: tiered, priority-ordered admission under constrained quota.
//
// Submit free- and paid-tier jobs whose total Request EXCEEDS the quota.
// Before Admit: all Suspended. After Admit: paid-tier admitted before
// free-tier, higher priority first, Used() <= Quota.Total, and jobs that did
// not fit remain Suspended.
//
// Mutation: admitting in submit-order instead of (tier,priority) order yields a
// different admitted slice (e.g. free F-lo submitted first would be admitted),
// which the explicit order/state assertions below reject.
func TestAdmit_TierPriorityOrderUnderConstrainedQuota(t *testing.T) {
	uuid := runUUID(t)
	t.Logf("run=%s clause=1 START", uuid)

	m := NewManager(Quota{Total: 10})

	// Submit order deliberately NOT in tier/priority order so submit-order
	// admission would produce a visibly different result.
	// F-lo:  free,  prio 1, req 4, seq 0
	// P-lo:  paid,  prio 1, req 4, seq 1
	// P-hi:  paid,  prio 9, req 5, seq 2
	// F-hi:  free,  prio 9, req 4, seq 3
	for _, j := range []Job{
		{ID: "F-lo", Tier: TierFree, Priority: 1, Request: 4},
		{ID: "P-lo", Tier: TierPaid, Priority: 1, Request: 4},
		{ID: "P-hi", Tier: TierPaid, Priority: 9, Request: 5},
		{ID: "F-hi", Tier: TierFree, Priority: 9, Request: 4},
	} {
		if !m.Submit(j) {
			t.Fatalf("submit %s failed", j.ID)
		}
	}

	// Total request = 4+4+5+4 = 17 > quota 10.
	for _, id := range []string{"F-lo", "P-lo", "P-hi", "F-hi"} {
		if st, _ := m.State(id); st != StateSuspended {
			t.Fatalf("before: %s = %v, want Suspended", id, st)
		}
	}
	t.Logf("run=%s before: all Suspended, used=%d", uuid, m.Used())

	admitted := m.Admit()

	// Expected admission order: P-hi (paid,9,5), then P-lo (paid,1,4) -> used 9.
	// F-hi (free,9,4) does not fit (9+4=13>10) -> stays suspended.
	// F-lo (free,1,4) does not fit -> stays suspended.
	want := []string{"P-hi", "P-lo"}
	if len(admitted) != len(want) {
		t.Fatalf("admitted=%v, want %v", admitted, want)
	}
	for i := range want {
		if admitted[i] != want[i] {
			t.Fatalf("admitted order[%d]=%s, want %s (full=%v)", i, admitted[i], want[i], admitted)
		}
	}

	// Paid admitted, free not.
	for _, id := range []string{"P-hi", "P-lo"} {
		if st, _ := m.State(id); st != StateAdmitted {
			t.Fatalf("%s = %v, want Admitted", id, st)
		}
	}
	for _, id := range []string{"F-hi", "F-lo"} {
		if st, _ := m.State(id); st != StateSuspended {
			t.Fatalf("%s = %v, want still Suspended", id, st)
		}
	}

	if m.Used() != 9 {
		t.Fatalf("Used()=%d, want 9", m.Used())
	}
	if m.Used() > m.quota.Total {
		t.Fatalf("Used()=%d exceeds quota %d", m.Used(), m.quota.Total)
	}
	t.Logf("run=%s after: admitted=%v used=%d quota=%d END", uuid, admitted, m.Used(), m.quota.Total)
}

// Clause 2: freeing capacity admits a previously-suspended job on next pass.
//
// Mutation: if Complete fails to release used quota (or Admit ignores freed
// capacity), the second Admit pass would not admit the waiting job and the
// final state/Used assertions fail.
func TestComplete_FreesCapacityForNextPass(t *testing.T) {
	uuid := runUUID(t)
	t.Logf("run=%s clause=2 START", uuid)

	m := NewManager(Quota{Total: 10})
	for _, j := range []Job{
		{ID: "A", Tier: TierPaid, Priority: 5, Request: 8},
		{ID: "B", Tier: TierFree, Priority: 5, Request: 6},
	} {
		if !m.Submit(j) {
			t.Fatalf("submit %s failed", j.ID)
		}
	}

	first := m.Admit()
	// A fits (8<=10). B does not (8+6=14>10).
	if len(first) != 1 || first[0] != "A" {
		t.Fatalf("first pass admitted=%v, want [A]", first)
	}
	if st, _ := m.State("B"); st != StateSuspended {
		t.Fatalf("B=%v after first pass, want Suspended", st)
	}
	t.Logf("run=%s after first pass: admitted=%v used=%d B suspended", uuid, first, m.Used())

	// A second pass with no change must NOT admit B (still no room).
	if mid := m.Admit(); len(mid) != 0 {
		t.Fatalf("no-change pass admitted=%v, want none", mid)
	}
	if st, _ := m.State("B"); st != StateSuspended {
		t.Fatalf("B=%v after no-change pass, want Suspended", st)
	}

	// Free A's 8 units.
	if !m.Complete("A") {
		t.Fatalf("Complete(A) failed")
	}
	if m.Used() != 0 {
		t.Fatalf("Used()=%d after completing A, want 0", m.Used())
	}

	second := m.Admit()
	if len(second) != 1 || second[0] != "B" {
		t.Fatalf("post-free pass admitted=%v, want [B]", second)
	}
	if st, _ := m.State("B"); st != StateAdmitted {
		t.Fatalf("B=%v after free, want Admitted", st)
	}
	if m.Used() != 6 {
		t.Fatalf("Used()=%d, want 6", m.Used())
	}
	t.Logf("run=%s after free+pass: admitted=%v used=%d END", uuid, second, m.Used())
}

// Clause 3: exact accounting; Used() never exceeds Quota.Total across a run.
//
// Mutation: dropping the fits-quota check in Admit would over-admit and push
// Used() above Quota.Total, tripping the invariant assertion in this loop.
func TestExactAccounting_UsedNeverExceedsQuota(t *testing.T) {
	uuid := runUUID(t)
	t.Logf("run=%s clause=3 START", uuid)

	const quota = 12
	m := NewManager(Quota{Total: quota})

	// Many jobs whose total request (5+5+5+5+5 = 25) far exceeds quota.
	for _, j := range []Job{
		{ID: "j1", Tier: TierPaid, Priority: 3, Request: 5},
		{ID: "j2", Tier: TierPaid, Priority: 2, Request: 5},
		{ID: "j3", Tier: TierPaid, Priority: 1, Request: 5},
		{ID: "j4", Tier: TierFree, Priority: 9, Request: 5},
		{ID: "j5", Tier: TierFree, Priority: 8, Request: 5},
	} {
		if !m.Submit(j) {
			t.Fatalf("submit %s failed", j.ID)
		}
	}

	assertInvariant := func(stage string) {
		if m.Used() > quota {
			t.Fatalf("%s: Used()=%d exceeds quota %d", stage, m.Used(), quota)
		}
		if m.Used() < 0 {
			t.Fatalf("%s: Used()=%d negative", stage, m.Used())
		}
	}

	assertInvariant("after submit")
	t.Logf("run=%s used after submit=%d", uuid, m.Used())

	// Drive several passes interleaved with completions; invariant must hold
	// after every observable mutation.
	a1 := m.Admit()
	assertInvariant("after pass 1")
	// j1+j2 = 10 admitted; j3 would be 15>12 so only two admitted.
	if m.Used() != 10 {
		t.Fatalf("used after pass1=%d, want 10 (admitted=%v)", m.Used(), a1)
	}

	if !m.Complete("j1") {
		t.Fatalf("Complete(j1) failed")
	}
	assertInvariant("after complete j1")

	a2 := m.Admit()
	assertInvariant("after pass 2")
	t.Logf("run=%s pass1=%v pass2=%v used=%d", uuid, a1, a2, m.Used())

	if !m.Complete("j2") {
		t.Fatalf("Complete(j2) failed")
	}
	assertInvariant("after complete j2")

	a3 := m.Admit()
	assertInvariant("after pass 3")

	// Final sanity: Used must equal the sum of currently-admitted requests.
	wantUsed := 0
	for id, j := range m.jobs {
		if st, _ := m.State(id); st == StateAdmitted {
			wantUsed += j.Request
		}
	}
	if m.Used() != wantUsed {
		t.Fatalf("Used()=%d but sum of admitted requests=%d", m.Used(), wantUsed)
	}
	t.Logf("run=%s pass3=%v final used=%d (sum-admitted=%d) END", uuid, a3, m.Used(), wantUsed)
}

// Submit guarantees: a job whose request alone exceeds quota is SUSPENDED, not
// rejected, and never gets admitted (fits-check holds at admission).
//
// Mutation: if Submit rejected oversized jobs (returning false) instead of
// suspending them, the state assertion below would fail.
func TestSubmit_OversizedJobIsSuspendedNotRejected(t *testing.T) {
	uuid := runUUID(t)
	t.Logf("run=%s clause=submit START", uuid)

	m := NewManager(Quota{Total: 4})
	if !m.Submit(Job{ID: "big", Tier: TierPaid, Priority: 9, Request: 100}) {
		t.Fatalf("Submit(big) returned false; oversized job must be accepted as Suspended")
	}
	if st, _ := m.State("big"); st != StateSuspended {
		t.Fatalf("big=%v, want Suspended", st)
	}

	admitted := m.Admit()
	if len(admitted) != 0 {
		t.Fatalf("admitted=%v, want none (cannot fit)", admitted)
	}
	if st, _ := m.State("big"); st != StateSuspended {
		t.Fatalf("big=%v after admit pass, want still Suspended", st)
	}
	if m.Used() != 0 {
		t.Fatalf("Used()=%d, want 0", m.Used())
	}
	t.Logf("run=%s oversized stays Suspended, used=%d END", uuid, m.Used())
}
