package etcd

// Adversarial, package-internal probes for pkg/etcd.
//
// SCOPE / HONEST FRAMING (read this first):
//
// pkg/etcd is a THIN wrapper over go.etcd.io/etcd/client/v3 plus a set of pure,
// stateless key-builder functions (keys.go). It holds NO local mutable state:
//   - There is no in-process KV map, lease cache, or revision store.
//   - Put/Get/CAS/Txn/Lease/TTL/Watch/Lock semantics are delegated ENTIRELY to a
//     live etcd server via clientv3; the only persistent field on Client is the
//     *clientv3.Client handle.
// Therefore the four "tonight's classes" that require local stateful logic —
// (a) unguarded shared mutable state, (b) in-process CAS/version tiebreak,
// (c) lease/TTL expiry-boundary arithmetic — have NO testable surface in this
// package. They live behind a real etcd and are exercised by the build-tagged
// integration suite (etcd_integration_test.go, hxc1115_integration_test.go).
// Re-proving them here without a server would be a CLAUDE-1 PASS-bluff, so we
// deliberately do NOT.
//
// What IS locally testable, and what this file pins:
//   1. The ONE concurrency surface that exists without a server: Client.Close()
//      and the New() constructor's default-injection branch. We hammer them with
//      -race to prove there is no data race on the wrapper itself.
//   2. Class (d) IDENTITY / ENCODING — the key-builders are pure functions with
//      full local logic. The existing tests pin exact strings; they do NOT pin
//      the security-relevant PREFIX-BOUNDARY invariant: a point key like
//      NodeKey("n1") is a STRING PREFIX of NodeKey("n10"), so any caller that
//      does GetPrefix(NodeKey(id)) (without a trailing "/") silently range-scans
//      sibling nodes. This is a LATENT RISK in the key SCHEME (not a bug in this
//      package — every in-tree caller correctly appends "/"). We pin the property
//      so a future builder change OR a new unsafe caller is caught deterministically.
//
// Every test below PASSES on unfixed production: each pins a real, current
// contract. None is gated behind an unapplied fix. None needs a live server.

import (
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// (a) CONCURRENCY surface that exists WITHOUT a live server.
// ---------------------------------------------------------------------------

// TestClient_ConcurrentCloseNilReceiver_NoRace hammers the nil-guard path of
// Close from many goroutines. Close on a nil *Client and on a Client whose cli
// is nil must be a race-free no-op (the documented defensive contract). This is
// the only Client method that touches its own state without a server.
//
// SINK-SIDE assertion: every concurrent Close returns nil (no panic, no race).
// Run under -race; a guard that read c.cli without protection, or a future
// change introducing shared mutable bookkeeping in Close, trips the detector.
func TestClient_ConcurrentCloseNilReceiver_NoRace(t *testing.T) {
	t.Parallel()

	// Case 1: nil *Client receiver.
	var nilClient *Client
	// Case 2: non-nil Client with nil cli (constructed without New).
	emptyClient := &Client{}

	const goroutines = 256
	errs := make([]error, goroutines*2)
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)
	for i := 0; i < goroutines; i++ {
		idx := i
		go func() {
			defer wg.Done()
			errs[idx] = nilClient.Close()
		}()
		go func() {
			defer wg.Done()
			errs[goroutines+idx] = emptyClient.Close()
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Close() #%d returned %v; nil-guard contract requires nil", i, err)
		}
	}
}

// TestClient_ConcurrentConstruct_NoRace constructs many Clients concurrently
// through New (which mutates only its local cfg copy, then returns a fresh
// Client). clientv3 dials lazily, so New succeeds without a server. This proves
// New has no shared mutable state across calls.
//
// SINK-SIDE assertion: every returned Client is non-nil and its Raw() handle is
// non-nil and distinct per call (no accidental shared/global handle).
func TestClient_ConcurrentConstruct_NoRace(t *testing.T) {
	t.Parallel()

	const goroutines = 128
	clients := make([]*Client, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		idx := i
		go func() {
			defer wg.Done()
			c, err := New(Config{Endpoints: []string{"127.0.0.1:2379"}})
			if err != nil {
				// New must not fail for a syntactically valid endpoint (lazy dial).
				t.Errorf("New() goroutine %d: unexpected error %v", idx, err)
				return
			}
			clients[idx] = c
		}()
	}
	wg.Wait()

	seen := make(map[interface{}]struct{}, goroutines)
	for i, c := range clients {
		if c == nil {
			t.Fatalf("New() goroutine %d produced nil Client", i)
		}
		raw := c.Raw()
		if raw == nil {
			t.Fatalf("New() goroutine %d: Raw() is nil; constructor did not wire clientv3", i)
		}
		if _, dup := seen[raw]; dup {
			t.Fatalf("New() goroutine %d: Raw() handle is shared across constructions "+
				"(distinct New calls must yield distinct clientv3 handles)", i)
		}
		seen[raw] = struct{}{}
		_ = c.Close()
	}
}

// ---------------------------------------------------------------------------
// (d) IDENTITY / ENCODING — prefix-boundary collision in the key SCHEME.
// ---------------------------------------------------------------------------

// builder is a (label, fn) pair over the point-key builders that take a single
// id and return a point key (no fixed sub-suffix).
type pointBuilder struct {
	label string
	fn    func(string) string
}

var pointBuilders = []pointBuilder{
	{"NodeKey", NodeKey},
	{"SessionKey", SessionKey},
	{"SchedulerPoolKey", SchedulerPoolKey},
	{"SecurityKey", SecurityKey},
	{"ConfigKey", ConfigKey},
	{"LockKey", LockKey},
}

// TestPointKey_IsStringPrefixOfNumericSibling DOCUMENTS the latent range-scan
// hazard: a point key built from id "n1" is a STRING PREFIX of the point key
// built from id "n10". etcd's WithPrefix() range-matches by raw byte prefix, so
// GetPrefix(NodeKey("1")) on a flat scheme would capture "10", "11", ... .
//
// This is NOT a bug in pkg/etcd — it is an inherent property of any flat
// hierarchical key namespace, and every in-tree caller of GetPrefix correctly
// appends a trailing "/" (e.g. internal/node/etcd_registry.go List uses
// NamespaceNodes+"/"). We pin the property so the risk is documented and any
// future builder that "fixed" this by, say, zero-padding or by appending a
// terminator would visibly change this test (forcing a conscious review).
//
// SINK-SIDE: the actual string-prefix relationship between two builder outputs.
func TestPointKey_IsStringPrefixOfNumericSibling(t *testing.T) {
	t.Parallel()

	for _, b := range pointBuilders {
		b := b
		t.Run(b.label, func(t *testing.T) {
			short := b.fn("1")  // .../1
			long := b.fn("10")  // .../10
			if short == long {
				t.Fatalf("%s(\"1\") == %s(\"10\") == %q; ids must produce distinct keys",
					b.label, b.label, short)
			}
			if !strings.HasPrefix(long, short) {
				t.Fatalf("%s: expected %q to be a raw string prefix of %q "+
					"(documents the WithPrefix range-scan hazard); it is not — the "+
					"key scheme changed, re-review prefix-scan call sites", b.label, short, long)
			}
			// The boundary-SAFE form (append "/") must NOT collide: this is the
			// invariant every caller relies on to scope a sub-tree scan to one id.
			if strings.HasPrefix(long, short+"/") {
				t.Fatalf("%s: %q must NOT be under boundary-safe prefix %q+\"/\"; "+
					"if it is, even the trailing-slash convention is unsafe", b.label, long, short)
			}
		})
	}
}

// TestSubKey_BoundarySafeScanIsolatesOneParent proves the POSITIVE contract that
// callers depend on: scanning the boundary-safe prefix NodeKey(id)+"/" captures
// id's OWN sub-records (status, heartbeat) and does NOT capture a sibling whose
// id has id as a string prefix.
//
// SINK-SIDE: membership of concrete builder outputs under the boundary-safe
// prefix. This is the deterministic, server-free analogue of "GetPrefix returns
// only this node's sub-tree".
func TestSubKey_BoundarySafeScanIsolatesOneParent(t *testing.T) {
	t.Parallel()

	const id = "1"
	const sibling = "10" // has id "1" as a string prefix — the adversarial case
	safePrefix := NodeKey(id) + "/"

	// id's own sub-records MUST be under the safe prefix.
	own := []struct {
		label string
		key   string
	}{
		{"NodeStatusKey", NodeStatusKey(id)},
		{"NodeHeartbeatKey", NodeHeartbeatKey(id)},
	}
	for _, o := range own {
		if !strings.HasPrefix(o.key, safePrefix) {
			t.Fatalf("%s(%q) = %q; must be under boundary-safe prefix %q (own sub-record lost)",
				o.label, id, o.key, safePrefix)
		}
	}

	// The sibling's point key and ALL its sub-records MUST NOT be under id's
	// boundary-safe prefix — otherwise a per-node scan leaks a neighbor.
	siblingKeys := []struct {
		label string
		key   string
	}{
		{"NodeKey(sibling)", NodeKey(sibling)},
		{"NodeStatusKey(sibling)", NodeStatusKey(sibling)},
		{"NodeHeartbeatKey(sibling)", NodeHeartbeatKey(sibling)},
	}
	for _, s := range siblingKeys {
		if strings.HasPrefix(s.key, safePrefix) {
			t.Fatalf("LEAK: %s = %q is under boundary-safe prefix %q for id %q; "+
				"a per-node sub-tree scan would capture sibling %q",
				s.label, s.key, safePrefix, id, sibling)
		}
	}
}

// TestEmptyID_DoesNotCollapseToScanRoot pins class-(d) empty-key behavior at the
// boundary: an empty id must NOT make a point key collapse onto its parent
// namespace (which would turn an intended point Put/Get into a namespace-wide
// scan root). The builders emit a trailing "/" sentinel instead.
//
// SINK-SIDE: the empty-id key is strictly longer than the namespace and ends in
// "/", so it can never equal the namespace used as a scan prefix.
func TestEmptyID_DoesNotCollapseToScanRoot(t *testing.T) {
	t.Parallel()

	cases := []struct {
		label string
		key   string
		ns    string
	}{
		{"NodeKey", NodeKey(""), NamespaceNodes},
		{"SessionKey", SessionKey(""), NamespaceSessions},
		{"SchedulerPoolKey", SchedulerPoolKey(""), NamespaceScheduler},
		{"SecurityKey", SecurityKey(""), NamespaceSecurity},
		{"ConfigKey", ConfigKey(""), NamespaceConfig},
		{"LockKey", LockKey(""), NamespaceLocks},
	}
	for _, c := range cases {
		c := c
		t.Run(c.label, func(t *testing.T) {
			if c.key == c.ns {
				t.Fatalf("%s(\"\") = %q collapsed onto bare namespace %q; an empty id "+
					"would turn a point op into a namespace-wide scan root", c.label, c.key, c.ns)
			}
			if !strings.HasPrefix(c.key, c.ns+"/") {
				t.Fatalf("%s(\"\") = %q; want it under %q+\"/\"", c.label, c.key, c.ns)
			}
			if !strings.HasSuffix(c.key, "/") {
				t.Fatalf("%s(\"\") = %q; want trailing-slash empty-id sentinel", c.label, c.key)
			}
		})
	}
}

// TestCrossNamespace_NoPrefixContainment proves no top-level sub-namespace is a
// string prefix of another (e.g. /clusteros/nodes vs a hypothetical
// /clusteros/nodesX), so a WithPrefix scan of one namespace can never bleed into
// another. Guards against a future namespace rename that reintroduces a prefix
// overlap.
//
// SINK-SIDE: pairwise prefix-containment over the boundary-safe namespace forms.
func TestCrossNamespace_NoPrefixContainment(t *testing.T) {
	t.Parallel()

	ns := []string{
		NamespaceNodes, NamespaceSessions, NamespaceScheduler,
		NamespaceSecurity, NamespaceConfig, NamespaceLocks,
	}
	for i := range ns {
		for j := range ns {
			if i == j {
				continue
			}
			a, b := ns[i], ns[j]
			// Scans use prefix+"/"; assert b's key space (b+"/") is not under a+"/".
			if strings.HasPrefix(b+"/", a+"/") {
				t.Fatalf("namespace %q is a boundary-prefix of %q; a scan of %q would "+
					"leak into %q", a, b, a, b)
			}
		}
	}
}
