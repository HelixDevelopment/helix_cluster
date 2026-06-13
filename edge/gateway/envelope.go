// Package gateway implements a transport-agnostic edge protocol gateway for
// the Helix cluster. It accepts edge work-dispatch and status/heartbeat
// traffic over multiple transports (WebSocket, in-process loopback, and any
// future MQTT/QUIC adapter) and routes messages between connected transports
// via one internal message model — the Envelope.
//
// The gateway never speaks a wire protocol directly. Each transport adapts its
// own framing to and from the common Envelope, and the gateway routes purely
// on Envelope fields (destination node and topic). This keeps the routing core
// independent of any single protocol so a WebSocket client can address a node
// that is reachable over MQTT, and vice versa, without the core knowing or
// caring which transport carries the bytes.
//
// HXC-1186. Self-contained module — it deliberately does NOT import
// edge/mqttclient or transport/quic; those are separate modules whose adapters
// plug in behind the Transport interface as honest future integration.
package gateway

import (
	"encoding/json"
	"fmt"
	"time"
)

// Kind enumerates the classes of edge traffic the gateway routes. The set is
// intentionally small: the gateway routes opaque payloads and only needs to
// know enough to apply policy (e.g. broadcast vs. unicast) and to let
// observers distinguish a work dispatch from a status report.
type Kind string

const (
	// KindDispatch is a unit of work dispatched TO an edge node. It is always
	// unicast: it must reach exactly the addressed node and no other.
	KindDispatch Kind = "dispatch"
	// KindStatus is a status/heartbeat report FROM an edge node, flowing back
	// toward whoever dispatched or is observing that node.
	KindStatus Kind = "status"
)

// Topic conventions mirror edge/mqttclient (HXC-1178) so a future MQTT adapter
// maps one-to-one onto the gateway's internal model without translation:
//
//	helix/edge/<node_id>/dispatch  — work items dispatched TO an edge node
//	helix/edge/<node_id>/status    — status/heartbeat published BY an edge node
const (
	topicRoot       = "helix/edge"
	dispatchSegment = "dispatch"
	statusSegment   = "status"
	topicSeparator  = "/"
)

// DispatchTopic returns the canonical dispatch topic for a node, e.g.
// helix/edge/node-7/dispatch.
func DispatchTopic(nodeID string) string {
	return topicRoot + topicSeparator + nodeID + topicSeparator + dispatchSegment
}

// StatusTopic returns the canonical status topic for a node, e.g.
// helix/edge/node-7/status.
func StatusTopic(nodeID string) string {
	return topicRoot + topicSeparator + nodeID + topicSeparator + statusSegment
}

// Envelope is the single internal message model every transport normalizes to.
// Routing is performed purely on Dest and Topic; Source is informational and
// used for status fan-back. Payload is opaque application bytes that the
// gateway copies verbatim — it is never interpreted or mutated in transit, so
// a receiver gets byte-identical content to what the sender emitted.
type Envelope struct {
	// ID uniquely identifies the message (idempotency / tracing key).
	ID string `json:"id"`
	// Kind classifies the traffic (dispatch vs. status).
	Kind Kind `json:"kind"`
	// Source is the node id (or logical sender) that originated the message.
	Source string `json:"source,omitempty"`
	// Dest is the node id the message is addressed to. Empty Dest on a status
	// message means "fan back to all observers"; empty Dest on a dispatch is
	// rejected by the codec because an unaddressed work item cannot be routed.
	Dest string `json:"dest,omitempty"`
	// Topic is the canonical topic string. When set it is authoritative for
	// routing; when empty it is derived from Dest+Kind. Carrying it explicitly
	// lets topic-oriented transports (MQTT) round-trip without information loss.
	Topic string `json:"topic,omitempty"`
	// Payload is the opaque application payload, copied byte-for-byte.
	Payload json.RawMessage `json:"payload,omitempty"`
	// IssuedAt is when the envelope was created.
	IssuedAt time.Time `json:"issued_at"`
}

// RouteTopic returns the topic the gateway routes this envelope on. An explicit
// Topic always wins; otherwise it is derived from Dest and Kind so callers may
// address purely by node id without constructing topic strings themselves.
func (e Envelope) RouteTopic() string {
	if e.Topic != "" {
		return e.Topic
	}
	switch e.Kind {
	case KindStatus:
		return StatusTopic(e.Dest)
	default:
		return DispatchTopic(e.Dest)
	}
}

// Validate reports whether the envelope is well-formed enough to be routed. A
// malformed envelope is rejected at the codec boundary rather than being
// silently dropped or misrouted later.
func (e Envelope) Validate() error {
	if e.ID == "" {
		return fmt.Errorf("gateway: envelope missing id")
	}
	if e.Kind != KindDispatch && e.Kind != KindStatus {
		return fmt.Errorf("gateway: envelope %q has invalid kind %q", e.ID, e.Kind)
	}
	if e.Kind == KindDispatch && e.Dest == "" && e.Topic == "" {
		return fmt.Errorf("gateway: dispatch envelope %q has no destination", e.ID)
	}
	return nil
}

// EncodeEnvelope serializes an Envelope to wire bytes (JSON), validating it
// first so a malformed envelope is never put on a transport.
func EncodeEnvelope(e Envelope) ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(e)
}

// DecodeEnvelope parses wire bytes into an Envelope and validates it, so a
// decode that yields an unroutable envelope is reported as an error rather than
// returning a zero value that would later misroute.
func DecodeEnvelope(b []byte) (Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(b, &e); err != nil {
		return Envelope{}, fmt.Errorf("gateway: decode envelope: %w", err)
	}
	if err := e.Validate(); err != nil {
		return Envelope{}, err
	}
	return e, nil
}
