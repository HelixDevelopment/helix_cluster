package bootstrap

// Adversarial / sink-side probes for pkg/bootstrap.
//
// This package is peer RENDEZVOUS / bootstrap discovery — not cluster-quorum
// formation. There is no Raft-style quorum count, no init-dependency DAG, no
// shared mutable readiness map, and no cross-goroutine shared state in the
// production types. The prompt's four canonical bug classes are therefore
// mapped honestly onto the contract that actually exists:
//
//	(a) QUORUM / SEED BOUNDARY  -> the minimum-seed boundary of NewStaticStrategy
//	    (exactly 1 seed forms a usable strategy; 0 seeds must be rejected), and
//	    the Bootstrapper "form vs not-form" verdict at the >0-peer boundary.
//	(b) DETERMINISM / TOTAL-ORDER -> NewStaticStrategy sorts seeds and assigns
//	    IDs; the contract claims a total order INDEPENDENT of input permutation.
//	(c) UNGUARDED SHARED STATE -> concurrent Resolve / Bootstrap under -race,
//	    plus the defensive-copy claim (a returned slice mutated by a caller must
//	    not corrupt internal peer state or another caller's view).
//	(d) FAIL-OPEN / IDEMPOTENCY -> a strategy that returns (zero peers, nil) must
//	    NOT be treated as a formed cluster (fail-open); Bootstrap run twice must
//	    yield identical verdicts (idempotent, no double-init drift).
//
// Every assertion is SINK-SIDE: the actual formed/not-formed verdict, the actual
// (ID,Address) tuples, conservation of peer count, never "did not panic".

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// ─────────────────────────────────────────────────────────────────────────────
// (a) SEED / FORMATION BOUNDARY — prove the verdict at EXACTLY the boundary
// ─────────────────────────────────────────────────────────────────────────────

// TestAdv_SeedBoundary_ExactlyOne pins the minimum-seed boundary of the only
// operational strategy. The "cluster forms" analog here is: a StaticStrategy can
// be constructed and yields >0 peers. The boundary is exactly 1.
//
//   - 0 seeds  -> MUST NOT form (NewStaticStrategy returns error).
//   - 1 seed   -> MUST form     (no error, Resolve yields exactly 1 peer).
//
// An off-by-one that accepted 0 seeds (forming an empty/peerless strategy =
// split-brain analog: a node that thinks it can bootstrap with no one) is caught
// by the n==0 must-error arm; an off-by-one that rejected the legitimate single
// seed is caught by the n==1 must-form arm.
func TestAdv_SeedBoundary_ExactlyOne(t *testing.T) {
	// Below boundary: zero seeds must NOT form.
	if _, err := NewStaticStrategy(nil); err == nil {
		t.Fatal("seed boundary: 0 seeds formed a strategy (want rejection) — empty-bootstrap/split-brain analog")
	}
	if _, err := NewStaticStrategy([]string{}); err == nil {
		t.Fatal("seed boundary: empty-slice seeds formed a strategy (want rejection)")
	}

	// Exactly at boundary: one seed must form and yield exactly one peer.
	s, err := NewStaticStrategy([]string{"10.0.0.1:2379"})
	if err != nil {
		t.Fatalf("seed boundary: 1 seed rejected (want formation): %v", err)
	}
	peers, err := s.Resolve(context.Background())
	if err != nil {
		t.Fatalf("seed boundary: Resolve at n==1: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("seed boundary: n==1 yielded %d peers, want exactly 1", len(peers))
	}
	if peers[0].Address != "10.0.0.1:2379" {
		t.Errorf("seed boundary: peer addr = %q, want %q", peers[0].Address, "10.0.0.1:2379")
	}
}

// TestAdv_FormationVerdict_AtZeroPeerBoundary pins the Bootstrapper formation
// verdict at the >0-peer boundary. A strategy yielding exactly 0 peers must NOT
// be accepted as "formed"; a strategy yielding exactly 1 peer MUST be accepted.
// This proves the boundary of the `len(peers) > 0` gate is at 1, not 0.
func TestAdv_FormationVerdict_AtZeroPeerBoundary(t *testing.T) {
	// zeroPeerStrategy yields (empty, nil): the fail-open trap.
	zero := fakeStrategy{name: "zero", peers: []Peer{}, err: nil}
	// onePeerStrategy yields exactly one peer: the minimal "formed" cluster.
	one := fakeStrategy{name: "one", peers: []Peer{{ID: "x", Address: "10.0.0.9:1"}}, err: nil}

	// Below the formation boundary: zero peers alone must NOT form a cluster.
	bZero := NewBootstrapper(zero)
	p, err := bZero.Bootstrap(context.Background())
	if len(p) != 0 {
		t.Fatalf("formation boundary: zero-peer strategy was accepted as formed (got %d peers) — FAIL-OPEN", len(p))
	}
	if err == nil {
		t.Fatal("formation boundary: zero-peer-only bootstrap returned nil error (want surfaced miss)")
	}

	// Exactly at the boundary: one peer MUST form.
	bOne := NewBootstrapper(one)
	p, err = bOne.Bootstrap(context.Background())
	if err != nil {
		t.Fatalf("formation boundary: one-peer strategy not formed: %v", err)
	}
	if len(p) != 1 {
		t.Fatalf("formation boundary: one-peer strategy yielded %d, want exactly 1", len(p))
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// (b) DETERMINISM / TOTAL ORDER — many runs, permuted inputs, identical sink
// ─────────────────────────────────────────────────────────────────────────────

// TestAdv_TotalOrder_PermutationInvariant is the strongest determinism probe.
// The doc contract on NewStaticStrategy states IDs are assigned "in sorted seed
// order so results are deterministic across calls REGARDLESS OF THE ORDER seeds
// were originally supplied."
//
// We feed several DISTINCT permutations of the SAME seed set and assert the
// resulting (ID,Address) tuple sequence is byte-identical for every permutation.
// A nondeterministic order (e.g. dropping the sort and leaking input/map order)
// would make at least one permutation differ — failing the equality sink.
func TestAdv_TotalOrder_PermutationInvariant(t *testing.T) {
	base := []string{
		"10.0.0.3:7000",
		"10.0.0.1:7000",
		"10.0.0.10:7000",
		"10.0.0.2:7000",
		"a.example.com:9",
	}

	// Canonical expectation: build from one strategy, then require every
	// permutation to reproduce it exactly.
	canonStrat, err := NewStaticStrategy(base)
	if err != nil {
		t.Fatalf("NewStaticStrategy(base): %v", err)
	}
	canon, err := canonStrat.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve(base): %v", err)
	}

	// A few hand-rolled permutations (deterministic, no randomness needed to
	// expose a deterministic-order violation: distinct input orders suffice).
	perms := [][]string{
		{"a.example.com:9", "10.0.0.2:7000", "10.0.0.10:7000", "10.0.0.1:7000", "10.0.0.3:7000"},
		{"10.0.0.10:7000", "10.0.0.3:7000", "a.example.com:9", "10.0.0.1:7000", "10.0.0.2:7000"},
		{"10.0.0.2:7000", "10.0.0.1:7000", "10.0.0.3:7000", "a.example.com:9", "10.0.0.10:7000"},
		{"10.0.0.1:7000", "10.0.0.10:7000", "10.0.0.2:7000", "a.example.com:9", "10.0.0.3:7000"},
	}

	for pi, perm := range perms {
		// Run each permutation MANY times to catch per-call nondeterminism too.
		for iter := 0; iter < 200; iter++ {
			st, err := NewStaticStrategy(perm)
			if err != nil {
				t.Fatalf("perm %d: NewStaticStrategy: %v", pi, err)
			}
			got, err := st.Resolve(context.Background())
			if err != nil {
				t.Fatalf("perm %d: Resolve: %v", pi, err)
			}
			if len(got) != len(canon) {
				t.Fatalf("perm %d iter %d: len(got)=%d, len(canon)=%d", pi, iter, len(got), len(canon))
			}
			for i := range got {
				if got[i] != canon[i] {
					t.Fatalf("perm %d iter %d: total-order violation at [%d]: got %+v, canon %+v",
						pi, iter, i, got[i], canon[i])
				}
			}
		}
	}

	// Independently assert the canonical order is the lexicographic total order
	// AND that IDs are a stable 0..n-1 prefix bound to that order — so the test
	// pins the ACTUAL ordering, not merely "two runs agree".
	// Byte-lexicographic order (sort.Strings). At the char after "10.0.0.1",
	// '0' (0x30) < ':' (0x3A), so "10.0.0.10:..." sorts BEFORE "10.0.0.1:...".
	wantAddrs := []string{
		"10.0.0.10:7000",
		"10.0.0.1:7000",
		"10.0.0.2:7000",
		"10.0.0.3:7000",
		"a.example.com:9",
	}
	for i, w := range wantAddrs {
		if canon[i].Address != w {
			t.Errorf("total order [%d]: addr=%q, want %q", i, canon[i].Address, w)
		}
		if canon[i].ID != fmt.Sprintf("static-%d", i) {
			t.Errorf("total order [%d]: id=%q, want %q", i, canon[i].ID, fmt.Sprintf("static-%d", i))
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// (c) UNGUARDED SHARED STATE — concurrency under -race + defensive-copy proof
// ─────────────────────────────────────────────────────────────────────────────

// TestAdv_ConcurrentResolve_NoRaceNoDrift hammers Resolve and Bootstrap from
// many goroutines. Run under -race this fails on any unsynchronised access; the
// value assertions additionally pin that every concurrent caller observes the
// SAME conserved set of peers (no lost/torn reads).
func TestAdv_ConcurrentResolve_NoRaceNoDrift(t *testing.T) {
	seeds := []string{"10.0.0.1:1", "10.0.0.2:2", "10.0.0.3:3", "10.0.0.4:4"}
	s, err := NewStaticStrategy(seeds)
	if err != nil {
		t.Fatalf("NewStaticStrategy: %v", err)
	}
	b := NewBootstrapper(s)

	const goroutines = 64
	var wg sync.WaitGroup
	errCh := make(chan error, goroutines*2)

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				rp, err := s.Resolve(context.Background())
				if err != nil {
					errCh <- fmt.Errorf("Resolve: %w", err)
					return
				}
				if len(rp) != len(seeds) {
					errCh <- fmt.Errorf("Resolve: got %d peers, want %d", len(rp), len(seeds))
					return
				}
				bp, err := b.Bootstrap(context.Background())
				if err != nil {
					errCh <- fmt.Errorf("Bootstrap: %w", err)
					return
				}
				if len(bp) != len(seeds) {
					errCh <- fmt.Errorf("Bootstrap: got %d peers, want %d", len(bp), len(seeds))
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for e := range errCh {
		t.Error(e)
	}
}

// TestAdv_ResolveDefensiveCopy proves Resolve hands back a defensive copy: a
// caller mutating the returned slice MUST NOT corrupt the strategy's internal
// state or a subsequent caller's view. If Resolve aliased its internal slice,
// the second Resolve would observe the poisoned element — a cross-caller shared
// -state leak even without goroutines.
func TestAdv_ResolveDefensiveCopy(t *testing.T) {
	s, err := NewStaticStrategy([]string{"10.0.0.1:1", "10.0.0.2:2"})
	if err != nil {
		t.Fatalf("NewStaticStrategy: %v", err)
	}
	first, err := s.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Poison the returned slice.
	for i := range first {
		first[i] = Peer{ID: "POISON", Address: "0.0.0.0:0"}
	}
	// A fresh Resolve must be pristine.
	second, err := s.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve#2: %v", err)
	}
	for i, p := range second {
		if p.ID == "POISON" || p.Address == "0.0.0.0:0" {
			t.Fatalf("defensive-copy violation: caller mutation leaked into internal state at [%d]: %+v", i, p)
		}
	}
	if second[0].Address != "10.0.0.1:1" || second[1].Address != "10.0.0.2:2" {
		t.Errorf("post-poison view corrupted: %+v", second)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// (d) FAIL-OPEN / IDEMPOTENCY
// ─────────────────────────────────────────────────────────────────────────────

// TestAdv_NoFailOpen_ZeroPeerMissIsNotFormation pins that a strategy reporting
// (zero peers, nil error) — the classic "ready before deps are ready" shape — is
// NOT accepted as a formed cluster, and crucially does NOT mask a later working
// strategy. Sink: the FINAL returned peer set must be the working strategy's,
// and a zero-peer-only run must surface an error (not silently "succeed empty").
func TestAdv_NoFailOpen_ZeroPeerMissIsNotFormation(t *testing.T) {
	zero := fakeStrategy{name: "zero", peers: []Peer{}, err: nil}
	working, err := NewStaticStrategy([]string{"172.16.0.1:2380"})
	if err != nil {
		t.Fatalf("NewStaticStrategy: %v", err)
	}

	// zero precedes working: the zero-peer miss must be skipped, not declared ready.
	b := NewBootstrapper(zero, working)
	peers, err := b.Bootstrap(context.Background())
	if err != nil {
		t.Fatalf("Bootstrap: unexpected error: %v", err)
	}
	if len(peers) != 1 || peers[0].Address != "172.16.0.1:2380" {
		t.Fatalf("fail-open: zero-peer miss leaked into verdict; got %+v, want the working peer", peers)
	}

	// zero alone: must report a surfaced miss, never a silent empty success.
	bAlone := NewBootstrapper(zero)
	p2, err2 := bAlone.Bootstrap(context.Background())
	if len(p2) != 0 {
		t.Fatalf("fail-open: zero-only bootstrap fabricated %d peers", len(p2))
	}
	if err2 == nil {
		t.Fatal("fail-open: zero-only bootstrap returned (nil,nil) — declared ready with zero peers")
	}
}

// TestAdv_Idempotent_DoubleBootstrap pins that running Bootstrap twice yields the
// IDENTICAL verdict — no double-init drift, no accumulation, no second "cluster".
func TestAdv_Idempotent_DoubleBootstrap(t *testing.T) {
	s, err := NewStaticStrategy([]string{"10.1.1.2:5", "10.1.1.1:5"})
	if err != nil {
		t.Fatalf("NewStaticStrategy: %v", err)
	}
	b := NewBootstrapper(s)

	run1, err := b.Bootstrap(context.Background())
	if err != nil {
		t.Fatalf("Bootstrap#1: %v", err)
	}
	run2, err := b.Bootstrap(context.Background())
	if err != nil {
		t.Fatalf("Bootstrap#2: %v", err)
	}
	if len(run1) != len(run2) {
		t.Fatalf("idempotency: run1 has %d peers, run2 has %d", len(run1), len(run2))
	}
	for i := range run1 {
		if run1[i] != run2[i] {
			t.Errorf("idempotency: peer[%d] drift: %+v vs %+v", i, run1[i], run2[i])
		}
	}
	// Sorted total order: 10.1.1.1 before 10.1.1.2.
	if run1[0].Address != "10.1.1.1:5" {
		t.Errorf("idempotency: first peer %q, want 10.1.1.1:5", run1[0].Address)
	}
}

// TestAdv_MissingDependencyNotSatisfied pins that an unsupported strategy (a
// genuinely-absent dependency) is never silently treated as satisfied: it does
// NOT contribute peers, and if it is the ONLY strategy, Bootstrap fails with an
// error wrapping ErrUnsupported rather than declaring an empty cluster ready.
func TestAdv_MissingDependencyNotSatisfied(t *testing.T) {
	b := NewBootstrapper(NewDNSSRVStrategy("_helix._tcp", "cluster.local"))
	peers, err := b.Bootstrap(context.Background())
	if len(peers) != 0 {
		t.Fatalf("missing-dep: unsupported strategy fabricated %d peers", len(peers))
	}
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("missing-dep: error %v does not wrap ErrUnsupported (dep silently satisfied?)", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// test double
// ─────────────────────────────────────────────────────────────────────────────

// fakeStrategy is a controllable Strategy for probing the Bootstrapper's
// formation verdict at the zero-peer boundary and fail-open paths.
type fakeStrategy struct {
	name  string
	peers []Peer
	err   error
}

func (f fakeStrategy) Name() string { return f.name }
func (f fakeStrategy) Resolve(_ context.Context) ([]Peer, error) {
	return f.peers, f.err
}
