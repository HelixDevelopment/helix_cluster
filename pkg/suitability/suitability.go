// Package suitability provides a pure-Go, deterministic workload-suitability
// classifier that routes HPC-style workloads versus inference-style workloads.
//
// The core insight: an HPC workload (tightly-coupled MPI, latency-sensitive
// interconnect) MUST be pinned to a local node with a low-latency interconnect
// (e.g. InfiniBand). A stateless inference workload (token generation) MAY be
// routed to a remote provider. An HPC workload must NEVER silently fall back to
// a high-latency remote proxy; if no suitable local interconnect-capable node
// exists, placement fails with a typed error.
//
// This package is self-contained: it defines its own minimal types and does not
// depend on the cluster scheduler/cost/latency packages.
package suitability

import (
	"errors"
	"fmt"
)

// WorkloadKind enumerates the classification outcome for a workload.
type WorkloadKind int

const (
	// KindInference is a stateless, loosely-coupled workload (e.g. token
	// generation) that tolerates remote placement.
	KindInference WorkloadKind = iota
	// KindHPC is a tightly-coupled, interconnect-sensitive workload that must
	// run on a local low-latency interconnect.
	KindHPC
)

// String renders a WorkloadKind for logging.
func (k WorkloadKind) String() string {
	switch k {
	case KindHPC:
		return "HPC"
	case KindInference:
		return "Inference"
	default:
		return fmt.Sprintf("WorkloadKind(%d)", int(k))
	}
}

// Locality describes where a candidate placement physically lives.
type Locality int

const (
	// Local is a node inside the cluster's low-latency fabric.
	Local Locality = iota
	// Remote is an off-cluster provider reached over a higher-latency link.
	Remote
)

// String renders a Locality for logging.
func (l Locality) String() string {
	switch l {
	case Local:
		return "Local"
	case Remote:
		return "Remote"
	default:
		return fmt.Sprintf("Locality(%d)", int(l))
	}
}

// Interconnect describes the fabric class available at a candidate.
type Interconnect int

const (
	// InterconnectEthernet is a commodity, higher-latency fabric.
	InterconnectEthernet Interconnect = iota
	// InterconnectInfiniBand is a low-latency RDMA fabric suitable for
	// tightly-coupled MPI.
	InterconnectInfiniBand
)

// String renders an Interconnect for logging.
func (i Interconnect) String() string {
	switch i {
	case InterconnectInfiniBand:
		return "InfiniBand"
	case InterconnectEthernet:
		return "Ethernet"
	default:
		return fmt.Sprintf("Interconnect(%d)", int(i))
	}
}

// LowLatencyInterconnect reports whether this fabric class is fast enough to
// host a tightly-coupled HPC workload. Only InfiniBand qualifies.
func (i Interconnect) LowLatencyInterconnect() bool {
	return i == InterconnectInfiniBand
}

// Workload carries the characteristics used to classify a job.
type Workload struct {
	// Name identifies the workload (logging/diagnostics only).
	Name string
	// TightlyCoupled is true when ranks must communicate synchronously
	// (e.g. MPI collectives), implying HPC.
	TightlyCoupled bool
	// InterconnectSensitive is true when throughput/latency of the fabric
	// dominates the job's performance, implying HPC.
	InterconnectSensitive bool
	// Stateless is true for request/response token-generation style jobs that
	// carry no cross-node coupling, implying inference.
	Stateless bool
}

// Candidate is a possible placement target.
type Candidate struct {
	// NodeID identifies the target (logging/diagnostics only).
	NodeID string
	// Where the candidate lives relative to the cluster fabric.
	Locality Locality
	// Interconnect is the fabric class available at this candidate.
	Interconnect Interconnect
}

// ErrNoSuitablePlacement is returned when no candidate satisfies the routing
// constraints for the workload's kind. For an HPC workload this specifically
// means there was no local, low-latency-interconnect node available; the router
// does NOT silently fall back to a remote proxy.
var ErrNoSuitablePlacement = errors.New("suitability: no suitable placement for workload")

// Classify maps a workload's characteristics to a WorkloadKind.
//
// A workload is HPC if it is tightly-coupled OR explicitly interconnect-
// sensitive: either condition means it needs a low-latency local fabric.
// Otherwise it is treated as inference (the remote-tolerant default).
//
// Note: Stateless alone does not force Inference — a stateless-but-tightly-
// coupled job is still HPC. The coupling/interconnect signals dominate because
// they are what make remote placement unsafe.
func Classify(w Workload) WorkloadKind {
	if w.TightlyCoupled || w.InterconnectSensitive {
		return KindHPC
	}
	return KindInference
}

// eligible reports whether a candidate may host a workload of the given kind.
//
//   - HPC: only a Local candidate on a low-latency interconnect is eligible.
//     A remote candidate (any fabric) and a local Ethernet candidate are both
//     ineligible.
//   - Inference: any candidate is eligible (local or remote, any fabric).
func eligible(kind WorkloadKind, c Candidate) bool {
	switch kind {
	case KindHPC:
		return c.Locality == Local && c.Interconnect.LowLatencyInterconnect()
	case KindInference:
		return true
	default:
		return false
	}
}

// Route selects an eligible placement for a workload of the given kind from the
// candidate list, returning the first eligible candidate in slice order.
//
// For KindHPC, only a local low-latency-interconnect node is eligible; if none
// exists, Route returns ErrNoSuitablePlacement rather than falling back to a
// remote proxy. For KindInference, any candidate (local or remote) is eligible.
func Route(kind WorkloadKind, candidates []Candidate) (Candidate, error) {
	for _, c := range candidates {
		if eligible(kind, c) {
			return c, nil
		}
	}
	return Candidate{}, fmt.Errorf("%w: kind=%s candidates=%d", ErrNoSuitablePlacement, kind, len(candidates))
}

// ClassifyAndRoute is a convenience that classifies then routes in one call.
func ClassifyAndRoute(w Workload, candidates []Candidate) (WorkloadKind, Candidate, error) {
	kind := Classify(w)
	c, err := Route(kind, candidates)
	return kind, c, err
}
