// Package nodeselector provides a deterministic, self-contained placement
// constraint schema and matcher for selecting cluster nodes.
//
// A Node carries hardware attributes (GPU count, VRAM, model) plus arbitrary
// string labels. A NodeSelector expresses constraints: a minimum GPU count, a
// minimum VRAM, a set of excluded models, and required label key=value matches.
// Filter returns only the nodes that satisfy ALL constraints.
//
// This package is pure Go and has no external dependencies. It performs no I/O,
// no time access, and no host/network calls. All behavior is a deterministic
// function of its inputs.
package nodeselector

// Node describes a candidate node and its placement-relevant attributes.
type Node struct {
	// ID is an opaque identifier for the node (used only for reporting/equality).
	ID string
	// GPUCount is the number of GPUs available on the node.
	GPUCount int
	// VRAMGiB is the per-node VRAM capacity in GiB.
	VRAMGiB int
	// Model is the GPU/accelerator model name (e.g. "A100", "H100").
	Model string
	// Labels is an arbitrary set of string key=value attributes.
	Labels map[string]string
}

// NodeSelector expresses placement constraints. A node satisfies the selector
// only when it satisfies EVERY constraint expressed below. A zero-value
// selector (no minimums, no exclusions, no required labels) matches every node.
type NodeSelector struct {
	// MinGPUCount is the minimum required GPU count. A node passes when
	// node.GPUCount >= MinGPUCount. Zero imposes no lower bound.
	MinGPUCount int
	// MinVRAMGiB is the minimum required per-node VRAM in GiB. A node passes
	// when node.VRAMGiB >= MinVRAMGiB. Zero imposes no lower bound.
	MinVRAMGiB int
	// ExcludeModels lists GPU/accelerator models that must NOT be selected. A
	// node whose Model appears in this set is rejected.
	ExcludeModels []string
	// RequireLabels lists label key=value pairs that must all be present and
	// equal on the node. A node missing any required key, or carrying a
	// different value for it, is rejected.
	RequireLabels map[string]string
}

// Matches reports whether a single node satisfies every constraint in sel.
//
// The constraints are evaluated as a conjunction (logical AND): the node must
// pass the GPU-count minimum, the VRAM minimum, the exclude-models check, and
// every required label. Failing any one constraint makes the node not match.
func Matches(sel NodeSelector, node Node) bool {
	// Constraint 1: minimum GPU count.
	if node.GPUCount < sel.MinGPUCount {
		return false
	}

	// Constraint 2: minimum VRAM.
	if node.VRAMGiB < sel.MinVRAMGiB {
		return false
	}

	// Constraint 3: excluded models.
	for _, excluded := range sel.ExcludeModels {
		if node.Model == excluded {
			return false
		}
	}

	// Constraint 4: required labels (key must exist AND value must match).
	for key, want := range sel.RequireLabels {
		got, ok := node.Labels[key]
		if !ok || got != want {
			return false
		}
	}

	return true
}

// Filter returns, in input order, only those nodes that satisfy every
// constraint in sel. The returned slice is always non-nil and is a freshly
// allocated slice that does not alias the input.
func Filter(sel NodeSelector, nodes []Node) []Node {
	out := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		if Matches(sel, n) {
			out = append(out, n)
		}
	}
	return out
}
