// Package sandbox provides a general-purpose capability-based guard with
// resource-limit enforcement and structured audit logging.
//
// Design intent: this package is entirely in-process. Real OS-level syscall
// isolation (seccomp, namespaces, pledge/unveil) requires host kernel features
// that are outside the scope of a pure-Go in-process guard; that fact is
// documented here and NOT faked. The genuine new value over pkg/wasm is
// resource-limit enforcement (MaxOps / MaxBytes / MaxDuration) applied across
// any in-process operation — not just WebAssembly host functions.
package sandbox

import (
	"sort"
	"strings"
)

// Capability names a single resource class a guarded operation may be
// permitted to use. Capabilities are deny-by-default: a Guard only allows a
// capability that has been explicitly granted via Capabilities.Grant.
type Capability string

const (
	// CapReadFS permits reading from the filesystem.
	CapReadFS Capability = "read_fs"
	// CapWriteFS permits writing to the filesystem.
	CapWriteFS Capability = "write_fs"
	// CapNetwork permits network access.
	CapNetwork Capability = "network"
	// CapExec permits executing external processes.
	CapExec Capability = "exec"
	// CapClock permits reading the host wall-clock.
	CapClock Capability = "clock"
)

// Capabilities is an explicit allow-list of resource classes. The zero value
// and a nil *Capabilities grant nothing (deny-by-default).
type Capabilities struct {
	granted map[Capability]bool
}

// NewCapabilities returns an allow-list pre-populated with caps. Passing no
// arguments yields an empty (deny-everything) grant set.
func NewCapabilities(caps ...Capability) *Capabilities {
	c := &Capabilities{granted: make(map[Capability]bool, len(caps))}
	for _, cap := range caps {
		c.granted[cap] = true
	}
	return c
}

// Grant adds cap to the allow-list and returns the receiver for chaining.
func (c *Capabilities) Grant(cap Capability) *Capabilities {
	if c.granted == nil {
		c.granted = make(map[Capability]bool)
	}
	c.granted[cap] = true
	return c
}

// Has reports whether cap is granted. A nil receiver grants nothing.
func (c *Capabilities) Has(cap Capability) bool {
	if c == nil || c.granted == nil {
		return false
	}
	return c.granted[cap]
}

// Granted returns the granted capabilities in deterministic (sorted) order.
func (c *Capabilities) Granted() []Capability {
	if c == nil {
		return nil
	}
	out := make([]Capability, 0, len(c.granted))
	for cap := range c.granted {
		out = append(out, cap)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// String renders the grant set deterministically, e.g.
// "sandbox.Capabilities{clock,network}".
func (c *Capabilities) String() string {
	g := c.Granted()
	parts := make([]string, len(g))
	for i, cap := range g {
		parts[i] = string(cap)
	}
	return "sandbox.Capabilities{" + strings.Join(parts, ",") + "}"
}
