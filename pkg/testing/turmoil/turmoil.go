// Package turmoil provides a deterministic multi-cluster network simulation
// built on top of the pkg/testing/dst virtual clock and seeded RNG. The same
// seed and operation sequence always produce a byte-identical event log —
// the core reproducibility contract.
//
// # Determinism model
//
// Every [Simulation.Send] call draws one value from the seeded [dst.Engine]
// PRNG to produce a per-message Nonce. This serves two purposes:
//
//  1. It advances the PRNG state so that two simulations with different seeds
//     genuinely diverge — the Nonce values differ, and [EncodeLog] encodes them,
//     making canonical log comparisons sensitive to the seed.
//
//  2. It reserves the seeded-RNG hook for future RNG-driven behaviour (jitter,
//     tie-breaking, fault injection) without breaking the determinism contract.
//
// # Architecture
//
// A [Simulation] owns a [dst.Engine] which provides the virtual clock, the
// priority event queue, and the seeded PRNG. Each cluster is a named group of
// node IDs. Messages are enqueued as DST events and delivered in
// deterministic virtual-time order.
//
// Network partitions and latency injections are stored as simple tables inside
// the Simulation; no wall-clock timer or OS thread is involved.
package turmoil

import (
	"bytes"
	"errors"
	"fmt"
	"sort"

	"github.com/HelixDevelopment/helix_cluster/pkg/testing/dst"
)

// ── errors ────────────────────────────────────────────────────────────────────

// ErrUnknownCluster is returned when an operation names a cluster that has not
// been registered with [Simulation.AddCluster].
var ErrUnknownCluster = errors.New("unknown cluster")

// ErrUnknownNode is returned when fromNode or toNode is not a member of the
// named cluster.
var ErrUnknownNode = errors.New("unknown node")

// ErrDuplicateCluster is returned when [Simulation.AddCluster] is called with a
// clusterID that is already registered.
var ErrDuplicateCluster = errors.New("duplicate cluster")

// ── delivery event ────────────────────────────────────────────────────────────

// DeliveryEvent is the sink-side record appended to the event log for every
// message that was either delivered or dropped.
type DeliveryEvent struct {
	// Tick is the virtual clock value at which the message was processed.
	Tick int64
	// FromCluster / FromNode identify the sender.
	FromCluster string
	FromNode    string
	// ToCluster / ToNode identify the intended recipient.
	ToCluster string
	ToNode    string
	// Payload is the message body (a copy).
	Payload []byte
	// Dropped is true when the message was not delivered to the recipient.
	Dropped bool
	// Reason describes why the message was dropped; empty when Dropped is false.
	Reason string
	// Nonce is a seed-derived per-message value drawn from the engine PRNG at
	// Send time. Two simulations initialised with the same seed produce equal
	// Nonce sequences; different seeds produce different Nonce sequences. The
	// Nonce is included in [EncodeLog] output so that canonical-log comparisons
	// are sensitive to the seed.
	Nonce int64
}

// ── internal message payload stored in DST events ─────────────────────────────

// msgPayload is the value stored in a dst.Event when Kind == eventKindMsg.
type msgPayload struct {
	fromCluster string
	fromNode    string
	toCluster   string
	toNode      string
	payload     []byte
	nonce       int64 // seed-derived per-message value; see DeliveryEvent.Nonce
}

const eventKindMsg = "turmoil.msg"

// ── partition key ─────────────────────────────────────────────────────────────

// partitionKey is the canonical (sorted) pair used for partition and latency
// tables. Storing the pair sorted ensures that Partition("A","B") and
// Partition("B","A") refer to the same entry (symmetric partition).
type partitionKey struct{ a, b string }

func newPartitionKey(x, y string) partitionKey {
	if x > y {
		x, y = y, x
	}
	return partitionKey{x, y}
}

// latencyKey is directional (c1→c2 latency need not equal c2→c1).
type latencyKey struct{ from, to string }

// ── Simulation ────────────────────────────────────────────────────────────────

// Simulation is a deterministic multi-cluster network simulation. All
// randomness is derived from the seeded [dst.Engine] RNG so that identical
// (seed, operation sequence) pairs always produce an identical [DeliveryEvent]
// log. No OS time, no goroutines, no external I/O.
type Simulation struct {
	eng        *dst.Engine
	clusters   map[string]map[string]struct{} // clusterID → set of nodeIDs
	partitions map[partitionKey]struct{}       // symmetric blocked pairs
	latencies  map[latencyKey]int64            // directional extra ticks
	log        []DeliveryEvent
}

// NewSimulation creates a fresh Simulation seeded with seed. Two independent
// NewSimulation(s) invocations with the same seed and identical subsequent
// operations will always produce identical [DeliveryEvent] logs. Simulations
// with different seeds produce different [DeliveryEvent.Nonce] sequences and
// therefore diverge under [EncodeLog].
func NewSimulation(seed int64) *Simulation {
	s := &Simulation{
		eng:        dst.NewEngine(seed),
		clusters:   make(map[string]map[string]struct{}),
		partitions: make(map[partitionKey]struct{}),
		latencies:  make(map[latencyKey]int64),
	}
	s.eng.RegisterHandler(eventKindMsg, s.handleDelivery)
	return s
}

// AddCluster registers a cluster with the given nodeIDs. Returns
// [ErrDuplicateCluster] if the cluster already exists.
func (s *Simulation) AddCluster(clusterID string, nodeIDs []string) error {
	if _, exists := s.clusters[clusterID]; exists {
		return fmt.Errorf("AddCluster %q: %w", clusterID, ErrDuplicateCluster)
	}
	members := make(map[string]struct{}, len(nodeIDs))
	for _, id := range nodeIDs {
		members[id] = struct{}{}
		// Also register in the DST engine so the engine's node registry is
		// consistent; address is a synthetic "<cluster>/<node>" label.
		s.eng.RegisterNode(clusterID+"/"+id, clusterID+"/"+id)
	}
	s.clusters[clusterID] = members
	return nil
}

// Send enqueues a message from (fromCluster, fromNode) to (toCluster, toNode).
// delayTicks is the base delivery delay in virtual ticks before latency
// injection is applied. Returns [ErrUnknownCluster] or [ErrUnknownNode] for
// unknown endpoints.
//
// Each Send draws one value from the engine PRNG to produce [DeliveryEvent.Nonce].
// This ensures that the PRNG state advances with each send, making the event log
// genuinely sensitive to the seed used in [NewSimulation].
func (s *Simulation) Send(fromCluster, fromNode, toCluster, toNode string, payload []byte, delayTicks int64) error {
	if err := s.validateEndpoint(fromCluster, fromNode); err != nil {
		return fmt.Errorf("Send from: %w", err)
	}
	if err := s.validateEndpoint(toCluster, toNode); err != nil {
		return fmt.Errorf("Send to: %w", err)
	}
	// Total delay = base + any configured extra latency for this direction.
	total := delayTicks + s.latencies[latencyKey{fromCluster, toCluster}]

	// Draw one PRNG value to advance the seeded RNG state. This nonce is
	// stored in the event payload and surfaced in DeliveryEvent.Nonce and in
	// EncodeLog output, making the canonical log seed-sensitive.
	nonce := s.eng.RNG().Int63()

	p := &msgPayload{
		fromCluster: fromCluster,
		fromNode:    fromNode,
		toCluster:   toCluster,
		toNode:      toNode,
		payload:     copyBytes(payload),
		nonce:       nonce,
	}
	s.eng.ScheduleEvent(eventKindMsg, p, total)
	return nil
}

// Partition blocks all message delivery between clusterA and clusterB
// (symmetric). Messages sent across the partition are recorded as dropped.
func (s *Simulation) Partition(clusterA, clusterB string) {
	s.partitions[newPartitionKey(clusterA, clusterB)] = struct{}{}
}

// Heal removes a previously-established partition between clusterA and
// clusterB. If no such partition exists, Heal is a no-op.
func (s *Simulation) Heal(clusterA, clusterB string) {
	delete(s.partitions, newPartitionKey(clusterA, clusterB))
}

// InjectLatency adds extraTicks to every message delivered from fromCluster to
// toCluster. The injection is directional: fromCluster→toCluster only. Calling
// InjectLatency again for the same pair replaces the previous value.
func (s *Simulation) InjectLatency(fromCluster, toCluster string, extraTicks int64) {
	s.latencies[latencyKey{fromCluster, toCluster}] = extraTicks
}

// Run processes up to maxSteps DST events, delivering (or dropping) messages in
// deterministic virtual-time order. Returns the number of events processed.
func (s *Simulation) Run(maxSteps int) int {
	return s.eng.Run(maxSteps)
}

// EventLog returns the complete, ordered list of delivery events recorded so
// far. The slice is a copy and safe to inspect or sort by the caller.
func (s *Simulation) EventLog() []DeliveryEvent {
	out := make([]DeliveryEvent, len(s.log))
	copy(out, s.log)
	return out
}

// ── canonical encoding ────────────────────────────────────────────────────────

// EncodeLog writes a canonical, byte-stable, seed-sensitive representation of
// log and returns it as a string. Two independent [Simulation] instances with
// the same seed and identical operation sequences always produce the same
// EncodeLog output. Instances with different seeds produce different output
// because [DeliveryEvent.Nonce] values differ.
//
// The encoding sorts events by (Tick, FromCluster, FromNode, ToCluster, ToNode)
// before serialising, making it stable even if the caller reorders the slice.
func EncodeLog(log []DeliveryEvent) string {
	var buf bytes.Buffer
	encodeLog(log, &buf)
	return buf.String()
}

// encodeLog writes a canonical, byte-stable representation of log to buf so
// two independent simulations can be compared for exact equality without
// reflection.
func encodeLog(log []DeliveryEvent, buf *bytes.Buffer) {
	// Ensure deterministic order before encoding.
	sorted := make([]DeliveryEvent, len(log))
	copy(sorted, log)
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Tick != b.Tick {
			return a.Tick < b.Tick
		}
		if a.FromCluster != b.FromCluster {
			return a.FromCluster < b.FromCluster
		}
		if a.FromNode != b.FromNode {
			return a.FromNode < b.FromNode
		}
		if a.ToCluster != b.ToCluster {
			return a.ToCluster < b.ToCluster
		}
		return a.ToNode < b.ToNode
	})
	for _, de := range sorted {
		dropped := byte(0)
		if de.Dropped {
			dropped = 1
		}
		fmt.Fprintf(buf, "%d|%s|%s|%s|%s|%x|%d|%s|%d\n",
			de.Tick,
			de.FromCluster, de.FromNode,
			de.ToCluster, de.ToNode,
			de.Payload,
			dropped, de.Reason,
			de.Nonce,
		)
	}
}

// ── internal helpers ──────────────────────────────────────────────────────────

// handleDelivery is the DST handler invoked for every turmoil.msg event.
// It records a [DeliveryEvent] in the log (dropped or delivered).
func (s *Simulation) handleDelivery(e *dst.Event) {
	mp, ok := e.Payload.(*msgPayload)
	if !ok {
		return
	}
	tick := e.Timestamp
	de := DeliveryEvent{
		Tick:        tick,
		FromCluster: mp.fromCluster,
		FromNode:    mp.fromNode,
		ToCluster:   mp.toCluster,
		ToNode:      mp.toNode,
		Payload:     copyBytes(mp.payload),
		Nonce:       mp.nonce,
	}

	// Check partition.
	if _, partitioned := s.partitions[newPartitionKey(mp.fromCluster, mp.toCluster)]; partitioned {
		// Only drop cross-cluster messages; intra-cluster with the same
		// clusterID on both sides is never checked here because
		// newPartitionKey("X","X") would only match a self-partition (never
		// set by Partition in normal usage), but the cross-cluster guard
		// uses != below as the authoritative check.
		if mp.fromCluster != mp.toCluster {
			de.Dropped = true
			de.Reason = "partition"
			s.log = append(s.log, de)
			return
		}
	}

	de.Dropped = false
	s.log = append(s.log, de)
}

// validateEndpoint returns an error if clusterID or nodeID are not registered.
func (s *Simulation) validateEndpoint(clusterID, nodeID string) error {
	members, ok := s.clusters[clusterID]
	if !ok {
		return fmt.Errorf("cluster %q: %w", clusterID, ErrUnknownCluster)
	}
	if _, ok := members[nodeID]; !ok {
		return fmt.Errorf("node %q in cluster %q: %w", nodeID, clusterID, ErrUnknownNode)
	}
	return nil
}

// copyBytes returns a copy of b; nil if b is nil.
func copyBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	c := make([]byte, len(b))
	copy(c, b)
	return c
}
