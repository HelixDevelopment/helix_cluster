package workerpool

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestPool(t *testing.T) {
	p := New(2)
	var counter int32
	done := make(chan struct{})
	p.Submit(func() {
		atomic.AddInt32(&counter, 1)
		close(done)
	})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for worker")
	}
	p.Stop()
	if atomic.LoadInt32(&counter) != 1 {
		t.Errorf("expected counter 1, got %d", counter)
	}
}
