package session

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"sync"
)

// CRDTSessionState is a CRDT-based session state container.
// Uses a simple LWW (Last-Write-Wins) register for now.
type CRDTSessionState struct {
	mu      sync.RWMutex
	windows map[string]*CRDTWindow
	version uint64
}

// CRDTWindow represents a window in CRDT form.
type CRDTWindow struct {
	ID      string
	Name    string
	Layout  string
	Panes   map[string]*CRDTPane
	Version uint64
}

// CRDTPane represents a pane in CRDT form.
type CRDTPane struct {
	ID         string
	Command    string
	WorkingDir string
	Status     PaneStatus
	Version    uint64
}

// NewCRDTSessionState creates an empty CRDT state.
func NewCRDTSessionState() *CRDTSessionState {
	return &CRDTSessionState{
		windows: make(map[string]*CRDTWindow),
	}
}

// Merge merges another CRDT state (LWW semantics).
func (c *CRDTSessionState) Merge(other *CRDTSessionState) error {
	if other == nil {
		return nil
	}

	other.mu.RLock()
	defer other.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	for wid, ow := range other.windows {
		cw, exists := c.windows[wid]
		if !exists {
			// No local window: adopt a deep copy of the incoming one.
			c.windows[wid] = cloneWindow(ow)
			continue
		}

		// A window present on both replicas is the join of the two: scalar
		// fields are resolved by a deterministic total order (windowScalarWins)
		// and the pane set is the deterministic union (mergePanes). Resolving
		// scalars and panes INDEPENDENTLY — rather than copying one side
		// wholesale — is what makes Merge commutative and associative even when
		// the two windows carry different pane subsets at the same version.
		if cw.Panes == nil {
			cw.Panes = make(map[string]*CRDTPane)
		}
		if windowScalarWins(ow, cw) {
			cw.Name = ow.Name
			cw.Layout = ow.Layout
		}
		if ow.Version > cw.Version {
			cw.Version = ow.Version
		}
		mergePanes(cw.Panes, ow.Panes)
	}

	if other.version > c.version {
		c.version = other.version
	}

	return nil
}

// ToBytes serializes the CRDT state.
func (c *CRDTSessionState) ToBytes() ([]byte, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)

	snapshot := struct {
		Windows map[string]*CRDTWindow
		Version uint64
	}{
		Windows: c.windows,
		Version: c.version,
	}

	if err := enc.Encode(snapshot); err != nil {
		return nil, fmt.Errorf("encode crdt state: %w", err)
	}
	return buf.Bytes(), nil
}

// FromBytes deserializes CRDT state.
func FromBytes(data []byte) (*CRDTSessionState, error) {
	var snapshot struct {
		Windows map[string]*CRDTWindow
		Version uint64
	}

	buf := bytes.NewBuffer(data)
	dec := gob.NewDecoder(buf)
	if err := dec.Decode(&snapshot); err != nil {
		return nil, fmt.Errorf("decode crdt state: %w", err)
	}

	c := &CRDTSessionState{
		windows: make(map[string]*CRDTWindow),
		version: snapshot.Version,
	}

	for wid, w := range snapshot.Windows {
		c.windows[wid] = &CRDTWindow{
			ID:      w.ID,
			Name:    w.Name,
			Layout:  w.Layout,
			Panes:   make(map[string]*CRDTPane),
			Version: w.Version,
		}
		for pid, p := range w.Panes {
			c.windows[wid].Panes[pid] = &CRDTPane{
				ID:         p.ID,
				Command:    p.Command,
				WorkingDir: p.WorkingDir,
				Status:     p.Status,
				Version:    p.Version,
			}
		}
	}

	return c, nil
}

// UpdateWindow updates or creates a window.
func (c *CRDTSessionState) UpdateWindow(w *CRDTWindow) {
	if w == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.version++
	existing, exists := c.windows[w.ID]
	if !exists {
		// New window: adopt a deep copy directly.
		nw := cloneWindow(w)
		if nw.Panes == nil {
			nw.Panes = make(map[string]*CRDTPane)
		}
		// If no version was provided, stamp with current global version.
		if nw.Version == 0 {
			nw.Version = c.version
		}
		c.windows[w.ID] = nw
		return
	}

	// Existing window: scalar fields are resolved by a deterministic total
	// order (windowScalarWins) so that reordered / repeated local applies of
	// equal-version writes converge instead of degrading to "last caller wins".
	// A caller-supplied version of 0 means "freshest local write": stamp it
	// with the monotonic global counter and always take its scalars.
	if w.Version == 0 {
		// "Freshest local write" semantics: stamp the monotonic global counter
		// and take the incoming scalars.
		existing.Name = w.Name
		existing.Layout = w.Layout
		if c.version > existing.Version {
			existing.Version = c.version
		}
	} else if w.Version > existing.Version ||
		(w.Version == existing.Version && windowScalarWins(w, existing)) {
		existing.Name = w.Name
		existing.Layout = w.Layout
		if w.Version > existing.Version {
			existing.Version = w.Version
		}
	}

	// Panes supplied via UpdateWindow are merged in (deterministic union) rather
	// than replacing the existing pane set — a scalar/window update must not
	// destroy panes contributed by earlier or concurrent writes.
	if existing.Panes == nil {
		existing.Panes = make(map[string]*CRDTPane)
	}
	mergePanes(existing.Panes, w.Panes)
}

// UpdatePane updates or creates a pane within a window.
func (c *CRDTSessionState) UpdatePane(windowID string, p *CRDTPane) {
	if p == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.version++
	w, exists := c.windows[windowID]
	if !exists {
		w = &CRDTWindow{
			ID:      windowID,
			Panes:   make(map[string]*CRDTPane),
			Version: c.version,
		}
		c.windows[windowID] = w
	}

	existing, pexists := w.Panes[p.ID]
	// Overwrite when: the pane is new, the caller supplied no explicit version
	// (0 => freshest local write, stamped with the global counter), the
	// incoming version strictly dominates, or — on an EQUAL explicit version —
	// a deterministic total order (paneWins) selects the incoming content. The
	// deterministic tie-break keeps reordered / repeated equal-version writes
	// convergent instead of "last caller wins".
	overwrite := !pexists ||
		p.Version == 0 ||
		p.Version > existing.Version ||
		(p.Version == existing.Version && paneWins(p, existing))
	if overwrite {
		newVersion := p.Version
		if newVersion == 0 {
			newVersion = c.version
		}
		w.Panes[p.ID] = &CRDTPane{
			ID:         p.ID,
			Command:    p.Command,
			WorkingDir: p.WorkingDir,
			Status:     p.Status,
			Version:    newVersion,
		}
	}
}

// GetWindow returns a window by ID.
func (c *CRDTSessionState) GetWindow(id string) (*CRDTWindow, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	w, ok := c.windows[id]
	if !ok {
		return nil, false
	}

	// Return a copy to prevent external mutation.
	panes := make(map[string]*CRDTPane)
	for pid, p := range w.Panes {
		panes[pid] = &CRDTPane{
			ID:         p.ID,
			Command:    p.Command,
			WorkingDir: p.WorkingDir,
			Status:     p.Status,
			Version:    p.Version,
		}
	}

	return &CRDTWindow{
		ID:      w.ID,
		Name:    w.Name,
		Layout:  w.Layout,
		Panes:   panes,
		Version: w.Version,
	}, true
}

// GetPane returns a pane by window and pane ID.
func (c *CRDTSessionState) GetPane(windowID, paneID string) (*CRDTPane, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	w, ok := c.windows[windowID]
	if !ok {
		return nil, false
	}

	p, ok := w.Panes[paneID]
	if !ok {
		return nil, false
	}

	return &CRDTPane{
		ID:         p.ID,
		Command:    p.Command,
		WorkingDir: p.WorkingDir,
		Status:     p.Status,
		Version:    p.Version,
	}, true
}
