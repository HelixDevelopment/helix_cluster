package events

// Concrete Avro schemas + payload types for the non-Node Helix event families
// (HXC-1626 deliverable #2).
//
// Before HXC-1626 only NodeEvent was a concrete Go type; SessionEvent,
// SchedulerEvent and AuditEvent existed only as STREAM CONCEPTS (the
// HELIX_SESSIONS / HELIX_SCHEDULER / audit subject families). This file makes
// them concrete, flat, primitive-record types with matching AvroSchema so the
// Avro single-object wire path (avro.go + avro_wire.go) carries them just like
// NodeEvent.
//
// Each type maps the {domain}.{entity_id}.{action} subject convention and a small
// set of identity fields. They are deliberately flat (string/long only) so they
// fit the Avro subset; richer context goes through the JSON path.

import (
	"fmt"
	"time"
)

// --- SessionEvent (HELIX_SESSIONS) -----------------------------------------

// SessionEvent is a session-lifecycle event (create / attach / detach /
// terminate) carried on the HELIX_SESSIONS stream under "sessions.>".
type SessionEvent struct {
	RunID     string    `json:"run_id"`
	SessionID string    `json:"session_id"`
	Action    string    `json:"action"`
	Timestamp time.Time `json:"timestamp"`
}

// Subject returns the canonical "sessions.{SessionID}.{Action}" subject.
func (e *SessionEvent) Subject() string {
	return HelixSubject("sessions", e.SessionID, e.Action)
}

// SessionEventSchemaV1 is the flat Avro projection of SessionEvent.
func SessionEventSchemaV1() *AvroSchema {
	return &AvroSchema{
		Name: "SessionEvent",
		Fields: []AvroField{
			{Name: "run_id", Type: AvroString},
			{Name: "session_id", Type: AvroString},
			{Name: "action", Type: AvroString},
			{Name: "timestamp", Type: AvroLong},
		},
	}
}

// EncodeSessionEventAvro encodes a SessionEvent as Avro single-object bytes.
func EncodeSessionEventAvro(e *SessionEvent, writer *AvroSchema) ([]byte, error) {
	if e == nil {
		return nil, fmt.Errorf("events/avro-wire: cannot encode nil SessionEvent")
	}
	return AvroEncode(writer, map[string]any{
		"run_id":     e.RunID,
		"session_id": e.SessionID,
		"action":     e.Action,
		"timestamp":  e.Timestamp.UTC().UnixNano(),
	})
}

// DecodeSessionEventAvro decodes Avro single-object bytes into a SessionEvent.
func DecodeSessionEventAvro(data []byte, registry *SchemaRegistry, readerSchema *AvroSchema) (*SessionEvent, error) {
	rec, err := AvroDecode(data, registry, readerSchema)
	if err != nil {
		return nil, err
	}
	e := &SessionEvent{}
	if v, ok := rec["run_id"].(string); ok {
		e.RunID = v
	}
	if v, ok := rec["session_id"].(string); ok {
		e.SessionID = v
	}
	if v, ok := rec["action"].(string); ok {
		e.Action = v
	}
	if v, ok := rec["timestamp"].(int64); ok {
		e.Timestamp = time.Unix(0, v).UTC()
	}
	return e, nil
}

// --- SchedulerEvent (HELIX_SCHEDULER) --------------------------------------

// SchedulerEvent is a scheduler event (assignment / preemption / queue change)
// carried on the HELIX_SCHEDULER stream under "scheduler.>". EntityID is the
// scheduled unit (e.g. a task or run id).
type SchedulerEvent struct {
	RunID     string    `json:"run_id"`
	EntityID  string    `json:"entity_id"`
	Action    string    `json:"action"`
	Timestamp time.Time `json:"timestamp"`
}

// Subject returns the canonical "scheduler.{EntityID}.{Action}" subject.
func (e *SchedulerEvent) Subject() string {
	return HelixSubject("scheduler", e.EntityID, e.Action)
}

// SchedulerEventSchemaV1 is the flat Avro projection of SchedulerEvent.
func SchedulerEventSchemaV1() *AvroSchema {
	return &AvroSchema{
		Name: "SchedulerEvent",
		Fields: []AvroField{
			{Name: "run_id", Type: AvroString},
			{Name: "entity_id", Type: AvroString},
			{Name: "action", Type: AvroString},
			{Name: "timestamp", Type: AvroLong},
		},
	}
}

// EncodeSchedulerEventAvro encodes a SchedulerEvent as Avro single-object bytes.
func EncodeSchedulerEventAvro(e *SchedulerEvent, writer *AvroSchema) ([]byte, error) {
	if e == nil {
		return nil, fmt.Errorf("events/avro-wire: cannot encode nil SchedulerEvent")
	}
	return AvroEncode(writer, map[string]any{
		"run_id":    e.RunID,
		"entity_id": e.EntityID,
		"action":    e.Action,
		"timestamp": e.Timestamp.UTC().UnixNano(),
	})
}

// DecodeSchedulerEventAvro decodes Avro single-object bytes into a SchedulerEvent.
func DecodeSchedulerEventAvro(data []byte, registry *SchemaRegistry, readerSchema *AvroSchema) (*SchedulerEvent, error) {
	rec, err := AvroDecode(data, registry, readerSchema)
	if err != nil {
		return nil, err
	}
	e := &SchedulerEvent{}
	if v, ok := rec["run_id"].(string); ok {
		e.RunID = v
	}
	if v, ok := rec["entity_id"].(string); ok {
		e.EntityID = v
	}
	if v, ok := rec["action"].(string); ok {
		e.Action = v
	}
	if v, ok := rec["timestamp"].(int64); ok {
		e.Timestamp = time.Unix(0, v).UTC()
	}
	return e, nil
}

// --- AuditEvent (HELIX audit family) ---------------------------------------

// AuditEvent is an audit-log event (an actor performed an action on a subject).
// It carries the actor, the affected subject id, and the action.
type AuditEvent struct {
	RunID     string    `json:"run_id"`
	Actor     string    `json:"actor"`
	SubjectID string    `json:"subject_id"`
	Action    string    `json:"action"`
	Timestamp time.Time `json:"timestamp"`
}

// Subject returns the canonical "audit.{SubjectID}.{Action}" subject.
func (e *AuditEvent) Subject() string {
	return HelixSubject("audit", e.SubjectID, e.Action)
}

// AuditEventSchemaV1 is the flat Avro projection of AuditEvent.
func AuditEventSchemaV1() *AvroSchema {
	return &AvroSchema{
		Name: "AuditEvent",
		Fields: []AvroField{
			{Name: "run_id", Type: AvroString},
			{Name: "actor", Type: AvroString},
			{Name: "subject_id", Type: AvroString},
			{Name: "action", Type: AvroString},
			{Name: "timestamp", Type: AvroLong},
		},
	}
}

// EncodeAuditEventAvro encodes an AuditEvent as Avro single-object bytes.
func EncodeAuditEventAvro(e *AuditEvent, writer *AvroSchema) ([]byte, error) {
	if e == nil {
		return nil, fmt.Errorf("events/avro-wire: cannot encode nil AuditEvent")
	}
	return AvroEncode(writer, map[string]any{
		"run_id":     e.RunID,
		"actor":      e.Actor,
		"subject_id": e.SubjectID,
		"action":     e.Action,
		"timestamp":  e.Timestamp.UTC().UnixNano(),
	})
}

// DecodeAuditEventAvro decodes Avro single-object bytes into an AuditEvent.
func DecodeAuditEventAvro(data []byte, registry *SchemaRegistry, readerSchema *AvroSchema) (*AuditEvent, error) {
	rec, err := AvroDecode(data, registry, readerSchema)
	if err != nil {
		return nil, err
	}
	e := &AuditEvent{}
	if v, ok := rec["run_id"].(string); ok {
		e.RunID = v
	}
	if v, ok := rec["actor"].(string); ok {
		e.Actor = v
	}
	if v, ok := rec["subject_id"].(string); ok {
		e.SubjectID = v
	}
	if v, ok := rec["action"].(string); ok {
		e.Action = v
	}
	if v, ok := rec["timestamp"].(int64); ok {
		e.Timestamp = time.Unix(0, v).UTC()
	}
	return e, nil
}
