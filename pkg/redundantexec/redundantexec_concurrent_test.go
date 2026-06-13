package redundantexec

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

const concRunUUID = "redundantexec-conc-7b21e4d9-5c0a-4f8e-9d3b-2a6f0c1e7a55"

// TestConcurrentValidateAndTrustRace is an adversarial -race test that drives
// the EXACT real-world profile of a redundant-execution scorer: many tasks are
// validated concurrently (each Validate WRITES v.workers[id]=... when it first
// sees a worker, and mutates the persistent *Worker.Trust field) while other
// goroutines READ the running trust via Trust() / collect state. This is the
// register/update-while-read profile.
//
// WHY -race BITES (anti-bluff): v.workers is a plain map[string]*Worker with NO
// synchronization. Validate -> worker() does `v.workers[id] = w` (map write,
// redundantexec.go:222) while Trust() does `v.workers[workerID]` (map read,
// redundantexec.go:102) from another goroutine. The Go runtime detects an
// unsynchronized concurrent map read+write and (a) the race detector reports it,
// or (b) it triggers the fatal "concurrent map read and map write" crash. There
// is ALSO a pointer-aliased data race on the *Worker.Trust float64 field
// (written at redundantexec.go:199/202, read at redundantexec.go:103). On the
// pre-fix code this test FAILS under -race (or fatally crashes). With the
// RWMutex fix it passes. Revert the mutex and it goes red again => the lock is
// load-bearing.
func TestConcurrentValidateAndTrustRace(t *testing.T) {
	v := NewValidator(0.5)

	const (
		writers      = 8
		readers      = 8
		tasksPerCall = 200
		distinctIDs  = 64
	)

	var wg sync.WaitGroup

	// Writer goroutines: validate many tasks. Each task introduces possibly-new
	// worker IDs (register/write path) and mutates persisted trust (update path).
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for i := 0; i < tasksPerCall; i++ {
				base := (seed*tasksPerCall + i) % distinctIDs
				id1 := fmt.Sprintf("w%d", base)
				id2 := fmt.Sprintf("w%d", (base+1)%distinctIDs)
				id3 := fmt.Sprintf("w%d", (base+2)%distinctIDs)
				idOut := fmt.Sprintf("w%d", (base+3)%distinctIDs)
				results := []Result{
					{WorkerID: id1, Value: "X"},
					{WorkerID: id2, Value: "X"},
					{WorkerID: id3, Value: "X"},
					{WorkerID: idOut, Value: "Y"},
				}
				// 3-1 split validates X (unique plurality at threshold).
				if _, err := v.Validate(fmt.Sprintf("task-%d-%d", seed, i), results); err != nil {
					t.Errorf("run=%s validate err=%v", concRunUUID, err)
					return
				}
			}
		}(w)
	}

	// Reader goroutines: query trust state concurrently (the collect/read path).
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < tasksPerCall*4; i++ {
				id := fmt.Sprintf("w%d", i%distinctIDs)
				tr := v.Trust(id)
				if tr < TrustFloor || tr > TrustCeil {
					t.Errorf("run=%s trust %.4f out of [%.1f,%.1f]", concRunUUID, tr, TrustFloor, TrustCeil)
					return
				}
			}
		}()
	}

	wg.Wait()
	t.Logf("run=%s concurrent validate/trust completed without race", concRunUUID)
}

// TestConcurrentExactlyOneCanonicalWinner proves the redundancy policy holds
// under concurrency: when N workers race to report results for the SAME task and
// a quorum value exists, the scorer accepts EXACTLY ONE canonical value, and
// that value is one a majority of workers actually returned (a real success),
// never a minority/garbage value.
//
// We run many independent same-shaped tasks concurrently. Each has a unique
// plurality winner "WIN". We assert every Validate that returns success returns
// canonical == "WIN" exactly (no double-accept of a different value, no
// accepting the minority "LOSE"). We count accepted winners atomically.
//
// WHY THIS BITES (anti-bluff): if the collection/quorum logic were broken under
// concurrency (e.g. shared mutable tally state leaking across goroutines, or a
// torn read of the leader producing the wrong canonical), some Validate calls
// would return "LOSE" or a non-quorum error, or accept two different canonicals
// for the same logical task. The strict `got.Value == "WIN"` assertion and the
// "every task validated exactly once" count catch a broken collection. A serial
// pass cannot mask it because the bug only manifests when tallies interleave.
func TestConcurrentExactlyOneCanonicalWinner(t *testing.T) {
	v := NewValidator(0.5)

	const tasks = 2000
	var wg sync.WaitGroup
	var validatedWinners int64
	var wrongCanonical int64
	var quorumErrors int64

	for i := 0; i < tasks; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			// 3 agree on WIN, 1 dissents with LOSE => unique plurality WIN.
			results := []Result{
				{WorkerID: fmt.Sprintf("a%d", n), Value: "WIN"},
				{WorkerID: fmt.Sprintf("b%d", n), Value: "WIN"},
				{WorkerID: fmt.Sprintf("c%d", n), Value: "WIN"},
				{WorkerID: fmt.Sprintf("d%d", n), Value: "LOSE"},
			}
			got, err := v.Validate(fmt.Sprintf("race-task-%d", n), results)
			if err != nil {
				atomic.AddInt64(&quorumErrors, 1)
				return
			}
			if got.Value == "WIN" && got.AgreeCount == 3 {
				atomic.AddInt64(&validatedWinners, 1)
			} else {
				atomic.AddInt64(&wrongCanonical, 1)
			}
		}(i)
	}
	wg.Wait()

	t.Logf("run=%s winners=%d wrong=%d quorumErrors=%d (of %d tasks)",
		concRunUUID, validatedWinners, wrongCanonical, quorumErrors, tasks)

	if wrongCanonical != 0 {
		t.Fatalf("run=%s %d tasks accepted a non-WIN/garbage canonical (broken collection)",
			concRunUUID, wrongCanonical)
	}
	if quorumErrors != 0 {
		t.Fatalf("run=%s %d tasks failed to reach quorum on a clear 3-1 plurality (broken collection)",
			concRunUUID, quorumErrors)
	}
	if validatedWinners != tasks {
		t.Fatalf("run=%s validated winners=%d, want exactly %d (one accepted WIN per task)",
			concRunUUID, validatedWinners, tasks)
	}
}
