//go:build integration

package etcd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// TestHXC1115_KeyNamespace_PutGetRoundTripAllPrefixes is the CLAUDE-1 §7.1
// closure test for HXC-1115.  It proves:
//  1. Every top-level /clusteros sub-namespace prefix (nodes/, sessions/,
//     scheduler/, security/, config/, locks/) is usable as a real etcd key.
//  2. A per-run UUID value written under each prefix is read back byte-for-byte.
//  3. The stored key matches the key-builder output exactly.
//  4. The test hard-FAILs on any mismatch — no soft skips after the endpoint
//     is confirmed available.
//
// The test reads ETCD_ENDPOINTS from the environment.  If the variable is
// unset or empty, the test is skipped (the orchestrator always sets it).
// If the variable is set but the endpoint is unreachable, the test hard-fails.
func TestHXC1115_KeyNamespace_PutGetRoundTripAllPrefixes(t *testing.T) {
	endpoints := os.Getenv("ETCD_ENDPOINTS")
	if endpoints == "" {
		t.Skip("ETCD_ENDPOINTS not set; skipping integration test (orchestrator sets this)")
	}

	epList := strings.Split(endpoints, ",")
	for i, ep := range epList {
		epList[i] = strings.TrimSpace(ep)
	}

	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   epList,
		DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("HXC-1115: failed to connect to etcd at %v: %v", epList, err)
	}
	defer cli.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Per-run UUID token embedded in all values so a stale read cannot masquerade.
	runToken := fmt.Sprintf("hxc1115-%d", time.Now().UnixNano())
	t.Logf("HXC-1115 run token: %s", runToken)

	// Build one test case per top-level prefix.  Each entry specifies:
	//   - a key produced by a builder function
	//   - the exact expected key string (cross-checked against the builder)
	//   - a value embedding the run token
	type testCase struct {
		label   string
		key     string
		wantKey string
		value   string
	}

	suffix := runToken
	cases := []testCase{
		{
			label:   "nodes",
			key:     NodeKey(suffix),
			wantKey: NamespaceNodes + "/" + suffix,
			value:   fmt.Sprintf(`{"prefix":"nodes","token":%q}`, runToken),
		},
		{
			label:   "sessions",
			key:     SessionKey(suffix),
			wantKey: NamespaceSessions + "/" + suffix,
			value:   fmt.Sprintf(`{"prefix":"sessions","token":%q}`, runToken),
		},
		{
			label:   "scheduler",
			key:     SchedulerPoolKey(suffix),
			wantKey: NamespaceScheduler + "/" + suffix,
			value:   fmt.Sprintf(`{"prefix":"scheduler","token":%q}`, runToken),
		},
		{
			label:   "security",
			key:     SecurityKey(suffix),
			wantKey: NamespaceSecurity + "/" + suffix,
			value:   fmt.Sprintf(`{"prefix":"security","token":%q}`, runToken),
		},
		{
			label:   "config",
			key:     ConfigKey(suffix),
			wantKey: NamespaceConfig + "/" + suffix,
			value:   fmt.Sprintf(`{"prefix":"config","token":%q}`, runToken),
		},
		{
			label:   "locks",
			key:     LockKey(suffix),
			wantKey: NamespaceLocks + "/" + suffix,
			value:   fmt.Sprintf(`{"prefix":"locks","token":%q}`, runToken),
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.label, func(t *testing.T) {
			// Verify the builder output matches the expected key string exactly.
			if tc.key != tc.wantKey {
				t.Fatalf("key builder mismatch for prefix %q: builder returned %q, want %q",
					tc.label, tc.key, tc.wantKey)
			}

			// Put the value into real etcd.
			if _, err := cli.Put(ctx, tc.key, tc.value); err != nil {
				t.Fatalf("Put(%q): %v", tc.key, err)
			}

			// Get it back — byte-equality assertion.
			resp, err := cli.Get(ctx, tc.key)
			if err != nil {
				t.Fatalf("Get(%q): %v", tc.key, err)
			}
			if len(resp.Kvs) == 0 {
				t.Fatalf("Get(%q): key absent after Put", tc.key)
			}

			gotKey := string(resp.Kvs[0].Key)
			gotValue := string(resp.Kvs[0].Value)

			// The stored key must match the builder output exactly.
			if gotKey != tc.key {
				t.Fatalf("stored key mismatch: got %q, want %q (builder output)", gotKey, tc.key)
			}

			// The stored value must equal the written value byte-for-byte.
			if gotValue != tc.value {
				t.Fatalf("byte-equality fail for %q:\n  wrote: %q\n  read:  %q",
					tc.key, tc.value, gotValue)
			}

			// The run token must be present in the retrieved value.
			if !strings.Contains(gotValue, runToken) {
				t.Fatalf("run token %q absent from retrieved value %q", runToken, gotValue)
			}

			t.Logf("HXC-1115 %s: key=%q round-trip OK (token present)", tc.label, tc.key)
		})
	}

	// Also exercise the sub-key builders that were added for HXC-1115.
	subCases := []testCase{
		{
			label:   "nodes/status",
			key:     NodeStatusKey(suffix),
			wantKey: NamespaceNodes + "/" + suffix + "/status",
			value:   fmt.Sprintf(`{"sub":"status","token":%q}`, runToken),
		},
		{
			label:   "nodes/heartbeat",
			key:     NodeHeartbeatKey(suffix),
			wantKey: NamespaceNodes + "/" + suffix + "/heartbeat",
			value:   fmt.Sprintf(`{"sub":"heartbeat","token":%q}`, runToken),
		},
		{
			label:   "sessions/routing",
			key:     SessionRoutingKey(suffix),
			wantKey: NamespaceSessions + "/" + suffix + "/routing",
			value:   fmt.Sprintf(`{"sub":"routing","token":%q}`, runToken),
		},
		{
			label:   "scheduler/queue",
			key:     SchedulerQueueKey(suffix),
			wantKey: NamespaceScheduler + "/" + suffix + "/queue",
			value:   fmt.Sprintf(`{"sub":"queue","token":%q}`, runToken),
		},
	}

	for _, tc := range subCases {
		tc := tc
		t.Run(tc.label, func(t *testing.T) {
			if tc.key != tc.wantKey {
				t.Fatalf("sub-key builder mismatch for %q: builder returned %q, want %q",
					tc.label, tc.key, tc.wantKey)
			}

			if _, err := cli.Put(ctx, tc.key, tc.value); err != nil {
				t.Fatalf("Put(%q): %v", tc.key, err)
			}

			resp, err := cli.Get(ctx, tc.key)
			if err != nil {
				t.Fatalf("Get(%q): %v", tc.key, err)
			}
			if len(resp.Kvs) == 0 {
				t.Fatalf("Get(%q): key absent after Put", tc.key)
			}

			gotKey := string(resp.Kvs[0].Key)
			gotValue := string(resp.Kvs[0].Value)

			if gotKey != tc.key {
				t.Fatalf("stored key mismatch: got %q, want %q", gotKey, tc.key)
			}
			if gotValue != tc.value {
				t.Fatalf("byte-equality fail for %q:\n  wrote: %q\n  read:  %q",
					tc.key, tc.value, gotValue)
			}
			if !strings.Contains(gotValue, runToken) {
				t.Fatalf("run token %q absent from retrieved value %q", runToken, gotValue)
			}

			t.Logf("HXC-1115 %s: key=%q round-trip OK (token present)", tc.label, tc.key)
		})
	}
}
