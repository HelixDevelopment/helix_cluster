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
