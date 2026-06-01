package etcd

import (
	"strings"
	"testing"
)

// TestNodeStatusKey_ExactString proves NodeStatusKey produces EXACTLY the
// documented key /clusteros/nodes/<uuid>/status.
//
// §1.1 mutation target: change "status" suffix → "state" → assertion fails.
func TestNodeStatusKey_ExactString(t *testing.T) {
	const uuid = "n1"
	got := NodeStatusKey(uuid)
	want := "/clusteros/nodes/n1/status"
	if got != want {
		t.Fatalf("NodeStatusKey(%q) = %q; want %q", uuid, got, want)
	}
	// Structural check: exactly 4 slash-separated parts after root.
	parts := strings.Split(strings.TrimPrefix(got, "/"), "/")
	// ["clusteros","nodes","n1","status"] = 4 parts
	if len(parts) != 4 {
		t.Fatalf("NodeStatusKey: want 4 path segments, got %d: %v", len(parts), parts)
	}
	if parts[3] != "status" {
		t.Fatalf("NodeStatusKey: last segment = %q, want \"status\"", parts[3])
	}
}

// TestNodeHeartbeatKey_ExactString proves NodeHeartbeatKey produces EXACTLY the
// documented key /clusteros/nodes/<uuid>/heartbeat.
//
// §1.1 mutation target: change "heartbeat" suffix → "hb" → assertion fails.
func TestNodeHeartbeatKey_ExactString(t *testing.T) {
	const uuid = "n1"
	got := NodeHeartbeatKey(uuid)
	want := "/clusteros/nodes/n1/heartbeat"
	if got != want {
		t.Fatalf("NodeHeartbeatKey(%q) = %q; want %q", uuid, got, want)
	}
	parts := strings.Split(strings.TrimPrefix(got, "/"), "/")
	if len(parts) != 4 {
		t.Fatalf("NodeHeartbeatKey: want 4 path segments, got %d: %v", len(parts), parts)
	}
	if parts[3] != "heartbeat" {
		t.Fatalf("NodeHeartbeatKey: last segment = %q, want \"heartbeat\"", parts[3])
	}
}

// TestSessionRoutingKey_ExactString proves SessionRoutingKey produces EXACTLY
// the documented key /clusteros/sessions/<sessionID>/routing.
//
// §1.1 mutation target: change "routing" suffix → "route" → assertion fails.
func TestSessionRoutingKey_ExactString(t *testing.T) {
	const sid = "sess-001"
	got := SessionRoutingKey(sid)
	want := "/clusteros/sessions/sess-001/routing"
	if got != want {
		t.Fatalf("SessionRoutingKey(%q) = %q; want %q", sid, got, want)
	}
	if !strings.Contains(got, "/sessions/") {
		t.Fatalf("SessionRoutingKey must contain /sessions/ segment")
	}
	if !strings.HasSuffix(got, "/routing") {
		t.Fatalf("SessionRoutingKey must end with /routing")
	}
}

// TestSchedulerQueueKey_ExactString proves SchedulerQueueKey produces EXACTLY
// the documented key /clusteros/scheduler/<queueID>/queue.
//
// §1.1 mutation target: change "queue" suffix → "tasks" → assertion fails.
func TestSchedulerQueueKey_ExactString(t *testing.T) {
	const qid = "q-gpu-0"
	got := SchedulerQueueKey(qid)
	want := "/clusteros/scheduler/q-gpu-0/queue"
	if got != want {
		t.Fatalf("SchedulerQueueKey(%q) = %q; want %q", qid, got, want)
	}
	if !strings.Contains(got, "/scheduler/") {
		t.Fatalf("SchedulerQueueKey must contain /scheduler/ segment")
	}
	if !strings.HasSuffix(got, "/queue") {
		t.Fatalf("SchedulerQueueKey must end with /queue")
	}
}

// TestNewKeyBuilders_AllUnderNamespace proves all new key-builder outputs are
// rooted under the top-level /clusteros namespace (cross-cutting guard).
func TestNewKeyBuilders_AllUnderNamespace(t *testing.T) {
	type pair struct {
		label string
		key   string
	}
	samples := []pair{
		{"NodeStatusKey", NodeStatusKey("uuid-test")},
		{"NodeHeartbeatKey", NodeHeartbeatKey("uuid-test")},
		{"SessionRoutingKey", SessionRoutingKey("sess-test")},
		{"SchedulerQueueKey", SchedulerQueueKey("q-test")},
	}
	for _, s := range samples {
		s := s
		t.Run(s.label, func(t *testing.T) {
			if !strings.HasPrefix(s.key, Namespace+"/") {
				t.Fatalf("%s = %q; must start with %q", s.label, s.key, Namespace+"/")
			}
		})
	}
}

// TestNewKeyBuilders_DifferentIDsProduceDifferentKeys proves distinctness.
func TestNewKeyBuilders_DifferentIDsProduceDifferentKeys(t *testing.T) {
	type twoKeys struct {
		label string
		a, b  string
	}
	pairs := []twoKeys{
		{"NodeStatusKey", NodeStatusKey("n1"), NodeStatusKey("n2")},
		{"NodeHeartbeatKey", NodeHeartbeatKey("n1"), NodeHeartbeatKey("n2")},
		{"SessionRoutingKey", SessionRoutingKey("s1"), SessionRoutingKey("s2")},
		{"SchedulerQueueKey", SchedulerQueueKey("q1"), SchedulerQueueKey("q2")},
	}
	for _, p := range pairs {
		p := p
		t.Run(p.label, func(t *testing.T) {
			if p.a == p.b {
				t.Fatalf("%s: distinct IDs produced the same key %q", p.label, p.a)
			}
		})
	}
}

// TestNodeStatusKey_IsSubordinateToNodeKey proves that the status sub-key is
// rooted directly under its parent node key: NodeStatusKey(id) == NodeKey(id)+"/status".
func TestNodeStatusKey_IsSubordinateToNodeKey(t *testing.T) {
	const id = "abc-123"
	parent := NodeKey(id)
	status := NodeStatusKey(id)
	want := parent + "/status"
	if status != want {
		t.Fatalf("NodeStatusKey(%q) = %q; want NodeKey(%q)+\"/status\" = %q", id, status, id, want)
	}
}

// TestNodeHeartbeatKey_IsSubordinateToNodeKey proves that the heartbeat sub-key is
// rooted directly under its parent node key: NodeHeartbeatKey(id) == NodeKey(id)+"/heartbeat".
func TestNodeHeartbeatKey_IsSubordinateToNodeKey(t *testing.T) {
	const id = "abc-123"
	parent := NodeKey(id)
	hb := NodeHeartbeatKey(id)
	want := parent + "/heartbeat"
	if hb != want {
		t.Fatalf("NodeHeartbeatKey(%q) = %q; want NodeKey(%q)+\"/heartbeat\" = %q", id, hb, id, want)
	}
}

// TestSessionRoutingKey_IsSubordinateToSessionKey proves the routing sub-key is
// rooted directly under its parent session key.
func TestSessionRoutingKey_IsSubordinateToSessionKey(t *testing.T) {
	const id = "sess-42"
	parent := SessionKey(id)
	routing := SessionRoutingKey(id)
	want := parent + "/routing"
	if routing != want {
		t.Fatalf("SessionRoutingKey(%q) = %q; want SessionKey(%q)+\"/routing\" = %q", id, routing, id, want)
	}
}

// TestSchedulerQueueKey_IsSubordinateToSchedulerPoolKey proves the queue sub-key is
// rooted directly under its parent pool key.
func TestSchedulerQueueKey_IsSubordinateToSchedulerPoolKey(t *testing.T) {
	const id = "pool-0"
	parent := SchedulerPoolKey(id)
	queue := SchedulerQueueKey(id)
	want := parent + "/queue"
	if queue != want {
		t.Fatalf("SchedulerQueueKey(%q) = %q; want SchedulerPoolKey(%q)+\"/queue\" = %q", id, queue, id, want)
	}
}
