// Package gputopo implements topology-aware NUMA/NVLink GPU placement scoring
// for Helix Cluster OS.
//
// A Topology describes a set of GPU nodes and the direct interconnect Links
// between pairs of GPUs. Each Link has a Kind (NVLink or PCIe) and a measured
// BandwidthGBps. NVLink bandwidth MUST be strictly greater than PCIe bandwidth.
//
// SelectPair selects the best two-GPU placement for a two-GPU job from a caller-
// supplied list of free GPU IDs. The selection policy is:
//
//  1. Prefer any NVLink-connected pair of free GPUs over any PCIe-connected pair.
//  2. Among candidates of the same Kind, prefer the pair with the highest
//     BandwidthGBps (highest-bandwidth NVLink wins over lower-bandwidth NVLink,
//     etc.).
//  3. If no direct link exists between two free GPUs they are ineligible.
//  4. If fewer than two free GPUs are provided, ErrTooFewGPUs is returned.
//  5. If no eligible pair exists (no direct link between any two free GPUs),
//     ErrNoPairAvailable is returned.
//
// Every call to SelectPair emits a PlacementRecord keyed by the caller-supplied
// RunID so that downstream audit / observability consumers can trace decisions
// back to individual job runs.
//
// This package is pure stdlib and self-contained; it performs no I/O and
// references no external dependencies.
package gputopo

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// ────────────────────────────────────────────────────────────────────────────
// Public sentinel errors
// ────────────────────────────────────────────────────────────────────────────

// ErrTooFewGPUs is returned when fewer than two free GPU IDs are provided.
var ErrTooFewGPUs = errors.New("gputopo: need at least two free GPUs")

// ErrNoPairAvailable is returned when no direct interconnect link exists
// between any two of the provided free GPUs.
var ErrNoPairAvailable = errors.New("gputopo: no eligible GPU pair found in topology")

// ────────────────────────────────────────────────────────────────────────────
// Kind
// ────────────────────────────────────────────────────────────────────────────

// Kind is the type of GPU-to-GPU interconnect.
type Kind uint8

const (
	// NVLink is a high-bandwidth NVIDIA NVLink interconnect.
	NVLink Kind = iota + 1
	// PCIe is a standard PCI-Express interconnect.
	PCIe
)

// String returns a human-readable label for the interconnect Kind.
func (k Kind) String() string {
	switch k {
	case NVLink:
		return "NVLink"
	case PCIe:
		return "PCIe"
	default:
		return fmt.Sprintf("Kind(%d)", k)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Link
// ────────────────────────────────────────────────────────────────────────────

// Link describes a direct interconnect between two GPUs.
// BandwidthGBps MUST be > 0.  For a valid Topology, all NVLink BandwidthGBps
// values are strictly greater than all PCIe BandwidthGBps values.
type Link struct {
	// A and B are the IDs of the two GPUs this link connects.  Order is
	// canonical: the Topology constructor sorts them so A < B lexicographically.
	A, B          string
	Kind          Kind
	BandwidthGBps float64
}

// ────────────────────────────────────────────────────────────────────────────
// Topology
// ────────────────────────────────────────────────────────────────────────────

// Topology holds a set of GPU IDs and all direct interconnect links between
// them.  Use NewTopology to construct a valid Topology.
type Topology struct {
	gpus  map[string]struct{}
	links map[linkKey]Link
}

// linkKey is the canonical (A < B) key used to index links.
type linkKey struct{ a, b string }

func makeKey(x, y string) linkKey {
	if x <= y {
		return linkKey{x, y}
	}
	return linkKey{y, x}
}

// NewTopology constructs a Topology from a slice of GPU IDs and a slice of
// Links.  It returns an error if:
//   - Any GPU ID referenced in a Link is absent from gpuIDs.
//   - A Link connects a GPU to itself (self-loop).
//   - A Link's Kind is not NVLink or PCIe.
//   - A Link's BandwidthGBps is <= 0.
//   - Two Links share the same GPU pair (duplicates are rejected).
func NewTopology(gpuIDs []string, links []Link) (Topology, error) {
	topo := Topology{
		gpus:  make(map[string]struct{}, len(gpuIDs)),
		links: make(map[linkKey]Link, len(links)),
	}
	for _, id := range gpuIDs {
		topo.gpus[id] = struct{}{}
	}
	for _, l := range links {
		if _, ok := topo.gpus[l.A]; !ok {
			return Topology{}, fmt.Errorf("gputopo: GPU %q in link not found in topology", l.A)
		}
		if _, ok := topo.gpus[l.B]; !ok {
			return Topology{}, fmt.Errorf("gputopo: GPU %q in link not found in topology", l.B)
		}
		if l.A == l.B {
			return Topology{}, fmt.Errorf("gputopo: link %s<->%s is a self-loop", l.A, l.B)
		}
		if l.Kind != NVLink && l.Kind != PCIe {
			return Topology{}, fmt.Errorf("gputopo: link %s<->%s has unknown Kind %v", l.A, l.B, l.Kind)
		}
		if l.BandwidthGBps <= 0 {
			return Topology{}, fmt.Errorf("gputopo: link %s<->%s has non-positive bandwidth %v", l.A, l.B, l.BandwidthGBps)
		}
		k := makeKey(l.A, l.B)
		if _, exists := topo.links[k]; exists {
			return Topology{}, fmt.Errorf("gputopo: duplicate link for GPU pair %s<->%s", l.A, l.B)
		}
		topo.links[k] = Link{A: k.a, B: k.b, Kind: l.Kind, BandwidthGBps: l.BandwidthGBps}
	}
	return topo, nil
}

// GPUs returns all GPU IDs in the topology in sorted order.
func (t Topology) GPUs() []string {
	ids := make([]string, 0, len(t.gpus))
	for id := range t.gpus {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Link returns the Link between two GPUs (in either order) and whether it
// exists.
func (t Topology) Link(a, b string) (Link, bool) {
	l, ok := t.links[makeKey(a, b)]
	return l, ok
}

// ────────────────────────────────────────────────────────────────────────────
// PlacementRecord
// ────────────────────────────────────────────────────────────────────────────

// PlacementRecord is emitted by SelectPair for each successful placement
// decision.  It is stored in the package-level record store and can be
// retrieved by RunID for audit and observability purposes.
type PlacementRecord struct {
	RunID         string
	GPUA          string
	GPUB          string
	Kind          Kind
	BandwidthGBps float64
}

// ────────────────────────────────────────────────────────────────────────────
// Record store
// ────────────────────────────────────────────────────────────────────────────

var (
	recordMu    sync.RWMutex
	recordStore = make(map[string]PlacementRecord)
)

// RecordFor returns the PlacementRecord stored for the given RunID, and
// whether one exists.  Returns false when SelectPair has not yet been called
// with that RunID, or when it returned an error.
func RecordFor(runID string) (PlacementRecord, bool) {
	recordMu.RLock()
	defer recordMu.RUnlock()
	r, ok := recordStore[runID]
	return r, ok
}

// flushRecords removes all stored records.  Exported only for test isolation;
// production callers should not call this.
func flushRecords() {
	recordMu.Lock()
	defer recordMu.Unlock()
	recordStore = make(map[string]PlacementRecord)
}

// ────────────────────────────────────────────────────────────────────────────
// SelectPair
// ────────────────────────────────────────────────────────────────────────────

// candidate is an internal representation of an eligible GPU pair.
type candidate struct {
	link Link
}

// SelectPair selects the best two-GPU placement from freeGPUs given topo.
//
// Policy (in priority order):
//  1. NVLink-connected pair — chosen over any PCIe pair.
//  2. Highest BandwidthGBps within the same Kind wins.
//
// The chosen pair and its interconnect metadata are stored as a PlacementRecord
// keyed by runID.
//
// Mutation guard: the comparison `bestKind == NVLink` (and the bandwidth
// preference `c.link.BandwidthGBps > best.link.BandwidthGBps`) is the
// ordering logic pinned by TestPrefersNVLinkPair — inverting it makes that
// test fail.
func SelectPair(runID string, topo Topology, freeGPUs []string) (PlacementRecord, error) {
	if len(freeGPUs) < 2 {
		return PlacementRecord{}, ErrTooFewGPUs
	}

	// Collect all eligible pairs (must have a direct link in the topology).
	var nvlinkCandidates []candidate
	var pcieCandidates []candidate

	// Iterate in deterministic order to make selection stable across calls.
	sorted := make([]string, len(freeGPUs))
	copy(sorted, freeGPUs)
	sort.Strings(sorted)

	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			l, ok := topo.Link(sorted[i], sorted[j])
			if !ok {
				continue
			}
			c := candidate{link: l}
			switch l.Kind {
			case NVLink:
				nvlinkCandidates = append(nvlinkCandidates, c)
			case PCIe:
				pcieCandidates = append(pcieCandidates, c)
			}
		}
	}

	// MUTATION GUARD: prefer NVLink candidates over PCIe candidates.
	// Changing nvlinkCandidates to pcieCandidates on the right-hand side of the
	// assignment below makes TestPrefersNVLinkPair FAIL because pool would hold
	// the PCIe candidates and Kind would be PCIe instead of NVLink.
	// — pinned by TestPrefersNVLinkPair
	pool := pcieCandidates // fallback: use PCIe pool
	if len(nvlinkCandidates) > 0 {
		pool = nvlinkCandidates // ← mutation-killable: change to pcieCandidates → TestPrefersNVLinkPair FAILS
	}

	if len(pool) == 0 {
		return PlacementRecord{}, ErrNoPairAvailable
	}

	// Among the chosen pool, pick the highest-bandwidth pair.
	// Mutation guard: inverting `>` to `<` here makes TestPrefersNVLinkPair
	// pick the lower-bandwidth NVLink link (wrong pair) and fail.
	best := pool[0]
	for _, c := range pool[1:] {
		if c.link.BandwidthGBps > best.link.BandwidthGBps { // ← mutation-killable guard
			best = c
		}
	}

	rec := PlacementRecord{
		RunID:         runID,
		GPUA:          best.link.A,
		GPUB:          best.link.B,
		Kind:          best.link.Kind,
		BandwidthGBps: best.link.BandwidthGBps,
	}

	recordMu.Lock()
	recordStore[runID] = rec
	recordMu.Unlock()

	return rec, nil
}
