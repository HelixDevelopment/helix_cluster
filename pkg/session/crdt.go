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
		if !exists || ow.Version > cw.Version {
			c.windows[wid] = &CRDTWindow{
				ID:      ow.ID,
				Name:    ow.Name,
				Layout:  ow.Layout,
				Panes:   make(map[string]*CRDTPane),
				Version: ow.Version,
			}
			for pid, op := range ow.Panes {
				c.windows[wid].Panes[pid] = &CRDTPane{
					ID:         op.ID,
					Command:    op.Command,
					WorkingDir: op.WorkingDir,
					Status:     op.Status,
					Version:    op.Version,
				}
			}
		} else if ow.Version == cw.Version {
			// Merge panes within the same window version.
			for pid, op := range ow.Panes {
				cp, pexists := cw.Panes[pid]
				if !pexists || op.Version > cp.Version {
					cw.Panes[pid] = &CRDTPane{
						ID:         op.ID,
						Command:    op.Command,
						WorkingDir: op.WorkingDir,
						Status:     op.Status,
						Version:    op.Version,
					}
				}
			}
		}
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
	if !exists || w.Version >= existing.Version {
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
		c.windows[w.ID] = &CRDTWindow{
			ID:      w.ID,
			Name:    w.Name,
			Layout:  w.Layout,
			Panes:   panes,
			Version: w.Version,
		}
		// If no version was provided, stamp with current global version.
		if c.windows[w.ID].Version == 0 {
			c.windows[w.ID].Version = c.version
		}
	}
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
	// When caller supplies an explicit version, use LWW semantics.
	// When no version is supplied (0), always overwrite with a fresh version.
	if !pexists || p.Version == 0 || p.Version >= existing.Version {
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
