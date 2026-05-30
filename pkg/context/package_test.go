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
