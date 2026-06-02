package workclaim

import (
	"fmt"
	"sync"
	"testing"
)

// runUUID is a fixed per-file forensic anchor so closure evidence in the test
// log is traceable to this exact suite (HXC-1585).
const runUUID = "workclaim-HXC-1585-7e3a1c9d"

// TestExactlyOnceDrain covers CLOSURE clause 1: N concurrent workers drain a
// queue of M tasks and EVERY task is claimed EXACTLY ONCE — the union of all
// claimed ids equals the full task set with no duplicates and no missing.
//
// Mutation: if Claim's state mutation (q.st[id] = stateClaimed) is removed so
// the claim-lock no longer hides a taken task, two workers claim the same id;
// the duplicate-detection map below records count>1 and this test FAILS (also
// surfaced by -race on the shared claimedCounts under the per-worker locks).
func TestExactlyOnceDrain(t *testing.T) {
	const (
		M = 1000 // tasks
		N = 16   // concurrent workers
	)

	q := New()
	want := make(map[string]struct{}, M)
	for i := 0; i < M; i++ {
		id := fmt.Sprintf("task-%04d", i)
		q.Add(id)
		want[id] = struct{}{}
	}

	beforeUnclaimed := q.Unclaimed()
	beforeClaimed := q.Claimed()
	t.Logf("[%s] before: unclaimed=%d claimed=%d (M=%d N=%d)", runUUID, beforeUnclaimed, beforeClaimed, M, N)

	// Each worker collects the ids IT claimed into its own slice (no shared
	// mutation during the hot loop), so a duplicate hand-out shows up as the
	// same id appearing across two workers' slices.
	perWorker := make([][]string, N)
	var wg sync.WaitGroup
	for w := 0; w < N; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for {
				id, ok := q.Claim()
				if !ok {
					return
				}
				perWorker[w] = append(perWorker[w], id)
			}
		}(w)
	}
	wg.Wait()

	// Aggregate every claimed id and detect duplicates.
	seen := make(map[string]int, M)
	total := 0
	dist := make([]int, N)
	for w := 0; w < N; w++ {
		dist[w] = len(perWorker[w])
		total += len(perWorker[w])
		for _, id := range perWorker[w] {
			seen[id]++
		}
	}

	// No duplicate claims.
	for id, c := range seen {
		if c != 1 {
			t.Fatalf("[%s] task %q claimed %d times (want exactly 1) — SKIP LOCKED violated", runUUID, id, c)
		}
	}
	// No missing tasks: claimed set == full task set.
	if len(seen) != M {
		t.Fatalf("[%s] distinct claimed=%d want=%d (tasks dropped)", runUUID, len(seen), M)
	}
	for id := range want {
		if _, ok := seen[id]; !ok {
			t.Fatalf("[%s] task %q was never claimed (dropped)", runUUID, id)
		}
	}

	afterUnclaimed := q.Unclaimed()
	afterClaimed := q.Claimed()
	t.Logf("[%s] after: unclaimed=%d claimed=%d total-handed-out=%d distinct=%d", runUUID, afterUnclaimed, afterClaimed, total, len(seen))
	t.Logf("[%s] per-worker distribution: %v (sum=%d)", runUUID, dist, total)

	if afterUnclaimed != 0 {
		t.Fatalf("[%s] after drain unclaimed=%d want 0", runUUID, afterUnclaimed)
	}
	if afterClaimed != M {
		t.Fatalf("[%s] after drain claimed=%d want %d", runUUID, afterClaimed, M)
	}
}

// TestNoOverClaim covers CLOSURE clause 2: total claims across all workers ==
// M exactly, never M+something. A drained queue yields no extra phantom claims.
//
// Mutation: if Claim returned ok==true for an already-claimed/empty slot (e.g.
// dropping the stateUnclaimed guard), total would exceed M and this FAILS.
func TestNoOverClaim(t *testing.T) {
	const (
		M = 500
		N = 12
	)
	q := New()
	for i := 0; i < M; i++ {
		q.Add(fmt.Sprintf("t-%d", i))
	}
	t.Logf("[%s] before: total-added=%d unclaimed=%d", runUUID, q.Len(), q.Unclaimed())

	var mu sync.Mutex
	total := 0
	var wg sync.WaitGroup
	for w := 0; w < N; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := 0
			for {
				_, ok := q.Claim()
				if !ok {
					break
				}
				local++
			}
			mu.Lock()
			total += local
			mu.Unlock()
		}()
	}
	wg.Wait()

	t.Logf("[%s] after: total-claims=%d want=%d claimed-count=%d", runUUID, total, M, q.Claimed())
	if total != M {
		t.Fatalf("[%s] total claims=%d want exactly %d (over/under claim)", runUUID, total, M)
	}
	if q.Claimed() != M {
		t.Fatalf("[%s] claimed bookkeeping=%d want %d", runUUID, q.Claimed(), M)
	}
}

// TestEmptyQueueClaim covers CLOSURE clause 3: Claim on a drained (or never
// filled) queue returns ok==false with no phantom task.
//
// Mutation: if Claim invented a task on empty (e.g. returned a zero id with
// ok==true), the assertions below FAIL.
func TestEmptyQueueClaim(t *testing.T) {
	// Never-filled queue.
	q := New()
	if id, ok := q.Claim(); ok {
		t.Fatalf("[%s] empty queue Claim returned (%q,%v) want (\"\",false)", runUUID, id, ok)
	}

	// Fill, fully drain, then Claim again.
	const M = 8
	for i := 0; i < M; i++ {
		q.Add(fmt.Sprintf("e-%d", i))
	}
	drained := 0
	for {
		_, ok := q.Claim()
		if !ok {
			break
		}
		drained++
	}
	t.Logf("[%s] drained=%d (M=%d) then claiming on empty", runUUID, drained, M)
	if drained != M {
		t.Fatalf("[%s] drained=%d want %d", runUUID, drained, M)
	}
	for i := 0; i < 5; i++ {
		if id, ok := q.Claim(); ok {
			t.Fatalf("[%s] post-drain Claim #%d returned phantom (%q,%v)", runUUID, i, id, ok)
		}
	}
	t.Logf("[%s] post-drain: 5x Claim all returned ok=false (no phantom)", runUUID)
}

// TestCompleteLifecycle proves Complete's state transitions and error paths so
// the counts API reflects real end-user-visible state.
//
// Mutation: if Complete did not change state to stateCompleted, Claimed() would
// stay 1 and Completed() 0; the assertions FAIL.
func TestCompleteLifecycle(t *testing.T) {
	q := New()
	q.Add("a")

	if err := q.Complete("a"); err != ErrNotClaimed {
		t.Fatalf("[%s] Complete(unclaimed) err=%v want ErrNotClaimed", runUUID, err)
	}
	if err := q.Complete("missing"); err != ErrUnknownTask {
		t.Fatalf("[%s] Complete(missing) err=%v want ErrUnknownTask", runUUID, err)
	}

	id, ok := q.Claim()
	if !ok || id != "a" {
		t.Fatalf("[%s] Claim got (%q,%v) want (\"a\",true)", runUUID, id, ok)
	}
	t.Logf("[%s] before complete: claimed=%d completed=%d", runUUID, q.Claimed(), q.Completed())
	if err := q.Complete("a"); err != nil {
		t.Fatalf("[%s] Complete(claimed) err=%v want nil", runUUID, err)
	}
	t.Logf("[%s] after complete: claimed=%d completed=%d", runUUID, q.Claimed(), q.Completed())
	if q.Claimed() != 0 {
		t.Fatalf("[%s] claimed=%d want 0 after Complete", runUUID, q.Claimed())
	}
	if q.Completed() != 1 {
		t.Fatalf("[%s] completed=%d want 1 after Complete", runUUID, q.Completed())
	}
	// Double-complete must fail (already completed, not claimed).
	if err := q.Complete("a"); err != ErrNotClaimed {
		t.Fatalf("[%s] double Complete err=%v want ErrNotClaimed", runUUID, err)
	}
}

// TestAddIdempotent proves re-Adding an id does not create a duplicate slot or
// reset a claimed task, so the exactly-once accounting cannot be inflated.
//
// Mutation: if Add appended unconditionally, Len() would be 2 and the task
// could be claimed twice; the assertions FAIL.
func TestAddIdempotent(t *testing.T) {
	q := New()
	q.Add("x")
	q.Add("x")
	if q.Len() != 1 {
		t.Fatalf("[%s] Len=%d want 1 after duplicate Add", runUUID, q.Len())
	}
	id, ok := q.Claim()
	if !ok || id != "x" {
		t.Fatalf("[%s] Claim got (%q,%v) want (\"x\",true)", runUUID, id, ok)
	}
	// Re-Add of an already-claimed id must not make it claimable again.
	q.Add("x")
	if _, ok := q.Claim(); ok {
		t.Fatalf("[%s] re-Add resurrected a claimed task", runUUID)
	}
	t.Logf("[%s] idempotent Add: Len=1, claimed once, no resurrection", runUUID)
}
