package pubsub

import "testing"

func TestBroker(t *testing.T) {
	b := NewBroker()
	ch := b.Subscribe("test")
	b.Publish("test", "hello")
	msg := <-ch
	if msg != "hello" {
		t.Errorf("expected 'hello', got %s", msg)
	}
}
