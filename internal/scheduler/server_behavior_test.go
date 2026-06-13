// Package scheduler — real-behavior tests for the Scheduler gRPC server.
//
// These tests assert sink-side, end-user-visible effects: that scheduling
// actually consumes node capacity, that cancellation actually returns it, and
// that duplicate jobs are rejected rather than silently double-consuming
// resources. Each test names a concrete one-line source mutation that breaks it.
package scheduler

import (
	"context"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
	"github.com/HelixDevelopment/helix_cluster/pkg/discovery"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// seedServer returns a Server with one node of the given capacity registered in
// discovery. Using the Server directly (not over gRPC) lets us inspect the
// scheduler's node capacity, which is the real sink for resource accounting.
func seedServer(t *testing.T, cpuMillis int32, memMB uint64) *Server {
	t.Helper()
	srv := NewServer()
	err := srv.registry.Register(context.Background(), &discovery.Instance{
		ID:      "node-cap",
		Service: "helix-node",
		Address: "127.0.0.1",
		Port:    9000,
		Healthy: true,
		Metadata: map[string]string{
			"cpu":       itoaF(cpuMillis),
			"memory_mb": itoaU(memMB),
		},
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	return srv
}

func itoaF(m int32) string {
	// node "cpu" metadata is parsed as float cores via Sscanf("%f").
	switch m {
	case 4000:
		return "4"
	case 8000:
		return "8"
	default:
		return "2"
	}
}

func itoaU(mb uint64) string {
	switch mb {
	case 16384:
		return "16384"
	case 8192:
		return "8192"
	default:
		return "4096"
	}
}

// TestScheduleConsumesNodeCapacity proves that ScheduleJob debits the node's
// available resources by the requested amount — i.e. scheduling has a real
// effect on cluster state, not just a map write.
//
// Mutation that breaks it: in pkg is out of scope, but at the server level,
// removing `resources: job.Resources` from the jobRecord and the corresponding
// requirement plumbing would not break this directly; the load-bearing source
// here is ScheduleJob actually calling s.sched.Schedule(job). Replacing the
// requested 4 CPU with 0 (e.g. dropping the `req.Requirements != nil` block in
// ScheduleJob so job.Resources stays zero) makes the post-schedule capacity
// equal the original 8.0 instead of 4.0, failing this assertion.
func TestScheduleConsumesNodeCapacity(t *testing.T) {
	srv := seedServer(t, 8000, 16384)
	ctx := context.Background()

	resp, err := srv.ScheduleJob(ctx, &helixv1.ScheduleJobRequest{
		JobId: "cap-1",
		Requirements: &helixv1.ResourceAllocation{
			CpuMillicores: 4000, // 4 cores
			MemoryBytes:   4096 * 1024 * 1024,
		},
	})
	if err != nil {
		t.Fatalf("ScheduleJob: %v", err)
	}
	if !resp.Scheduled || resp.NodeId != "node-cap" {
		t.Fatalf("unexpected response: %+v", resp)
	}

	node, ok := srv.sched.GetNode("node-cap")
	if !ok {
		t.Fatal("node-cap missing after schedule")
	}
	if node.AvailableResources.CPU != 4.0 {
		t.Errorf("CPU after schedule = %v, want 4.0 (8 - 4 consumed)", node.AvailableResources.CPU)
	}
	if node.AvailableResources.Memory != 16384-4096 {
		t.Errorf("Memory after schedule = %v, want %d", node.AvailableResources.Memory, 16384-4096)
	}
}

// TestCancelRestoresNodeCapacity proves the resource-leak fix: cancelling a job
// returns its consumed CPU/memory to the node so capacity is exactly restored.
//
// Mutation that breaks it: delete the resource-credit block in CancelJob
// (the `node.AvailableResources.CPU += rec.resources.CPU` ... RegisterNode
// sequence). Then capacity stays at 4.0 after cancel and this fails.
func TestCancelRestoresNodeCapacity(t *testing.T) {
	srv := seedServer(t, 8000, 16384)
	ctx := context.Background()

	if _, err := srv.ScheduleJob(ctx, &helixv1.ScheduleJobRequest{
		JobId: "cap-2",
		Requirements: &helixv1.ResourceAllocation{
			CpuMillicores: 4000,
			MemoryBytes:   4096 * 1024 * 1024,
		},
	}); err != nil {
		t.Fatalf("ScheduleJob: %v", err)
	}

	if _, err := srv.CancelJob(ctx, &helixv1.CancelJobRequest{JobId: "cap-2"}); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}

	node, ok := srv.sched.GetNode("node-cap")
	if !ok {
		t.Fatal("node-cap missing after cancel")
	}
	if node.AvailableResources.CPU != 8.0 {
		t.Errorf("CPU after cancel = %v, want 8.0 (fully restored)", node.AvailableResources.CPU)
	}
	if node.AvailableResources.Memory != 16384 {
		t.Errorf("Memory after cancel = %v, want 16384 (fully restored)", node.AvailableResources.Memory)
	}
}

// TestScheduleCancelCycleDoesNotLeak proves the end-to-end usability guarantee:
// a node with capacity for exactly one big job can run that job repeatedly
// across schedule/cancel cycles. Without the cancel-restore fix, the second
// schedule would fail with ResourceExhausted because capacity leaked.
//
// Mutation that breaks it: same as above — drop the credit-back in CancelJob.
// The 2nd ScheduleJob then returns ResourceExhausted and this fails.
func TestScheduleCancelCycleDoesNotLeak(t *testing.T) {
	srv := seedServer(t, 8000, 16384)
	ctx := context.Background()
	req := &helixv1.ResourceAllocation{
		CpuMillicores: 8000, // consumes the whole node
		MemoryBytes:   16384 * 1024 * 1024,
	}

	for i := 0; i < 3; i++ {
		if _, err := srv.ScheduleJob(ctx, &helixv1.ScheduleJobRequest{JobId: "cycle", Requirements: req}); err != nil {
			t.Fatalf("ScheduleJob iter %d: %v", i, err)
		}
		if _, err := srv.CancelJob(ctx, &helixv1.CancelJobRequest{JobId: "cycle"}); err != nil {
			t.Fatalf("CancelJob iter %d: %v", i, err)
		}
	}

	node, ok := srv.sched.GetNode("node-cap")
	if !ok {
		t.Fatal("node-cap missing")
	}
	if node.AvailableResources.CPU != 8.0 {
		t.Errorf("CPU after 3 cycles = %v, want 8.0 (no leak)", node.AvailableResources.CPU)
	}
}

// TestScheduleDuplicateJobRejected proves that re-scheduling an in-flight job ID
// is rejected with AlreadyExists rather than silently double-consuming capacity
// and orphaning the first placement's resources.
//
// Mutation that breaks it: remove the `if _, exists := s.jobs[job.ID]` guard in
// ScheduleJob. The duplicate then succeeds and err is nil, failing this test.
func TestScheduleDuplicateJobRejected(t *testing.T) {
	srv := seedServer(t, 8000, 16384)
	ctx := context.Background()
	req := &helixv1.ResourceAllocation{CpuMillicores: 4000, MemoryBytes: 4096 * 1024 * 1024}

	if _, err := srv.ScheduleJob(ctx, &helixv1.ScheduleJobRequest{JobId: "dup", Requirements: req}); err != nil {
		t.Fatalf("first ScheduleJob: %v", err)
	}

	_, err := srv.ScheduleJob(ctx, &helixv1.ScheduleJobRequest{JobId: "dup", Requirements: req})
	if status.Code(err) != codes.AlreadyExists {
		t.Fatalf("duplicate schedule: code = %v, want AlreadyExists", status.Code(err))
	}

	// Capacity must reflect exactly ONE placement (8 - 4 = 4), proving the
	// rejected duplicate did not consume a second 4 cores.
	node, _ := srv.sched.GetNode("node-cap")
	if node.AvailableResources.CPU != 4.0 {
		t.Errorf("CPU = %v, want 4.0 (duplicate must not double-consume)", node.AvailableResources.CPU)
	}
}

// TestScheduleResourceExhausted proves the server surfaces a real
// ResourceExhausted error (not Scheduled=true) when no node has room.
//
// Mutation that breaks it: change the Schedule-error branch in ScheduleJob to
// return Scheduled:true / nil error. Then this expects an error and fails.
func TestScheduleResourceExhausted(t *testing.T) {
	srv := seedServer(t, 2000, 4096) // small node: 2 cores, 4 GB
	ctx := context.Background()

	resp, err := srv.ScheduleJob(ctx, &helixv1.ScheduleJobRequest{
		JobId: "too-big",
		Requirements: &helixv1.ResourceAllocation{
			CpuMillicores: 16000, // 16 cores — cannot fit
			MemoryBytes:   4096 * 1024 * 1024,
		},
	})
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("code = %v, want ResourceExhausted", status.Code(err))
	}
	if resp.Scheduled {
		t.Error("expected Scheduled=false on exhaustion")
	}
}

// TestGetJobStatusReportsStartedAt proves the status carries the placement's
// start timestamp — observable lifecycle metadata for end users.
//
// Mutation that breaks it: drop `startedAt: time.Now().Unix()` from the
// jobRecord written in ScheduleJob (leaving it zero). StartedAt then reports 0
// and this fails.
func TestGetJobStatusReportsStartedAt(t *testing.T) {
	srv := seedServer(t, 8000, 16384)
	ctx := context.Background()
	before := time.Now().Unix()

	if _, err := srv.ScheduleJob(ctx, &helixv1.ScheduleJobRequest{JobId: "ts"}); err != nil {
		t.Fatalf("ScheduleJob: %v", err)
	}

	st, err := srv.GetJobStatus(ctx, &helixv1.GetJobStatusRequest{JobId: "ts"})
	if err != nil {
		t.Fatalf("GetJobStatus: %v", err)
	}
	if st.StartedAt < before {
		t.Errorf("StartedAt = %d, want >= %d", st.StartedAt, before)
	}
}

// TestListJobsFiltersByNode proves the node-ID filter actually narrows results:
// a job on node-cap is returned when filtering by node-cap and excluded when
// filtering by a different node.
//
// Mutation that breaks it: remove the `if req.NodeId != "" && rec.nodeID !=
// req.NodeId { continue }` filter in ListJobs. The "other" filter then returns
// the job and this fails.
func TestListJobsFiltersByNode(t *testing.T) {
	srv := seedServer(t, 8000, 16384)
	ctx := context.Background()

	if _, err := srv.ScheduleJob(ctx, &helixv1.ScheduleJobRequest{JobId: "lj"}); err != nil {
		t.Fatalf("ScheduleJob: %v", err)
	}

	match, err := srv.ListJobs(ctx, &helixv1.ListJobsRequest{NodeId: "node-cap"})
	if err != nil {
		t.Fatalf("ListJobs match: %v", err)
	}
	if len(match.Jobs) != 1 {
		t.Fatalf("matching filter returned %d jobs, want 1", len(match.Jobs))
	}

	miss, err := srv.ListJobs(ctx, &helixv1.ListJobsRequest{NodeId: "no-such-node"})
	if err != nil {
		t.Fatalf("ListJobs miss: %v", err)
	}
	if len(miss.Jobs) != 0 {
		t.Errorf("non-matching node filter returned %d jobs, want 0", len(miss.Jobs))
	}
}

// TestCancelUnknownJobNotFound proves cancelling an unknown job is a NotFound
// error, not a silent success.
//
// Mutation that breaks it: change the `if !ok` branch in CancelJob to return
// Cancelled:true / nil. Then status.Code is OK and this fails.
func TestCancelUnknownJobNotFound(t *testing.T) {
	srv := seedServer(t, 8000, 16384)
	_, err := srv.CancelJob(context.Background(), &helixv1.CancelJobRequest{JobId: "ghost"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v, want NotFound", status.Code(err))
	}
}

// TestInstanceToNodeParsesMetadata proves the metadata->Node conversion parses
// cpu, memory_mb and gpu fields into real scheduler.Resources.
//
// Mutation that breaks it: change the gpu Sscanf format in instanceToNode from
// "%d" to "%f" (or drop the gpu block). GPU then parses to 0 and this fails.
func TestInstanceToNodeParsesMetadata(t *testing.T) {
	inst := &discovery.Instance{
		ID: "n1",
		Metadata: map[string]string{
			"cpu":       "12",
			"memory_mb": "32768",
			"gpu":       "4",
		},
	}
	node := instanceToNode(inst)
	if node.AvailableResources.CPU != 12 {
		t.Errorf("CPU = %v, want 12", node.AvailableResources.CPU)
	}
	if node.AvailableResources.Memory != 32768 {
		t.Errorf("Memory = %v, want 32768", node.AvailableResources.Memory)
	}
	if node.AvailableResources.GPU != 4 {
		t.Errorf("GPU = %v, want 4", node.AvailableResources.GPU)
	}
}
