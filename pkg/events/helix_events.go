// Package events provides event handling for Helix Cluster OS.
//
// This file defines the domain event types, the 5 HELIX_* JetStream streams,
// the {domain}.{entity_id}.{action} subject convention, and the HelixPublisher
// / HelixSubscriber interfaces that the real NATS backend implements.
package events

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// HelixStream enumerates the five authoritative JetStream streams used by
// Helix Cluster OS. Each stream captures a broad family of events (identified
// by a subject wildcard) so a single JetStream stream carries all topics for
// that domain.
type HelixStream string

const (
	// StreamNodes captures node lifecycle events (heartbeats, joins, leaves,
	// failures). Subject wildcard: "nodes.>".
	StreamNodes HelixStream = "HELIX_NODES"

	// StreamSessions captures session lifecycle events (create, attach, detach,
	// terminate). Subject wildcard: "sessions.>".
	StreamSessions HelixStream = "HELIX_SESSIONS"

	// StreamScheduler captures scheduler events (assignment, preemption, queue
	// changes). Subject wildcard: "scheduler.>".
	StreamScheduler HelixStream = "HELIX_SCHEDULER"

	// StreamHealth captures health-check events (probe results, thresholds,
	// alerts). Subject wildcard: "health.>".
	StreamHealth HelixStream = "HELIX_HEALTH"

	// StreamAlerts captures alert events (fire, resolve, escalate). Subject
	// wildcard: "alerts.>".
	StreamAlerts HelixStream = "HELIX_ALERTS"
)

// helixStreamDomains maps each HELIX_* stream to its root NATS subject domain.
// The domain is the first token of the {domain}.{entity_id}.{action} triple.
var helixStreamDomains = map[HelixStream]string{
	StreamNodes:     "nodes",
	StreamSessions:  "sessions",
	StreamScheduler: "scheduler",
	StreamHealth:    "health",
	StreamAlerts:    "alerts",
}

// AllHelixStreams is the ordered list of all five HELIX_* streams. Callers
// that must iterate them (e.g. stream admin setup) use this slice so they
// cannot accidentally miss one.
var AllHelixStreams = []HelixStream{
	StreamNodes,
	StreamSessions,
	StreamScheduler,
	StreamHealth,
	StreamAlerts,
}

// HelixSubject builds the canonical {domain}.{entity_id}.{action} NATS
// subject. All three components are sanitised: empty tokens are replaced with
// "_", and NATS-illegal characters (space, "*", ">") are replaced with "_".
// This prevents broken routing while preserving the three-token structure.
//
// Examples:
//
//	HelixSubject("nodes", "n1", "heartbeat") → "nodes.n1.heartbeat"
//	HelixSubject("alerts", "zone-a", "fire")  → "alerts.zone-a.fire"
func HelixSubject(domain, entityID, action string) string {
	return sanitizeHelixToken(domain) + "." +
		sanitizeHelixToken(entityID) + "." +
		sanitizeHelixToken(action)
}

// sanitizeHelixToken replaces empty input and illegal NATS characters with "_".
func sanitizeHelixToken(tok string) string {
	if tok == "" {
		return "_"
	}
	var sb strings.Builder
	sb.Grow(len(tok))
	for _, r := range tok {
		switch r {
		case '*', '>', ' ', '\t', '\n', '\r':
			sb.WriteByte('_')
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// NodeEvent is a structured event emitted by a Helix cluster node. It is the
// canonical payload for messages published on the StreamNodes stream.
type NodeEvent struct {
	// RunID is a per-test / per-invocation UUID embedded by the publisher so
	// the subscriber can assert it received the IDENTICAL event (not a
	// different one from a previous run). Required per §7.1 anti-bluff.
	RunID string `json:"run_id"`

	// NodeID is the unique identifier of the reporting node.
	NodeID string `json:"node_id"`

	// Action describes what happened (e.g. "heartbeat", "join", "leave",
	// "failure"). It maps to the third token of the subject triple.
	Action string `json:"action"`

	// Timestamp is the wall-clock time at which the event was created.
	Timestamp time.Time `json:"timestamp"`

	// Metadata is a free-form key-value bag for additional context.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Subject returns the canonical NATS subject for this NodeEvent.
// It follows the {domain}.{entity_id}.{action} convention.
func (e *NodeEvent) Subject() string {
	return HelixSubject("nodes", e.NodeID, e.Action)
}

// MarshalPayload serialises the NodeEvent to JSON bytes suitable for
// embedding in an Event.Payload.
func (e *NodeEvent) MarshalPayload() ([]byte, error) {
	return json.Marshal(e)
}

// UnmarshalNodeEvent deserialises a NodeEvent from raw JSON bytes. It is the
// inverse of MarshalPayload and is the canonical way to decode Event.Payload
// on the subscriber side.
func UnmarshalNodeEvent(payload []byte) (*NodeEvent, error) {
	var ne NodeEvent
	if err := json.Unmarshal(payload, &ne); err != nil {
		return nil, fmt.Errorf("events: unmarshal NodeEvent: %w", err)
	}
	return &ne, nil
}

// HelixPublisher is the interface for publishing domain events onto the NATS
// JetStream-backed Helix event bus. The real implementation is HelixBackend;
// unit tests inject a fake that implements this interface.
type HelixPublisher interface {
	// PublishNodeEvent publishes a NodeEvent on the StreamNodes stream using
	// the subject derived from ne.Subject().
	PublishNodeEvent(ne *NodeEvent) error
}

// HelixSubscriber is the interface for subscribing to domain events from the
// NATS JetStream-backed Helix event bus.
type HelixSubscriber interface {
	// SubscribeNodeEvents subscribes to NodeEvents for a specific nodeID and
	// action pattern. Passing "*" for either field matches any value.
	SubscribeNodeEvents(nodeID, action string) (<-chan *NodeEvent, func(), error)
}

// HelixStreamInfo captures metadata about a JetStream stream, returned by
// HelixBackend.StreamInfo for observability and §7.1 evidence.
type HelixStreamInfo struct {
	// Name is the HELIX_* stream name.
	Name HelixStream
	// NumMessages is the count of messages currently stored in the stream.
	NumMessages uint64
	// NumConsumers is the count of active consumers on the stream.
	NumConsumers int
}
