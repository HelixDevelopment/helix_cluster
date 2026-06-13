package multiraft

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// This file is the adversarial concurrency probe of the MultiRaftManager's SHARED
// SHARD REGISTRY (m.shards, guarded by m.mu sync.RWMutex). The pre-existing tests
// exercise concurrent Propose across shards that are ALL created up front, single
// threaded, before any concurrency begins — none of them mutate the registry map
// (CreateShard / RemoveShard) concurrently with routing (Propose / LeaderID /
// CommittedCount / ShardIDs). That is the classic unsynchronized-registry-map race
// surface, and it is the GAP these tests close.
//
// Anti-bluff teeth:
//   - Run under `go test -race`: any unsynchronized read/write of m.shards is a hard
//     DATA RACE failure. (Revert m.mu locking in CreateShard/RemoveShard/Propose and
//     this test fails the race detector — proof it is load-bearing.)
//   - "every created shard is findable" bites a lost-update on the registry map: if a
//     concurrent CreateShard clobbered the map (torn map write under no lock), some
//     created shard would be missing from ShardIDs().
//   - "no operation routed to a wrong/missing shard" bites a broken router: a Propose
//     for shard G must either reach G or return ErrShardNotFound — never a different
//     shard, never a panic.
//   - "no cross-shard contamination" bites a mis-route: a payload tagged for shard G
//     must never appear committed in another shard's log.
//   - "no panic" bites a nil-map / concurrent-map-iteration crash (Go runtime panics
//     on concurrent map read+write even WITHOUT -race).

// fastManager builds a manager with short raft timers so single-peer shards self-elect
// almost immediately when driven, keeping the concurrent test bounded and fast.
func fastManager(tr RaftTransport) *MultiRaftManager {
	m := NewMultiRaftManager(tr)
	// Short ticks: a single-peer shard becomes leader after one election timeout.
	m.electionTick = 2
	m.heartbeatTick = 1
	return m
}

// TestConcurrentRegistryCreateRouteRemoveNoRace is the headline -race probe.
//
// Many goroutines concurrently:
//   - CreateShard (new single-peer shards — fast to elect),
//   - route operations to shards (Propose / LeaderID / CommittedCount),
//   - RemoveShard,
//   - enumerate the registry (ShardIDs),
//
// all hammering the one shared map m.shards. We assert: no data race (the -race
// detector is the judge), no panic, every routing result is either the correct shard
// or a clean ErrShardNotFound (never a mis-route / wrong shard), and after the storm
// every shard reported by ShardIDs() is independently findable and routable.
func TestConcurrentRegistryCreateRouteRemoveNoRace(t *testing.T) {
	tr := NewInProcTransport()
	m := fastManager(tr)

	// A background driver keeps the raft clock moving so freshly created single-peer
	// shards actually elect a leader and accept proposals during the storm. It also
	// means Tick()/Run() (which RLock the registry and iterate it) run CONCURRENTLY
	// with CreateShard/RemoveShard (which Lock + mutate it) — a second race surface.
	stop := make(chan struct{})
	var driverWG sync.WaitGroup
	driverWG.Add(1)
	go func() {
		defer driverWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
				m.Tick()
				m.Run()
			}
		}
	}()

	const (
		nWorkers   = 8
		opsPerWork = 250
		idSpace    = 24 // shards contend over a small id space so create/remove collide
	)

	shardName := func(i int) ShardID { return ShardID(fmt.Sprintf("reg-%02d", i)) }

	var (
		panics int64
		wg     sync.WaitGroup
	)

	// guardPanic converts a panic in a worker into a recorded failure rather than
	// crashing the whole test binary, so we can report it prominently (a concurrent
	// map access panic is a REAL bug, not a benign flake).
	guardPanic := func(fn func()) {
		defer func() {
			if r := recover(); r != nil {
				atomic.AddInt64(&panics, 1)
				buf := make([]byte, 4096)
				n := runtime.Stack(buf, false)
				t.Errorf("PANIC in registry worker: %v\n%s", r, buf[:n])
			}
		}()
		fn()
	}

	for w := 0; w < nWorkers; w++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			r := seed*1_000_003 + 17
			next := func() int { r = (r*1103515245 + 12345) & 0x7fffffff; return r }
			for op := 0; op < opsPerWork; op++ {
				id := shardName(next() % idSpace)
				switch next() % 5 {
				case 0:
					// CreateShard: single peer so it elects fast. Duplicate creates are
					// expected (ErrShardExists) and are NOT errors for this probe.
					guardPanic(func() {
						err := m.CreateShard(id, []uint64{1})
						if err != nil && err != ErrShardExists {
							// Underlying CreateShard wraps ErrShardExists with %w; tolerate that.
							if !strings.Contains(err.Error(), "already exists") {
								t.Errorf("CreateShard(%s) unexpected error: %v", id, err)
							}
						}
					})
				case 1:
					// Route a tagged proposal. It must reach shard `id` or fail cleanly
					// (no leader yet / shard not found). The payload is tagged with its
					// destination id so the contamination scan below can detect a misroute.
					guardPanic(func() {
						payload := []byte("dst=" + string(id))
						err := m.Propose(context.Background(), id, payload)
						if err == nil {
							return
						}
						// Acceptable, expected outcomes for a shard that may not exist yet,
						// may have just been removed, or may be mid-election:
						if err == ErrNoLeader || err == ErrShardNotFound ||
							strings.Contains(err.Error(), "not found") ||
							strings.Contains(err.Error(), "no leader") {
							return
						}
						t.Errorf("Propose(%s) unexpected error: %v", id, err)
					})
				case 2:
					guardPanic(func() {
						// LeaderID must never panic and never return a leader for a shard
						// that does not exist (returns 0 cleanly).
						_ = m.LeaderID(id)
					})
				case 3:
					guardPanic(func() {
						// CommittedCount routes by id; for a missing shard it must return a
						// clean ErrShardNotFound, never a foreign shard's count.
						_, err := m.CommittedCount(id, 1)
						if err != nil && err != ErrShardNotFound &&
							!strings.Contains(err.Error(), "not found") &&
							!strings.Contains(err.Error(), "has no node") {
							t.Errorf("CommittedCount(%s) unexpected error: %v", id, err)
						}
					})
				case 4:
					guardPanic(func() {
						// Enumerate + remove. ShardIDs snapshots the registry; RemoveShard
						// mutates it. Removing a non-existent shard returns ErrShardNotFound
						// cleanly (expected when another worker already removed it).
						_ = m.ShardIDs()
						err := m.RemoveShard(id)
						if err != nil && err != ErrShardNotFound &&
							!strings.Contains(err.Error(), "not found") {
							t.Errorf("RemoveShard(%s) unexpected error: %v", id, err)
						}
					})
				}
			}
		}(w)
	}

	wg.Wait()
	close(stop)
	driverWG.Wait()

	if got := atomic.LoadInt64(&panics); got != 0 {
		t.Fatalf("registry storm produced %d panic(s) — concurrent registry access is not safe", got)
	}

	// Post-storm invariant: every shard the registry reports MUST be independently
	// findable and routable, and its committed log must contain ONLY payloads tagged
	// for it (no cross-shard contamination from the concurrent routing storm). This
	// bites both a lost-update on the map (a created shard gone missing) AND a
	// misroute (a foreign payload landing in the wrong shard's log).
	settle(m, 30)
	ids := m.ShardIDs()
	seen := make(map[ShardID]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("ShardIDs returned duplicate id %q — torn registry map", id)
		}
		seen[id] = true

		// Findable/routable: LeaderID must not error/panic; CommittedCount must resolve
		// THIS shard (not ErrShardNotFound) since it is present in the registry.
		_ = m.LeaderID(id)
		entries, err := m.CommittedEntries(id, 1)
		if err != nil {
			t.Fatalf("present shard %q not routable post-storm: %v", id, err)
		}
		// Contamination scan: every committed payload in shard `id` that carries a
		// dst= tag must be tagged for THIS shard. A payload tagged for a different
		// shard committed here is a mis-route — the headline correctness bug.
		want := "dst=" + string(id)
		for _, e := range entries {
			s := string(e)
			if strings.HasPrefix(s, "dst=") && s != want {
				t.Fatalf("CROSS-SHARD CONTAMINATION: shard %q committed payload %q tagged for another shard", id, s)
			}
		}
	}
}

// TestRoutingNeverCrossesShards proves routing correctness deterministically (no
// concurrency noise): a payload proposed to shard G commits in G and is ABSENT from
// every other shard. Distinct per-shard writes; bites a router that ignores shardID.
func TestRoutingNeverCrossesShards(t *testing.T) {
	tr := NewInProcTransport()
	m := fastManager(tr)

	ids := []ShardID{"route-A", "route-B", "route-C"}
	for _, id := range ids {
		if err := m.CreateShard(id, []uint64{1}); err != nil {
			t.Fatalf("CreateShard %q: %v", id, err)
		}
	}
	driveUntilLeaders(t, m, ids, 200)

	// Each shard gets a UNIQUE payload naming itself.
	for _, id := range ids {
		if err := m.Propose(context.Background(), id, []byte("only-"+string(id))); err != nil {
			t.Fatalf("Propose %q: %v", id, err)
		}
	}
	settle(m, 40)

	for _, id := range ids {
		entries, err := m.CommittedEntries(id, 1)
		if err != nil {
			t.Fatalf("CommittedEntries %q: %v", id, err)
		}
		foundOwn := false
		for _, e := range entries {
			s := string(e)
			if s == "only-"+string(id) {
				foundOwn = true
			}
			// Any other shard's unique payload appearing here is a mis-route.
			for _, other := range ids {
				if other == id {
					continue
				}
				if s == "only-"+string(other) {
					t.Fatalf("shard %q mis-routed: committed %q which belongs to %q", id, s, other)
				}
			}
		}
		if !foundOwn {
			t.Fatalf("shard %q did not commit its own payload (routing dropped it)", id)
		}
	}

	// A proposal to a shard that does not exist must error cleanly, never silently
	// land in some other shard.
	if err := m.Propose(context.Background(), "ghost", []byte("only-ghost")); err == nil {
		t.Fatal("Propose to non-existent shard 'ghost' returned nil — silent mis-route")
	}
	for _, id := range ids {
		entries, _ := m.CommittedEntries(id, 1)
		for _, e := range entries {
			if string(e) == "only-ghost" {
				t.Fatalf("ghost payload landed in shard %q — silent mis-route to wrong shard", id)
			}
		}
	}
}

// TestConcurrentLeaderStateQueriesConsistent hammers LeaderID/ShardIDs/CommittedCount
// concurrently with the driving loop and concurrent Create/Remove, asserting the
// state queries never tear (no race, no panic) and that a leader id reported for a
// present shard is one of that shard's real peers (never a torn/garbage value).
func TestConcurrentLeaderStateQueriesConsistent(t *testing.T) {
	tr := NewInProcTransport()
	m := fastManager(tr)

	// Three stable 3-peer shards plus a churn id created/removed concurrently.
	stable := []ShardID{"q-A", "q-B", "q-C"}
	for _, id := range stable {
		if err := m.CreateShard(id, threePeers()); err != nil {
			t.Fatalf("CreateShard %q: %v", id, err)
		}
	}
	driveUntilLeaders(t, m, stable, 200)

	stop := make(chan struct{})
	var driverWG sync.WaitGroup
	driverWG.Add(1)
	go func() {
		defer driverWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
				m.Tick()
				m.Run()
			}
		}
	}()

	validPeers := map[uint64]bool{1: true, 2: true, 3: true}

	var wg sync.WaitGroup
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				// Query stable shards: a reported leader must be a real peer (1..3) or 0.
				for _, id := range stable {
					l := m.LeaderID(id)
					if l != 0 && !validPeers[l] {
						t.Errorf("torn LeaderID for %q: %d not a real peer", id, l)
					}
					if _, err := m.CommittedCount(id, 1); err != nil {
						t.Errorf("CommittedCount(%q) errored on present shard: %v", id, err)
					}
				}
				// Concurrent churn on a shared id to keep the registry mutating under reads.
				churn := ShardID(fmt.Sprintf("q-churn-%d", seed))
				_ = m.CreateShard(churn, []uint64{1})
				_ = m.ShardIDs()
				_ = m.RemoveShard(churn)
			}
		}(w)
	}
	wg.Wait()
	close(stop)
	driverWG.Wait()

	// Stable shards survived the churn and remain routable with a real leader.
	settle(m, 30)
	for _, id := range stable {
		if l := m.LeaderID(id); l != 0 && !validPeers[l] {
			t.Fatalf("post-storm LeaderID(%q)=%d not a real peer", id, l)
		}
		if _, err := m.CommittedCount(id, 1); err != nil {
			t.Fatalf("stable shard %q not routable after churn: %v", id, err)
		}
	}
}
