package jobadmit

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func crandID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("uuid: %v", err)
	}
	return hex.EncodeToString(b)
}

// TestConcurrent_AdmitLookupRace is the adversarial -race probe for the exact
// concurrent-by-role profile of a job-admission controller: many goroutines
// SUBMIT jobs (write m.jobs[id]=... and m.state[id]=...) and run Admit/Complete
// (write m.state, m.used) while other goroutines LOOK UP / read job state via
// State() and Used() (read m.state, m.used). With no synchronization these
// unsynchronized map reads concurrent with map writes are a data race and the
// Go runtime aborts the process with the fatal "concurrent map read and map
// write" — the exact class that crashed pkg/slotmigration and raced
// pkg/edgeregistry.
//
// WHY IT BITES: under -race (or even without, via the map-access guard) the
// reader goroutines touch m.state / m.jobs while writer goroutines mutate the
// same maps. Pre-fix (no mutex) this triggers either the race detector or the
// runtime's fatal concurrent-map-access throw. Post-fix (RWMutex) the maps are
// only ever touched under a held lock, so the run is clean.
//
// Run with: go test -race -run TestConcurrent_AdmitLookupRace -count=10 .
func TestConcurrent_AdmitLookupRace(t *testing.T) {
	id := crandID(t)
	t.Logf("run=%s concurrent admit/lookup race probe START", id)

	const (
		writers    = 8
		readers    = 8
		perWriter  = 200
		quotaTotal = 1 << 30 // huge: every submitted job fits so Admit mutates state every pass
	)

	m := NewManager(Quota{Total: quotaTotal})

	var wg sync.WaitGroup
	var submitted int64

	// Writers: Submit + Admit + Complete churn (all WRITE paths).
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				jid := fmt.Sprintf("w%d-j%d", w, i)
				if m.Submit(Job{ID: jid, Tier: TierPaid, Priority: i, Request: 1}) {
					atomic.AddInt64(&submitted, 1)
				}
				m.Admit()
				// Complete the job we just submitted to exercise the release path.
				m.Complete(jid)
			}
		}(w)
	}

	// Readers: concurrent State()/Used() lookups (READ paths) racing the writes.
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			for i := 0; i < perWriter*writers; i++ {
				for w := 0; w < writers; w++ {
					m.State(fmt.Sprintf("w%d-j%d", w, i%perWriter))
				}
				_ = m.Used()
			}
		}(r)
	}

	wg.Wait()
	t.Logf("run=%s concurrent churn done; submitted=%d used=%d END", id, atomic.LoadInt64(&submitted), m.Used())
}

// TestConcurrent_AtMostQuotaNeverOverAdmitted asserts the capacity gate's
// at-most-N invariant under heavy concurrency. A constrained quota admits at
// most floor(Quota.Total / perJobRequest) jobs at any instant. If a concurrent
// Admit race read a stale m.used and let two passes both pass the
// (used+req<=Total) check, Used() would exceed Quota.Total — the over-admission
// bug class (ratelimit/budgetcap). We hammer Submit+Admit from many goroutines
// and assert Used() never exceeds the budget at any sampled moment and at the
// end.
//
// WHY IT BITES: a lost-update race on m.used (read-modify-write without a lock)
// over-admits past the gate; the Used()<=Total assertion (sampled mid-flight by
// readers AND checked at the end) fails. With the RWMutex the read-modify-write
// of m.used inside Admit is atomic w.r.t. other writers, so the budget holds.
func TestConcurrent_AtMostQuotaNeverOverAdmitted(t *testing.T) {
	id := crandID(t)
	t.Logf("run=%s concurrent at-most-quota probe START", id)

	const (
		writers    = 16
		perJob     = 7
		quotaTotal = 70 // at most 10 jobs admitted at once
		perWriter  = 150
	)

	m := NewManager(Quota{Total: quotaTotal})

	var wg sync.WaitGroup
	var maxSeen int64
	var violations int64

	bump := func(v int) {
		for {
			cur := atomic.LoadInt64(&maxSeen)
			if int64(v) <= cur || atomic.CompareAndSwapInt64(&maxSeen, cur, int64(v)) {
				return
			}
		}
	}

	// Writers churn submit/admit/complete on a constrained budget.
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				jid := fmt.Sprintf("w%d-j%d", w, i)
				m.Submit(Job{ID: jid, Tier: TierPaid, Priority: i, Request: perJob})
				m.Admit()
				if u := m.Used(); u > quotaTotal {
					atomic.AddInt64(&violations, 1)
				}
				bump(m.Used())
				// Release some to keep the system churning capacity.
				if i%2 == 0 {
					m.Complete(jid)
				}
			}
		}(w)
	}

	// Readers sample Used() to catch a transient over-admit.
	for r := 0; r < writers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter*4; i++ {
				if u := m.Used(); u > quotaTotal {
					atomic.AddInt64(&violations, 1)
				}
			}
		}()
	}

	wg.Wait()

	if v := atomic.LoadInt64(&violations); v != 0 {
		t.Fatalf("OVER-ADMISSION: Used() exceeded quota %d on %d samples (max seen=%d)", quotaTotal, v, atomic.LoadInt64(&maxSeen))
	}
	if m.Used() > quotaTotal {
		t.Fatalf("final Used()=%d exceeds quota %d", m.Used(), quotaTotal)
	}
	if m.Used() < 0 {
		t.Fatalf("final Used()=%d negative (lost-update on release)", m.Used())
	}
	t.Logf("run=%s at-most-quota held: max-used-seen=%d quota=%d final=%d END", id, atomic.LoadInt64(&maxSeen), quotaTotal, m.Used())
}

// TestConcurrent_LifecycleAndConservation verifies lifecycle + conservation
// after concurrent churn: every successfully-submitted job is findable (State
// returns ok), double-submit of the same ID is rejected exactly once (coalesced,
// never duplicated), and after the dust settles Used() exactly equals the sum of
// the Requests of the currently-Admitted jobs (conservation: no lost/duplicate
// accounting).
//
// WHY IT BITES: a racy map write could drop a job (State !ok for a submitted id),
// or a lost-update on m.used would make Used() diverge from the recomputed
// sum-of-admitted-requests — the conservation assertion catches both. Double
// submit counted as success twice would mean the same id admitted/accounted
// twice.
func TestConcurrent_LifecycleAndConservation(t *testing.T) {
	id := crandID(t)
	t.Logf("run=%s lifecycle+conservation probe START", id)

	const (
		writers    = 12
		perWriter  = 120
		quotaTotal = 200
		perJob     = 3
	)

	m := NewManager(Quota{Total: quotaTotal})

	// Each id is targeted by TWO goroutines to force double-submit contention;
	// exactly one Submit per id must win.
	var wg sync.WaitGroup
	type result struct {
		mu       sync.Mutex
		accepted map[string]int
	}
	res := &result{accepted: make(map[string]int)}

	ids := make([]string, 0, writers*perWriter)
	for w := 0; w < writers; w++ {
		for i := 0; i < perWriter; i++ {
			ids = append(ids, fmt.Sprintf("j-%d-%d", w, i))
		}
	}

	// Two waves of submitters over the SAME id space -> double-submit races.
	for wave := 0; wave < 2; wave++ {
		for w := 0; w < writers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for i := 0; i < perWriter; i++ {
					jid := fmt.Sprintf("j-%d-%d", w, i)
					if m.Submit(Job{ID: jid, Tier: TierPaid, Priority: 1, Request: perJob}) {
						res.mu.Lock()
						res.accepted[jid]++
						res.mu.Unlock()
					}
					m.Admit()
				}
			}(w)
		}
	}
	wg.Wait()

	// Lifecycle: every submitted id is findable.
	for _, jid := range ids {
		if _, ok := m.State(jid); !ok {
			t.Fatalf("LOST JOB: submitted id %s not findable", jid)
		}
	}

	// Double-submit coalesced: each id accepted exactly once.
	for jid, n := range res.accepted {
		if n != 1 {
			t.Fatalf("DUPLICATE SUBMIT: id %s accepted %d times, want exactly 1", jid, n)
		}
	}
	if len(res.accepted) != len(ids) {
		t.Fatalf("accepted %d distinct ids, want %d (lost submit)", len(res.accepted), len(ids))
	}

	// Conservation: Used() == sum of Requests of currently-Admitted jobs.
	wantUsed := 0
	for _, jid := range ids {
		if st, ok := m.State(jid); ok && st == StateAdmitted {
			wantUsed += perJob
		}
	}
	if m.Used() != wantUsed {
		t.Fatalf("CONSERVATION VIOLATION: Used()=%d but sum of admitted requests=%d", m.Used(), wantUsed)
	}
	if m.Used() > quotaTotal {
		t.Fatalf("Used()=%d exceeds quota %d", m.Used(), quotaTotal)
	}
	t.Logf("run=%s lifecycle ok; %d ids each accepted once; conservation Used=%d END", id, len(ids), m.Used())
}
