// Package scheduler — adversarial, sink-side concurrency/determinism probes for
// the gRPC SchedulerService server wrapping pkg/scheduler.
//
// These tests are package-internal (package scheduler) so they can inspect the
// real post-debit node state via s.sched.GetNode and the s.jobs map size,
// asserting SINK-SIDE invariants rather than "no panic":
//
//   - OVER-COMMIT: under N concurrent ScheduleJob RPCs against a node with
//     capacity K cores, no more than K jobs may be placed, and the node's
//     residual CPU must equal K-successes (never negative). This is the
//     §7.1 / CLAUDE-1 end-user guarantee: a scheduler that double-books a node
//     is a non-functional feature even if every RPC "returns without panic".
//   - SHARED-POINTER / UNGUARDED STATE: concurrent ScheduleJob + ListJobs +
//     GetJobStatus must be -race clean, and the number of Scheduled=true
//     responses must equal len(s.jobs) (no lost update on the job map).
//   - DETERMINISM / TOTAL-ORDER: two equal-score nodes that tie must place
//     deterministically (lexicographically smallest node ID wins) across many
//     fresh servers — inheriting the pkg/scheduler nondeterministic-placement
//     fix at the server boundary.
//
// Anti-hang: methods are called directly (in-process, no gRPC socket),
// concurrency is capped at <= 8, and the whole file completes well under the
// 90s test timeout.
package scheduler

import (
	"context"
	"sync"
	"testing"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
	"github.com/HelixDevelopment/helix_cluster/pkg/discovery"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// seedNode registers a helix-node instance with the given whole-core CPU
// capacity (and ample memory) into the server's registry so scheduling has a
// real, capacity-bounded target.
func seedNode(t *testing.T, srv *Server, id string, cpuCores int) {
	t.Helper()
	if err := srv.registry.Register(context.Background(), &discovery.Instance{
		ID:      id,
		Service: "helix-node",
		Address: "127.0.0.1",
		Port:    8080,
		Healthy: true,
		Metadata: map[string]string{
			// instanceToNode parses "cpu" as a float core count and "memory_mb"
			// as MB. Give huge memory so CPU is the sole binding constraint.
			"cpu":       intToStr(cpuCores),
			"memory_mb": "1048576", // 1 TiB worth of MB — never the bottleneck
		},
	}); err != nil {
		t.Fatalf("seed node %s: %v", id, err)
	}
}

func intToStr(n int) string {
	// tiny, allocation-free itoa for small positive n used in metadata
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// oneCoreJob builds a ScheduleJobRequest that demands exactly 1.0 CPU core
// (1000 millicores) and negligible memory.
func oneCoreJob(id string) *helixv1.ScheduleJobRequest {
	return &helixv1.ScheduleJobRequest{
		JobId: id,
		Requirements: &helixv1.ResourceAllocation{
			CpuMillicores: 1000,    // 1.0 core
			MemoryBytes:   1 << 20, // 1 MiB -> 1 MB, trivially fits
		},
	}
}

// residualCPU reads the post-debit available CPU of a node from the underlying
// scheduler. GetNode returns a copy, so this is a faithful read of the real,
// Bind-debited capacity — the SINK we assert against for over-commit.
func residualCPU(t *testing.T, srv *Server, nodeID string) float64 {
	t.Helper()
	n, ok := srv.sched.GetNode(nodeID)
	if !ok {
		t.Fatalf("node %s not registered in scheduler", nodeID)
	}
	return n.AvailableResources.CPU
}

// TestScheduleJob_NoOverCommitUnderConcurrentRPCs is the headline sink-side
// probe: N goroutines call ScheduleJob concurrently against a single node with
// capacity K cores, each job demanding 1 core. The server promises (under its
// s.mu) that resources are debited atomically and duplicate placements are
// rejected. We assert:
//
//	(1) successes <= K                      — NO OVER-COMMIT
//	(2) residual CPU == K - successes       — CONSERVATION, never negative
//	(3) every failure is ResourceExhausted  — honest rejection, not a silent drop
//	(4) len(s.jobs) == successes            — job map matches placements (no lost update)
//
// Run with -race to additionally catch any unguarded shared-state access.
func TestScheduleJob_NoOverCommitUnderConcurrentRPCs(t *testing.T) {
	const (
		K = 4 // node capacity in whole cores
		N = 8 // concurrent ScheduleJob RPCs (capped <= 8)
	)

	srv := NewServer()
	seedNode(t, srv, "node-cap", K)

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		successes int
		exhausted int
		otherErr  []error
		start     = make(chan struct{})
	)

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // maximize contention: release all goroutines together
			resp, err := srv.ScheduleJob(context.Background(), oneCoreJob("job-"+intToStr(i)))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil && resp.Scheduled:
				successes++
			case status.Code(err) == codes.ResourceExhausted:
				exhausted++
			default:
				otherErr = append(otherErr, err)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if len(otherErr) != 0 {
		t.Fatalf("unexpected non-ResourceExhausted errors: %v", otherErr)
	}

	// (1) NO OVER-COMMIT.
	if successes > K {
		t.Fatalf("OVER-COMMIT: %d jobs placed on a %d-core node", successes, K)
	}
	// With K<N and each job needing exactly 1 core, the node should fill exactly.
	if successes != K {
		t.Fatalf("expected node to fill to capacity: placed=%d want=%d", successes, K)
	}
	if exhausted != N-K {
		t.Fatalf("expected %d ResourceExhausted rejections, got %d", N-K, exhausted)
	}

	// (2) CONSERVATION: residual must be exactly K-successes and never negative.
	res := residualCPU(t, srv, "node-cap")
	if res < 0 {
		t.Fatalf("OVER-COMMIT: node residual CPU went negative: %v", res)
	}
	if want := float64(K - successes); res != want {
		t.Fatalf("conservation violated: residual CPU=%v want=%v (placed=%d)", res, want, successes)
	}

	// (4) Job map must match the number of successful placements (no lost update).
	srv.mu.RLock()
	jobCount := len(srv.jobs)
	srv.mu.RUnlock()
	if jobCount != successes {
		t.Fatalf("lost update: len(jobs)=%d but successes=%d", jobCount, successes)
	}
}

// TestScheduleJob_ConcurrentScheduleAndReads is the shared-pointer / unguarded
// shared-state probe (the internal/node class). It hammers ScheduleJob
// concurrently with ListJobs and GetJobStatus reads. Under -race this fails if
// any handler mutates a shared map/struct in place without synchronization.
// Sink-side, after the storm we assert conservation: len(s.jobs) equals the
// number of successful placements and node residual is non-negative.
func TestScheduleJob_ConcurrentScheduleAndReads(t *testing.T) {
	const (
		K = 3
		N = 6
	)

	srv := NewServer()
	seedNode(t, srv, "node-rw", K)

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		successes int
		start     = make(chan struct{})
	)

	// Writers: concurrent ScheduleJob.
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			resp, err := srv.ScheduleJob(context.Background(), oneCoreJob("rw-"+intToStr(i)))
			if err == nil && resp.Scheduled {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}(i)
	}

	// Readers: concurrent ListJobs + GetJobStatus, racing the writers.
	for r := 0; r < 2; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for k := 0; k < 50; k++ {
				if _, err := srv.ListJobs(context.Background(), &helixv1.ListJobsRequest{}); err != nil {
					t.Errorf("ListJobs: %v", err)
					return
				}
				// GetJobStatus on a possibly-not-yet-scheduled ID: NotFound is fine.
				if _, err := srv.GetJobStatus(context.Background(), &helixv1.GetJobStatusRequest{JobId: "rw-0"}); err != nil &&
					status.Code(err) != codes.NotFound {
					t.Errorf("GetJobStatus unexpected: %v", err)
					return
				}
			}
		}()
	}

	close(start)
	wg.Wait()

	if successes > K {
		t.Fatalf("OVER-COMMIT under concurrent read/write: placed=%d cap=%d", successes, K)
	}
	if res := residualCPU(t, srv, "node-rw"); res < 0 {
		t.Fatalf("node residual CPU negative: %v", res)
	}
	srv.mu.RLock()
	jobCount := len(srv.jobs)
	srv.mu.RUnlock()
	if jobCount != successes {
		t.Fatalf("job map lost update: len(jobs)=%d successes=%d", jobCount, successes)
	}
}

// TestScheduleJob_DeterministicTieBreak pins the total-order/determinism
// contract inherited from the pkg/scheduler fix at the SERVER boundary: when two
// nodes are byte-for-byte equal in capacity (and therefore tie on every plugin
// score), the single placed job must always land on the same node — the
// lexicographically smallest node ID — across many independent fresh servers.
// A pre-fix map-iteration-order scheduler would scatter placements across the
// two nodes and fail this.
func TestScheduleJob_DeterministicTieBreak(t *testing.T) {
	const trials = 40
	var first string
	for i := 0; i < trials; i++ {
		srv := NewServer()
		// Two identical-capacity nodes: "node-a" < "node-b" lexicographically.
		seedNode(t, srv, "node-b", 8)
		seedNode(t, srv, "node-a", 8)

		resp, err := srv.ScheduleJob(context.Background(), oneCoreJob("det-"+intToStr(i)))
		if err != nil {
			t.Fatalf("trial %d ScheduleJob: %v", i, err)
		}
		if !resp.Scheduled || resp.NodeId == "" {
			t.Fatalf("trial %d: not scheduled: %+v", i, resp)
		}
		if i == 0 {
			first = resp.NodeId
			if first != "node-a" {
				t.Fatalf("expected deterministic smallest-ID placement node-a, got %q", first)
			}
			continue
		}
		if resp.NodeId != first {
			t.Fatalf("NONDETERMINISTIC PLACEMENT: trial %d placed on %q, trial 0 placed on %q",
				i, resp.NodeId, first)
		}
	}
}

// TestScheduleJob_DuplicateIDRejected pins the documented duplicate-ID contract:
// re-scheduling the same job ID must be rejected with AlreadyExists (not a
// second silent debit of node resources). This is the resource-leak guard the
// production comment promises; we assert it holds and that residual capacity is
// debited exactly once.
func TestScheduleJob_DuplicateIDRejected(t *testing.T) {
	srv := NewServer()
	seedNode(t, srv, "node-dup", 8)

	if _, err := srv.ScheduleJob(context.Background(), oneCoreJob("dup")); err != nil {
		t.Fatalf("first schedule: %v", err)
	}
	resAfterFirst := residualCPU(t, srv, "node-dup")

	_, err := srv.ScheduleJob(context.Background(), oneCoreJob("dup"))
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("duplicate schedule: code=%v err=%v, want AlreadyExists", status.Code(err), err)
	}
	// Residual must be unchanged: the rejected duplicate must NOT have debited.
	if res := residualCPU(t, srv, "node-dup"); res != resAfterFirst {
		t.Fatalf("duplicate schedule leaked/double-debited: residual=%v want=%v", res, resAfterFirst)
	}
}
