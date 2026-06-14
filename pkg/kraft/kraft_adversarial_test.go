package kraft

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// snapshot renders the full observable metadata state into a comparable,
// order-independent form. listTopics already sorts topic names and returns
// per-partition leader maps, so reflect.DeepEqual over the slice is an exact
// equality oracle for two state machines: identical snapshot <=> converged.
func snapshot(s *metadataState) []TopicMetadata { return s.listTopics() }

// applyAll applies every command, failing the test on the first unexpected error.
func applyAll(t *testing.T, s *metadataState, cmds []command) {
	t.Helper()
	for i, c := range cmds {
		if err := s.apply(c); err != nil {
			t.Fatalf("apply[%d](%+v) unexpected error: %v", i, c, err)
		}
	}
}

// TestApplyDeterminismManyCommands is the centerpiece: a long, varied command
// sequence applied to two independent fresh state machines MUST yield
// byte-identical observable state (replicated-state-machine convergence). It
// exercises both command kinds, multiple topics, partition-0 / leader-0 edges,
// idempotent re-creates, and leader reassignment (last-writer-wins).
func TestApplyDeterminismManyCommands(t *testing.T) {
	cmds := []command{
		{Kind: cmdCreateTopic, Topic: "orders", Partitions: 3},
		{Kind: cmdCreateTopic, Topic: "events", Partitions: 1},
		{Kind: cmdCreateTopic, Topic: "audit", Partitions: 5},
		{Kind: cmdAssignLeader, Topic: "orders", Partition: 0, Leader: 0}, // leader 0 edge
		{Kind: cmdAssignLeader, Topic: "orders", Partition: 2, Leader: 11},
		{Kind: cmdAssignLeader, Topic: "events", Partition: 0, Leader: 4},
		{Kind: cmdAssignLeader, Topic: "audit", Partition: 4, Leader: 9},
		// idempotent re-create: identical definition is a no-op.
		{Kind: cmdCreateTopic, Topic: "events", Partitions: 1},
		// last-writer-wins leader reassignment.
		{Kind: cmdAssignLeader, Topic: "orders", Partition: 2, Leader: 22},
	}

	s1, s2 := newMetadataState(), newMetadataState()
	applyAll(t, s1, cmds)
	applyAll(t, s2, cmds)

	if !reflect.DeepEqual(snapshot(s1), snapshot(s2)) {
		t.Fatalf("two replicas diverged on identical log:\n s1=%v\n s2=%v", snapshot(s1), snapshot(s2))
	}

	// Pin the exact expected state so a silent mutation in apply is caught even
	// if BOTH replicas drift identically.
	got := snapshot(s1)
	if len(got) != 3 {
		t.Fatalf("want 3 topics, got %d: %v", len(got), got)
	}
	// Sorted: audit, events, orders.
	if got[0].Name != "audit" || got[1].Name != "events" || got[2].Name != "orders" {
		t.Fatalf("topics not sorted/complete: %v", got)
	}
	if got[2].Partitions != 3 {
		t.Fatalf("orders partitions = %d, want 3", got[2].Partitions)
	}
	// last-writer-wins: orders/2 must be 22, not 11.
	if l := got[2].Leaders[2]; l != 22 {
		t.Fatalf("orders partition 2 leader = %d, want 22 (last-writer-wins)", l)
	}
	// leader 0 must be RECORDED (present in map), distinct from "no leader".
	if l, ok := got[2].Leaders[0]; !ok || l != 0 {
		t.Fatalf("orders partition 0 leader = (%d,%v), want (0,true) — leader 0 must be recorded", l, ok)
	}
	// partition 1 of orders was never assigned: must be ABSENT.
	if l, ok := got[2].Leaders[1]; ok {
		t.Fatalf("orders partition 1 leader = %d present, want absent", l)
	}
}

// TestApplyDeterminismLeaderZeroVsAbsent isolates the leader-0 contract:
// partitionLeader must distinguish an explicitly-assigned leader 0 from a
// never-assigned partition. If apply ever dropped a leader-0 assignment, two
// replicas where one took the assignment and one did not would diverge.
func TestApplyDeterminismLeaderZeroVsAbsent(t *testing.T) {
	s := newMetadataState()
	applyAll(t, s, []command{
		{Kind: cmdCreateTopic, Topic: "t", Partitions: 2},
		{Kind: cmdAssignLeader, Topic: "t", Partition: 0, Leader: 0},
	})
	if l, ok := s.partitionLeader("t", 0); !ok || l != 0 {
		t.Fatalf("partitionLeader(t,0) = (%d,%v), want (0,true)", l, ok)
	}
	if l, ok := s.partitionLeader("t", 1); ok {
		t.Fatalf("partitionLeader(t,1) = (%d,true), want (_,false) — unassigned", l)
	}
}

// TestBadCommandRejectionNoMutation is the second centerpiece: each malformed /
// out-of-contract command MUST be rejected AND leave state byte-identical to
// before the attempt. A bad command that mutates state corrupts every replica.
func TestBadCommandRejectionNoMutation(t *testing.T) {
	base := newMetadataState()
	// Seed a valid topic so assign-leader range checks have something to chew on.
	applyAll(t, base, []command{{Kind: cmdCreateTopic, Topic: "ok", Partitions: 2}})
	before := snapshot(base)

	bad := []struct {
		name string
		cmd  command
	}{
		{"empty-topic-create", command{Kind: cmdCreateTopic, Topic: "", Partitions: 1}},
		{"zero-partitions", command{Kind: cmdCreateTopic, Topic: "z", Partitions: 0}},
		{"negative-partitions", command{Kind: cmdCreateTopic, Topic: "z", Partitions: -4}},
		{"assign-unknown-topic", command{Kind: cmdAssignLeader, Topic: "missing", Partition: 0, Leader: 1}},
		{"assign-empty-topic", command{Kind: cmdAssignLeader, Topic: "", Partition: 0, Leader: 1}},
		{"assign-partition-too-high", command{Kind: cmdAssignLeader, Topic: "ok", Partition: 2, Leader: 1}},
		{"assign-partition-at-bound", command{Kind: cmdAssignLeader, Topic: "ok", Partition: 5, Leader: 1}},
		{"assign-negative-partition", command{Kind: cmdAssignLeader, Topic: "ok", Partition: -1, Leader: 1}},
		{"unknown-kind", command{Kind: commandKind(99), Topic: "ok"}},
		{"zero-kind", command{}}, // zero-value command: Kind 0 is not a valid kind
	}

	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			err := base.apply(tc.cmd)
			if err == nil {
				t.Fatalf("apply(%+v) = nil, want rejection", tc.cmd)
			}
			if got := snapshot(base); !reflect.DeepEqual(got, before) {
				t.Fatalf("rejected command mutated state:\n before=%v\n after =%v", before, got)
			}
		})
	}
}

// TestAssignLeaderUnknownTopicDoesNotCreate proves a rejected assign-leader does
// not lazily create the topic or a leader map (a subtle corruption vector).
func TestAssignLeaderUnknownTopicDoesNotCreate(t *testing.T) {
	s := newMetadataState()
	if err := s.apply(command{Kind: cmdAssignLeader, Topic: "ghost", Partition: 0, Leader: 1}); err == nil {
		t.Fatal("assign-leader to unknown topic accepted")
	}
	if len(s.listTopics()) != 0 {
		t.Fatalf("ghost topic was created by rejected assign-leader: %v", s.listTopics())
	}
	if _, ok := s.partitionLeader("ghost", 0); ok {
		t.Fatal("leader map created for ghost topic")
	}
}

// TestApplyOrderSensitivityLeaders proves leader assignment is order-sensitive:
// the LAST committed assignment for a (topic,partition) wins. Two different
// orders that end on different assignments MUST produce different state.
func TestApplyOrderSensitivityLeaders(t *testing.T) {
	mk := func(last uint64) *metadataState {
		s := newMetadataState()
		applyAll(t, s, []command{
			{Kind: cmdCreateTopic, Topic: "t", Partitions: 1},
			{Kind: cmdAssignLeader, Topic: "t", Partition: 0, Leader: 1},
			{Kind: cmdAssignLeader, Topic: "t", Partition: 0, Leader: last},
		})
		return s
	}
	a, b := mk(2), mk(3)
	if reflect.DeepEqual(snapshot(a), snapshot(b)) {
		t.Fatal("leader assignment not order-sensitive: last-writer-wins broken")
	}
	if l, _ := a.partitionLeader("t", 0); l != 2 {
		t.Fatalf("a leader = %d, want 2", l)
	}
	if l, _ := b.partitionLeader("t", 0); l != 3 {
		t.Fatalf("b leader = %d, want 3", l)
	}
}

// TestApplyCreateOrderIndependence proves topic-create order does NOT matter for
// final state (the set of topics is the same regardless of insertion order) —
// the convergence guarantee for independent leaders applying in committed order.
func TestApplyCreateOrderIndependence(t *testing.T) {
	fwd := newMetadataState()
	rev := newMetadataState()
	applyAll(t, fwd, []command{
		{Kind: cmdCreateTopic, Topic: "a", Partitions: 1},
		{Kind: cmdCreateTopic, Topic: "b", Partitions: 2},
		{Kind: cmdCreateTopic, Topic: "c", Partitions: 3},
	})
	applyAll(t, rev, []command{
		{Kind: cmdCreateTopic, Topic: "c", Partitions: 3},
		{Kind: cmdCreateTopic, Topic: "b", Partitions: 2},
		{Kind: cmdCreateTopic, Topic: "a", Partitions: 1},
	})
	if !reflect.DeepEqual(snapshot(fwd), snapshot(rev)) {
		t.Fatalf("create order changed final state:\n fwd=%v\n rev=%v", snapshot(fwd), snapshot(rev))
	}
}

// TestCommandRoundTripExhaustive proves encode->decode is lossless across the
// fields that trigger the JSON omitempty trap (Partition:0, Leader:0). A dropped
// field would desync the proposing leader from the applying followers.
func TestCommandRoundTripExhaustive(t *testing.T) {
	cases := []command{
		{Kind: cmdCreateTopic, Topic: "t", Partitions: 1},
		{Kind: cmdCreateTopic, Topic: "unicode-✓-名", Partitions: 9},
		{Kind: cmdAssignLeader, Topic: "t", Partition: 0, Leader: 0}, // both omitempty zeros
		{Kind: cmdAssignLeader, Topic: "t", Partition: 0, Leader: 7},
		{Kind: cmdAssignLeader, Topic: "t", Partition: 7, Leader: 0},
		{Kind: cmdAssignLeader, Topic: "t", Partition: 2, Leader: 18446744073709551615}, // max uint64
	}
	for _, in := range cases {
		b, err := in.encode()
		if err != nil {
			t.Fatalf("encode(%+v): %v", in, err)
		}
		out, err := decodeCommand(b)
		if err != nil {
			t.Fatalf("decode(%s): %v", b, err)
		}
		if !reflect.DeepEqual(in, out) {
			t.Fatalf("round-trip lost a field: in=%+v out=%+v wire=%s", in, out, b)
		}
	}
}

// TestRoundTripWireSurvivesApply closes the loop: a command encoded, decoded,
// then applied yields the SAME state as applying the original command directly.
// This is the real raft path (propose-encode -> commit -> decode-apply).
func TestRoundTripWireSurvivesApply(t *testing.T) {
	seq := []command{
		{Kind: cmdCreateTopic, Topic: "t", Partitions: 4},
		{Kind: cmdAssignLeader, Topic: "t", Partition: 0, Leader: 0},
		{Kind: cmdAssignLeader, Topic: "t", Partition: 3, Leader: 5},
	}
	direct := newMetadataState()
	wired := newMetadataState()
	for _, c := range seq {
		if err := direct.apply(c); err != nil {
			t.Fatalf("direct apply %+v: %v", c, err)
		}
		b, err := c.encode()
		if err != nil {
			t.Fatalf("encode %+v: %v", c, err)
		}
		dc, err := decodeCommand(b)
		if err != nil {
			t.Fatalf("decode %s: %v", b, err)
		}
		if err := wired.apply(dc); err != nil {
			t.Fatalf("wired apply %+v: %v", dc, err)
		}
	}
	if !reflect.DeepEqual(snapshot(direct), snapshot(wired)) {
		t.Fatalf("wire path diverged from direct path:\n direct=%v\n wired=%v", snapshot(direct), snapshot(wired))
	}
}

// TestDecodeHostileBytesNoPanic proves decodeCommand returns an error (never
// panics) on truncated / garbage / structurally-hostile input — a decode panic
// on attacker-controlled log bytes is a DoS.
func TestDecodeHostileBytesNoPanic(t *testing.T) {
	hostile := [][]byte{
		nil,
		{},
		[]byte("not json"),
		[]byte("{"),
		[]byte("[1,2,3]"),                 // wrong top-level type
		[]byte(`{"kind":"not-a-number"}`), // type mismatch on kind
		[]byte(`{"partitions":"oops"}`),   // type mismatch on int
		[]byte(`{"leader":-1}`),           // negative into uint64
		[]byte(`{"kind":99999999999999999999999999999999}`), // overflow
		[]byte("\x00\x01\x02\xff\xfe"),                      // raw binary
		[]byte(`{"topic":` + string(make([]byte, 0)) + `}`), // dangling
	}
	for i, data := range hostile {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("decodeCommand(hostile[%d]=%q) PANICKED: %v", i, data, r)
				}
			}()
			// We do not require an error for every case (e.g. {} decodes to a
			// zero command, which apply then rejects); we require NO panic.
			_, _ = decodeCommand(data)
		}()
	}
	// And the clearly-invalid ones MUST error.
	mustErr := [][]byte{[]byte("not json"), []byte("{"), []byte("[1,2,3]"), []byte(`{"kind":"x"}`)}
	for i, data := range mustErr {
		if _, err := decodeCommand(data); err == nil {
			t.Fatalf("decodeCommand(mustErr[%d]=%q) = nil error, want error", i, data)
		}
	}
}

// TestDecodeZeroCommandRejectedByApply proves the defense-in-depth seam: an empty
// JSON object decodes successfully to a zero-value command, but apply then
// REJECTS it (kind 0 is not valid) rather than silently mutating state.
func TestDecodeZeroCommandRejectedByApply(t *testing.T) {
	c, err := decodeCommand([]byte("{}"))
	if err != nil {
		t.Fatalf("decode {}: %v", err)
	}
	if c.Kind != 0 {
		t.Fatalf("zero command kind = %d, want 0", c.Kind)
	}
	s := newMetadataState()
	if err := s.apply(c); err == nil {
		t.Fatal("apply(zero command) = nil, want rejection of unknown kind 0")
	}
	if len(s.listTopics()) != 0 {
		t.Fatalf("zero command mutated state: %v", s.listTopics())
	}
}

// TestEmptyLogYieldsEmptyState proves a fresh state machine that applied no
// commands reports empty, deterministic state (baseline for convergence).
func TestEmptyLogYieldsEmptyState(t *testing.T) {
	a, b := newMetadataState(), newMetadataState()
	if !reflect.DeepEqual(snapshot(a), snapshot(b)) {
		t.Fatal("two empty states differ")
	}
	if len(snapshot(a)) != 0 {
		t.Fatalf("empty state lists %d topics", len(snapshot(a)))
	}
}

// TestListTopicsLeaderMapIsCopy proves listTopics returns a defensive copy of the
// leaders map: mutating the returned snapshot must not corrupt internal state
// (otherwise a reader could desync this replica from its peers).
func TestListTopicsLeaderMapIsCopy(t *testing.T) {
	s := newMetadataState()
	applyAll(t, s, []command{
		{Kind: cmdCreateTopic, Topic: "t", Partitions: 1},
		{Kind: cmdAssignLeader, Topic: "t", Partition: 0, Leader: 7},
	})
	got := s.listTopics()
	got[0].Leaders[0] = 999      // mutate the snapshot
	got[0].Leaders[123456] = 999 // inject a bogus partition
	if l, _ := s.partitionLeader("t", 0); l != 7 {
		t.Fatalf("internal leader corrupted by snapshot mutation: got %d, want 7", l)
	}
	if _, ok := s.partitionLeader("t", 123456); ok {
		t.Fatal("snapshot mutation injected a partition into internal state")
	}
}

// sanity: ensure encode produces valid JSON (guards against a future binary swap
// that would silently change the wire contract decode still parses).
func TestEncodeProducesJSON(t *testing.T) {
	b, err := command{Kind: cmdCreateTopic, Topic: "t", Partitions: 1}.encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !json.Valid(b) {
		t.Fatalf("encode produced invalid JSON: %s", b)
	}
}

// guard: keep errors import meaningful even if other assertions change.
var _ = errors.Is
