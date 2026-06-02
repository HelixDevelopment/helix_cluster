package discovery

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

var (
	// ErrDuplicateCell is returned by AddRemoteCell when the given cellID is
	// already registered (either as the local cell or as an existing remote).
	ErrDuplicateCell = errors.New("cell already registered")

	// ErrServiceNotFound is returned by Lookup when no cell — local or remote —
	// carries the requested service.
	ErrServiceNotFound = errors.New("service not found in any cell")
)

// FederatedInstance wraps a discovered Instance with the ID of the cell that
// owns it. The wrapper is returned by value so callers cannot mutate the
// registry's internal state; the embedded *Instance pointer still points to the
// registry's copy, so callers MUST NOT write through it.
type FederatedInstance struct {
	// CellID is the ID of the cell (local or remote) from which this instance
	// originated. It is set by FederatedDiscovery and never reflects
	// metadata stored in the Instance itself.
	CellID string

	// Instance is the service instance returned by the underlying
	// *ServiceRegistry.  It is read-only from the caller's perspective.
	Instance *Instance
}

// FederatedDiscovery holds a local *ServiceRegistry plus a keyed map of remote
// cell registries.  It implements cell-local preference (preferLocal) and
// deterministic global aggregation.
//
// All exported methods are safe for concurrent use.
type FederatedDiscovery struct {
	mu          sync.RWMutex
	localCellID string
	local       *ServiceRegistry
	remotes     map[string]*ServiceRegistry // cellID -> registry
}

// NewFederatedDiscovery creates a FederatedDiscovery that wraps local as the
// registry for localCellID.  Neither argument may be empty/nil.
func NewFederatedDiscovery(localCellID string, local *ServiceRegistry) *FederatedDiscovery {
	if localCellID == "" {
		panic("discovery.NewFederatedDiscovery: localCellID must not be empty")
	}
	if local == nil {
		panic("discovery.NewFederatedDiscovery: local registry must not be nil")
	}
	return &FederatedDiscovery{
		localCellID: localCellID,
		local:       local,
		remotes:     make(map[string]*ServiceRegistry),
	}
}

// AddRemoteCell registers a remote cell registry under cellID.  It returns
// [ErrDuplicateCell] (wrapped) if cellID equals the local cell ID or if cellID
// has already been added.  reg must not be nil.
func (fd *FederatedDiscovery) AddRemoteCell(cellID string, reg *ServiceRegistry) error {
	if reg == nil {
		return fmt.Errorf("AddRemoteCell %q: registry must not be nil", cellID)
	}
	fd.mu.Lock()
	defer fd.mu.Unlock()
	if cellID == fd.localCellID {
		return fmt.Errorf("AddRemoteCell: cellID %q equals local cell: %w", cellID, ErrDuplicateCell)
	}
	if _, exists := fd.remotes[cellID]; exists {
		return fmt.Errorf("AddRemoteCell: cellID %q already registered: %w", cellID, ErrDuplicateCell)
	}
	fd.remotes[cellID] = reg
	return nil
}

// RemoveRemoteCell removes the remote cell with the given cellID from the
// federation.  It is a no-op if cellID is not currently registered.
func (fd *FederatedDiscovery) RemoveRemoteCell(cellID string) {
	fd.mu.Lock()
	delete(fd.remotes, cellID)
	fd.mu.Unlock()
}

// RemoteCells returns a sorted, deterministic list of registered remote cell
// IDs.  The local cell ID is never included.
func (fd *FederatedDiscovery) RemoteCells() []string {
	fd.mu.RLock()
	ids := make([]string, 0, len(fd.remotes))
	for id := range fd.remotes {
		ids = append(ids, id)
	}
	fd.mu.RUnlock()
	sort.Strings(ids)
	return ids
}

// RegisterLocal registers inst into the local cell's *ServiceRegistry.
// It is a convenience wrapper around fd.local.Register.
func (fd *FederatedDiscovery) RegisterLocal(ctx context.Context, inst *Instance) error {
	return fd.local.Register(ctx, inst)
}

// Lookup returns all healthy instances for service across the federation.
//
// When preferLocal is true and the local registry has at least one healthy
// instance for service, ONLY those local instances are returned (each tagged
// with the local cell ID).  If the local registry has none, the method falls
// back to aggregating ALL cells (local + remote) exactly as if preferLocal
// were false.
//
// When preferLocal is false, instances from all cells are always aggregated.
//
// Aggregation order is deterministic: the local cell is always listed first,
// followed by remote cells in ascending lexicographic order of their cellID,
// and within each cell instances are ordered by instance ID.
//
// Lookup returns [ErrServiceNotFound] when no cell carries the service.
func (fd *FederatedDiscovery) Lookup(ctx context.Context, service string, preferLocal bool) ([]FederatedInstance, error) {
	fd.mu.RLock()
	// Snapshot remote map under read-lock so we don't hold the lock during I/O.
	remoteCells := make([]string, 0, len(fd.remotes))
	for id := range fd.remotes {
		remoteCells = append(remoteCells, id)
	}
	remoteRegs := make(map[string]*ServiceRegistry, len(fd.remotes))
	for id, reg := range fd.remotes {
		remoteRegs[id] = reg
	}
	localCellID := fd.localCellID
	localReg := fd.local
	fd.mu.RUnlock()

	sort.Strings(remoteCells)

	// Query the local registry first.
	localInsts, err := localReg.Lookup(ctx, service)
	if err != nil {
		return nil, fmt.Errorf("Lookup local cell %q: %w", localCellID, err)
	}

	// Apply the prefer-local short-circuit.
	// sortByID is applied here to keep the output order deterministic when there
	// are two or more local instances; the aggregate path below also calls
	// sortByID so both paths are consistent.
	if preferLocal && len(localInsts) > 0 {
		return wrapInstances(localCellID, sortByID(localInsts)), nil
	}

	// Aggregate: local first, then remotes in sorted order.
	var all []FederatedInstance

	sortedLocalInsts := sortByID(localInsts)
	all = append(all, wrapInstances(localCellID, sortedLocalInsts)...)

	for _, cid := range remoteCells {
		reg := remoteRegs[cid]
		insts, err := reg.Lookup(ctx, service)
		if err != nil {
			return nil, fmt.Errorf("Lookup remote cell %q: %w", cid, err)
		}
		all = append(all, wrapInstances(cid, sortByID(insts))...)
	}

	if len(all) == 0 {
		return nil, fmt.Errorf("service %q: %w", service, ErrServiceNotFound)
	}
	return all, nil
}

// wrapInstances tags each instance with cellID, returning copies of the
// FederatedInstance wrapper (not copies of the underlying Instance).
func wrapInstances(cellID string, insts []*Instance) []FederatedInstance {
	out := make([]FederatedInstance, len(insts))
	for i, inst := range insts {
		out[i] = FederatedInstance{CellID: cellID, Instance: inst}
	}
	return out
}

// sortByID returns a new slice of Instance pointers sorted ascending by ID.
// The original slice is not mutated.
func sortByID(insts []*Instance) []*Instance {
	out := make([]*Instance, len(insts))
	copy(out, insts)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
