package multiraft

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"go.etcd.io/raft/v3"
	pb "go.etcd.io/raft/v3/raftpb"
)

// faultStorage wraps a real raft.MemoryStorage and can be told to start failing
// SetHardState/Append on demand. When failing, it does NOT mutate the underlying
// real storage and returns a typed error — the exact behavior a real WAL/disk
// backend exhibits on an I/O error. It also COUNTS how many SetHardState/Append
// calls succeeded vs failed so a test can assert the driver reacted correctly.
//
// CLAUDE-2 note: this is a fault-injecting test double used ONLY to exercise the
// real durability-error path of the production driver. The production code path
// it drives (processReadyLocked persisting before Advance) is real on every OS;
// no OS-specific mechanism is stubbed here.
type faultStorage struct {
	mem *raft.MemoryStorage

	fail       atomic.Bool // when true, SetHardState/Append return errFaultInjected
	appendOK   atomic.Int64
	appendFail atomic.Int64
	hardOK     atomic.Int64
	hardFail   atomic.Int64
}

var errFaultInjected = errors.New("multiraft: injected persistence fault")

func newFaultStorage() *faultStorage { return &faultStorage{mem: raft.NewMemoryStorage()} }

// raft.Storage methods delegate to the real MemoryStorage.
func (f *faultStorage) InitialState() (pb.HardState, pb.ConfState, error) {
	return f.mem.InitialState()
}
func (f *faultStorage) Entries(lo, hi, maxSize uint64) ([]pb.Entry, error) {
	return f.mem.Entries(lo, hi, maxSize)
}
func (f *faultStorage) Term(i uint64) (uint64, error) { return f.mem.Term(i) }
func (f *faultStorage) LastIndex() (uint64, error)    { return f.mem.LastIndex() }
func (f *faultStorage) FirstIndex() (uint64, error)   { return f.mem.FirstIndex() }
func (f *faultStorage) Snapshot() (pb.Snapshot, error) {
	return f.mem.Snapshot()
}

func (f *faultStorage) Append(entries []pb.Entry) error {
	if f.fail.Load() {
		f.appendFail.Add(1)
		return errFaultInjected
	}
	f.appendOK.Add(1)
	return f.mem.Append(entries)
}

func (f *faultStorage) SetHardState(st pb.HardState) error {
	if f.fail.Load() {
		f.hardFail.Add(1)
		return errFaultInjected
	}
	f.hardOK.Add(1)
	return f.mem.SetHardState(st)
}

// TestProcessReadyDoesNotAdvanceOnPersistFailure is the core HXC-917 regression
// test. A single-node shard elects itself and commits a baseline entry through
// the REAL raft engine and the REAL MemoryStorage. We then flip the storage into
// fault mode and propose. Because persistence now fails, the driver MUST NOT call
// Advance for that Ready — raft therefore re-presents the SAME unhandled Ready on
// every subsequent cycle and the proposal NEVER becomes a committed entry. We
// prove this by observing that:
//
//  1. committed entries do NOT grow while persistence is failing, AND
//  2. the storage records repeated FAILED persistence attempts (the Ready is
//     re-presented), AND
//  3. after we clear the fault and drive again, the proposal finally commits.
//
// MUTATION GUARD: reverting the fix (ignoring the error and advancing anyway)
// makes raft consider the Ready durably handled, so the failing-phase append
// count would NOT keep climbing and — far worse — the entry would commit despite
// never being persisted. Either makes this test fail.
func TestProcessReadyDoesNotAdvanceOnPersistFailure(t *testing.T) {
	fs := newFaultStorage()
	m := NewMultiRaftManagerWithStorage(NewInProcTransport(), func() shardPersister { return fs })

	const sid = ShardID("durable")
	if err := m.CreateShard(sid, []uint64{1}); err != nil {
		t.Fatalf("CreateShard: %v", err)
	}
	driveUntilLeaders(t, m, []ShardID{sid}, 200)

	// Baseline commit on the healthy path.
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

	// Inject persistence faults, then propose. The proposal produces entries that
	// must be persisted before Advance — persistence now fails.
	fs.fail.Store(true)
	if err := m.Propose(context.Background(), sid, []byte("doomed")); err != nil {
		t.Fatalf("propose doomed: %v", err)
	}

	// Drive several cycles while persistence keeps failing.
	failBefore := fs.appendFail.Load()
	settle(m, 20)
	failAfter := fs.appendFail.Load()

	// The doomed entry must NOT have committed while persistence failed.
	during, _ := m.CommittedCount(sid, 1)
	if during != base {
		t.Fatalf("entry committed despite persistence failure: committed=%d, want %d (Advance was called on un-persisted Ready)", during, base)
	}
	// The Ready must have been re-presented: the driver kept RE-attempting (and
	// re-failing) the append rather than advancing past it once.
	if failAfter <= failBefore {
		t.Fatalf("expected repeated failed Append attempts (Ready re-presented), failBefore=%d failAfter=%d — driver advanced past the un-persisted Ready", failBefore, failAfter)
	}

	// Heal the storage and drive again: now the SAME Ready persists and commits.
	fs.fail.Store(false)
	settle(m, 40)
	after, _ := m.CommittedCount(sid, 1)
	if after != base+1 {
		t.Fatalf("after healing, committed=%d, want %d (the previously-doomed entry must now commit)", after, base+1)
	}
	entries, _ := m.CommittedEntries(sid, 1)
	foundDoomed := false
	for _, e := range entries {
		if bytes.Equal(e, []byte("doomed")) {
			foundDoomed = true
		}
	}
	if !foundDoomed {
		t.Fatalf("the healed entry %q never committed", "doomed")
	}
}

// TestProcessReadyHappyPathStillAdvances proves the fix does not regress the
// in-memory happy path: with a never-failing real storage, proposals commit
// exactly as before and the storage records only successful persistence.
func TestProcessReadyHappyPathStillAdvances(t *testing.T) {
	fs := newFaultStorage()
	m := NewMultiRaftManagerWithStorage(NewInProcTransport(), func() shardPersister { return fs })

	const sid = ShardID("happy")
	if err := m.CreateShard(sid, []uint64{1}); err != nil {
		t.Fatalf("CreateShard: %v", err)
	}
	driveUntilLeaders(t, m, []ShardID{sid}, 200)

	for i := 0; i < 5; i++ {
		if err := m.Propose(context.Background(), sid, []byte{byte('a' + i)}); err != nil {
			t.Fatalf("propose %d: %v", i, err)
		}
	}
	settle(m, 40)

	count, _ := m.CommittedCount(sid, 1)
	if count != 5 {
		t.Fatalf("happy path committed=%d, want 5", count)
	}
	if fs.appendFail.Load() != 0 || fs.hardFail.Load() != 0 {
		t.Fatalf("unexpected persistence failures on happy path: appendFail=%d hardFail=%d", fs.appendFail.Load(), fs.hardFail.Load())
	}
	if fs.appendOK.Load() == 0 {
		t.Fatalf("expected real Append calls on happy path, got 0 (storage not actually exercised)")
	}
}

// TestProcessReadyLockedReturnsPersistError is a focused unit test on the driver
// helper: it must SURFACE (not swallow) a persistence error and must NOT Advance
// the faulting node. We inject the fault BEFORE the very first Ready is produced
// (by campaigning) so the driver's first persist attempt fails. We then observe
// non-advance directly via the node's parked-Ready state: a correct driver parks
// the un-persisted Ready (hasPendingReady == true) instead of advancing past it.
//
// MUTATION GUARD: if the fix is reverted to swallow the error and Advance anyway,
// (a) the returned error is nil and (b) hasPendingReady is false — both fail here.
func TestProcessReadyLockedReturnsPersistError(t *testing.T) {
	fs := newFaultStorage()
	m := NewMultiRaftManagerWithStorage(NewInProcTransport(), func() shardPersister { return fs })
	const sid = ShardID("surface")
	if err := m.CreateShard(sid, []uint64{1}); err != nil {
		t.Fatalf("CreateShard: %v", err)
	}

	m.mu.RLock()
	sh := m.shards[sid]
	m.mu.RUnlock()

	// Fail persistence, THEN campaign: the candidate->leader transition produces a
	// Ready (HardState vote/term + a fresh entry) whose persistence will now fail.
	fs.fail.Store(true)
	sh.mu.Lock()
	if err := sh.nodes[1].raw.Campaign(); err != nil {
		sh.mu.Unlock()
		t.Fatalf("Campaign: %v", err)
	}
	_, err := sh.processReadyLockedErr()
	parked := sh.nodes[1].hasPendingReady
	sh.mu.Unlock()

	if err == nil {
		t.Fatal("processReadyLockedErr swallowed the persistence error (returned nil)")
	}
	if !errors.Is(err, errFaultInjected) {
		t.Fatalf("expected injected fault to be surfaced, got %v", err)
	}
	if !parked {
		t.Fatal("faulting node did not park its un-persisted Ready — driver advanced past it (durability violation)")
	}
	if got := fs.appendFail.Load() + fs.hardFail.Load(); got == 0 {
		t.Fatal("driver never attempted persistence")
	}
}
