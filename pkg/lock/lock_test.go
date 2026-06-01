package lock

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/testing/evidence"
)

func TestMemoryLocker(t *testing.T) {
	l := NewMemoryLocker()
	ctx := context.Background()

	unlock, err := l.Lock(ctx, "test-key")
	if err != nil {
		t.Fatalf("lock: %v", err)
	}

	// After unlock, should succeed
	if err := unlock(); err != nil {
		t.Fatalf("unlock: %v", err)
	}

	unlock2, err := l.Lock(ctx, "test-key")
	if err != nil {
		t.Fatalf("re-lock: %v", err)
	}
	unlock2()
}

func TestMemoryLockerDifferentKeys(t *testing.T) {
	l := NewMemoryLocker()
	ctx := context.Background()

	unlock1, err := l.Lock(ctx, "key-a")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock1()

	// key-b should not be blocked
	unlock2, err := l.Lock(ctx, "key-b")
	if err != nil {
		t.Fatal(err)
	}
	unlock2()
}

// TestMemoryLockerBlocksSecondHolder proves the core mutual-exclusion contract
// directly, without relying on -race: while holder A holds the lock, holder B's
// Lock call MUST block, and B may only proceed once A releases. A no-op lock
// (the mutation this test is designed to kill) would let B acquire immediately,
// failing the "still blocked while A holds" assertion below.
//
// It also captures CLAUDE-1 §7.1 evidence: a unique token is written into a
// shared slice ONLY from inside the critical section, in lock-ordered fashion;
// the resulting ordered transcript is positive, sink-side proof that the two
// holders were serialized rather than interleaved.
func TestMemoryLockerBlocksSecondHolder(t *testing.T) {
	e := evidence.NewT(t, "memory-locker-mutual-exclusion", t.TempDir())

	l := NewMemoryLocker()
	const key = "blocking-key"

	unlockA, err := l.Lock(context.Background(), key)
	if err != nil {
		t.Fatalf("A Lock: %v", err)
	}

	var transcript []string // mutated ONLY under the held lock
	transcript = append(transcript, "A-enter")

	bAcquired := make(chan time.Time, 1)
	go func() {
		unlockB, err := l.Lock(context.Background(), key)
		if err != nil {
			t.Errorf("B Lock: %v", err)
			close(bAcquired)
			return
		}
		transcript = append(transcript, "B-enter")
		bAcquired <- time.Now()
		_ = unlockB()
	}()

	// Give B time to start blocking, then confirm it has NOT acquired: a working
	// lock keeps B parked here; a broken (no-op) lock would have let B through.
	time.Sleep(200 * time.Millisecond)
	select {
	case <-bAcquired:
		t.Fatal("B acquired the lock while A still held it (mutual exclusion broken)")
	default:
		// expected: B is still blocked
	}

	releaseTime := time.Now()
	transcript = append(transcript, "A-exit")
	if err := unlockA(); err != nil {
		t.Fatalf("A Unlock: %v", err)
	}

	select {
	case acq, ok := <-bAcquired:
		if !ok {
			t.Fatal("B failed to acquire after A released")
		}
		if acq.Before(releaseTime) {
			t.Fatal("B acquired before A released (mutex violation)")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("B never acquired the lock after A released")
	}

	// Sink-side evidence: the transcript MUST be exactly the serialized order.
	// An interleaving lock would scramble or race this slice.
	want := []string{"A-enter", "A-exit", "B-enter"}
	if len(transcript) != len(want) {
		t.Fatalf("transcript = %v, want %v", transcript, want)
	}
	for i := range want {
		if transcript[i] != want[i] {
			t.Fatalf("transcript[%d] = %q, want %q (full: %v)", i, transcript[i], want[i], transcript)
		}
	}

	got := transcript[0] + "," + transcript[1] + "," + transcript[2]
	e.MustPositive(t, "serialized-transcript", got, "A-enter,A-exit,B-enter")
}

// TestMemoryLockerConcurrent proves the lock SERIALIZES concurrent holders even
// WITHOUT -race. It is deliberately constructed so that a no-op / broken lock
// FAILS deterministically under the default `go test` command:
//
//   - `inCritical` is a non-atomic int flag set on entry and cleared on exit of
//     the critical section. If two goroutines were ever inside simultaneously
//     (i.e. the lock did not exclude), the second would observe inCritical==1
//     on entry and bump `violations`.
//   - the critical section also does a non-atomic read-modify-write of `counter`
//     with a sleep in the middle; lost updates (counter != N) are a second,
//     independent failure signal of a non-exclusive lock.
//
// Under a correct lock: violations==0 and counter==N.
// Under a no-op lock: violations>0 (overlap detected) AND/OR counter<N.
func TestMemoryLockerConcurrent(t *testing.T) {
	l := NewMemoryLocker()
	ctx := context.Background()

	const n = 16
	var counter int        // non-atomic; only safe under real mutual exclusion
	var inCritical int      // non-atomic occupancy flag
	var violations int64    // atomic so the failure signal itself is race-free
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock, err := l.Lock(ctx, "counter")
			if err != nil {
				t.Errorf("lock: %v", err)
				return
			}
			// --- critical section ---
			if inCritical != 0 {
				atomic.AddInt64(&violations, 1)
			}
			inCritical = 1
			v := counter
			time.Sleep(time.Millisecond) // widen the overlap window for broken locks
			counter = v + 1
			inCritical = 0
			// --- end critical section ---
			unlock()
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt64(&violations); got != 0 {
		t.Fatalf("detected %d concurrent entries into the critical section (lock not mutually exclusive)", got)
	}
	if counter != n {
		t.Fatalf("counter = %d, want %d (lost updates => lock not mutually exclusive)", counter, n)
	}
}

func TestMemoryLockerContextCancellation(t *testing.T) {
	l := NewMemoryLocker()
	ctx := context.Background()

	unlock, err := l.Lock(ctx, "cancel-key")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	// Try to lock with cancelled context
	ctx2, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = l.Lock(ctx2, "cancel-key")
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
