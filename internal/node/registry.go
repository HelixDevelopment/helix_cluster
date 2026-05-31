package node

import (
	"context"
	"fmt"
	"sync"

	"github.com/HelixDevelopment/helix_cluster/api/v1"
	"github.com/HelixDevelopment/helix_cluster/pkg/discovery"
)

// NodeRegistry is a pluggable storage backend for cluster node registrations.
//
// The Server routes every registration, deregistration and lookup through a
// NodeRegistry so the node inventory can live somewhere shareable (e.g. an
// etcd-backed discovery registry) instead of being trapped in a single
// process's heap. Implementations MUST be safe for concurrent use.
//
// Lookup returns (nil, false) — not an error — when the node is absent, so the
// gRPC layer can map a clean miss to a not-found response.
type NodeRegistry interface {
	// Register stores (or replaces) a node under its ID.
	Register(ctx context.Context, n *helixv1.Node) error
	// Deregister removes a node by ID. Removing an unknown ID is not an error;
	// the boolean reports whether a node was actually present and removed.
	Deregister(ctx context.Context, id string) (bool, error)
	// Lookup returns the node with the given ID. ok is false when absent.
	Lookup(ctx context.Context, id string) (n *helixv1.Node, ok bool, err error)
	// List returns every registered node. Order is unspecified.
	List(ctx context.Context) ([]*helixv1.Node, error)
}

// memRegistry is the default in-process NodeRegistry. It preserves the exact
// behavior the Server had before the backend seam was introduced, so existing
// callers and tests keep working unchanged.
type memRegistry struct {
	mu    sync.RWMutex
	nodes map[string]*helixv1.Node
}

// newMemRegistry creates an empty in-memory node registry.
func newMemRegistry() *memRegistry {
	return &memRegistry{nodes: make(map[string]*helixv1.Node)}
}

func (m *memRegistry) Register(_ context.Context, n *helixv1.Node) error {
	if n == nil || n.Id == "" {
		return fmt.Errorf("node ID is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nodes[n.Id] = n
	return nil
}

func (m *memRegistry) Deregister(_ context.Context, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.nodes[id]; !ok {
		return false, nil
	}
	delete(m.nodes, id)
	return true, nil
}

func (m *memRegistry) Lookup(_ context.Context, id string) (*helixv1.Node, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n, ok := m.nodes[id]
	return n, ok, nil
}

func (m *memRegistry) List(_ context.Context) ([]*helixv1.Node, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*helixv1.Node, 0, len(m.nodes))
	for _, n := range m.nodes {
		out = append(out, n)
	}
	return out, nil
}

// nodeService is the discovery service name under which cluster nodes are
// registered when using the discovery-backed NodeRegistry adapter.
const nodeService = "helix-node"

// DiscoveryRegistry adapts the existing pkg/discovery Backend into a
// NodeRegistry. Registrations written through it become visible to anything
// reading the same Backend — so wiring in an etcd-backed discovery.Backend
// makes node registrations cluster-visible without changing the Server.
//
// It reads and writes the injected Backend directly (Put/Delete/List) keyed by
// "<service>/<id>", round-tripping the helixv1.Node fields through
// discovery.Instance. No live etcd is required: any discovery.Backend
// (InMemoryBackend or a fake) works in unit tests.
type DiscoveryRegistry struct {
	backend discovery.Backend
	service string
}

// NewDiscoveryRegistry builds a NodeRegistry that persists nodes through the
// supplied discovery.Backend under the "helix-node" service.
func NewDiscoveryRegistry(backend discovery.Backend) *DiscoveryRegistry {
	return &DiscoveryRegistry{backend: backend, service: nodeService}
}

func (d *DiscoveryRegistry) key(id string) string {
	return d.service + "/" + id
}

func (d *DiscoveryRegistry) Register(ctx context.Context, n *helixv1.Node) error {
	if n == nil || n.Id == "" {
		return fmt.Errorf("node ID is required")
	}
	return d.backend.Put(ctx, d.key(n.Id), nodeToInstance(n, d.service))
}

func (d *DiscoveryRegistry) Deregister(ctx context.Context, id string) (bool, error) {
	// Confirm presence first so we can report a truthful removed/not-present
	// result and treat an absent node as a clean no-op rather than an error.
	_, ok, err := d.Lookup(ctx, id)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	if err := d.backend.Delete(ctx, d.key(id)); err != nil {
		return false, err
	}
	return true, nil
}

func (d *DiscoveryRegistry) Lookup(ctx context.Context, id string) (*helixv1.Node, bool, error) {
	key := d.key(id)
	insts, err := d.backend.List(ctx, key)
	if err != nil {
		return nil, false, err
	}
	for _, inst := range insts {
		// List is a prefix scan; require an exact ID match so "n1" does not
		// match "n12".
		if inst.ID == id {
			return instanceToNode(inst), true, nil
		}
	}
	return nil, false, nil
}

func (d *DiscoveryRegistry) List(ctx context.Context) ([]*helixv1.Node, error) {
	insts, err := d.backend.List(ctx, d.service+"/")
	if err != nil {
		return nil, err
	}
	out := make([]*helixv1.Node, 0, len(insts))
	for _, inst := range insts {
		out = append(out, instanceToNode(inst))
	}
	return out, nil
}

// nodeToInstance projects a helixv1.Node onto a discovery.Instance, preserving
// the fields the node service cares about (region + labels) in Metadata so they
// survive a round-trip through the backend.
func nodeToInstance(n *helixv1.Node, service string) *discovery.Instance {
	meta := make(map[string]string, len(n.Labels)+3)
	for k, v := range n.Labels {
		meta[k] = v
	}
	meta["helix.region"] = n.Region
	meta["helix.status"] = n.Status
	meta["helix.hostname"] = n.Hostname

	var addr string
	if len(n.IpAddresses) > 0 {
		addr = n.IpAddresses[0]
	}

	return &discovery.Instance{
		ID:       n.Id,
		Service:  service,
		Address:  addr,
		Metadata: meta,
		Healthy:  true,
	}
}

// instanceToNode reconstructs a helixv1.Node from a discovery.Instance written
// by nodeToInstance. Helix bookkeeping keys are stripped back out of Labels.
func instanceToNode(inst *discovery.Instance) *helixv1.Node {
	labels := make(map[string]string, len(inst.Metadata))
	var region, status, hostname string
	for k, v := range inst.Metadata {
		switch k {
		case "helix.region":
			region = v
		case "helix.status":
			status = v
		case "helix.hostname":
			hostname = v
		default:
			labels[k] = v
		}
	}
	n := &helixv1.Node{
		Id:       inst.ID,
		Region:   region,
		Status:   status,
		Hostname: hostname,
		Labels:   labels,
	}
	if inst.Address != "" {
		n.IpAddresses = []string{inst.Address}
	}
	return n
}
