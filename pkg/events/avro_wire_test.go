package events

import (
	"testing"
	"time"
)

// TestEncodeNodeEventAvro_HasSingleObjectMarker proves the wire bytes for a
// NodeEvent are Avro single-object encoded: 0xC3 0x01 + the writer fingerprint.
// This is the on-wire-format guarantee — JSON would never start with 0xC3 0x01.
func TestEncodeNodeEventAvro_HasSingleObjectMarker(t *testing.T) {
	ne := &NodeEvent{RunID: "r1", NodeID: "n1", Action: "join", Timestamp: time.Unix(0, 123).UTC()}
	w := NodeEventSchemaV2()

	enc, err := EncodeNodeEventAvro(ne, w)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(enc) < 10 || enc[0] != 0xC3 || enc[1] != 0x01 {
		t.Fatalf("missing single-object marker; got % x", enc)
	}
}

// TestNodeEventAvro_RoundTrip proves a NodeEvent survives encode->decode under
// the same schema with every identity field intact (timestamp to nanos).
func TestNodeEventAvro_RoundTrip(t *testing.T) {
	w := NodeEventSchemaV2()
	reg := registryWith(w)
	ts := time.Date(2026, 6, 3, 10, 11, 12, 13, time.UTC)
	ne := &NodeEvent{RunID: "run-1", NodeID: "node-7", Action: "heartbeat", Timestamp: ts}

	enc, err := EncodeNodeEventAvro(ne, w)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := DecodeNodeEventAvro(enc, reg, w)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.RunID != ne.RunID || got.NodeID != ne.NodeID || got.Action != ne.Action {
		t.Fatalf("identity mismatch: got %#v want %#v", got, ne)
	}
	if !got.Timestamp.Equal(ts) {
		t.Fatalf("timestamp mismatch: got %v want %v", got.Timestamp, ts)
	}
}

// TestNodeEventAvro_Evolution_V2WriterV1Reader: a V1 reader decodes a V2-written
// NodeEvent, keeping the shared fields and dropping action (which V1 lacks).
func TestNodeEventAvro_Evolution_V2WriterV1Reader(t *testing.T) {
	writer := NodeEventSchemaV2()
	reader := NodeEventSchemaV1()
	reg := registryWith(writer)
	ne := &NodeEvent{RunID: "rX", NodeID: "nX", Action: "leave", Timestamp: time.Unix(0, 99).UTC()}

	enc, err := EncodeNodeEventAvro(ne, writer)
	if err != nil {
		t.Fatalf("encode v2: %v", err)
	}
	got, err := DecodeNodeEventAvro(enc, reg, reader)
	if err != nil {
		t.Fatalf("v1 reader decode: %v", err)
	}
	if got.RunID != "rX" || got.NodeID != "nX" || got.Timestamp.UnixNano() != 99 {
		t.Fatalf("shared fields not intact: %#v", got)
	}
	if got.Action != "" {
		t.Fatalf("v1 reader must not surface v2 action, got %q", got.Action)
	}
}

// TestNodeEventAvro_Evolution_V1WriterV2Reader: a V2 reader decodes a V1-written
// NodeEvent, synthesizing the missing action from its schema default ("").
func TestNodeEventAvro_Evolution_V1WriterV2Reader(t *testing.T) {
	writer := NodeEventSchemaV1()
	reader := NodeEventSchemaV2()
	reg := registryWith(writer)
	ne := &NodeEvent{RunID: "rOld", NodeID: "nOld", Action: "ignored-by-v1-schema", Timestamp: time.Unix(0, 7).UTC()}

	enc, err := EncodeNodeEventAvro(ne, writer)
	if err != nil {
		t.Fatalf("encode v1: %v", err)
	}
	got, err := DecodeNodeEventAvro(enc, reg, reader)
	if err != nil {
		t.Fatalf("v2 reader decode: %v", err)
	}
	if got.RunID != "rOld" || got.NodeID != "nOld" || got.Timestamp.UnixNano() != 7 {
		t.Fatalf("shared fields not intact: %#v", got)
	}
	if got.Action != "" {
		t.Fatalf("v2 reader must default action to \"\", got %q", got.Action)
	}
}

// TestSessionEventAvro_RoundTrip proves the new concrete SessionEvent type
// round-trips over the Avro codec with the single-object marker.
func TestSessionEventAvro_RoundTrip(t *testing.T) {
	w := SessionEventSchemaV1()
	reg := registryWith(w)
	ts := time.Unix(0, 555).UTC()
	e := &SessionEvent{RunID: "s-run", SessionID: "sess-9", Action: "attach", Timestamp: ts}

	enc, err := EncodeSessionEventAvro(e, w)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if enc[0] != 0xC3 || enc[1] != 0x01 {
		t.Fatalf("missing single-object marker")
	}
	got, err := DecodeSessionEventAvro(enc, reg, w)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.RunID != e.RunID || got.SessionID != e.SessionID || got.Action != e.Action || !got.Timestamp.Equal(ts) {
		t.Fatalf("round-trip mismatch: got %#v want %#v", got, e)
	}
	if got := e.Subject(); got != "sessions.sess-9.attach" {
		t.Fatalf("subject: got %q", got)
	}
}

// TestSchedulerEventAvro_RoundTrip proves the new SchedulerEvent type round-trips.
func TestSchedulerEventAvro_RoundTrip(t *testing.T) {
	w := SchedulerEventSchemaV1()
	reg := registryWith(w)
	ts := time.Unix(0, 777).UTC()
	e := &SchedulerEvent{RunID: "sc-run", EntityID: "task-3", Action: "assign", Timestamp: ts}

	enc, err := EncodeSchedulerEventAvro(e, w)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if enc[0] != 0xC3 || enc[1] != 0x01 {
		t.Fatalf("missing single-object marker")
	}
	got, err := DecodeSchedulerEventAvro(enc, reg, w)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.RunID != e.RunID || got.EntityID != e.EntityID || got.Action != e.Action || !got.Timestamp.Equal(ts) {
		t.Fatalf("round-trip mismatch: got %#v want %#v", got, e)
	}
	if got := e.Subject(); got != "scheduler.task-3.assign" {
		t.Fatalf("subject: got %q", got)
	}
}

// TestAuditEventAvro_RoundTrip proves the new AuditEvent type round-trips.
func TestAuditEventAvro_RoundTrip(t *testing.T) {
	w := AuditEventSchemaV1()
	reg := registryWith(w)
	ts := time.Unix(0, 888).UTC()
	e := &AuditEvent{RunID: "a-run", Actor: "alice", SubjectID: "node-1", Action: "delete", Timestamp: ts}

	enc, err := EncodeAuditEventAvro(e, w)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if enc[0] != 0xC3 || enc[1] != 0x01 {
		t.Fatalf("missing single-object marker")
	}
	got, err := DecodeAuditEventAvro(enc, reg, w)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.RunID != e.RunID || got.Actor != e.Actor || got.SubjectID != e.SubjectID || got.Action != e.Action || !got.Timestamp.Equal(ts) {
		t.Fatalf("round-trip mismatch: got %#v want %#v", got, e)
	}
	if got := e.Subject(); got != "audit.node-1.delete" {
		t.Fatalf("subject: got %q", got)
	}
}
