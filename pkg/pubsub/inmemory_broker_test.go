// Unit tests for InMemoryBroker — deterministic, no external deps.
// All tests run under -short -race.
package pubsub

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestInMemoryBroker_PublishSubscribe_RoundTrip is the fundamental sink-side
// assertion: the exact value published must appear on the subscriber's channel.
func TestInMemoryBroker_PublishSubscribe_RoundTrip(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := NewInMemoryBroker()
	ch, err := b.Subscribe(ctx, "greetings", "g1")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	wantKey := "k1"
	wantVal := "hello-world"

	if err := b.Publish(ctx, "greetings", wantKey, wantVal); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	select {
	case msg := <-ch:
		if string(msg.Key) != wantKey {
			t.Errorf("key = %q, want %q", msg.Key, wantKey)
		}
		if string(msg.Value) != wantVal {
			t.Errorf("value = %q, want %q", msg.Value, wantVal)
		}
		if msg.Topic != "greetings" {
			t.Errorf("topic = %q, want %q", msg.Topic, "greetings")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for message")
	}
}

// TestInMemoryBroker_WrongTopicIsolation_Mutation guards the topic-routing
// branch: publishing to "X" must NOT deliver to a subscriber on "Y".
// Mutation: if Publish broadcasted to all subjects regardless of key, this test
// fails because ch would receive the unrelated message.
func TestInMemoryBroker_WrongTopicIsolation_Mutation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := NewInMemoryBroker()
	ch, _ := b.Subscribe(ctx, "sports", "g1")
	_ = b.Publish(ctx, "politics", "k", "election")

	select {
	case m := <-ch:
		t.Errorf("received %q from unrelated topic 'politics'", m.Value)
	case <-time.After(50 * time.Millisecond):
		// expected: nothing delivered
	}
}

// TestInMemoryBroker_MultipleSubscribers_Mutation verifies fan-out: all
// subscribers on the same topic receive the published message.
// Mutation: if Subscribe overwrote instead of appended, ch2 would miss the msg.
func TestInMemoryBroker_MultipleSubscribers_Mutation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := NewInMemoryBroker()
	ch1, _ := b.Subscribe(ctx, "news", "g1")
	ch2, _ := b.Subscribe(ctx, "news", "g2")

	_ = b.Publish(ctx, "news", "k", "alert")

	for i, ch := range []<-chan Message{ch1, ch2} {
		select {
		case m := <-ch:
			if string(m.Value) != "alert" {
				t.Errorf("subscriber %d: got %q, want 'alert'", i+1, m.Value)
			}
		case <-time.After(time.Second):
			t.Errorf("subscriber %d: timeout", i+1)
		}
	}
}

// TestInMemoryBroker_BufferBoundary_Mutation proves the 10-slot drop contract:
// exactly 10 messages are retained; the 11th is silently dropped.
// Mutation: removing the select-default (blocking on full) would cause Publish
// to deadlock and the test goroutine never completes, OR if buffer size changed
// the retained count would differ.
func TestInMemoryBroker_BufferBoundary_Mutation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := NewInMemoryBroker()
	ch, _ := b.Subscribe(ctx, "boundary", "g1")

	for i := 0; i < 10; i++ {
		_ = b.Publish(ctx, "boundary", "k", fmt.Sprintf("msg-%d", i))
	}
	// 11th must be dropped, not block.
	done := make(chan struct{})
	go func() {
		_ = b.Publish(ctx, "boundary", "k", "msg-10-dropped")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Publish blocked on full buffer — select-default missing?")
	}

	// Drain and verify exact retained set.
	got := make([]string, 0, 11)
	for {
		select {
		case m := <-ch:
			got = append(got, string(m.Value))
		default:
			goto drained
		}
	}
drained:
	if len(got) != 10 {
		t.Fatalf("expected 10 retained messages, got %d: %v", len(got), got)
	}
	for i, v := range got {
		if want := fmt.Sprintf("msg-%d", i); v != want {
			t.Errorf("retained[%d] = %q, want %q", i, v, want)
		}
	}
	for _, v := range got {
		if v == "msg-10-dropped" {
			t.Error("11th message should have been dropped but was present")
		}
	}
}

// TestInMemoryBroker_ContextCancellationClosesChannel_Mutation verifies that
// cancelling the context closes the channel so a range-over works cleanly.
// Mutation: removing the goroutine that closes ch on cancel would leave the
// channel open forever and this test would time-out.
func TestInMemoryBroker_ContextCancellationClosesChannel_Mutation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	b := NewInMemoryBroker()
	ch, _ := b.Subscribe(ctx, "cancel-test", "g1")

	cancel() // trigger closure

	select {
	case _, open := <-ch:
		if open {
			t.Error("channel should be closed but received a value")
		}
		// correctly closed
	case <-time.After(time.Second):
		t.Fatal("channel was not closed after context cancellation")
	}
}

// TestInMemoryBroker_PublishCancelledContext_Mutation verifies that Publish
// with an already-cancelled context returns ctx.Err() without delivering.
// Mutation: if Publish ignored ctx, it would return nil and potentially deliver.
func TestInMemoryBroker_PublishCancelledContext_Mutation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	b := NewInMemoryBroker()
	err := b.Publish(ctx, "topic", "k", "v")
	if err == nil {
		t.Error("Publish on cancelled context must return non-nil error")
	}
}

// TestInMemoryBroker_UnknownTopicPublish_NoOp proves publishing to an
// unsubscribed topic is safe (no panic) and delivers nothing to other subs.
func TestInMemoryBroker_UnknownTopicPublish_NoOp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := NewInMemoryBroker()
	other, _ := b.Subscribe(ctx, "known", "g1")

	// Must not panic.
	if err := b.Publish(ctx, "unknown-topic", "k", "x"); err != nil {
		t.Errorf("Publish to unknown topic returned error: %v", err)
	}

	select {
	case m := <-other:
		t.Errorf("unrelated subscriber received %q from unknown-topic publish", m.Value)
	case <-time.After(50 * time.Millisecond):
		// correct
	}
}

// TestInMemoryBroker_ConcurrentProducersConsumers_Race spawns N publishers and
// M subscribers concurrently and asserts deterministic per-subscriber counts.
// Buffer is saturated to cap but not overflowed (deterministic drop-free).
// -race flag will catch any missing locks.
func TestInMemoryBroker_ConcurrentProducersConsumers_Race(t *testing.T) {
	const (
		numSubjects        = 4
		subsPerSubject     = 3
		numPublishers      = 5
		msgsPerPublisher   = 2 // 5*2 == 10 == buffer cap; no drops
		perSubjectMessages = numPublishers * msgsPerPublisher
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := NewInMemoryBroker()

	subjects := make([]string, numSubjects)
	for i := range subjects {
		subjects[i] = fmt.Sprintf("subject-%d", i)
	}

	type counter struct {
		subject string
		ch      <-chan Message
		got     int
	}

	var subscribers []*counter
	for _, s := range subjects {
		for j := 0; j < subsPerSubject; j++ {
			ch, _ := b.Subscribe(ctx, s, fmt.Sprintf("g-%d", j))
			subscribers = append(subscribers, &counter{subject: s, ch: ch})
		}
	}

	var producersWG sync.WaitGroup
	for _, s := range subjects {
		for p := 0; p < numPublishers; p++ {
			producersWG.Add(1)
			go func(s string, p int) {
				defer producersWG.Done()
				for m := 0; m < msgsPerPublisher; m++ {
					_ = b.Publish(ctx, s, fmt.Sprintf("k-%d", p), fmt.Sprintf("%s-p%d-m%d", s, p, m))
				}
			}(s, p)
		}
	}

	// Concurrent Subscribe on new subjects — exercises write-lock path.
	for i := 0; i < numSubjects; i++ {
		producersWG.Add(1)
		go func(i int) {
			defer producersWG.Done()
			_, _ = b.Subscribe(ctx, fmt.Sprintf("late-%d", i), "g1")
		}(i)
	}

	producersWG.Wait()

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

	for _, c := range subscribers {
		if c.got != perSubjectMessages {
			t.Errorf("subscriber on %q received %d messages, want %d",
				c.subject, c.got, perSubjectMessages)
		}
	}
}

// TestInMemoryBroker_Close_NoError verifies that Close() returns nil.
func TestInMemoryBroker_Close_NoError(t *testing.T) {
	b := NewInMemoryBroker()
	if err := b.Close(); err != nil {
		t.Errorf("Close() returned error: %v", err)
	}
}
