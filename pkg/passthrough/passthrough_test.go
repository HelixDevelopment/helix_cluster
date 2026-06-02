package passthrough

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func runUUID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b[:])
}

// waitGoroutines polls until the live goroutine count drops to <= baseline+slack
// or the deadline passes. It returns the final observed count. This is a
// deterministic convergence check (it depends only on goroutine lifecycle, not
// on wall-clock timing of logic under test).
func waitGoroutines(t *testing.T, baseline, slack int) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		runtime.GC()
		n := runtime.NumGoroutine()
		if n <= baseline+slack {
			return n
		}
		if time.Now().After(deadline) {
			return n
		}
		time.Sleep(time.Millisecond)
	}
}

// Clause 1: client disconnect mid-stream must propagate an abort to the upstream
// producer, the producer must stop producing, Run must return a cancellation
// error, and no goroutine may leak.
//
// Mutation: if Run did NOT propagate the client cancel to the upstream (never
// firing abort), the producer's select on abort.Done() would never unblock and
// it would keep producing past disconnectAt; producedAfterAbort would be > 0
// and observedAbort would stay false — failing the asserts below.
func TestRun_ClientDisconnectAbortsUpstream(t *testing.T) {
	id := runUUID(t)
	baseline := runtime.NumGoroutine()
	t.Logf("run=%s clause=1 phase=before baseline_goroutines=%d", id, baseline)

	ctx, cancel := context.WithCancel(context.Background())

	const disconnectAt = 5 // cancel after this many chunks reach the client

	var observedAbort atomic.Bool
	var producedAfterAbort atomic.Int64
	var totalProduced atomic.Int64

	// Producer yields an effectively unbounded stream until it observes abort.
	producer := func(abort *AbortSignal, emit Emit) error {
		seq := 0
		for {
			select {
			case <-abort.Done():
				observedAbort.Store(true)
				return ErrClientDisconnected
			default:
			}
			c := Chunk{Seq: seq, Data: []byte{byte(seq)}}
			if err := emit(c); err != nil {
				// Emit unblocked us with the disconnect error.
				observedAbort.Store(true)
				return err
			}
			seq++
			totalProduced.Add(1)
			if observedAbort.Load() {
				producedAfterAbort.Add(1)
			}
		}
	}

	var delivered int
	sink := SinkFunc(func(c Chunk) error {
		delivered++
		// Derive the expected sequence from delivery order: the Nth chunk
		// (0-based) must carry Seq == N. This asserts ordered passthrough.
		if c.Seq != delivered-1 {
			t.Errorf("run=%s out-of-order chunk: got Seq=%d want=%d", id, c.Seq, delivered-1)
		}
		if delivered == disconnectAt {
			cancel() // client disconnects mid-stream
		}
		return nil
	})

	res, err := Run(ctx, producer, sink)

	t.Logf("run=%s clause=1 phase=after delivered=%d total_produced=%d produced_after_abort=%d upstream_aborted=%v err=%v",
		id, res.ChunksDelivered, totalProduced.Load(), producedAfterAbort.Load(), res.UpstreamAborted, err)

	if !errors.Is(err, ErrClientDisconnected) {
		t.Fatalf("run=%s expected ErrClientDisconnected, got %v", id, err)
	}
	if !res.UpstreamAborted {
		t.Fatalf("run=%s upstream did NOT observe abort (cancel not propagated)", id)
	}
	if !observedAbort.Load() {
		t.Fatalf("run=%s producer never observed abort signal", id)
	}
	// The producer must have stopped: it does not keep producing chunks after
	// it observed the abort. We allow at most one in-flight chunk that was
	// already mid-emit when abort fired.
	if pa := producedAfterAbort.Load(); pa > 1 {
		t.Fatalf("run=%s producer kept producing after abort: %d extra chunks", id, pa)
	}
	if res.ChunksDelivered < disconnectAt {
		t.Fatalf("run=%s expected at least %d delivered before disconnect, got %d",
			id, disconnectAt, res.ChunksDelivered)
	}

	// No goroutine leak: Run must have joined the producer goroutine.
	got := waitGoroutines(t, baseline, 1)
	t.Logf("run=%s clause=1 goroutines_after=%d baseline=%d", id, got, baseline)
	if got > baseline+1 {
		t.Fatalf("run=%s goroutine leak: baseline=%d after=%d", id, baseline, got)
	}
}

// Clause 2: a short stream runs to completion; the upstream finishes WITHOUT an
// abort signal and ALL chunks reach the client in order.
//
// Mutation: if Run spuriously fired the abort on normal completion, the
// producer's emit calls would fail early, total delivered would be < n, and
// UpstreamAborted would be true — failing the asserts below.
func TestRun_NormalCompletionNoAbort(t *testing.T) {
	id := runUUID(t)
	baseline := runtime.NumGoroutine()
	const n = 8
	t.Logf("run=%s clause=2 phase=before baseline_goroutines=%d want_chunks=%d", id, baseline, n)

	var sawAbort atomic.Bool

	producer := func(abort *AbortSignal, emit Emit) error {
		for i := 0; i < n; i++ {
			select {
			case <-abort.Done():
				sawAbort.Store(true)
				return ErrClientDisconnected
			default:
			}
			// Each chunk's Data is derived from its sequence so the sink can
			// independently verify content integrity, not just count.
			if err := emit(Chunk{Seq: i, Data: []byte{byte(i * 3)}}); err != nil {
				return err
			}
		}
		return nil
	}

	var mu sync.Mutex
	var got []Chunk
	sink := SinkFunc(func(c Chunk) error {
		mu.Lock()
		got = append(got, c)
		mu.Unlock()
		return nil
	})

	res, err := Run(context.Background(), producer, sink)

	t.Logf("run=%s clause=2 phase=after delivered=%d upstream_aborted=%v saw_abort=%v err=%v",
		id, res.ChunksDelivered, res.UpstreamAborted, sawAbort.Load(), err)

	if err != nil {
		t.Fatalf("run=%s expected nil error on clean completion, got %v", id, err)
	}
	if res.UpstreamAborted {
		t.Fatalf("run=%s upstream incorrectly observed an abort on clean completion", id)
	}
	if sawAbort.Load() {
		t.Fatalf("run=%s producer saw an abort signal it should never have seen", id)
	}
	if res.ChunksDelivered != n {
		t.Fatalf("run=%s delivered=%d want=%d", id, res.ChunksDelivered, n)
	}
	if len(got) != n {
		t.Fatalf("run=%s sink received %d chunks, want %d", id, len(got), n)
	}
	for i, c := range got {
		if c.Seq != i {
			t.Fatalf("run=%s chunk %d has Seq=%d, want %d", id, i, c.Seq, i)
		}
		if len(c.Data) != 1 || c.Data[0] != byte(i*3) {
			t.Fatalf("run=%s chunk %d Data=%v, want [%d]", id, i, c.Data, byte(i*3))
		}
	}

	got2 := waitGoroutines(t, baseline, 1)
	t.Logf("run=%s clause=2 goroutines_after=%d baseline=%d", id, got2, baseline)
	if got2 > baseline+1 {
		t.Fatalf("run=%s goroutine leak: baseline=%d after=%d", id, baseline, got2)
	}
}

// Extra: a client-side Sink.Write error tears down the upstream too (abort
// fired) and is surfaced as the Run error. Guards the Write-error branch.
//
// Mutation: if the Write-error branch did not fire the abort, the producer
// would keep producing and UpstreamAborted would be false.
func TestRun_SinkErrorAbortsUpstream(t *testing.T) {
	id := runUUID(t)
	baseline := runtime.NumGoroutine()
	t.Logf("run=%s clause=extra phase=before baseline=%d", id, baseline)

	wantErr := errors.New("sink boom")
	var observed atomic.Bool

	producer := func(abort *AbortSignal, emit Emit) error {
		for i := 0; ; i++ {
			select {
			case <-abort.Done():
				observed.Store(true)
				return ErrClientDisconnected
			default:
			}
			if err := emit(Chunk{Seq: i}); err != nil {
				observed.Store(true)
				return err
			}
		}
	}

	failAt := 3
	calls := 0
	sink := SinkFunc(func(c Chunk) error {
		calls++
		if calls == failAt {
			return wantErr
		}
		return nil
	})

	res, err := Run(context.Background(), producer, sink)
	t.Logf("run=%s clause=extra phase=after delivered=%d upstream_aborted=%v err=%v",
		id, res.ChunksDelivered, res.UpstreamAborted, err)

	if !errors.Is(err, wantErr) {
		t.Fatalf("run=%s expected sink error, got %v", id, err)
	}
	if !res.UpstreamAborted {
		t.Fatalf("run=%s sink error did not abort upstream", id)
	}
	if !observed.Load() {
		t.Fatalf("run=%s producer did not observe abort after sink error", id)
	}
	// failAt-1 chunks delivered successfully before the failing write.
	if res.ChunksDelivered != failAt-1 {
		t.Fatalf("run=%s delivered=%d want=%d", id, res.ChunksDelivered, failAt-1)
	}

	got := waitGoroutines(t, baseline, 1)
	if got > baseline+1 {
		t.Fatalf("run=%s goroutine leak: baseline=%d after=%d", id, baseline, got)
	}
}
