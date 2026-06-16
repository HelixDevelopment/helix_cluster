// Package scheduler — SECOND-PASS adversarial probes (HXC bug-hunt wave).
//
// The existing scheduler_adversarial_test.go proves NO-OVER-COMMIT under
// concurrent ScheduleJob RPCs (all of which serialize on the server's s.mu).
// This file attacks a path that the first pass does NOT exercise: the
// RESOURCE-RESTORE side of PreemptJob, which deliberately DROPS s.mu before it
// reads-modifies-writes the node's available resources back via the underlying
// scheduler.
//
// PreemptJob does:
//
//	s.mu.Lock()
//	... delete(s.jobs, jobID) ...
//	s.mu.Unlock()                     // <-- lock released here
//	node, _ := s.sched.GetNode(...)   // copy of node (residual R)
//	node.AvailableResources.CPU += rec.resources.CPU   // R + restore
//	s.sched.RegisterNode(node)        // overwrite s.nodes[id] with the copy
//
// Because the GetNode (read copy) / RegisterNode (overwrite) pair runs WITHOUT
// the server's s.mu, a concurrent ScheduleJob — which DOES hold s.mu and debits
// the same node inside s.sched.Schedule -> Bind — can interleave between
// PreemptJob's read and write. The Schedule's debit is then silently CLOBBERED
// by PreemptJob's stale copy + restore, corrupting resource accounting and
// permitting OVER-COMMIT.
//
// Sink-side invariant asserted: at every quiescent point, the node's residual
// CPU plus the CPU consumed by the jobs the server still believes are placed
// must equal the node capacity K. A lost-update breaks this conservation law.
//
// Anti-hang: all calls are in-process direct method calls, concurrency capped,
// completes well under the test timeout.
package scheduler

import (
	"context"
	"sync"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// placedCPU returns the total CPU the server still believes is consumed by
// currently-placed jobs on nodeID (sum over s.jobs of rec.resources.CPU).
func placedCPU(srv *Server, nodeID string) float64 {
	srv.mu.RLock()
	defer srv.mu.RUnlock()
	var total float64
	for _, rec := range srv.jobs {
		if rec.nodeID == nodeID {
			total += rec.resources.CPU
		}
	}
	return total
}

// TestPreemptVsSchedule_ConservationUnderRace hammers PreemptJob (which restores
// freed capacity without holding the server lock) against ScheduleJob (which
// debits capacity while holding the server lock) on a single shared node, then
// asserts the conservation law residual + placed == K at the quiescent end.
//
// If PreemptJob's unlocked read-copy/restore clobbers a concurrent Schedule's
// debit (a lost update), residual is inflated above the true free capacity and
// the invariant breaks — the sink-side signature of OVER-COMMIT.
func TestPreemptVsSchedule_ConservationUnderRace(t *testing.T) {
	const (
		K      = 4  // node capacity (whole cores)
		rounds = 60 // contention rounds
	)

	srv := NewServer()
	seedNode(t, srv, "node-pre", K)

	// Fill the node to capacity with K one-core jobs: "fill-0".."fill-(K-1)".
	for i := 0; i < K; i++ {
		id := "fill-" + intToStr(i)
		resp, err := srv.ScheduleJob(context.Background(), oneCoreJob(id))
		if err != nil || !resp.Scheduled {
			t.Fatalf("seed-fill %s: resp=%+v err=%v", id, resp, err)
		}
	}
	if got := residualCPU(t, srv, "node-pre"); got != 0 {
		t.Fatalf("precondition: node should be full, residual=%v", got)
	}

	// Each round: concurrently preempt one currently-placed job (frees 1 core)
	// and schedule a brand-new one-core job competing for that freed core.
	// Over many rounds the preempt-restore and schedule-debit windows overlap.
	for r := 0; r < rounds; r++ {
		victimID := "fill-" + intToStr(r%K)
		newID := "new-" + intToStr(r)

		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			// Preempt the victim; ignore NotFound (already gone in a prior round).
			if err := srv.PreemptJob(context.Background(), victimID); err != nil &&
				status.Code(err) != codes.NotFound {
				t.Errorf("PreemptJob(%s): %v", victimID, err)
			}
		}()

		go func() {
			defer wg.Done()
			// Try to grab the freed core. ResourceExhausted is acceptable (the
			// freed core may not be visible yet); a successful placement must be
			// honestly accounted.
			_, err := srv.ScheduleJob(context.Background(), oneCoreJob(newID))
			if err != nil && status.Code(err) != codes.ResourceExhausted {
				t.Errorf("ScheduleJob(%s): unexpected %v", newID, err)
			}
		}()

		wg.Wait()

		// Re-seat the victim's slot for the next round so capacity churns:
		// reschedule a fresh fill job into the rotation if room exists.
		_, _ = srv.ScheduleJob(context.Background(), oneCoreJob("fill-"+intToStr(r%K)+"-"+intToStr(r)))

		// CONSERVATION at this quiescent point: residual + placed == K, and
		// residual must never be negative (hard over-commit) nor exceed K
		// (phantom capacity from a clobbered debit).
		res := residualCPU(t, srv, "node-pre")
		placed := placedCPU(srv, "node-pre")
		if res < 0 {
			t.Fatalf("round %d OVER-COMMIT: residual CPU negative: %v", r, res)
		}
		if res > K {
			t.Fatalf("round %d PHANTOM CAPACITY: residual=%v > K=%d (a debit was clobbered)", r, res, K)
		}
		if res+placed != float64(K) {
			t.Fatalf("round %d CONSERVATION VIOLATED: residual=%v + placed=%v != K=%d", r, res, placed, K)
		}
	}
}

// TestPreemptVsSchedule_NoResidualOvershoot is a higher-contention variant that
// keeps the node permanently holding ~1 free core so that a Schedule's debit and
// a Preempt's restore have a real, overlapping window to clobber each other.
//
// The construction: a node of capacity K is filled to K-1 (one free core). A
// pool of distinct jobs is repeatedly preempted (each freeing a core) and
// rescheduled (each debiting a core) by many goroutines with NO per-iteration
// barrier, so PreemptJob's unlocked GetNode/RegisterNode read-modify-write
// races ScheduleJob's locked Bind debit. At the quiescent end every in-flight
// op has drained, so the conservation law residual + placed == K must hold
// EXACTLY. A clobbered debit (lost update) leaves residual inflated and breaks
// it; over-commit shows as residual < 0.
func TestPreemptVsSchedule_NoResidualOvershoot(t *testing.T) {
	const (
		K     = 4   // node capacity
		N     = 8   // worker goroutines
		iters = 200 // preempt+reschedule cycles per worker
	)

	srv := NewServer()
	seedNode(t, srv, "node-os", K)

	// Each worker owns a private job ID slot it repeatedly preempts and
	// reschedules. With N>K some workers will be ResourceExhausted on schedule
	// and NotFound on preempt — both fine — but the freed-core churn keeps a
	// hot debit/restore overlap on the shared node.
	var wg sync.WaitGroup
	start := make(chan struct{})
	for w := 0; w < N; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			<-start
			id := "w-" + intToStr(w)
			// Seed this worker's job once (best effort: may be exhausted).
			_, _ = srv.ScheduleJob(context.Background(), oneCoreJob(id))
			for k := 0; k < iters; k++ {
				_ = srv.PreemptJob(context.Background(), id)
				_, _ = srv.ScheduleJob(context.Background(), oneCoreJob(id))
			}
		}(w)
	}
	close(start)
	wg.Wait()

	res := residualCPU(t, srv, "node-os")
	placed := placedCPU(srv, "node-os")
	if res < 0 {
		t.Fatalf("OVER-COMMIT: residual negative: %v", res)
	}
	if res > K {
		t.Fatalf("PHANTOM CAPACITY: residual=%v > K=%d", res, K)
	}
	if res+placed != float64(K) {
		t.Fatalf("CONSERVATION VIOLATED: residual=%v + placed=%v != K=%d (lost update in preempt restore)", res, placed, K)
	}
}
