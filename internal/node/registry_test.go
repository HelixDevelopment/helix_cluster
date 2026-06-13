package node

import (
	"context"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/apiv1"
	"github.com/HelixDevelopment/helix_cluster/pkg/discovery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServerRoutesRegistrationThroughBackendLookupAndList proves that a
// registration submitted through the gRPC RegisterNode entrypoint is actually
// written into the injected discovery.Backend, such that an INDEPENDENT reader
// of that same backend (not the Server's own map) can retrieve it by both
// Lookup and List.
//
// Mutation that breaks this: make memRegistry/DiscoveryRegistry.Register a
// no-op (drop the backend.Put), or make Server.RegisterNode stop calling
// s.registry.Register. The backend stays empty, so backend.List below returns
// zero instances and the assertions fail. This is the core sink-side proof:
// the node is retrievable THROUGH THE BACKEND, not merely returned by a stub.
func TestServerRoutesRegistrationThroughBackendLookupAndList(t *testing.T) {
	ctx := context.Background()
	backend := discovery.NewInMemoryBackend()
	srv := NewServerWithRegistry(NewDiscoveryRegistry(backend))

	_, err := srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{
		Node: &helixv1.Node{
			Id:          "node-A",
			Region:      "us-east-1",
			Hostname:    "host-a",
			IpAddresses: []string{"10.0.0.1"},
			Labels:      map[string]string{"tier": "T1"},
		},
	})
	require.NoError(t, err)

	// Sink-side proof #1: an independent reader of the backend sees the node via
	// the discovery service key prefix.
	insts, err := backend.List(ctx, "helix-node/")
	require.NoError(t, err)
	require.Len(t, insts, 1, "registration must land in the injected backend")
	assert.Equal(t, "node-A", insts[0].ID)
	assert.Equal(t, "10.0.0.1", insts[0].Address)
	assert.Equal(t, "us-east-1", insts[0].Metadata["helix.region"])
	assert.Equal(t, "T1", insts[0].Metadata["tier"])

	// Sink-side proof #2: the gRPC GetNode path reads back through the backend.
	got, err := srv.GetNode(ctx, &helixv1.GetNodeRequest{NodeId: "node-A"})
	require.NoError(t, err)
	assert.Equal(t, "us-east-1", got.Region)
	assert.Equal(t, "host-a", got.Hostname)
	require.Len(t, got.IpAddresses, 1)
	assert.Equal(t, "10.0.0.1", got.IpAddresses[0])

	// Sink-side proof #3: ListNodes (region filter) reflects the backend.
	list, err := srv.ListNodes(ctx, &helixv1.ListNodesRequest{Region: "us-east-1"})
	require.NoError(t, err)
	require.Len(t, list.Nodes, 1)
	assert.Equal(t, "node-A", list.Nodes[0].Id)
}

// TestServerDeregisterRemovesFromBackend proves deregistration via gRPC removes
// the node from the injected backend (sink-side ABSENCE), not just from a local
// cache.
//
// Mutation that breaks this: make DiscoveryRegistry.Deregister a no-op (skip
// backend.Delete) or always return removed=false. The backend would still hold
// the instance and backend.List would return 1, failing the empty assertion.
func TestServerDeregisterRemovesFromBackend(t *testing.T) {
	ctx := context.Background()
	backend := discovery.NewInMemoryBackend()
	srv := NewServerWithRegistry(NewDiscoveryRegistry(backend))

	_, err := srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{
		Node: &helixv1.Node{Id: "node-D", Region: "eu"},
	})
	require.NoError(t, err)
	pre, err := backend.List(ctx, "helix-node/")
	require.NoError(t, err)
	require.Len(t, pre, 1, "precondition: node present in backend")

	resp, err := srv.DeregisterNode(ctx, &helixv1.DeregisterNodeRequest{NodeId: "node-D"})
	require.NoError(t, err)
	assert.True(t, resp.Success)

	// Sink-side absence: the backend no longer holds the instance.
	post, err := backend.List(ctx, "helix-node/")
	require.NoError(t, err)
	assert.Empty(t, post, "deregister must remove the instance from the backend")

	// And the gRPC read path agrees.
	_, err = srv.GetNode(ctx, &helixv1.GetNodeRequest{NodeId: "node-D"})
	assert.Error(t, err)
}

// TestDiscoveryRegistryReadsWritesInjectedBackend proves the adapter genuinely
// round-trips through the SAME injected backend: a node written through the
// adapter is visible by Lookup/List, and one written by an independent path
// into the backend is visible through the adapter's Lookup. This exercises both
// directions of the read/write seam.
//
// Mutation that breaks this: have Lookup ignore the backend and return a
// canned/empty result, or have Register stash into a private map instead of the
// backend. Either direction's assertion then fails.
func TestDiscoveryRegistryReadsWritesInjectedBackend(t *testing.T) {
	ctx := context.Background()
	backend := discovery.NewInMemoryBackend()
	reg := NewDiscoveryRegistry(backend)

	// Write through the adapter -> must be readable from the raw backend.
	require.NoError(t, reg.Register(ctx, &helixv1.Node{Id: "w1", Region: "r1"}))
	raw, err := backend.List(ctx, "helix-node/")
	require.NoError(t, err)
	require.Len(t, raw, 1)
	assert.Equal(t, "w1", raw[0].ID)

	// Write directly into the backend (independent path) -> must be readable
	// through the adapter Lookup, proving the adapter really reads the backend.
	require.NoError(t, backend.Put(ctx, "helix-node/w2", &discovery.Instance{
		ID:      "w2",
		Service: "helix-node",
		Healthy: true,
		Metadata: map[string]string{
			"helix.region": "r2",
			"zone":         "z9",
		},
	}))
	got, ok, err := reg.Lookup(ctx, "w2")
	require.NoError(t, err)
	require.True(t, ok, "adapter Lookup must read what was put into the backend")
	assert.Equal(t, "r2", got.Region)
	assert.Equal(t, "z9", got.Labels["zone"])

	all, err := reg.List(ctx)
	require.NoError(t, err)
	assert.Len(t, all, 2, "List must reflect both backend writes")
}

// TestDiscoveryRegistryLookupExactIDNoPrefixCollision proves Lookup matches the
// exact node ID and does not return a different node whose ID merely shares the
// prefix (the backend List is a prefix scan).
//
// Mutation that breaks this: remove the `inst.ID == id` guard in
// DiscoveryRegistry.Lookup and return the first listed instance. Looking up
// "n1" would then wrongly return "n12".
func TestDiscoveryRegistryLookupExactIDNoPrefixCollision(t *testing.T) {
	ctx := context.Background()
	backend := discovery.NewInMemoryBackend()
	reg := NewDiscoveryRegistry(backend)

	require.NoError(t, reg.Register(ctx, &helixv1.Node{Id: "n12", Region: "r12"}))

	_, ok, err := reg.Lookup(ctx, "n1")
	require.NoError(t, err)
	assert.False(t, ok, "Lookup(n1) must not match n12 via prefix")

	got, ok, err := reg.Lookup(ctx, "n12")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "r12", got.Region)
}

// TestUpdateNodeStatusPersistsThroughBackend proves a status mutation submitted
// via gRPC is written back through the injected backend, so a shared/cluster
// reader sees the new status — not just the in-process caller.
//
// Mutation that breaks this: in UpdateNodeStatus drop the
// `s.registry.Register(ctx, node)` re-persist call. The backend keeps the old
// "active" status and the assertion on "maintenance" fails.
func TestUpdateNodeStatusPersistsThroughBackend(t *testing.T) {
	ctx := context.Background()
	backend := discovery.NewInMemoryBackend()
	srv := NewServerWithRegistry(NewDiscoveryRegistry(backend))

	_, err := srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{Node: &helixv1.Node{Id: "s1"}})
	require.NoError(t, err)

	_, err = srv.UpdateNodeStatus(ctx, &helixv1.UpdateNodeStatusRequest{
		NodeId: "s1",
		Status: "maintenance",
	})
	require.NoError(t, err)

	insts, err := backend.List(ctx, "helix-node/s1")
	require.NoError(t, err)
	require.Len(t, insts, 1)
	assert.Equal(t, "maintenance", insts[0].Metadata["helix.status"],
		"status mutation must be persisted through the backend")
}

// TestMemRegistryIsDefaultBackend proves NewServer() (no explicit registry)
// uses the in-process registry and remains fully functional — guarding
// backward compatibility of the default constructor.
//
// Mutation that breaks this: make NewServer wire a registry whose Register is a
// no-op. GetNode would then fail to find the node.
func TestMemRegistryIsDefaultBackend(t *testing.T) {
	ctx := context.Background()
	srv := NewServer()

	_, err := srv.RegisterNode(ctx, &helixv1.RegisterNodeRequest{
		Node: &helixv1.Node{Id: "def-1", Region: "dc"},
	})
	require.NoError(t, err)

	got, err := srv.GetNode(ctx, &helixv1.GetNodeRequest{NodeId: "def-1"})
	require.NoError(t, err)
	assert.Equal(t, "dc", got.Region)
}
