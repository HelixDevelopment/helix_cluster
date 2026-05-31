package session

import (
	"fmt"
	"sort"
	"strings"
)

// This file extends CRDTSessionState with the operations required to PROVE
// state-based CRDT (CvRDT) convergence:
//
//   - Clone:        a deep, independent copy (so merges don't mutate inputs).
//   - Fingerprint:  a deterministic, content-only canonical encoding used to
//                   compare observable replica state for equality.
//
// It also provides the deterministic conflict-resolution primitives used by
// Merge / UpdateWindow / UpdatePane so that the merge is commutative,
// associative, and idempotent even when two replicas collide on the same
// (id, version) with DIFFERENT content. The internal global `version` counter
// is deliberately excluded from the fingerprint because it is per-replica
// bookkeeping, not observable shared state.

// Clone returns a deep, independent copy of the CRDT state.
func (c *CRDTSessionState) Clone() *CRDTSessionState {
	c.mu.RLock()
	defer c.mu.RUnlock()

	out := &CRDTSessionState{
		windows: make(map[string]*CRDTWindow, len(c.windows)),
		version: c.version,
	}
	for wid, w := range c.windows {
		out.windows[wid] = cloneWindow(w)
	}
	return out
}

// cloneWindow deep-copies a window (and its panes).
func cloneWindow(w *CRDTWindow) *CRDTWindow {
	cw := &CRDTWindow{
		ID:      w.ID,
		Name:    w.Name,
		Layout:  w.Layout,
		Version: w.Version,
		Panes:   make(map[string]*CRDTPane, len(w.Panes)),
	}
	for pid, p := range w.Panes {
		cw.Panes[pid] = clonePane(p)
	}
	return cw
}

// clonePane deep-copies a pane.
func clonePane(p *CRDTPane) *CRDTPane {
	return &CRDTPane{
		ID:         p.ID,
		Command:    p.Command,
		WorkingDir: p.WorkingDir,
		Status:     p.Status,
		Version:    p.Version,
	}
}

// Fingerprint returns a deterministic, content-only canonical string of the
// observable replica state. Two replicas with the same fingerprint hold the
// same shared state; the internal `version` counter is intentionally excluded.
func (c *CRDTSessionState) Fingerprint() string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	wids := make([]string, 0, len(c.windows))
	for wid := range c.windows {
		wids = append(wids, wid)
	}
	sort.Strings(wids)

	var b strings.Builder
	for _, wid := range wids {
		w := c.windows[wid]
		fmt.Fprintf(&b, "W{id=%s,name=%s,layout=%s,ver=%d|", w.ID, w.Name, w.Layout, w.Version)

		pids := make([]string, 0, len(w.Panes))
		for pid := range w.Panes {
			pids = append(pids, pid)
		}
		sort.Strings(pids)
		for _, pid := range pids {
			p := w.Panes[pid]
			fmt.Fprintf(&b, "P{id=%s,cmd=%s,dir=%s,st=%d,ver=%d}", p.ID, p.Command, p.WorkingDir, int(p.Status), p.Version)
		}
		b.WriteString("}")
	}
	return b.String()
}

// windowContentKey is the deterministic tie-break key for a window when two
// replicas collide on the same (ID, Version). Lexicographic ordering of this
// key gives a total order independent of merge direction.
func windowContentKey(w *CRDTWindow) string {
	return w.Name + "\x00" + w.Layout
}

// paneContentKey is the deterministic tie-break key for a pane on equal version.
func paneContentKey(p *CRDTPane) string {
	return fmt.Sprintf("%s\x00%s\x00%d", p.Command, p.WorkingDir, int(p.Status))
}

// windowScalarWins reports whether candidate ow should overwrite the scalar
// fields of cw. The rule is a total order: higher Version wins; on an equal
// Version, the lexicographically greater content key wins. This makes the
// choice independent of which replica initiated the merge (commutativity) and
// stable under repeated application (idempotence).
func windowScalarWins(ow, cw *CRDTWindow) bool {
	if ow.Version != cw.Version {
		return ow.Version > cw.Version
	}
	return windowContentKey(ow) > windowContentKey(cw)
}

// paneWins reports whether candidate pane op should overwrite cp using the same
// total-order rule as windows.
func paneWins(op, cp *CRDTPane) bool {
	if op.Version != cp.Version {
		return op.Version > cp.Version
	}
	return paneContentKey(op) > paneContentKey(cp)
}

// mergePanes deterministically merges src panes into dst (in place). dst panes
// map MUST be non-nil.
func mergePanes(dst, src map[string]*CRDTPane) {
	for pid, op := range src {
		cp, exists := dst[pid]
		if !exists || paneWins(op, cp) {
			dst[pid] = clonePane(op)
		}
	}
}
