package federation

import (
	"sort"
	"sync"
	"time"
)

// TransitionEvent records a single lifecycle FSM transition for a cell. The
// registry emits one of these to every subscribed listener on each successful
// Advance, providing a sink-side audit trail of the federation's lifecycle.
type TransitionEvent struct {
	CellID string
	From   CellPhase
	To     CellPhase
	At     time.Time
}

// TransitionListener is a callback invoked (synchronously, under no lock) for
// each successful phase transition. Listeners must not call back into the
// registry on the same goroutine or they will deadlock-free but re-enter; keep
// them cheap (append to a slice, push to a channel).
type TransitionListener func(TransitionEvent)

// CellRegistry is a concurrency-safe directory of federated cells. It owns the
// canonical lifecycle state and is the only legal mutator of a stored Cell's
// phase, so the FSM invariants in cell.go hold globally.
type CellRegistry struct {
	mu        sync.RWMutex
	cells     map[string]*Cell
	listeners []TransitionListener

	// now is an injectable clock so transition timestamps are deterministic in
	// tests. Defaults to time.Now.
	now func() time.Time
}

// RegistryOption configures a CellRegistry.
type RegistryOption func(*CellRegistry)

// WithClock injects a time source for transition timestamps (test seam).
func WithClock(now func() time.Time) RegistryOption {
	return func(r *CellRegistry) {
		if now != nil {
			r.now = now
		}
	}
}

// NewCellRegistry constructs an empty registry.
func NewCellRegistry(opts ...RegistryOption) *CellRegistry {
	r := &CellRegistry{
		cells: make(map[string]*Cell),
		now:   time.Now,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// OnTransition subscribes a listener to lifecycle transition events. Listeners
// are invoked in subscription order. There is no unsubscribe; the registry is
// expected to outlive its listeners for the federation's lifetime.
func (r *CellRegistry) OnTransition(l TransitionListener) {
	if l == nil {
		return
	}
	r.mu.Lock()
	r.listeners = append(r.listeners, l)
	r.mu.Unlock()
}

// Register adds a cell to the registry. It returns ErrEmptyCellID if the cell
// has no id, or ErrCellExists if the id is already registered. The registry
// stores the supplied pointer and becomes its sole lifecycle mutator.
func (r *CellRegistry) Register(c *Cell) error {
	if c == nil || c.ID == "" {
		return ErrEmptyCellID
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.cells[c.ID]; ok {
		return ErrCellExists
	}
	r.cells[c.ID] = c
	return nil
}

// Deregister removes a cell by id. It returns ErrCellNotFound if the id is not
// present. After Deregister the cell no longer appears in Lookup or List.
func (r *CellRegistry) Deregister(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.cells[id]; !ok {
		return ErrCellNotFound
	}
	delete(r.cells, id)
	return nil
}

// Lookup returns a value snapshot of the cell with the given id, and a bool that
// is false if the cell is not registered. The snapshot is decoupled from the
// stored cell: later transitions do not mutate a returned snapshot.
func (r *CellRegistry) Lookup(id string) (Cell, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.cells[id]
	if !ok {
		return Cell{}, false
	}
	return c.clone(), true
}

// List returns value snapshots of all registered cells, sorted by id for
// deterministic output (and deterministic fan-out ordering in the aggregator).
func (r *CellRegistry) List() []Cell {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Cell, 0, len(r.cells))
	for _, c := range r.cells {
		out = append(out, c.clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Len reports the number of registered cells.
func (r *CellRegistry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.cells)
}

// Advance transitions the named cell to phase `to`, enforcing the lifecycle FSM.
// It returns ErrCellNotFound for an unknown id and ErrIllegalTransition for a
// disallowed edge (the stored cell is left unchanged on either error). On
// success it emits a TransitionEvent to all listeners AFTER releasing the lock,
// so listeners may safely call back into the registry.
func (r *CellRegistry) Advance(id string, to CellPhase) error {
	r.mu.Lock()
	c, ok := r.cells[id]
	if !ok {
		r.mu.Unlock()
		return ErrCellNotFound
	}
	from := c.phase
	at := r.now()
	if err := c.advance(to, at); err != nil {
		r.mu.Unlock()
		return err
	}
	// Snapshot listeners under the lock, then fire them after unlocking.
	listeners := make([]TransitionListener, len(r.listeners))
	copy(listeners, r.listeners)
	r.mu.Unlock()

	ev := TransitionEvent{CellID: id, From: from, To: to, At: at}
	for _, l := range listeners {
		l(ev)
	}
	return nil
}

// SetHealth updates the health classification (and optionally member count) of a
// cell without touching its lifecycle phase. It returns ErrCellNotFound for an
// unknown id. A negative memberCount is treated as "leave unchanged".
func (r *CellRegistry) SetHealth(id string, h Health, memberCount int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.cells[id]
	if !ok {
		return ErrCellNotFound
	}
	c.Health = h
	if memberCount >= 0 {
		c.MemberCount = memberCount
	}
	c.updatedAt = r.now()
	return nil
}
