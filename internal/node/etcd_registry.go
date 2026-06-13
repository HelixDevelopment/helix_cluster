// Package node provides the gRPC NodeService implementation.
package node

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
	hxetcd "github.com/HelixDevelopment/helix_cluster/pkg/etcd"
)

// etcdRegistrySeam is the interface the EtcdRegistry uses to talk to etcd so
// unit tests can inject a fake without a live server.
type etcdRegistrySeam interface {
	// PutWithLease writes key=value under an existing lease.
	PutWithLease(ctx context.Context, key, value string, leaseID clientv3.LeaseID) error
	// Put writes key=value with no lease (durable until explicitly deleted).
	Put(ctx context.Context, key, value string) error
	// Get returns the value for key, ok=false when absent.
	Get(ctx context.Context, key string) (value string, ok bool, err error)
	// GetPrefix returns all key-value pairs under the prefix.
	GetPrefix(ctx context.Context, prefix string) (map[string]string, error)
	// Delete removes key.
	Delete(ctx context.Context, key string) error
	// Lease grants a lease with TTL seconds and returns the LeaseID.
	Lease(ctx context.Context, ttl int64) (clientv3.LeaseID, error)
	// KeepAlive keeps the lease alive until ctx is cancelled.
	KeepAlive(ctx context.Context, id clientv3.LeaseID) (<-chan *clientv3.LeaseKeepAliveResponse, error)
	// RevokeLease revokes the lease, deleting all attached keys immediately.
	RevokeLease(ctx context.Context, id clientv3.LeaseID) error
}

// realEtcdSeam adapts the *hxetcd.Client to etcdRegistrySeam.
type realEtcdSeam struct{ c *hxetcd.Client }

func (r *realEtcdSeam) PutWithLease(ctx context.Context, key, value string, id clientv3.LeaseID) error {
	return r.c.PutWithLease(ctx, key, value, id)
}
func (r *realEtcdSeam) Put(ctx context.Context, key, value string) error {
	return r.c.Put(ctx, key, value)
}
func (r *realEtcdSeam) Get(ctx context.Context, key string) (string, bool, error) {
	return r.c.Get(ctx, key)
}
func (r *realEtcdSeam) GetPrefix(ctx context.Context, prefix string) (map[string]string, error) {
	return r.c.GetPrefix(ctx, prefix)
}
func (r *realEtcdSeam) Delete(ctx context.Context, key string) error {
	return r.c.Delete(ctx, key)
}
func (r *realEtcdSeam) Lease(ctx context.Context, ttl int64) (clientv3.LeaseID, error) {
	return r.c.Lease(ctx, ttl)
}
func (r *realEtcdSeam) KeepAlive(ctx context.Context, id clientv3.LeaseID) (<-chan *clientv3.LeaseKeepAliveResponse, error) {
	return r.c.KeepAlive(ctx, id)
}
func (r *realEtcdSeam) RevokeLease(ctx context.Context, id clientv3.LeaseID) error {
	return r.c.RevokeLease(ctx, id)
}

// EtcdRegistry implements NodeRegistry persisting nodes into etcd under the
// /clusteros/nodes/ namespace. Keys are lease-bound: each Register call
// attaches the key to a lease whose TTL equals the heartbeat window. When the
// node's heartbeat stops the lease expires and the key is deleted automatically
// — making the node disappear from ListNodes on the next read.
//
// Two processes that share the same etcd will therefore see each other's nodes:
// node A's Join lands in etcd, node B's ListNodes reads etcd.
type EtcdRegistry struct {
	seam     etcdRegistrySeam
	leaseTTL int64 // seconds
}

// NewEtcdRegistry creates an EtcdRegistry backed by the supplied *hxetcd.Client.
// leaseTTL is the heartbeat window in seconds; keys expire after this many
// seconds without a Heartbeat (KeepAlive) renewal.
func NewEtcdRegistry(c *hxetcd.Client, leaseTTL time.Duration) *EtcdRegistry {
	ttlSec := int64(leaseTTL.Seconds())
	if ttlSec < 1 {
		ttlSec = 30
	}
	return &EtcdRegistry{seam: &realEtcdSeam{c: c}, leaseTTL: ttlSec}
}

// newEtcdRegistryWithSeam is the unit-test constructor: accepts any seam
// implementation instead of a real *hxetcd.Client.
func newEtcdRegistryWithSeam(seam etcdRegistrySeam, leaseTTL time.Duration) *EtcdRegistry {
	ttlSec := int64(leaseTTL.Seconds())
	if ttlSec < 1 {
		ttlSec = 2
	}
	return &EtcdRegistry{seam: seam, leaseTTL: ttlSec}
}

// nodePayload is the JSON structure stored in etcd per node key.
type nodePayload struct {
	ID          string            `json:"id"`
	Region      string            `json:"region"`
	Status      string            `json:"status"`
	Hostname    string            `json:"hostname"`
	IpAddresses []string          `json:"ip_addresses,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
}

func payloadFromNode(n *helixv1.Node) nodePayload {
	return nodePayload{
		ID:          n.Id,
		Region:      n.Region,
		Status:      n.Status,
		Hostname:    n.Hostname,
		IpAddresses: n.IpAddresses,
		Labels:      n.Labels,
	}
}

func (p nodePayload) toProto() *helixv1.Node {
	return &helixv1.Node{
		Id:          p.ID,
		Region:      p.Region,
		Status:      p.Status,
		Hostname:    p.Hostname,
		IpAddresses: p.IpAddresses,
		Labels:      p.Labels,
	}
}

// Register writes (or refreshes) the node into etcd under a NEW lease.
// Every Register obtains its own lease; the caller is responsible for keeping
// that lease alive via heartbeats. This method does not keep the old lease
// alive — callers that need lease continuity should use Heartbeat.
func (e *EtcdRegistry) Register(ctx context.Context, n *helixv1.Node) error {
	if n == nil || n.Id == "" {
		return fmt.Errorf("node ID is required")
	}

	payload, err := json.Marshal(payloadFromNode(n))
	if err != nil {
		return fmt.Errorf("marshal node: %w", err)
	}

	leaseID, err := e.seam.Lease(ctx, e.leaseTTL)
	if err != nil {
		return fmt.Errorf("grant lease for node %s: %w", n.Id, err)
	}

	key := hxetcd.NodeKey(n.Id)
	if err := e.seam.PutWithLease(ctx, key, string(payload), leaseID); err != nil {
		return fmt.Errorf("put node %s: %w", n.Id, err)
	}
	return nil
}

// Deregister removes the node's etcd key immediately. Removing an unknown node
// is not an error; the bool reports whether a node was actually present and
// removed. The associated lease will eventually expire on its own (we cannot
// revoke it without tracking lease IDs — lease tracking is done by callers via
// StartHeartbeat).
func (e *EtcdRegistry) Deregister(ctx context.Context, id string) (bool, error) {
	_, ok, err := e.Lookup(ctx, id)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	if err := e.seam.Delete(ctx, hxetcd.NodeKey(id)); err != nil {
		return false, fmt.Errorf("delete node %s: %w", id, err)
	}
	return true, nil
}

// Lookup returns the node stored under the canonical etcd key for id.
func (e *EtcdRegistry) Lookup(ctx context.Context, id string) (*helixv1.Node, bool, error) {
	val, ok, err := e.seam.Get(ctx, hxetcd.NodeKey(id))
	if err != nil {
		return nil, false, fmt.Errorf("get node %s: %w", id, err)
	}
	if !ok {
		return nil, false, nil
	}
	var p nodePayload
	if err := json.Unmarshal([]byte(val), &p); err != nil {
		return nil, false, fmt.Errorf("unmarshal node %s: %w", id, err)
	}
	return p.toProto(), true, nil
}

// List returns every node registered under /clusteros/nodes/.
func (e *EtcdRegistry) List(ctx context.Context) ([]*helixv1.Node, error) {
	prefix := hxetcd.NamespaceNodes + "/"
	kvs, err := e.seam.GetPrefix(ctx, prefix)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	out := make([]*helixv1.Node, 0, len(kvs))
	for key, val := range kvs {
		var p nodePayload
		if err := json.Unmarshal([]byte(val), &p); err != nil {
			return nil, fmt.Errorf("unmarshal node at key %s: %w", key, err)
		}
		out = append(out, p.toProto())
	}
	return out, nil
}

// StartHeartbeat grants a lease for nodeID and starts a keep-alive loop in the
// background. It writes the node to etcd under the new lease, then keeps the
// lease alive until ctx is cancelled. When ctx is cancelled the loop stops and
// the node's lease expires after leaseTTL seconds (causing the key to vanish).
//
// StartHeartbeat returns a stop function that may be called to revoke the lease
// immediately (instant key removal). Callers that want graceful expiry can
// simply cancel ctx and NOT call the stop function.
func (e *EtcdRegistry) StartHeartbeat(ctx context.Context, n *helixv1.Node) (stop func(), err error) {
	if n == nil || n.Id == "" {
		return func() {}, fmt.Errorf("node ID is required")
	}

	payload, err := json.Marshal(payloadFromNode(n))
	if err != nil {
		return func() {}, fmt.Errorf("marshal node: %w", err)
	}

	leaseID, err := e.seam.Lease(ctx, e.leaseTTL)
	if err != nil {
		return func() {}, fmt.Errorf("grant lease: %w", err)
	}

	key := hxetcd.NodeKey(n.Id)
	if err := e.seam.PutWithLease(ctx, key, string(payload), leaseID); err != nil {
		return func() {}, fmt.Errorf("put node: %w", err)
	}

	kaCh, err := e.seam.KeepAlive(ctx, leaseID)
	if err != nil {
		return func() {}, fmt.Errorf("keep-alive: %w", err)
	}

	// Drain the keep-alive channel in the background.
	go func() {
		for range kaCh {
			// drain; KeepAlive responses are sent periodically by etcd to
			// confirm the lease was renewed. We only need to consume them so
			// the channel does not block.
		}
	}()

	stop = func() {
		// Best-effort revoke; ignore the error — the lease expires anyway.
		rCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = e.seam.RevokeLease(rCtx, leaseID)
	}
	return stop, nil
}
