package bootstrap

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// ────────────────────────────────────────────────────────────────────────────
// StaticStrategy — happy path
// ────────────────────────────────────────────────────────────────────────────

// TestStaticStrategy_Resolve_ExactPeers asserts that Resolve returns exactly
// the parsed seeds with deterministic IDs and in sorted-address order.
//
// Anti-bluff mutation: drop one seed in the range loop inside Resolve (e.g.
// skip i==0) → len assertion fails and the missing address check fails.
func TestStaticStrategy_Resolve_ExactPeers(t *testing.T) {
	seeds := []string{"10.0.0.2:7000", "10.0.0.1:7000", "10.0.0.3:7000"}
	s, err := NewStaticStrategy(seeds)
	if err != nil {
		t.Fatalf("NewStaticStrategy: unexpected error: %v", err)
	}

	peers, err := s.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: unexpected error: %v", err)
	}

	// After lexicographic sort: 10.0.0.1:7000, 10.0.0.2:7000, 10.0.0.3:7000
	want := []Peer{
		{ID: "static-0", Address: "10.0.0.1:7000"},
		{ID: "static-1", Address: "10.0.0.2:7000"},
		{ID: "static-2", Address: "10.0.0.3:7000"},
	}
	if len(peers) != len(want) {
		t.Fatalf("Resolve: got %d peers, want %d", len(peers), len(want))
	}
	for i, p := range peers {
		if p != want[i] {
			t.Errorf("peers[%d]: got %+v, want %+v", i, p, want[i])
		}
	}
}

// TestStaticStrategy_Resolve_SingleSeed verifies the single-seed edge case.
//
// Anti-bluff mutation: change ID assignment to start at 1 → "static-1" ≠ "static-0".
func TestStaticStrategy_Resolve_SingleSeed(t *testing.T) {
	s, err := NewStaticStrategy([]string{"192.168.1.1:2379"})
	if err != nil {
		t.Fatalf("NewStaticStrategy: unexpected error: %v", err)
	}
	peers, err := s.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: unexpected error: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("got %d peers, want 1", len(peers))
	}
	if peers[0].ID != "static-0" {
		t.Errorf("ID: got %q, want %q", peers[0].ID, "static-0")
	}
	if peers[0].Address != "192.168.1.1:2379" {
		t.Errorf("Address: got %q, want %q", peers[0].Address, "192.168.1.1:2379")
	}
}

// TestStaticStrategy_Resolve_Deterministic verifies two Resolve calls on the
// same strategy return identical slices (no random ordering).
//
// Anti-bluff mutation: randomise the output slice in Resolve → order differs
// between calls, making the equality check flaky / failing.
func TestStaticStrategy_Resolve_Deterministic(t *testing.T) {
	seeds := []string{"b.example.com:2380", "a.example.com:2380"}
	s, err := NewStaticStrategy(seeds)
	if err != nil {
		t.Fatalf("NewStaticStrategy: %v", err)
	}
	// Errors are captured and fatal-ed so a broken Resolve returning (nil, err)
	// is caught immediately rather than producing nil slices that bypass assertions.
	p1, err := s.Resolve(context.Background())
	if err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	p2, err := s.Resolve(context.Background())
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if len(p1) != len(p2) {
		t.Fatalf("non-deterministic length: %d vs %d", len(p1), len(p2))
	}
	for i := range p1 {
		if p1[i] != p2[i] {
			t.Errorf("non-deterministic peer[%d]: %+v vs %+v", i, p1[i], p2[i])
		}
	}
	// Confirm lexicographic order: a.example.com < b.example.com
	if p1[0].Address != "a.example.com:2380" {
		t.Errorf("expected a.example.com first, got %q", p1[0].Address)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// StaticStrategy — validation / rejection
// ────────────────────────────────────────────────────────────────────────────

// TestNewStaticStrategy_RejectsMalformed verifies that malformed addresses cause
// NewStaticStrategy to return a non-nil error. The table covers both halves of
// the `host == "" || port == ""` guard independently:
//   - "host:" exercises port == "" (non-empty host, empty port).
//   - ":8080" exercises host == "" (empty host, non-empty port).
//
// Removing either half of the OR causes the corresponding subtest to fail.
//
// Anti-bluff mutation: remove the net.SplitHostPort validation call →
// NewStaticStrategy returns nil error for a bare hostname.
// Anti-bluff mutation (host guard): change `host == "" || port == ""` to
// `port == ""` → the ":8080" subtest returns nil error instead of an error.
func TestNewStaticStrategy_RejectsMalformed(t *testing.T) {
	cases := []struct {
		name  string
		seeds []string
	}{
		{"bare_hostname", []string{"localhost"}},
		{"empty_string", []string{""}},
		{"no_port", []string{"192.168.1.1"}},
		{"colon_only_no_port", []string{"host:"}},
		// Exercises the host == "" branch: SplitHostPort succeeds (port="8080"),
		// but host is empty — the guard must reject this.
		{"empty_host_with_port", []string{":8080"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewStaticStrategy(tc.seeds)
			if err == nil {
				t.Errorf("NewStaticStrategy(%v): expected error, got nil", tc.seeds)
			}
		})
	}
}

// TestNewStaticStrategy_RejectsEmptyList verifies that an empty seed list is
// rejected.
//
// Anti-bluff mutation: remove the len(seeds)==0 guard → returns nil error.
func TestNewStaticStrategy_RejectsEmptyList(t *testing.T) {
	_, err := NewStaticStrategy(nil)
	if err == nil {
		t.Fatal("expected error for nil seeds, got nil")
	}
	_, err = NewStaticStrategy([]string{})
	if err == nil {
		t.Fatal("expected error for empty seeds, got nil")
	}
}

// TestNewStaticStrategy_RejectsDuplicates verifies that duplicate seed
// addresses are rejected.
//
// Anti-bluff mutation: remove the duplicate-detection map → err is nil for a
// duplicated seed.
func TestNewStaticStrategy_RejectsDuplicates(t *testing.T) {
	seeds := []string{"10.0.0.1:2379", "10.0.0.2:2379", "10.0.0.1:2379"}
	_, err := NewStaticStrategy(seeds)
	if err == nil {
		t.Fatal("expected error for duplicate seed, got nil")
	}
}

// ────────────────────────────────────────────────────────────────────────────
// DNSSRVStrategy — CLAUDE-2 typed-unsupported check
// ────────────────────────────────────────────────────────────────────────────

// TestDNSSRVStrategy_Resolve_Unsupported asserts:
//  1. Resolve returns a nil peer slice (no fabricated peers).
//  2. The error wraps ErrUnsupported.
//
// Anti-bluff mutation: make Resolve return a fabricated peer
// → the nil-peers assertion fires AND errors.Is check may still pass,
// but the caller would silently use bogus data — which is the PASS-bluff.
// The len(peers)==0 assertion catches this directly.
func TestDNSSRVStrategy_Resolve_Unsupported(t *testing.T) {
	s := NewDNSSRVStrategy("_helix._tcp", "cluster.local")
	if s.Name() != "dns-srv" {
		t.Errorf("Name: got %q, want %q", s.Name(), "dns-srv")
	}
	peers, err := s.Resolve(context.Background())
	if len(peers) != 0 {
		t.Errorf("Resolve: got %d peers, want 0 (must not fabricate)", len(peers))
	}
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("Resolve: error %v does not wrap ErrUnsupported", err)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// MDNSStrategy — CLAUDE-2 typed-unsupported check
// ────────────────────────────────────────────────────────────────────────────

// TestMDNSStrategy_Resolve_Unsupported asserts:
//  1. Resolve returns a nil peer slice (no fabricated peers).
//  2. The error wraps ErrUnsupported.
//
// Anti-bluff mutation: make Resolve return a fabricated peer → the
// len(peers)==0 assertion fails, exposing the PASS-bluff.
func TestMDNSStrategy_Resolve_Unsupported(t *testing.T) {
	s := NewMDNSStrategy("_helix._tcp")
	if s.Name() != "mdns" {
		t.Errorf("Name: got %q, want %q", s.Name(), "mdns")
	}
	peers, err := s.Resolve(context.Background())
	if len(peers) != 0 {
		t.Errorf("Resolve: got %d peers, want 0 (must not fabricate)", len(peers))
	}
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("Resolve: error %v does not wrap ErrUnsupported", err)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Bootstrapper — fallthrough on unsupported
// ────────────────────────────────────────────────────────────────────────────

// TestBootstrapper_FallsThroughToStatic verifies that when an unsupported
// strategy precedes a working StaticStrategy, Bootstrap returns the static
// peers (skipping the unsupported one).
//
// Anti-bluff mutation: short-circuit on the FIRST strategy regardless of
// result (i.e. break even on error) → Bootstrap returns an error instead of
// the two static peers, causing the len(peers)==2 assertion to fail.
func TestBootstrapper_FallsThroughToStatic(t *testing.T) {
	static, err := NewStaticStrategy([]string{"10.0.0.1:2379", "10.0.0.2:2379"})
	if err != nil {
		t.Fatalf("NewStaticStrategy: %v", err)
	}

	b := NewBootstrapper(
		NewDNSSRVStrategy("_helix._tcp", "cluster.local"), // unsupported — must be skipped
		static,
	)

	peers, err := b.Bootstrap(context.Background())
	if err != nil {
		t.Fatalf("Bootstrap: unexpected error: %v", err)
	}
	if len(peers) != 2 {
		t.Fatalf("Bootstrap: got %d peers, want 2", len(peers))
	}
	// Confirm the exact addresses are present (not just a count check).
	wantAddrs := map[string]bool{"10.0.0.1:2379": true, "10.0.0.2:2379": true}
	for _, p := range peers {
		if !wantAddrs[p.Address] {
			t.Errorf("unexpected peer address %q", p.Address)
		}
	}
}

// countingStrategy wraps any Strategy and records how many times Resolve was
// called. It is used to pin that the Bootstrapper visits every strategy before
// giving up, rather than short-circuiting at the first failure.
type countingStrategy struct {
	inner Strategy
	calls int
}

func (c *countingStrategy) Name() string { return c.inner.Name() }
func (c *countingStrategy) Resolve(ctx context.Context) ([]Peer, error) {
	c.calls++
	return c.inner.Resolve(ctx)
}

// TestBootstrapper_MDNSThenStatic verifies the multi-skip path: both mDNS and
// DNS-SRV stubs must be attempted (and skipped) before the static strategy
// succeeds. Call counters on each stub strategy pin that removing either stub
// would change the observed call count, making this a real mutation-proof test.
//
// Anti-bluff mutation (loop break on first error): break after the first
// ErrUnsupported → mdnsC.calls==1, dnssrvC.calls==0; the call-count assertion
// `dnssrvC.calls != 1` fires.
// Anti-bluff mutation (remove mDNS from Bootstrapper): mdnsC is never called →
// mdnsC.calls==0; the assertion `mdnsC.calls != 1` fires.
func TestBootstrapper_MDNSThenStatic(t *testing.T) {
	static, err := NewStaticStrategy([]string{"172.16.0.1:2380"})
	if err != nil {
		t.Fatalf("NewStaticStrategy: %v", err)
	}

	mdnsC := &countingStrategy{inner: NewMDNSStrategy("_helix._tcp")}
	dnssrvC := &countingStrategy{inner: NewDNSSRVStrategy("_helix._tcp", "cluster.local")}

	b := NewBootstrapper(mdnsC, dnssrvC, static)

	peers, err := b.Bootstrap(context.Background())
	if err != nil {
		t.Fatalf("Bootstrap: unexpected error: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("Bootstrap: got %d peers, want 1", len(peers))
	}
	if peers[0].Address != "172.16.0.1:2380" {
		t.Errorf("peer address: got %q, want %q", peers[0].Address, "172.16.0.1:2380")
	}
	// Pin that BOTH failing strategies were visited before Bootstrap reached the
	// successful one. Removing either stub from the Bootstrapper causes its
	// counter to remain 0, which fires the assertion below.
	if mdnsC.calls != 1 {
		t.Errorf("mDNS strategy: expected 1 Resolve call, got %d", mdnsC.calls)
	}
	if dnssrvC.calls != 1 {
		t.Errorf("DNS-SRV strategy: expected 1 Resolve call, got %d", dnssrvC.calls)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Bootstrapper — all-unsupported returns joined error
// ────────────────────────────────────────────────────────────────────────────

// TestBootstrapper_AllUnsupported_ReturnsError verifies that when every
// strategy returns ErrUnsupported, Bootstrap returns (nil, err) where err
// wraps ErrUnsupported.
//
// Anti-bluff mutation: return nil error when all strategies fail (e.g.
// `return nil, nil` at the end of Bootstrap) → errors.Is assertion fires.
func TestBootstrapper_AllUnsupported_ReturnsError(t *testing.T) {
	b := NewBootstrapper(
		NewDNSSRVStrategy("_helix._tcp", "cluster.local"),
		NewMDNSStrategy("_helix._tcp"),
	)

	peers, err := b.Bootstrap(context.Background())
	if peers != nil {
		t.Errorf("Bootstrap: got %v peers, want nil", peers)
	}
	if err == nil {
		t.Fatal("Bootstrap: expected error, got nil")
	}
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("Bootstrap: error %v does not wrap ErrUnsupported", err)
	}
}

// TestBootstrapper_AllUnsupported_ErrorMentionsBothStrategies pins that both
// strategy names appear in the joined error message. This ensures the loop
// visits every strategy and does not short-circuit after the first failure —
// mutating the loop to break on first error causes the second strategy's name
// to be absent from the message.
//
// Anti-bluff mutation: break after first error in Bootstrap → joined error
// contains only "dns-srv", not "mdns"; strings.Contains("mdns") fires.
func TestBootstrapper_AllUnsupported_ErrorMentionsBothStrategies(t *testing.T) {
	b := NewBootstrapper(
		NewDNSSRVStrategy("_helix._tcp", "cluster.local"),
		NewMDNSStrategy("_helix._tcp"),
	)
	_, err := b.Bootstrap(context.Background())
	if err == nil {
		t.Fatal("Bootstrap: expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "dns-srv") {
		t.Errorf("joined error missing dns-srv strategy name: %q", msg)
	}
	if !strings.Contains(msg, "mdns") {
		t.Errorf("joined error missing mdns strategy name: %q", msg)
	}
}

// TestBootstrapper_EmptyStrategies_ReturnsNilError documents the contract that
// a Bootstrapper with zero strategies returns (nil, nil). This is a pure
// contract/specification check: with no strategies the loop body never executes,
// errs stays nil, and errors.Join(nil) is specified to return nil. The
// claimed mutation ('add unreachable code that errors on empty list') would be
// dead code, so this is NOT presented as a mutation-proof behavioral test — it
// is a regression guard ensuring no spurious error is ever added for the
// zero-strategy path.
func TestBootstrapper_EmptyStrategies_ReturnsNilError(t *testing.T) {
	b := NewBootstrapper()
	peers, err := b.Bootstrap(context.Background())
	if peers != nil {
		t.Errorf("Bootstrap: got peers %v, want nil", peers)
	}
	if err != nil {
		t.Errorf("Bootstrap: got error %v, want nil", err)
	}
}

// TestBootstrapper_ShortCircuitsOnFirstSuccess verifies that a strategy after
// the first successful one is never consulted.
//
// Anti-bluff mutation: continue iterating even after finding peers → the
// second static strategy's peers would overwrite or merge, changing the count.
func TestBootstrapper_ShortCircuitsOnFirstSuccess(t *testing.T) {
	first, err := NewStaticStrategy([]string{"10.0.0.1:2379"})
	if err != nil {
		t.Fatalf("NewStaticStrategy first: %v", err)
	}
	second, err := NewStaticStrategy([]string{"10.0.0.2:2379", "10.0.0.3:2379"})
	if err != nil {
		t.Fatalf("NewStaticStrategy second: %v", err)
	}

	// Wrap the second strategy to detect if it is ever called.
	secondC := &countingStrategy{inner: second}
	b := NewBootstrapper(first, secondC)
	peers, err := b.Bootstrap(context.Background())
	if err != nil {
		t.Fatalf("Bootstrap: unexpected error: %v", err)
	}
	// Only first strategy's single peer should be returned.
	if len(peers) != 1 {
		t.Fatalf("Bootstrap: got %d peers, want 1 (short-circuit)", len(peers))
	}
	if peers[0].Address != "10.0.0.1:2379" {
		t.Errorf("wrong peer: got %q, want %q", peers[0].Address, "10.0.0.1:2379")
	}
	// The second strategy must never have been called.
	if secondC.calls != 0 {
		t.Errorf("second strategy Resolve called %d time(s), want 0 (short-circuit broken)", secondC.calls)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Strategy interface compliance — runtime table-driven
// ────────────────────────────────────────────────────────────────────────────

// TestStrategyInterface_Compliance verifies at runtime that each concrete
// strategy type satisfies the Strategy interface with correct Name() values and
// that Resolve behaves consistently with documented contracts (no panic, correct
// error type for unsupported strategies, no fabricated peers).
//
// The compile-time interface check (var _ Strategy = (*T)(nil)) is retained
// inside the test body as well, but the table assertions are what provide
// runtime mutation-proof coverage.
//
// Anti-bluff mutation: rename Name() to name() on any strategy type →
// compile error (compile-time check) AND the name assertion in the table fires
// (runtime check).
// Anti-bluff mutation: make DNSSRVStrategy.Resolve return (nil, nil) instead of
// (nil, ErrUnsupported) → wantUnsupported==true assertion fires.
// Anti-bluff mutation: make MDNSStrategy.Resolve return a fabricated peer →
// wantNoPeers==true assertion fires.
func TestStrategyInterface_Compliance(t *testing.T) {
	// Compile-time guarantees — these are intentionally left in and documented
	// as such; they complement, not replace, the runtime assertions below.
	var _ Strategy = (*StaticStrategy)(nil)
	var _ Strategy = (*DNSSRVStrategy)(nil)
	var _ Strategy = (*MDNSStrategy)(nil)

	static, err := NewStaticStrategy([]string{"127.0.0.1:2379"})
	if err != nil {
		t.Fatalf("NewStaticStrategy: %v", err)
	}

	cases := []struct {
		strategy        Strategy
		wantName        string
		wantUnsupported bool // Resolve must return ErrUnsupported
		wantNoPeers     bool // Resolve must return zero peers
	}{
		{
			strategy:        static,
			wantName:        "static",
			wantUnsupported: false,
			wantNoPeers:     false,
		},
		{
			strategy:        NewDNSSRVStrategy("_helix._tcp", "cluster.local"),
			wantName:        "dns-srv",
			wantUnsupported: true,
			wantNoPeers:     true,
		},
		{
			strategy:        NewMDNSStrategy("_helix._tcp"),
			wantName:        "mdns",
			wantUnsupported: true,
			wantNoPeers:     true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.wantName, func(t *testing.T) {
			if got := tc.strategy.Name(); got != tc.wantName {
				t.Errorf("Name(): got %q, want %q", got, tc.wantName)
			}
			peers, resolveErr := tc.strategy.Resolve(context.Background())
			if tc.wantNoPeers && len(peers) != 0 {
				t.Errorf("Resolve(): got %d peers, want 0 (must not fabricate)", len(peers))
			}
			if !tc.wantNoPeers && len(peers) == 0 {
				t.Errorf("Resolve(): got 0 peers, want >0 for operational strategy")
			}
			if tc.wantUnsupported && !errors.Is(resolveErr, ErrUnsupported) {
				t.Errorf("Resolve(): error %v does not wrap ErrUnsupported", resolveErr)
			}
			if !tc.wantUnsupported && resolveErr != nil {
				t.Errorf("Resolve(): unexpected error %v", resolveErr)
			}
		})
	}
}
