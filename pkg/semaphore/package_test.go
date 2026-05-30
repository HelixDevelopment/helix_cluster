package semaphore

import (
	"testing"
	"time"
)

func TestSemaphore(t *testing.T) {
	s := New(1)
	s.Acquire()
	if s.TryAcquire() {
		t.Error("expected TryAcquire to fail when full")
	}
	s.Release()
	if !s.TryAcquire() {
		t.Error("expected TryAcquire to succeed after release")
	}
}

// --- Mutation tests ---

func TestSemaphore_CapacityRespected_Mutation(t *testing.T) {
	// Mutation: channel buffer size wrong → semaphore allows more than N concurrent holders
	s := New(2)
	s.Acquire()
	s.Acquire()
	if s.TryAcquire() {
		t.Error("capacity=2: third TryAcquire should fail")
	}
	s.Release()
	if !s.TryAcquire() {
		t.Error("after one release, TryAcquire should succeed")
	}
}

func TestSemaphore_AcquireBlocks_Mutation(t *testing.T) {
	// Mutation: Acquire uses TryAcquire (non-blocking) instead of blocking send
	s := New(1)
	s.Acquire()
	done := make(chan struct{})
	go func() {
		s.Acquire() // should block until Release
		close(done)
	}()
	select {
	case <-done:
		t.Error("second Acquire should block while semaphore is held")
	case <-time.After(50 * time.Millisecond):
		// expected
	}
	s.Release()
	select {
	case <-done:
		// expected
	case <-time.After(200 * time.Millisecond):
		t.Error("second Acquire should unblock after Release")
	}
}

func TestSemaphore_ReleaseTooMany_Mutation(t *testing.T) {
	// Mutation: Release does not validate underflow → extra releases corrupt capacity
	s := New(1)
	s.Acquire()
	s.Release()
	// Releasing again would deadlock or panic if channel is empty
	done := make(chan struct{})
	go func() {
		defer close(done)
		// This should deadlock or panic if Release is broken; we just ensure we don't hang test
		s.Release()
	}()
	select {
	case <-done:
		// If Release doesn't block on empty channel, it completes immediately.
		// We verify state is still sane by acquiring again.
		if !s.TryAcquire() {
			t.Error("semaphore should still be usable after extra release")
		}
	case <-time.After(100 * time.Millisecond):
		// Release blocked because channel was empty — correct behavior
	}
}
