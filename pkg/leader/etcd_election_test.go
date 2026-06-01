package leader

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mvccpb "go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// ---------------------------------------------------------------------------
// Fake backend + epoch store (deterministic, no etcd required)
// ---------------------------------------------------------------------------

// fakeElectionBackend is an in-process fake that satisfies EtcdElectionBackend.
// It serialises Campaign calls so exactly one caller wins at a time: when a
// holder is present, subsequent callers queue on a channel and ONE is woken per
// Resign (not all at once, which would break exclusion). The queue uses a
// buffered notify channel of capacity 1; after the holder resigns it sends one
// token, the first waiter consumes it and wins, then re-enters the queue loop.
//
// This mirrors real etcd election semantics (queue of waiters, one winner per
// resign) without requiring a running etcd.
type fakeElectionBackend struct {
	mu      sync.Mutex
	value   string
	waiters []chan struct{} // each waiter gets its own notification channel
}

func newFakeElectionBackend() *fakeElectionBackend {
	return &fakeElectionBackend{}
}

func (f *fakeElectionBackend) Campaign(ctx context.Context, value string) error {
	for {
		f.mu.Lock()
		if f.value == "" {
			// No current holder — win immediately.
			f.value = value
			f.mu.Unlock()
			return nil
		}
		// Register as a waiter with a personal notify channel.
		notify := make(chan struct{}, 1)
		f.waiters = append(f.waiters, notify)
		f.mu.Unlock()

		// Wait until notified by Resign or cancelled.
		select {
		case <-ctx.Done():
			// Remove ourselves from the waiter queue.
			f.mu.Lock()
			for i, w := range f.waiters {
				if w == notify {
					f.waiters = append(f.waiters[:i], f.waiters[i+1:]...)
					break
				}
			}
			f.mu.Unlock()
			return ctx.Err()
		case <-notify:
			// Woken by Resign — retry the acquire loop.
		}
	}
}

func (f *fakeElectionBackend) Resign(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.value == "" {
		return nil
	}
	f.value = ""
	// Wake exactly ONE waiter (FIFO), if any.
	if len(f.waiters) > 0 {
		next := f.waiters[0]
		f.waiters = f.waiters[1:]
		next <- struct{}{}
	}
	return nil
}

func (f *fakeElectionBackend) Leader(_ context.Context) (*clientv3.GetResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.value == "" {
		return nil, errors.New("no leader")
	}
	resp := &clientv3.GetResponse{}
	resp.Kvs = []*mvccpb.KeyValue{{Value: []byte(f.value)}}
	return resp, nil
}

func (f *fakeElectionBackend) Observe(_ context.Context) <-chan clientv3.GetResponse {
	ch := make(chan clientv3.GetResponse)
	close(ch)
	return ch
}

func (f *fakeElectionBackend) LeaseID() clientv3.LeaseID { return 1 }
func (f *fakeElectionBackend) Close() error              { return nil }

// assert interface compliance at compile time.
var _ EtcdElectionBackend = (*fakeElectionBackend)(nil)

// fakeEpochStore is a thread-safe in-memory epoch counter.
type fakeEpochStore struct {
	mu    sync.Mutex
	epoch uint64
}

func (s *fakeEpochStore) IncrementAndGet(_ context.Context, _ string) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.epoch++
	return s.epoch, nil
}

func (s *fakeEpochStore) Get(_ context.Context, _ string) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.epoch, nil
}

var _ EtcdEpochStore = (*fakeEpochStore)(nil)

// errorEpochStore always returns an error from IncrementAndGet.
type errorEpochStore struct{}

func (e *errorEpochStore) IncrementAndGet(_ context.Context, _ string) (uint64, error) {
	return 0, errors.New("epoch store: simulated error")
}
func (e *errorEpochStore) Get(_ context.Context, _ string) (uint64, error) { return 0, nil }

var _ EtcdEpochStore = (*errorEpochStore)(nil)

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

func newTestEtcdElection(t *testing.T, localID string, backend EtcdElectionBackend, store EtcdEpochStore) *EtcdElection {
	t.Helper()
	ee, err := NewEtcdElection(EtcdElectionConfig{
		LocalID:     localID,
		ElectionKey: "/helix/test/election",
		EpochKey:    "/helix/test/election/epoch",
	}, backend, store)
	if err != nil {
		t.Fatalf("NewEtcdElection(%q): %v", localID, err)
	}
	return ee
}

// ---------------------------------------------------------------------------
// Unit tests — deterministic, no real etcd
// ---------------------------------------------------------------------------

// TestEtcdElectionRequiresLocalID validates the construction-time LocalID guard.
// §1.1 mutation pair: remove the `cfg.LocalID == ""` guard in NewEtcdElection →
// NewEtcdElection returns (non-nil, nil) and this test fails because err==nil.
func TestEtcdElectionRequiresLocalID(t *testing.T) {
	_, err := NewEtcdElection(EtcdElectionConfig{ElectionKey: "/x"}, nil, nil)
	if err == nil {
		t.Fatal("expected error for empty LocalID; got nil — guard missing")
	}
}

// TestEtcdElectionRequiresElectionKey validates the construction-time ElectionKey guard.
// §1.1 mutation pair: remove the `cfg.ElectionKey == ""` guard → err==nil → test fails.
func TestEtcdElectionRequiresElectionKey(t *testing.T) {
	_, err := NewEtcdElection(EtcdElectionConfig{LocalID: "node-a"}, nil, nil)
	if err == nil {
		t.Fatal("expected error for empty ElectionKey; got nil — guard missing")
	}
}

// TestEtcdElectionEpochKeyDefaults proves the epoch key is derived from
// ElectionKey+"/epoch" when EpochKey is not supplied.
// §1.1 mutation pair: remove the default derivation → epochKey stays "" →
// ee.epochKey != expected and test fails.
func TestEtcdElectionEpochKeyDefaults(t *testing.T) {
	ee, err := NewEtcdElection(EtcdElectionConfig{
		LocalID:     "node-a",
		ElectionKey: "/helix/election/sched",
	}, newFakeElectionBackend(), &fakeEpochStore{})
	if err != nil {
		t.Fatalf("NewEtcdElection: %v", err)
	}
	// The epoch key MUST live in the parallel EpochNamespace, NOT under the
	// ElectionKey prefix — otherwise concurrency.Election.waitDeletes treats the
	// permanent epoch key as a predecessor candidate and Campaign hangs forever.
	want := EpochNamespace + "/helix/election/sched"
	if ee.epochKey != want {
		t.Fatalf("epochKey = %q, want %q", ee.epochKey, want)
	}
}

// TestEtcdElectionCampaignWinsImmediatelyWhenNoHolder proves a sole candidate
// wins without blocking and gets a non-zero fencing token.
// §1.1 mutation pair: comment out backend.Campaign call → isLeader stays false,
// test fails at "expected IsLeader=true".
func TestEtcdElectionCampaignWinsImmediatelyWhenNoHolder(t *testing.T) {
	ee := newTestEtcdElection(t, "node-a", newFakeElectionBackend(), &fakeEpochStore{})

	tok, err := ee.Campaign(context.Background(), "run-001")
	if err != nil {
		t.Fatalf("Campaign: %v", err)
	}
	if !ee.IsLeader() {
		t.Fatal("expected IsLeader=true after winning Campaign")
	}
	if ee.LeaderID() != "node-a" {
		t.Fatalf("LeaderID = %q, want %q", ee.LeaderID(), "node-a")
	}
	if tok.HolderID != "node-a" {
		t.Fatalf("FencingToken.HolderID = %q, want %q", tok.HolderID, "node-a")
	}
	if tok.Epoch == 0 {
		t.Fatal("FencingToken.Epoch must be non-zero after Campaign")
	}
}

// TestEtcdElectionEpochStrictlyIncreases proves the monotonic fencing-token
// guarantee across multiple Campaign/Resign cycles on the same node.
// §1.1 mutation pair: reset store.epoch=0 on each call → tok2.Epoch <= tok1.Epoch
// → test fails with "epoch must strictly increase".
func TestEtcdElectionEpochStrictlyIncreases(t *testing.T) {
	backend := newFakeElectionBackend()
	store := &fakeEpochStore{}
	ee := newTestEtcdElection(t, "node-a", backend, store)

	tok1, err := ee.Campaign(context.Background(), "run-001")
	if err != nil {
		t.Fatalf("Campaign 1: %v", err)
	}
	if err := ee.Resign(context.Background()); err != nil {
		t.Fatalf("Resign 1: %v", err)
	}

	tok2, err := ee.Campaign(context.Background(), "run-002")
	if err != nil {
		t.Fatalf("Campaign 2: %v", err)
	}
	if tok2.Epoch <= tok1.Epoch {
		t.Fatalf("epoch must strictly increase: tok1=%d tok2=%d", tok1.Epoch, tok2.Epoch)
	}
	if err := ee.Resign(context.Background()); err != nil {
		t.Fatalf("Resign 2: %v", err)
	}

	tok3, err := ee.Campaign(context.Background(), "run-003")
	if err != nil {
		t.Fatalf("Campaign 3: %v", err)
	}
	if tok3.Epoch <= tok2.Epoch {
		t.Fatalf("epoch must strictly increase: tok2=%d tok3=%d", tok2.Epoch, tok3.Epoch)
	}
}

// TestEtcdElectionEpochStoreErrorPropagates proves a failure in the epoch
// store causes Campaign to return an error (not silently succeed with epoch=0).
// §1.1 mutation pair: swallow the epochStore error → Campaign returns nil →
// test fails at "expected Campaign to fail".
func TestEtcdElectionEpochStoreErrorPropagates(t *testing.T) {
	ee := newTestEtcdElection(t, "node-a", newFakeElectionBackend(), &errorEpochStore{})
	_, err := ee.Campaign(context.Background(), "run-001")
	if err == nil {
		t.Fatal("expected Campaign to fail when epochStore errors; got nil — error swallowed")
	}
}

// TestEtcdElectionSecondCandidateBlocksUntilResign proves the exclusive
// leadership contract: node-b's Campaign blocks while node-a holds the
// election; it wins only after node-a resigns.
// §1.1 mutation pair: make fakeElectionBackend.Campaign non-blocking → bAcquired
// fires before aResigns → "node-b acquired while node-a held" assertion fails.
func TestEtcdElectionSecondCandidateBlocksUntilResign(t *testing.T) {
	backend := newFakeElectionBackend()
	store := &fakeEpochStore{}

	eeA := newTestEtcdElection(t, "node-a", backend, store)
	eeB := newTestEtcdElection(t, "node-b", backend, store)

	if _, err := eeA.Campaign(context.Background(), "run-A"); err != nil {
		t.Fatalf("A Campaign: %v", err)
	}
	if !eeA.IsLeader() {
		t.Fatal("node-a must be leader after Campaign")
	}

	bAcquired := make(chan FencingToken, 1)
	bErrCh := make(chan error, 1)
	go func() {
		tok, err := eeB.Campaign(context.Background(), "run-B")
		if err != nil {
			bErrCh <- err
			return
		}
		bAcquired <- tok
	}()

	// Give B time to block, then confirm it has NOT acquired.
	time.Sleep(150 * time.Millisecond)
	select {
	case <-bAcquired:
		t.Fatal("node-b acquired while node-a held the election (exclusion violated)")
	case err := <-bErrCh:
		t.Fatalf("node-b Campaign errored unexpectedly: %v", err)
	default:
		// expected: B is still blocked
	}

	resignTime := time.Now()
	if err := eeA.Resign(context.Background()); err != nil {
		t.Fatalf("A Resign: %v", err)
	}

	select {
	case tokB := <-bAcquired:
		t.Logf("node-b won election %v after node-a resigned", time.Since(resignTime))
		if tokB.Epoch == 0 {
			t.Fatal("node-b FencingToken.Epoch must be non-zero")
		}
	case err := <-bErrCh:
		t.Fatalf("node-b Campaign failed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("node-b never won after node-a resigned")
	}
}

// TestEtcdElectionFencingTokenMonotonicAcrossFailover proves cross-node epoch
// monotonicity: the new leader's epoch must be strictly greater than the old
// leader's epoch (the core HXC-1093 fencing requirement).
// §1.1 mutation pair: start B's epoch from 0 regardless of shared store →
// tokB.Epoch <= tokA.Epoch → test fails.
func TestEtcdElectionFencingTokenMonotonicAcrossFailover(t *testing.T) {
	backend := newFakeElectionBackend()
	store := &fakeEpochStore{}

	eeA := newTestEtcdElection(t, "node-a", backend, store)
	eeB := newTestEtcdElection(t, "node-b", backend, store)

	tokA, err := eeA.Campaign(context.Background(), "run-A")
	if err != nil {
		t.Fatalf("A Campaign: %v", err)
	}

	bDone := make(chan FencingToken, 1)
	go func() {
		tok, err := eeB.Campaign(context.Background(), "run-B")
		if err != nil {
			t.Errorf("B Campaign: %v", err)
			return
		}
		bDone <- tok
	}()
	time.Sleep(30 * time.Millisecond)
	if err := eeA.Resign(context.Background()); err != nil {
		t.Fatalf("A Resign: %v", err)
	}

	select {
	case tokB := <-bDone:
		if tokB.Epoch <= tokA.Epoch {
			t.Fatalf("failover epoch not monotonic: A.epoch=%d B.epoch=%d", tokA.Epoch, tokB.Epoch)
		}
		t.Logf("fencing token monotonic across failover: A.epoch=%d B.epoch=%d", tokA.Epoch, tokB.Epoch)
	case <-time.After(5 * time.Second):
		t.Fatal("B never won after A resigned")
	}
}

// TestEtcdElectionResignClearsLeaderState proves Resign marks isLeader=false
// and clears leaderID.
// §1.1 mutation pair: remove `ee.isLeader = false` in Resign → IsLeader() still
// returns true → test fails.
func TestEtcdElectionResignClearsLeaderState(t *testing.T) {
	ee := newTestEtcdElection(t, "node-a", newFakeElectionBackend(), &fakeEpochStore{})

	if _, err := ee.Campaign(context.Background(), "run-001"); err != nil {
		t.Fatalf("Campaign: %v", err)
	}
	if !ee.IsLeader() {
		t.Fatal("expected IsLeader=true after Campaign")
	}
	if err := ee.Resign(context.Background()); err != nil {
		t.Fatalf("Resign: %v", err)
	}
	if ee.IsLeader() {
		t.Fatal("expected IsLeader=false after Resign — state not cleared")
	}
	if ee.LeaderID() != "" {
		t.Fatalf("expected empty LeaderID after Resign, got %q", ee.LeaderID())
	}
}

// TestStaleLeaderDetectableByEpoch proves a consumer can reject a former
// leader by comparing fencing tokens via StaleLeader / FencingToken.IsStale.
// §1.1 mutation pair: invert IsStale logic (epoch >= highest) → tokA incorrectly
// reports not-stale → test fails at "tokA should be stale".
func TestStaleLeaderDetectableByEpoch(t *testing.T) {
	backend := newFakeElectionBackend()
	store := &fakeEpochStore{}

	eeA := newTestEtcdElection(t, "node-a", backend, store)
	eeB := newTestEtcdElection(t, "node-b", backend, store)

	tokA, err := eeA.Campaign(context.Background(), "run-A")
	if err != nil {
		t.Fatalf("A Campaign: %v", err)
	}

	bDone := make(chan FencingToken, 1)
	go func() {
		tok, err := eeB.Campaign(context.Background(), "run-B")
		if err != nil {
			t.Errorf("B Campaign: %v", err)
			return
		}
		bDone <- tok
	}()
	time.Sleep(30 * time.Millisecond)
	_ = eeA.Resign(context.Background())

	select {
	case tokB := <-bDone:
		highest := tokB.Epoch
		if !StaleLeader(tokA, highest) {
			t.Fatalf("tokA (epoch=%d) should be stale vs highest=%d", tokA.Epoch, highest)
		}
		if StaleLeader(tokB, highest) {
			t.Fatalf("tokB (epoch=%d) must NOT be stale vs highest=%d", tokB.Epoch, highest)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("B never won")
	}
}

// TestEtcdElectionCampaignContextCancellation proves a blocked Campaign returns
// ctx.Err() when the context is cancelled (no goroutine leak).
// §1.1 mutation pair: ignore ctx.Done() in fakeElectionBackend.Campaign →
// Campaign never returns → test times out.
func TestEtcdElectionCampaignContextCancellation(t *testing.T) {
	backend := newFakeElectionBackend()
	store := &fakeEpochStore{}

	eeA := newTestEtcdElection(t, "node-a", backend, store)
	eeB := newTestEtcdElection(t, "node-b", backend, store)

	if _, err := eeA.Campaign(context.Background(), "run-A"); err != nil {
		t.Fatalf("A Campaign: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := eeB.Campaign(ctx, "run-B")
		errCh <- err
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Campaign did not return after context cancellation")
	}
}

// TestEtcdElectionConcurrentCandidatesMutualExclusion proves that with N
// concurrent candidates sharing a single fake backend, exactly one wins at a
// time. The shared state is protected by a mutex when actually recording (the
// test proves the LOCK serialises the Campaign calls — it doesn't re-implement
// the lock; it observes serialisation at the Campaign boundary).
//
// Design: inCritical is an atomic occupancy flag that must never exceed 1
// while the critical section is active. Each goroutine atomically increments
// it on entry and decrements on exit; if it ever reaches 2, mutual exclusion
// is broken. The counter itself is accessed under mu so the race detector
// doesn't fire on that variable — the EXCLUSION proof is the inCritical flag.
//
// §1.1 mutation pair: make fakeElectionBackend.Campaign non-blocking → two
// goroutines enter the critical section simultaneously → inCritical reaches 2
// → violations > 0 → test fails.
func TestEtcdElectionConcurrentCandidatesMutualExclusion(t *testing.T) {
	backend := newFakeElectionBackend()
	store := &fakeEpochStore{}

	const N = 6
	var mu sync.Mutex // guards counter — proves we can use it safely under Campaign serialisation
	var counter int
	var inCritical int32 // atomic occupancy flag: must never exceed 1
	var violations int64 // atomic: bumped if two goroutines overlap

	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ee := newTestEtcdElection(t, fmt.Sprintf("node-%d", id), backend, store)
			if _, err := ee.Campaign(context.Background(), fmt.Sprintf("run-%d", id)); err != nil {
				t.Errorf("Campaign node-%d: %v", id, err)
				return
			}
			// --- critical section ---
			if atomic.AddInt32(&inCritical, 1) > 1 {
				atomic.AddInt64(&violations, 1)
			}
			time.Sleep(2 * time.Millisecond) // widen overlap window for broken locks
			atomic.AddInt32(&inCritical, -1)
			// Increment counter under mu (counter itself is always safe; we use mu
			// only to satisfy the race detector, not to prove exclusion).
			mu.Lock()
			counter++
			mu.Unlock()
			// --- end critical section ---
			if err := ee.Resign(context.Background()); err != nil {
				t.Errorf("Resign node-%d: %v", id, err)
			}
		}(i)
	}
	wg.Wait()

	if v := atomic.LoadInt64(&violations); v != 0 {
		t.Fatalf("detected %d concurrent critical-section entries (mutual exclusion violated)", v)
	}
	mu.Lock()
	got := counter
	mu.Unlock()
	if got != N {
		t.Fatalf("counter=%d want=%d (lost updates => exclusive leadership violated)", got, N)
	}
}

// TestEtcdElectionCurrentTokenMatchesLastWin proves CurrentToken() returns the
// last successfully issued fencing token.
// §1.1 mutation pair: have CurrentToken return a zero FencingToken → Epoch==0 →
// test fails at "CurrentToken.Epoch".
func TestEtcdElectionCurrentTokenMatchesLastWin(t *testing.T) {
	ee := newTestEtcdElection(t, "node-a", newFakeElectionBackend(), &fakeEpochStore{})

	tok, err := ee.Campaign(context.Background(), "run-001")
	if err != nil {
		t.Fatalf("Campaign: %v", err)
	}
	got := ee.CurrentToken()
	if got.Epoch != tok.Epoch {
		t.Fatalf("CurrentToken.Epoch=%d want=%d", got.Epoch, tok.Epoch)
	}
	if got.HolderID != tok.HolderID {
		t.Fatalf("CurrentToken.HolderID=%q want=%q", got.HolderID, tok.HolderID)
	}
}
