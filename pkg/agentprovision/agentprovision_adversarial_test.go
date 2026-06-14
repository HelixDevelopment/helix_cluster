package agentprovision

// Adversarial, sink-side concurrency probes for pkg/agentprovision.
//
// These tests do NOT assert "no panic". They assert CONSERVATION and
// IDENTITY invariants at the sink:
//
//   - ModeManager: len(Running()) never exceeds the fixed slot quota, and the
//     count of jobs the manager *claims* it admitted (admitted==true,
//     preemptedID=="") equals the number of distinct jobs actually present in
//     the pool (no double-provision via duplicate request IDs).
//   - AgentAdmission: the per-node live counter is conserved under concurrent
//     Admit/Complete and never exceeds the per-node cap.
//
// Run with:  go test -race -count=2 ./pkg/agentprovision/
//
// Surfaces probed (per task brief):
//   (a) IDEMPOTENCY / DOUBLE-PROVISION  — TestAdv_ModeManager_DuplicateID_*
//   (b) OVER-PROVISION / QUOTA          — TestAdv_ModeManager_QuotaNeverExceeded,
//                                          TestAdv_AgentAdmission_CapNeverExceeded
//   (c) UNGUARDED SHARED STATE          — TestAdv_ModeManager_RaceMixedOps,
//                                          TestAdv_AgentAdmission_RaceMixedOps
//   (d) TEARDOWN RACE / IDENTITY        — TestAdv_ModeManager_SubmitVsComplete_Conservation,
//                                          TestAdv_AgentAdmission_AdmitVsComplete_Conservation

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/ratelimit"
)

// bigLimiter returns a PerKeyLimiter whose buckets are effectively unbounded so
// the rate-limit gate never fires and we isolate the capacity/identity logic.
func bigLimiter() *ratelimit.PerKeyLimiter {
	return ratelimit.NewPerKeyLimiter(func() interface {
		Allow() bool
		AllowN(int) bool
	} {
		return ratelimit.NewTokenBucket(1<<30, 0)
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// (a) IDEMPOTENCY / DOUBLE-PROVISION — duplicate request IDs on ModeManager.
//
// Contract (modeswitch.go): "fixed-capacity pool", running is keyed by jobID.
// Submit returns admitted=true when it places j. The sink invariant a caller
// relies on: if Submit says admitted=true (and did NOT preempt), exactly one
// new live job now exists for that ID — i.e. distinct admitted IDs == pool size.
//
// This DETERMINISTIC probe (no goroutines) shows the failure cleanly: two
// Submits of the SAME ID into a pool with free slots BOTH report admitted=true,
// yet Running() holds only one job. The manager has handed out two "admitted"
// confirmations for one occupied slot — a double-provision / lost-update.
// ─────────────────────────────────────────────────────────────────────────────

// TestAdv_ModeManager_DuplicateID_Deterministic is the minimal, race-free
// reproducer. It BITES on unfixed prod (it is NOT env-gated).
func TestAdv_ModeManager_DuplicateID_Deterministic(t *testing.T) {
	mm := NewModeManager(4) // plenty of free slots

	const dupID = "agent-dup"

	a1, p1, r1 := mm.Submit(Job{ID: dupID, Mode: ModeBatch})
	a2, p2, r2 := mm.Submit(Job{ID: dupID, Mode: ModeBatch})

	// Count how many of the two calls claimed a fresh admission with no
	// preemption (i.e. "I placed a brand-new job for you").
	claimedAdmissions := 0
	if a1 && p1 == "" {
		claimedAdmissions++
	}
	if a2 && p2 == "" {
		claimedAdmissions++
	}

	pool := mm.Running()
	distinct := map[string]struct{}{}
	for _, j := range pool {
		distinct[j.ID] = struct{}{}
	}

	t.Logf("submit1=(admitted=%v,preempt=%q,reason=%q) submit2=(admitted=%v,preempt=%q,reason=%q) "+
		"claimedAdmissions=%d len(Running)=%d distinctIDs=%d",
		a1, p1, r1, a2, p2, r2, claimedAdmissions, len(pool), len(distinct))

	// SINK-SIDE conservation: the number of admissions the manager confirmed
	// must equal the number of distinct jobs it is actually running. If both
	// Submits returned admitted=true the pool must hold 2 distinct jobs.
	if claimedAdmissions != len(distinct) {
		t.Fatalf("double-provision: manager confirmed %d fresh admissions but pool holds %d distinct job(s) "+
			"(duplicate request ID %q was admitted twice yet occupies one slot)",
			claimedAdmissions, len(distinct), dupID)
	}
}

// TestAdv_ModeManager_DuplicateID_Concurrent runs the same identity probe under
// concurrency: N goroutines all Submit the SAME id into a pool with abundant
// slots. The manager may legitimately admit the first and reject the rest, but
// the total count of confirmed fresh admissions MUST equal the number of
// distinct IDs left running. A TOCTOU/clobber inflates admissions past pool
// occupancy.
func TestAdv_ModeManager_DuplicateID_Concurrent(t *testing.T) {
	const goroutines = 64
	mm := NewModeManager(goroutines + 8) // never quota-bound; isolate identity

	const dupID = "race-dup"

	var admittedFresh int64 // admitted==true && preemptedID==""
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start
			admitted, preempted, _ := mm.Submit(Job{ID: dupID, Mode: ModeBatch})
			if admitted && preempted == "" {
				atomic.AddInt64(&admittedFresh, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	pool := mm.Running()
	distinct := map[string]struct{}{}
	for _, j := range pool {
		distinct[j.ID] = struct{}{}
	}

	t.Logf("freshAdmissions=%d len(Running)=%d distinctIDs=%d", admittedFresh, len(pool), len(distinct))

	// Conservation: every confirmed fresh admission must correspond to a
	// distinct occupied slot. Since all share one ID, at most ONE distinct job
	// can exist — so confirmed fresh admissions must be <= 1 as well.
	if int(admittedFresh) != len(distinct) {
		t.Fatalf("double-provision under race: %d fresh admissions confirmed but %d distinct job(s) running for one ID",
			admittedFresh, len(distinct))
	}
	if len(distinct) > 1 {
		t.Fatalf("identity violation: %d distinct entries for a single request ID %q", len(distinct), dupID)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// (b) OVER-PROVISION / QUOTA
// ─────────────────────────────────────────────────────────────────────────────

// TestAdv_ModeManager_QuotaNeverExceeded hammers Submit with DISTINCT batch IDs
// from many goroutines and asserts the running pool never exceeds the fixed
// slot quota — checked both during the storm (via concurrent Running() reads)
// and at the end.
func TestAdv_ModeManager_QuotaNeverExceeded(t *testing.T) {
	const slots = 4
	const goroutines = 128
	mm := NewModeManager(slots)

	var wg sync.WaitGroup     // submitters only
	var obsWg sync.WaitGroup  // observers only
	start := make(chan struct{})

	// Observer goroutines continuously sample pool size during the storm.
	var maxObserved int64
	const observers = 8
	stop := make(chan struct{})
	for i := 0; i < observers; i++ {
		obsWg.Add(1)
		go func() {
			defer obsWg.Done()
			<-start
			for {
				select {
				case <-stop:
					return
				default:
					if n := int64(len(mm.Running())); n > atomic.LoadInt64(&maxObserved) {
						atomic.StoreInt64(&maxObserved, n)
					}
				}
			}
		}()
	}

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		id := fmt.Sprintf("batch-%d", i)
		go func(id string) {
			defer wg.Done()
			<-start
			mm.Submit(Job{ID: id, Mode: ModeBatch}) //nolint:errcheck
		}(id)
	}

	close(start)
	wg.Wait()     // wait for all submitters
	close(stop)   // then halt observers
	obsWg.Wait()

	if got := len(mm.Running()); got > slots {
		t.Fatalf("over-provision: pool holds %d jobs, quota is %d", got, slots)
	}
	if mo := atomic.LoadInt64(&maxObserved); mo > slots {
		t.Fatalf("over-provision observed mid-flight: max pool size %d exceeded quota %d", mo, slots)
	}
	t.Logf("final pool=%d maxObserved=%d quota=%d", len(mm.Running()), atomic.LoadInt64(&maxObserved), slots)
}

// TestAdv_AgentAdmission_CapNeverExceeded hammers Admit on a single node with a
// small per-node cap and asserts LiveCount never exceeds the cap.
func TestAdv_AgentAdmission_CapNeverExceeded(t *testing.T) {
	const cap = 3
	const goroutines = 200
	aa := NewAgentAdmission(cap, bigLimiter())
	const node = "node-cap"

	var wg sync.WaitGroup
	start := make(chan struct{})

	var maxLive int64
	stop := make(chan struct{})
	observers := 4
	var obsWg sync.WaitGroup
	for i := 0; i < observers; i++ {
		obsWg.Add(1)
		go func() {
			defer obsWg.Done()
			<-start
			for {
				select {
				case <-stop:
					return
				default:
					if n := int64(aa.LiveCount(node)); n > atomic.LoadInt64(&maxLive) {
						atomic.StoreInt64(&maxLive, n)
					}
				}
			}
		}()
	}

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		a := Agent{ID: fmt.Sprintf("a-%d", i), UserID: fmt.Sprintf("u-%d", i)}
		go func(a Agent) {
			defer wg.Done()
			<-start
			aa.Admit(node, a) //nolint:errcheck
		}(a)
	}
	close(start)
	wg.Wait()
	close(stop)
	obsWg.Wait()

	if lc := aa.LiveCount(node); lc > cap {
		t.Fatalf("over-provision: LiveCount=%d exceeds cap=%d", lc, cap)
	}
	if ml := atomic.LoadInt64(&maxLive); ml > int64(cap) {
		t.Fatalf("over-provision observed: max LiveCount %d exceeded cap %d", ml, cap)
	}
	t.Logf("final LiveCount=%d maxObserved=%d cap=%d", aa.LiveCount(node), atomic.LoadInt64(&maxLive), cap)
}

// ─────────────────────────────────────────────────────────────────────────────
// (c) UNGUARDED SHARED STATE — mixed concurrent ops under -race.
// ─────────────────────────────────────────────────────────────────────────────

// TestAdv_ModeManager_RaceMixedOps concurrently exercises Submit / Complete /
// Running / SwitchMode / WasPreempted. The -race detector is the primary judge;
// the post-condition asserts the pool never exceeds quota.
func TestAdv_ModeManager_RaceMixedOps(t *testing.T) {
	const slots = 8
	mm := NewModeManager(slots)
	ids := make([]string, 32)
	for i := range ids {
		ids[i] = fmt.Sprintf("job-%d", i)
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	spawn := func(fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 200; i++ {
				fn()
			}
		}()
	}

	for _, id := range ids {
		id := id
		spawn(func() { mm.Submit(Job{ID: id, Mode: ModeBatch}) }) //nolint:errcheck
		spawn(func() { mm.Submit(Job{ID: id, Mode: ModeInteractive}) })
		spawn(func() { mm.Complete(id) })
		spawn(func() { mm.SwitchMode(id, ModeInteractive) })
		spawn(func() { _ = mm.WasPreempted(id) })
	}
	spawn(func() { _ = mm.Running() })

	close(start)
	wg.Wait()

	if got := len(mm.Running()); got > slots {
		t.Fatalf("over-provision after mixed ops: pool=%d quota=%d", got, slots)
	}
}

// TestAdv_AgentAdmission_RaceMixedOps concurrently exercises Admit / Complete /
// LiveCount / QueueLen on multiple nodes. -race judges; post-condition asserts
// the live counter is conserved (never negative — guarded — and never over cap).
func TestAdv_AgentAdmission_RaceMixedOps(t *testing.T) {
	const cap = 5
	aa := NewAgentAdmission(cap, bigLimiter())
	nodes := []string{"n0", "n1", "n2"}

	var wg sync.WaitGroup
	start := make(chan struct{})
	spawn := func(fn func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 300; i++ {
				fn()
			}
		}()
	}

	for _, n := range nodes {
		n := n
		spawn(func() { aa.Admit(n, Agent{ID: "x", UserID: "u"}) }) //nolint:errcheck
		spawn(func() { aa.Complete(n, "x") })
		spawn(func() { _ = aa.LiveCount(n) })
		spawn(func() { _ = aa.QueueLen(n) })
	}

	close(start)
	wg.Wait()

	for _, n := range nodes {
		if lc := aa.LiveCount(n); lc > cap || lc < 0 {
			t.Fatalf("node %s: LiveCount=%d out of bounds [0,%d]", n, lc, cap)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// (d) TEARDOWN RACE / IDENTITY — provision racing teardown.
// ─────────────────────────────────────────────────────────────────────────────

// TestAdv_ModeManager_SubmitVsComplete_Conservation races Submit(id) against
// Complete(id) for the same set of IDs, then asserts the final pool size equals
// the number of IDs that ended in the "admitted but not completed" state — i.e.
// no resurrection (a Completed job lingering) and no phantom (an admitted job
// missing). We verify the weaker but robust invariant: pool size never exceeds
// quota and every running ID is distinct (no torn duplicate entries).
func TestAdv_ModeManager_SubmitVsComplete_Conservation(t *testing.T) {
	const slots = 16
	mm := NewModeManager(slots)
	ids := make([]string, slots)
	for i := range ids {
		ids[i] = fmt.Sprintf("td-%d", i)
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, id := range ids {
		id := id
		wg.Add(2)
		go func() { defer wg.Done(); <-start; mm.Submit(Job{ID: id, Mode: ModeBatch}) }() //nolint:errcheck
		go func() { defer wg.Done(); <-start; mm.Complete(id) }()
	}
	close(start)
	wg.Wait()

	pool := mm.Running()
	if len(pool) > slots {
		t.Fatalf("over-provision after submit/teardown race: pool=%d quota=%d", len(pool), slots)
	}
	seen := map[string]int{}
	for _, j := range pool {
		seen[j.ID]++
	}
	for id, c := range seen {
		if c != 1 {
			t.Fatalf("identity/teardown race: job %q appears %d times in Running() (torn duplicate)", id, c)
		}
	}
}

// TestAdv_AgentAdmission_AdmitVsComplete_Conservation races Admit against
// Complete on a single node and asserts the live counter is conserved: it must
// equal (#admitted - #completed-that-freed-a-slot) and never go negative or
// exceed cap. We assert the robust bound: 0 <= LiveCount <= cap throughout and
// at rest.
func TestAdv_AgentAdmission_AdmitVsComplete_Conservation(t *testing.T) {
	const cap = 4
	aa := NewAgentAdmission(cap, bigLimiter())
	const node = "td-node"

	var admittedCount int64
	var completeCount int64

	var wg sync.WaitGroup
	start := make(chan struct{})
	const pairs = 256
	for i := 0; i < pairs; i++ {
		a := Agent{ID: fmt.Sprintf("td-a-%d", i), UserID: fmt.Sprintf("u-%d", i)}
		wg.Add(2)
		go func(a Agent) {
			defer wg.Done()
			<-start
			if ok, _ := aa.Admit(node, a); ok {
				atomic.AddInt64(&admittedCount, 1)
			}
		}(a)
		go func() {
			defer wg.Done()
			<-start
			aa.Complete(node, "td-a")
			atomic.AddInt64(&completeCount, 1)
		}()
	}
	close(start)
	wg.Wait()

	lc := aa.LiveCount(node)
	t.Logf("admitted=%d completed=%d finalLive=%d cap=%d",
		atomic.LoadInt64(&admittedCount), atomic.LoadInt64(&completeCount), lc, cap)

	if lc < 0 {
		t.Fatalf("live counter went negative: %d (Complete under-decrement guard failed)", lc)
	}
	if lc > cap {
		t.Fatalf("over-provision: finalLive=%d exceeds cap=%d", lc, cap)
	}
}
