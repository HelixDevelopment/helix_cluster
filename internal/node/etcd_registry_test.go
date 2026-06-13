// Package node — deterministic unit tests for EtcdRegistry (HXC-1087).
//
// These tests prove the EtcdRegistry satisfies the NodeRegistry contract and
// provides lease-TTL semantics, using a fake etcdRegistrySeam so no live etcd
// is required. All assertions are sink-side (exact byte values / exact state
// transitions), never "no panic" or "non-nil".
package node

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	clientv3 "go.etcd.io/etcd/client/v3"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
	hxetcd "github.com/HelixDevelopment/helix_cluster/pkg/etcd"
)

// ── fakeEtcdSeam ─────────────────────────────────────────────────────────────

// fakeEtcdSeam is a concurrency-safe in-memory implementation of
// etcdRegistrySeam. It models lease-based expiry: keys written under a lease
// are tracked in a per-lease set; RevokeLease removes all keys in the set.
// KeepAlive returns a closed channel (no-op keep-alive), which is sufficient
// for unit tests that test lease expiry via manual revocation.
type fakeEtcdSeam struct {
	mu        sync.Mutex
	kv        map[string]string             // key → value
	leaseKeys map[clientv3.LeaseID][]string // lease → keys under it
	nextLease clientv3.LeaseID
}

func newFakeEtcdSeam() *fakeEtcdSeam {
	return &fakeEtcdSeam{
		kv:        make(map[string]string),
		leaseKeys: make(map[clientv3.LeaseID][]string),
		nextLease: 1,
	}
}

func (f *fakeEtcdSeam) PutWithLease(_ context.Context, key, value string, id clientv3.LeaseID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kv[key] = value
	f.leaseKeys[id] = append(f.leaseKeys[id], key)
	return nil
}

func (f *fakeEtcdSeam) Put(_ context.Context, key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kv[key] = value
	return nil
}

func (f *fakeEtcdSeam) Get(_ context.Context, key string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.kv[key]
	return v, ok, nil
}

func (f *fakeEtcdSeam) GetPrefix(_ context.Context, prefix string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]string)
	for k, v := range f.kv {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			out[k] = v
		}
	}
	return out, nil
}

func (f *fakeEtcdSeam) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.kv, key)
	return nil
}

func (f *fakeEtcdSeam) Lease(_ context.Context, _ int64) (clientv3.LeaseID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.nextLease
	f.nextLease++
	return id, nil
}

func (f *fakeEtcdSeam) KeepAlive(_ context.Context, _ clientv3.LeaseID) (<-chan *clientv3.LeaseKeepAliveResponse, error) {
	ch := make(chan *clientv3.LeaseKeepAliveResponse)
	close(ch) // no-op: immediately closed, simulating a lease that doesn't need renewal for this test
	return ch, nil
}

func (f *fakeEtcdSeam) RevokeLease(_ context.Context, id clientv3.LeaseID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	keys := f.leaseKeys[id]
	for _, k := range keys {
		delete(f.kv, k)
	}
	delete(f.leaseKeys, id)
	return nil
}

// expireLease forcibly removes all keys attached to a lease and deletes the
// lease entry. This models TTL expiry without waiting for real time.
func (f *fakeEtcdSeam) expireLease(id clientv3.LeaseID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range f.leaseKeys[id] {
		delete(f.kv, k)
	}
	delete(f.leaseKeys, id)
}

// lastLeaseID returns the most recently granted lease ID (useful for
// deterministic expiry in tests without racing against background goroutines).
func (f *fakeEtcdSeam) lastLeaseID() clientv3.LeaseID {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nextLease - 1
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestEtcdRegistry_RegisterAndLookup proves Register writes the node into the
// seam and Lookup reads it back with exact field values.
//
// Mutation that breaks this: make Register skip PutWithLease. Lookup returns
// ok=false and the require.True assertion fails.
func TestEtcdRegistry_RegisterAndLookup(t *testing.T) {
	seam := newFakeEtcdSeam()
	reg := newEtcdRegistryWithSeam(seam, 30*time.Second)
	ctx := context.Background()

	nodeID := fmt.Sprintf("etcd-lu-%d", time.Now().UnixNano())
	n := &helixv1.Node{
		Id:          nodeID,
		Region:      "us-east-1",
		Status:      "active",
		Hostname:    "host-lu",
		IpAddresses: []string{"192.168.1.1"},
		Labels:      map[string]string{"tier": "T1"},
	}
	require.NoError(t, reg.Register(ctx, n))

	// Sink-side: Lookup must return exact field values.
	got, ok, err := reg.Lookup(ctx, nodeID)
	require.NoError(t, err)
	require.True(t, ok, "registered node must be found by Lookup")
	assert.Equal(t, nodeID, got.Id)
	assert.Equal(t, "us-east-1", got.Region)
	assert.Equal(t, "active", got.Status)
	assert.Equal(t, "host-lu", got.Hostname)
	require.Len(t, got.IpAddresses, 1)
	assert.Equal(t, "192.168.1.1", got.IpAddresses[0])
	assert.Equal(t, "T1", got.Labels["tier"])
}

// TestEtcdRegistry_LookupAbsent proves Lookup returns ok=false for an unknown
// node ID without returning an error.
//
// Mutation that breaks this: return ok=true unconditionally. The assert.False
// on ok fails.
func TestEtcdRegistry_LookupAbsent(t *testing.T) {
	seam := newFakeEtcdSeam()
	reg := newEtcdRegistryWithSeam(seam, 30*time.Second)
	ctx := context.Background()

	_, ok, err := reg.Lookup(ctx, "no-such-node")
	require.NoError(t, err)
	assert.False(t, ok, "Lookup for absent node must return ok=false")
}

// TestEtcdRegistry_List proves List returns every registered node, with each
// node's ID matching the registered value exactly.
//
// Mutation that breaks this: return an empty slice from List. The Len assertion
// fails.
func TestEtcdRegistry_List(t *testing.T) {
	seam := newFakeEtcdSeam()
	reg := newEtcdRegistryWithSeam(seam, 30*time.Second)
	ctx := context.Background()

	ids := []string{
		fmt.Sprintf("list-a-%d", time.Now().UnixNano()),
		fmt.Sprintf("list-b-%d", time.Now().UnixNano()),
		fmt.Sprintf("list-c-%d", time.Now().UnixNano()),
	}
	for _, id := range ids {
		require.NoError(t, reg.Register(ctx, &helixv1.Node{Id: id, Region: "eu"}))
	}

	nodes, err := reg.List(ctx)
	require.NoError(t, err)
	require.Len(t, nodes, len(ids), "List must return all registered nodes")

	got := make(map[string]bool)
	for _, n := range nodes {
		got[n.Id] = true
	}
	for _, id := range ids {
		assert.True(t, got[id], "List must include %s", id)
	}
}

// TestEtcdRegistry_Deregister_SinkSideAbsence proves Deregister removes the
// node from the seam (Lookup returns ok=false) and reports removed=true.
//
// Mutation that breaks this: make Deregister a no-op (skip Delete). The node
// persists in the seam and the assert.False(ok) fails.
func TestEtcdRegistry_Deregister_SinkSideAbsence(t *testing.T) {
	seam := newFakeEtcdSeam()
	reg := newEtcdRegistryWithSeam(seam, 30*time.Second)
	ctx := context.Background()

	nodeID := fmt.Sprintf("dereg-%d", time.Now().UnixNano())
	require.NoError(t, reg.Register(ctx, &helixv1.Node{Id: nodeID}))

	// Precondition.
	_, ok, _ := reg.Lookup(ctx, nodeID)
	require.True(t, ok, "precondition: node must be present before deregister")

	removed, err := reg.Deregister(ctx, nodeID)
	require.NoError(t, err)
	assert.True(t, removed, "Deregister must report removed=true")

	// Sink-side absence.
	_, ok, _ = reg.Lookup(ctx, nodeID)
	assert.False(t, ok, "Deregister must remove the node from the seam")
}

// TestEtcdRegistry_Deregister_UnknownNodeIsNoOp proves Deregister returns
// removed=false (not an error) when the node was never registered.
//
// Mutation that breaks this: return removed=true unconditionally. The
// assert.False(removed) fails.
func TestEtcdRegistry_Deregister_UnknownNodeIsNoOp(t *testing.T) {
	seam := newFakeEtcdSeam()
	reg := newEtcdRegistryWithSeam(seam, 30*time.Second)
	ctx := context.Background()

	removed, err := reg.Deregister(ctx, "never-registered")
	require.NoError(t, err)
	assert.False(t, removed, "Deregister of unknown node must report removed=false")
}

// TestEtcdRegistry_Register_RequiresNodeID proves Register returns an error
// when the node has an empty ID.
//
// Mutation that breaks this: remove the nil/empty check in Register. No error
// is returned and the require.Error assertion fails.
func TestEtcdRegistry_Register_RequiresNodeID(t *testing.T) {
	seam := newFakeEtcdSeam()
	reg := newEtcdRegistryWithSeam(seam, 30*time.Second)
	ctx := context.Background()

	err := reg.Register(ctx, &helixv1.Node{Id: ""})
	require.Error(t, err, "Register with empty ID must return an error")
	assert.Contains(t, err.Error(), "node ID is required")
}

// TestEtcdRegistry_LeaseExpiry_RemovesNode proves the lease-TTL semantics: a
// node registered under a lease becomes absent after the lease expires. In the
// unit test we simulate expiry by calling expireLease on the fake seam rather
// than waiting for a real clock.
//
// This is the critical mutation gate for HXC-1087: the mutation is dropping the
// lease (writing the key without a lease via Put instead of PutWithLease).
// When the lease is dropped the key never expires, so expireLease has no keys
// to delete, Lookup still returns ok=true, and the assert.False fails — which
// is exactly how we know the production code correctly uses PutWithLease.
//
// §1.1 mutation proof:
//   - Break: Replace `e.seam.PutWithLease(...)` with `e.seam.Put(...)` in Register.
//   - Observe: expireLease removes no keys (they are not lease-bound), so Lookup
//     still returns ok=true. assert.False(ok) FAILS for the right reason.
//   - Restore: Revert the change.
func TestEtcdRegistry_LeaseExpiry_RemovesNode(t *testing.T) {
	seam := newFakeEtcdSeam()
	reg := newEtcdRegistryWithSeam(seam, 2*time.Second)
	ctx := context.Background()

	nodeID := fmt.Sprintf("lease-exp-%d", time.Now().UnixNano())
	require.NoError(t, reg.Register(ctx, &helixv1.Node{Id: nodeID, Status: "active"}))

	// Precondition: node is visible after registration.
	_, ok, _ := reg.Lookup(ctx, nodeID)
	require.True(t, ok, "precondition: node must be present before expiry")

	// Simulate lease expiry (models TTL elapsed without heartbeat).
	leaseID := seam.lastLeaseID()
	seam.expireLease(leaseID)

	// Sink-side: node must be absent after lease expiry.
	_, ok, err := reg.Lookup(ctx, nodeID)
	require.NoError(t, err)
	assert.False(t, ok, "node must be absent after lease expiry — dropping the lease breaks this")
}

// TestEtcdRegistry_RegisterUsesNodeKeyPrefix proves the canonical
// /clusteros/nodes/ key prefix is used (not an arbitrary key), so two processes
// reading from the same etcd will agree on the key location.
//
// Mutation that breaks this: use a different key prefix in Register. The seam's
// kv map will not contain any key with the expected prefix, and require.Greater
// on matchCount fails.
func TestEtcdRegistry_RegisterUsesNodeKeyPrefix(t *testing.T) {
	seam := newFakeEtcdSeam()
	reg := newEtcdRegistryWithSeam(seam, 30*time.Second)
	ctx := context.Background()

	nodeID := fmt.Sprintf("prefix-check-%d", time.Now().UnixNano())
	require.NoError(t, reg.Register(ctx, &helixv1.Node{Id: nodeID}))

	// Inspect the seam directly: must find at least one key under
	// /clusteros/nodes/.
	expectedPrefix := hxetcd.NamespaceNodes + "/"
	seam.mu.Lock()
	var matchCount int
	for k := range seam.kv {
		if len(k) >= len(expectedPrefix) && k[:len(expectedPrefix)] == expectedPrefix {
			matchCount++
		}
	}
	seam.mu.Unlock()

	require.Greater(t, matchCount, 0,
		"Register must write under %s prefix; got no matching keys", expectedPrefix)
}

// TestEtcdRegistry_StartHeartbeat_ThenRevoke proves StartHeartbeat writes the
// node to the seam, and the returned stop function revokes the lease (removing
// the node immediately).
//
// Mutation that breaks this: make the stop function a no-op (skip RevokeLease).
// The key persists after stop(), and assert.False(ok) fails.
func TestEtcdRegistry_StartHeartbeat_ThenRevoke(t *testing.T) {
	seam := newFakeEtcdSeam()
	reg := newEtcdRegistryWithSeam(seam, 30*time.Second)
	ctx := context.Background()

	nodeID := fmt.Sprintf("hb-revoke-%d", time.Now().UnixNano())
	n := &helixv1.Node{Id: nodeID, Status: "active"}

	stop, err := reg.StartHeartbeat(ctx, n)
	require.NoError(t, err)

	// Precondition: node present after StartHeartbeat.
	_, ok, _ := reg.Lookup(ctx, nodeID)
	require.True(t, ok, "precondition: node must be present after StartHeartbeat")

	// Revoke immediately.
	stop()

	// Sink-side: node key must be gone.
	_, ok, _ = reg.Lookup(ctx, nodeID)
	assert.False(t, ok, "stop() must revoke the lease and remove the node")
}

// TestEtcdRegistry_CrossProcessVisibility proves that nodes written through one
// EtcdRegistry are visible to a second EtcdRegistry backed by the SAME seam.
// This models the "two processes sharing one etcd" scenario from HXC-1087
// without a live etcd connection.
//
// Mutation that breaks this: make Register write to a private in-process map
// instead of the shared seam. The second registry's Lookup finds nothing and
// assert.True(ok) fails.
func TestEtcdRegistry_CrossProcessVisibility(t *testing.T) {
	// One shared seam (models one real etcd cluster).
	seam := newFakeEtcdSeam()

	regA := newEtcdRegistryWithSeam(seam, 30*time.Second)
	regB := newEtcdRegistryWithSeam(seam, 30*time.Second)
	ctx := context.Background()

	nodeID := fmt.Sprintf("cross-%d", time.Now().UnixNano())

	// Node A registers itself.
	require.NoError(t, regA.Register(ctx, &helixv1.Node{
		Id: nodeID, Region: "us-west-2", Status: "active",
	}))

	// Node B (independent registry instance) must see A's registration.
	got, ok, err := regB.Lookup(ctx, nodeID)
	require.NoError(t, err)
	require.True(t, ok, "cross-process visibility: regB must find the node regA registered")
	assert.Equal(t, nodeID, got.Id)
	assert.Equal(t, "us-west-2", got.Region)

	// Node B must also see A in ListNodes.
	nodes, err := regB.List(ctx)
	require.NoError(t, err)
	found := false
	for _, n := range nodes {
		if n.Id == nodeID {
			found = true
			break
		}
	}
	assert.True(t, found, "cross-process: regB's ListNodes must include regA's node")
}
