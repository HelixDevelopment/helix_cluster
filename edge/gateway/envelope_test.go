package gateway

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	payload := json.RawMessage(`{"task":"inference","model":"helix-7b"}`)
	in := Envelope{
		ID:       "msg-1",
		Kind:     KindDispatch,
		Source:   "scheduler",
		Dest:     "node-A",
		Payload:  payload,
		IssuedAt: time.Unix(1700000000, 0).UTC(),
	}
	b, err := EncodeEnvelope(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	out, err := DecodeEnvelope(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ID != in.ID || out.Kind != in.Kind || out.Dest != in.Dest || out.Source != in.Source {
		t.Fatalf("header mismatch: got %+v want %+v", out, in)
	}
	if !bytes.Equal(out.Payload, in.Payload) {
		t.Fatalf("payload not byte-equal: got %q want %q", out.Payload, in.Payload)
	}
}

func TestValidateRejectsMalformed(t *testing.T) {
	cases := []struct {
		name string
		env  Envelope
	}{
		{"no id", Envelope{Kind: KindDispatch, Dest: "node-A"}},
		{"bad kind", Envelope{ID: "x", Kind: "bogus", Dest: "node-A"}},
		{"dispatch no dest", Envelope{ID: "x", Kind: KindDispatch}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.env.Validate(); err == nil {
				t.Fatalf("expected validation error for %s", tc.name)
			}
			if _, err := EncodeEnvelope(tc.env); err == nil {
				t.Fatalf("expected encode to reject %s", tc.name)
			}
		})
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	if _, err := DecodeEnvelope([]byte("not json")); err == nil {
		t.Fatal("expected decode error on garbage")
	}
}

func TestRouteTopicDerivation(t *testing.T) {
	d := Envelope{ID: "1", Kind: KindDispatch, Dest: "node-7"}
	if got, want := d.RouteTopic(), "helix/edge/node-7/dispatch"; got != want {
		t.Fatalf("dispatch topic: got %q want %q", got, want)
	}
	s := Envelope{ID: "2", Kind: KindStatus, Dest: "node-7"}
	if got, want := s.RouteTopic(), "helix/edge/node-7/status"; got != want {
		t.Fatalf("status topic: got %q want %q", got, want)
	}
	explicit := Envelope{ID: "3", Kind: KindDispatch, Dest: "node-7", Topic: "custom/topic"}
	if got := explicit.RouteTopic(); got != "custom/topic" {
		t.Fatalf("explicit topic should win: got %q", got)
	}
}

func TestNodeFromTopic(t *testing.T) {
	cases := map[string]string{
		"helix/edge/node-A/dispatch": "node-A",
		"helix/edge/node-B/status":   "node-B",
		"unrelated/topic":            "",
		"helix/edge":                 "",
	}
	for topic, want := range cases {
		if got := nodeFromTopic(topic); got != want {
			t.Fatalf("nodeFromTopic(%q): got %q want %q", topic, got, want)
		}
	}
}
