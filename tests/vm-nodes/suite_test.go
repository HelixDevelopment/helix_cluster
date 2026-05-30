//go:build vm

package vmnodes

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func vmEnabled() bool {
	return os.Getenv("HELIX_VM_TESTS") == "1"
}

func TestMain(m *testing.M) {
	if !vmEnabled() {
		fmt.Println("Skipping VM tests (set HELIX_VM_TESTS=1 to enable)")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestClusterFormation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	skipIfNoKVM(t)
	skipIfNoVMImage(t)

	nodes := spawnNodes(ctx, t, 3, defaultNodeConfig())
	require.Len(t, nodes, 3)

	waitForClusterStable(ctx, t, nodes, 2*time.Minute)

	for i, node := range nodes {
		status, err := node.GetNodeStatus(ctx)
		require.NoError(t, err, "node %d status", i)
		require.Equal(t, "joined", status.State, "node %d should be joined", i)
	}
}

func TestNodeFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	skipIfNoKVM(t)
	skipIfNoVMImage(t)

	nodes := spawnNodes(ctx, t, 3, defaultNodeConfig())
	waitForClusterStable(ctx, t, nodes, 2*time.Minute)

	// Simulate failure on node 2
	err := nodes[2].SimulateFailure(ctx)
	require.NoError(t, err)

	// Wait for failure detection
	time.Sleep(10 * time.Second)

	// Verify remaining nodes still healthy
	for i := 0; i < 2; i++ {
		status, err := nodes[i].GetNodeStatus(ctx)
		require.NoError(t, err, "node %d should still be reachable", i)
		require.Equal(t, "joined", status.State)
	}
}

func TestNetworkPartition(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	skipIfNoKVM(t)
	skipIfNoVMImage(t)

	nodes := spawnNodes(ctx, t, 5, defaultNodeConfig())
	waitForClusterStable(ctx, t, nodes, 2*time.Minute)

	// Partition: isolate nodes 3,4 from 0,1,2
	err := nodes[3].SimulateNetworkPartition(ctx, 15*time.Second)
	require.NoError(t, err)
	err = nodes[4].SimulateNetworkPartition(ctx, 15*time.Second)
	require.NoError(t, err)

	time.Sleep(5 * time.Second)

	// Heal partition
	// (VMs auto-heal after duration)
	time.Sleep(15 * time.Second)

	// Verify cluster reconciles
	waitForClusterStable(ctx, t, nodes, 2*time.Minute)
}

func TestSessionMigration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	skipIfNoKVM(t)
	skipIfNoVMImage(t)

	nodes := spawnNodes(ctx, t, 2, defaultNodeConfig())
	waitForClusterStable(ctx, t, nodes, 2*time.Minute)

	// Create session on node 0
	// (placeholder — actual session creation requires helix-agent)
	t.Log("Session migration test: placeholder for full implementation")
}
