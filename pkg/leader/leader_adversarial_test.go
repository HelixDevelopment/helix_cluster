package leader

// Adversarial safety audit for the in-memory leader-election / fencing model
// (Election + LeaseRegistry in fencing.go). The dominant bug class here is a
// FENCING VIOLATION that enables split-brain: a stale leader accepted as valid,
// a fencing epoch that fails to strictly increase (so a returning old leader's
// writes are accepted), an acquire BEFORE expiry wrongly granted (two leaders),
// or an expiry off-by-one that opens an overlap window.
//
// All tests below use ONLY the clock-injection / in-memory LeaseRegistry seam —
// no real etcd, no wall-clock, no time.Sleep. fakeClock / newLeaderWithClock are
// defined in fencing_test.go.

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// CENTERPIECE A: EPOCH STRICTLY INCREASES across every acquisition path.
// ---------------------------------------------------------------------------

// TestAdversarialEpochStrictlyIncreasesAcrossManyCycles drives many
// expire/re-acquire cycles (single holder) and asserts the fencing epoch is
// strictly greater every single time — never reused, never flat. A non-strict
// epoch (>=) is the classic fencing hole that lets a returning old leader's
// write be accepted at a downstream resource.
func TestAdversarialEpochStrictlyIncreasesAcrossManyCycles(t *testing.T) {
	const ttl = 100 * time.Millisecond
	clk := newFakeClock(time.Unix(10_000_000, 0))
	e := newLeaderWithClock(t, "node-a", ttl, clk)

	seen := make(map[uint64]bool)
	var prev uint64
	for i := 0; i < 200; i++ {
		tok, err := e.AcquireLease()
		if err != nil {
			t.Fatalf("cycle %d: AcquireLease: %v", i, err)
		}
		if tok.Epoch <= prev {
			t.Fatalf("cycle %d: epoch not strictly increasing: got %d, prev %d", i, tok.Epoch, prev)
		}
		if seen[tok.Epoch] {
			t.Fatalf("cycle %d: epoch %d REUSED — fencing token must never repeat", i, tok.Epoch)
		}
		seen[tok.Epoch] = true
		// Election.Epoch() must agree with the issued token.
		if e.Epoch() != tok.Epoch {
			t.Fatalf("cycle %d: Election.Epoch()=%d disagrees with token epoch %d", i, e.Epoch(), tok.Epoch)
		}
		prev = tok.Epoch
		clk.Advance(ttl) // exactly to expiry: lease expired, next acquire permitted
	}
	if prev != 200 {
		t.Fatalf("expected 200 strictly increasing epochs, ended at %d", prev)
	}
}

// TestAdversarialEpochStrictlyIncreasesAcrossNodeTakeovers proves the epoch is a
// GLOBAL monotonic token across the whole election (not per-node): when leadership
// ping-pongs between two nodes through the shared registry, the epoch sequence is
// strictly increasing across the handoffs. A per-node or reset epoch would let a
// new term collide with an old one.
func TestAdversarialEpochStrictlyIncreasesAcrossNodeTakeovers(t *testing.T) {
	const ttl = 100 * time.Millisecond
	clk := newFakeClock(time.Unix(11_000_000, 0))
	reg := NewLeaseRegistry()

	a := newLeaderWithClock(t, "node-a", ttl, clk)
	a.SetLeaseRegistry(reg)
	b := newLeaderWithClock(t, "node-b", ttl, clk)
	b.SetLeaseRegistry(reg)

	nodes := []*Election{a, b}
	var prev uint64
	for i := 0; i < 50; i++ {
		n := nodes[i%2]
		tok, err := n.AcquireLease()
		if err != nil {
			t.Fatalf("handoff %d: AcquireLease: %v", i, err)
		}
		if tok.Epoch <= prev {
			t.Fatalf("handoff %d: global epoch not strictly increasing: got %d, prev %d", i, tok.Epoch, prev)
		}
		prev = tok.Epoch
		clk.Advance(ttl) // expire so the other node can take over
	}
}

// ---------------------------------------------------------------------------
// CENTERPIECE B: STALE LEADER REJECTED — old epoch is stale, current is not.
// ---------------------------------------------------------------------------

// TestAdversarialStaleLeaderRejectedAcrossGenerations builds a chain of N
// successive leadership generations and asserts the fencing contract a consumer
// relies on: EVERY older token is stale relative to the highest seen, and ONLY
// the current token is accepted. This is the predicate that fences a partitioned
// former leader's writes out of a downstream resource.
func TestAdversarialStaleLeaderRejectedAcrossGenerations(t *testing.T) {
	const ttl = 100 * time.Millisecond
	clk := newFakeClock(time.Unix(12_000_000, 0))
	e := newLeaderWithClock(t, "node-a", ttl, clk)

	var tokens []FencingToken
	for i := 0; i < 10; i++ {
		tok, err := e.AcquireLease()
		if err != nil {
			t.Fatalf("gen %d: AcquireLease: %v", i, err)
		}
		tokens = append(tokens, tok)
		clk.Advance(ttl)
	}

	highest := tokens[len(tokens)-1].Epoch

	// Every prior generation MUST be stale; a downstream resource fenced by the
	// highest epoch must reject all of them.
	for i := 0; i < len(tokens)-1; i++ {
		if !StaleLeader(tokens[i], highest) {
			t.Fatalf("gen %d token (epoch %d) MUST be stale vs highest %d — fencing hole accepts a former leader",
				i, tokens[i].Epoch, highest)
		}
		if !tokens[i].IsStale(highest) {
			t.Fatalf("gen %d IsStale disagrees with StaleLeader", i)
		}
	}
	// The current generation MUST be accepted (not stale against itself).
	if StaleLeader(tokens[len(tokens)-1], highest) {
		t.Fatalf("current token (epoch %d) wrongly reported stale vs highest %d — would fence the live leader",
			highest, highest)
	}
}

// TestAdversarialStaleLeaderTwoNodeFencing pins the cross-node version: after a
// real takeover via the registry, the deposed leader's token is stale and the new
// leader's token is fresh, evaluated by an independent consumer's highest-seen.
func TestAdversarialStaleLeaderTwoNodeFencing(t *testing.T) {
	const ttl = 100 * time.Millisecond
	clk := newFakeClock(time.Unix(13_000_000, 0))
	reg := NewLeaseRegistry()

	a := newLeaderWithClock(t, "node-a", ttl, clk)
	a.SetLeaseRegistry(reg)
	b := newLeaderWithClock(t, "node-b", ttl, clk)
	b.SetLeaseRegistry(reg)

	deposed, err := a.AcquireLease()
	if err != nil {
		t.Fatalf("node-a acquire: %v", err)
	}
	clk.Advance(ttl + time.Millisecond) // expire node-a's lease
	fresh, err := b.AcquireLease()
	if err != nil {
		t.Fatalf("node-b takeover: %v", err)
	}

	// Consumer observed node-b's higher epoch first; it must now fence node-a.
	highest := fresh.Epoch
	if !StaleLeader(deposed, highest) {
		t.Fatalf("deposed leader token (epoch %d) accepted as valid vs highest %d — SPLIT-BRAIN write would be admitted",
			deposed.Epoch, highest)
	}
	if StaleLeader(fresh, highest) {
		t.Fatalf("fresh leader token (epoch %d) wrongly fenced", fresh.Epoch)
	}
	// And the deposed node must report its lease invalid (registry owned by b).
	if a.LeaseValid() {
		t.Fatal("deposed node-a still reports a valid lease after takeover — split-brain")
	}
}

// ---------------------------------------------------------------------------
// CENTERPIECE C: ACQUIRE-BEFORE-EXPIRY REJECTED (no two simultaneous leaders).
// ---------------------------------------------------------------------------

// TestAdversarialNoTwoValidLeasesAtAnyInstant sweeps the clock across the whole
// lifecycle of a contended lease in fine steps and asserts that at NO observed
// instant do two nodes both hold a valid lease, AND that any contender that does
// manage to acquire only did so strictly after the incumbent's expiry, always
// with a strictly higher epoch.
func TestAdversarialNoTwoValidLeasesAtAnyInstant(t *testing.T) {
	const ttl = 100 * time.Millisecond
	clk := newFakeClock(time.Unix(14_000_000, 0))
	reg := NewLeaseRegistry()

	a := newLeaderWithClock(t, "node-a", ttl, clk)
	a.SetLeaseRegistry(reg)
	b := newLeaderWithClock(t, "node-b", ttl, clk)
	b.SetLeaseRegistry(reg)

	var maxEpoch uint64
	grant := func(name string, e *Election) {
		tok, err := e.AcquireLease()
		if err == nil {
			if tok.Epoch <= maxEpoch {
				t.Fatalf("%s acquired with non-increasing epoch %d (max so far %d)", name, tok.Epoch, maxEpoch)
			}
			maxEpoch = tok.Epoch
		} else if err != ErrLeaseHeld {
			t.Fatalf("%s unexpected error: %v", name, err)
		}
	}

	// Sweep ~5 lease lifetimes in 10ms steps; both nodes hammer AcquireLease.
	for step := 0; step < 50; step++ {
		grant("node-a", a)
		grant("node-b", b)
		if a.LeaseValid() && b.LeaseValid() {
			t.Fatalf("step %d (t=%v): SPLIT-BRAIN — both nodes hold a valid lease at once", step, clk.Now())
		}
		clk.Advance(10 * time.Millisecond)
	}
}

// TestAdversarialContenderRejectedThroughoutValidWindow nails the exclusion edge:
// while the incumbent's lease is valid (clock strictly < expiry), the contender's
// acquire is rejected at every sampled instant AND burns no epoch; at exactly
// expiry it succeeds with a strictly higher epoch.
func TestAdversarialContenderRejectedThroughoutValidWindow(t *testing.T) {
	const ttl = 100 * time.Millisecond
	clk := newFakeClock(time.Unix(15_000_000, 0))
	reg := NewLeaseRegistry()

	a := newLeaderWithClock(t, "node-a", ttl, clk)
	a.SetLeaseRegistry(reg)
	b := newLeaderWithClock(t, "node-b", ttl, clk)
	b.SetLeaseRegistry(reg)

	incumbent, err := a.AcquireLease()
	if err != nil {
		t.Fatalf("node-a acquire: %v", err)
	}
	expiry := incumbent.Expiry

	// Sample many instants strictly inside the valid window: contender rejected,
	// epoch must not advance.
	for off := time.Duration(0); off < ttl; off += 5 * time.Millisecond {
		clk.now = incumbent.Expiry.Add(-(ttl - off)) // somewhere in [acquire, expiry)
		if !clk.Now().Before(expiry) {
			continue
		}
		if _, err := b.AcquireLease(); err != ErrLeaseHeld {
			t.Fatalf("t=%v inside valid window: contender acquired (err=%v) — two leaders", clk.Now(), err)
		}
		if got := b.Epoch(); got != 0 {
			t.Fatalf("rejected contender burned an epoch (now %d) — epoch must not advance on rejection", got)
		}
	}

	// At exactly expiry the incumbent lease is expired: contender succeeds higher.
	clk.now = expiry
	tokB, err := b.AcquireLease()
	if err != nil {
		t.Fatalf("contender at exact expiry should succeed: %v", err)
	}
	if tokB.Epoch <= incumbent.Epoch {
		t.Fatalf("takeover epoch %d not strictly above incumbent %d", tokB.Epoch, incumbent.Epoch)
	}
}

// ---------------------------------------------------------------------------
// EXPIRY BOUNDARY: pin the inclusive/exclusive edge at expiry-1 / expiry / +1.
// ---------------------------------------------------------------------------

// TestAdversarialExpiryBoundaryExact pins the exact edge of lease validity and
// acquisition: valid iff now < expiry. At expiry-1ns valid; at expiry expired;
// at expiry+1ns expired. Acquire mirrors this (rejected strictly before expiry,
// granted at/after). An off-by-one here (now <= expiry) would open an overlap
// window where the incumbent is still "valid" yet a contender can also acquire.
func TestAdversarialExpiryBoundaryExact(t *testing.T) {
	const ttl = 100 * time.Millisecond
	clk := newFakeClock(time.Unix(16_000_000, 0))
	e := newLeaderWithClock(t, "node-a", ttl, clk)

	tok, err := e.AcquireLease()
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	expiry := tok.Expiry

	// expiry - 1ns : still valid, re-acquire by a DIFFERENT contender rejected.
	clk.now = expiry.Add(-time.Nanosecond)
	if !e.LeaseValid() {
		t.Fatal("lease must be valid at expiry-1ns")
	}

	// exactly expiry : expired (now < expiry is false).
	clk.now = expiry
	if e.LeaseValid() {
		t.Fatal("lease must be EXPIRED at exactly expiry (validity is now<expiry, exclusive) — inclusive edge opens overlap window")
	}

	// expiry + 1ns : expired.
	clk.now = expiry.Add(time.Nanosecond)
	if e.LeaseValid() {
		t.Fatal("lease must be expired at expiry+1ns")
	}
}

// TestAdversarialContenderRejectedAtExpiryMinusOne uses the registry so the edge
// is enforced cross-node: at expiry-1ns the contender is rejected; at expiry it
// is granted. Proves the exclusion boundary is the SAME as the validity boundary
// (no instant where incumbent valid AND contender admitted).
func TestAdversarialContenderRejectedAtExpiryMinusOne(t *testing.T) {
	const ttl = 100 * time.Millisecond
	clk := newFakeClock(time.Unix(17_000_000, 0))
	reg := NewLeaseRegistry()

	a := newLeaderWithClock(t, "node-a", ttl, clk)
	a.SetLeaseRegistry(reg)
	b := newLeaderWithClock(t, "node-b", ttl, clk)
	b.SetLeaseRegistry(reg)

	tok, err := a.AcquireLease()
	if err != nil {
		t.Fatalf("node-a acquire: %v", err)
	}

	clk.now = tok.Expiry.Add(-time.Nanosecond)
	if _, err := b.AcquireLease(); err != ErrLeaseHeld {
		t.Fatalf("contender admitted at expiry-1ns (err=%v) — overlap with valid incumbent", err)
	}
	if !a.LeaseValid() || b.LeaseValid() {
		t.Fatalf("at expiry-1ns incumbent must be sole valid holder (a=%v b=%v)", a.LeaseValid(), b.LeaseValid())
	}

	clk.now = tok.Expiry
	if _, err := b.AcquireLease(); err != nil {
		t.Fatalf("contender at exact expiry must succeed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// EDGES: first acquisition, same-holder renewal epoch behavior, empty id.
// ---------------------------------------------------------------------------

// TestAdversarialFirstAcquisitionEpochNonZero pins the documented contract that a
// fresh Election has epoch 0 and the FIRST acquisition yields epoch 1 (strictly
// positive), so a zero epoch can be used by consumers as "never led".
func TestAdversarialFirstAcquisitionEpochNonZero(t *testing.T) {
	clk := newFakeClock(time.Unix(18_000_000, 0))
	e := newLeaderWithClock(t, "node-a", 100*time.Millisecond, clk)

	if e.Epoch() != 0 {
		t.Fatalf("fresh election must report epoch 0, got %d", e.Epoch())
	}
	if e.LeaseValid() {
		t.Fatal("fresh election must not hold a valid lease")
	}
	tok, err := e.AcquireLease()
	if err != nil {
		t.Fatalf("first AcquireLease: %v", err)
	}
	if tok.Epoch != 1 {
		t.Fatalf("first acquisition must yield epoch 1, got %d", tok.Epoch)
	}
	// A token from a never-elected node (epoch 0) is stale vs any real leader.
	if !StaleLeader(FencingToken{Epoch: 0}, tok.Epoch) {
		t.Fatal("epoch-0 (never led) token must be stale vs a real leader")
	}
}

// TestAdversarialSameHolderRenewalBumpsEpochAndExtends documents the renewal
// behavior of the in-memory model: the SAME holder re-acquiring BEFORE expiry is
// treated as a renewal (NOT rejected), it bumps the epoch strictly, and it
// extends the expiry. This is the registry path (local registry-less mode rejects
// the holder's own early re-acquire — covered separately below). Pinning this
// prevents a regression that would either reject a legitimate renewal or, worse,
// reuse the epoch.
func TestAdversarialSameHolderRenewalBumpsEpochAndExtends(t *testing.T) {
	const ttl = 100 * time.Millisecond
	clk := newFakeClock(time.Unix(19_000_000, 0))
	reg := NewLeaseRegistry()

	a := newLeaderWithClock(t, "node-a", ttl, clk)
	a.SetLeaseRegistry(reg)

	tok1, err := a.AcquireLease()
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	clk.Advance(40 * time.Millisecond) // still inside the valid window
	tok2, err := a.AcquireLease()      // same holder => renewal, NOT ErrLeaseHeld
	if err != nil {
		t.Fatalf("same-holder renewal before expiry must succeed, got %v", err)
	}
	if tok2.Epoch <= tok1.Epoch {
		t.Fatalf("renewal must still strictly bump epoch: got %d, prev %d", tok2.Epoch, tok1.Epoch)
	}
	if !tok2.Expiry.After(tok1.Expiry) {
		t.Fatalf("renewal must extend expiry: new %v not after old %v", tok2.Expiry, tok1.Expiry)
	}
	// Even mid-renewal, a DIFFERENT contender is still locked out.
	b := newLeaderWithClock(t, "node-b", ttl, clk)
	b.SetLeaseRegistry(reg)
	if _, err := b.AcquireLease(); err != ErrLeaseHeld {
		t.Fatalf("contender admitted during incumbent renewal window (err=%v) — split-brain", err)
	}
}

// TestAdversarialLocalModeHolderEarlyReacquireRejected pins the documented
// difference of registry-less (local) mode: per AcquireLease's comment it rejects
// ANY acquisition (even by the same holder) while its own lease is still valid.
func TestAdversarialLocalModeHolderEarlyReacquireRejected(t *testing.T) {
	const ttl = 100 * time.Millisecond
	clk := newFakeClock(time.Unix(20_000_000, 0))
	e := newLeaderWithClock(t, "node-a", ttl, clk) // no registry => local mode

	tok1, err := e.AcquireLease()
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	clk.Advance(40 * time.Millisecond)
	if _, err := e.AcquireLease(); err != ErrLeaseHeld {
		t.Fatalf("local-mode early re-acquire must be rejected, got %v", err)
	}
	if e.Epoch() != tok1.Epoch {
		t.Fatalf("rejected local re-acquire must not bump epoch: got %d want %d", e.Epoch(), tok1.Epoch)
	}
}

// TestAdversarialEmptyHolderIDNoPanic documents behavior for a zero/empty id:
// construction with an empty LocalID is rejected at NewElection, but an empty
// holderID flowing through the registry directly must not panic and must still
// honor strict-epoch + single-valid-holder invariants.
func TestAdversarialEmptyHolderIDNoPanic(t *testing.T) {
	// NewElection rejects empty LocalID (documented).
	if _, err := NewElection(&Config{LocalID: ""}); err == nil {
		t.Fatal("NewElection with empty LocalID must error")
	}

	// Registry with an empty holder id: no panic, epoch strictly increases, and a
	// non-empty contender is fenced while the empty holder's lease is valid.
	const ttl = 100 * time.Millisecond
	now := time.Unix(21_000_000, 0)
	reg := NewLeaseRegistry()

	ep1, exp1, err := reg.acquire("", now, ttl)
	if err != nil {
		t.Fatalf("registry acquire empty id: %v", err)
	}
	if ep1 != 1 {
		t.Fatalf("first registry epoch must be 1, got %d", ep1)
	}
	if !reg.validFor("", now) {
		t.Fatal("empty holder must own a valid lease right after acquire")
	}
	// A different holder is locked out while empty-holder's lease is valid.
	if _, _, err := reg.acquire("other", now.Add(ttl/2), ttl); err != ErrLeaseHeld {
		t.Fatalf("contender admitted over valid empty-holder lease: %v", err)
	}
	// After expiry, takeover with strictly higher epoch.
	ep2, _, err := reg.acquire("other", exp1, ttl)
	if err != nil {
		t.Fatalf("takeover at expiry: %v", err)
	}
	if ep2 <= ep1 {
		t.Fatalf("takeover epoch %d not strictly above %d", ep2, ep1)
	}
}
