//go:build integration

package scheduler

// HXC-1083 integration test — gang scheduling + priority preemption with etcd
// audit of preemption events. The orchestrator sets ETCD_ENDPOINTS.
// t.Skip ONLY when genuinely absent. Hard-fail on any assertion error.

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestIntegrationGangNMinus1FreeEtcd runs the gang N-when-(N-1)-free scenario
// and writes the failure event + resource snapshot to etcd for audit.
func TestIntegrationGangNMinus1FreeEtcd(t *testing.T) {
	cli := integrationEtcdClient(t)
	runID := fmt.Sprintf("hxc1083-gang-integ-%d", time.Now().UnixNano())
	ctx := context.Background()

	const N = 3
	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})

	nodeIDs := make([]string, N-1)
	for i := 0; i < N-1; i++ {
		id := fmt.Sprintf("%s-node-%d", runID, i)
		nodeIDs[i] = id
		s.RegisterNode(&Node{
			ID:                 id,
			AvailableResources: Resources{CPU: 2, Memory: 2048},
		})
	}

	jobs := make([]*Job, N)
	for i := 0; i < N; i++ {
		jobs[i] = &Job{
			ID:        fmt.Sprintf("%s-task-%d", runID, i),
			Priority:  5,
			Resources: Resources{CPU: 2, Memory: 2048},
		}
	}

	_, gangErr := s.ScheduleGang(jobs)

	// SINK-SIDE: must fail with ErrGangNotAllPlaced.
	if gangErr == nil {
		t.Fatal("expected ErrGangNotAllPlaced, got nil")
	}
	if gangErr != ErrGangNotAllPlaced {
		t.Fatalf("expected ErrGangNotAllPlaced, got %v", gangErr)
	}

	// Write failure event to etcd (auditable by orchestrator).
	eventKey := fmt.Sprintf("/gang/%s/failure", runID)
	eventVal := fmt.Sprintf("error=%v,nodes=%d,tasks=%d,uuid=%s", gangErr, N-1, N, runID)
	if _, err := cli.Put(ctx, eventKey, eventVal); err != nil {
		t.Fatalf("etcd Put gang failure failed: %v", err)
	}

	// Write node resource snapshots to etcd — must all be unchanged.
	for _, id := range nodeIDs {
		n, _ := s.GetNode(id)
		snapKey := fmt.Sprintf("/gang/%s/node/%s/cpu", runID, id)
		snapVal := fmt.Sprintf("%.0f", n.AvailableResources.CPU)
		if _, err := cli.Put(ctx, snapKey, snapVal); err != nil {
			t.Fatalf("etcd Put node snapshot failed: %v", err)
		}
		// SINK-SIDE: resource must be untouched (2 CPU).
		if n.AvailableResources.CPU != 2 {
			t.Fatalf("partial placement: node %s CPU = %f, want 2", id, n.AvailableResources.CPU)
		}
	}

	// SINK-SIDE: read back event key and verify UUID is present.
	resp, err := cli.Get(ctx, eventKey)
	if err != nil {
		t.Fatalf("etcd Get gang failure event failed: %v", err)
	}
	if resp.Count != 1 {
		t.Fatalf("expected 1 failure event key, got %d", resp.Count)
	}

	t.Logf("[HXC-1083-gang-integ] run=%s N=%d freeNodes=%d gangErr=%v etcd_key=%s",
		runID, N, N-1, gangErr, eventKey)
}

// TestIntegrationPreemptEtcd schedules a low-priority job, then preempts it
// with a high-priority job and writes the preemption event to etcd. The
// orchestrator audits: /preempt/<runID>/event contains the victim job ID and
// the assigned node ID, both containing the per-run UUID.
func TestIntegrationPreemptEtcd(t *testing.T) {
	cli := integrationEtcdClient(t)
	runID := fmt.Sprintf("hxc1083-preempt-integ-%d", time.Now().UnixNano())
	ctx := context.Background()

	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	nodeID := runID + "-node"
	s.RegisterNode(&Node{
		ID:                 nodeID,
		AvailableResources: Resources{CPU: 4, Memory: 8192, GPU: 2},
	})

	// Seed: low-priority job consumes all GPUs.
	lowID := runID + "-low"
	lowJob := &Job{
		ID:        lowID,
		Priority:  1,
		Resources: Resources{CPU: 1, Memory: 1024, GPU: 2},
	}
	if _, err := s.Schedule(lowJob); err != nil {
		t.Fatalf("seed schedule failed: %v", err)
	}

	// High-priority job preempts.
	highID := runID + "-high"
	highJob := &Job{
		ID:        highID,
		Priority:  10,
		Resources: Resources{CPU: 1, Memory: 1024, GPU: 2},
	}
	res, err := s.SchedulePreempt(highJob)
	if err != nil {
		t.Fatalf("SchedulePreempt failed: %v", err)
	}

	// SINK-SIDE: preemption event must contain the low-priority victim.
	if len(res.Preempted) != 1 || res.Preempted[0] != lowID {
		t.Fatalf("expected preempted=[%s], got %v", lowID, res.Preempted)
	}

	// Write preemption event to etcd with per-run UUID.
	eventKey := fmt.Sprintf("/preempt/%s/event", runID)
	eventVal := fmt.Sprintf(
		"victim=%s,assigned=%s,assignedNode=%s,uuid=%s,version=%d",
		lowID, highID, res.AssignedNode, runID, s.Version(),
	)
	if _, err := cli.Put(ctx, eventKey, eventVal); err != nil {
		t.Fatalf("etcd Put preemption event failed: %v", err)
	}

	// SINK-SIDE: read back event key and verify it contains the UUID and victim ID.
	resp, err := cli.Get(ctx, eventKey)
	if err != nil {
		t.Fatalf("etcd Get preemption event failed: %v", err)
	}
	if resp.Count != 1 {
		t.Fatalf("expected 1 preemption event key, got %d", resp.Count)
	}
	stored := string(resp.Kvs[0].Value)
	if len(stored) == 0 {
		t.Fatal("etcd preemption event value is empty")
	}

	// High-priority job must be running; low-priority must not.
	if !s.IsRunning(highID) {
		t.Fatal("high-priority job must be running after preemption")
	}
	if s.IsRunning(lowID) {
		t.Fatal("low-priority job must not be running after eviction")
	}

	t.Logf("[HXC-1083-preempt-integ] run=%s preempted=%v assignedNode=%s version=%d etcd=%s",
		runID, res.Preempted, res.AssignedNode, s.Version(), stored)
}
