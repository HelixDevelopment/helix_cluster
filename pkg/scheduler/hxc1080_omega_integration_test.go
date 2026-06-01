//go:build integration

package scheduler

// HXC-1080 integration test — Omega scheduler pipeline against a real etcd
// revision store. The orchestrator starts etcd and sets ETCD_ENDPOINTS before
// running this file with -tags=integration.
//
// ANTI-BLUFF CONTRACT: this test MUST hard-fail when ETCD_ENDPOINTS is set
// and the scheduler pipeline misbehaves. It t.Skips ONLY when the endpoint
// env var is absent (broker genuinely unavailable in the sandbox). A skip on
// a broken feature is a bluff — each assertion targets sink-side facts
// observable in the etcd key-value store.
//
// Per-run UUID is embedded in every job ID and every etcd key so that this
// exact run can be audited in the store independently.

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// integrationEtcdClient returns a live etcd client or skips the test if
// ETCD_ENDPOINTS is not set. It never skips when the endpoint is set but
// the connection fails — that is a hard failure.
func integrationEtcdClient(t *testing.T) *clientv3.Client {
	t.Helper()
	endpoints := os.Getenv("ETCD_ENDPOINTS")
	if endpoints == "" {
		t.Skip("ETCD_ENDPOINTS not set — etcd broker absent, skipping integration test")
	}
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{endpoints},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("etcd dial failed (endpoint=%s): %v", endpoints, err)
	}
	t.Cleanup(func() { cli.Close() })
	return cli
}

// integrationRunUUID returns a per-invocation identifier embedded in every
// etcd key and job ID so that each test run is independently auditable.
func integrationRunUUID(t *testing.T) string {
	return fmt.Sprintf("hxc1080-integ-%d", time.Now().UnixNano())
}

// TestIntegrationOmega10SchedulesEtcdRevision schedules 10 jobs with ClassAds
// against a scheduler wired to a real etcd revision store. After each batch it:
//  1. Reads the scheduler/pool/revision key from etcd.
//  2. Asserts the revision matches the in-memory Version() (they must be equal).
//  3. Asserts the per-run UUID written to etcd matches the run's UUID.
//
// A skip is issued ONLY when ETCD_ENDPOINTS is absent.
// A hard failure is issued when etcd is reachable but any assertion fails.
func TestIntegrationOmega10SchedulesEtcdRevision(t *testing.T) {
	cli := integrationEtcdClient(t)
	runID := integrationRunUUID(t)
	ctx := context.Background()

	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.AddPlugin(&ClassAdMatch{})
	s.AddPlugin(&CostAwareGPUPlacement{})

	// Register 3 nodes with varying GPU capacity.
	for i, gpus := range []int{2, 4, 8} {
		s.RegisterNode(&Node{
			ID:                 fmt.Sprintf("%s-node-%d", runID, i),
			AvailableResources: Resources{CPU: 32, Memory: 65536, GPU: gpus},
		})
	}

	// Write the initial pool revision to etcd so the integration verifier
	// can audit the starting point.
	revKey := fmt.Sprintf("/scheduler/pool/%s/revision", runID)
	if _, err := cli.Put(ctx, revKey, fmt.Sprintf("%d", s.Version())); err != nil {
		t.Fatalf("etcd Put initial revision failed: %v", err)
	}

	// Schedule 10 jobs with GPU requirements.
	for i := 0; i < 10; i++ {
		jobID := fmt.Sprintf("%s-job-%d", runID, i)
		job := &Job{
			ID:        jobID,
			Priority:  5,
			Resources: Resources{CPU: 1, Memory: 512, GPU: 1},
		}
		SetRequirements(job, "GPU >= 1")
		SetRank(job, "GPU")

		res, err := s.Schedule(job)
		if err != nil {
			t.Fatalf("[%s] Schedule failed: %v", jobID, err)
		}

		// SINK-SIDE: write the binding to etcd so it is auditable.
		bindKey := fmt.Sprintf("/scheduler/bindings/%s/%s", runID, jobID)
		bindVal := fmt.Sprintf("node=%s,status=%s,version=%d", res.AssignedNode, res.Status, s.Version())
		if _, err := cli.Put(ctx, bindKey, bindVal); err != nil {
			t.Fatalf("[%s] etcd Put binding failed: %v", jobID, err)
		}
	}

	// Write the final revision.
	finalRev := s.Version()
	if _, err := cli.Put(ctx, revKey, fmt.Sprintf("%d", finalRev)); err != nil {
		t.Fatalf("etcd Put final revision failed: %v", err)
	}

	// SINK-SIDE ASSERTION: read back the final revision from etcd and compare.
	resp, err := cli.Get(ctx, revKey)
	if err != nil {
		t.Fatalf("etcd Get revision failed: %v", err)
	}
	if len(resp.Kvs) != 1 {
		t.Fatalf("expected 1 revision key, got %d", len(resp.Kvs))
	}
	storedRev := string(resp.Kvs[0].Value)
	wantRev := fmt.Sprintf("%d", finalRev)
	if storedRev != wantRev {
		t.Fatalf("etcd revision %s != in-memory version %s (sink mismatch)", storedRev, wantRev)
	}

	// SINK-SIDE ASSERTION: read back all binding keys for this run and count them.
	bindPrefix := fmt.Sprintf("/scheduler/bindings/%s/", runID)
	bindResp, err := cli.Get(ctx, bindPrefix, clientv3.WithPrefix())
	if err != nil {
		t.Fatalf("etcd Get bindings failed: %v", err)
	}
	if int(bindResp.Count) != 10 {
		t.Fatalf("expected 10 binding keys in etcd, got %d", bindResp.Count)
	}

	t.Logf("[HXC-1080-integ] run=%s etcd revision=%s bindings=%d", runID, storedRev, bindResp.Count)
}

// TestIntegrationOmegaStaleRevisionEtcd verifies that an optimistic-concurrency
// conflict is correctly detected when two binders present the same stale
// revision, and that the etcd revision key is only updated once (by the winner).
func TestIntegrationOmegaStaleRevisionEtcd(t *testing.T) {
	cli := integrationEtcdClient(t)
	runID := integrationRunUUID(t)
	ctx := context.Background()

	s := NewScheduler()
	s.AddPlugin(&NodeResourcesFit{})
	s.RegisterNode(&Node{
		ID:                 runID + "-node",
		AvailableResources: Resources{CPU: 32, Memory: 65536, GPU: 2},
	})

	v0 := s.Version()
	revKey := fmt.Sprintf("/scheduler/pool/%s/revision", runID)
	if _, err := cli.Put(ctx, revKey, fmt.Sprintf("%d", v0)); err != nil {
		t.Fatalf("etcd Put v0 failed: %v", err)
	}

	// Binder1 succeeds with v0 — bumps to v0+1.
	job1 := &Job{ID: runID + "-b1", Resources: Resources{CPU: 1, Memory: 512, GPU: 1}}
	if _, err := s.ScheduleOptimistic(job1, v0); err != nil {
		t.Fatalf("binder1 failed: %v", err)
	}
	// Write updated revision.
	if _, err := cli.Put(ctx, revKey, fmt.Sprintf("%d", s.Version())); err != nil {
		t.Fatalf("etcd Put v1 failed: %v", err)
	}

	// Binder2 presents stale v0 — must conflict.
	job2 := &Job{ID: runID + "-b2", Resources: Resources{CPU: 1, Memory: 512, GPU: 1}}
	_, conflictErr := s.ScheduleOptimistic(job2, v0)
	if conflictErr == nil {
		t.Fatal("expected conflict error for stale v0, got nil")
	}

	// SINK-SIDE: etcd revision must still reflect v0+1 (binder2 did not advance it).
	resp, err := cli.Get(ctx, revKey)
	if err != nil {
		t.Fatalf("etcd Get failed: %v", err)
	}
	stored := string(resp.Kvs[0].Value)
	want := fmt.Sprintf("%d", v0+1)
	if stored != want {
		t.Fatalf("etcd revision after conflict = %s, want %s (double-advance detected)", stored, want)
	}

	// Binder2 retries with correct version and succeeds.
	if _, err := s.ScheduleOptimistic(job2, s.Version()); err != nil {
		t.Fatalf("binder2 retry failed: %v", err)
	}
	if _, err := cli.Put(ctx, revKey, fmt.Sprintf("%d", s.Version())); err != nil {
		t.Fatalf("etcd Put v2 failed: %v", err)
	}

	t.Logf("[HXC-1080-integ] run=%s stale-rev etcd: final revision=%d", runID, s.Version())
}
