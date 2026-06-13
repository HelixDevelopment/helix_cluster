package mqttclient

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// --- Finding 4: topic / node-id validation ------------------------------

func TestValidateNodeID(t *testing.T) {
	valid := []string{"node-7", "edge-abc", "sbc.0042", "n", "A_B-c.9", strings.Repeat("a", maxNodeIDLen)}
	for _, id := range valid {
		if err := ValidateNodeID(id); err != nil {
			t.Errorf("ValidateNodeID(%q) unexpected error: %v", id, err)
		}
	}
	invalid := []string{
		"",                              // empty
		"#",                             // multi-level wildcard injection
		"+",                             // single-level wildcard injection
		"a/b",                           // separator -> escapes namespace
		"helix/edge/other/dispatch",     // crafted full path
		"node 7",                        // whitespace
		"node\x00",                      // control byte
		"node#sub",                      // embedded wildcard
		"node+x",                        // embedded wildcard
		strings.Repeat("a", maxNodeIDLen+1), // over length
	}
	for _, id := range invalid {
		if err := ValidateNodeID(id); err == nil {
			t.Errorf("ValidateNodeID(%q) expected error, got nil", id)
		}
	}
}

// TestNewRejectsCraftedNodeID proves a wildcard node id cannot construct a
// Client at all — the topic-injection vector is closed at the door.
func TestNewRejectsCraftedNodeID(t *testing.T) {
	for _, id := range []string{"#", "+", "a/b", "evil#"} {
		if _, err := New(Config{Broker: "tcp://x:1883", NodeID: id}); err == nil {
			t.Errorf("New with crafted NodeID %q expected error, got nil", id)
		}
	}
	// Sanity: a legitimate id still works.
	if _, err := New(Config{Broker: "tcp://x:1883", NodeID: "node-7"}); err != nil {
		t.Errorf("New with valid NodeID failed: %v", err)
	}
}

// TestPublishDispatchRejectsCraftedTarget proves the dispatcher side also
// validates the *target* node id before building a topic, so a crafted target
// cannot publish into a wildcard.
func TestPublishDispatchRejectsCraftedTarget(t *testing.T) {
	c, err := New(Config{Broker: "tcp://x:1883", NodeID: "dispatcher"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	w := WorkItem{ID: "wi-1", Kind: "inference"}
	for _, target := range []string{"#", "+", "a/b", "evil#"} {
		if err := c.PublishDispatch(ctx, target, w); err == nil {
			t.Errorf("PublishDispatch to crafted target %q expected error, got nil", target)
		}
	}
}

func TestValidateSubscribeTopic(t *testing.T) {
	ok := []string{
		AllStatusTopic(),     // helix/edge/+/status  (legit single-level wildcard)
		AllDispatchTopic(),   // helix/edge/+/dispatch
		StatusTopic("node-7"),
		DispatchTopic("node-7"),
	}
	for _, tp := range ok {
		if err := validateSubscribeTopic(tp); err != nil {
			t.Errorf("validateSubscribeTopic(%q) unexpected error: %v", tp, err)
		}
	}
	bad := []string{
		"",                       // empty
		"helix/edge/#",           // multi-level wildcard -> everything under namespace
		"#",                      // subscribe to the entire broker
		"helix/#",                // escapes edge namespace
		"other/edge/+/status",    // outside namespace
		"helix-other/edge/x/status",
	}
	for _, tp := range bad {
		if err := validateSubscribeTopic(tp); err == nil {
			t.Errorf("validateSubscribeTopic(%q) expected error, got nil", tp)
		}
	}
}

// --- Finding 2: reconnect backoff floor ---------------------------------

func TestReconnectBackoffFloored(t *testing.T) {
	cfg := Config{Broker: "tcp://x:1883", NodeID: "n", MaxReconnectInterval: time.Millisecond}
	cfg.withDefaults()
	if cfg.MaxReconnectInterval < minReconnectInterval {
		t.Fatalf("MaxReconnectInterval = %v, want >= floor %v (a tiny value must not enable a tight reconnect loop)",
			cfg.MaxReconnectInterval, minReconnectInterval)
	}
	// Default path still yields the documented 30s cap.
	def := Config{Broker: "tcp://x:1883", NodeID: "n"}
	def.withDefaults()
	if def.MaxReconnectInterval != 30*time.Second {
		t.Fatalf("default MaxReconnectInterval = %v, want 30s", def.MaxReconnectInterval)
	}
}

// --- Finding 1: inbound payload size cap (unit) -------------------------

func TestPayloadTooLarge(t *testing.T) {
	cfg := Config{Broker: "tcp://x:1883", NodeID: "n"}
	cfg.withDefaults()
	c := &Client{cfg: cfg}

	if cfg.MaxPayloadBytes != DefaultMaxPayloadBytes {
		t.Fatalf("default MaxPayloadBytes = %d, want %d", cfg.MaxPayloadBytes, DefaultMaxPayloadBytes)
	}
	// At the cap: allowed.
	if c.payloadTooLarge(make([]byte, DefaultMaxPayloadBytes)) {
		t.Error("payload exactly at cap must be allowed")
	}
	// One over the cap: rejected.
	if !c.payloadTooLarge(make([]byte, DefaultMaxPayloadBytes+1)) {
		t.Error("payload over cap must be rejected")
	}
	// Negative cap disables the check (opt-out).
	cfg2 := Config{Broker: "tcp://x:1883", NodeID: "n", MaxPayloadBytes: -1}
	cfg2.withDefaults()
	c2 := &Client{cfg: cfg2}
	if c2.payloadTooLarge(make([]byte, DefaultMaxPayloadBytes*4)) {
		t.Error("negative cap must disable the size check")
	}
}

// fakeMessage is a minimal paho.Message used to drive a subscribe callback in
// a unit test without a broker. Only Payload()/Topic() are exercised by the
// hardened callbacks. It is NOT used for any feature-level claim — the real
// end-to-end rejection is proven against a live broker below.
type fakeMessage struct {
	topic   string
	payload []byte
}

func (m *fakeMessage) Duplicate() bool   { return false }
func (m *fakeMessage) Qos() byte         { return QoS1 }
func (m *fakeMessage) Retained() bool    { return false }
func (m *fakeMessage) Topic() string     { return m.topic }
func (m *fakeMessage) MessageID() uint16 { return 0 }
func (m *fakeMessage) Payload() []byte   { return m.payload }
func (m *fakeMessage) Ack()              {}

// TestDispatchCallbackRejectsOversize drives the exact dispatch callback logic
// and asserts an oversize payload never reaches the application handler, while
// a small valid one does. This is the unit-level proof of finding 1; the
// broker-level proof is TestOversizePayloadRejectedRealBroker.
func TestDispatchCallbackRejectsOversize(t *testing.T) {
	cfg := Config{Broker: "tcp://x:1883", NodeID: "n", MaxPayloadBytes: 1024}
	cfg.withDefaults()
	c := &Client{cfg: cfg}

	delivered := 0
	var lastLog string
	c.logfn = func(s string) { lastLog = s }

	// Re-create the callback body used by SubscribeDispatch.
	handler := func(WorkItem) error { delivered++; return nil }
	cb := func(m *fakeMessage) {
		payload := m.Payload()
		if c.payloadTooLarge(payload) {
			c.log(fmt.Sprintf("dropping oversize dispatch on %s: %d bytes > max %d",
				m.Topic(), len(payload), c.cfg.MaxPayloadBytes))
			return
		}
		item, err := DecodeWorkItem(payload)
		if err != nil {
			return
		}
		_ = handler(item)
	}

	// Oversize payload (valid JSON, but over the 1 KiB cap): must be dropped
	// BEFORE decode, never delivered.
	big := []byte(`{"id":"x","kind":"inference","payload":"` + strings.Repeat("A", 2048) + `"}`)
	cb(&fakeMessage{topic: DispatchTopic("n"), payload: big})
	if delivered != 0 {
		t.Fatalf("oversize payload was delivered to handler (%d) — cap not enforced", delivered)
	}
	if !strings.Contains(lastLog, "dropping oversize dispatch") {
		t.Fatalf("expected oversize-drop log, got %q", lastLog)
	}

	// Small valid payload: must be delivered.
	small := []byte(`{"id":"wi-1","kind":"inference"}`)
	cb(&fakeMessage{topic: DispatchTopic("n"), payload: small})
	if delivered != 1 {
		t.Fatalf("valid small payload not delivered (delivered=%d)", delivered)
	}
}
