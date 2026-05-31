package leader

import (
	"sync"
	"testing"
	"time"
)

// fakeClock is a deterministic, manually-advanced clock for testing failover and
// TTL expiry without any wall-clock dependence or time.Sleep.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// assert fakeClock satisfies the Clock interface.
var _ Clock = (*fakeClock)(nil)

func newLeaderWithClock(t *testing.T, id string, ttl time.Duration, clk Clock) *Election {
	t.Helper()
	e, err := NewElection(&Config{LocalID: id, TTL: ttl}, WithClock(clk))
	if err != nil {
		t.Fatalf("NewElection: %v", err)
	}
	return e
}

// TestWithClockInjectionDeterministic proves the clock is injectable and used by
// the lease/expiry logic — no wall-clock dependence.
func TestWithClockInjectionDeterministic(t *testing.T) {
	clk := newFakeClock(time.Unix(1_000_000, 0))
	e := newLeaderWithClock(t, "node-a", 100*time.Millisecond, clk)

	tok, err := e.AcquireLease()
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	if !e.LeaseValid() {
		t.Fatal("lease should be valid immediately after acquire")
	}
	if tok.Epoch == 0 {
		t.Fatal("fencing epoch must be non-zero after acquisition")
	}

	// Advance fake clock to exactly TTL boundary: still within because expiry is
	// strict ( now < expiry ). Advance just under TTL.
	clk.Advance(99 * time.Millisecond)
	if !e.LeaseValid() {
		t.Fatal("lease should still be valid just before TTL")
	}

	// Advance past TTL: lease must now be invalid, driven purely by the fake clock.
	clk.Advance(2 * time.Millisecond)
	if e.LeaseValid() {
		t.Fatal("lease must expire once fake clock passes TTL")
	}
}

// TestFencingEpochStrictlyIncreases is a mutation-paired test: every successful
// acquisition MUST issue a strictly greater fencing token than the previous one.
func TestFencingEpochStrictlyIncreases(t *testing.T) {
	clk := newFakeClock(time.Unix(2_000_000, 0))
	e := newLeaderWithClock(t, "node-a", 50*time.Millisecond, clk)

	var prev uint64
	for i := 0; i < 5; i++ {
		tok, err := e.AcquireLease()
		if err != nil {
			t.Fatalf("iter %d AcquireLease: %v", i, err)
		}
		if tok.Epoch <= prev {
			t.Fatalf("iter %d: epoch must strictly increase: got %d, prev %d", i, tok.Epoch, prev)
		}
		prev = tok.Epoch
		// Let the lease expire so the next acquisition is permitted.
		clk.Advance(60 * time.Millisecond)
	}
}

// TestAcquireBeforeExpiryRejected is the mutation pair for failover: a second
// acquire while the lease is still valid MUST be rejected, and the epoch MUST NOT
// advance (no fencing token burned).
func TestAcquireBeforeExpiryRejected(t *testing.T) {
	clk := newFakeClock(time.Unix(3_000_000, 0))
	e := newLeaderWithClock(t, "node-a", 100*time.Millisecond, clk)

	tok1, err := e.AcquireLease()
	if err != nil {
		t.Fatalf("first AcquireLease: %v", err)
	}

	// Advance, but not past TTL.
	clk.Advance(40 * time.Millisecond)

	_, err = e.AcquireLease()
	if err != ErrLeaseHeld {
		t.Fatalf("expected ErrLeaseHeld before expiry, got %v", err)
	}
	if e.Epoch() != tok1.Epoch {
		t.Fatalf("epoch must not advance on rejected acquire: got %d want %d", e.Epoch(), tok1.Epoch)
	}
}

// TestAcquireAfterExpirySucceedsWithHigherEpoch proves deterministic failover:
// once (and only once) the lease has expired, a new acquisition succeeds and the
// fencing token strictly increases.
func TestAcquireAfterExpirySucceedsWithHigherEpoch(t *testing.T) {
	clk := newFakeClock(time.Unix(4_000_000, 0))
	e := newLeaderWithClock(t, "node-a", 100*time.Millisecond, clk)

	tok1, err := e.AcquireLease()
	if err != nil {
		t.Fatalf("first AcquireLease: %v", err)
	}

	// Before expiry: rejected.
	clk.Advance(50 * time.Millisecond)
	if _, err := e.AcquireLease(); err != ErrLeaseHeld {
		t.Fatalf("expected rejection before expiry, got %v", err)
	}

	// After expiry: succeeds with higher epoch.
	clk.Advance(60 * time.Millisecond) // total 110ms > 100ms TTL
	tok2, err := e.AcquireLease()
	if err != nil {
		t.Fatalf("acquire after expiry should succeed: %v", err)
	}
	if tok2.Epoch <= tok1.Epoch {
		t.Fatalf("epoch must increase after failover: got %d, prev %d", tok2.Epoch, tok1.Epoch)
	}
}

// TestSplitBrainGuard proves two distinct nodes cannot both hold a valid lease
// with the same epoch via the shared split-brain registry. The second node may
// only acquire after the first node's lease has expired, and it gets a strictly
// higher fencing token — so no two valid leases ever share an epoch.
func TestSplitBrainGuard(t *testing.T) {
	clk := newFakeClock(time.Unix(5_000_000, 0))
	reg := NewLeaseRegistry()

	a := newLeaderWithClock(t, "node-a", 100*time.Millisecond, clk)
	a.SetLeaseRegistry(reg)
	b := newLeaderWithClock(t, "node-b", 100*time.Millisecond, clk)
	b.SetLeaseRegistry(reg)

	tokA, err := a.AcquireLease()
	if err != nil {
		t.Fatalf("node-a acquire: %v", err)
	}

	// node-b must NOT be able to acquire while node-a's lease is valid.
	if _, err := b.AcquireLease(); err != ErrLeaseHeld {
		t.Fatalf("split-brain: node-b acquired while node-a holds valid lease: %v", err)
	}

	// While both think about leadership, only node-a holds a valid lease.
	if !a.LeaseValid() {
		t.Fatal("node-a lease should be valid")
	}
	if b.LeaseValid() {
		t.Fatal("split-brain: node-b must not have a valid lease")
	}

	// Expire node-a's lease; now node-b can take over with a higher epoch.
	clk.Advance(101 * time.Millisecond)
	if a.LeaseValid() {
		t.Fatal("node-a lease should have expired")
	}
	tokB, err := b.AcquireLease()
	if err != nil {
		t.Fatalf("node-b takeover after expiry: %v", err)
	}
	if tokB.Epoch <= tokA.Epoch {
		t.Fatalf("takeover epoch must be strictly higher: B=%d A=%d", tokB.Epoch, tokA.Epoch)
	}

	// node-a can no longer hold a valid lease (registry owned by node-b now).
	if a.LeaseValid() {
		t.Fatal("split-brain: stale node-a must not be valid after node-b takeover")
	}
}

// TestSplitBrainNeverTwoValidSameEpoch is the mutation pair for the split-brain
// guard: across an aggressive contention loop, at no observed instant may two
// nodes both hold a valid lease.
func TestSplitBrainNeverTwoValidSameEpoch(t *testing.T) {
	clk := newFakeClock(time.Unix(6_000_000, 0))
	reg := NewLeaseRegistry()
	ttl := 100 * time.Millisecond

	a := newLeaderWithClock(t, "node-a", ttl, clk)
	a.SetLeaseRegistry(reg)
	b := newLeaderWithClock(t, "node-b", ttl, clk)
	b.SetLeaseRegistry(reg)

	for step := 0; step < 20; step++ {
		_, _ = a.AcquireLease()
		_, _ = b.AcquireLease()

		if a.LeaseValid() && b.LeaseValid() {
			t.Fatalf("step %d: split-brain — both nodes hold a valid lease simultaneously", step)
		}
		clk.Advance(40 * time.Millisecond)
	}
}

// TestStaleLeaderDetectableByFencingToken proves a consumer can reject a stale
// leader: the fencing token issued earlier is strictly less than the current
// epoch after failover, so monotonic comparison detects staleness.
func TestStaleLeaderDetectableByFencingToken(t *testing.T) {
	clk := newFakeClock(time.Unix(7_000_000, 0))
	reg := NewLeaseRegistry()
	ttl := 100 * time.Millisecond

	a := newLeaderWithClock(t, "node-a", ttl, clk)
	a.SetLeaseRegistry(reg)
	b := newLeaderWithClock(t, "node-b", ttl, clk)
	b.SetLeaseRegistry(reg)

	staleTok, _ := a.AcquireLease()

	clk.Advance(101 * time.Millisecond)
	freshTok, err := b.AcquireLease()
	if err != nil {
		t.Fatalf("b takeover: %v", err)
	}

	// Simulated consumer: tracks the highest fencing token seen.
	highest := freshTok.Epoch
	if staleTok.Epoch >= highest {
		t.Fatalf("stale token (%d) must be < highest seen (%d)", staleTok.Epoch, highest)
	}
	if !staleTok.IsStale(highest) {
		t.Fatal("stale token must report IsStale against a higher epoch")
	}
	if freshTok.IsStale(highest) {
		t.Fatal("fresh token must not report stale against itself")
	}
}

// TestExistingTTLPathUnaffected ensures the additive clock does not break the
// pre-existing term-based BecomeLeader path.
func TestExistingTTLPathUnaffected(t *testing.T) {
	clk := newFakeClock(time.Unix(8_000_000, 0))
	e := newLeaderWithClock(t, "node-a", 100*time.Millisecond, clk)
	if err := e.BecomeLeader(); err != nil {
		t.Fatalf("BecomeLeader: %v", err)
	}
	if !e.IsLeader() {
		t.Fatal("expected leader via existing path with injected clock")
	}
	// Drive expiry with the fake clock (IsLeader uses ttl*2 window).
	clk.Advance(201 * time.Millisecond)
	if e.IsLeader() {
		t.Fatal("expected IsLeader to expire under fake clock")
	}
}
