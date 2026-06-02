package kraft

import (
	"context"
	"testing"
)

// settle ticks the quorum enough times to let any pending election and
// replication complete, then runs the Ready loop to flush commitment. It uses
// real ticks (no fakery): with electionTick=10 a handful of ticks is plenty to
// elect a leader and replicate proposals across the in-process transport.
func settle(q *MetadataQuorum) {
	for i := 0; i < 50; i++ {
		q.Tick()
	}
	q.Run()
}

// electController campaigns a specific member and settles until that member (or
// some member) is the controller. Returns the elected controller id. Fails the
// test if no controller emerges, which would mean the raft election did not run.
func electController(t *testing.T, q *MetadataQuorum, candidate uint64) uint64 {
	t.Helper()
	if err := q.Campaign(candidate); err != nil {
		t.Fatalf("Campaign(%d): %v", candidate, err)
	}
	settle(q)
	id := q.ControllerID()
	if id == 0 {
		t.Fatalf("no controller elected after campaign by %d", candidate)
	}
	return id
}

// TestCreateTopicReplicatesToEveryMember is closure criterion #1: with NO
// ZooKeeper / external process, CreateTopic replicates through the quorum and
// ListTopics on EVERY member reflects it.
//
// MUTATION GUARD: if CreateTopic mutated only local state instead of proposing
// through raft, the topic would appear (at most) on one member and the
// "reflected on every member" assertion below would fail.
func TestCreateTopicReplicatesToEveryMember(t *testing.T) {
	q, err := NewMetadataQuorum([]uint64{1, 2, 3})
	if err != nil {
		t.Fatalf("NewMetadataQuorum: %v", err)
	}
	electController(t, q, 1)

	if err := q.CreateTopic(context.Background(), "orders", 4); err != nil {
		t.Fatalf("CreateTopic: %v", err)
	}
	if err := q.CreateTopic(context.Background(), "events", 2); err != nil {
		t.Fatalf("CreateTopic: %v", err)
	}
	settle(q)

	// Assert sink-side: every member's applied state reflects both topics.
	for _, id := range []uint64{1, 2, 3} {
		topics, err := q.ListTopics(id)
		if err != nil {
			t.Fatalf("ListTopics(%d): %v", id, err)
		}
		got := map[string]int{}
		for _, tm := range topics {
			got[tm.Name] = tm.Partitions
		}
		if got["orders"] != 4 {
			t.Fatalf("member %d: orders partitions = %d, want 4 (topics=%v)", id, got["orders"], got)
		}
		if got["events"] != 2 {
			t.Fatalf("member %d: events partitions = %d, want 2 (topics=%v)", id, got["events"], got)
		}
		if len(got) != 2 {
			t.Fatalf("member %d: got %d topics, want 2: %v", id, len(got), got)
		}
	}

	// Sink-side proof of replication: every member committed exactly 2 normal
	// metadata entries through the raft log.
	for _, id := range []uint64{1, 2, 3} {
		n, err := q.CommittedCount(id)
		if err != nil {
			t.Fatalf("CommittedCount(%d): %v", id, err)
		}
		if n != 2 {
			t.Fatalf("member %d committed %d entries, want 2", id, n)
		}
	}
}

// TestControllerElectionAndPartitionLeaderRecorded is closure criterion #2: a
// controller election occurs and a partition-leader assignment is recorded in the
// replicated log and visible cluster-wide.
func TestControllerElectionAndPartitionLeaderRecorded(t *testing.T) {
	q, err := NewMetadataQuorum([]uint64{1, 2, 3})
	if err != nil {
		t.Fatalf("NewMetadataQuorum: %v", err)
	}

	// Before any election there is no controller.
	if id := q.ControllerID(); id != 0 {
		t.Fatalf("controller elected before any campaign: %d", id)
	}
	ctrl := electController(t, q, 2)
	t.Logf("elected controller = %d", ctrl)

	if err := q.CreateTopic(context.Background(), "telemetry", 3); err != nil {
		t.Fatalf("CreateTopic: %v", err)
	}
	settle(q)

	// Assign partition 1's leader to member 3 through the replicated log.
	if err := q.AssignPartitionLeader(context.Background(), "telemetry", 1, 3); err != nil {
		t.Fatalf("AssignPartitionLeader: %v", err)
	}
	settle(q)

	// Visible cluster-wide: every live member sees partition 1 led by member 3.
	for _, id := range []uint64{1, 2, 3} {
		leader, ok, err := q.PartitionLeader(id, "telemetry", 1)
		if err != nil {
			t.Fatalf("PartitionLeader(%d): %v", id, err)
		}
		if !ok {
			t.Fatalf("member %d: partition 1 has no recorded leader", id)
		}
		if leader != 3 {
			t.Fatalf("member %d: partition 1 leader = %d, want 3", id, leader)
		}
	}

	// An unassigned partition must NOT report a leader (guards against the apply
	// path inventing assignments).
	if _, ok, _ := q.PartitionLeader(1, "telemetry", 0); ok {
		t.Fatalf("partition 0 unexpectedly has a leader")
	}
}

// TestControllerLossNewControllerElectedAndOpsResume is closure criterion #3:
// stop the controller node, show a new controller is elected and metadata ops
// resume on the surviving quorum.
func TestControllerLossNewControllerElectedAndOpsResume(t *testing.T) {
	q, err := NewMetadataQuorum([]uint64{1, 2, 3})
	if err != nil {
		t.Fatalf("NewMetadataQuorum: %v", err)
	}
	first := electController(t, q, 1)

	if err := q.CreateTopic(context.Background(), "before", 1); err != nil {
		t.Fatalf("CreateTopic before failover: %v", err)
	}
	settle(q)

	// Kill the controller — simulates controller loss with NO external service to
	// fall back on; the surviving raft quorum must self-heal.
	stopped := q.StopController()
	if stopped != first {
		t.Fatalf("StopController returned %d, expected current controller %d", stopped, first)
	}

	// Drive elections on the surviving members until a NEW controller emerges.
	var newCtrl uint64
	for i := 0; i < 200 && newCtrl == 0; i++ {
		q.Tick()
		if c := q.ControllerID(); c != 0 && c != stopped {
			newCtrl = c
		}
	}
	q.Run()
	if newCtrl == 0 {
		t.Fatalf("no new controller elected after controller loss")
	}
	if newCtrl == stopped {
		t.Fatalf("stopped controller %d still reported as controller", stopped)
	}
	t.Logf("new controller after failover = %d", newCtrl)

	// Metadata ops resume: a new CreateTopic commits on the surviving quorum.
	if err := q.CreateTopic(context.Background(), "after", 2); err != nil {
		t.Fatalf("CreateTopic after failover: %v", err)
	}
	settle(q)

	// Both surviving members reflect both topics.
	for _, id := range q.MemberIDs() {
		topics, err := q.ListTopics(id)
		if err != nil {
			t.Fatalf("ListTopics(%d): %v", id, err)
		}
		names := map[string]bool{}
		for _, tm := range topics {
			names[tm.Name] = true
		}
		if !names["before"] {
			t.Fatalf("member %d lost pre-failover topic 'before': %v", id, names)
		}
		if !names["after"] {
			t.Fatalf("member %d missing post-failover topic 'after': %v", id, names)
		}
	}
}

// TestWriteWithoutControllerRejected proves a metadata write before any election
// is honestly refused rather than silently lost. This also guards the
// ErrNoController branch in propose.
func TestWriteWithoutControllerRejected(t *testing.T) {
	q, err := NewMetadataQuorum([]uint64{1, 2, 3})
	if err != nil {
		t.Fatalf("NewMetadataQuorum: %v", err)
	}
	err = q.CreateTopic(context.Background(), "x", 1)
	if err == nil {
		t.Fatalf("CreateTopic without controller succeeded, want ErrNoController")
	}
	// No member should have committed anything.
	for _, id := range []uint64{1, 2, 3} {
		n, _ := q.CommittedCount(id)
		if n != 0 {
			t.Fatalf("member %d committed %d entries with no controller, want 0", id, n)
		}
	}
}

// TestAssignLeaderValidation guards the input-validation branches of
// AssignPartitionLeader (unknown topic, out-of-range partition, non-member leader).
func TestAssignLeaderValidation(t *testing.T) {
	q, err := NewMetadataQuorum([]uint64{1, 2, 3})
	if err != nil {
		t.Fatalf("NewMetadataQuorum: %v", err)
	}
	electController(t, q, 1)
	if err := q.CreateTopic(context.Background(), "t", 2); err != nil {
		t.Fatalf("CreateTopic: %v", err)
	}
	settle(q)

	cases := []struct {
		name      string
		topic     string
		partition int
		leader    uint64
	}{
		{"unknown topic", "nope", 0, 1},
		{"partition out of range", "t", 5, 1},
		{"negative partition", "t", -1, 1},
		{"non-member leader", "t", 0, 99},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := q.AssignPartitionLeader(context.Background(), tc.topic, tc.partition, tc.leader); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}

// TestConstructorValidation guards the constructor input checks.
func TestConstructorValidation(t *testing.T) {
	if _, err := NewMetadataQuorum(nil); err == nil {
		t.Fatal("empty peers accepted")
	}
	if _, err := NewMetadataQuorum([]uint64{1, 1, 2}); err == nil {
		t.Fatal("duplicate peers accepted")
	}
	if _, err := NewMetadataQuorum([]uint64{0, 1}); err == nil {
		t.Fatal("zero member id accepted")
	}
	if _, err := NewMetadataQuorum([]uint64{1}); err != nil {
		t.Fatalf("single-member quorum rejected: %v", err)
	}
}
