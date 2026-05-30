package events

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestBus(t *testing.T) {
	b := NewBus()
	var counter int32
	b.Subscribe("test", func(e Event) {
		atomic.AddInt32(&counter, 1)
	})
	b.Publish(Event{ID: "1", Type: "test"})
	time.Sleep(100 * time.Millisecond)
	if atomic.LoadInt32(&counter) != 1 {
		t.Errorf("expected counter 1, got %d", counter)
	}
}

// --- Mutation tests ---

func TestBus_MultipleSubscribers_Mutation(t *testing.T) {
	// Mutation: Subscribe overwrites previous handlers instead of appending
	b := NewBus()
	var c1, c2 int32
	b.Subscribe("evt", func(e Event) { atomic.AddInt32(&c1, 1) })
	b.Subscribe("evt", func(e Event) { atomic.AddInt32(&c2, 1) })
	b.Publish(Event{ID: "1", Type: "evt"})
	time.Sleep(100 * time.Millisecond)
	if atomic.LoadInt32(&c1) != 1 {
		t.Error("first subscriber should have been invoked")
	}
	if atomic.LoadInt32(&c2) != 1 {
		t.Error("second subscriber should have been invoked")
	}
}

func TestBus_WrongTypeNotDelivered_Mutation(t *testing.T) {
	// Mutation: Publish delivers events to all handlers regardless of type
	b := NewBus()
	var counter int32
	b.Subscribe("right", func(e Event) { atomic.AddInt32(&counter, 1) })
	b.Publish(Event{ID: "1", Type: "wrong"})
	time.Sleep(100 * time.Millisecond)
	if atomic.LoadInt32(&counter) != 0 {
		t.Error("handler for different type should not be invoked")
	}
}

func TestBus_NewBusNotNil_Mutation(t *testing.T) {
	// Mutation: NewBus returns nil → any method call would panic
	b := NewBus()
	if b == nil {
		t.Fatal("NewBus must not return nil")
	}
	// Should not panic
	b.Subscribe("x", func(e Event) {})
	b.Publish(Event{Type: "x"})
}
