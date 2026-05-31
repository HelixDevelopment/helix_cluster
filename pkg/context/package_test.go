package context

import (
	stdctx "context"
	"testing"
	"time"
)

type ctxKey string

func TestWithTimeout(t *testing.T) {
	const timeout = 1 * time.Second
	start := time.Now()
	ctx, cancel := WithTimeout(stdctx.Background(), timeout)
	defer cancel()

	dl, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected deadline")
	}
	// Deadline must be approximately start + timeout (not a hardcoded/ignored value).
	got := dl.Sub(start)
	const tol = 100 * time.Millisecond
	if got < timeout-tol || got > timeout+tol {
		t.Errorf("deadline %v not within %v of timeout %v", got, tol, timeout)
	}
}

// TestWithTimeout_PositiveExpiry proves a real positive-duration timeout actually
// fires and surfaces context.DeadlineExceeded (not a swallowed/ignored timeout).
func TestWithTimeout_PositiveExpiry(t *testing.T) {
	const timeout = 30 * time.Millisecond
	start := time.Now()
	ctx, cancel := WithTimeout(stdctx.Background(), timeout)
	defer cancel()

	select {
	case <-ctx.Done():
		elapsed := time.Since(start)
		// Must wait a real positive duration before expiring (mutation: timeout==0).
		if elapsed < timeout/2 {
			t.Errorf("context expired too early (%v) for timeout %v", elapsed, timeout)
		}
		if err := ctx.Err(); err != stdctx.DeadlineExceeded {
			t.Errorf("expected DeadlineExceeded, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("positive timeout never expired")
	}
}

// TestWithTimeout_CancelPath proves the returned CancelFunc is wired through and
// transitions Err() to context.Canceled (mutation: cancel swallowed/dropped).
func TestWithTimeout_CancelPath(t *testing.T) {
	ctx, cancel := WithTimeout(stdctx.Background(), 1*time.Hour)
	cancel()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("cancel() did not close Done()")
	}
	if err := ctx.Err(); err != stdctx.Canceled {
		t.Errorf("expected Canceled after cancel(), got %v", err)
	}
}

// TestDetach_PreservesValues proves the headline guarantee from package.go:14 —
// Detach "keeps only values": a value stored in the parent SURVIVES through the
// detached context even after the parent is cancelled, and the detached context
// is itself NOT cancelled. Mutation-paired: if Detach returned context.Background()
// (or any fresh context that drops values), Value(key) would be nil and this fails.
func TestDetach_PreservesValues(t *testing.T) {
	const key ctxKey = "request-id"
	const want = "req-42"

	parent, cancel := stdctx.WithCancel(stdctx.WithValue(stdctx.Background(), key, want))
	detachedCtx := Detach(parent)

	// Cancel the parent: the value must still be carried, and detached must not cancel.
	cancel()
	time.Sleep(10 * time.Millisecond) // let cancellation propagate

	// 1) Value actually carried (mutation: Detach returns Background() -> nil).
	got := detachedCtx.Value(key)
	if got == nil {
		t.Fatal("detached context dropped the value; Value(key) is nil")
	}
	if s, ok := got.(string); !ok || s != want {
		t.Errorf("detached context value = %v, want %q", got, want)
	}

	// 2) Detached context must NOT be cancelled despite parent cancellation.
	if err := detachedCtx.Err(); err != nil {
		t.Errorf("detached context should not be cancelled, Err() = %v", err)
	}
	select {
	case <-detachedCtx.Done():
		t.Error("detached context Done() fired after parent cancel")
	default:
	}
}

func TestDetach(t *testing.T) {
	ctx, cancel := stdctx.WithCancel(stdctx.Background())
	defer cancel()
	detachedCtx := Detach(ctx)
	select {
	case <-detachedCtx.Done():
		t.Error("detached context should not be done")
	default:
	}
}

// --- Mutation tests ---

func TestDetach_DoneNotNil_Mutation(t *testing.T) {
	// Mutation: Detached Done() returns parent's Done() channel instead of nil
	// → cancelling parent would close the detached context's Done channel
	parent, cancel := stdctx.WithCancel(stdctx.Background())
	detachedCtx := Detach(parent)
	cancel()
	select {
	case <-detachedCtx.Done():
		t.Error("detached context should remain uncancelled even when parent is cancelled")
	case <-time.After(50 * time.Millisecond):
		// expected: detached context ignores parent's cancellation
	}
}

func TestDetach_ErrNotNil_Mutation(t *testing.T) {
	// Mutation: Detached Err() returns parent's Err() instead of nil
	parent, cancel := stdctx.WithCancel(stdctx.Background())
	detachedCtx := Detach(parent)
	cancel()
	// Give parent time to propagate cancellation
	time.Sleep(10 * time.Millisecond)
	if err := detachedCtx.Err(); err != nil {
		t.Errorf("detached context Err() should be nil, got %v", err)
	}
}

func TestWithTimeout_ZeroTimeout_Mutation(t *testing.T) {
	// Mutation: WithTimeout ignores timeout parameter → zero timeout would not expire
	ctx, cancel := WithTimeout(stdctx.Background(), 0)
	defer cancel()
	select {
	case <-ctx.Done():
		// expected: zero timeout should immediately expire
	case <-time.After(100 * time.Millisecond):
		t.Error("zero timeout should have expired immediately")
	}
}
