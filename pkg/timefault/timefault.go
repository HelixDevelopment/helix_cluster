// Package timefault provides deterministic time-fault injectors and a
// clock-skew detector for testing distributed-clock robustness in HelixCluster.
//
// # Model
//
// A shared base clock advances in discrete ticks (seconds). Each node derives
// its own logical clock from the base tick through a per-node Fault. The base
// clock represents the host / quorum reference time and is never mutated by a
// node-level fault.
//
// Three fault injectors are provided:
//
//   - SkewFault: offsets a node's clock by a fixed delta so it reads base+delta.
//   - FreezeFault: stops a node's clock at the instant it was frozen; the node
//     falls progressively behind as the base advances.
//   - MonotonicViolationFault: makes a node's clock jump backward exactly once
//     at a given base instant, violating monotonicity, then resumes tracking
//     the base from the (lower) jumped value.
//
// A Detector compares each node's reported time against the base reference and
// flags a node INCONSISTENT when |reported-base| exceeds a tolerance. It also
// detects monotonicity violations distinctly from skew by observing a backward
// step in a node's successive reported values. A lease/heartbeat decision uses
// the detector to deny leadership to a clock-faulty node (split-brain
// avoidance). Resetting a node's fault re-syncs it to the base.
//
// The package is pure Go, deterministic and self-contained: time is modeled as
// int64 seconds driven by explicit base ticks, never by the wall clock.
package timefault

import (
	"fmt"
	"sort"
)

// FaultKind enumerates the supported time faults.
type FaultKind int

const (
	// FaultNone means the node tracks the base clock exactly.
	FaultNone FaultKind = iota
	// FaultSkew offsets the node clock by a fixed delta.
	FaultSkew
	// FaultFreeze stops the node clock at the freeze instant.
	FaultFreeze
	// FaultMonotonicViolation makes the node jump backward once.
	FaultMonotonicViolation
)

func (k FaultKind) String() string {
	switch k {
	case FaultNone:
		return "none"
	case FaultSkew:
		return "skew"
	case FaultFreeze:
		return "freeze"
	case FaultMonotonicViolation:
		return "monotonic_violation"
	default:
		return "unknown"
	}
}

// Fault is the per-node clock transform applied to a base tick (seconds).
// Given the current base time, ReportedTime returns the time the node would
// report. Implementations must be deterministic and pure.
type Fault interface {
	Kind() FaultKind
	// ReportedTime maps a base time (seconds) to the node's reported time.
	ReportedTime(base int64) int64
}

// noFault tracks the base exactly.
type noFault struct{}

func (noFault) Kind() FaultKind            { return FaultNone }
func (noFault) ReportedTime(b int64) int64 { return b }

// SkewFault offsets the node clock by Delta seconds (negative = behind).
type SkewFault struct {
	Delta int64
}

// Kind reports the fault kind.
func (SkewFault) Kind() FaultKind { return FaultSkew }

// ReportedTime returns base+Delta.
func (f SkewFault) ReportedTime(base int64) int64 { return base + f.Delta }

// FreezeFault stops the node clock at At. For base < At the node still tracks
// the base (the freeze has not occurred yet); from At onward the node is stuck
// at At and falls progressively behind as the base advances.
type FreezeFault struct {
	At int64
}

// Kind reports the fault kind.
func (FreezeFault) Kind() FaultKind { return FaultFreeze }

// ReportedTime returns min(base, At): frozen at At once the base passes it.
func (f FreezeFault) ReportedTime(base int64) int64 {
	if base >= f.At {
		return f.At
	}
	return base
}

// MonotonicViolationFault makes the node jump backward by Jump seconds once the
// base reaches or passes At. Before At the node tracks the base; from At onward
// it reports base-Jump, so the reported value steps backward across the At
// boundary (a monotonicity violation) and then advances normally from there.
type MonotonicViolationFault struct {
	At   int64
	Jump int64 // positive magnitude of the backward jump
}

// Kind reports the fault kind.
func (MonotonicViolationFault) Kind() FaultKind { return FaultMonotonicViolation }

// ReportedTime returns base before At and base-Jump at/after At.
func (f MonotonicViolationFault) ReportedTime(base int64) int64 {
	if base >= f.At {
		return base - f.Jump
	}
	return base
}

// Node is a cluster member with a per-node fault transform and a record of the
// last reported time (used to detect backward steps / monotonicity violations).
type Node struct {
	ID    string
	fault Fault

	hasLast  bool
	lastBase int64
	last     int64
}

// NewNode creates a healthy node that tracks the base clock.
func NewNode(id string) *Node {
	return &Node{ID: id, fault: noFault{}}
}

// Inject installs a fault on the node and clears any monotonicity history so a
// freshly injected fault is evaluated from a clean state.
func (n *Node) Inject(f Fault) {
	if f == nil {
		f = noFault{}
	}
	n.fault = f
	n.hasLast = false
}

// Reset clears the node's fault (re-sync to base) and its history.
func (n *Node) Reset() {
	n.fault = noFault{}
	n.hasLast = false
}

// FaultKind returns the node's current fault kind.
func (n *Node) FaultKind() FaultKind { return n.fault.Kind() }

// Read returns the node's reported time at the given base tick and records it
// for monotonicity tracking. Reads must be issued with non-decreasing base
// values to model a forward-advancing base clock.
func (n *Node) Read(base int64) int64 {
	r := n.fault.ReportedTime(base)
	n.hasLast = true
	n.lastBase = base
	n.last = r
	return r
}

// Tolerance bounds the acceptable absolute skew (seconds) before a node is
// flagged INCONSISTENT.
type Tolerance int64

// Detector evaluates node reports against the base reference.
type Detector struct {
	Tol Tolerance
}

// NewDetector builds a Detector with the given tolerance in seconds.
func NewDetector(tolSeconds int64) *Detector {
	return &Detector{Tol: Tolerance(tolSeconds)}
}

// Verdict captures the detector's evaluation of one node at one base tick.
type Verdict struct {
	NodeID       string
	Base         int64
	Reported     int64
	Skew         int64 // reported - base
	Consistent   bool
	Monotonicity bool // true => a backward step was observed (violation)
	Kind         FaultKind
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// EvaluateAt reads the node at base and judges it against the base reference.
//
// A node is INCONSISTENT when its absolute skew exceeds the tolerance OR when a
// backward step (monotonicity violation) is observed relative to its previous
// read. The monotonicity check fires only on a strict backward step between two
// reads taken at non-decreasing base values, so it distinguishes a backward
// jump from a steady offset (skew).
func (d *Detector) EvaluateAt(n *Node, base int64) Verdict {
	prevHad := n.hasLast
	prev := n.last
	prevBase := n.lastBase

	reported := n.Read(base)
	skew := reported - base

	monoViolation := false
	if prevHad && base >= prevBase && reported < prev {
		// The base did not move backward, yet the node's reported time did:
		// a monotonicity violation.
		monoViolation = true
	}

	withinTol := abs64(skew) <= int64(d.Tol)
	consistent := withinTol && !monoViolation

	return Verdict{
		NodeID:       n.ID,
		Base:         base,
		Reported:     reported,
		Skew:         skew,
		Consistent:   consistent,
		Monotonicity: monoViolation,
		Kind:         n.FaultKind(),
	}
}

// LeaseDecision records whether a node may be granted leadership at a base tick.
type LeaseDecision struct {
	NodeID  string
	Granted bool
	Reason  string
}

// GrantLease evaluates the node and grants leadership only if the detector finds
// it consistent. A skewed, frozen-behind or monotonicity-violating node is
// denied to avoid split-brain.
func (d *Detector) GrantLease(n *Node, base int64) LeaseDecision {
	v := d.EvaluateAt(n, base)
	if v.Consistent {
		return LeaseDecision{NodeID: n.ID, Granted: true, Reason: "clock_consistent"}
	}
	reason := fmt.Sprintf("clock_inconsistent_skew=%ds_tol=%ds", v.Skew, int64(d.Tol))
	if v.Monotonicity {
		reason = fmt.Sprintf("clock_monotonicity_violation_reported=%d_prev_higher", v.Reported)
	}
	return LeaseDecision{NodeID: n.ID, Granted: false, Reason: reason}
}

// Cluster is an ordered collection of nodes sharing a base clock and detector.
type Cluster struct {
	nodes    map[string]*Node
	order    []string
	Detector *Detector
}

// NewCluster builds a cluster from node IDs with the given tolerance.
func NewCluster(tolSeconds int64, ids ...string) *Cluster {
	c := &Cluster{nodes: map[string]*Node{}, Detector: NewDetector(tolSeconds)}
	for _, id := range ids {
		c.nodes[id] = NewNode(id)
		c.order = append(c.order, id)
	}
	sort.Strings(c.order)
	return c
}

// Node returns the node by ID (nil if absent).
func (c *Cluster) Node(id string) *Node { return c.nodes[id] }

// IDs returns the sorted node IDs.
func (c *Cluster) IDs() []string {
	out := make([]string, len(c.order))
	copy(out, c.order)
	return out
}

// Inject installs a fault on the named node.
func (c *Cluster) Inject(id string, f Fault) error {
	n, ok := c.nodes[id]
	if !ok {
		return fmt.Errorf("timefault: unknown node %q", id)
	}
	n.Inject(f)
	return nil
}

// Reset clears the fault on the named node.
func (c *Cluster) Reset(id string) error {
	n, ok := c.nodes[id]
	if !ok {
		return fmt.Errorf("timefault: unknown node %q", id)
	}
	n.Reset()
	return nil
}

// EvaluateAll evaluates every node at the base tick, returning verdicts keyed by
// node ID in sorted order.
func (c *Cluster) EvaluateAll(base int64) []Verdict {
	out := make([]Verdict, 0, len(c.order))
	for _, id := range c.order {
		out = append(out, c.Detector.EvaluateAt(c.nodes[id], base))
	}
	return out
}

// Inconsistent returns the sorted IDs of nodes the detector flags inconsistent
// at the base tick.
func (c *Cluster) Inconsistent(base int64) []string {
	var out []string
	for _, v := range c.EvaluateAll(base) {
		if !v.Consistent {
			out = append(out, v.NodeID)
		}
	}
	sort.Strings(out)
	return out
}
