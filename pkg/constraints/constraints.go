// Package constraints implements a Pacemaker-style placement constraint engine.
//
// It models four constraint types — Location, Colocation, Order, and
// Stickiness — and resolves them into a Placement (resource->node) plus a
// start Order. The engine is pure Go and fully deterministic: candidate nodes
// and resources are processed in caller-supplied order, and ties are broken by
// stable node ordering. No clock, network, or host access is used.
package constraints

import "errors"

// ErrUnsatisfiable is returned when a constraint set cannot be satisfied, e.g.
// every candidate node is hard-forbidden for some resource, or a positive
// colocation forces two resources onto a node that is forbidden for one of them.
var ErrUnsatisfiable = errors.New("constraints: unsatisfiable constraint set")

// ErrOrderCycle is returned when the Order constraints contain a dependency
// cycle and therefore no valid start sequence exists.
var ErrOrderCycle = errors.New("constraints: cyclic order constraints")

// Affinity expresses how strongly a Location constraint binds a resource to a
// node. Required and Forbidden are hard (score is ignored); Prefer is soft.
type Affinity int

const (
	// Prefer adds the constraint's Score to the node's tally (soft).
	Prefer Affinity = iota
	// Required hard-pins the resource to the node: it may run ONLY there.
	Required
	// Forbidden hard-bans the resource from the node: it may never run there.
	Forbidden
)

// Resource is a placeable unit. Stickiness is the score bonus added to the node
// the resource currently occupies (Current) during a re-score, modelling
// Pacemaker resource-stickiness.
type Resource struct {
	ID         string
	Current    string // node the resource is presently on ("" if unplaced)
	Stickiness int
}

// Node is a candidate placement target.
type Node struct {
	ID string
}

// Location constrains a single resource relative to a single node.
type Location struct {
	Resource string
	Node     string
	Affinity Affinity
	Score    int // applied only when Affinity == Prefer
}

// Colocation requires two resources to share a node (Together) or to be on
// distinct nodes (anti-affinity, !Together).
type Colocation struct {
	First    string
	Second   string
	Together bool
}

// Order requires Before to be started ahead of After in the returned sequence.
type Order struct {
	Before string
	After  string
}

// ConstraintSet bundles all constraints handed to the engine.
type ConstraintSet struct {
	Locations   []Location
	Colocations []Colocation
	Orders      []Order
}

// Placement is the resolved resource->node assignment.
type Placement map[string]string

// Result is the engine output: a Placement plus a valid start Order.
type Result struct {
	Placement Placement
	StartOrder []string
}
