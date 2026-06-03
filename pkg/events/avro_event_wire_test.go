package events

import (
	"testing"
	"time"
)

// TestTypedEventJSON_RoundTrip proves the JSON back-compat marshal/unmarshal
// helpers preserve every identity field for the three non-Node event families.
// The on-wire JSON MUST start with '{' (0x7B) — never the Avro 0xC3 marker — so
// the default wire format stays JSON and back-compatible.
func TestTypedEventJSON_RoundTrip(t *testing.T) {
	ts := time.Date(2026, 6, 3, 9, 49, 46, 123, time.UTC)

	t.Run("Session", func(t *testing.T) {
		e := &SessionEvent{RunID: "r", SessionID: "s9", Action: "attach", Timestamp: ts}
		b, err := e.MarshalPayload()
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if b[0] != '{' {
			t.Fatalf("JSON payload must start with '{', got % x", b[:1])
		}
		got, err := UnmarshalSessionEvent(b)
		if err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.RunID != e.RunID || got.SessionID != e.SessionID || got.Action != e.Action || !got.Timestamp.Equal(ts) {
			t.Fatalf("mismatch: got %#v want %#v", got, e)
		}
	})

	t.Run("Scheduler", func(t *testing.T) {
		e := &SchedulerEvent{RunID: "r", EntityID: "t3", Action: "assign", Timestamp: ts}
		b, err := e.MarshalPayload()
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if b[0] != '{' {
			t.Fatalf("JSON payload must start with '{', got % x", b[:1])
		}
		got, err := UnmarshalSchedulerEvent(b)
		if err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.RunID != e.RunID || got.EntityID != e.EntityID || got.Action != e.Action || !got.Timestamp.Equal(ts) {
			t.Fatalf("mismatch: got %#v want %#v", got, e)
		}
	})

	t.Run("Audit", func(t *testing.T) {
		e := &AuditEvent{RunID: "r", Actor: "alice", SubjectID: "n1", Action: "delete", Timestamp: ts}
		b, err := e.MarshalPayload()
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if b[0] != '{' {
			t.Fatalf("JSON payload must start with '{', got % x", b[:1])
		}
		got, err := UnmarshalAuditEvent(b)
		if err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.RunID != e.RunID || got.Actor != e.Actor || got.SubjectID != e.SubjectID || got.Action != e.Action || !got.Timestamp.Equal(ts) {
			t.Fatalf("mismatch: got %#v want %#v", got, e)
		}
	})
}

// TestWithAvroEvents_RegistersAllWriterSchemas proves WithAvroEvents flips the
// backend to WireAvro, sets each family's writer schema, and registers all three
// writer fingerprints into the shared registry (so subscribe-side resolution
// works for any of them). It exercises the option in isolation — no broker.
func TestWithAvroEvents_RegistersAllWriterSchemas(t *testing.T) {
	reg := NewSchemaRegistry()
	n := &NATSBackend{wireFormat: WireJSON}
	WithAvroEvents(reg)(n)

	if n.wireFormat != WireAvro {
		t.Fatalf("WithAvroEvents must set WireAvro, got %v", n.wireFormat)
	}
	if n.sessionWriter == nil || n.schedulerWriter == nil || n.auditWriter == nil {
		t.Fatal("WithAvroEvents must set all three writer schemas")
	}
	for _, s := range []*AvroSchema{SessionEventSchemaV1(), SchedulerEventSchemaV1(), AuditEventSchemaV1()} {
		if _, ok := reg.Lookup(s.Fingerprint()); !ok {
			t.Fatalf("writer schema %s not registered by WithAvroEvents", s.Name)
		}
	}
}
