package node

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServerRegisterAndGetNode(t *testing.T) {
	srv := NewServer()
	ctx := context.Background()

	node := &helixv1.Node{
		Id:     "node-1",
		Region: "us-east-1",
		Labels: map[string]string{"tier": "T1"},
	}

	resp, err := srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{Node: node})
	require.NoError(t, err)
	assert.True(t, resp.Accepted)
	assert.Equal(t, "node-1", resp.NodeId)

	got, err := srv.GetNode(ctx, &helixv1.GetNodeRequest{NodeId: "node-1"})
	require.NoError(t, err)
	assert.Equal(t, "us-east-1", got.Region)
}

func TestServerGetNodeNotFound(t *testing.T) {
	srv := NewServer()
	ctx := context.Background()

	_, err := srv.GetNode(ctx, &helixv1.GetNodeRequest{NodeId: "missing"})
	assert.Error(t, err)
}

func TestServerListNodesWithFilters(t *testing.T) {
	srv := NewServer()
	ctx := context.Background()

	_, _ = srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{Node: &helixv1.Node{Id: "n1", Region: "us-east-1", Labels: map[string]string{"tier": "T1"}}})
	_, _ = srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{Node: &helixv1.Node{Id: "n2", Region: "us-west-2", Labels: map[string]string{"tier": "T2"}}})
	_, _ = srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{Node: &helixv1.Node{Id: "n3", Region: "us-east-1", Labels: map[string]string{"tier": "T1"}}})

	// Filter by region
	list, err := srv.ListNodes(ctx, &helixv1.ListNodesRequest{Region: "us-east-1"})
	require.NoError(t, err)
	assert.Len(t, list.Nodes, 2)

	// Filter by labels
	list2, err := srv.ListNodes(ctx, &helixv1.ListNodesRequest{Filters: map[string]string{"tier": "T2"}})
	require.NoError(t, err)
	assert.Len(t, list2.Nodes, 1)
	assert.Equal(t, "n2", list2.Nodes[0].Id)
}

func TestServerUpdateNodeStatus(t *testing.T) {
	srv := NewServer()
	ctx := context.Background()

	_, _ = srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{Node: &helixv1.Node{Id: "n1", Status: "active"}})

	updated, err := srv.UpdateNodeStatus(ctx, &helixv1.UpdateNodeStatusRequest{
		NodeId: "n1",
		Status: "maintenance",
		Health: &helixv1.HealthScore{Overall: 85},
	})
	require.NoError(t, err)
	assert.Equal(t, "maintenance", updated.Status)

	// Sink-side proof: the reported HealthScore must actually be persisted and
	// retrievable. Without this read-back, deleting the
	// `s.health[req.NodeId] = req.Health` line in UpdateNodeStatus would leave
	// this test green (a PASS-bluff).
	got, ok := srv.HealthScore("n1")
	require.True(t, ok, "health score must be persisted")
	assert.Equal(t, int32(85), got.Overall)
}

func TestServerDeregisterNode(t *testing.T) {
	srv := NewServer()
	ctx := context.Background()

	_, _ = srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{Node: &helixv1.Node{Id: "n1"}})

	resp, err := srv.DeregisterNode(ctx, &helixv1.DeregisterNodeRequest{NodeId: "n1"})
	require.NoError(t, err)
	assert.True(t, resp.Success)

	_, err = srv.GetNode(ctx, &helixv1.GetNodeRequest{NodeId: "n1"})
	assert.Error(t, err)
}

func TestServerConcurrentOperations(t *testing.T) {
	srv := NewServer()
	ctx := context.Background()

	var wg sync.WaitGroup

	// Concurrent registrations
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("node-%d", i)
			_, _ = srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{Node: &helixv1.Node{Id: id}})
		}(i)
	}

	// Concurrent reads
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = srv.ListNodes(ctx, &helixv1.ListNodesRequest{})
		}()
	}

	// Barrier: the test must not return while goroutines still touch srv,
	// otherwise -race flags an access after the test frame is gone.
	wg.Wait()

	// Sink-side proof that the concurrent registrations actually landed.
	list, err := srv.ListNodes(ctx, &helixv1.ListNodesRequest{})
	require.NoError(t, err)
	assert.Len(t, list.Nodes, 100)
}
