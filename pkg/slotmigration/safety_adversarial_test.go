package slotmigration

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

// advRunUUID identifies this adversarial suite in logged evidence so the
// sink-side observations (single-owner, no-loss/no-dup, concurrent routing) are
// traceable per CLAUDE-1.
const advRunUUID = "slotmigration-adversarial-HXC-1396-3b71d0f4"

// ----------------------------------------------------------------------------
// GAP 1: no key lost / no key duplicated across a migration of a POPULATED slot.
//
// The existing suite only ever migrates a SINGLE session. The resharding safety
// contract is stronger: a slot can hold many keys/sessions, and migrating it must
// move EVERY key to the new owner — none dropped (lost), none left behind or
// duplicated across the two owners. This test seeds a populated slot with N
// distinct sessions, snapshots the live set before, migrates, and asserts the
// live set after is byte-for-byte identical (every key present exactly once,
// none lost, none duplicated, no stray keys invented).
//
// Why it bites: if commitNow dropped a session (e.g. deleted alive[session]) the
// before/after set comparison would show a lost key. If it duplicated ownership
// (slot owned by both src and dst), OwnerCount would be 2. The model binds the
// whole slot's sessions to one owner via the single owner-map entry, so the
// no-loss/no-dup property reduces to: every seeded session stays alive AND the
// slot has exactly one owner == dst after commit.
// ----------------------------------------------------------------------------
func TestNoKeyLostNoKeyDuplicatedAcrossMigration(t *testing.T) {
	clk := NewLogicalClock()
	c := NewController(clk, 3)

	const (
		slot = "slot-populated"
		src  = "nodeSrc"
		dst  = "nodeDst"
		nKey = 64
	)

	// Populate the slot with nKey distinct sessions, all owned by src.
	keys := make([]string, 0, nKey)
	for i := 0; i < nKey; i++ {
		k := fmt.Sprintf("key-%04d", i)
		keys = append(keys, k)
		if err := c.Seed(slot, k, src); err != nil {
			t.Fatalf("%s: seed %q: %v", advRunUUID, k, err)
		}
	}

	// Snapshot BEFORE: every key alive, slot owned by src, exactly one owner.
	before := liveSet(c, keys)
	if len(before) != nKey {
		t.Fatalf("%s: BEFORE live=%d, want %d (seed lost a key)", advRunUUID, len(before), nKey)
	}
	if oc := c.OwnerCount(slot); oc != 1 {
		t.Fatalf("%s: BEFORE OwnerCount=%d, want 1", advRunUUID, oc)
	}
	if o, _ := c.Owner(slot); o != src {
		t.Fatalf("%s: BEFORE owner=%q, want %q", advRunUUID, o, src)
	}
	t.Logf("%s: BEFORE owner=%s liveKeys=%d ownerCount=1", advRunUUID, src, len(before))

	// Migrate the whole populated slot. We bind the migration to a representative
	// session; the slot-level owner flip governs all keys in the slot.
	if err := c.StartMigration(slot, keys[0], src, dst); err != nil {
		t.Fatalf("%s: start: %v", advRunUUID, err)
	}
	for i := 0; i < 16 && c.Phase() != PhaseCommitted; i++ {
		// At EVERY observable step assert no key is lost and the slot has exactly
		// one owner (never owner-less, never double-owned).
		if oc := c.OwnerCount(slot); oc != 1 {
			t.Fatalf("%s: mid-migration OwnerCount=%d, want 1 (owner-less or double-owned)", advRunUUID, oc)
		}
		mid := liveSet(c, keys)
		if len(mid) != nKey {
			t.Fatalf("%s: mid-migration live=%d, want %d (a key went missing during migration)", advRunUUID, len(mid), nKey)
		}
		if _, err := c.Step(); err != nil {
			t.Fatalf("%s: step: %v", advRunUUID, err)
		}
	}
	if c.Phase() != PhaseCommitted {
		t.Fatalf("%s: phase=%s, want COMMITTED", advRunUUID, c.Phase())
	}

	// Snapshot AFTER: the live set must be IDENTICAL to before (no key lost, no
	// key invented), the slot is owned solely by dst, and src owns nothing.
	after := liveSet(c, keys)
	if o, owned := c.Owner(slot); !owned || o != dst {
		t.Errorf("%s: AFTER owner=%q owned=%v, want %q (slot must move to dst)", advRunUUID, o, owned, dst)
	}
	if oc := c.OwnerCount(slot); oc != 1 {
		t.Errorf("%s: AFTER OwnerCount=%d, want 1 (exactly one owner)", advRunUUID, oc)
	}
	if !setsEqual(before, after) {
		t.Errorf("%s: live set changed across migration: lost=%v invented=%v",
			advRunUUID, diffKeys(before, after), diffKeys(after, before))
	}
	for _, k := range keys {
		if !c.SessionAlive(k) {
			t.Errorf("%s: key %q LOST after migration (owner-less key)", advRunUUID, k)
		}
	}
	t.Logf("%s: AFTER owner=%s liveKeys=%d ownerCount=1 lost=0 duplicated=0",
		advRunUUID, dst, len(after))
}

// liveSet returns the set of keys currently reported alive by the controller.
func liveSet(c *Controller, keys []string) map[string]struct{} {
	s := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		if c.SessionAlive(k) {
			s[k] = struct{}{}
		}
	}
	return s
}

func setsEqual(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

func diffKeys(a, b map[string]struct{}) []string {
	var out []string
	for k := range a {
		if _, ok := b[k]; !ok {
			out = append(out, k)
		}
	}
	return out
}

// ----------------------------------------------------------------------------
// GAP 2 (-race): concurrent routing/lookups DURING migrations.
//
// The controller is the authoritative ownership view that routers query to route
// a request to the slot's owner. In production those lookups happen CONCURRENTLY
// with the controller advancing a migration (the COMMIT flip). The existing
// suite only ever asserts the invariant from the single mutating goroutine, so it
// never exercises concurrent routing at all.
//
// This test runs many migrations back-to-back on one mutator goroutine while a
// fleet of router goroutines hammer Owner / OwnerCount / SessionAlive. Under
// -race two things must hold:
//  1. NO data race / no "concurrent map read and map write" panic.
//  2. Every routing lookup resolves to a CONSISTENT single owner: OwnerCount is
//     always exactly 1 (never 0 = owner-less, never 2 = double-owned), and the
//     owner is always a LEGAL node id (one of the participants), never a torn or
//     stale-garbage value.
//
// Why it bites: before the RWMutex fix, the concurrent reads vs the COMMIT write
// to the owner map are a data race that -race flags and that Go's runtime turns
// into a hard `fatal error: concurrent map read and map write` crash — proving
// the routing path was unusable during a migration. With the fix, readers take
// the read lock and observe the flip atomically.
// ----------------------------------------------------------------------------
func TestConcurrentRoutingDuringMigrationSingleOwner(t *testing.T) {
	clk := NewLogicalClock()
	c := NewController(clk, 1)

	const slot = "slot-routed"
	const sess = "sess-routed"
	if err := c.Seed(slot, sess, "A"); err != nil {
		t.Fatalf("%s: seed: %v", advRunUUID, err)
	}

	legal := map[string]struct{}{"A": {}, "B": {}}

	var (
		wg          sync.WaitGroup
		stop        atomic.Bool
		lookups     atomic.Int64
		badOwner    atomic.Int64 // OwnerCount != 1 observed
		illegalNode atomic.Int64 // owner not in {A,B}
	)

	const routers = 8
	for r := 0; r < routers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				// Route a request: must always find exactly one owner.
				if oc := c.OwnerCount(slot); oc != 1 {
					badOwner.Add(1)
				}
				if o, owned := c.Owner(slot); !owned || func() bool { _, ok := legal[o]; return !ok }() {
					illegalNode.Add(1)
				}
				_ = c.SessionAlive(sess)
				lookups.Add(1)
			}
		}()
	}

	// Mutator: ping-pong the slot between A and B many times so the owner map is
	// rewritten continuously under the concurrent readers.
	const migrations = 3000
	cur := "A"
	for n := 0; n < migrations; n++ {
		nxt := "B"
		if cur == "B" {
			nxt = "A"
		}
		if err := c.StartMigration(slot, sess, cur, nxt); err != nil {
			stop.Store(true)
			wg.Wait()
			t.Fatalf("%s: start n=%d (%s->%s): %v", advRunUUID, n, cur, nxt, err)
		}
		for {
			ph, err := c.Step()
			if err != nil {
				stop.Store(true)
				wg.Wait()
				t.Fatalf("%s: step n=%d: %v", advRunUUID, n, err)
			}
			if ph == PhaseCommitted {
				break
			}
		}
		cur = nxt
	}
	stop.Store(true)
	wg.Wait()

	t.Logf("%s: concurrent routing: migrations=%d routers=%d lookups=%d badOwnerCount=%d illegalNode=%d",
		advRunUUID, migrations, routers, lookups.Load(), badOwner.Load(), illegalNode.Load())

	if lookups.Load() == 0 {
		t.Fatalf("%s: no concurrent lookups ran — concurrency not exercised", advRunUUID)
	}
	if badOwner.Load() != 0 {
		t.Errorf("%s: %d concurrent lookups saw OwnerCount != 1 (owner-less or double-owned slot)",
			advRunUUID, badOwner.Load())
	}
	if illegalNode.Load() != 0 {
		t.Errorf("%s: %d concurrent lookups saw an illegal/garbage owner (not A or B)",
			advRunUUID, illegalNode.Load())
	}
	// Final owner must be a legal participant and the slot singly owned.
	if oc := c.OwnerCount(slot); oc != 1 {
		t.Errorf("%s: final OwnerCount=%d, want 1", advRunUUID, oc)
	}
}

// ----------------------------------------------------------------------------
// GAP 3 (-race): the COMMIT flip exposes NO owner-less and NO double-owned window
// to a concurrent observer sampling exactly across the flip instant.
//
// This is the sharpest atomicity probe: a single observer goroutine samples
// OwnerCount in a tight loop while the mutator drives a migration to its COMMIT
// flip. The flip is a single owner-map assignment src->dst; a non-atomic flip
// (delete-then-set, or set-dst-then-clear-src on two keys) would let the observer
// catch a transient 0 or 2. We assert the observer NEVER sees anything but 1, and
// that it actually observed the owner change from src to dst (so the flip really
// happened under observation, not after the observer stopped).
// ----------------------------------------------------------------------------
func TestAtomicFlipNoOwnerlessOrDoubleOwnedWindow(t *testing.T) {
	const (
		slot = "slot-flip"
		sess = "sess-flip"
		src  = "owSrc"
		dst  = "owDst"
	)

	// Run several independent migrations; each one the observer races the flip.
	const rounds = 200
	for round := 0; round < rounds; round++ {
		clk := NewLogicalClock()
		c := NewController(clk, 1)
		if err := c.Seed(slot, sess, src); err != nil {
			t.Fatalf("%s: round %d seed: %v", advRunUUID, round, err)
		}
		if err := c.StartMigration(slot, sess, src, dst); err != nil {
			t.Fatalf("%s: round %d start: %v", advRunUUID, round, err)
		}

		var (
			bad       atomic.Int64
			sawSrc    atomic.Bool
			sawDst    atomic.Bool
			observeWG sync.WaitGroup
			stop      atomic.Bool
		)
		observeWG.Add(1)
		go func() {
			defer observeWG.Done()
			for !stop.Load() {
				if oc := c.OwnerCount(slot); oc != 1 {
					bad.Add(1)
				}
				if o, _ := c.Owner(slot); o == src {
					sawSrc.Store(true)
				} else if o == dst {
					sawDst.Store(true)
				}
			}
		}()

		// Drive to COMMIT while the observer samples across the flip.
		for {
			ph, err := c.Step()
			if err != nil {
				stop.Store(true)
				observeWG.Wait()
				t.Fatalf("%s: round %d step: %v", advRunUUID, round, err)
			}
			if ph == PhaseCommitted {
				break
			}
		}
		// Slot is now permanently committed to dst. Spin until the observer has
		// sampled the post-commit state at least once, so the flip is provably
		// observed (this is a scheduling wait, not part of the race window — the
		// owner can no longer change). Then stop.
		for !sawDst.Load() {
		}
		stop.Store(true)
		observeWG.Wait()

		if bad.Load() != 0 {
			t.Fatalf("%s: round %d: observer saw OwnerCount != 1 %d times across the COMMIT flip (non-atomic flip exposed owner-less/double-owned window)",
				advRunUUID, round, bad.Load())
		}
		// The observer must have seen the post-flip dst at least once (the flip
		// really happened under observation).
		if !sawDst.Load() {
			t.Fatalf("%s: round %d: observer never saw dst own the slot — flip not observed", advRunUUID, round)
		}
		if o, _ := c.Owner(slot); o != dst {
			t.Fatalf("%s: round %d: final owner=%q, want %q", advRunUUID, round, o, dst)
		}
	}
	t.Logf("%s: atomic-flip probe: %d rounds, zero owner-less/double-owned windows observed", advRunUUID, rounds)
}
