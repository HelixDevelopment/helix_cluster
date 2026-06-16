package jobadmit

// Adversarial SECOND PASS for pkg/jobadmit.
//
// The first adversarial suite (jobadmit_adversarial_test.go) found and pinned the
// int-overflow cap-bypass in the Admit gate (now fixed: the gate compares
// `j.Request <= m.quota.Total - m.used` instead of the overflow-prone
// `m.used + j.Request <= m.quota.Total`). This pass deliberately does NOT
// re-probe that family. Instead it attacks the OTHER load-bearing parts of the
// admission decision that the existing suites do not assert head-on:
//
//   - WITHIN-PASS live accounting: a single Admit() walks many jobs; each admit
//     must subtract from the SAME running `used` so two half-quota jobs exhaust
//     the budget and a third in the same pass is rejected. A batch/stale-`used`
//     bug (computing fits against the pass-START used for every job) would
//     over-admit within one pass — Used() > Quota.Total. The existing tests admit
//     at most 2 fitting jobs but never force the THIRD-in-same-pass rejection that
//     distinguishes live-used from start-of-pass-used.
//
//   - NO HEAD-OF-LINE BLOCKING: the sorted pass puts a huge, never-fitting,
//     highest-precedence job FIRST; the loop must `continue` past it and still
//     admit a smaller fitting job later in the same pass. A `break`-on-first-miss
//     bug (treating the first non-fit as "queue full") would starve every job
//     behind an oversized one — a real fairness/liveness hole.
//
//   - NEGATIVE/ZERO QUOTA fail-CLOSED: a Manager built with a non-positive quota
//     must NEVER admit a resource-consuming job (no oversubscription). This pins
//     the gate's behavior when Quota.Total <= 0 so a later "optimization" that
//     drops the subtraction guard (e.g. reverting to additive) cannot fail open.
//
//   - COMPLETE/RESUBMIT accounting: Complete releases EXACTLY the job's Request
//     once; after completion a NEW job with the same id (resubmission of freed
//     capacity) must be accounted independently, with Used() reconciling to the
//     exact sum of admitted Requests — no double-charge, no lost release.
//
//   - CONCURRENT conservation under -race: the single -race run for this pass.
//     Many goroutines Submit/Admit/Complete; at the end Used() MUST equal the
//     recomputed sum of Requests of the currently-Admitted jobs AND stay within
//     [0, Quota.Total]. Catches a lost-update on `used` or a torn map access that
//     the existing concurrent suite's coarser assertions might let slip.
//
// Every probe asserts SINK-SIDE (job State + committed Used() vs Quota), never
// merely "no panic". All probes PASS on the code left behind.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"
)

func adv2ID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("uuid: %v", err)
	}
	return hex.EncodeToString(b)
}

func adv2State(t *testing.T, m *Manager, id string) State {
	t.Helper()
	s, _ := m.State(id)
	return s
}

// TestAdversarial2_WithinPassLiveUsedRejectsThird proves the fits-check reads the
// LIVE `used` updated within the same Admit() pass, not the start-of-pass value.
//
// Setup: quota 10; three paid jobs of Request 5 at descending priority so the
// pass evaluates them a, b, c in that fixed order. a (5) fits -> used 5. b (5)
// fits -> used 10. c (5) must be REJECTED because 5 > 10-10 = 0.
//
// SINK: exactly {a,b} admitted, c Suspended, Used()==10==Quota (never 15).
//
// MUTATION PROOF: if Admit checked each job against the pass-start used (0) — a
// classic batch-admission bug — all three would pass (each 5 <= 10) and Used()
// would commit 15 > quota. The == 10 / c-Suspended assertions reject that.
func TestAdversarial2_WithinPassLiveUsedRejectsThird(t *testing.T) {
	id := adv2ID(t)
	t.Logf("run=%s within-pass-live-used START", id)

	const quota = 10
	m := NewManager(Quota{Total: quota})
	mustSubmit(t, m, Job{ID: "a", Tier: TierPaid, Priority: 3, Request: 5})
	mustSubmit(t, m, Job{ID: "b", Tier: TierPaid, Priority: 2, Request: 5})
	mustSubmit(t, m, Job{ID: "c", Tier: TierPaid, Priority: 1, Request: 5})

	admitted := m.Admit()
	used := m.Used()
	t.Logf("run=%s admitted=%v used=%d", id, admitted, used)

	if len(admitted) != 2 || admitted[0] != "a" || admitted[1] != "b" {
		t.Fatalf("admitted=%v, want [a b] (live-used must reject the 3rd in-pass)", admitted)
	}
	if st := adv2State(t, m, "c"); st != StateSuspended {
		t.Fatalf("WITHIN-PASS OVER-ADMIT: c=%v, want Suspended; fits-check used stale start-of-pass used", st)
	}
	if used != quota {
		t.Fatalf("WITHIN-PASS OVER-ADMIT: Used()=%d, want %d (committed a 3rd 5-unit job past the cap)", used, quota)
	}
}

// TestAdversarial2_NoHeadOfLineBlocking proves an oversized highest-precedence
// job that can NEVER fit does not starve a smaller fitting job behind it.
//
// Setup: quota 10; "big" (paid, prio 100, Request 50) sorts FIRST and never
// fits; "small" (paid, prio 1, Request 4) sorts after it and DOES fit.
//
// SINK: small Admitted, big Suspended, Used()==4.
//
// MUTATION PROOF: if Admit did `break` on the first non-fitting job (treating it
// as "no more room") instead of `continue`, small would be starved (Suspended)
// and Used() would be 0. The small==Admitted / Used()==4 assertions reject that
// liveness hole.
func TestAdversarial2_NoHeadOfLineBlocking(t *testing.T) {
	id := adv2ID(t)
	t.Logf("run=%s no-head-of-line-blocking START", id)

	const quota = 10
	m := NewManager(Quota{Total: quota})
	mustSubmit(t, m, Job{ID: "big", Tier: TierPaid, Priority: 100, Request: 50})
	mustSubmit(t, m, Job{ID: "small", Tier: TierPaid, Priority: 1, Request: 4})

	admitted := m.Admit()
	used := m.Used()
	t.Logf("run=%s admitted=%v used=%d big=%v small=%v", id, admitted, used,
		adv2State(t, m, "big"), adv2State(t, m, "small"))

	if st := adv2State(t, m, "small"); st != StateAdmitted {
		t.Fatalf("STARVATION: small=%v, want Admitted; oversized head-of-line job blocked a fitting job", st)
	}
	if st := adv2State(t, m, "big"); st != StateSuspended {
		t.Fatalf("big=%v, want Suspended (it cannot fit)", st)
	}
	if used != 4 {
		t.Fatalf("STARVATION: Used()=%d, want 4 (small must be admitted past the oversized head)", used)
	}
}

// TestAdversarial2_NonPositiveQuotaFailsClosed pins that a Manager built with a
// non-positive quota never admits a resource-consuming job, and Used() never goes
// positive. This is the fail-CLOSED contract for a degenerate/empty pool.
//
// SINK: across Total in {0, -1, MinInt}, a Request>=1 job stays Suspended and
// Used()==0 (no oversubscription regardless of how negative the cap is).
//
// MUTATION PROOF: reverting the gate to the additive form
// `m.used + j.Request <= m.quota.Total` would, for Total=MinInt and a positive
// Request, still reject (0+1 <= MinInt is false) — so this probe does NOT depend
// on the overflow fix. What it DOES pin: any change that admits a positive-cost
// job into a <=0 pool (e.g. a `>=`/`>` swap or dropping the gate for "empty"
// pools) flips a Suspended->Admitted here and trips Used()>0.
func TestAdversarial2_NonPositiveQuotaFailsClosed(t *testing.T) {
	id := adv2ID(t)
	t.Logf("run=%s non-positive-quota-fail-closed START", id)

	for _, total := range []int{0, -1, math.MinInt} {
		m := NewManager(Quota{Total: total})
		mustSubmit(t, m, Job{ID: "consume", Tier: TierPaid, Priority: 9, Request: 1})

		admitted := m.Admit()
		used := m.Used()
		st := adv2State(t, m, "consume")
		t.Logf("run=%s total=%d admitted=%v consume=%v used=%d", id, total, admitted, st, used)

		if st == StateAdmitted {
			t.Fatalf("FAIL-OPEN: Request=1 job admitted into quota=%d (non-positive pool must admit nothing)", total)
		}
		if used > 0 {
			t.Fatalf("FAIL-OPEN: Used()=%d > 0 for quota=%d; a non-positive pool oversubscribed", used, total)
		}
	}
}

// TestAdversarial2_CompleteReleasesExactlyAndFreesCapacity proves release is
// EXACT (Complete subtracts precisely the job's Request, once) and that the
// freed capacity is then usable by a previously-suspended job that did NOT fit
// before, with Used() reconciling to the exact sum of admitted Requests.
//
// Note on contract: a Completed id is NOT re-submittable — Submit rejects ANY
// duplicate id, completed or not (see Submit's "Submitting a duplicate ID ... is
// a no-op that returns false"). This is by design (ids are permanent), not a
// fail-open: a rejected resubmit cannot oversubscribe. So this probe exercises
// freed-capacity reuse via a DISTINCT waiting job rather than id reuse.
//
// Sequence (quota 10):
//  1. Submit "x" Request 8 and "y" Request 7; Admit -> only x fits (8); y suspended.
//  2. Complete "x" -> Used 0 (released exactly 8, not more/less).
//  3. Admit again -> y now fits (7 <= 10) -> Used 7.
//
// SINK: Used()==8 after first admit, ==0 after release, ==7 after y admits, and
// the final Used() equals the recomputed sum of Admitted Requests.
//
// MUTATION PROOF: if Complete released the WRONG amount (e.g. a fixed 1, or
// double-subtracted) Used() would not be 0 at step 2 and y might wrongly admit or
// stay blocked; if release leaked, the step-3 reconcile (Used()==7==sum) fails.
func TestAdversarial2_CompleteReleasesExactlyAndFreesCapacity(t *testing.T) {
	id := adv2ID(t)
	t.Logf("run=%s complete-release-frees-capacity START", id)

	const quota = 10
	m := NewManager(Quota{Total: quota})

	mustSubmit(t, m, Job{ID: "x", Tier: TierPaid, Priority: 2, Request: 8})
	mustSubmit(t, m, Job{ID: "y", Tier: TierPaid, Priority: 1, Request: 7})

	m.Admit()
	if u := m.Used(); u != 8 {
		t.Fatalf("after admit: Used()=%d, want 8 (only x fits)", u)
	}
	if st := adv2State(t, m, "y"); st != StateSuspended {
		t.Fatalf("y=%v before release, want Suspended (7 does not fit remaining 2)", st)
	}

	if !m.Complete("x") {
		t.Fatalf("Complete(x) returned false")
	}
	if u := m.Used(); u != 0 {
		t.Fatalf("RELEASE WRONG: Used()=%d after Complete(x Request=8), want 0", u)
	}

	m.Admit()
	if st := adv2State(t, m, "y"); st != StateAdmitted {
		t.Fatalf("y=%v after release, want Admitted (7 fits freed quota 10)", st)
	}

	// Reconcile: Used() == sum of Requests of currently-Admitted jobs.
	want := 0
	for jid := range m.jobs {
		if st, _ := m.State(jid); st == StateAdmitted {
			want += m.jobs[jid].Request
		}
	}
	used := m.Used()
	if used != want {
		t.Fatalf("RECONCILE: Used()=%d but sum of admitted Requests=%d", used, want)
	}
	if used != 7 {
		t.Fatalf("RECONCILE: Used()=%d, want 7 after release+admit", used)
	}
	if used > quota {
		t.Fatalf("Used()=%d exceeds quota %d", used, quota)
	}
	t.Logf("run=%s release-frees-capacity reconciled used=%d END", id, used)
}

// TestAdversarial2_ConcurrentConservation is the single -race run for this pass.
// Many goroutines Submit/Admit/Complete over a constrained quota; afterward
// Used() MUST equal the recomputed sum of Requests of the currently-Admitted jobs
// and stay within [0, Quota.Total]. A lost-update on `used` (read-modify-write
// without the lock) or a torn map access either trips -race or makes the
// conservation equality fail.
//
// Run with: go test ./pkg/jobadmit/ -race -run TestAdversarial2_ConcurrentConservation -count=1
func TestAdversarial2_ConcurrentConservation(t *testing.T) {
	id := adv2ID(t)
	t.Logf("run=%s concurrent-conservation START", id)

	const (
		workers    = 12
		perWorker  = 200
		perJob     = 4
		quotaTotal = 64 // at most 16 jobs admitted at once
	)
	m := NewManager(Quota{Total: quotaTotal})

	allIDs := make([]string, 0, workers*perWorker)
	for w := 0; w < workers; w++ {
		for i := 0; i < perWorker; i++ {
			allIDs = append(allIDs, fmt.Sprintf("w%d-j%d", w, i))
		}
	}

	var wg sync.WaitGroup
	var overQuota int64
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				jid := fmt.Sprintf("w%d-j%d", w, i)
				m.Submit(Job{ID: jid, Tier: TierPaid, Priority: i, Request: perJob})
				m.Admit()
				if m.Used() > quotaTotal {
					atomic.AddInt64(&overQuota, 1)
				}
				// Release half to keep capacity churning so later jobs can admit.
				if i%2 == 0 {
					m.Complete(jid)
				}
			}
		}(w)
	}
	// Concurrent readers to race the maps under -race.
	for r := 0; r < workers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				if m.Used() > quotaTotal {
					atomic.AddInt64(&overQuota, 1)
				}
			}
		}()
	}
	wg.Wait()

	if n := atomic.LoadInt64(&overQuota); n != 0 {
		t.Fatalf("OVER-ADMISSION: Used() exceeded quota %d on %d samples", quotaTotal, n)
	}

	// Conservation: Used() == sum of Requests of currently-Admitted jobs.
	want := 0
	for _, jid := range allIDs {
		if st, ok := m.State(jid); ok && st == StateAdmitted {
			want += perJob
		}
	}
	used := m.Used()
	if used != want {
		t.Fatalf("CONSERVATION VIOLATION: Used()=%d but sum of admitted Requests=%d", used, want)
	}
	if used < 0 || used > quotaTotal {
		t.Fatalf("Used()=%d outside [0,%d] after concurrent churn", used, quotaTotal)
	}
	t.Logf("run=%s conservation held: used=%d quota=%d END", id, used, quotaTotal)
}

func mustSubmit(t *testing.T, m *Manager, j Job) {
	t.Helper()
	if !m.Submit(j) {
		t.Fatalf("Submit(%s) returned false", j.ID)
	}
}
