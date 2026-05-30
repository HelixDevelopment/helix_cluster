package context

import (
	stdctx "context"
	"testing"
	"time"
)

func TestWithTimeout(t *testing.T) {
	ctx, cancel := WithTimeout(stdctx.Background(), 1*time.Second)
	defer cancel()
	if _, ok := ctx.Deadline(); !ok {
		t.Error("expected deadline")
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
