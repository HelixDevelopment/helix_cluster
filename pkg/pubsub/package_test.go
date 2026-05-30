package pubsub

import (
	"testing"
	"time"
)

func TestBroker(t *testing.T) {
	b := NewBroker()
	ch := b.Subscribe("test")
	b.Publish("test", "hello")
	msg := <-ch
	if msg != "hello" {
		t.Errorf("expected 'hello', got %s", msg)
	}
}

// --- Mutation tests ---

func TestBroker_MultipleSubscribers_Mutation(t *testing.T) {
	// Mutation: Subscribe overwrites previous channels for same subject
	b := NewBroker()
	ch1 := b.Subscribe("news")
	ch2 := b.Subscribe("news")
	b.Publish("news", "alert")
	select {
	case m1 := <-ch1:
		if m1 != "alert" {
			t.Errorf("ch1 expected 'alert', got %s", m1)
		}
	default:
		t.Error("ch1 should have received the message")
	}
	select {
	case m2 := <-ch2:
		if m2 != "alert" {
			t.Errorf("ch2 expected 'alert', got %s", m2)
		}
	default:
		t.Error("ch2 should have received the message")
	}
}

func TestBroker_WrongSubjectNotDelivered_Mutation(t *testing.T) {
	// Mutation: Publish broadcasts to all subjects regardless of key
	b := NewBroker()
	ch := b.Subscribe("sports")
	b.Publish("politics", "election")
	select {
	case <-ch:
		t.Error("message for wrong subject should not be delivered")
	default:
		// expected
	}
}

func TestBroker_NonBlockingPublish_Mutation(t *testing.T) {
	// Mutation: Publish blocks when channel full instead of dropping
	b := NewBroker()
	// Fill the 10-buffer channel
	ch := b.Subscribe("test")
	for i := 0; i < 10; i++ {
		b.Publish("test", "fill")
	}
	// Additional publish must not block (current impl drops via select default)
	done := make(chan struct{})
	go func() {
		b.Publish("test", "overflow")
		close(done)
	}()
	select {
	case <-done:
		// expected: non-blocking
	case <-time.After(500 * time.Millisecond):
		t.Error("Publish should not block when subscriber buffer is full")
	}
	_ = ch // consume if needed, but we already filled it
}
