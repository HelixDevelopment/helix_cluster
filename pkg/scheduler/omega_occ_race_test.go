package scheduler

// Omega-model optimistic-concurrency control (OCC) — barrier-synchronized race proof.
//
// This file complements hxc1081_optimistic_test.go with a HARD-BARRIER race that
// forces every competing scheduler goroutine to read the SAME initial version and
// then commit at (as close as the runtime allows to) the same instant. The point
// is to make the CAS / version check BITE: with a single scarce resource and N
// schedulers presenting the identical stale version, exactly ONE commit may win;
// every other commit MUST be rejected with a version-mismatch conflict. If the CAS
// were a no-op, multiple commits would succeed and the node would be oversubscribed
// (GPU < 0) — which the assertions below catch.
//
// The scheduler's OCC API exercised here:
//   - cell state:      *Scheduler{ nodes, version uint64, placements } guarded by RWMutex
//   - propose/commit:  ScheduleOptimistic(job, expectedVersion)
//   - conflict signal: error "optimistic concurrency conflict: expected version X, got Y"
//   - version field:   Version() — advances by exactly 1 per successful commit
//   - sink-side state:  GetNode().AvailableResources.GPU, RunningPlacements()

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// isOCCConflict reports whether err is the optimistic-concurrency version-mismatch
// rejection (as opposed to ErrNoNodeAvailable, which is scarcity, not a conflict).
func isOCCConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "optimistic concurrency conflict")
}

// TestOmegaOCC_BarrierRaceExactlyOneCommits is the headline anti-bluff test.
//
// Setup: ONE node with capacity for exactly ONE GPU pod.
// Race:  N scheduler goroutines each read the SAME initial version v0, then all
//
//	block on a release barrier and fire ScheduleOptimistic(job, v0) together.
//
// Load-bearing invariant (this is what proves OCC is REAL):
//
//	commits == 1  AND  conflicts == N-1
//
// Because all N goroutines present the identical stale v0, the first commit bumps
// the version to v0+1; every subsequent commit sees s.version(v0+1) != expected(v0)
// and is REJECTED. If the version/CAS check were removed or made a no-op, ALL N
// commits would pass the guard, each NodeResourcesFit.Bind would decrement GPU, and
// the node would end at GPU == 1-N (oversubscribed). The GPU>=0 / commits==1
// assertions below would then fail. THAT is why "exactly one commit" bites: it is
// the observable difference between a working CAS and a broken one.
func TestOmegaOCC_BarrierRaceExactlyOneCommits(t *testing.T) {
	const schedulers = 16 // many racers contending for ONE resource

	// Run the barrier race many times so the interleaving is genuinely exercised,
	// not a single lucky scheduling order. The invariant is a CORRECTNESS property
	// (not probabilistic): it MUST hold on every iteration.
	const iterations = 200

	for iter := 0; iter < iterations; iter++ {
		s := NewScheduler()
		s.AddPlugin(&NodeResourcesFit{})

		nodeID := fmt.Sprintf("occ-barrier-node-iter%d", iter)
		s.RegisterNode(&Node{
			ID: nodeID,
			// Capacity for EXACTLY ONE of the contending GPU pods.
			AvailableResources: Resources{CPU: 1000, Memory: 1_000_000, GPU: 1},
		})

		v0 := s.Version()

		var (
			commits   atomic.Int32
			conflicts atomic.Int32
			other     atomic.Int32 // any non-conflict, non-nil error (should be 0)
		)

		// release is closed once to unblock all goroutines simultaneously; ready
		// guarantees every goroutine has already captured v0 and is parked on the
		// barrier before the race begins — so they all genuinely contend on v0.
		release := make(chan struct{})
		var ready sync.WaitGroup
		var done sync.WaitGroup
		ready.Add(schedulers)
		done.Add(schedulers)

		for i := 0; i < schedulers; i++ {
			go func(idx int) {
				defer done.Done()
				job := &Job{
					ID:        fmt.Sprintf("occ-barrier-iter%d-sched%d", iter, idx),
					Priority:  5,
					Resources: Resources{CPU: 1, Memory: 1024, GPU: 1},
				}
				// All goroutines read the SAME v0 (already captured above) and park.
				ready.Done()
				<-release // fire together

				_, err := s.ScheduleOptimistic(job, v0)
				switch {
				case err == nil:
					commits.Add(1)
				case isOCCConflict(err):
					conflicts.Add(1)
				default:
					other.Add(1)
				}
			}(i)
		}

		ready.Wait()   // everyone has v0 and is on the barrier
		close(release) // GO — true simultaneous contention on v0
		done.Wait()

		// LOAD-BEARING ASSERTION A: exactly ONE commit succeeded.
		if got := commits.Load(); got != 1 {
			t.Fatalf("iter %d: expected exactly 1 commit, got %d (CAS failed to reject stale writers -> double-book)", iter, got)
		}
		// LOAD-BEARING ASSERTION B: every other racer was rejected as a CONFLICT,
		// not silently dropped or errored some other way.
		if got := conflicts.Load(); got != int32(schedulers-1) {
			t.Fatalf("iter %d: expected %d OCC conflicts, got %d", iter, schedulers-1, got)
		}
		if got := other.Load(); got != 0 {
			t.Fatalf("iter %d: expected 0 non-conflict errors, got %d", iter, got)
		}

		// SINK-SIDE: resource is NOT double-booked. GPU must be exactly 0, never
		// negative. A negative value is the smoking gun of oversubscription.
		n, ok := s.GetNode(nodeID)
		if !ok {
			t.Fatalf("iter %d: node %s vanished", iter, nodeID)
		}
		if n.AvailableResources.GPU != 0 {
			t.Fatalf("iter %d: GPU=%d, want 0 (negative => OCC let two schedulers double-book the one GPU)", iter, n.AvailableResources.GPU)
		}

		// SINK-SIDE: exactly one running placement is recorded.
		if got := len(s.RunningPlacements()); got != 1 {
			t.Fatalf("iter %d: expected 1 running placement, got %d", iter, got)
		}

		// SINK-SIDE: version advanced EXACTLY once (one successful commit only).
		if got := s.Version(); got != v0+1 {
			t.Fatalf("iter %d: version=%d, want %d (must advance exactly once per commit)", iter, got, v0+1)
		}

		if iter == 0 {
			t.Logf("[OMEGA-OCC barrier] node-cap=1 schedulers=%d: commits=%d conflicts=%d other=%d finalGPU=%d version=%d->%d (per-iter, %d iters)",
				schedulers, commits.Load(), conflicts.Load(), other.Load(), n.AvailableResources.GPU, v0, s.Version(), iterations)
		}
	}
}

// TestOmegaOCC_LoserRetryConvergesNoDoubleBook proves the optimistic RETRY loop the
// scheduler is designed for: the conflict loser RE-READS the now-updated version and
// retries. With a SINGLE scarce resource, the retry must observe the resource is gone
// and fail CLEANLY with ErrNoNodeAvailable — it must NOT double-book. With a SECOND
// resource available, the loser's retry must succeed on the alternative.
func TestOmegaOCC_LoserRetryConvergesNoDoubleBook(t *testing.T) {
	t.Run("single_resource_retry_fails_clean", func(t *testing.T) {
		s := NewScheduler()
		s.AddPlugin(&NodeResourcesFit{})
		nodeID := "occ-retry-single"
		s.RegisterNode(&Node{
			ID:                 nodeID,
			AvailableResources: Resources{CPU: 1000, Memory: 1_000_000, GPU: 1},
		})

		v0 := s.Version()
		jobA := &Job{ID: "retry-A", Resources: Resources{CPU: 1, Memory: 1024, GPU: 1}}
		jobB := &Job{ID: "retry-B", Resources: Resources{CPU: 1, Memory: 1024, GPU: 1}}

		// A commits at v0 (winner), consuming the lone GPU.
		if _, err := s.ScheduleOptimistic(jobA, v0); err != nil {
			t.Fatalf("winner A unexpectedly failed: %v", err)
		}

		// B presents the now-stale v0 -> must be an OCC conflict.
		_, errStale := s.ScheduleOptimistic(jobB, v0)
		if !isOCCConflict(errStale) {
			t.Fatalf("B with stale v0 expected OCC conflict, got: %v", errStale)
		}
		// A failed commit must NOT advance the version.
		if got := s.Version(); got != v0+1 {
			t.Fatalf("version must stay at %d after rejected commit, got %d", v0+1, got)
		}

		// B re-reads the fresh version and retries. The GPU is gone, so this is
		// NOT a conflict — it is honest scarcity: ErrNoNodeAvailable. Crucially it
		// is NOT a double-book.
		_, errRetry := s.ScheduleOptimistic(jobB, s.Version())
		if errRetry != ErrNoNodeAvailable {
			t.Fatalf("B retry on exhausted node expected ErrNoNodeAvailable, got: %v", errRetry)
		}

		// Sink-side: still exactly one placement, GPU still 0 (never -1).
		if got := len(s.RunningPlacements()); got != 1 {
			t.Fatalf("expected 1 placement after loser's clean-fail retry, got %d", got)
		}
		n, _ := s.GetNode(nodeID)
		if n.AvailableResources.GPU != 0 {
			t.Fatalf("GPU=%d, want 0 (loser must not double-book on retry)", n.AvailableResources.GPU)
		}
		t.Logf("[OMEGA-OCC retry/single] winner=A; loser B conflicted then retry->ErrNoNodeAvailable (no double-book); GPU=%d version=%d",
			n.AvailableResources.GPU, s.Version())
	})

	t.Run("alternative_resource_retry_succeeds", func(t *testing.T) {
		s := NewScheduler()
		s.AddPlugin(&NodeResourcesFit{})
		// TWO GPUs on the node: after the winner takes one, the loser's retry
		// must succeed on the remaining capacity (place elsewhere / on what's left).
		nodeID := "occ-retry-alt"
		s.RegisterNode(&Node{
			ID:                 nodeID,
			AvailableResources: Resources{CPU: 1000, Memory: 1_000_000, GPU: 2},
		})

		v0 := s.Version()
		jobA := &Job{ID: "alt-A", Resources: Resources{CPU: 1, Memory: 1024, GPU: 1}}
		jobB := &Job{ID: "alt-B", Resources: Resources{CPU: 1, Memory: 1024, GPU: 1}}

		if _, err := s.ScheduleOptimistic(jobA, v0); err != nil {
			t.Fatalf("winner A failed: %v", err)
		}
		if _, errStale := s.ScheduleOptimistic(jobB, v0); !isOCCConflict(errStale) {
			t.Fatalf("B stale v0 expected OCC conflict, got %v", errStale)
		}
		// Retry with the fresh version: capacity remains, so commit succeeds.
		if _, errRetry := s.ScheduleOptimistic(jobB, s.Version()); errRetry != nil {
			t.Fatalf("B retry on node with spare GPU should succeed, got %v", errRetry)
		}

		if got := len(s.RunningPlacements()); got != 2 {
			t.Fatalf("expected 2 placements (both converged), got %d", got)
		}
		n, _ := s.GetNode(nodeID)
		if n.AvailableResources.GPU != 0 {
			t.Fatalf("GPU=%d, want 0 (2 capacity, 2 committed)", n.AvailableResources.GPU)
		}
		if got := s.Version(); got != v0+2 {
			t.Fatalf("version=%d, want %d (two successful commits)", got, v0+2)
		}
		t.Logf("[OMEGA-OCC retry/alt] winner=A; loser B conflicted then retry succeeded on spare GPU; placements=2 GPU=%d version=%d->%d",
			n.AvailableResources.GPU, v0, s.Version())
	})
}

// TestOmegaOCC_ConcurrentPoolExactCapacity is the pool variant: many schedulers
// race for a pool of K GPUs via the real optimistic retry loop. The number of
// successful commits MUST exactly equal K (capacity), and the version MUST advance
// exactly K times. Oversubscription (GPU < 0) or extra commits would indicate the
// CAS is not actually serializing the read-modify-write — the anti-bluff bite at
// pool scale.
func TestOmegaOCC_ConcurrentPoolExactCapacity(t *testing.T) {
	const capacity = 5
	const schedulers = 40

	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	nodeID := "occ-pool"
	s.RegisterNode(&Node{
		ID:                 nodeID,
		AvailableResources: Resources{CPU: 100000, Memory: 1_000_000_000, GPU: capacity},
	})

	v0 := s.Version()
	var commits atomic.Int32
	var noCapacity atomic.Int32

	release := make(chan struct{})
	var ready, done sync.WaitGroup
	ready.Add(schedulers)
	done.Add(schedulers)

	for i := 0; i < schedulers; i++ {
		go func(idx int) {
			defer done.Done()
			job := &Job{
				ID:        fmt.Sprintf("pool-sched%d", idx),
				Resources: Resources{CPU: 1, Memory: 1024, GPU: 1},
			}
			ready.Done()
			<-release
			// Real OCC retry loop: read fresh version, attempt commit, retry on
			// conflict, stop on genuine scarcity.
			for attempt := 0; attempt < 1000; attempt++ {
				v := s.Version()
				_, err := s.ScheduleOptimistic(job, v)
				if err == nil {
					commits.Add(1)
					return
				}
				if err == ErrNoNodeAvailable {
					noCapacity.Add(1)
					return
				}
				// OCC conflict: loop and retry on the fresh version.
				if !isOCCConflict(err) {
					t.Errorf("sched%d unexpected error: %v", idx, err)
					return
				}
			}
			t.Errorf("sched%d exhausted retries without resolution", idx)
		}(i)
	}

	ready.Wait()
	close(release)
	done.Wait()

	// LOAD-BEARING: exactly `capacity` commits — never more (double-book), never
	// fewer (lost update). Everyone else cleanly hit no-capacity.
	if got := commits.Load(); got != capacity {
		t.Fatalf("expected exactly %d commits (== capacity), got %d", capacity, got)
	}
	if got := noCapacity.Load(); got != int32(schedulers-capacity) {
		t.Fatalf("expected %d clean no-capacity outcomes, got %d", schedulers-capacity, got)
	}
	// Version advanced exactly once per commit.
	if got := s.Version(); got != v0+uint64(capacity) {
		t.Fatalf("version=%d, want %d (exactly one bump per commit)", got, v0+uint64(capacity))
	}
	// Sink-side: pool exactly drained, never negative.
	n, _ := s.GetNode(nodeID)
	if n.AvailableResources.GPU != 0 {
		t.Fatalf("GPU=%d, want 0 (negative => pool oversubscribed)", n.AvailableResources.GPU)
	}
	if got := len(s.RunningPlacements()); got != capacity {
		t.Fatalf("expected %d running placements, got %d", capacity, got)
	}

	t.Logf("[OMEGA-OCC pool] capacity=%d schedulers=%d: commits=%d noCapacity=%d finalGPU=%d version=%d->%d",
		capacity, schedulers, commits.Load(), noCapacity.Load(), n.AvailableResources.GPU, v0, s.Version())
}
