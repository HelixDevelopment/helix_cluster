package workerpool

import (
	"sync"
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

// --- Mutation tests ---

func TestPool_MultipleWorkers_Mutation(t *testing.T) {
	// Mutation: New creates only 1 worker regardless of n → tasks serialize instead of parallelize
	p := New(3)
	var active int32
	var maxActive int32
	var mu sync.Mutex
	barrier := make(chan struct{})

	for i := 0; i < 3; i++ {
		p.Submit(func() {
			n := atomic.AddInt32(&active, 1)
			mu.Lock()
			if n > maxActive {
				maxActive = n
			}
			mu.Unlock()
			<-barrier
			atomic.AddInt32(&active, -1)
		})
	}

	time.Sleep(100 * time.Millisecond) // let workers pick up tasks
	mu.Lock()
	if maxActive < 3 {
		t.Errorf("expected 3 concurrent workers, got %d", maxActive)
	}
	mu.Unlock()
	close(barrier)
	p.Stop()
}

func TestPool_StopWaitsForWorkers_Mutation(t *testing.T) {
	// Mutation: Stop does not wait → work may not complete
	p := New(1)
	var completed int32
	p.Submit(func() {
		time.Sleep(50 * time.Millisecond)
		atomic.StoreInt32(&completed, 1)
	})
	p.Stop()
	if atomic.LoadInt32(&completed) != 1 {
		t.Error("Stop should wait for submitted work to finish")
	}
}

func TestPool_NilWorkSafe_Mutation(t *testing.T) {
	// Mutation: worker panics on nil fn instead of skipping
	p := New(1)
	// Submit nil work; current implementation checks fn != nil
	p.Submit(nil)
	// If nil check removed, this would panic.
	// We give worker time to process
	time.Sleep(50 * time.Millisecond)
	p.Stop()
	// Reaching here without panic means nil guard is in place
}
