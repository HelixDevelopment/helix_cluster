package node

import (
	"context"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withClock installs a deterministic clock for the duration of a test and
// restores the real clock afterwards.
func withClock(t *testing.T, now time.Time) {
	t.Helper()
	prev := nowFunc
	nowFunc = func() time.Time { return now }
	t.Cleanup(func() { nowFunc = prev })
}

// TestUpdateNodeStatusPersistsHealth proves the reported HealthScore is actually
// stored and retrievable, not silently dropped.
//
// Mutation that breaks this: in UpdateNodeStatus, remove the line
//   s.health[req.NodeId] = req.Health
// (which is what the original code effectively did). HealthScore() then returns
// ok==false and the test fails.
func TestUpdateNodeStatusPersistsHealth(t *testing.T) {
	srv := NewServer()
	ctx := context.Background()

	_, err := srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{Node: &helixv1.Node{Id: "n1"}})
	require.NoError(t, err)

	_, err = srv.UpdateNodeStatus(ctx, &helixv1.UpdateNodeStatusRequest{
		NodeId: "n1",
		Status: "maintenance",
		Health: &helixv1.HealthScore{Overall: 85, Components: map[string]int32{"cpu": 90}},
	})
	require.NoError(t, err)

	got, ok := srv.HealthScore("n1")
	require.True(t, ok, "health score must be persisted")
	assert.Equal(t, int32(85), got.Overall)
	assert.Equal(t, int32(90), got.Components["cpu"])
}

// TestUpdateNodeStatusEmptyStatusKeepsExisting proves an empty Status in a
// heartbeat-style update does not clobber the node's current status.
//
// Mutation that breaks this: change the guarded assignment back to an
// unconditional `node.Status = req.Status`. The status becomes "" and the
// assertion fails.
func TestUpdateNodeStatusEmptyStatusKeepsExisting(t *testing.T) {
	srv := NewServer()
	ctx := context.Background()

	_, _ = srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{Node: &helixv1.Node{Id: "n1"}})
	// After register, status is forced to "active".

	updated, err := srv.UpdateNodeStatus(ctx, &helixv1.UpdateNodeStatusRequest{
		NodeId: "n1",
		Health: &helixv1.HealthScore{Overall: 70},
	})
	require.NoError(t, err)
	assert.Equal(t, "active", updated.Status, "empty status must not overwrite existing status")
}

// TestUpdateNodeStatusMissingNodeID proves the RPC rejects an empty node ID
// with a dedicated validation error rather than a generic not-found.
//
// Mutation that breaks this: delete the `if req.NodeId == ""` guard. The lookup
// then returns the "not found" error instead of "node ID is required", so the
// errorContains assertion fails.
func TestUpdateNodeStatusMissingNodeID(t *testing.T) {
	srv := NewServer()
	ctx := context.Background()

	_, err := srv.UpdateNodeStatus(ctx, &helixv1.UpdateNodeStatusRequest{NodeId: ""})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "node ID is required")
}

// TestUpdateNodeStatusNotFound proves updating an unknown node errors and does
// not create phantom health/last-seen entries.
//
// Mutation that breaks this: replace the `!ok` not-found return with creating a
// fresh node; HealthScore/LastSeen would then report ok==true.
func TestUpdateNodeStatusNotFound(t *testing.T) {
	srv := NewServer()
	ctx := context.Background()

	_, err := srv.UpdateNodeStatus(ctx, &helixv1.UpdateNodeStatusRequest{
		NodeId: "ghost",
		Health: &helixv1.HealthScore{Overall: 10},
	})
	require.Error(t, err)

	_, hOK := srv.HealthScore("ghost")
	assert.False(t, hOK)
	_, lOK := srv.LastSeen("ghost")
	assert.False(t, lOK)
}

// TestRegisterRecordsLastSeen proves registration timestamps the node so the
// registry can reason about liveness.
//
// Mutation that breaks this: remove `s.lastSeen[req.Node.Id] = nowFunc()` from
// RegisterNode. LastSeen() returns ok==false and the test fails.
func TestRegisterRecordsLastSeen(t *testing.T) {
	fixed := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	withClock(t, fixed)

	srv := NewServer()
	ctx := context.Background()
	_, _ = srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{Node: &helixv1.Node{Id: "n1"}})

	seen, ok := srv.LastSeen("n1")
	require.True(t, ok)
	assert.Equal(t, fixed, seen)
}

// TestHeartbeatRefreshesLastSeen proves UpdateNodeStatus advances the last-seen
// timestamp (i.e. it functions as a heartbeat).
//
// Mutation that breaks this: remove `s.lastSeen[req.NodeId] = nowFunc()` from
// UpdateNodeStatus. The timestamp stays at registration time and the equality
// against the later clock fails.
func TestHeartbeatRefreshesLastSeen(t *testing.T) {
	t0 := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	srv := NewServer()
	ctx := context.Background()

	withClock(t, t0)
	_, _ = srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{Node: &helixv1.Node{Id: "n1"}})

	t1 := t0.Add(90 * time.Second)
	nowFunc = func() time.Time { return t1 }
	_, err := srv.UpdateNodeStatus(ctx, &helixv1.UpdateNodeStatusRequest{NodeId: "n1", Status: "active"})
	require.NoError(t, err)

	seen, ok := srv.LastSeen("n1")
	require.True(t, ok)
	assert.Equal(t, t1, seen, "heartbeat must advance last-seen")
}

// TestStaleNodesDetectsExpiredHeartbeat proves stale-node detection compares
// against the configured threshold.
//
// Mutation that breaks this: flip the comparison in StaleNodes from
// `now.Sub(seen) > threshold` to `<`. The fresh node would be reported stale and
// the stale node would not, failing both assertions.
func TestStaleNodesDetectsExpiredHeartbeat(t *testing.T) {
	t0 := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	srv := NewServer()
	ctx := context.Background()

	// "old" registered far in the past, "fresh" registered just now.
	nowFunc = func() time.Time { return t0.Add(-10 * time.Minute) }
	_, _ = srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{Node: &helixv1.Node{Id: "old"}})
	nowFunc = func() time.Time { return t0 }
	_, _ = srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{Node: &helixv1.Node{Id: "fresh"}})
	t.Cleanup(func() { nowFunc = time.Now })

	stale := srv.StaleNodes(5 * time.Minute)
	assert.Contains(t, stale, "old")
	assert.NotContains(t, stale, "fresh")
}

// TestDeregisterClearsAuxiliaryState proves deregistration purges the health and
// last-seen bookkeeping, preventing unbounded growth / stale reads.
//
// Mutation that breaks this: drop `delete(s.health, req.NodeId)` and
// `delete(s.lastSeen, req.NodeId)` from DeregisterNode. The stale entries
// survive and the False assertions fail.
func TestDeregisterClearsAuxiliaryState(t *testing.T) {
	srv := NewServer()
	ctx := context.Background()

	_, _ = srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{Node: &helixv1.Node{Id: "n1"}})
	_, _ = srv.UpdateNodeStatus(ctx, &helixv1.UpdateNodeStatusRequest{
		NodeId: "n1",
		Health: &helixv1.HealthScore{Overall: 50},
	})

	resp, err := srv.DeregisterNode(ctx, &helixv1.DeregisterNodeRequest{NodeId: "n1"})
	require.NoError(t, err)
	require.True(t, resp.Success)

	_, hOK := srv.HealthScore("n1")
	assert.False(t, hOK, "health must be purged on deregister")
	_, lOK := srv.LastSeen("n1")
	assert.False(t, lOK, "last-seen must be purged on deregister")
}

// TestListNodesRegionAndLabelCombined proves region and label filters are
// AND-combined (a node must satisfy both to be returned).
//
// Mutation that breaks this: change matchLabels to return true unconditionally,
// or drop the region guard in ListNodes. The result count then changes from 1.
func TestListNodesRegionAndLabelCombined(t *testing.T) {
	srv := NewServer()
	ctx := context.Background()

	_, _ = srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{Node: &helixv1.Node{Id: "a", Region: "eu", Labels: map[string]string{"tier": "T1"}}})
	_, _ = srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{Node: &helixv1.Node{Id: "b", Region: "eu", Labels: map[string]string{"tier": "T2"}}})
	_, _ = srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{Node: &helixv1.Node{Id: "c", Region: "us", Labels: map[string]string{"tier": "T1"}}})

	list, err := srv.ListNodes(ctx, &helixv1.ListNodesRequest{
		Region:  "eu",
		Filters: map[string]string{"tier": "T1"},
	})
	require.NoError(t, err)
	require.Len(t, list.Nodes, 1)
	assert.Equal(t, "a", list.Nodes[0].Id)
}
