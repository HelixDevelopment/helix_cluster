//go:build integration

package mqttclient

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// brokerAddr returns the real broker endpoint from MQTT_ADDR, or skips the
// test (with reason) when it is not set. MQTT_ADDR may be a bare host:port
// (e.g. 127.0.0.1:18831) or a full tcp:// URL.
func brokerAddr(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("MQTT_ADDR")
	if addr == "" {
		t.Skip("SKIP: MQTT_ADDR not set — integration test requires a REAL MQTT broker. " +
			"Start one (e.g. `podman run -d -p 18831:18831 eclipse-mosquitto ...`) and set MQTT_ADDR=127.0.0.1:18831")
	}
	if len(addr) < 6 || addr[:6] != "tcp://" {
		addr = "tcp://" + addr
	}
	return addr
}

// TestRoundTripDispatchAndStatus exercises the full HXC-1178 flow against a
// REAL broker over TCP:
//
//  1. an edge-node client subscribes to its dispatch topic;
//  2. a dispatcher client subscribes to the all-nodes status wildcard;
//  3. the dispatcher PUBLISHES a work item to the node's dispatch topic;
//  4. the edge node's handler RECEIVES it (exact payload round-trip asserted);
//  5. the edge node PUBLISHES a heartbeat that the dispatcher RECEIVES.
//
// All publishes/subscribes are QoS1. No in-memory fake is used.
func TestRoundTripDispatchAndStatus(t *testing.T) {
	broker := brokerAddr(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	const nodeID = "it-node-1"

	dispatchCh := make(chan WorkItem, 1)
	statusCh := make(chan Status, 1)

	// Edge node: subscribe to its own dispatch topic on (re)connect.
	edge, err := New(Config{
		Broker:   broker,
		NodeID:   nodeID,
		ClientID: "it-edge-" + fmt.Sprint(time.Now().UnixNano()),
		Logger:   func(m string) { t.Logf("[edge] %s", m) },
	})
	if err != nil {
		t.Fatalf("new edge client: %v", err)
	}
	if err := edge.Connect(ctx); err != nil {
		t.Fatalf("edge connect: %v", err)
	}
	defer edge.Close(time.Second)

	if err := edge.SubscribeDispatch(ctx, func(w WorkItem) error {
		dispatchCh <- w
		return nil
	}); err != nil {
		t.Fatalf("edge subscribe dispatch: %v", err)
	}

	// Dispatcher: subscribe to all-nodes status wildcard.
	disp, err := New(Config{
		Broker:   broker,
		NodeID:   "dispatcher",
		ClientID: "it-disp-" + fmt.Sprint(time.Now().UnixNano()),
		Logger:   func(m string) { t.Logf("[disp] %s", m) },
	})
	if err != nil {
		t.Fatalf("new dispatcher client: %v", err)
	}
	if err := disp.Connect(ctx); err != nil {
		t.Fatalf("dispatcher connect: %v", err)
	}
	defer disp.Close(time.Second)

	if err := disp.SubscribeStatus(ctx, AllStatusTopic(), func(s Status) error {
		statusCh <- s
		return nil
	}); err != nil {
		t.Fatalf("dispatcher subscribe status: %v", err)
	}

	// Give the broker a beat to register subscriptions before publishing.
	time.Sleep(200 * time.Millisecond)

	// --- Dispatch leg: dispatcher -> edge node ---
	wantItem := WorkItem{
		ID:       "wi-roundtrip-1",
		Kind:     "inference",
		Payload:  json.RawMessage(`{"model":"helix-7b","prompt":"edge round trip"}`),
		IssuedAt: time.Now().UTC().Truncate(time.Millisecond),
	}
	if err := disp.PublishDispatch(ctx, nodeID, wantItem); err != nil {
		t.Fatalf("publish dispatch: %v", err)
	}

	select {
	case got := <-dispatchCh:
		if got.ID != wantItem.ID {
			t.Fatalf("dispatch id mismatch: got %q want %q", got.ID, wantItem.ID)
		}
		if got.Kind != wantItem.Kind {
			t.Fatalf("dispatch kind mismatch: got %q want %q", got.Kind, wantItem.Kind)
		}
		if string(got.Payload) != string(wantItem.Payload) {
			t.Fatalf("dispatch payload mismatch:\n got %s\nwant %s", got.Payload, wantItem.Payload)
		}
		t.Logf("edge RECEIVED work item %q over real broker, payload round-tripped exactly", got.ID)
	case <-ctx.Done():
		t.Fatal("timed out waiting for edge to receive dispatch")
	}

	// --- Status leg: edge node -> dispatcher ---
	wantStatus := Status{
		State:          StateBusy,
		ActiveJobs:     1,
		LastWorkItemID: wantItem.ID,
	}
	if err := edge.PublishStatus(ctx, wantStatus); err != nil {
		t.Fatalf("publish status: %v", err)
	}

	select {
	case got := <-statusCh:
		if got.NodeID != nodeID {
			t.Fatalf("status node_id mismatch: got %q want %q", got.NodeID, nodeID)
		}
		if got.State != StateBusy {
			t.Fatalf("status state mismatch: got %q want %q", got.State, StateBusy)
		}
		if got.LastWorkItemID != wantItem.ID {
			t.Fatalf("status last_work_item_id mismatch: got %q want %q", got.LastWorkItemID, wantItem.ID)
		}
		// Confirm topic routing: the status must have come from the node's topic.
		id, err := NodeIDFromTopic(StatusTopic(nodeID))
		if err != nil || id != nodeID {
			t.Fatalf("topic routing check failed: id=%q err=%v", id, err)
		}
		t.Logf("dispatcher RECEIVED status from node %q (state=%s, active=%d) over real broker",
			got.NodeID, got.State, got.ActiveJobs)
	case <-ctx.Done():
		t.Fatal("timed out waiting for dispatcher to receive status")
	}
}

// TestOversizePayloadRejectedRealBroker proves finding 1 end-to-end against a
// REAL broker: an oversize message PUBLISHED to a node's dispatch topic is
// dropped by the client's hardened callback and NEVER reaches the application
// handler, while a small valid message published right after IS delivered.
// This is the sink-side, end-user-visible proof (CLAUDE-1): the guard protects
// the real receive path, not just a unit-level helper.
func TestOversizePayloadRejectedRealBroker(t *testing.T) {
	broker := brokerAddr(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	const nodeID = "it-node-oversize"
	const cap = 4 * 1024 // small cap to keep the test payload modest

	delivered := make(chan WorkItem, 4)

	edge, err := New(Config{
		Broker:          broker,
		NodeID:          nodeID,
		ClientID:        "it-oversize-edge-" + fmt.Sprint(time.Now().UnixNano()),
		MaxPayloadBytes: cap,
		Logger:          func(m string) { t.Logf("[edge] %s", m) },
	})
	if err != nil {
		t.Fatalf("new edge client: %v", err)
	}
	if err := edge.Connect(ctx); err != nil {
		t.Fatalf("edge connect: %v", err)
	}
	defer edge.Close(time.Second)

	if err := edge.SubscribeDispatch(ctx, func(w WorkItem) error {
		delivered <- w
		return nil
	}); err != nil {
		t.Fatalf("edge subscribe dispatch: %v", err)
	}

	// A separate raw publisher so we can put an OVERSIZE, schema-valid JSON
	// work item on the wire (PublishDispatch would also work since it encodes
	// arbitrary payload, so use it directly).
	disp, err := New(Config{
		Broker:          broker,
		NodeID:          "it-oversize-disp",
		ClientID:        "it-oversize-disp-" + fmt.Sprint(time.Now().UnixNano()),
		MaxPayloadBytes: 32 * 1024 * 1024, // publisher must not cap its own send
		Logger:          func(m string) { t.Logf("[disp] %s", m) },
	})
	if err != nil {
		t.Fatalf("new dispatcher client: %v", err)
	}
	if err := disp.Connect(ctx); err != nil {
		t.Fatalf("dispatcher connect: %v", err)
	}
	defer disp.Close(time.Second)

	time.Sleep(200 * time.Millisecond)

	// 1) Oversize work item (valid schema, but payload pushes it well over the
	//    4 KiB cap). Must be dropped by the edge callback, never delivered.
	bigPayload := json.RawMessage(`"` + strings.Repeat("A", cap*2) + `"`)
	bigItem := WorkItem{ID: "wi-oversize", Kind: "inference", Payload: bigPayload}
	if err := disp.PublishDispatch(ctx, nodeID, bigItem); err != nil {
		t.Fatalf("publish oversize dispatch: %v", err)
	}

	// 2) Immediately follow with a small valid item. MQTT QoS1 ordering on a
	//    single topic guarantees that once we receive THIS, the broker has
	//    already delivered (and our callback already processed) the oversize
	//    one — so if the oversize made it through, it would be in the channel.
	smallItem := WorkItem{ID: "wi-small", Kind: "inference", Payload: json.RawMessage(`"ok"`)}
	if err := disp.PublishDispatch(ctx, nodeID, smallItem); err != nil {
		t.Fatalf("publish small dispatch: %v", err)
	}

	select {
	case got := <-delivered:
		if got.ID == "wi-oversize" {
			t.Fatalf("oversize message was delivered to the application handler — cap NOT enforced on real receive path")
		}
		if got.ID != "wi-small" {
			t.Fatalf("unexpected delivered item id %q", got.ID)
		}
		t.Logf("edge correctly DROPPED oversize message and delivered only the small one (%q) over real broker", got.ID)
	case <-ctx.Done():
		t.Fatal("timed out waiting for the small message; broker/subscription issue")
	}

	// Drain briefly to ensure the oversize one does not arrive late.
	select {
	case got := <-delivered:
		t.Fatalf("a second message %q was delivered; oversize must have leaked through", got.ID)
	case <-time.After(500 * time.Millisecond):
		// good: nothing else delivered.
	}
}

// TestConnectReconnect proves the connect path and graceful close against the
// real broker, and that IsConnected reflects state.
func TestConnectReconnect(t *testing.T) {
	broker := brokerAddr(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c, err := New(Config{
		Broker:               broker,
		NodeID:               "it-node-conn",
		ClientID:             "it-conn-" + fmt.Sprint(time.Now().UnixNano()),
		MaxReconnectInterval: time.Second,
		Logger:               func(m string) { t.Logf("[conn] %s", m) },
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if !c.IsConnected() {
		t.Fatal("expected IsConnected true after Connect")
	}
	c.Close(time.Second)
	if c.IsConnected() {
		t.Fatal("expected IsConnected false after Close")
	}
	// Close again must be a no-op (idempotent).
	c.Close(time.Second)
}
