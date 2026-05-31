package pubsub

import (
	"fmt"
	"sync"
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

// TestBroker_BufferBoundary_Mutation proves the documented drop contract is
// exact: with a 10-slot buffer and no concurrent drain, exactly the first 10
// published messages are retained and the 11th is dropped. If the buffer size
// changed or the select-default drop were removed (causing block/overwrite),
// this test fails.
func TestBroker_BufferBoundary_Mutation(t *testing.T) {
	b := NewBroker()
	ch := b.Subscribe("boundary")

	// Publish exactly buffer-size + 1 messages with no reader draining.
	for i := 0; i < 10; i++ {
		b.Publish("boundary", fmt.Sprintf("msg-%d", i))
	}
	// The 11th message must be dropped (buffer is full).
	b.Publish("boundary", "msg-10-dropped")

	// Drain and verify exactly 10 messages retained, in FIFO order, and that
	// the 11th ("msg-10-dropped") is NOT present.
	got := make([]string, 0, 11)
	for {
		select {
		case m := <-ch:
			got = append(got, m)
			continue
		default:
		}
		break
	}

	if len(got) != 10 {
		t.Fatalf("expected exactly 10 retained messages, got %d: %v", len(got), got)
	}
	for i := 0; i < 10; i++ {
		want := fmt.Sprintf("msg-%d", i)
		if got[i] != want {
			t.Errorf("retained[%d] = %q, want %q", i, got[i], want)
		}
	}
	for _, m := range got {
		if m == "msg-10-dropped" {
			t.Error("11th message should have been dropped but was delivered")
		}
	}
}

// TestBroker_UnknownSubjectPublish_NoOp proves that publishing to a subject
// that was never subscribed is a safe no-op: it must not panic and must
// deliver nothing to unrelated subscribers. Guards the nil/empty-subject path.
func TestBroker_UnknownSubjectPublish_NoOp(t *testing.T) {
	b := NewBroker()
	other := b.Subscribe("known")

	// Must not panic on a completely unknown subject.
	b.Publish("never-subscribed", "x")

	// An unrelated subscriber must receive nothing from that publish.
	select {
	case m := <-other:
		t.Errorf("unrelated subscriber received %q from unknown-subject publish", m)
	default:
		// expected: no delivery
	}
}

// TestBroker_ConcurrentProducersConsumers_Race spawns N publishers and M
// subscribers over overlapping subjects and asserts deterministic per-subscriber
// received counts.
//
// Determinism: the broker drops on a full buffer (intended, non-blocking
// contract). To make counts EXACT regardless of goroutine scheduling, the total
// number of messages published to each subject is held at the 10-slot buffer
// capacity, and draining happens only after a barrier guarantees all publishing
// is complete. The buffer therefore holds every message and nothing is dropped,
// so each of the M subscribers on a subject must observe exactly
// perSubjectMessages messages.
//
// Race coverage: N publishers per subject call Publish (read-lock + map read)
// concurrently while extra goroutines call Subscribe (write-lock + slice append)
// on overlapping/new subjects. Run with -race: if the RWMutex were removed, the
// concurrent append in Subscribe and the map read in Publish race and the
// detector fires.
func TestBroker_ConcurrentProducersConsumers_Race(t *testing.T) {
	const (
		numSubjects    = 4
		subsPerSubject = 3 // M subscribers per subject
		numPublishers  = 5 // N publishers per subject
		// Per-subject total messages must not exceed the broker's 10-slot
		// buffer, otherwise the non-blocking Publish would drop and counts
		// would stop being deterministic. 5 publishers * 2 each = 10 == buffer.
		msgsPerPublisher   = 2
		perSubjectMessages = numPublishers * msgsPerPublisher
	)

	b := NewBroker()

	subjects := make([]string, numSubjects)
	for i := range subjects {
		subjects[i] = fmt.Sprintf("subject-%d", i)
	}

	type counter struct {
		subject string
		ch      chan string
		got     int
	}

	// Subscribe first (overlapping: M subscribers share each subject).
	var subscribers []*counter
	for _, s := range subjects {
		for j := 0; j < subsPerSubject; j++ {
			subscribers = append(subscribers, &counter{subject: s, ch: b.Subscribe(s)})
		}
	}

	// Publishers run concurrently across all subjects (read-lock path).
	var producersWG sync.WaitGroup
	for _, s := range subjects {
		for p := 0; p < numPublishers; p++ {
			producersWG.Add(1)
			go func(s string, p int) {
				defer producersWG.Done()
				for m := 0; m < msgsPerPublisher; m++ {
					b.Publish(s, fmt.Sprintf("%s-p%d-m%d", s, p, m))
				}
			}(s, p)
		}
	}

	// Concurrently exercise the write-lock path (Subscribe) against the
	// read-lock path (Publish) to give -race overlapping lock acquisitions on
	// the shared subjects map. These late subscribers may miss messages, so
	// they are intentionally not asserted on.
	for i := 0; i < numSubjects; i++ {
		producersWG.Add(1)
		go func(i int) {
			defer producersWG.Done()
			_ = b.Subscribe(fmt.Sprintf("late-subject-%d", i))
		}(i)
	}

	// Barrier: all publishing is finished before any draining begins. Because
	// per-subject volume == buffer capacity, every message is buffered and none
	// is dropped.
	producersWG.Wait()

	// Drain each subscriber concurrently and count.
	var consumersWG sync.WaitGroup
	for _, c := range subscribers {
		consumersWG.Add(1)
		go func(c *counter) {
			defer consumersWG.Done()
			for {
				select {
				case <-c.ch:
					c.got++
				default:
					return
				}
			}
		}(c)
	}
	consumersWG.Wait()

	// Deterministic assertion: every subscriber received exactly the full count
	// of messages published to its subject.
	for _, c := range subscribers {
		if c.got != perSubjectMessages {
			t.Errorf("subscriber on %q received %d messages, want %d",
				c.subject, c.got, perSubjectMessages)
		}
	}
}
