//go:build integration

package events

import (
	"context"
	"testing"
	"time"

	"digital.vasic.containers/pkg/brokertest"
)

// TestNATSBackend_AvroWire_RealRoundTrip is the HXC-1626 CLAUDE-1 sink-side proof:
// it boots a REAL NATS+JetStream broker via brokertest, builds an Avro-mode
// NATSBackend against the live endpoint, publishes a NodeEvent as an Avro
// single-object payload, and proves on the SAME live bus that:
//
//  1. the bytes ACTUALLY traversing the broker start with 0xC3 0x01 (the Avro
//     single-object marker) — i.e. Avro-on-the-wire, NOT JSON (a JSON payload
//     would start with '{' = 0x7B);
//  2. the typed Avro subscriber decodes the EXACT NodeEvent back via the
//     SchemaRegistry (real round-trip of every identity field);
//  3. schema evolution holds across the live bus: a V2-written payload is read
//     by a V1 reader (action dropped) and a V1-written payload is read by a V2
//     reader (action defaulted).
//
// No mocks — the bytes cross a real NATS server. t.Skip fires ONLY if the broker
// fails to boot (e.g. no container runtime), with the reason recorded.
func TestNATSBackend_AvroWire_RealRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	endpoint, stopBroker, err := brokertest.StartNATS(ctx)
	if err != nil {
		t.Skipf("brokertest.StartNATS failed to boot a real broker (no container runtime?): %v", err)
	}
	defer stopBroker()
	t.Logf("real NATS broker up at %s", endpoint)

	// Registry knows BOTH NodeEvent schema versions so writer-fingerprint
	// resolution works regardless of which version produced a given payload.
	reg := registryWith(NodeEventSchemaV1(), NodeEventSchemaV2())

	// Avro-mode backend: writer = V2, reader-resolution against the registry.
	backend, err := NewNATSBackend(ctx, endpoint, WithAvro(NodeEventSchemaV2(), reg))
	if err != nil {
		t.Fatalf("NewNATSBackend(avro): %v", err)
	}
	defer backend.Close()

	const nodeID = "node-42"
	const action = "heartbeat"
	subject := HelixSubject("nodes", nodeID, action)

	// --- Leg 1: capture the RAW on-wire bytes via the generic Subscribe and
	// assert the Avro single-object marker (proving Avro, not JSON). ---
	rawCtx, rawCancel := context.WithCancel(ctx)
	defer rawCancel()
	rawCh, rawUnsub, err := backend.Subscribe(rawCtx, subject)
	if err != nil {
		t.Fatalf("Subscribe(raw %q): %v", subject, err)
	}
	defer rawUnsub()

	// --- Leg 2: typed Avro subscriber for the decoded round-trip. ---
	typedCtx, typedCancel := context.WithCancel(ctx)
	defer typedCancel()
	typedCh, typedUnsub, err := backend.SubscribeNodeEvents(typedCtx, nodeID, action)
	if err != nil {
		t.Fatalf("SubscribeNodeEvents(%q,%q): %v", nodeID, action, err)
	}
	defer typedUnsub()

	// JetStream push consumers use DeliverNew — let both attach before publish.
	time.Sleep(500 * time.Millisecond)

	ts := time.Date(2026, 6, 3, 9, 49, 46, 0, time.UTC)
	want := &NodeEvent{RunID: "hxc-1626-run", NodeID: nodeID, Action: action, Timestamp: ts}

	if err := backend.PublishNodeEvent(want); err != nil {
		t.Fatalf("PublishNodeEvent(avro): %v", err)
	}

	// Assert raw bytes off the live bus carry the Avro single-object marker.
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

	// Assert the typed subscriber decoded the EXACT NodeEvent via the registry.
	select {
	case got, ok := <-typedCh:
		if !ok {
			t.Fatal("typed subscription channel closed before delivery")
		}
		if got.RunID != want.RunID || got.NodeID != want.NodeID || got.Action != want.Action {
			t.Fatalf("Avro round-trip identity mismatch: got %#v want %#v", got, want)
		}
		if !got.Timestamp.Equal(ts) {
			t.Fatalf("Avro round-trip timestamp: got %v want %v", got.Timestamp, ts)
		}
		t.Logf("real Avro round-trip decoded: run=%s node=%s action=%s ts=%s",
			got.RunID, got.NodeID, got.Action, got.Timestamp)
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for typed Avro NodeEvent over the live broker")
	}

	// --- Leg 3: schema evolution across the LIVE bus. ---
	// (a) V2 writer -> V1 reader: action is dropped; shared fields intact.
	t.Run("V2WriterV1Reader_overLiveBus", func(t *testing.T) {
		b2 := mustAvroBackend(ctx, t, endpoint, NodeEventSchemaV2(), reg)
		defer b2.Close()
		b1 := mustAvroBackend(ctx, t, endpoint, NodeEventSchemaV1(), reg)
		defer b1.Close()

		const nid, act = "evo-node-a", "join"
		sctx, scancel := context.WithCancel(ctx)
		defer scancel()
		ch, unsub, err := b1.SubscribeNodeEvents(sctx, nid, act)
		if err != nil {
			t.Fatalf("v1 subscribe: %v", err)
		}
		defer unsub()
		time.Sleep(500 * time.Millisecond)

		ets := time.Unix(0, 123456789).UTC()
		if err := b2.PublishNodeEvent(&NodeEvent{RunID: "evo-v2", NodeID: nid, Action: act, Timestamp: ets}); err != nil {
			t.Fatalf("v2 publish: %v", err)
		}
		select {
		case got := <-ch:
			if got.RunID != "evo-v2" || got.NodeID != nid || !got.Timestamp.Equal(ets) {
				t.Fatalf("v1 reader shared fields not intact: %#v", got)
			}
			if got.Action != "" {
				t.Fatalf("v1 reader must drop v2 action; got %q", got.Action)
			}
			t.Logf("live evolution V2->V1 ok: action correctly dropped, shared fields intact")
		case <-time.After(20 * time.Second):
			t.Fatal("timed out: v1 reader never received v2-written event")
		}
	})

	// (b) V1 writer -> V2 reader: action synthesized from default ("").
	t.Run("V1WriterV2Reader_overLiveBus", func(t *testing.T) {
		b1 := mustAvroBackend(ctx, t, endpoint, NodeEventSchemaV1(), reg)
		defer b1.Close()
		b2 := mustAvroBackend(ctx, t, endpoint, NodeEventSchemaV2(), reg)
		defer b2.Close()

		const nid, act = "evo-node-b", "leave"
		sctx, scancel := context.WithCancel(ctx)
		defer scancel()
		ch, unsub, err := b2.SubscribeNodeEvents(sctx, nid, act)
		if err != nil {
			t.Fatalf("v2 subscribe: %v", err)
		}
		defer unsub()
		time.Sleep(500 * time.Millisecond)

		ets := time.Unix(0, 987654321).UTC()
		// The V1 writer schema has no "action" field, so it is not encoded; the
		// V2 reader must default it. (We still set Action on the struct to prove
		// the V1 schema projection drops it — the wire bytes carry no action.)
		if err := b1.PublishNodeEvent(&NodeEvent{RunID: "evo-v1", NodeID: nid, Action: act, Timestamp: ets}); err != nil {
			t.Fatalf("v1 publish: %v", err)
		}
		select {
		case got := <-ch:
			if got.RunID != "evo-v1" || got.NodeID != nid || !got.Timestamp.Equal(ets) {
				t.Fatalf("v2 reader shared fields not intact: %#v", got)
			}
			if got.Action != "" {
				t.Fatalf("v2 reader must default action to \"\" (v1 payload carries none); got %q", got.Action)
			}
			t.Logf("live evolution V1->V2 ok: action defaulted from schema, shared fields intact")
		case <-time.After(20 * time.Second):
			t.Fatal("timed out: v2 reader never received v1-written event")
		}
	})
}

// mustAvroBackend builds an Avro-mode NATSBackend with the given writer schema and
// shared registry against the live endpoint, failing the test on error.
func mustAvroBackend(ctx context.Context, t *testing.T, endpoint string, writer *AvroSchema, reg *SchemaRegistry) *NATSBackend {
	t.Helper()
	b, err := NewNATSBackend(ctx, endpoint, WithAvro(writer, reg))
	if err != nil {
		t.Fatalf("NewNATSBackend(avro,%s): %v", writer.Name, err)
	}
	return b
}
