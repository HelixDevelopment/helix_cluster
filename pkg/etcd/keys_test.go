package etcd

import (
	"strings"
	"testing"
)

// TestNamespace_RootIsClusterOS proves the root Namespace constant is exactly
// "/clusteros" — the value that every sub-namespace and key-builder depends on.
// §1.1 mutation target: change Namespace to "/WRONG" → every sub-test below
// (and the sub-namespace constants) will fail.
func TestNamespace_RootIsClusterOS(t *testing.T) {
	const want = "/clusteros"
	if Namespace != want {
		t.Fatalf("Namespace = %q; want %q", Namespace, want)
	}
}

// TestSubNamespaces_ExactPaths proves each sub-namespace constant has EXACTLY
// the documented path (not just a prefix of it). A wrong suffix (e.g. "node"
// instead of "nodes") would silently corrupt every key written by the control
// plane, so we assert byte-for-byte equality.
func TestSubNamespaces_ExactPaths(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"NamespaceNodes", NamespaceNodes, "/clusteros/nodes"},
		{"NamespaceSessions", NamespaceSessions, "/clusteros/sessions"},
		{"NamespaceScheduler", NamespaceScheduler, "/clusteros/scheduler"},
		{"NamespaceSecurity", NamespaceSecurity, "/clusteros/security"},
		{"NamespaceConfig", NamespaceConfig, "/clusteros/config"},
		{"NamespaceLocks", NamespaceLocks, "/clusteros/locks"},
		{"LockSchedulerKey", LockSchedulerKey, "/clusteros/locks/scheduler"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("%s = %q; want %q", tc.name, tc.got, tc.want)
			}
		})
	}
}

// TestNodeKey_Composition proves NodeKey builds the correct path with the
// separator "/" between the namespace and the uuid segment. Verifies:
//  1. The result starts with the nodes sub-namespace prefix.
//  2. The uuid appears verbatim after a single "/".
//  3. No double-slash or missing slash is introduced.
//
// §1.1 mutation target: remove the "/" separator in NodeKey (use Sprintf("%s%s"))
// → the "has-slash-before-uuid" assertion fails.
func TestNodeKey_Composition(t *testing.T) {
	const uuid = "a1b2c3d4-e5f6-7890-abcd-ef1234567890"
	key := NodeKey(uuid)

	const wantPrefix = "/clusteros/nodes/"
	const wantFull = "/clusteros/nodes/" + uuid

	if !strings.HasPrefix(key, wantPrefix) {
		t.Fatalf("NodeKey(%q) = %q; want prefix %q", uuid, key, wantPrefix)
	}
	if key != wantFull {
		t.Fatalf("NodeKey(%q) = %q; want %q", uuid, key, wantFull)
	}
	// Prove no double-slash: split on "/" and check component count
	parts := strings.Split(strings.TrimPrefix(key, "/"), "/")
	// Expect exactly: ["clusteros", "nodes", uuid] = 3 parts
	if len(parts) != 3 {
		t.Fatalf("NodeKey(%q) has %d slash-separated parts, want 3: %v", uuid, len(parts), parts)
	}
	if parts[2] != uuid {
		t.Fatalf("NodeKey(%q): last segment = %q, want uuid %q", uuid, parts[2], uuid)
	}
}

// TestSessionKey_Composition proves SessionKey produces the correct path.
// §1.1 mutation target: change NamespaceSessions → NamespaceNodes in
// SessionKey → the prefix assertion fails.
func TestSessionKey_Composition(t *testing.T) {
	const sid = "sess-001"
	key := SessionKey(sid)
	want := "/clusteros/sessions/" + sid
	if key != want {
		t.Fatalf("SessionKey(%q) = %q; want %q", sid, key, want)
	}
	if !strings.Contains(key, "/sessions/") {
		t.Fatalf("SessionKey(%q) = %q; must contain /sessions/ segment", sid, key)
	}
}

// TestSchedulerPoolKey_Composition proves SchedulerPoolKey produces the correct path.
// §1.1 mutation target: replace "/scheduler/" with "/sched/" → fails.
func TestSchedulerPoolKey_Composition(t *testing.T) {
	const poolID = "pool-gpu-0"
	key := SchedulerPoolKey(poolID)
	want := "/clusteros/scheduler/" + poolID
	if key != want {
		t.Fatalf("SchedulerPoolKey(%q) = %q; want %q", poolID, key, want)
	}
}

// TestLockSchedulerKey_IsConstant proves LockSchedulerKey is the compile-time
// constant "/clusteros/locks/scheduler" so it can be used as a map key and
// switch case without function-call overhead.
// §1.1 mutation target: change LockSchedulerKey to "/clusteros/lock/scheduler"
// (drop the "s") → exact-match assertion fails.
func TestLockSchedulerKey_IsConstant(t *testing.T) {
	const want = "/clusteros/locks/scheduler"
	if LockSchedulerKey != want {
		t.Fatalf("LockSchedulerKey = %q; want %q", LockSchedulerKey, want)
	}
}

// TestLockKey_Composition proves LockKey builds the correct per-name lock path.
// §1.1 mutation target: use NamespaceScheduler instead of NamespaceLocks in
// LockKey → the "/locks/" segment assertion fails.
func TestLockKey_Composition(t *testing.T) {
	const name = "node-upgrade-abc123"
	key := LockKey(name)
	want := "/clusteros/locks/" + name
	if key != want {
		t.Fatalf("LockKey(%q) = %q; want %q", name, key, want)
	}
	if !strings.Contains(key, "/locks/") {
		t.Fatalf("LockKey(%q) = %q; must contain /locks/ segment", name, key)
	}
}

// TestKeyBuilder_AllUnderNamespace proves that ALL key-builder outputs are
// rooted under the top-level Namespace prefix. This is the cross-cutting guard:
// no builder may silently drop the root prefix (e.g. by using a hardcoded
// string that diverges after a Namespace rename).
// §1.1 mutation target: change Namespace = "" → all HasPrefix checks fail.
func TestKeyBuilder_AllUnderNamespace(t *testing.T) {
	type pair struct {
		label string
		key   string
	}
	samples := []pair{
		{"NodeKey", NodeKey("uuid-test")},
		{"SessionKey", SessionKey("sess-test")},
		{"SchedulerPoolKey", SchedulerPoolKey("pool-test")},
		{"SecurityKey", SecurityKey("sec-test")},
		{"ConfigKey", ConfigKey("cfg-test")},
		{"LockKey", LockKey("lk-test")},
		{"LockSchedulerKey", LockSchedulerKey},
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

// TestKeyBuilder_DifferentIDsProduceDifferentKeys proves that two distinct IDs
// produce distinct keys for every builder. Identical keys for different
// identifiers would cause silent data overwrites in the control plane.
// §1.1 mutation target: remove the id argument from any builder (return a fixed
// string) → the inequality assertion fails.
func TestKeyBuilder_DifferentIDsProduceDifferentKeys(t *testing.T) {
	type twoKeys struct {
		label string
		a, b  string
	}
	pairs := []twoKeys{
		{"NodeKey", NodeKey("uuid-aaa"), NodeKey("uuid-bbb")},
		{"SessionKey", SessionKey("s1"), SessionKey("s2")},
		{"SchedulerPoolKey", SchedulerPoolKey("p1"), SchedulerPoolKey("p2")},
		{"SecurityKey", SecurityKey("k1"), SecurityKey("k2")},
		{"ConfigKey", ConfigKey("c1"), ConfigKey("c2")},
		{"LockKey", LockKey("l1"), LockKey("l2")},
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

// TestKeyBuilder_EmptyIDProducesTrailingSlash documents (and guards) the
// behaviour when an empty ID is passed: the key ends with "/" which callers
// can detect as invalid. This is NOT a security guarantee — callers must
// validate IDs — but it proves the builder does not silently return the
// namespace prefix (which could then be used as a scan prefix rather than a
// point key).
func TestKeyBuilder_EmptyIDProducesTrailingSlash(t *testing.T) {
	key := NodeKey("")
	if !strings.HasSuffix(key, "/") {
		t.Fatalf("NodeKey(\"\") = %q; want trailing slash (empty-id sentinel)", key)
	}
	// Must NOT equal the plain namespace: must have the sub-namespace segment.
	if key == Namespace {
		t.Fatalf("NodeKey(\"\") = %q; must not collapse to root namespace", key)
	}
}
