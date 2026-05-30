package semaphore

import "testing"

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
