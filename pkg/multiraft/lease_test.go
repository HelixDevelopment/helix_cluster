package multiraft

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// manualClock is an injected, test-controlled Clock. Advancing it is how we drive
// the lease decision path deterministically (CLAUDE-2: no time.Now in the path).
type manualClock struct{ t time.Time }

func (c *manualClock) Now() time.Time { return c.t }
func (c *manualClock) advance(d time.Duration) {
	c.t = c.t.Add(d)
}

// newLeaseHarness spins up a REAL single-shard 3-peer raft group, elects a
// leader, commits one value, then enables leaseholder reads with an injected
// clock and grants the lease to the leader. It returns everything a test needs.
func newLeaseHarness(t *testing.T) (*MultiRaftManager, *InProcTransport, *LeaseTracker, *manualClock, ShardID, uint64) {
	t.Helper()
	tr := NewInProcTransport()
	m := NewMultiRaftManager(tr)
	id := ShardID("lease-shard")
	if err := m.CreateShard(id, threePeers()); err != nil {
		t.Fatalf("CreateShard: %v", err)
	}
	driveUntilLeaders(t, m, []ShardID{id}, 200)

	// Commit a real value through raft so there is a latest committed value.
	if err := m.Propose(context.Background(), id, []byte("v1")); err != nil {
		t.Fatalf("Propose v1: %v", err)
	}
	settle(m, 50)

	leader := m.LeaderID(id)
	if leader == 0 {
		t.Fatalf("no leader elected")
	}

	clk := &manualClock{t: time.Unix(1_700_000_000, 0)}
	lt, err := NewLeaseTracker(clk, 5*time.Second)
	if err != nil {
		t.Fatalf("NewLeaseTracker: %v", err)
	}
	if err := m.EnableLeaseholderReads(lt); err != nil {
		t.Fatalf("EnableLeaseholderReads: %v", err)
	}
	if _, err := m.AcquireLease(id, leader); err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	return m, tr, lt, clk, id, leader
}

// pickFollower returns a live peer that is NOT the leader.
func pickFollower(leader uint64) uint64 {
	for _, p := range threePeers() {
		if p != leader {
			return p
		}
	}
	return 0
}

// Test #1 (closure criterion 1): a read on the leaseholder returns the latest
// committed value AND records NO new Raft proposal. The committed-entry count of
// the leaseholder is the proposal oracle — a real read must not append anything.
func TestLeaseholderLocalReadNoProposal(t *testing.T) {
	m, tr, _, clk, id, leader := newLeaseHarness(t)

	before, err := m.CommittedCount(id, leader)
	if err != nil {
		t.Fatalf("CommittedCount before: %v", err)
	}
	routedBefore := tr.RoutedReads()

	res, err := m.Read(id, leader, clk.Now())
	if err != nil {
		t.Fatalf("Read on leaseholder: %v", err)
	}
	if !res.Local {
		t.Fatalf("expected local fast-path read on leaseholder, got %+v", res)
	}
	if res.Routed {
		t.Fatalf("leaseholder read must not route over transport, got %+v", res)
	}
	if !bytes.Equal(res.Value, []byte("v1")) {
		t.Fatalf("read returned %q, want latest committed %q", res.Value, "v1")
	}

	after, err := m.CommittedCount(id, leader)
	if err != nil {
		t.Fatalf("CommittedCount after: %v", err)
	}
	if after != before {
		t.Fatalf("read appended Raft entries: before=%d after=%d (a local read MUST NOT propose)", before, after)
	}
	if tr.RoutedReads() != routedBefore {
		t.Fatalf("leaseholder read was routed over transport: routedReads %d -> %d", routedBefore, tr.RoutedReads())
	}

	// Settle a few cycles and re-check: still no new committed entries, proving the
	// read truly issued no proposal that would commit later.
	settle(m, 20)
	settled, _ := m.CommittedCount(id, leader)
	if settled != before {
		t.Fatalf("a proposal leaked from the read path: before=%d after-settle=%d", before, settled)
	}
}

// Test #2 (closure criterion 2): the same read issued on a FOLLOWER is forwarded
// to the leaseholder via the transport. We assert SendRead was invoked
// (RoutedReads increments) and the routed value is correct.
func TestFollowerReadRoutedToLeaseholder(t *testing.T) {
	m, tr, _, clk, id, leader := newLeaseHarness(t)

	follower := pickFollower(leader)
	if follower == 0 {
		t.Fatalf("could not pick a follower")
	}

	// Follower must NOT be a local leaseholder.
	if m.leases.IsLocalLeaseholder(id, follower, clk.Now()) {
		t.Fatalf("follower %d unexpectedly reports as local leaseholder", follower)
	}

	routedBefore := tr.RoutedReads()
	res, err := m.Read(id, follower, clk.Now())
	if err != nil {
		t.Fatalf("Read on follower: %v", err)
	}
	if !res.Routed {
		t.Fatalf("follower read should be routed, got %+v", res)
	}
	if res.Local {
		t.Fatalf("follower read must not be served locally, got %+v", res)
	}
	if res.Leaseholder != leader {
		t.Fatalf("follower read routed to %d, want leaseholder %d", res.Leaseholder, leader)
	}
	if tr.RoutedReads() != routedBefore+1 {
		t.Fatalf("SendRead not invoked: routedReads %d -> %d (expected +1)", routedBefore, tr.RoutedReads())
	}
	if !bytes.Equal(res.Value, []byte("v1")) {
		t.Fatalf("routed read returned %q, want %q", res.Value, "v1")
	}
}

// Test #3 (closure criterion 3 + MUTATION GUARD): after advancing the injected
// clock past lease expiry, the local fast path on the (former) leaseholder is
// REFUSED. With no other valid lease, the read surfaces ErrLeaseExpired and is
// NOT served locally.
//
// MUTATION GUARD: if IsLocalLeaseholder ignored expiry (always true for the
// holder), the leaseholder read below would still take the local fast path and
// this test would fail at the "must be refused" assertion.
func TestLeaseExpiryRefusesLocalFastPath(t *testing.T) {
	m, _, lt, clk, id, leader := newLeaseHarness(t)

	// Before expiry: the leaseholder serves locally.
	if !lt.IsLocalLeaseholder(id, leader, clk.Now()) {
		t.Fatalf("leaseholder should be valid before expiry")
	}
	res, err := m.Read(id, leader, clk.Now())
	if err != nil || !res.Local {
		t.Fatalf("pre-expiry leaseholder read should be local: res=%+v err=%v", res, err)
	}

	// Advance the injected clock PAST the 5s lease.
	clk.advance(6 * time.Second)

	// The decision point now refuses the local fast path.
	if lt.IsLocalLeaseholder(id, leader, clk.Now()) {
		t.Fatalf("IsLocalLeaseholder still true after expiry — expiry was ignored (mutation guard)")
	}

	// And a read by the former leaseholder is NOT served locally; with the lease
	// expired and not renewed it re-routes and surfaces ErrLeaseExpired.
	res2, err := m.Read(id, leader, clk.Now())
	if err == nil {
		t.Fatalf("expected expired-lease read to error, got value=%q local=%v routed=%v", res2.Value, res2.Local, res2.Routed)
	}
	if res2.Local {
		t.Fatalf("expired lease must NOT serve a local fast-path read, got %+v", res2)
	}

	// After re-acquiring the lease (clock has moved on), the fast path resumes —
	// proving the refusal was purely time-based, not a permanent disable.
	if _, err := m.AcquireLease(id, leader); err != nil {
		t.Fatalf("re-AcquireLease: %v", err)
	}
	res3, err := m.Read(id, leader, clk.Now())
	if err != nil {
		t.Fatalf("post-renew read: %v", err)
	}
	if !res3.Local {
		t.Fatalf("after lease renewal the leaseholder read should be local again, got %+v", res3)
	}
}

// Test: a follower read after the leaseholder's lease expires (and is not
// renewed) does not silently serve a stale value — it surfaces ErrLeaseExpired.
func TestFollowerReadRefusedWhenLeaseholderLeaseExpired(t *testing.T) {
	m, _, _, clk, id, leader := newLeaseHarness(t)
	follower := pickFollower(leader)

	clk.advance(10 * time.Second)
	res, err := m.Read(id, follower, clk.Now())
	if err == nil {
		t.Fatalf("expected ErrLeaseExpired routing to an expired leaseholder, got %+v", res)
	}
}

// Test: LeaseTracker.IsLocalLeaseholder honors BOTH holder identity and expiry,
// and Grant/Revoke behave. Pure unit test of the decision component.
func TestLeaseTrackerDecision(t *testing.T) {
	clk := &manualClock{t: time.Unix(0, 0)}
	lt, err := NewLeaseTracker(clk, time.Second)
	if err != nil {
		t.Fatalf("NewLeaseTracker: %v", err)
	}
	id := ShardID("s")

	// No lease yet.
	if lt.IsLocalLeaseholder(id, 1, clk.Now()) {
		t.Fatal("no lease should not be local leaseholder")
	}
	if _, ok := lt.Leaseholder(id); ok {
		t.Fatal("Leaseholder should report no lease")
	}

	lt.Grant(id, 1)
	if !lt.IsLocalLeaseholder(id, 1, clk.Now()) {
		t.Fatal("granted holder should be local leaseholder")
	}
	// Wrong node never holds the lease (holder-identity guard).
	if lt.IsLocalLeaseholder(id, 2, clk.Now()) {
		t.Fatal("non-holder must not be local leaseholder")
	}
	h, ok := lt.Leaseholder(id)
	if !ok || h != 1 {
		t.Fatalf("Leaseholder=%d,%v want 1,true", h, ok)
	}

	// Expiry guard: at exactly expiry the lease is no longer valid (Before is strict).
	clk.advance(time.Second)
	if lt.IsLocalLeaseholder(id, 1, clk.Now()) {
		t.Fatal("lease should be expired at expiry instant")
	}

	// Revoke removes the lease entirely.
	lt.Grant(id, 1)
	lt.Revoke(id)
	if _, ok := lt.Leaseholder(id); ok {
		t.Fatal("revoked lease should be gone")
	}
}

// Test: constructor rejects a non-positive duration so a lease cannot be born
// already expired.
func TestNewLeaseTrackerRejectsBadDuration(t *testing.T) {
	if _, err := NewLeaseTracker(nil, 0); err == nil {
		t.Fatal("expected error for zero duration")
	}
	if _, err := NewLeaseTracker(nil, -time.Second); err == nil {
		t.Fatal("expected error for negative duration")
	}
	// nil clock is allowed (falls back to wall clock).
	if _, err := NewLeaseTracker(nil, time.Second); err != nil {
		t.Fatalf("nil clock should be allowed: %v", err)
	}
}

// Test: AcquireLease refuses a non-leader (only the leader has the up-to-date
// committed log to back local reads).
func TestAcquireLeaseRequiresLeader(t *testing.T) {
	m, _, _, _, id, leader := newLeaseHarness(t)
	follower := pickFollower(leader)
	if _, err := m.AcquireLease(id, follower); err == nil {
		t.Fatalf("AcquireLease by follower %d should fail (not leader)", follower)
	}
}

// Test: SendRead to an unregistered leaseholder surfaces ErrNoReadHandler rather
// than a fake success.
func TestSendReadNoHandler(t *testing.T) {
	tr := NewInProcTransport()
	if _, err := tr.SendRead("missing", 7); err == nil {
		t.Fatal("expected ErrNoReadHandler for unregistered leaseholder")
	}
}

// Test (Important fix #1 — stale-leaseholder safety gap): stopping the leaseholder
// while its lease is still clock-valid MUST immediately revoke the lease AND
// unregister its read handler, so a follower's slow-path Read does NOT route a
// SendRead to the stopped node and return a frozen/stale committed value.
//
// MUTATION GUARD: if StopNode omitted m.revokeReadAccess (the bug being fixed),
// the follower Read below would still find the lease "valid" and SendRead would
// succeed against the stopped node's still-registered handler, so the test's
// "expected error" assertion would fail. Equivalently, inverting the holder check
// in revokeReadAccess (revoking nobody) reopens the gap and fails this test.
func TestStopLeaseholderRevokesLeaseAndReadHandler(t *testing.T) {
	m, tr, lt, clk, id, leader := newLeaseHarness(t)
	follower := pickFollower(leader)

	// Sanity: before stopping, a follower read routes successfully to the leader.
	routedBefore := tr.RoutedReads()
	pre, err := m.Read(id, follower, clk.Now())
	if err != nil || !pre.Routed {
		t.Fatalf("pre-stop follower read should route to leaseholder: res=%+v err=%v", pre, err)
	}
	if tr.RoutedReads() != routedBefore+1 {
		t.Fatalf("pre-stop SendRead not counted: %d -> %d", routedBefore, tr.RoutedReads())
	}

	// The lease is still clock-valid (we did NOT advance the clock).
	if !lt.IsLocalLeaseholder(id, leader, clk.Now()) {
		t.Fatalf("precondition: leaseholder lease should still be clock-valid")
	}

	// Stop the leaseholder while its lease is still valid by the clock.
	if err := m.StopNode(id, leader); err != nil {
		t.Fatalf("StopNode leaseholder: %v", err)
	}

	// The lease must have been revoked synchronously by the stop event...
	if _, ok := lt.Leaseholder(id); ok {
		t.Fatalf("stale-leaseholder gap: lease still recorded after stopping leaseholder")
	}
	if lt.IsLocalLeaseholder(id, leader, clk.Now()) {
		t.Fatalf("stale-leaseholder gap: stopped leaseholder still reports a valid lease")
	}

	// ...and a follower Read must NOT serve a stale value via the stopped node.
	// With the lease gone the read surfaces ErrNoLease (no fake/frozen value).
	res, err := m.Read(id, follower, clk.Now())
	if err == nil {
		t.Fatalf("follower read after stopping leaseholder returned %+v; expected error, not a stale value", res)
	}
	if res.Routed || res.Local {
		t.Fatalf("follower read after leaseholder stop must not be served: %+v", res)
	}

	// And even a direct SendRead to the stopped node's handler is gone.
	if _, err := tr.SendRead(id, leader); err == nil {
		t.Fatalf("stopped leaseholder's read handler should be unregistered (expected ErrNoReadHandler)")
	}
}

// Test (Important fix #2 — create-after-enable ordering): a shard created AFTER
// EnableLeaseholderReads must still get read handlers wired, so a follower Read
// against the new shard's leaseholder routes successfully instead of failing with
// ErrNoReadHandler.
//
// MUTATION GUARD: if CreateShard did not call registerShardReadHandlers for the
// already-enabled case (the bug), the follower Read on shard2 below would fail
// with ErrNoReadHandler and this test's success assertion would fail.
func TestCreateShardAfterEnableGetsReadHandler(t *testing.T) {
	tr := NewInProcTransport()
	m := NewMultiRaftManager(tr)

	shard1 := ShardID("s1")
	if err := m.CreateShard(shard1, threePeers()); err != nil {
		t.Fatalf("CreateShard s1: %v", err)
	}
	driveUntilLeaders(t, m, []ShardID{shard1}, 200)

	clk := &manualClock{t: time.Unix(1_700_000_000, 0)}
	lt, err := NewLeaseTracker(clk, 5*time.Second)
	if err != nil {
		t.Fatalf("NewLeaseTracker: %v", err)
	}
	// Enable leaseholder reads BEFORE shard2 exists — the snapshot moment that the
	// original bug captured.
	if err := m.EnableLeaseholderReads(lt); err != nil {
		t.Fatalf("EnableLeaseholderReads: %v", err)
	}

	// Now create a SECOND shard, after enable.
	shard2 := ShardID("s2")
	if err := m.CreateShard(shard2, threePeers()); err != nil {
		t.Fatalf("CreateShard s2: %v", err)
	}
	driveUntilLeaders(t, m, []ShardID{shard2}, 200)

	if err := m.Propose(context.Background(), shard2, []byte("v2")); err != nil {
		t.Fatalf("Propose to s2: %v", err)
	}
	settle(m, 50)

	leader2 := m.LeaderID(shard2)
	if leader2 == 0 {
		t.Fatalf("shard2 has no leader")
	}
	if _, err := m.AcquireLease(shard2, leader2); err != nil {
		t.Fatalf("AcquireLease s2: %v", err)
	}

	// A follower read on the later-created shard must route successfully — proving
	// its leaseholder's read handler WAS wired despite being created after enable.
	follower2 := pickFollower(leader2)
	routedBefore := tr.RoutedReads()
	res, err := m.Read(shard2, follower2, clk.Now())
	if err != nil {
		t.Fatalf("follower read on after-enable shard failed (read handler not wired?): %v", err)
	}
	if !res.Routed {
		t.Fatalf("follower read on shard2 should route, got %+v", res)
	}
	if tr.RoutedReads() != routedBefore+1 {
		t.Fatalf("SendRead not invoked for shard2: %d -> %d", routedBefore, tr.RoutedReads())
	}
	if !bytes.Equal(res.Value, []byte("v2")) {
		t.Fatalf("routed read on shard2 returned %q, want %q", res.Value, "v2")
	}
}

// Test: RemoveShard revokes the shard's lease and unregisters its read handlers,
// so a subsequent SendRead to the (now-gone) leaseholder returns ErrNoReadHandler
// rather than a stale value.
func TestRemoveShardRevokesLeaseAndReadHandlers(t *testing.T) {
	m, tr, lt, _, id, leader := newLeaseHarness(t)

	if _, ok := lt.Leaseholder(id); !ok {
		t.Fatalf("precondition: shard should have a lease")
	}
	if err := m.RemoveShard(id); err != nil {
		t.Fatalf("RemoveShard: %v", err)
	}
	if _, ok := lt.Leaseholder(id); ok {
		t.Fatalf("RemoveShard did not revoke the shard's lease")
	}
	if _, err := tr.SendRead(id, leader); err == nil {
		t.Fatalf("removed shard's read handler should be unregistered (expected ErrNoReadHandler)")
	}
}
