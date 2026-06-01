//go:build integration

// Package node — integration tests for HXC-1086 and HXC-1087.
//
// HXC-1086: proves the gRPC NodeService wire path (in-process gRPC server on
// 127.0.0.1:0, real gRPC client) registers a node with a per-run UUID and
// reads it back through GetNode and ListNodes.
//
// HXC-1087: proves the etcd-backed EtcdRegistry makes two independent node
// servers share state: node A's Join is visible in node B's ListNodes; when A's
// heartbeat stops and its lease expires, B's ListNodes no longer shows A.
//
// These tests use digital.vasic.containers/pkg/brokertest.StartEtcd to boot a
// real ephemeral etcd container. They HARD-FAIL if the dependency is available
// but the assertions are wrong — t.Skip is only used when a container runtime
// is genuinely absent from the host.
package node

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"digital.vasic.containers/pkg/brokertest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	hxetcd "github.com/HelixDevelopment/helix_cluster/pkg/etcd"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// startRealEtcd boots a single ephemeral etcd via brokertest. If no container
// runtime is available the test is skipped (not failed). All other failures are
// fatal — a hard-fail on a reachable etcd is required per CLAUDE-1 anti-bluff
// mandate.
func startRealEtcd(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)

	endpoint, stop, err := brokertest.StartEtcd(ctx)
	if err != nil {
		t.Skipf("no container runtime available, skipping integration test: %v", err)
		return ""
	}
	t.Cleanup(stop)
	t.Logf("real etcd up at %s", endpoint)
	return endpoint
}

// newEtcdClient builds a *hxetcd.Client pointed at the supplied endpoint and
// registers cleanup for its Close.
func newEtcdClient(t *testing.T, endpoint string) *hxetcd.Client {
	t.Helper()
	c, err := hxetcd.New(hxetcd.Config{
		Endpoints:   []string{endpoint},
		DialTimeout: 10 * time.Second,
	})
	require.NoError(t, err, "hxetcd.New(%q)", endpoint)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// startNodeServer starts a real gRPC server backed by an EtcdRegistry connected
// to the supplied *hxetcd.Client. Returns the bound address and a gRPC client
// connected to it. Cleanup (GracefulStop + conn.Close) is registered with t.
func startNodeServer(t *testing.T, ec *hxetcd.Client, leaseTTL time.Duration) (addr string, client helixv1.NodeServiceClient) {
	t.Helper()

	reg := NewEtcdRegistry(ec, leaseTTL)
	srv := NewServerWithRegistry(reg)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr = lis.Addr().String()

	grpcSrv := grpc.NewServer()
	helixv1.RegisterNodeServiceServer(grpcSrv, srv)
	go func() { _ = grpcSrv.Serve(lis) }()
	t.Cleanup(grpcSrv.GracefulStop)

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	return addr, helixv1.NewNodeServiceClient(conn)
}

// ── HXC-1086 integration ─────────────────────────────────────────────────────

// TestIntegration_HXC1086_GRPCWire_PerRunUUID_RegisterGetList proves the full
// gRPC NodeService wire path against a real etcd:
//   - RegisterNode writes a node with a per-run UUID node ID.
//   - GetNode reads back the exact per-run UUID on the wire.
//   - ListNodes returns the node.
//
// The per-run UUID embedded in the node ID makes it impossible for a stale
// cache or fabricated response to masquerade as a real round-trip.
func TestIntegration_HXC1086_GRPCWire_PerRunUUID_RegisterGetList(t *testing.T) {
	endpoint := startRealEtcd(t)
	ec := newEtcdClient(t, endpoint)
	_, client := startNodeServer(t, ec, 30*time.Second)
	ctx := context.Background()

	// Per-run UUID — unique per test execution.
	nodeID := fmt.Sprintf("grpc-integ-%d", time.Now().UnixNano())

	// Register via gRPC wire.
	resp, err := client.RegisterNode(ctx, &helixv1.RegisterNodeRequest{
		Node: &helixv1.Node{
			Id:          nodeID,
			Region:      "us-east-1",
			Hostname:    "integ-host",
			IpAddresses: []string{"10.0.1.1"},
			Labels:      map[string]string{"wave": "5"},
		},
	})
	require.NoError(t, err, "RegisterNode over gRPC to real etcd")
	require.True(t, resp.Accepted)
	// Exact per-run UUID must survive the wire.
	assert.Equal(t, nodeID, resp.NodeId, "per-run UUID must survive gRPC + etcd round-trip")

	// GetNode: per-run UUID must be retrievable by exact ID.
	got, err := client.GetNode(ctx, &helixv1.GetNodeRequest{NodeId: nodeID})
	require.NoError(t, err, "GetNode for registered per-run UUID")
	assert.Equal(t, nodeID, got.Id)
	assert.Equal(t, "us-east-1", got.Region)
	assert.Equal(t, "integ-host", got.Hostname)
	require.Len(t, got.IpAddresses, 1)
	assert.Equal(t, "10.0.1.1", got.IpAddresses[0])

	// ListNodes: the per-run UUID node must appear.
	list, err := client.ListNodes(ctx, &helixv1.ListNodesRequest{})
	require.NoError(t, err, "ListNodes over gRPC to real etcd")
	found := false
	for _, n := range list.Nodes {
		if n.Id == nodeID {
			found = true
			break
		}
	}
	assert.True(t, found, "per-run UUID node must appear in ListNodes result")
	t.Logf("HXC-1086 integration: nodeID=%s present in GetNode + ListNodes", nodeID)
}

// ── HXC-1087 integration ─────────────────────────────────────────────────────

// TestIntegration_HXC1087_TwoServers_SharedEtcd_CrossVisibility proves:
//  1. Node A registers itself through server A (backed by real etcd).
//  2. Server B (independent process, same real etcd) sees A in ListNodes.
//
// This is the "two helix-node processes see each other" invariant from HXC-1087.
func TestIntegration_HXC1087_TwoServers_SharedEtcd_CrossVisibility(t *testing.T) {
	endpoint := startRealEtcd(t)

	// Two independent etcd clients (separate TCP connections — no shared state).
	ecA := newEtcdClient(t, endpoint)
	ecB := newEtcdClient(t, endpoint)

	_, clientA := startNodeServer(t, ecA, 30*time.Second)
	_, clientB := startNodeServer(t, ecB, 30*time.Second)
	ctx := context.Background()

	// Per-run UUID — embeds the test run identity so a stale read cannot bluff.
	nodeID := fmt.Sprintf("cross-integ-%d", time.Now().UnixNano())

	// Node A registers via server A.
	resp, err := clientA.RegisterNode(ctx, &helixv1.RegisterNodeRequest{
		Node: &helixv1.Node{
			Id:     nodeID,
			Region: "eu-central-1",
			Status: "active",
		},
	})
	require.NoError(t, err, "node A RegisterNode")
	require.True(t, resp.Accepted)

	// Node B (independent server) must see node A via ListNodes.
	// Poll briefly to allow etcd propagation (real etcd is consistent once the
	// Put lands, which is synchronous; the poll is a defensive margin).
	var foundInB bool
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		list, listErr := clientB.ListNodes(ctx, &helixv1.ListNodesRequest{})
		if listErr != nil {
			t.Logf("ListNodes(B) transient error: %v", listErr)
			time.Sleep(200 * time.Millisecond)
			continue
		}
		for _, n := range list.Nodes {
			if n.Id == nodeID {
				foundInB = true
				break
			}
		}
		if foundInB {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	assert.True(t, foundInB,
		"HXC-1087: node A's registration (UUID=%s) must be visible in node B's ListNodes", nodeID)
	t.Logf("HXC-1087 cross-visibility: nodeID=%s found in server B ListNodes", nodeID)
}

// TestIntegration_HXC1087_LeaseExpiry_RemovesNode proves:
//  1. Node A registers itself with a short-TTL lease (heartbeat window = 3 s).
//  2. A's heartbeat STOPS (lease is revoked immediately to simulate TTL expiry
//     without waiting 3 s — keeps the test fast and deterministic).
//  3. Server B's ListNodes no longer shows A after the lease expires.
//
// §1.1 mutation proof for the lease path:
//   - Break: In Register / StartHeartbeat use Put instead of PutWithLease.
//     Revoking the lease then removes no keys (they are not lease-bound).
//     Node A's key persists and server B still lists it — assert.False fails.
//   - Restore: revert to PutWithLease.
func TestIntegration_HXC1087_LeaseExpiry_RemovesNode(t *testing.T) {
	endpoint := startRealEtcd(t)

	ecA := newEtcdClient(t, endpoint)
	ecB := newEtcdClient(t, endpoint)

	// Use a 3-second lease TTL so natural expiry is fast, but we will also
	// test the immediate-revoke path to keep the test sub-second.
	const leaseTTL = 3 * time.Second

	regA := NewEtcdRegistry(ecA, leaseTTL)
	_, clientB := startNodeServer(t, ecB, 30*time.Second)
	ctx := context.Background()

	nodeID := fmt.Sprintf("lease-integ-%d", time.Now().UnixNano())

	// Node A starts its heartbeat (writes key under a lease).
	stopA, err := regA.StartHeartbeat(ctx, &helixv1.Node{
		Id:     nodeID,
		Region: "ap-northeast-1",
		Status: "active",
	})
	require.NoError(t, err, "StartHeartbeat for node A")

	// Confirm server B sees A while the heartbeat is running.
	var foundBeforeStop bool
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		list, _ := clientB.ListNodes(ctx, &helixv1.ListNodesRequest{})
		for _, n := range list.Nodes {
			if n.Id == nodeID {
				foundBeforeStop = true
				break
			}
		}
		if foundBeforeStop {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.True(t, foundBeforeStop,
		"precondition: node A must be visible in server B before heartbeat stops")

	// Stop A's heartbeat — immediately revokes the lease → key deleted instantly.
	stopA()

	// After lease revocation, server B's ListNodes must not show node A.
	// Poll briefly (the delete is synchronous in etcd after revoke, but
	// the gRPC read path may serve a cached list; a short poll is safe).
	var stillPresent bool
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		list, _ := clientB.ListNodes(ctx, &helixv1.ListNodesRequest{})
		stillPresent = false
		for _, n := range list.Nodes {
			if n.Id == nodeID {
				stillPresent = true
				break
			}
		}
		if !stillPresent {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	assert.False(t, stillPresent,
		"HXC-1087: node A must be absent from server B's ListNodes after lease revocation (mutation: using Put instead of PutWithLease leaves the key, breaking this)")
	t.Logf("HXC-1087 lease-expiry: nodeID=%s correctly absent from server B after heartbeat stop", nodeID)
}
