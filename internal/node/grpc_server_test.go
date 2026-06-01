// Package node — deterministic unit tests for the in-process gRPC server.
//
// HXC-1086: these tests prove the gRPC wire path end-to-end using a real
// gRPC server bound to 127.0.0.1:0 (a real OS port) and a real gRPC client.
// The registry is a fake (fakeRegistry) that satisfies the NodeRegistry
// interface so no live etcd is required. Sink-side assertions check exact
// field values on the wire, not just "no panic" or "non-nil".
package node

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
)

// fakeRegistry is a concurrency-safe in-process NodeRegistry for unit tests.
// It is the injected seam that keeps the gRPC test hermetic (no etcd, no
// network beyond the in-process gRPC server itself).
type fakeRegistry struct {
	mu    sync.RWMutex
	nodes map[string]*helixv1.Node
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{nodes: make(map[string]*helixv1.Node)}
}

func (f *fakeRegistry) Register(_ context.Context, n *helixv1.Node) error {
	if n == nil || n.Id == "" {
		return fmt.Errorf("node ID is required")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	// Deep-copy the proto so mutation by the caller does not affect the stored
	// value — matches the behaviour of a real persistence layer.
	stored := &helixv1.Node{
		Id:          n.Id,
		Region:      n.Region,
		Status:      n.Status,
		Hostname:    n.Hostname,
		IpAddresses: append([]string(nil), n.IpAddresses...),
		Labels:      copyLabels(n.Labels),
	}
	f.nodes[n.Id] = stored
	return nil
}

func (f *fakeRegistry) Deregister(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.nodes[id]; !ok {
		return false, nil
	}
	delete(f.nodes, id)
	return true, nil
}

func (f *fakeRegistry) Lookup(_ context.Context, id string) (*helixv1.Node, bool, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	n, ok := f.nodes[id]
	return n, ok, nil
}

func (f *fakeRegistry) List(_ context.Context) ([]*helixv1.Node, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]*helixv1.Node, 0, len(f.nodes))
	for _, n := range f.nodes {
		out = append(out, n)
	}
	return out, nil
}

func copyLabels(src map[string]string) map[string]string {
	if src == nil {
		return nil
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// startGRPCServer starts a real gRPC server on a random loopback port and
// registers the supplied NodeServiceServer. It returns a client connected to
// the same port and a cleanup function. The server is stopped and the
// connection closed when the test ends.
func startGRPCServer(t *testing.T, srv helixv1.NodeServiceServer) helixv1.NodeServiceClient {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "listen on 127.0.0.1:0")

	grpcSrv := grpc.NewServer()
	helixv1.RegisterNodeServiceServer(grpcSrv, srv)

	go func() {
		if err := grpcSrv.Serve(lis); err != nil {
			// Serve returns a non-nil error after GracefulStop; that is expected.
		}
	}()

	t.Cleanup(func() {
		grpcSrv.GracefulStop()
	})

	conn, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err, "grpc.NewClient")
	t.Cleanup(func() { _ = conn.Close() })

	return helixv1.NewNodeServiceClient(conn)
}

// ── HXC-1086: test 1 ─────────────────────────────────────────────────────────

// TestGRPC_RegisterAndGetNode_PerRunUUID proves the full gRPC wire path for
// RegisterNode → GetNode: a node registered with a per-run UUID node ID is
// retrievable byte-for-byte through the wire, not just from an in-process map.
//
// Mutation that breaks this: make Server.RegisterNode skip calling
// s.registry.Register. GetNode then returns "not found" and the test fails.
func TestGRPC_RegisterAndGetNode_PerRunUUID(t *testing.T) {
	nodeID := fmt.Sprintf("grpc-test-%d", time.Now().UnixNano())
	reg := newFakeRegistry()
	srv := NewServerWithRegistry(reg)
	client := startGRPCServer(t, srv)
	ctx := context.Background()

	// Register via gRPC wire.
	resp, err := client.RegisterNode(ctx, &helixv1.RegisterNodeRequest{
		Node: &helixv1.Node{
			Id:          nodeID,
			Region:      "eu-west-1",
			Hostname:    "host-grpc-test",
			IpAddresses: []string{"10.1.2.3"},
			Labels:      map[string]string{"env": "test"},
		},
	})
	require.NoError(t, err, "RegisterNode over gRPC")

	// Sink-side proof #1: the response carries the exact node ID sent.
	require.True(t, resp.Accepted, "RegisterNode response must be accepted")
	assert.Equal(t, nodeID, resp.NodeId, "per-run UUID must survive the wire round-trip")

	// Sink-side proof #2: GetNode reads back the exact fields.
	got, err := client.GetNode(ctx, &helixv1.GetNodeRequest{NodeId: nodeID})
	require.NoError(t, err, "GetNode over gRPC")
	assert.Equal(t, nodeID, got.Id)
	assert.Equal(t, "eu-west-1", got.Region)
	assert.Equal(t, "host-grpc-test", got.Hostname)
	require.Len(t, got.IpAddresses, 1)
	assert.Equal(t, "10.1.2.3", got.IpAddresses[0])
	assert.Equal(t, "test", got.Labels["env"])

	// Sink-side proof #3: the fakeRegistry backend actually holds the node
	// (proves the gRPC call went through the registry, not a local cache).
	stored, ok, _ := reg.Lookup(ctx, nodeID)
	require.True(t, ok, "registry must hold the node after RegisterNode")
	assert.Equal(t, nodeID, stored.Id)
}

// ── HXC-1086: test 2 ─────────────────────────────────────────────────────────

// TestGRPC_ListNodes_ReturnsRegisteredNodes proves ListNodes over the gRPC wire
// returns every registered node, with exact ID values (not just count > 0).
//
// Mutation that breaks this: make Server.ListNodes return an empty slice
// regardless of the registry contents. The assertion on exact IDs fails.
func TestGRPC_ListNodes_ReturnsRegisteredNodes(t *testing.T) {
	reg := newFakeRegistry()
	srv := NewServerWithRegistry(reg)
	client := startGRPCServer(t, srv)
	ctx := context.Background()

	ids := []string{
		fmt.Sprintf("list-node-a-%d", time.Now().UnixNano()),
		fmt.Sprintf("list-node-b-%d", time.Now().UnixNano()),
	}
	for _, id := range ids {
		_, err := client.RegisterNode(ctx, &helixv1.RegisterNodeRequest{
			Node: &helixv1.Node{Id: id, Region: "ap-southeast-1"},
		})
		require.NoError(t, err, "RegisterNode %s", id)
	}

	list, err := client.ListNodes(ctx, &helixv1.ListNodesRequest{})
	require.NoError(t, err, "ListNodes over gRPC")

	// Sink-side: both IDs must be present by exact value.
	got := make(map[string]bool)
	for _, n := range list.Nodes {
		got[n.Id] = true
	}
	for _, id := range ids {
		assert.True(t, got[id], "ListNodes must include node %s", id)
	}
}

// ── HXC-1086: test 3 ─────────────────────────────────────────────────────────

// TestGRPC_Heartbeat_RefreshesLastSeen proves UpdateNodeStatus (the heartbeat
// RPC) advances the last-seen timestamp visible through the Server helper.
//
// Mutation that breaks this: remove `s.lastSeen[req.NodeId] = nowFunc()` from
// UpdateNodeStatus. The timestamp stays at registration time and the assertion
// on t1 fails.
func TestGRPC_Heartbeat_RefreshesLastSeen(t *testing.T) {
	t0 := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	withClock(t, t0)

	reg := newFakeRegistry()
	srv := NewServerWithRegistry(reg)
	client := startGRPCServer(t, srv)
	ctx := context.Background()

	nodeID := fmt.Sprintf("hb-%d", time.Now().UnixNano())
	_, err := client.RegisterNode(ctx, &helixv1.RegisterNodeRequest{
		Node: &helixv1.Node{Id: nodeID},
	})
	require.NoError(t, err)

	// Advance the clock and send a heartbeat via UpdateNodeStatus.
	t1 := t0.Add(30 * time.Second)
	nowFunc = func() time.Time { return t1 }

	updated, err := client.UpdateNodeStatus(ctx, &helixv1.UpdateNodeStatusRequest{
		NodeId: nodeID,
		Status: "active",
		Health: &helixv1.HealthScore{Overall: 95},
	})
	require.NoError(t, err)
	assert.Equal(t, "active", updated.Status)

	// Sink-side: LastSeen must reflect t1, not t0.
	seen, ok := srv.LastSeen(nodeID)
	require.True(t, ok)
	assert.Equal(t, t1, seen, "heartbeat must advance last-seen to t1")
}

// ── HXC-1086: test 4 ─────────────────────────────────────────────────────────

// TestGRPC_DeregisterNode_RemovesNodeFromRegistry proves DeregisterNode over
// gRPC removes the node from the registry (sink-side absence), not just from an
// in-process cache.
//
// Mutation that breaks this: make Server.DeregisterNode skip calling
// s.registry.Deregister. The node survives in the registry, GetNode succeeds,
// and the error assertion fails.
func TestGRPC_DeregisterNode_RemovesNodeFromRegistry(t *testing.T) {
	reg := newFakeRegistry()
	srv := NewServerWithRegistry(reg)
	client := startGRPCServer(t, srv)
	ctx := context.Background()

	nodeID := fmt.Sprintf("dereg-%d", time.Now().UnixNano())
	_, err := client.RegisterNode(ctx, &helixv1.RegisterNodeRequest{
		Node: &helixv1.Node{Id: nodeID},
	})
	require.NoError(t, err)

	// Precondition: node is present.
	_, ok, _ := reg.Lookup(ctx, nodeID)
	require.True(t, ok, "precondition: node must be in registry before deregister")

	resp, err := client.DeregisterNode(ctx, &helixv1.DeregisterNodeRequest{NodeId: nodeID})
	require.NoError(t, err)
	assert.True(t, resp.Success)

	// Sink-side absence through the registry seam (not the gRPC path).
	_, ok, _ = reg.Lookup(ctx, nodeID)
	assert.False(t, ok, "node must be absent from registry after DeregisterNode")

	// And the gRPC read path must also report not found.
	_, err = client.GetNode(ctx, &helixv1.GetNodeRequest{NodeId: nodeID})
	assert.Error(t, err, "GetNode must return error for deregistered node")
}

// ── HXC-1086: test 5 ─────────────────────────────────────────────────────────

// TestGRPC_GetNode_NotFound proves GetNode over gRPC returns a gRPC error for
// an unknown node ID, not a nil response.
//
// Mutation that breaks this: return &helixv1.Node{} instead of an error for
// missing nodes. The require.Error assertion fails.
func TestGRPC_GetNode_NotFound(t *testing.T) {
	srv := NewServerWithRegistry(newFakeRegistry())
	client := startGRPCServer(t, srv)
	ctx := context.Background()

	_, err := client.GetNode(ctx, &helixv1.GetNodeRequest{NodeId: "does-not-exist"})
	require.Error(t, err, "GetNode for unknown ID must return an error")
}

// ── HXC-1086: test 6 ─────────────────────────────────────────────────────────

// TestGRPC_ListNodes_RegionFilter proves the region filter is honoured on the
// wire: only nodes matching the requested region are returned.
//
// Mutation that breaks this: drop the region-filter guard in ListNodes.
// All 3 nodes would be returned instead of 2, failing require.Len.
func TestGRPC_ListNodes_RegionFilter(t *testing.T) {
	reg := newFakeRegistry()
	srv := NewServerWithRegistry(reg)
	client := startGRPCServer(t, srv)
	ctx := context.Background()

	ts := time.Now().UnixNano()
	nodes := []struct {
		id     string
		region string
	}{
		{fmt.Sprintf("eu-1-%d", ts), "eu-west-1"},
		{fmt.Sprintf("eu-2-%d", ts), "eu-west-1"},
		{fmt.Sprintf("us-1-%d", ts), "us-east-1"},
	}
	for _, n := range nodes {
		_, err := client.RegisterNode(ctx, &helixv1.RegisterNodeRequest{
			Node: &helixv1.Node{Id: n.id, Region: n.region},
		})
		require.NoError(t, err)
	}

	list, err := client.ListNodes(ctx, &helixv1.ListNodesRequest{Region: "eu-west-1"})
	require.NoError(t, err)
	require.Len(t, list.Nodes, 2, "only the two eu-west-1 nodes must be returned")

	for _, n := range list.Nodes {
		assert.Equal(t, "eu-west-1", n.Region, "filter must exclude us-east-1 nodes")
	}
}
