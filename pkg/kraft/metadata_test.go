package kraft

import (
	"reflect"
	"testing"
)

// TestMetadataApplyDeterministic proves that two independent state copies that
// apply the SAME command sequence reach byte-identical state. This is the
// determinism property that lets every raft member converge from one log.
func TestMetadataApplyDeterministic(t *testing.T) {
	cmds := []command{
		{Kind: cmdCreateTopic, Topic: "b", Partitions: 2},
		{Kind: cmdCreateTopic, Topic: "a", Partitions: 3},
		{Kind: cmdAssignLeader, Topic: "a", Partition: 1, Leader: 7},
		{Kind: cmdAssignLeader, Topic: "b", Partition: 0, Leader: 4},
		// Replay of an existing topic is a no-op (idempotent).
		{Kind: cmdCreateTopic, Topic: "a", Partitions: 3},
	}
	s1, s2 := newMetadataState(), newMetadataState()
	for _, c := range cmds {
		if err := s1.apply(c); err != nil {
			t.Fatalf("s1.apply(%+v): %v", c, err)
		}
	}
	for _, c := range cmds {
		if err := s2.apply(c); err != nil {
			t.Fatalf("s2.apply(%+v): %v", c, err)
		}
	}
	if !reflect.DeepEqual(s1.listTopics(), s2.listTopics()) {
		t.Fatalf("states diverged:\n s1=%v\n s2=%v", s1.listTopics(), s2.listTopics())
	}

	got := s1.listTopics()
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" {
		t.Fatalf("listTopics not sorted/complete: %v", got)
	}
	if l := got[0].Leaders[1]; l != 7 {
		t.Fatalf("topic a partition 1 leader = %d, want 7", l)
	}
}

// TestMetadataApplyRejectsBadCommands guards the apply error branches.
func TestMetadataApplyRejectsBadCommands(t *testing.T) {
	s := newMetadataState()
	cases := []command{
		{Kind: cmdCreateTopic, Topic: "", Partitions: 1},
		{Kind: cmdCreateTopic, Topic: "x", Partitions: 0},
		{Kind: cmdAssignLeader, Topic: "missing", Partition: 0, Leader: 1},
		{Kind: commandKind(99), Topic: "x"},
	}
	for _, c := range cases {
		if err := s.apply(c); err == nil {
			t.Fatalf("apply(%+v) = nil, want error", c)
		}
	}
	// Out-of-range partition on an existing topic.
	if err := s.apply(command{Kind: cmdCreateTopic, Topic: "ok", Partitions: 1}); err != nil {
		t.Fatalf("setup create: %v", err)
	}
	if err := s.apply(command{Kind: cmdAssignLeader, Topic: "ok", Partition: 5, Leader: 1}); err == nil {
		t.Fatal("out-of-range partition accepted")
	}
}

// TestCommandRoundTrip proves encode/decode is lossless.
func TestCommandRoundTrip(t *testing.T) {
	in := command{Kind: cmdAssignLeader, Topic: "t", Partition: 2, Leader: 9}
	b, err := in.encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	out, err := decodeCommand(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip mismatch: in=%+v out=%+v", in, out)
	}
	if _, err := decodeCommand([]byte("not json")); err == nil {
		t.Fatal("decode of garbage succeeded")
	}
}
