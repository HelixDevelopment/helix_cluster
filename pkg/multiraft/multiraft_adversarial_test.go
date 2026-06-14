package multiraft

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"go.etcd.io/raft/v3"
	pb "go.etcd.io/raft/v3/raftpb"
)

// This file is an adversarial, mutation-verified audit of the multiraft Raft-safety
// contract. The load-bearing invariant under test is the durability rule:
//
//	a PERSIST FAILURE MUST NOT advance committed/applied state.
//
// Raft is only safe if a Ready is durably persisted (HardState + log Entries) to
// stable storage BEFORE the driver calls Advance — which is what Steps the
// storage-append-response that lets raft mark the data committed/applied. Advancing
// past an un-persisted Ready tells raft data is durable when it is not, risking
// acknowledged writes that vanish on restart and log divergence across peers.
//
// The existing durability_test.go already pins the single-node HardState-fault case.
// These tests bite additional, independent facets the suite did not yet cover:
//
//   - APPEND-only fault (HardState persists, log Append fails): the entries-durability
//     half in isolation — the doomed entry must not commit, and must commit after heal.
//   - cross-shard persist ISOLATION: a persisting-fault shard must not defer or corrupt
//     a healthy shard's commits in the same driving passes.
//   - order + exactly-once after a park/retry cycle: the healed entry commits exactly
//     once, in log order, with no duplicate and no gap.
//   - concurrent multi-node commit EXACTLY-ONCE under -race with a fault that heals
//     mid-flight: every proposal commits exactly once on the leader, no race.
//   - Run/Tick must not stall or spin forever while a node is parked.

// hardStateOnlyFaultStorage wraps a real MemoryStorage and fails ONLY Append (the
// log-entry durability path) while letting SetHardState through. This isolates the
// "entries are not durable" half of the invariant from the HardState half that
// durability_test.go already exercises. A real WAL can fail an fsync of the log
// segment while an earlier metadata write succeeded, so this is a real failure mode.
type appendOnlyFaultStorage struct {
	mem        *raft.MemoryStorage
	failAppend atomic.Bool
	appendOK   atomic.Int64
	appendFail atomic.Int64
}

func newAppendOnlyFaultStorage() *appendOnlyFaultStorage {
	return &appendOnlyFaultStorage{mem: raft.NewMemoryStorage()}
}

func (f *appendOnlyFaultStorage) InitialState() (pb.HardState, pb.ConfState, error) {
	return f.mem.InitialState()
}
func (f *appendOnlyFaultStorage) Entries(lo, hi, maxSize uint64) ([]pb.Entry, error) {
	return f.mem.Entries(lo, hi, maxSize)
}
func (f *appendOnlyFaultStorage) Term(i uint64) (uint64, error) { return f.mem.Term(i) }
func (f *appendOnlyFaultStorage) LastIndex() (uint64, error)    { return f.mem.LastIndex() }
func (f *appendOnlyFaultStorage) FirstIndex() (uint64, error)   { return f.mem.FirstIndex() }
func (f *appendOnlyFaultStorage) Snapshot() (pb.Snapshot, error) {
	return f.mem.Snapshot()
}
func (f *appendOnlyFaultStorage) Append(entries []pb.Entry) error {
	if f.failAppend.Load() {
		f.appendFail.Add(1)
		return errFaultInjected
	}
	f.appendOK.Add(1)
	return f.mem.Append(entries)
}
func (f *appendOnlyFaultStorage) SetHardState(st pb.HardState) error {
	return f.mem.SetHardState(st)
}

// TestAppendFailureDefersAdvanceAndCommitsAfterHeal pins the entries-durability half
// of the invariant in isolation: HardState persistence SUCCEEDS, but the log Append
// FAILS. A correct driver must still refuse to Advance — because the log entries are
// not on stable storage, advancing would let raft commit an entry whose payload was
// never durably appended.
//
// MUTATION GUARD: change processReadyLockedErr so the Append error path does not
// `continue` (i.e. it falls through to apply+Advance), or drop the Append error check
// entirely, and the doomed entry commits while Append is failing -> this test fails on
// the `during != base` assertion.
func TestAppendFailureDefersAdvanceAndCommitsAfterHeal(t *testing.T) {
	fs := newAppendOnlyFaultStorage()
	m := NewMultiRaftManagerWithStorage(NewInProcTransport(), func() shardPersister { return fs })

	const sid = ShardID("append-fault")
	if err := m.CreateShard(sid, []uint64{1}); err != nil {
		t.Fatalf("CreateShard: %v", err)
	}
	driveUntilLeaders(t, m, []ShardID{sid}, 200)

	if err := m.Propose(context.Background(), sid, []byte("ok-1")); err != nil {
		t.Fatalf("propose ok-1: %v", err)
	}
	settle(m, 30)
	base, err := m.CommittedCount(sid, 1)
	if err != nil {
		t.Fatalf("CommittedCount: %v", err)
	}
	if base != 1 {
		t.Fatalf("baseline committed=%d, want 1", base)
	}

	// Fail ONLY Append now. SetHardState still succeeds, so this proves the driver
	// gates Advance on the Append result independently, not just on HardState.
	fs.failAppend.Store(true)
	if err := m.Propose(context.Background(), sid, []byte("doomed-append")); err != nil {
		t.Fatalf("propose doomed-append: %v", err)
	}
	failBefore := fs.appendFail.Load()
	settle(m, 20)
	failAfter := fs.appendFail.Load()

	during, _ := m.CommittedCount(sid, 1)
	if during != base {
		t.Fatalf("entry committed despite Append failure: committed=%d want %d (Advance ran on un-persisted log entries)", during, base)
	}
	if failAfter <= failBefore {
		t.Fatalf("expected the parked Ready to be re-attempted (Append re-failed): before=%d after=%d", failBefore, failAfter)
	}

	// Heal: the SAME parked Ready must now persist and commit — exactly once.
	fs.failAppend.Store(false)
	settle(m, 40)
	after, _ := m.CommittedCount(sid, 1)
	if after != base+1 {
		t.Fatalf("after healing committed=%d, want %d (parked entry must commit exactly once)", after, base+1)
	}
	entries, _ := m.CommittedEntries(sid, 1)
	occurrences := 0
	for _, e := range entries {
		if bytes.Equal(e, []byte("doomed-append")) {
			occurrences++
		}
	}
	if occurrences != 1 {
		t.Fatalf("healed entry committed %d times, want exactly 1 (park/retry must not double-apply)", occurrences)
	}
}

// TestPersistFaultIsolatedToFaultingShard proves a persist fault in one shard does
// NOT defer or corrupt a healthy shard's commits. Each shard owns its own persister;
// a faulting shard parks its Ready while the healthy shard advances normally in the
// same Tick/Run passes. This is the persist-layer companion to the routing-isolation
// invariant: a durability fault is contained to its shard.
//
// MUTATION GUARD: if the driver surfaced the persist error in a way that aborted the
// whole driving pass (e.g. returned early from Tick/Run on first error) instead of
// continuing other shards, the healthy shard would stop committing -> fails here.
func TestPersistFaultIsolatedToFaultingShard(t *testing.T) {
	healthy := newFaultStorage()
	faulty := newFaultStorage()
	stores := map[ShardID]*faultStorage{"healthy": healthy, "faulty": faulty}
	var mkOrder []ShardID
	var mkMu sync.Mutex
	// newStorage is called once per peer at CreateShard time, in shard-creation order.
	// Both shards are single-peer, so we hand out the right store per creation.
	m := NewMultiRaftManagerWithStorage(NewInProcTransport(), func() shardPersister {
		mkMu.Lock()
		defer mkMu.Unlock()
		id := mkOrder[0]
		mkOrder = mkOrder[1:]
		return stores[id]
	})

	mkOrder = []ShardID{"healthy"}
	if err := m.CreateShard("healthy", []uint64{1}); err != nil {
		t.Fatalf("CreateShard healthy: %v", err)
	}
	mkOrder = []ShardID{"faulty"}
	if err := m.CreateShard("faulty", []uint64{1}); err != nil {
		t.Fatalf("CreateShard faulty: %v", err)
	}
	driveUntilLeaders(t, m, []ShardID{"healthy", "faulty"}, 200)

	// Baseline on both.
	if err := m.Propose(context.Background(), "healthy", []byte("h-0")); err != nil {
		t.Fatalf("propose h-0: %v", err)
	}
	if err := m.Propose(context.Background(), "faulty", []byte("f-0")); err != nil {
		t.Fatalf("propose f-0: %v", err)
	}
	settle(m, 40)
	hBase, _ := m.CommittedCount("healthy", 1)
	fBase, _ := m.CommittedCount("faulty", 1)
	if hBase != 1 || fBase != 1 {
		t.Fatalf("baseline commits healthy=%d faulty=%d, want 1/1", hBase, fBase)
	}

	// Break the faulty shard's persistence, then propose to BOTH shards repeatedly.
	faulty.fail.Store(true)
	for i := 0; i < 5; i++ {
		if err := m.Propose(context.Background(), "healthy", []byte(fmt.Sprintf("h-%d", i+1))); err != nil {
			t.Fatalf("propose healthy during fault: %v", err)
		}
		_ = m.Propose(context.Background(), "faulty", []byte(fmt.Sprintf("f-%d", i+1)))
		settle(m, 6)
	}

	// The healthy shard kept committing despite the faulty shard parking its Ready.
	hDuring, _ := m.CommittedCount("healthy", 1)
	if hDuring != hBase+5 {
		t.Fatalf("healthy shard committed %d during faulty shard's outage, want %d (fault leaked across shards)", hDuring, hBase+5)
	}
	// The faulty shard did NOT advance while persistence failed.
	fDuring, _ := m.CommittedCount("faulty", 1)
	if fDuring != fBase {
		t.Fatalf("faulty shard committed %d while persistence failed, want %d (advanced on un-persisted Ready)", fDuring, fBase)
	}
	// And only the faulty shard's persist-error record moved.
	if cnt, _, _ := m.LastPersistError("faulty"); cnt == 0 {
		t.Fatal("faulty shard recorded no persist error")
	}
	if cnt, _, _ := m.LastPersistError("healthy"); cnt != 0 {
		t.Fatalf("healthy shard recorded %d persist errors, want 0 (cross-shard leak)", cnt)
	}

	// Heal the faulty shard: its parked proposals now drain and commit, in order.
	faulty.fail.Store(false)
	settle(m, 60)
	fAfter, _ := m.CommittedCount("faulty", 1)
	if fAfter != fBase+5 {
		t.Fatalf("after heal faulty committed=%d, want %d", fAfter, fBase+5)
	}
	entries, _ := m.CommittedEntries("faulty", 1)
	// Verify order + exactly-once for the faulty shard's application payloads.
	want := []string{"f-0", "f-1", "f-2", "f-3", "f-4", "f-5"}
	if len(entries) != len(want) {
		t.Fatalf("faulty shard committed %d entries, want %d", len(entries), len(want))
	}
	for i, e := range entries {
		if string(e) != want[i] {
			t.Fatalf("faulty shard entry[%d]=%q, want %q (out-of-order or duplicate after park/retry)", i, e, want[i])
		}
	}
}

// TestConcurrentCommitExactlyOnceWithHealingFault is the concurrency centerpiece.
// A 3-peer shard takes proposals from many goroutines while a background ticker
// drives the clock. Partway through, persistence faults are injected on ALL peers
// then healed, forcing the park/retry path to interleave with concurrent Propose and
// async-style driving. Under -race the test asserts:
//   - no data race / panic, AND
//   - every distinct proposed payload commits EXACTLY ONCE on the leader (no dup from
//     the park/retry, no gap from a dropped Ready), AND
//   - the leader's committed application entries are unique (exactly-once apply).
//
// MUTATION GUARD: removing the Append/HardState error `continue` (advancing on an
// un-persisted Ready) corrupts the log under fault and either loses or duplicates a
// payload; removing sh.mu around the inbox Step races under -race. Either fails here.
func TestConcurrentCommitExactlyOnceWithHealingFault(t *testing.T) {
	// One fault-injecting persister per peer so we can fault the whole group at once.
	var faultStores []*faultStorage
	var mkMu sync.Mutex
	m := NewMultiRaftManagerWithStorage(NewInProcTransport(), func() shardPersister {
		mkMu.Lock()
		defer mkMu.Unlock()
		fsx := newFaultStorage()
		faultStores = append(faultStores, fsx)
		return fsx
	})

	const sid = ShardID("xact")
	if err := m.CreateShard(sid, threePeers()); err != nil {
		t.Fatalf("CreateShard: %v", err)
	}
	driveUntilLeaders(t, m, []ShardID{sid}, 300)

	const nProposals = 40
	setFault := func(v bool) {
		mkMu.Lock()
		defer mkMu.Unlock()
		for _, fsx := range faultStores {
			fsx.fail.Store(v)
		}
	}

	stop := make(chan struct{})
	var tickWG sync.WaitGroup
	tickWG.Add(1)
	go func() {
		defer tickWG.Done()
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

	// Flip persistence faults on then off in a separate goroutine to interleave the
	// park/retry path with concurrent proposing and driving.
	var faultWG sync.WaitGroup
	faultWG.Add(1)
	go func() {
		defer faultWG.Done()
		for i := 0; i < 6; i++ {
			setFault(true)
			for j := 0; j < 2000; j++ {
			}
			setFault(false)
			for j := 0; j < 2000; j++ {
			}
		}
		setFault(false)
	}()

	var pwg sync.WaitGroup
	errs := make(chan error, nProposals)
	for j := 0; j < nProposals; j++ {
		pwg.Add(1)
		go func(j int) {
			defer pwg.Done()
			payload := []byte(fmt.Sprintf("xact:%d", j))
			var lastErr error
			for attempt := 0; attempt < 500; attempt++ {
				if err := m.Propose(context.Background(), sid, payload); err != nil {
					lastErr = err
					continue
				}
				lastErr = nil
				break
			}
			if lastErr != nil {
				errs <- fmt.Errorf("propose %d: %w", j, lastErr)
			}
		}(j)
	}
	pwg.Wait()
	faultWG.Wait()
	close(stop)
	tickWG.Wait()

	// Ensure faults are cleared, then settle so every parked Ready drains and commits.
	setFault(false)
	settle(m, 200)

	select {
	case err := <-errs:
		t.Fatalf("a proposal never succeeded even after retries: %v", err)
	default:
	}

	leader := m.LeaderID(sid)
	if leader == 0 {
		t.Fatal("shard lost its leader after healing")
	}
	entries, err := m.CommittedEntries(sid, leader)
	if err != nil {
		t.Fatalf("CommittedEntries: %v", err)
	}

	// EXACTLY-ONCE: each payload appears exactly once; no foreign/duplicate entries.
	seen := make(map[string]int, len(entries))
	for _, e := range entries {
		seen[string(e)]++
	}
	for j := 0; j < nProposals; j++ {
		key := fmt.Sprintf("xact:%d", j)
		switch seen[key] {
		case 1:
			// good
		case 0:
			t.Fatalf("payload %q never committed (dropped Ready under fault)", key)
		default:
			t.Fatalf("payload %q committed %d times (double-apply via park/retry)", key, seen[key])
		}
	}
	for val, c := range seen {
		if c != 1 {
			t.Fatalf("committed log has a non-unique entry %q (count=%d): apply was not exactly-once", val, c)
		}
	}
}

// TestParkedNodeDoesNotStallRunOrSpin proves the deferral does not break liveness:
// while one (and only) node is parked on a persist fault, Run must TERMINATE (it must
// not spin forever treating the parked node as perpetual progress) and must not block.
// A faulting node is explicitly NOT counted as progress by processReadyLockedErr, so
// Run returns; after heal the parked Ready drains.
//
// MUTATION GUARD: if a parked node were counted as `progressed`, Run's outer loop
// would never observe "no progress" and would spin forever -> this test hangs/times
// out (caught by `go test` timeout).
func TestParkedNodeDoesNotStallRunOrSpin(t *testing.T) {
	fs := newFaultStorage()
	m := NewMultiRaftManagerWithStorage(NewInProcTransport(), func() shardPersister { return fs })
	const sid = ShardID("liveness")
	if err := m.CreateShard(sid, []uint64{1}); err != nil {
		t.Fatalf("CreateShard: %v", err)
	}
	driveUntilLeaders(t, m, []ShardID{sid}, 200)

	fs.fail.Store(true)
	if err := m.Propose(context.Background(), sid, []byte("parked")); err != nil {
		t.Fatalf("propose: %v", err)
	}

	// Each of these calls must return promptly even though the node is parked.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			m.Tick()
			m.Run()
		}
		close(done)
	}()
	// The whole package test timeout (go test default 10m, harness uses -count=1) is
	// the backstop; a spin would hang here. We also assert no commit happened.
	<-done

	c, _ := m.CommittedCount(sid, 1)
	if c != 0 {
		t.Fatalf("committed=%d while persistence failing, want 0", c)
	}
	if cnt, _, err := m.LastPersistError(sid); cnt == 0 || err == nil || !errors.Is(err, errFaultInjected) {
		t.Fatalf("expected surfaced persist error while parked: cnt=%d err=%v", cnt, err)
	}

	fs.fail.Store(false)
	settle(m, 40)
	c2, _ := m.CommittedCount(sid, 1)
	if c2 != 1 {
		t.Fatalf("after heal committed=%d, want 1 (parked Ready never drained)", c2)
	}
}
