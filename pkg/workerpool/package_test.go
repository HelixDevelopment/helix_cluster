package workerpool

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestPool(t *testing.T) {
	p := New(2)
	var counter int32
	p.Submit(func() {
		atomic.AddInt32(&counter, 1)
	})
	time.Sleep(100 * time.Millisecond)
	p.Stop()
	if atomic.LoadInt32(&counter) != 1 {
		t.Errorf("expected counter 1, got %d", counter)
	}
}
