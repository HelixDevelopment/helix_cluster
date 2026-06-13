package mqttclient

import (
	"encoding/json"
	"testing"
	"time"
)

func TestTopicConstruction(t *testing.T) {
	if got, want := DispatchTopic("node-7"), "helix/edge/node-7/dispatch"; got != want {
		t.Fatalf("DispatchTopic = %q, want %q", got, want)
	}
	if got, want := StatusTopic("node-7"), "helix/edge/node-7/status"; got != want {
		t.Fatalf("StatusTopic = %q, want %q", got, want)
	}
	if got, want := AllStatusTopic(), "helix/edge/+/status"; got != want {
		t.Fatalf("AllStatusTopic = %q, want %q", got, want)
	}
	if got, want := AllDispatchTopic(), "helix/edge/+/dispatch"; got != want {
		t.Fatalf("AllDispatchTopic = %q, want %q", got, want)
	}
}

func TestNodeIDFromTopic(t *testing.T) {
	cases := []struct {
		topic   string
		want    string
		wantErr bool
	}{
		{"helix/edge/node-7/status", "node-7", false},
		{"helix/edge/node-7/dispatch", "node-7", false},
		{"helix/edge/abc.123/status", "abc.123", false},
		{"helix/edge/+/status", "", true},
		{"helix/edge//status", "", true},
		{"helix/edge/node-7/bogus", "", true},
		{"other/edge/node-7/status", "", true},
		{"helix/edge/node-7", "", true},
		{"", "", true},
	}
	for _, tc := range cases {
		got, err := NodeIDFromTopic(tc.topic)
		if tc.wantErr {
			if err == nil {
				t.Errorf("NodeIDFromTopic(%q) expected error, got %q", tc.topic, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NodeIDFromTopic(%q) unexpected error: %v", tc.topic, err)
			continue
		}
		if got != tc.want {
			t.Errorf("NodeIDFromTopic(%q) = %q, want %q", tc.topic, got, tc.want)
		}
	}
}

func TestTopicRoundTrip(t *testing.T) {
	for _, node := range []string{"node-1", "edge-abc", "sbc.0042"} {
		dt := DispatchTopic(node)
		st := StatusTopic(node)
		if id, err := NodeIDFromTopic(dt); err != nil || id != node {
			t.Errorf("dispatch round-trip for %q: id=%q err=%v", node, id, err)
		}
		if id, err := NodeIDFromTopic(st); err != nil || id != node {
			t.Errorf("status round-trip for %q: id=%q err=%v", node, id, err)
		}
	}
}

func TestEncodeDecodeWorkItem(t *testing.T) {
	deadline := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	in := WorkItem{
		ID:       "wi-001",
		Kind:     "inference",
		Payload:  json.RawMessage(`{"model":"helix-7b","prompt":"hi"}`),
		Deadline: &deadline,
		IssuedAt: time.Date(2026, 6, 13, 11, 0, 0, 0, time.UTC),
	}
	b, err := EncodeWorkItem(in)
	if err != nil {
		t.Fatalf("EncodeWorkItem: %v", err)
	}
	out, err := DecodeWorkItem(b)
	if err != nil {
		t.Fatalf("DecodeWorkItem: %v", err)
	}
	if out.ID != in.ID || out.Kind != in.Kind {
		t.Fatalf("work item id/kind mismatch: got %+v", out)
	}
	if string(out.Payload) != string(in.Payload) {
		t.Fatalf("payload mismatch: got %s want %s", out.Payload, in.Payload)
	}
	if out.Deadline == nil || !out.Deadline.Equal(deadline) {
		t.Fatalf("deadline mismatch: got %v", out.Deadline)
	}
	if !out.IssuedAt.Equal(in.IssuedAt) {
		t.Fatalf("issued_at mismatch: got %v want %v", out.IssuedAt, in.IssuedAt)
	}
}

func TestEncodeWorkItemValidation(t *testing.T) {
	if _, err := EncodeWorkItem(WorkItem{Kind: "x"}); err == nil {
		t.Error("expected error encoding work item with no id")
	}
	if _, err := EncodeWorkItem(WorkItem{ID: "x"}); err == nil {
		t.Error("expected error encoding work item with no kind")
	}
}

func TestDecodeWorkItemValidation(t *testing.T) {
	if _, err := DecodeWorkItem([]byte("not json")); err == nil {
		t.Error("expected error decoding non-json")
	}
	if _, err := DecodeWorkItem([]byte(`{"kind":"x"}`)); err == nil {
		t.Error("expected error decoding work item with no id")
	}
	if _, err := DecodeWorkItem([]byte(`{"id":"x"}`)); err == nil {
		t.Error("expected error decoding work item with no kind")
	}
}

func TestEncodeDecodeStatus(t *testing.T) {
	in := Status{
		NodeID:         "node-7",
		State:          StateBusy,
		ActiveJobs:     3,
		LastWorkItemID: "wi-001",
		Timestamp:      time.Date(2026, 6, 13, 11, 30, 0, 0, time.UTC),
	}
	b, err := EncodeStatus(in)
	if err != nil {
		t.Fatalf("EncodeStatus: %v", err)
	}
	out, err := DecodeStatus(b)
	if err != nil {
		t.Fatalf("DecodeStatus: %v", err)
	}
	if out != in {
		t.Fatalf("status round-trip mismatch: got %+v want %+v", out, in)
	}
}

func TestStatusValidation(t *testing.T) {
	if _, err := EncodeStatus(Status{State: StateOnline}); err == nil {
		t.Error("expected error encoding status with no node_id")
	}
	if _, err := EncodeStatus(Status{NodeID: "n", State: "weird"}); err == nil {
		t.Error("expected error encoding status with invalid state")
	}
	if _, err := DecodeStatus([]byte(`{"node_id":"n","state":"weird"}`)); err == nil {
		t.Error("expected error decoding status with invalid state")
	}
	if _, err := DecodeStatus([]byte(`{"state":"online"}`)); err == nil {
		t.Error("expected error decoding status with no node_id")
	}
}

func TestNewValidation(t *testing.T) {
	if _, err := New(Config{NodeID: "n"}); err == nil {
		t.Error("expected error with no broker")
	}
	if _, err := New(Config{Broker: "tcp://x:1883"}); err == nil {
		t.Error("expected error with no node id")
	}
	c, err := New(Config{Broker: "tcp://x:1883", NodeID: "n"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.NodeID() != "n" {
		t.Errorf("NodeID = %q, want n", c.NodeID())
	}
}
