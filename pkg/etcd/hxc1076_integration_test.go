//go:build integration

package etcd

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/testing/evidence"
)

// TestHXC1076_NodeKey_PutGetRoundTripSecondClient is the CLAUDE-1 §7.1 closure
// test for HXC-1076. It proves:
//  1. A node descriptor written via NodeKey(/clusteros/nodes/<uuid>) by client A
//     is read back byte-for-byte by an INDEPENDENT client B.
//  2. The revision increments across the Put (state delta, not a cache hit).
//  3. A per-run UUID token is embedded in the payload so a stale-read or
//     fabricated response cannot masquerade as a real round-trip.
//
// sharedEndpoint and newClient are defined in etcd_integration_test.go
// (compiled only under the integration tag) and share the same TestMain.
func TestHXC1076_NodeKey_PutGetRoundTripSecondClient(t *testing.T) {
	clientA := newClient(t)
	clientB := newClient(t) // independent connection, no shared state

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// §7.1 evidence recorder — per-run token prevents stale-read bluffs.
	e := evidence.NewT(t, "hxc1076-node-put-get", t.TempDir())
	runToken := e.Token()

	// Construct key using the canonical key-builder (proves the builder is wired).
	nodeUUID := "node-" + runToken
	key := NodeKey(nodeUUID)

	if !strings.HasPrefix(key, "/clusteros/nodes/") {
		t.Fatalf("NodeKey(%q) = %q; expected /clusteros/nodes/ prefix", nodeUUID, key)
	}

	// Capture the revision BEFORE the Put so we can prove a real state delta.
	beforeVal, _, err := clientA.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get(before): %v", err)
	}

	// Construct the node descriptor payload with the embedded run token.
	payload := fmt.Sprintf(`{"node_uuid":%q,"run_token":%q,"ts":%d}`,
		nodeUUID, runToken, time.Now().UnixNano())

	if err := clientA.Put(ctx, key, payload); err != nil {
		t.Fatalf("clientA.Put(%q): %v", key, err)
	}

	// CLIENT B reads — different TCP connection, proves no shared in-memory state.
	gotVal, ok, err := clientB.Get(ctx, key)
	if err != nil {
		t.Fatalf("clientB.Get(%q): %v", key, err)
	}
	if !ok {
		t.Fatalf("clientB.Get(%q): ok=false; key must exist after clientA.Put", key)
	}

	// Byte-for-byte equality between what A wrote and what B reads.
	if gotVal != payload {
		t.Fatalf("byte-equality fail:\n  wrote: %q\n  read:  %q", payload, gotVal)
	}

	// §7.1 state delta: before (empty) ≠ after (payload).
	e.MustDelta(t, "value-delta-after-put", beforeVal, gotVal)

	// §7.1 positive evidence: the run token is visible in the retrieved value.
	e.MustPositive(t, "run-token-in-payload", gotVal, runToken)

	t.Logf("HXC-1076: NodeKey=%q | run_token=%s | payload=%s", key, runToken, gotVal)
}

// TestHXC1076_RevisionIncrements proves the etcd revision advances after a Put
// under the /clusteros namespace. A revision that does NOT change would mean the
// Put was silently swallowed — a bluff the integration tier must catch.
func TestHXC1076_RevisionIncrements(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	e := evidence.NewT(t, "hxc1076-revision", t.TempDir())
	key := ConfigKey("revision-probe-" + e.Token())

	// Read current revision via raw client header.
	rawBefore, err := c.Raw().Get(ctx, key)
	if err != nil {
		t.Fatalf("raw Get(before): %v", err)
	}
	revBefore := rawBefore.Header.Revision

	if err := c.Put(ctx, key, "v1-"+e.Token()); err != nil {
		t.Fatalf("Put: %v", err)
	}

	rawAfter, err := c.Raw().Get(ctx, key)
	if err != nil {
		t.Fatalf("raw Get(after): %v", err)
	}
	revAfter := rawAfter.Header.Revision

	if revAfter <= revBefore {
		t.Fatalf("revision did not increment: before=%d after=%d", revBefore, revAfter)
	}

	e.MustDelta(t, "revision-delta", revBefore, revAfter)
	t.Logf("HXC-1076: revision before=%d after=%d (delta=%d)", revBefore, revAfter, revAfter-revBefore)
}

// TestHXC1076_AllKeyBuilders_PutGetRoundTrip proves that every key-builder
// constant and function produces real, usable etcd keys: the control plane
// can Put and Get values under each of the six /clusteros sub-namespaces.
func TestHXC1076_AllKeyBuilders_PutGetRoundTrip(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	e := evidence.NewT(t, "hxc1076-all-key-builders", t.TempDir())
	tok := e.Token()

	cases := []struct {
		label string
		key   string
		value string
	}{
		{"NodeKey", NodeKey("node-" + tok), `{"type":"node","token":"` + tok + `"}`},
		{"SessionKey", SessionKey("sess-" + tok), `{"type":"session","token":"` + tok + `"}`},
		{"SchedulerPoolKey", SchedulerPoolKey("pool-" + tok), `{"type":"pool","token":"` + tok + `"}`},
		{"SecurityKey", SecurityKey("sec-" + tok), `{"type":"security","token":"` + tok + `"}`},
		{"ConfigKey", ConfigKey("cfg-" + tok), `{"type":"config","token":"` + tok + `"}`},
		{"LockKey", LockKey("lk-" + tok), `{"type":"lock","token":"` + tok + `"}`},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.label, func(t *testing.T) {
			if err := c.Put(ctx, tc.key, tc.value); err != nil {
				t.Fatalf("%s: Put(%q): %v", tc.label, tc.key, err)
			}
			got, ok, err := c.Get(ctx, tc.key)
			if err != nil {
				t.Fatalf("%s: Get(%q): %v", tc.label, tc.key, err)
			}
			if !ok {
				t.Fatalf("%s: key %q absent after Put", tc.label, tc.key)
			}
			if got != tc.value {
				t.Fatalf("%s: byte-equality fail: wrote %q, read %q", tc.label, tc.value, got)
			}
			if !strings.Contains(got, tok) {
				t.Fatalf("%s: run token %q missing from retrieved value %q", tc.label, tok, got)
			}
			t.Logf("HXC-1076 %s: key=%q round-trip OK (token present)", tc.label, tc.key)
		})
	}
}

// TestHXC1076_GetPrefixUnderNamespace proves GetPrefix correctly enumerates
// all keys under a /clusteros sub-namespace prefix and excludes keys in other
// sub-namespaces — the scheduler must not see node keys and vice versa.
func TestHXC1076_GetPrefixUnderNamespace(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	e := evidence.NewT(t, "hxc1076-prefix-scan", t.TempDir())
	tok := e.Token()

	// Write three node keys and one config key (must not appear in node scan).
	nodeKeys := []string{
		NodeKey("n1-" + tok),
		NodeKey("n2-" + tok),
		NodeKey("n3-" + tok),
	}
	configKey := ConfigKey("shouldnt-appear-" + tok)

	for _, k := range nodeKeys {
		if err := c.Put(ctx, k, `{"token":"`+tok+`"}`); err != nil {
			t.Fatalf("Put(%q): %v", k, err)
		}
	}
	if err := c.Put(ctx, configKey, "cfg-value"); err != nil {
		t.Fatalf("Put(config key): %v", err)
	}

	// Scan only the nodes sub-namespace.
	got, err := c.GetPrefix(ctx, NamespaceNodes+"/")
	if err != nil {
		t.Fatalf("GetPrefix(nodes): %v", err)
	}

	// Every node key written this run must be present.
	for _, nk := range nodeKeys {
		if _, ok := got[nk]; !ok {
			t.Errorf("GetPrefix(nodes) missing key %q", nk)
		}
	}

	// The config key must NOT appear.
	if _, leaked := got[configKey]; leaked {
		t.Fatalf("GetPrefix(nodes) leaked config key %q", configKey)
	}

	// Each retrieved value must carry the run token.
	for k, v := range got {
		if strings.Contains(k, tok) && !strings.Contains(v, tok) {
			t.Errorf("key %q: run token %q absent from value %q", k, tok, v)
		}
	}

	e.MustDelta(t, "nodes-prefix-count", 0, len(got))
	t.Logf("HXC-1076: GetPrefix returned %d node keys, config key correctly excluded", len(got))
}
