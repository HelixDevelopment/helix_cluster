// Adversarial concurrency + delivery-correctness tests for InMemoryBroker.
//
// These tests target the classic in-memory pub/sub failure modes that the
// existing suite does NOT exercise:
//
//   - send-on-closed-channel when a subscriber unsubscribes (ctx cancel) while a
//     Publish is fanning out to that subscriber's channel.
//   - a data race on the shared subjects map between Subscribe (write-lock,
//     slice append), Publish (read-lock, map read + send) and the per-subscriber
//     ctx.Done() goroutine that mutates the slice and closes the channel.
//   - delivery to an already-unsubscribed subscriber (registry leak).
//   - cross-topic delivery (broken routing).
//   - publisher liveness against a saturated subscriber (slow-subscriber policy).
//
// The existing TestInMemoryBroker_ConcurrentProducersConsumers_Race subscribes
// everything up front, publishes, THEN drains — it never cancels a subscriber
// mid-fan-out, so it cannot surface the send-on-closed / registry-mutation race.
// These tests close that gap. Run with -race.
//
// IMPLEMENTATION NOTE on the documented contract: InMemoryBroker uses a fixed
// 10-slot buffer per subscriber and a NON-BLOCKING send (select-default drop) —
// see the InMemoryBroker doc comment and Publish in broker.go. Therefore a test
// that asserts "every message arrives" MUST pace publishing so the per-subscriber
// drain keeps the buffer below capacity; otherwise the documented drop kicks in
// and the loss is correct behavior, not a bug. The delivery/isolation tests below
// pace accordingly (lock-step publish→receive), which still fully exercises the
// fan-out and routing paths.
//
// Worker goroutines NEVER call t.Errorf/t.Fatalf (that races the test runner's
// access to testing.common under -race and would be a TEST bug, not a broker
// bug); failures are reported via atomics/channels and asserted on the test
// goroutine after a barrier.
package pubsub

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestInMemoryBroker_ConcurrentSubUnsubPublish_NoSendOnClosed is the headline
// adversarial test. Many goroutines simultaneously Subscribe, Publish, and
// Unsubscribe (via ctx cancel) on a small set of OVERLAPPING topics. The crux:
// a subscriber's ctx is cancelled (triggering slice removal + close(ch)) WHILE
// other goroutines are publishing a fan-out to the same topic.
//
// Why it bites:
//   - If the broker ever sent on a channel after closing it (the classic pub/sub
//     bug), the runtime panics "send on closed channel". Every worker installs a
//     recover() that records the panic in an atomic; after the barrier the test
//     goroutine fails loudly. A panic is detected, never silently swallowed.
//   - Run under -race, any unsynchronized access to b.subjects (the registry) or
//     a concurrent map read during a slice append/remove fires the race detector,
//     which fails the test. This bites a missing/wrong mutex.
//
// Liveness: the whole churn must finish within the deadline (a blocking fan-out
// or deadlock would hang and trip the watchdog).
func TestInMemoryBroker_ConcurrentSubUnsubPublish_NoSendOnClosed(t *testing.T) {
	const (
		topics      = 6
		workers     = 24
		iterations  = 300
		testTimeout = 30 * time.Second
	)

	b := NewInMemoryBroker()
	topicName := func(i int) string { return fmt.Sprintf("topic-%d", i%topics) }

	var panics int32
	var panicMsg atomic.Value // last recovered panic, for the failure message
	var subErr atomic.Value
	var wg sync.WaitGroup

	recoverInto := func() {
		if r := recover(); r != nil {
			atomic.AddInt32(&panics, 1)
			panicMsg.Store(fmt.Sprintf("%v", r))
		}
	}

	// Publisher workers: continuously fan out to overlapping topics.
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			defer recoverInto()
			for i := 0; i < iterations; i++ {
				_ = b.Publish(context.Background(), topicName(w+i), fmt.Sprintf("k%d", i), fmt.Sprintf("v-%d-%d", w, i))
			}
		}(w)
	}

	// Subscriber/unsubscriber workers: subscribe then cancel (unsubscribe) in a
	// tight loop, overlapping the publishers' fan-out. The cancel races directly
	// against in-flight sends to the just-removed channel — the bug spot.
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			defer recoverInto()
			for i := 0; i < iterations; i++ {
				ctx, cancel := context.WithCancel(context.Background())
				ch, err := b.Subscribe(ctx, topicName(w+i), "g")
				if err != nil {
					subErr.Store(err)
					cancel()
					return
				}
				// Drain opportunistically so buffers don't wedge and so we touch
				// the receive side concurrently with publishers.
				go func(ch <-chan Message) {
					for range ch { //nolint:revive // intentional drain until close
					}
				}(ch)
				// Unsubscribe mid-stream: removes ch from the registry and closes
				// it, racing the publishers' fan-out on the same topic.
				cancel()
			}
		}(w)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatalf("concurrent churn did not complete within %s — publisher wedged or deadlock", testTimeout)
	}

	if err := subErr.Load(); err != nil {
		t.Fatalf("Subscribe returned error during churn: %v", err)
	}
	if n := atomic.LoadInt32(&panics); n != 0 {
		t.Fatalf("observed %d panic(s) during concurrent sub/unsub/publish churn (classic send-on-closed?): %v",
			n, panicMsg.Load())
	}
}

// TestInMemoryBroker_UnsubscribedReceivesNothing proves the registry is honestly
// pruned on unsubscribe: after a subscriber's ctx is cancelled, a subsequent
// Publish to that topic delivers NOTHING to it (no leak, no delivery to a removed
// subscriber). An active subscriber on the same topic still receives the message
// (delivery correctness for the survivor).
//
// Why it bites: if Subscribe's ctx.Done() goroutine failed to remove ch from the
// registry, Publish would still try to deliver to the cancelled subscriber. Two
// detectors: (1) the cancelled channel is closed, so a stray send panics
// send-on-closed; (2) a leaked-but-open channel would surface a value on the
// post-cancel receive below. Either fails the test.
func TestInMemoryBroker_UnsubscribedReceivesNothing(t *testing.T) {
	b := NewInMemoryBroker()

	liveCtx, liveCancel := context.WithCancel(context.Background())
	defer liveCancel()
	live, err := b.Subscribe(liveCtx, "orders", "g-live")
	if err != nil {
		t.Fatalf("Subscribe live: %v", err)
	}

	goneCtx, goneCancel := context.WithCancel(context.Background())
	gone, err := b.Subscribe(goneCtx, "orders", "g-gone")
	if err != nil {
		t.Fatalf("Subscribe gone: %v", err)
	}

	// Unsubscribe `gone` and wait until its channel is actually closed (the
	// ctx.Done goroutine has run and pruned the registry).
	goneCancel()
	closed := false
	deadline := time.After(2 * time.Second)
	for !closed {
		select {
		case _, open := <-gone:
			if !open {
				closed = true
			}
			// if open: a value buffered before cancel; keep draining to close.
		case <-deadline:
			t.Fatal("unsubscribed channel was not closed within deadline")
		}
	}

	// Publish AFTER unsubscribe. Must reach `live`, must not reach `gone`.
	if err := b.Publish(context.Background(), "orders", "k", "after-unsub"); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	select {
	case m, open := <-live:
		if !open {
			t.Fatal("live subscriber channel unexpectedly closed")
		}
		if string(m.Value) != "after-unsub" {
			t.Errorf("live got %q, want %q", m.Value, "after-unsub")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("live subscriber did not receive post-unsubscribe publish")
	}

	// `gone` must still be closed and yield no further values.
	select {
	case _, open := <-gone:
		if open {
			t.Error("unsubscribed subscriber received a value after unsubscribe (registry leak)")
		}
	default:
		t.Error("unsubscribed channel should be closed and immediately receive-ready")
	}
}

// TestInMemoryBroker_DeliveryCorrectness_AllMessagesArrive proves no-loss
// delivery for a subscriber active across the whole run. Because the broker drops
// on a full 10-slot buffer (documented non-blocking contract), publishing is
// paced lock-step with receiving so the buffer never overflows — this still fully
// exercises the fan-out path while making the no-loss assertion legitimate.
//
// Why it bites: a broken fan-out (lost append, wrong slice handling under the
// lock, value corruption) would drop/scramble messages and the per-index value
// assertion or final count fails.
func TestInMemoryBroker_DeliveryCorrectness_AllMessagesArrive(t *testing.T) {
	const total = 5000

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := NewInMemoryBroker()
	ch, err := b.Subscribe(ctx, "stream", "g")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	for i := 0; i < total; i++ {
		want := fmt.Sprintf("m-%d", i)
		if err := b.Publish(context.Background(), "stream", "k", want); err != nil {
			t.Fatalf("Publish %d: %v", i, err)
		}
		// Receive lock-step so the 10-slot buffer never overflows -> no drop.
		select {
		case m, open := <-ch:
			if !open {
				t.Fatalf("channel closed early at i=%d", i)
			}
			if string(m.Value) != want {
				t.Fatalf("ordering/delivery bug at i=%d: got %q, want %q", i, m.Value, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("message %d (%q) never delivered", i, want)
		}
	}
}

// TestInMemoryBroker_TopicIsolation proves a message published to topic A is
// never delivered to a subscriber that only subscribed to topic B, even while
// both are active concurrently. Bites a broken/shared registry that ignores the
// topic key. Publishing to A is paced lock-step with A's drain so the documented
// drop never masks a routing bug.
func TestInMemoryBroker_TopicIsolation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := NewInMemoryBroker()
	aCh, _ := b.Subscribe(ctx, "topic-A", "g")
	bCh, _ := b.Subscribe(ctx, "topic-B", "g")

	const n = 500
	for i := 0; i < n; i++ {
		if err := b.Publish(context.Background(), "topic-A", "k", "A"); err != nil {
			t.Fatalf("Publish: %v", err)
		}
		select {
		case m := <-aCh:
			if string(m.Value) != "A" {
				t.Fatalf("topic-A subscriber got %q, want A", m.Value)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("topic-A subscriber stalled at i=%d", i)
		}
		// topic-B subscriber must NEVER have anything queued.
		select {
		case m := <-bCh:
			t.Fatalf("topic-B subscriber received %q from a topic-A-only publish (broken isolation)", m.Value)
		default:
			// correct: no cross-topic delivery
		}
	}
}

// TestInMemoryBroker_SlowSubscriberDoesNotWedgePublisher asserts the DOCUMENTED
// slow-subscriber policy: "overflow is silently dropped (non-blocking contract)"
// (InMemoryBroker doc comment + Publish select-default). A subscriber that never
// drains MUST NOT block the publisher, and MUST NOT block delivery to a fast
// co-subscriber on the same topic.
//
// Why it bites: if the non-blocking select-default were removed (blocking send),
// the saturated slow subscriber would wedge every Publish and BOTH the publisher
// watchdog AND the fast subscriber's delivery would time out — surfacing the real
// liveness bug rather than passing.
func TestInMemoryBroker_SlowSubscriberDoesNotWedgePublisher(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := NewInMemoryBroker()

	// Slow subscriber: subscribes but never reads -> buffer saturates at 10.
	slow, _ := b.Subscribe(ctx, "mixed", "slow")
	_ = slow

	// Fast subscriber: drained promptly below.
	fast, _ := b.Subscribe(ctx, "mixed", "fast")

	const n = 1000
	pubDone := make(chan struct{})
	go func() {
		defer close(pubDone)
		for i := 0; i < n; i++ {
			// Pace fast's drain alongside so it isn't lost to its own 10-buffer.
			_ = b.Publish(context.Background(), "mixed", "k", fmt.Sprintf("m-%d", i))
			select {
			case <-fast:
			default:
			}
		}
	}()

	// Publisher must finish quickly despite the wedged slow subscriber.
	select {
	case <-pubDone:
	case <-time.After(5 * time.Second):
		t.Fatal("publisher wedged by a slow/full subscriber (blocking fan-out liveness bug)")
	}

	// Fast subscriber must still be able to receive (slow one didn't starve it).
	// Publish a few more and confirm at least one arrives promptly.
	got := 0
	deadline := time.After(2 * time.Second)
	for got < 5 {
		_ = b.Publish(context.Background(), "mixed", "k", "live-check")
		select {
		case <-fast:
			got++
		case <-deadline:
			t.Fatalf("fast co-subscriber starved by slow subscriber: got %d", got)
		}
	}
}
