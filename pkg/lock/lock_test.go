package lock

import (
	"context"
	"sync"
	"testing"
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

func TestMemoryLockerConcurrent(t *testing.T) {
	l := NewMemoryLocker()
	ctx := context.Background()
	var counter int
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock, err := l.Lock(ctx, "counter")
			if err != nil {
				t.Errorf("lock: %v", err)
				return
			}
			counter++
			unlock()
		}()
	}

	wg.Wait()
	if counter != 10 {
		t.Errorf("counter = %d, want 10", counter)
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
