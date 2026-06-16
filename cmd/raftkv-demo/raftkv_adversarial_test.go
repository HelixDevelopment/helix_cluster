package main

// Adversarial, sink-side tests for the raftkv-demo KV service. These go beyond
// the existing happy-path integration tests: they probe the exact value-fidelity
// contract of the replicated store (set/overwrite/empty/large/structured keys),
// the clean-miss contract for absent keys, determinism of applying the SAME write
// sequence to a fresh cluster, and the encode/decode round-trip of the underlying
// raft.Command the service relies on. Every KV assertion is made against the real
// replicated FSM on every node (genuine replication evidence, not a local map).
//
// All cluster operations are bounded by short timeouts so a failure surfaces as a
// FAIL, never a hang.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/raft"
)

// decodeCommand mirrors exactly how the replicated FSM decodes a log entry
// (encoding/json into a raft.Command), so the round-trip test exercises the same
// decode path the real service uses on every applied entry.
func decodeCommand(b []byte, c *raft.Command) error { return json.Unmarshal(b, c) }

// advTimeout bounds every blocking cluster operation in this file so a regression
// fails loudly instead of hanging the suite.
const advTimeout = 5 * time.Second

// assertAllNodes asserts that key resolves to (want, true) on EVERY node's
// replicated FSM, first waiting for replication to converge.
func assertAllNodes(t *testing.T, svc *KVService, key, want string) {
	t.Helper()
	ids := svc.NodeIDs()
	if err := svc.WaitReplicated(key, want, ids, advTimeout); err != nil {
		t.Fatalf("replication of %q=%q: %v", key, want, err)
	}
	for _, id := range ids {
		got, ok, err := svc.Get(id, key)
		if err != nil {
			t.Fatalf("Get(%s,%q): %v", id, key, err)
		}
		if !ok {
			t.Fatalf("node %s: key %q missing (ok=false), want present with %q", id, key, want)
		}
		if got != want {
			t.Fatalf("node %s: key %q = %q, want %q", id, key, got, want)
		}
	}
}

// TestSetGetExactValueFidelity proves that a value Put via the leader reads back
// byte-for-byte identical from every replica's FSM, across a battery of awkward
// (but valid-UTF-8) values: spaces, newlines, JSON-ish text, unicode, and a large
// value. This is the core "a write is not lost or corrupted sink-side" contract.
func TestSetGetExactValueFidelity(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)

	cases := map[string]string{
		"plain":            "hello",
		"with spaces":      "a b c  d",
		"with/slashes":     "x/y/z",
		"newlines":         "line1\nline2\nline3",
		"tabs":             "a\tb\tc",
		"quotes":           `he said "hi" and 'bye'`,
		"jsonish":          `{"nested":{"k":[1,2,3]},"b":true}`,
		"unicode":          "héllo-wörld-日本語-🚀",
		"trailing-spaces":  "value   ",
		"leading-spaces":   "   value",
		"large":            strings.Repeat("abcdefghij", 5000), // 50 KB
		"equals-and-amp":   "a=b&c=d",
		"control-chars":    "ring\x07bell\x1bescape",
	}
	for k, v := range cases {
		if err := svc.Put(k, v); err != nil {
			t.Fatalf("Put(%q): %v", k, err)
		}
	}
	for k, v := range cases {
		assertAllNodes(t, svc, k, v)
	}
}

// TestOverwriteUpdatesAllReplicas proves an overwrite of an existing key replaces
// the value on every replica (get == last set, never a stale earlier value).
func TestOverwriteUpdatesAllReplicas(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)

	const key = "config/version"
	for _, v := range []string{"v1", "v2", "v3-final", ""} {
		if err := svc.Put(key, v); err != nil {
			t.Fatalf("Put(%q,%q): %v", key, v, err)
		}
		assertAllNodes(t, svc, key, v)
	}
}

// TestEmptyValueSetIsPresent is an adversarial probe of the underlying
// raft.Command's `json:"value,omitempty"` tag: an empty value is OMITTED from the
// encoded command, so a careless decode could make the key look ABSENT. The
// correct, end-user-visible contract is: Put(key,"") stores the key with an empty
// value, and Get must return ("", true) — present, not a miss. We assert on every
// replica.
func TestEmptyValueSetIsPresent(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)

	const key = "flag/empty"
	if err := svc.Put(key, ""); err != nil {
		t.Fatalf("Put(%q,\"\"): %v", key, err)
	}
	// Wait for the key to be present (ok=true) with empty value on all nodes.
	deadline := time.Now().Add(advTimeout)
	ids := svc.NodeIDs()
	for {
		allPresent := true
		for _, id := range ids {
			v, ok, err := svc.Get(id, key)
			if err != nil {
				t.Fatalf("Get(%s,%q): %v", id, key, err)
			}
			if !ok || v != "" {
				allPresent = false
				break
			}
		}
		if allPresent {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("empty-value key %q did not become present(ok=true,val=\"\") on all of %v within %s", key, ids, advTimeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Explicit sink-side assertion of the (val,ok) pair on every node.
	for _, id := range ids {
		v, ok, err := svc.Get(id, key)
		if err != nil {
			t.Fatalf("Get(%s,%q): %v", id, key, err)
		}
		if !ok {
			t.Fatalf("node %s: empty-value key %q reads as ABSENT (ok=false); want present", id, key)
		}
		if v != "" {
			t.Fatalf("node %s: empty-value key %q = %q, want \"\"", id, key, v)
		}
	}
}

// TestUnknownKeyCleanMiss proves a Get for a never-written key is a clean miss
// ("", false) on every node — no panic, no phantom value, no error.
func TestUnknownKeyCleanMiss(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)

	// Write one real key so the FSM is non-empty (a miss must still be a miss).
	if err := svc.Put("present", "yes"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	assertAllNodes(t, svc, "present", "yes")

	for _, id := range svc.NodeIDs() {
		v, ok, err := svc.Get(id, "definitely/absent/key")
		if err != nil {
			t.Fatalf("Get(%s, absent): unexpected error %v", id, err)
		}
		if ok {
			t.Fatalf("node %s: absent key reported present with value %q", id, v)
		}
		if v != "" {
			t.Fatalf("node %s: absent key returned non-empty value %q with ok=false", id, v)
		}
	}
}

// TestGetUnknownNodeErrors proves Get against a node id that is not in the cluster
// returns an error (not a silent empty miss that could mask a typo'd node id).
func TestGetUnknownNodeErrors(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)

	if _, _, err := svc.Get("no-such-node", "k"); err == nil {
		t.Fatal("Get on unknown node id should error, got nil")
	}
	if err := svc.PutVia("no-such-node", "k", "v"); err == nil {
		t.Fatal("PutVia on unknown node id should error, got nil")
	}
}

// TestDeterministicApplyAcrossFreshClusters proves a CONSENSUS-SAFETY property:
// applying the SAME ordered sequence of writes to two INDEPENDENT fresh clusters
// yields identical final per-key state on every node of each. A non-deterministic
// apply (same log -> different state) would be a silent consensus-safety bluff.
func TestDeterministicApplyAcrossFreshClusters(t *testing.T) {
	t.Parallel()

	// An ordered program including overwrites (last-writer-wins must be honored
	// identically and deterministically).
	type op struct{ k, v string }
	program := []op{
		{"a", "1"},
		{"b", "2"},
		{"a", "1-overwritten"}, // overwrite earlier key
		{"c", "3"},
		{"b", ""}, // overwrite to empty
		{"d", "4"},
		{"a", "1-final"}, // overwrite again
	}
	// Expected final state after applying the program in order.
	want := map[string]string{
		"a": "1-final",
		"b": "",
		"c": "3",
		"d": "4",
	}

	runProgram := func() map[string]string {
		svc, err := NewKVService(electTimeout, testIDs...)
		if err != nil {
			t.Fatalf("NewKVService: %v", err)
		}
		defer func() { _ = svc.Close() }()
		for _, o := range program {
			if err := svc.Put(o.k, o.v); err != nil {
				t.Fatalf("Put(%q,%q): %v", o.k, o.v, err)
			}
		}
		// Wait for the last write of each key to converge on all nodes, then snapshot
		// node-1's FSM as the observed final state, and verify all nodes agree.
		ids := svc.NodeIDs()
		for k, v := range want {
			if err := svc.WaitReplicated(k, v, ids, advTimeout); err != nil {
				t.Fatalf("replication of %q=%q: %v", k, v, err)
			}
		}
		state := map[string]string{}
		for k := range want {
			v0, ok0, err := svc.Get(ids[0], k)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if !ok0 {
				t.Fatalf("key %q absent on %s after program", k, ids[0])
			}
			state[k] = v0
			// Cross-node agreement within this cluster.
			for _, id := range ids[1:] {
				v, ok, err := svc.Get(id, k)
				if err != nil {
					t.Fatalf("Get(%s,%q): %v", id, k, err)
				}
				if !ok || v != v0 {
					t.Fatalf("intra-cluster divergence on key %q: %s=%q(ok=%v) vs %s=%q", k, ids[0], v0, ok0, id, v)
				}
			}
		}
		return state
	}

	first := runProgram()
	second := runProgram()

	if len(first) != len(second) {
		t.Fatalf("non-deterministic apply: state sizes differ %d vs %d", len(first), len(second))
	}
	for k, v := range want {
		if first[k] != v {
			t.Fatalf("cluster A: key %q = %q, want %q", k, first[k], v)
		}
		if second[k] != v {
			t.Fatalf("cluster B: key %q = %q, want %q", k, second[k], v)
		}
		if first[k] != second[k] {
			t.Fatalf("non-deterministic apply: key %q = %q in cluster A but %q in cluster B", k, first[k], second[k])
		}
	}
}

// TestCommandEncodeRoundTripUTF8 pins the encode/decode round-trip the service
// relies on for the SET path: every command the demo ever produces (CommandSet
// with a UTF-8 string value, including empty) must decode back to an identical
// Command. This guards against an encoding regression silently corrupting writes.
func TestCommandEncodeRoundTripUTF8(t *testing.T) {
	t.Parallel()

	cmds := []raft.Command{
		{Op: raft.CommandSet, Key: "k", Value: "v"},
		{Op: raft.CommandSet, Key: "empty", Value: ""}, // omitempty path
		{Op: raft.CommandSet, Key: "service/db/host", Value: "10.0.0.7"},
		{Op: raft.CommandSet, Key: "unicode", Value: "日本語-🚀"},
		{Op: raft.CommandSet, Key: "newlines", Value: "a\nb\tc"},
		{Op: raft.CommandDelete, Key: "gone"},
		{Op: raft.CommandSet, Key: "", Value: ""}, // empty key + empty value
	}
	for _, c := range cmds {
		b, err := c.Encode()
		if err != nil {
			t.Fatalf("Encode(%+v): %v", c, err)
		}
		var got raft.Command
		if err := decodeCommand(b, &got); err != nil {
			t.Fatalf("decode(%+v): %v", c, err)
		}
		if got.Op != c.Op || got.Key != c.Key || got.Value != c.Value {
			t.Fatalf("round-trip mismatch: in=%+v out=%+v (json=%s)", c, got, string(b))
		}
	}
}
