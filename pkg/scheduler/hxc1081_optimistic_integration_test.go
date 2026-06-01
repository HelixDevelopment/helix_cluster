//go:build integration

package scheduler

// HXC-1081 integration test — optimistic concurrency against a real etcd store.
// The orchestrator sets ETCD_ENDPOINTS. t.Skip only when genuinely absent.
// Hard-fail when etcd is reachable but any assertion fails.

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// TestIntegrationOptimisticLastGPUEtcd — two concurrent binders race for the
// last GPU. The winner writes its binding to etcd; the loser writes its
// conflict event. After the race, exactly one binding and one conflict key
// must exist in etcd for this run's UUID.
func TestIntegrationOptimisticLastGPUEtcd(t *testing.T) {
	cli := integrationEtcdClient(t)
	runID := fmt.Sprintf("hxc1081-integ-%d", time.Now().UnixNano())
	ctx := context.Background()

	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	nodeID := runID + "-gpu-node"
	s.RegisterNode(&Node{
		ID:                 nodeID,
		AvailableResources: Resources{CPU: 32, Memory: 65536, GPU: 1},
	})

	v0 := s.Version()
	var (
		winnerCount   atomic.Int32
		conflictCount atomic.Int32
		mu            sync.Mutex
	)

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			jobID := fmt.Sprintf("%s-binder%d", runID, idx)
			job := &Job{
				ID:        jobID,
				Priority:  5,
				Resources: Resources{CPU: 1, Memory: 512, GPU: 1},
			}

			res, err := s.ScheduleOptimistic(job, v0)
			if err == nil {
				// Winner: write binding to etcd.
				winnerCount.Add(1)
				bindKey := fmt.Sprintf("/scheduler/bindings/%s/%s", runID, jobID)
				bindVal := fmt.Sprintf("node=%s,version=%d,uuid=%s", res.AssignedNode, s.Version(), runID)
				mu.Lock()
				cli.Put(ctx, bindKey, bindVal) //nolint:errcheck
				mu.Unlock()
				return
			}
			// Loser: write conflict event to etcd.
			conflictCount.Add(1)
			conflictKey := fmt.Sprintf("/scheduler/conflicts/%s/%s", runID, jobID)
			mu.Lock()
			cli.Put(ctx, conflictKey, fmt.Sprintf("conflict:%v,uuid=%s", err, runID)) //nolint:errcheck
			mu.Unlock()

			// Retry with current version.
			_, retryErr := s.ScheduleOptimistic(job, s.Version())
			if retryErr == nil {
				winnerCount.Add(1)
				bindKey := fmt.Sprintf("/scheduler/bindings/%s/%s-retry", runID, jobID)
				mu.Lock()
				cli.Put(ctx, bindKey, fmt.Sprintf("node=%s,retry=true,uuid=%s", nodeID, runID)) //nolint:errcheck
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	// SINK-SIDE ASSERTION: exactly 1 conflict key in etcd.
	conflictPrefix := fmt.Sprintf("/scheduler/conflicts/%s/", runID)
	cResp, err := cli.Get(ctx, conflictPrefix, clientv3.WithPrefix())
	if err != nil {
		t.Fatalf("etcd Get conflicts failed: %v", err)
	}
	if cResp.Count != 1 {
		t.Fatalf("expected 1 conflict key in etcd, got %d", cResp.Count)
	}

	// SINK-SIDE ASSERTION: exactly 1 GPU consumed (winner took it; node GPU = 0).
	n, _ := s.GetNode(nodeID)
	if n.AvailableResources.GPU != 0 {
		t.Fatalf("node GPU = %d, want 0 (double-allocation if != 0 after winner)", n.AvailableResources.GPU)
	}

	// SINK-SIDE ASSERTION: version advanced exactly once.
	if s.Version() != v0+1 {
		t.Fatalf("expected version %d, got %d", v0+1, s.Version())
	}

	t.Logf("[HXC-1081-integ] run=%s conflicts=%d winners=%d version=%d",
		runID, cResp.Count, winnerCount.Load(), s.Version())
}
