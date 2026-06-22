package node

import (
	"context"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/discovery"
	hxetcd "github.com/HelixDevelopment/helix_cluster/pkg/etcd"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// keyCapturingEtcdClient is a minimal discovery.EtcdClient that records every
// key written via Put/PutWithLease. It lets a hermetic unit test assert the
// EXACT etcd key the node-agent discovery path produces, without a live etcd.
type keyCapturingEtcdClient struct {
	puts []string
}

func (c *keyCapturingEtcdClient) Put(_ context.Context, key, _ string) error {
	c.puts = append(c.puts, key)
	return nil
}

func (c *keyCapturingEtcdClient) PutWithLease(_ context.Context, key, _ string, _ clientv3.LeaseID) error {
	c.puts = append(c.puts, key)
	return nil
}

func (c *keyCapturingEtcdClient) GetPrefix(_ context.Context, _ string) (map[string]string, error) {
	return map[string]string{}, nil
}

func (c *keyCapturingEtcdClient) Delete(_ context.Context, _ string) error { return nil }

func (c *keyCapturingEtcdClient) Watch(_ context.Context, _ string) <-chan hxetcd.WatchEvent {
	ch := make(chan hxetcd.WatchEvent)
	close(ch)
	return ch
}

func (c *keyCapturingEtcdClient) Lease(_ context.Context, _ int64) (clientv3.LeaseID, error) {
	return clientv3.LeaseID(1), nil
}

func (c *keyCapturingEtcdClient) KeepAlive(_ context.Context, _ clientv3.LeaseID) (<-chan *clientv3.LeaseKeepAliveResponse, error) {
	ch := make(chan *clientv3.LeaseKeepAliveResponse)
	close(ch)
	return ch, nil
}

// TestNodeAgent_RegistersUnderCanonicalClusterosNodesKey is the HXC CLAUDE-1
// anti-bluff regression guard for the live-20260622 finding: helix-agent
// reported status=healthy but wrote its descriptor to a private namespace
// ("helix-node/helix-node/<id>") that the control plane never reads, instead of
// the canonical "/clusteros/nodes/<id>".
//
// It reproduces the exact discovery wiring Agent.Start uses for the etcd
// backend — NewEtcdBackend(client, etcd.Namespace) + a ServiceRegistry whose
// Instance.Service is discoveryNodeService — and asserts the written key is
// EXACTLY hxetcd.NodeKey(id) == "/clusteros/nodes/<id>".
//
// §1.1 mutation anchors that this test kills:
//   - reverting the backend prefix to "helix-node" → key becomes
//     "helix-node/nodes/<id>" → assertion fails.
//   - reverting discoveryNodeService to "helix-node" with prefix "helix-node"
//     → key becomes "helix-node/helix-node/<id>" (the original double-prefix
//     bug) → assertion fails.
func TestNodeAgent_RegistersUnderCanonicalClusterosNodesKey(t *testing.T) {
	const nodeID = "probe-node-canonical"

	cap := &keyCapturingEtcdClient{}

	// Mirror Agent.Start's etcd discovery wiring exactly.
	backend := discovery.NewEtcdBackend(cap, hxetcd.Namespace)
	registry := discovery.NewServiceRegistry(backend)

	inst := &discovery.Instance{
		ID:      nodeID,
		Service: discoveryNodeService,
		Address: "127.0.0.1:7946",
		TTL:     30 * time.Second,
	}
	if err := registry.Register(context.Background(), inst); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if len(cap.puts) == 0 {
		t.Fatal("agent registration wrote NO key to etcd — node would be invisible to the cluster")
	}

	want := hxetcd.NodeKey(nodeID) // /clusteros/nodes/probe-node-canonical
	got := cap.puts[len(cap.puts)-1]
	if got != want {
		t.Fatalf("node-agent registered under wrong etcd key:\n  got  %q\n  want %q\n"+
			"(the control plane reads /clusteros/nodes/<id>; any other key is a CLAUDE-1 "+
			"PASS-bluff: status=healthy but invisible to scheduling/federation)", got, want)
	}
}
