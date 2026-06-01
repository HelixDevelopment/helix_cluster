//go:build integration

// Package events — HXC-1075 integration tests.
//
// These tests require a REAL NATS+JetStream broker. The broker is started
// via brokertest.StartNATS (digital.vasic.containers/pkg/brokertest); if
// StartNATS returns an error the test hard-FAILs (not skips) — a skip on
// a broken feature is itself a PASS-bluff.
//
// Per-run UUID discipline: every test embeds a fresh uuid in the published
// NodeEvent and asserts that exact uuid arrives on the subscriber side.
// This guarantees the CURRENT run's message is observed, not a stale one.
package events

import (
	"context"
	"fmt"
	"testing"
	"time"

	"digital.vasic.containers/pkg/brokertest"
	"github.com/google/uuid"
)

// TestHelixBackend_NodeEvent_RealRoundTrip is the §7.1 sink-side proof for
// HXC-1075. It boots a real NATS+JetStream broker, builds a HelixBackend,
// publishes a NodeEvent on nodes.<uuid>.heartbeat, and asserts the subscriber
// receives the IDENTICAL payload (per-run UUID exact match). It also captures
// stream-info for the HELIX_NODES stream as §7.1 evidence.
func TestHelixBackend_NodeEvent_RealRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Start a real NATS+JetStream broker in a container.
	endpoint, stopBroker, err := brokertest.StartNATS(ctx)
	if err != nil {
		// Hard-FAIL: if the broker is unavailable the integration gate is
		// broken. A skip here would be a PASS-bluff (CLAUDE-1 §7.1).
		t.Fatalf("brokertest.StartNATS: %v", err)
	}
	defer stopBroker()
	t.Logf("real NATS broker at %s", endpoint)

	backend, err := NewHelixBackend(ctx, HelixBackendConfig{URL: endpoint})
	if err != nil {
		t.Fatalf("NewHelixBackend: %v", err)
	}
	defer backend.Close()

	// Per-run UUID — this is the §7.1 anti-bluff anchor: the subscriber must
	// observe THIS exact value, not any prior run's value.
	runID := uuid.New().String()
	nodeID := "integ-node-" + runID[:8]

	// Subscribe BEFORE publishing (DeliverNew policy only delivers post-sub msgs).
	ch, stop, err := backend.SubscribeNodeEvents(nodeID, "heartbeat")
	if err != nil {
		t.Fatalf("SubscribeNodeEvents: %v", err)
	}
	defer stop()

	// Allow the push consumer to attach before publishing.
	time.Sleep(500 * time.Millisecond)

	want := &NodeEvent{
		RunID:     runID,
		NodeID:    nodeID,
		Action:    "heartbeat",
		Timestamp: time.Now().UTC().Truncate(time.Second),
		Metadata:  map[string]string{"test": "HXC-1075"},
	}

	// Verify the subject the event will be published on.
	wantSubject := fmt.Sprintf("nodes.%s.heartbeat", nodeID)
	if got := want.Subject(); got != wantSubject {
		t.Fatalf("NodeEvent.Subject: got %q want %q", got, wantSubject)
	}

	if err := backend.PublishNodeEvent(want); err != nil {
		t.Fatalf("PublishNodeEvent: %v", err)
	}
	t.Logf("published NodeEvent: runID=%s subject=%s", runID, wantSubject)

	// Assert the IDENTICAL event arrives on the subscriber.
	select {
	case got, ok := <-ch:
		if !ok {
			t.Fatal("subscription channel closed before event delivery")
		}
		// Per-run UUID: exact match proves THIS run's message traversed the
		// real broker, not a cached or prior-run message.
		if got.RunID != runID {
			t.Errorf("RunID mismatch: got %q want %q", got.RunID, runID)
		}
		if got.NodeID != nodeID {
			t.Errorf("NodeID: got %q want %q", got.NodeID, nodeID)
		}
		if got.Action != "heartbeat" {
			t.Errorf("Action: got %q want %q", got.Action, "heartbeat")
		}
		if !got.Timestamp.Equal(want.Timestamp) {
			t.Errorf("Timestamp: got %v want %v", got.Timestamp, want.Timestamp)
		}
		t.Logf("received NodeEvent: runID=%s nodeID=%s action=%s ts=%s",
			got.RunID, got.NodeID, got.Action, got.Timestamp)

	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for real NATS NodeEvent delivery")
	}

	// §7.1 stream-info evidence: capture HELIX_NODES stream metadata after
	// publish to confirm the message was durably stored.
	info, err := backend.StreamInfo(StreamNodes)
	if err != nil {
		t.Fatalf("StreamInfo(HELIX_NODES): %v", err)
	}
	if info.NumMessages == 0 {
		t.Errorf("StreamInfo: NumMessages=0, expected ≥1 after publish")
	}
	t.Logf("HELIX_NODES stream-info: name=%s msgs=%d consumers=%d",
		info.Name, info.NumMessages, info.NumConsumers)
}

// TestHelixBackend_AllStreamsExist verifies that all 5 HELIX_* streams are
// provisioned on the real broker after NewHelixBackend returns.
func TestHelixBackend_AllStreamsExist(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	endpoint, stopBroker, err := brokertest.StartNATS(ctx)
	if err != nil {
		t.Fatalf("brokertest.StartNATS: %v", err)
	}
	defer stopBroker()

	backend, err := NewHelixBackend(ctx, HelixBackendConfig{URL: endpoint})
	if err != nil {
		t.Fatalf("NewHelixBackend: %v", err)
	}
	defer backend.Close()

	for _, stream := range AllHelixStreams {
		info, err := backend.StreamInfo(stream)
		if err != nil {
			t.Errorf("StreamInfo(%q): %v — stream not provisioned", stream, err)
			continue
		}
		t.Logf("stream %q exists: msgs=%d consumers=%d", info.Name, info.NumMessages, info.NumConsumers)
	}
}

// TestHelixBackend_CrossNode_NoLeakage publishes a NodeEvent for node-A and
// asserts a subscriber for node-B receives nothing. This proves the
// {domain}.{entity_id}.{action} subject isolation on the real broker.
func TestHelixBackend_CrossNode_NoLeakage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	endpoint, stopBroker, err := brokertest.StartNATS(ctx)
	if err != nil {
		t.Fatalf("brokertest.StartNATS: %v", err)
	}
	defer stopBroker()

	backend, err := NewHelixBackend(ctx, HelixBackendConfig{URL: endpoint})
	if err != nil {
		t.Fatalf("NewHelixBackend: %v", err)
	}
	defer backend.Close()

	runID := uuid.New().String()
	nodeA := "nodeA-" + runID[:8]
	nodeB := "nodeB-" + runID[:8]

	// Subscribe to node-B heartbeat only.
	chB, stopB, err := backend.SubscribeNodeEvents(nodeB, "heartbeat")
	if err != nil {
		t.Fatalf("SubscribeNodeEvents(nodeB): %v", err)
	}
	defer stopB()
	time.Sleep(500 * time.Millisecond)

	// Publish for node-A — must NOT reach node-B's subscriber.
	evA := &NodeEvent{
		RunID:     runID,
		NodeID:    nodeA,
		Action:    "heartbeat",
		Timestamp: time.Now().UTC(),
	}
	if err := backend.PublishNodeEvent(evA); err != nil {
		t.Fatalf("PublishNodeEvent(nodeA): %v", err)
	}

	select {
	case leaked, ok := <-chB:
		if ok {
			t.Fatalf("cross-node leak: nodeB subscriber received event for %q (runID=%s)",
				leaked.NodeID, leaked.RunID)
		}
	case <-time.After(3 * time.Second):
		// Correct: no delivery to nodeB subscriber.
		t.Log("no cross-node leakage confirmed on real broker")
	}
}
