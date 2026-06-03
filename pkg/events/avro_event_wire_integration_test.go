//go:build integration

package events

import (
	"context"
	"testing"
	"time"

	"digital.vasic.containers/pkg/brokertest"
)

// TestNATSBackend_TypedAvroWire_RealRoundTrip is the HXC-1627 CLAUDE-1 sink-side
// proof for the three non-Node Helix event families (SessionEvent,
// SchedulerEvent, AuditEvent). It boots ONE real NATS+JetStream broker via
// brokertest, builds an Avro-mode NATSBackend with all three writer schemas
// registered, and proves on the SAME live bus, for EACH type, that:
//
//  1. the bytes ACTUALLY traversing the broker start with 0xC3 0x01 (the Avro
//     single-object marker) — i.e. Avro-on-the-wire, NOT JSON (a JSON payload
//     would start with '{' = 0x7B);
//  2. the typed Avro subscriber decodes the EXACT event back via the
//     SchemaRegistry (real round-trip of every identity field, ts to nanos);
//  3. the event lands on the correct {domain}.{entity}.{action} subject.
//
// No mocks — the bytes cross a real NATS server. t.Skip fires ONLY if the broker
// fails to boot (e.g. no container runtime), with the reason recorded.
func TestNATSBackend_TypedAvroWire_RealRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	endpoint, stopBroker, err := brokertest.StartNATS(ctx)
	if err != nil {
		t.Skipf("brokertest.StartNATS failed to boot a real broker (no container runtime?): %v", err)
	}
	defer stopBroker()
	t.Logf("real NATS broker up at %s", endpoint)

	// One registry pre-loaded with every writer schema we will publish so
	// writer-fingerprint resolution works on the subscribe side regardless of
	// which type produced a given payload.
	reg := registryWith(
		SessionEventSchemaV1(),
		SchedulerEventSchemaV1(),
		AuditEventSchemaV1(),
	)

	backend, err := NewNATSBackend(ctx, endpoint, WithAvroEvents(reg))
	if err != nil {
		t.Fatalf("NewNATSBackend(avro events): %v", err)
	}
	defer backend.Close()

	ts := time.Date(2026, 6, 3, 9, 49, 46, 123, time.UTC)

	t.Run("SessionEvent", func(t *testing.T) {
		const sid, act = "sess-9", "attach"
		subject := HelixSubject("sessions", sid, act)
		want := &SessionEvent{RunID: "hxc-1627-sess", SessionID: sid, Action: act, Timestamp: ts}

		rawCtx, rawCancel := context.WithCancel(ctx)
		defer rawCancel()
		rawCh, rawUnsub, err := backend.Subscribe(rawCtx, subject)
		if err != nil {
			t.Fatalf("Subscribe(raw %q): %v", subject, err)
		}
		defer rawUnsub()

		typedCtx, typedCancel := context.WithCancel(ctx)
		defer typedCancel()
		typedCh, typedUnsub, err := backend.SubscribeSessionEvents(typedCtx, sid, act)
		if err != nil {
			t.Fatalf("SubscribeSessionEvents: %v", err)
		}
		defer typedUnsub()

		time.Sleep(500 * time.Millisecond)
		if err := backend.PublishSessionEvent(want); err != nil {
			t.Fatalf("PublishSessionEvent(avro): %v", err)
		}

		assertAvroMarker(t, rawCh)

		select {
		case got, ok := <-typedCh:
			if !ok {
				t.Fatal("typed session channel closed before delivery")
			}
			if got.RunID != want.RunID || got.SessionID != want.SessionID || got.Action != want.Action || !got.Timestamp.Equal(ts) {
				t.Fatalf("session round-trip mismatch: got %#v want %#v", got, want)
			}
			t.Logf("real Avro session round-trip: %#v", got)
		case <-time.After(20 * time.Second):
			t.Fatal("timed out waiting for typed session event")
		}
	})

	t.Run("SchedulerEvent", func(t *testing.T) {
		const eid, act = "task-3", "assign"
		subject := HelixSubject("scheduler", eid, act)
		want := &SchedulerEvent{RunID: "hxc-1627-sched", EntityID: eid, Action: act, Timestamp: ts}

		rawCtx, rawCancel := context.WithCancel(ctx)
		defer rawCancel()
		rawCh, rawUnsub, err := backend.Subscribe(rawCtx, subject)
		if err != nil {
			t.Fatalf("Subscribe(raw %q): %v", subject, err)
		}
		defer rawUnsub()

		typedCtx, typedCancel := context.WithCancel(ctx)
		defer typedCancel()
		typedCh, typedUnsub, err := backend.SubscribeSchedulerEvents(typedCtx, eid, act)
		if err != nil {
			t.Fatalf("SubscribeSchedulerEvents: %v", err)
		}
		defer typedUnsub()

		time.Sleep(500 * time.Millisecond)
		if err := backend.PublishSchedulerEvent(want); err != nil {
			t.Fatalf("PublishSchedulerEvent(avro): %v", err)
		}

		assertAvroMarker(t, rawCh)

		select {
		case got, ok := <-typedCh:
			if !ok {
				t.Fatal("typed scheduler channel closed before delivery")
			}
			if got.RunID != want.RunID || got.EntityID != want.EntityID || got.Action != want.Action || !got.Timestamp.Equal(ts) {
				t.Fatalf("scheduler round-trip mismatch: got %#v want %#v", got, want)
			}
			t.Logf("real Avro scheduler round-trip: %#v", got)
		case <-time.After(20 * time.Second):
			t.Fatal("timed out waiting for typed scheduler event")
		}
	})

	t.Run("AuditEvent", func(t *testing.T) {
		const subjID, act = "node-1", "delete"
		subject := HelixSubject("audit", subjID, act)
		want := &AuditEvent{RunID: "hxc-1627-audit", Actor: "alice", SubjectID: subjID, Action: act, Timestamp: ts}

		rawCtx, rawCancel := context.WithCancel(ctx)
		defer rawCancel()
		rawCh, rawUnsub, err := backend.Subscribe(rawCtx, subject)
		if err != nil {
			t.Fatalf("Subscribe(raw %q): %v", subject, err)
		}
		defer rawUnsub()

		typedCtx, typedCancel := context.WithCancel(ctx)
		defer typedCancel()
		typedCh, typedUnsub, err := backend.SubscribeAuditEvents(typedCtx, subjID, act)
		if err != nil {
			t.Fatalf("SubscribeAuditEvents: %v", err)
		}
		defer typedUnsub()

		time.Sleep(500 * time.Millisecond)
		if err := backend.PublishAuditEvent(want); err != nil {
			t.Fatalf("PublishAuditEvent(avro): %v", err)
		}

		assertAvroMarker(t, rawCh)

		select {
		case got, ok := <-typedCh:
			if !ok {
				t.Fatal("typed audit channel closed before delivery")
			}
			if got.RunID != want.RunID || got.Actor != want.Actor || got.SubjectID != want.SubjectID || got.Action != want.Action || !got.Timestamp.Equal(ts) {
				t.Fatalf("audit round-trip mismatch: got %#v want %#v", got, want)
			}
			t.Logf("real Avro audit round-trip: %#v", got)
		case <-time.After(20 * time.Second):
			t.Fatal("timed out waiting for typed audit event")
		}
	})
}

// assertAvroMarker reads one raw Event off the live bus and asserts its payload
// begins with the 0xC3 0x01 Avro single-object marker (JSON would start '{').
func assertAvroMarker(t *testing.T, rawCh <-chan Event) {
	t.Helper()
	select {
	case got, ok := <-rawCh:
		if !ok {
			t.Fatal("raw subscription channel closed before delivery")
		}
		if len(got.Payload) < 10 {
			t.Fatalf("payload too short to be Avro single-object: % x", got.Payload)
		}
		if got.Payload[0] != 0xC3 || got.Payload[1] != 0x01 {
			t.Fatalf("on-wire bytes are NOT Avro single-object (got marker 0x%02X%02X, first byte %q) — Avro path not honored",
				got.Payload[0], got.Payload[1], string(got.Payload[0]))
		}
		t.Logf("on-wire Avro marker confirmed: % x ...", got.Payload[:10])
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for raw Avro bytes over the live broker")
	}
}
