//go:build integration

package events

import (
	"bytes"
	"context"
	"testing"
	"time"

	"digital.vasic.containers/pkg/brokertest"
)

// TestNATSBackend_RealRoundTrip is the CLAUDE-1 sink-side proof: it boots a REAL
// NATS+JetStream broker in a container via brokertest, builds a NATSBackend
// against that live endpoint, and asserts that a published event is delivered to
// a subscriber of the SAME type (real round-trip) while an event of a DIFFERENT
// type is NOT delivered to that subscriber. No mocks — the bytes traverse a real
// NATS server over the network.
func TestNATSBackend_RealRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	endpoint, stopBroker, err := brokertest.StartNATS(ctx)
	if err != nil {
		t.Fatalf("StartNATS: %v", err)
	}
	defer stopBroker()
	t.Logf("real NATS broker up at %s", endpoint)

	backend, err := NewNATSBackend(ctx, endpoint)
	if err != nil {
		t.Fatalf("NewNATSBackend(%q): %v", endpoint, err)
	}
	defer backend.Close()

	const wantType = "order.created"
	const otherType = "order.cancelled"

	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()

	ch, unsub, err := backend.Subscribe(subCtx, wantType)
	if err != nil {
		t.Fatalf("Subscribe(%q): %v", wantType, err)
	}
	defer unsub()

	// JetStream subscriptions use DeliverNew: only events published strictly
	// after the subscription is established are delivered. Give the push
	// consumer a moment to attach before publishing.
	time.Sleep(500 * time.Millisecond)

	wantPayload := []byte(`{"order_id":"ORD-42","amount":1999}`)
	want := Event{
		ID:        "evt-1",
		Type:      wantType,
		Payload:   wantPayload,
		Timestamp: time.Now().UTC().Truncate(time.Second),
	}

	// Publish a DIFFERENT type first — it must never reach the wantType channel.
	if err := backend.Publish(Event{
		ID:        "evt-other",
		Type:      otherType,
		Payload:   []byte(`{"order_id":"ORD-99"}`),
		Timestamp: time.Now().UTC().Truncate(time.Second),
	}); err != nil {
		t.Fatalf("Publish(other): %v", err)
	}

	// Publish the matching event.
	if err := backend.Publish(want); err != nil {
		t.Fatalf("Publish(want): %v", err)
	}

	// Assert the SAME event is received via the real broker round-trip.
	select {
	case got, ok := <-ch:
		if !ok {
			t.Fatal("subscription channel closed before delivery")
		}
		if got.ID != want.ID {
			t.Errorf("round-trip ID: got %q want %q", got.ID, want.ID)
		}
		if got.Type != want.Type {
			t.Errorf("round-trip Type: got %q want %q", got.Type, want.Type)
		}
		if !bytes.Equal(got.Payload, want.Payload) {
			t.Errorf("round-trip Payload: got %q want %q", got.Payload, want.Payload)
		}
		if !got.Timestamp.Equal(want.Timestamp) {
			t.Errorf("round-trip Timestamp: got %v want %v", got.Timestamp, want.Timestamp)
		}
		t.Logf("real round-trip delivered: id=%s type=%s payload=%s", got.ID, got.Type, got.Payload)
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for real NATS round-trip delivery")
	}

	// Assert the DIFFERENT type is NOT delivered to this subscriber. The
	// other-type event was published before the want event and already
	// round-tripped; if cross-type leakage existed it would be queued ahead and
	// observed now. A short drain window with nothing pending proves isolation.
	select {
	case leaked, ok := <-ch:
		if ok {
			t.Fatalf("different-type event leaked into %q subscription: id=%s type=%s",
				wantType, leaked.ID, leaked.Type)
		}
		// channel closed: acceptable (no leak), test already validated delivery.
	case <-time.After(3 * time.Second):
		// No further delivery — the other-type event was correctly NOT routed
		// to the wantType subscriber. This is the expected, passing path.
		t.Log("different-type event correctly NOT delivered to wantType subscriber")
	}
}
