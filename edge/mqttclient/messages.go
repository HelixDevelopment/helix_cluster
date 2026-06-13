package mqttclient

import (
	"encoding/json"
	"fmt"
	"time"
)

// WorkItem is a unit of work dispatched to an edge node on its dispatch
// topic. It is the payload that round-trips over the broker.
type WorkItem struct {
	// ID uniquely identifies the work item (idempotency key).
	ID string `json:"id"`
	// Kind is the work type (e.g. "inference", "scan", "build").
	Kind string `json:"kind"`
	// Payload is the opaque task payload understood by the worker.
	Payload json.RawMessage `json:"payload,omitempty"`
	// Deadline is an optional absolute deadline for completing the work.
	Deadline *time.Time `json:"deadline,omitempty"`
	// IssuedAt is when the dispatcher emitted the item.
	IssuedAt time.Time `json:"issued_at"`
}

// EncodeWorkItem serializes a WorkItem to wire bytes (JSON). It validates
// that the required fields are present so a malformed item is never put on
// the wire.
func EncodeWorkItem(w WorkItem) ([]byte, error) {
	if w.ID == "" {
		return nil, fmt.Errorf("mqttclient: work item missing id")
	}
	if w.Kind == "" {
		return nil, fmt.Errorf("mqttclient: work item %q missing kind", w.ID)
	}
	return json.Marshal(w)
}

// DecodeWorkItem parses wire bytes into a WorkItem, validating required
// fields. A decode that yields an item with no id is reported as an error
// rather than silently returning a zero value.
func DecodeWorkItem(b []byte) (WorkItem, error) {
	var w WorkItem
	if err := json.Unmarshal(b, &w); err != nil {
		return WorkItem{}, fmt.Errorf("mqttclient: decode work item: %w", err)
	}
	if w.ID == "" {
		return WorkItem{}, fmt.Errorf("mqttclient: decoded work item missing id")
	}
	if w.Kind == "" {
		return WorkItem{}, fmt.Errorf("mqttclient: decoded work item %q missing kind", w.ID)
	}
	return w, nil
}

// NodeState enumerates the high-level health of an edge node.
type NodeState string

const (
	// StateOnline means the node is connected and ready for work.
	StateOnline NodeState = "online"
	// StateBusy means the node is connected but at capacity.
	StateBusy NodeState = "busy"
	// StateDraining means the node is finishing work and will go offline.
	StateDraining NodeState = "draining"
)

// Status is a status/heartbeat published by an edge node on its status
// topic and consumed by a dispatcher/controller.
type Status struct {
	// NodeID identifies the publishing node.
	NodeID string `json:"node_id"`
	// State is the node's high-level health.
	State NodeState `json:"state"`
	// ActiveJobs is the number of in-flight work items.
	ActiveJobs int `json:"active_jobs"`
	// LastWorkItemID is the most recently received dispatch id, if any.
	LastWorkItemID string `json:"last_work_item_id,omitempty"`
	// Timestamp is when the heartbeat was emitted.
	Timestamp time.Time `json:"timestamp"`
}

// EncodeStatus serializes a Status to wire bytes (JSON), validating that a
// node id and a known state are present.
func EncodeStatus(s Status) ([]byte, error) {
	if s.NodeID == "" {
		return nil, fmt.Errorf("mqttclient: status missing node_id")
	}
	switch s.State {
	case StateOnline, StateBusy, StateDraining:
	default:
		return nil, fmt.Errorf("mqttclient: status for %q has invalid state %q", s.NodeID, s.State)
	}
	return json.Marshal(s)
}

// DecodeStatus parses wire bytes into a Status, validating required fields.
func DecodeStatus(b []byte) (Status, error) {
	var s Status
	if err := json.Unmarshal(b, &s); err != nil {
		return Status{}, fmt.Errorf("mqttclient: decode status: %w", err)
	}
	if s.NodeID == "" {
		return Status{}, fmt.Errorf("mqttclient: decoded status missing node_id")
	}
	switch s.State {
	case StateOnline, StateBusy, StateDraining:
	default:
		return Status{}, fmt.Errorf("mqttclient: decoded status for %q has invalid state %q", s.NodeID, s.State)
	}
	return s, nil
}
