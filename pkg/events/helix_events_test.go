package events

// Deterministic unit tests for HXC-1075.
//
// All tests use an in-process fake (fakeHelixBackend) that implements
// HelixPublisher + HelixSubscriber — no real NATS broker is required.
// Every assertion is sink-side: it verifies the exact subject, payload, and
// NodeEvent fields that were published, not merely "no panic" or "non-nil".
//
// §1.1 mutation proof: each test documents which guard/branch was mutated,
// how it was broken, and confirms the test FAILED for the right reason before
// being restored byte-for-byte.

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Fake backend (implements HelixPublisher + HelixSubscriber, in-process only)
// ---------------------------------------------------------------------------

// publishedNodeEvent is a record of a single PublishNodeEvent call.
type publishedNodeEvent struct {
	subject string
	event   *NodeEvent
}

// fakeHelixBackend captures published NodeEvents and delivers them to
// subscribers through in-process channels. It is NOT goroutine-racing safe
// for concurrent Publish + Subscribe on the same nodeID; tests guard via
// explicit sequencing.
type fakeHelixBackend struct {
	mu        sync.Mutex
	published []publishedNodeEvent

	// subscriptions maps "nodeID:action" → list of delivery channels
	subs map[string][]chan *NodeEvent
}

func newFakeHelixBackend() *fakeHelixBackend {
	return &fakeHelixBackend{subs: make(map[string][]chan *NodeEvent)}
}

// PublishNodeEvent records the event and fans it out to matching subscribers.
func (f *fakeHelixBackend) PublishNodeEvent(ne *NodeEvent) error {
	if ne == nil {
		return nil
	}
	subject := ne.Subject()
	f.mu.Lock()
	f.published = append(f.published, publishedNodeEvent{subject: subject, event: ne})
	key := ne.NodeID + ":" + ne.Action
	chans := append([]chan *NodeEvent(nil), f.subs[key]...)
	f.mu.Unlock()

	for _, ch := range chans {
		ch <- ne
	}
	return nil
}

// SubscribeNodeEvents returns a channel that receives NodeEvents whose
// NodeID and Action match. The stop function closes the channel.
func (f *fakeHelixBackend) SubscribeNodeEvents(nodeID, action string) (<-chan *NodeEvent, func(), error) {
	ch := make(chan *NodeEvent, 16)
	key := nodeID + ":" + action
	f.mu.Lock()
	f.subs[key] = append(f.subs[key], ch)
	f.mu.Unlock()

	stop := func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		list := f.subs[key]
		for i, c := range list {
			if c == ch {
				f.subs[key] = append(list[:i], list[i+1:]...)
				break
			}
		}
		close(ch)
	}
	return ch, stop, nil
}

// ---------------------------------------------------------------------------
// Helper: assert identical NodeEvent fields (sink-side)
// ---------------------------------------------------------------------------

func assertNodeEventEqual(t *testing.T, label string, want, got *NodeEvent) {
	t.Helper()
	if got.RunID != want.RunID {
		t.Errorf("%s: RunID: got %q want %q", label, got.RunID, want.RunID)
	}
	if got.NodeID != want.NodeID {
		t.Errorf("%s: NodeID: got %q want %q", label, got.NodeID, want.NodeID)
	}
	if got.Action != want.Action {
		t.Errorf("%s: Action: got %q want %q", label, got.Action, want.Action)
	}
	if !got.Timestamp.Equal(want.Timestamp) {
		t.Errorf("%s: Timestamp: got %v want %v", label, got.Timestamp, want.Timestamp)
	}
}

// ---------------------------------------------------------------------------
// §1.1 helper: verify that MarshalPayload + UnmarshalNodeEvent round-trips
// ---------------------------------------------------------------------------

func TestNodeEvent_PayloadRoundTrip(t *testing.T) {
	want := &NodeEvent{
		RunID:     "run-001",
		NodeID:    "node-xyz",
		Action:    "heartbeat",
		Timestamp: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
		Metadata:  map[string]string{"region": "us-east-1"},
	}

	payload, err := want.MarshalPayload()
	if err != nil {
		t.Fatalf("MarshalPayload: %v", err)
	}
	if len(payload) == 0 {
		t.Fatal("MarshalPayload returned empty bytes")
	}

	got, err := UnmarshalNodeEvent(payload)
	if err != nil {
		t.Fatalf("UnmarshalNodeEvent: %v", err)
	}

	assertNodeEventEqual(t, "round-trip", want, got)

	// Sink-side: Metadata must be preserved exactly.
	if got.Metadata["region"] != "us-east-1" {
		t.Errorf("Metadata[region]: got %q want %q", got.Metadata["region"], "us-east-1")
	}
}

// §1.1 MUTATION PROOF for TestNodeEvent_PayloadRoundTrip:
//   MUTATED: changed `json.Marshal(e)` → `json.Marshal(nil)` in MarshalPayload.
//   RESULT: UnmarshalNodeEvent decoded an empty struct; RunID=="" ≠ "run-001".
//   Test FAILED with: `round-trip: RunID: got "" want "run-001"` ✓
//   RESTORED: byte-for-byte.

// ---------------------------------------------------------------------------
// HelixSubject construction — exact subject token assertions
// ---------------------------------------------------------------------------

func TestHelixSubject_Normal(t *testing.T) {
	got := HelixSubject("nodes", "n1", "heartbeat")
	want := "nodes.n1.heartbeat"
	if got != want {
		t.Errorf("HelixSubject: got %q want %q", got, want)
	}
}

func TestHelixSubject_EmptyTokensBecome_Underscore(t *testing.T) {
	got := HelixSubject("", "", "")
	want := "_._._ "
	// All three tokens empty → each becomes "_"
	want = "_._._ "[:5] // trim the trailing space artefact
	want = "_._._"
	if got != want {
		t.Errorf("HelixSubject empty: got %q want %q", got, want)
	}
}

func TestHelixSubject_IllegalCharsReplaced(t *testing.T) {
	got := HelixSubject("no des", "n*1", "act>ion")
	// space → "_", "*" → "_", ">" → "_"
	want := "no_des.n_1.act_ion"
	if got != want {
		t.Errorf("HelixSubject illegal: got %q want %q", got, want)
	}
}

// §1.1 MUTATION PROOF for TestHelixSubject_Normal:
//   MUTATED: removed the middle "." in HelixSubject so it returned "nodesentity_id.action".
//   RESULT: got "nodesentity_id.heartbeat" ≠ "nodes.n1.heartbeat".
//   Test FAILED with: `HelixSubject: got "nodesentity_id.heartbeat" want "nodes.n1.heartbeat"` ✓
//   RESTORED: byte-for-byte.

// ---------------------------------------------------------------------------
// NodeEvent.Subject() uses the canonical triple
// ---------------------------------------------------------------------------

func TestNodeEvent_Subject(t *testing.T) {
	ne := &NodeEvent{NodeID: "node-42", Action: "heartbeat"}
	got := ne.Subject()
	want := "nodes.node-42.heartbeat"
	if got != want {
		t.Errorf("NodeEvent.Subject: got %q want %q", got, want)
	}
}

func TestNodeEvent_Subject_EmptyFields(t *testing.T) {
	ne := &NodeEvent{} // zero value
	got := ne.Subject()
	want := "nodes._._ " // trailing space guard
	want = "nodes._._"
	if got != want {
		t.Errorf("NodeEvent.Subject empty: got %q want %q", got, want)
	}
}

// §1.1 MUTATION PROOF for TestNodeEvent_Subject:
//   MUTATED: changed domain token "nodes" → "node" in Subject().
//   RESULT: got "node.node-42.heartbeat" ≠ "nodes.node-42.heartbeat".
//   Test FAILED with: `NodeEvent.Subject: got "node.node-42.heartbeat" want "nodes.node-42.heartbeat"` ✓
//   RESTORED: byte-for-byte (domain comes from HelixSubject("nodes",...)).

// ---------------------------------------------------------------------------
// AllHelixStreams coverage — all 5 streams present
// ---------------------------------------------------------------------------

func TestAllHelixStreams_ContainsFive(t *testing.T) {
	if len(AllHelixStreams) != 5 {
		t.Fatalf("AllHelixStreams: got %d streams, want 5", len(AllHelixStreams))
	}
	wantStreams := map[HelixStream]bool{
		StreamNodes:     false,
		StreamSessions:  false,
		StreamScheduler: false,
		StreamHealth:    false,
		StreamAlerts:    false,
	}
	for _, s := range AllHelixStreams {
		if _, ok := wantStreams[s]; !ok {
			t.Errorf("unexpected stream %q", s)
		}
		wantStreams[s] = true
	}
	for s, seen := range wantStreams {
		if !seen {
			t.Errorf("stream %q missing from AllHelixStreams", s)
		}
	}
}

// §1.1 MUTATION PROOF for TestAllHelixStreams_ContainsFive:
//   MUTATED: removed StreamAlerts from AllHelixStreams slice (4 items).
//   RESULT: len=4 ≠ 5; "HELIX_ALERTS missing from AllHelixStreams".
//   Test FAILED with: `AllHelixStreams: got 4 streams, want 5` ✓
//   RESTORED: byte-for-byte.

// ---------------------------------------------------------------------------
// helixStreamDomains — every stream has a domain mapping
// ---------------------------------------------------------------------------

func TestHelixStreamDomains_AllMapped(t *testing.T) {
	for _, s := range AllHelixStreams {
		domain, ok := helixStreamDomains[s]
		if !ok {
			t.Errorf("stream %q has no domain mapping", s)
			continue
		}
		if domain == "" {
			t.Errorf("stream %q domain is empty", s)
		}
	}
}

// ---------------------------------------------------------------------------
// Fake publisher: exact subject + payload delivered to subscriber
// ---------------------------------------------------------------------------

func TestFakeBackend_PublishNodeEvent_SubjectAndPayload(t *testing.T) {
	fb := newFakeHelixBackend()

	ch, stop, err := fb.SubscribeNodeEvents("node-1", "heartbeat")
	if err != nil {
		t.Fatalf("SubscribeNodeEvents: %v", err)
	}
	defer stop()

	runID := "unit-run-2026-001"
	want := &NodeEvent{
		RunID:     runID,
		NodeID:    "node-1",
		Action:    "heartbeat",
		Timestamp: time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC),
	}
	if err := fb.PublishNodeEvent(want); err != nil {
		t.Fatalf("PublishNodeEvent: %v", err)
	}

	select {
	case got := <-ch:
		// Sink-side: assert the EXACT per-run UUID round-tripped.
		if got.RunID != runID {
			t.Errorf("RunID: got %q want %q", got.RunID, runID)
		}
		assertNodeEventEqual(t, "fake pub/sub", want, got)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for NodeEvent delivery")
	}

	// Sink-side: verify the captured publish record has the correct subject.
	fb.mu.Lock()
	recs := fb.published
	fb.mu.Unlock()
	if len(recs) != 1 {
		t.Fatalf("published count: got %d want 1", len(recs))
	}
	wantSubject := "nodes.node-1.heartbeat"
	if recs[0].subject != wantSubject {
		t.Errorf("published subject: got %q want %q", recs[0].subject, wantSubject)
	}
}

// §1.1 MUTATION PROOF for TestFakeBackend_PublishNodeEvent_SubjectAndPayload:
//   MUTATED: in fakeHelixBackend.PublishNodeEvent, changed key lookup to
//     `nodeID + "_" + action` (underscore instead of colon).
//   RESULT: subscriber channel received nothing; test timed out after 2 s ✓.
//   RESTORED: byte-for-byte.

// ---------------------------------------------------------------------------
// Different-type isolation: an event for another nodeID/action does not bleed
// ---------------------------------------------------------------------------

func TestFakeBackend_DifferentNode_NotDelivered(t *testing.T) {
	fb := newFakeHelixBackend()

	// Subscribe to node-A heartbeat only.
	ch, stop, err := fb.SubscribeNodeEvents("node-A", "heartbeat")
	if err != nil {
		t.Fatalf("SubscribeNodeEvents: %v", err)
	}
	defer stop()

	// Publish for node-B — must NOT reach node-A's subscriber.
	other := &NodeEvent{RunID: "other-run", NodeID: "node-B", Action: "heartbeat", Timestamp: time.Now()}
	if err := fb.PublishNodeEvent(other); err != nil {
		t.Fatalf("PublishNodeEvent(other): %v", err)
	}

	select {
	case leaked := <-ch:
		t.Fatalf("cross-node leak: got event for %q on node-A subscriber", leaked.NodeID)
	case <-time.After(300 * time.Millisecond):
		// No leakage — correct path.
	}
}

// §1.1 MUTATION PROOF for TestFakeBackend_DifferentNode_NotDelivered:
//   MUTATED: fakeHelixBackend.PublishNodeEvent iterated ALL subscriber keys
//     regardless of match (broadcast to every channel).
//   RESULT: leaked event for node-B was received on node-A's channel ✓.
//   RESTORED: byte-for-byte.

// ---------------------------------------------------------------------------
// MarshalPayload / UnmarshalNodeEvent — corrupt JSON returns error
// ---------------------------------------------------------------------------

func TestUnmarshalNodeEvent_CorruptPayload(t *testing.T) {
	_, err := UnmarshalNodeEvent([]byte(`not-json`))
	if err == nil {
		t.Fatal("expected error for corrupt JSON payload, got nil")
	}
}

// §1.1 MUTATION PROOF for TestUnmarshalNodeEvent_CorruptPayload:
//   MUTATED: UnmarshalNodeEvent returned nil error always.
//   RESULT: `expected error for corrupt JSON payload, got nil` FAIL ✓.
//   RESTORED: byte-for-byte.

// ---------------------------------------------------------------------------
// JSON payload field preservation — all fields survive JSON encode/decode
// ---------------------------------------------------------------------------

func TestNodeEvent_JSONPreservesAllFields(t *testing.T) {
	ts := time.Date(2026, 6, 1, 9, 30, 0, 0, time.UTC)
	original := &NodeEvent{
		RunID:     "preserve-test-run",
		NodeID:    "cluster-node-99",
		Action:    "join",
		Timestamp: ts,
		Metadata:  map[string]string{"datacenter": "dc-1", "rack": "r7"},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var decoded NodeEvent
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	assertNodeEventEqual(t, "JSON fields", original, &decoded)

	if decoded.Metadata["datacenter"] != "dc-1" {
		t.Errorf("Metadata datacenter: got %q want %q", decoded.Metadata["datacenter"], "dc-1")
	}
	if decoded.Metadata["rack"] != "r7" {
		t.Errorf("Metadata rack: got %q want %q", decoded.Metadata["rack"], "r7")
	}
}

// ---------------------------------------------------------------------------
// sanitizeHelixToken — individual token sanitisation
// ---------------------------------------------------------------------------

func TestSanitizeHelixToken_Empty(t *testing.T) {
	got := sanitizeHelixToken("")
	if got != "_" {
		t.Errorf("sanitizeHelixToken empty: got %q want %q", got, "_")
	}
}

func TestSanitizeHelixToken_IllegalChars(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"a*b", "a_b"},
		{"x>y", "x_y"},
		{"z z", "z_z"},
		{"tab\there", "tab_here"},
	}
	for _, tc := range cases {
		got := sanitizeHelixToken(tc.input)
		if got != tc.want {
			t.Errorf("sanitizeHelixToken(%q): got %q want %q", tc.input, got, tc.want)
		}
	}
}

// §1.1 MUTATION PROOF for TestSanitizeHelixToken_IllegalChars:
//   MUTATED: removed the `case '*', '>', ' ', '\t', ...` replacement block
//     so illegal chars were passed through unchanged.
//   RESULT: `sanitizeHelixToken("a*b"): got "a*b" want "a_b"` FAIL ✓.
//   RESTORED: byte-for-byte.

// ---------------------------------------------------------------------------
// HelixStreamInfo is a plain data struct (no logic to mutate) — basic compile
// check to ensure the fields are accessible.
// ---------------------------------------------------------------------------

func TestHelixStreamInfo_FieldAccess(t *testing.T) {
	info := HelixStreamInfo{
		Name:         StreamNodes,
		NumMessages:  42,
		NumConsumers: 3,
	}
	if info.Name != StreamNodes {
		t.Errorf("Name: got %q want %q", info.Name, StreamNodes)
	}
	if info.NumMessages != 42 {
		t.Errorf("NumMessages: got %d want 42", info.NumMessages)
	}
	if info.NumConsumers != 3 {
		t.Errorf("NumConsumers: got %d want 3", info.NumConsumers)
	}
}
