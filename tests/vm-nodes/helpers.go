//go:build vm

package vmnodes

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// NodeSimulator is the interface we expect from containers/pkg/vm
type NodeSimulator interface {
	BootAndJoin(ctx context.Context, clusterEndpoint string) error
	LeaveCluster(ctx context.Context) error
	SimulateFailure(ctx context.Context) error
	SimulateNetworkPartition(ctx context.Context, duration time.Duration) error
	GetNodeStatus(ctx context.Context) (*NodeStatus, error)
}

// NodeStatus represents the status of a simulated node.
type NodeStatus struct {
	ID       string
	State    string
	Uptime   time.Duration
	Sessions []SessionInfo
}

// SessionInfo represents a session on a node.
type SessionInfo struct {
	ID     string
	Owner  string
	Status string
}

// simNode wraps a VM-based node for testing.
type simNode struct {
	id     string
	config *nodeConfig
}

type nodeConfig struct {
	nodeID           string
	clusterToken     string
	cpu              int
	memoryMB         int
	diskGB           int
	containerRuntime string
}

func defaultNodeConfig() *nodeConfig {
	return &nodeConfig{
		nodeID:           "vm-node",
		clusterToken:     "test-cluster",
		cpu:              2,
		memoryMB:         2048,
		diskGB:           10,
		containerRuntime: "podman",
	}
}

func (n *simNode) BootAndJoin(ctx context.Context, clusterEndpoint string) error {
	// Placeholder: in real implementation, this would use containers/pkg/vm
	fmt.Printf("Booting node %s and joining cluster at %s\n", n.id, clusterEndpoint)
	return nil
}

func (n *simNode) LeaveCluster(ctx context.Context) error {
	fmt.Printf("Node %s leaving cluster\n", n.id)
	return nil
}

func (n *simNode) SimulateFailure(ctx context.Context) error {
	fmt.Printf("Simulating failure on node %s\n", n.id)
	return nil
}

func (n *simNode) SimulateNetworkPartition(ctx context.Context, duration time.Duration) error {
	fmt.Printf("Simulating network partition on node %s for %v\n", n.id, duration)
	return nil
}

func (n *simNode) GetNodeStatus(ctx context.Context) (*NodeStatus, error) {
	return &NodeStatus{
		ID:     n.id,
		State:  "joined",
		Uptime: time.Minute,
	}, nil
}

func spawnNodes(ctx context.Context, t *testing.T, count int, baseConfig *nodeConfig) []NodeSimulator {
	t.Helper()
	nodes := make([]NodeSimulator, count)
	for i := 0; i < count; i++ {
		cfg := *baseConfig
		cfg.nodeID = fmt.Sprintf("%s-%d", baseConfig.nodeID, i)
		nodes[i] = &simNode{id: cfg.nodeID, config: &cfg}

		err := nodes[i].BootAndJoin(ctx, "helixd:50051")
		require.NoError(t, err, "failed to boot node %d", i)

		t.Cleanup(func() {
			_ = nodes[i].LeaveCluster(context.Background())
		})
	}
	return nodes
}

func waitForClusterStable(ctx context.Context, t *testing.T, nodes []NodeSimulator, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		allJoined := true
		for i, node := range nodes {
			status, err := node.GetNodeStatus(ctx)
			if err != nil || status.State != "joined" {
				allJoined = false
				t.Logf("Node %d not yet joined: err=%v", i, err)
				break
			}
		}
		if allJoined {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("cluster did not stabilize within %v", timeout)
}

func skipIfNoKVM(t *testing.T) {
	t.Helper()
	if os.Getenv("HELIX_VM_NO_KVM") == "1" {
		return // Allow TCG fallback
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("KVM not available (set HELIX_VM_NO_KVM=1 for TCG fallback)")
	}
}

func skipIfNoVMImage(t *testing.T) {
	t.Helper()
	path := getVMImagePath()
	if _, err := os.Stat(path); err != nil {
		t.Skipf("VM image not found at %s", path)
	}
}

func getVMImagePath() string {
	if path := os.Getenv("HELIX_VM_IMAGE"); path != "" {
		return path
	}
	return filepath.Join(os.Getenv("HOME"), ".helix", "vm-images", "alpine-test.qcow2")
}
