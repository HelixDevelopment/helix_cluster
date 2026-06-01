//go:build integration

package scheduler

// HXC-1082 integration test — ClassAds requirements/rank pipeline with etcd
// audit of matched-node list and rank scores.
// t.Skip ONLY when ETCD_ENDPOINTS is absent. Hard-fail on any assertion error.

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// TestIntegrationClassAdFilterRankEtcd runs the ClassAd filter/rank pipeline
// against 5 nodes and writes the matched-node list + scores to etcd keyed by
// the per-run UUID. The orchestrator can then audit the keys to verify:
//   - exactly the expected nodes appear under /classads/<runID>/matched/
//   - rank scores are strictly ordered (readable from etcd values)
func TestIntegrationClassAdFilterRankEtcd(t *testing.T) {
	cli := integrationEtcdClient(t)
	runID := fmt.Sprintf("hxc1082-integ-%d", time.Now().UnixNano())
	ctx := context.Background()

	p := &ClassAdMatch{}

	nodes := []*Node{
		{ID: runID + "-gpu0", AvailableResources: Resources{GPU: 0}},
		{ID: runID + "-gpu1", AvailableResources: Resources{GPU: 1}},
		{ID: runID + "-gpu2", AvailableResources: Resources{GPU: 2}},
		{ID: runID + "-gpu4", AvailableResources: Resources{GPU: 4}},
		{ID: runID + "-gpu8", AvailableResources: Resources{GPU: 8}},
	}

	job := &Job{ID: runID + "-job"}
	SetRequirements(job, "GPU >= 2")
	SetRank(job, "GPU")

	var matched []string
	scores := map[string]int{}
	for _, n := range nodes {
		if p.Filter(job, n) {
			matched = append(matched, n.ID)
			scores[n.ID] = p.Score(job, n)
		}
	}

	// SINK-SIDE ASSERTION: exactly 3 nodes match (GPU >= 2: gpu2, gpu4, gpu8).
	if len(matched) != 3 {
		t.Fatalf("expected 3 matched nodes, got %d: %v", len(matched), matched)
	}

	// Write matched nodes to etcd with their scores.
	for _, id := range matched {
		key := fmt.Sprintf("/classads/%s/matched/%s", runID, id)
		val := fmt.Sprintf("score=%d,uuid=%s", scores[id], runID)
		if _, err := cli.Put(ctx, key, val); err != nil {
			t.Fatalf("etcd Put matched node %s failed: %v", id, err)
		}
	}

	// Read back and verify count + ordering.
	prefix := fmt.Sprintf("/classads/%s/matched/", runID)
	resp, err := cli.Get(ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		t.Fatalf("etcd Get matched failed: %v", err)
	}
	if resp.Count != 3 {
		t.Fatalf("etcd: expected 3 matched keys, got %d", resp.Count)
	}

	// Verify rank ordering: gpu8 > gpu4 > gpu2.
	sort.Strings(matched) // alphabetical by node ID for determinism.
	// gpu2=2000, gpu4=4000, gpu8=8000.
	if scores[runID+"-gpu2"] != 2000 {
		t.Fatalf("gpu2 score = %d, want 2000", scores[runID+"-gpu2"])
	}
	if scores[runID+"-gpu4"] != 4000 {
		t.Fatalf("gpu4 score = %d, want 4000", scores[runID+"-gpu4"])
	}
	if scores[runID+"-gpu8"] != 8000 {
		t.Fatalf("gpu8 score = %d, want 8000", scores[runID+"-gpu8"])
	}

	t.Logf("[HXC-1082-integ] run=%s matched=%v scores=%v etcd_count=%d",
		runID, matched, scores, resp.Count)
}
