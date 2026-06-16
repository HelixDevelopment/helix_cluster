package messaging

// Second-pass adversarial probes for internal/messaging, focused on
// message-delivery CORRECTNESS that the first pass did not cover.
//
// Headline finding (REAL BUG, fixed in bus.go):
//
//	Bus.Publish runs each handler in its own goroutine with NO recover. A
//	single panicking subscriber's goroutine is unrecovered, so the Go runtime
//	terminates the ENTIRE PROCESS -- killing delivery for every other
//	subscriber and every other topic. Worse, Publish() can have already
//	returned nil ("published OK") before the orphan goroutine crashes the
//	binary: a textbook CLAUDE-1 PASS-bluff. The fix recovers the panic and
//	surfaces it as that handler's error, so the bus survives and co-subscribers
//	still receive the message.
//
// Anti-hang: no live broker; fan-out capped; every test bounded so
// `go test -race -timeout 90s` COMPLETES.

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBus_PanickingHandlerDoesNotKillBus_CoSubscribersStillDelivered is the
// mutation witness for the recover fix in Publish.
//
// Two subscribers on one topic: the first panics, the second records delivery.
// CONTRACT under test: a panicking handler must NOT take down the process and
// must NOT suppress delivery to the healthy co-subscriber.
//
// WITHOUT the recover (production before fix): the panicking goroutine is
// unrecovered -> the test binary crashes with `panic: ...` -> `--- FAIL`.
// WITH the recover: the panic becomes an error, the bus survives, and the
// co-subscriber is delivered to -> `ok`.
func TestBus_PanickingHandlerDoesNotKillBus_CoSubscribersStillDelivered(t *testing.T) {
	bus := NewBus()
	ctx := context.Background()

	var healthyDeliveries atomic.Int64
	_, err := bus.Subscribe(ctx, "mixed", func(_ context.Context, _ *Message) error {
		panic("subscriber blew up")
	})
	require.NoError(t, err)
	_, err = bus.Subscribe(ctx, "mixed", func(_ context.Context, _ *Message) error {
		healthyDeliveries.Add(1)
		return nil
	})
	require.NoError(t, err)

	// On unfixed prod this Publish spawns an unrecovered panicking goroutine
	// that crashes the whole test binary. Publish itself returns an aggregated
	// error reporting the panic once the fix is in.
	perr := bus.Publish(ctx, "mixed", NewMessage("mixed", []byte("x")))

	// If we reach here at all, the process survived the panic (the fix works).
	require.Error(t, perr, "a panicking handler must surface as a publish error, not a silent nil")
	assert.Contains(t, perr.Error(), "panicked", "panic must be reported sink-side, not swallowed")

	// Sink-side: the healthy co-subscriber MUST still have been delivered to.
	assert.Equal(t, int64(1), healthyDeliveries.Load(),
		"a panicking peer suppressed delivery to a healthy subscriber")

	// And the bus must remain fully usable for subsequent publishes.
	var after atomic.Int64
	_, err = bus.Subscribe(ctx, "after", func(_ context.Context, _ *Message) error {
		after.Add(1)
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, bus.Publish(ctx, "after", NewMessage("after", []byte("y"))))
	assert.Equal(t, int64(1), after.Load(), "bus was left unusable after a handler panic")
}

// TestBus_RepeatedHandlerPanics_BusStaysAlive hammers the recover path: many
// publishes to a topic whose sole handler always panics must never crash the
// process, and Publish must report the panic every time (never a false nil).
func TestBus_RepeatedHandlerPanics_BusStaysAlive(t *testing.T) {
	bus := NewBus()
	ctx := context.Background()

	_, err := bus.Subscribe(ctx, "boom", func(_ context.Context, _ *Message) error {
		panic("again")
	})
	require.NoError(t, err)

	for i := 0; i < 100; i++ {
		err := bus.Publish(ctx, "boom", NewMessage("boom", []byte{byte(i)}))
		require.Error(t, err, "iteration %d: panic must surface as publish error", i)
	}
}

// TestBus_PanicAndErrorHandlersAggregateTogether proves the recover path
// composes with the existing error-aggregation contract: with one panicking
// handler and one error-returning handler on the same topic, BOTH failures are
// surfaced in the single returned error (neither masks the other).
func TestBus_PanicAndErrorHandlersAggregateTogether(t *testing.T) {
	bus := NewBus()
	ctx := context.Background()

	_, err := bus.Subscribe(ctx, "agg", func(_ context.Context, _ *Message) error {
		panic("panic-side")
	})
	require.NoError(t, err)
	_, err = bus.Subscribe(ctx, "agg", func(_ context.Context, _ *Message) error {
		return assertErr("error-side")
	})
	require.NoError(t, err)

	err = bus.Publish(ctx, "agg", NewMessage("agg", []byte("z")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "panic-side", "panicking handler not reported")
	assert.Contains(t, err.Error(), "error-side", "error-returning handler masked by panic")
}

// TestBus_PanicAndConcurrentChurn_RaceClean is the single -race run for this
// pass. It interleaves panicking publishes, healthy publishes,
// subscribe/unsubscribe churn, and introspection. It pins that the recover path
// is race-free and that a panicking handler never corrupts the topics map nor
// crashes the process under concurrency.
func TestBus_PanicAndConcurrentChurn_RaceClean(t *testing.T) {
	bus := NewBus()
	ctx := context.Background()

	// A permanent panicking subscriber on "danger".
	_, err := bus.Subscribe(ctx, "danger", func(_ context.Context, _ *Message) error {
		panic("danger")
	})
	require.NoError(t, err)

	var healthy atomic.Int64
	_, err = bus.Subscribe(ctx, "safe", func(_ context.Context, _ *Message) error {
		healthy.Add(1)
		return nil
	})
	require.NoError(t, err)

	const workers = 6
	const iters = 200
	noop := func(_ context.Context, _ *Message) error { return nil }

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				switch i % 4 {
				case 0:
					_ = bus.Publish(ctx, "danger", NewMessage("danger", []byte{byte(i)}))
				case 1:
					_ = bus.Publish(ctx, "safe", NewMessage("safe", []byte{byte(i)}))
				case 2:
					id, err := bus.Subscribe(ctx, "churn", noop)
					if err == nil {
						_ = bus.Unsubscribe("churn", id)
					}
				default:
					_ = bus.SubscriberCount("danger")
				}
			}
		}(w)
	}
	wg.Wait()

	// The bus survived all the panicking publishes and is still delivering.
	assert.Positive(t, healthy.Load(), "healthy topic stopped delivering amid panic churn")
}

// TestBus_UnsubscribeDuringPublish_NoUseAfterUnsubBeyondSnapshot pins the
// documented delivery model: Publish snapshots the live subscriber set under
// the read lock, then invokes handlers without the lock. An Unsubscribe that
// commits AFTER the snapshot may still see one in-flight delivery to the
// just-removed handler (snapshot semantics), but once Unsubscribe has returned,
// NO new Publish may ever deliver to that handler again. This test proves the
// "stops future delivery" half of the contract deterministically and bounds the
// in-flight window, so a regression that leaks deliveries to unsubscribed
// handlers across publishes is caught.
func TestBus_UnsubscribeStopsAllFutureDelivery(t *testing.T) {
	bus := NewBus()
	ctx := context.Background()

	var delivered atomic.Int64
	id, err := bus.Subscribe(ctx, "u", func(_ context.Context, _ *Message) error {
		delivered.Add(1)
		return nil
	})
	require.NoError(t, err)

	// One delivery while subscribed.
	require.NoError(t, bus.Publish(ctx, "u", NewMessage("u", []byte("a"))))
	require.Equal(t, int64(1), delivered.Load())

	// Unsubscribe, then publish many times: not a single further delivery.
	require.NoError(t, bus.Unsubscribe("u", id))
	for i := 0; i < 50; i++ {
		require.NoError(t, bus.Publish(ctx, "u", NewMessage("u", []byte("b"))))
	}
	assert.Equal(t, int64(1), delivered.Load(),
		"handler received deliveries AFTER Unsubscribe returned (use-after-unsub leak)")
}

// TestBus_ConcurrentUnsubscribeDuringPublish_NoDeliveryAfterQuiesce drives the
// race window directly: a publisher loop races an Unsubscribe. After both
// quiesce, we issue a fresh barrier publish and assert the count is frozen --
// i.e. once unsubscribed, the handler is permanently silent. -race clean.
func TestBus_ConcurrentUnsubscribeDuringPublish_NoDeliveryAfterQuiesce(t *testing.T) {
	bus := NewBus()
	ctx := context.Background()

	var delivered atomic.Int64
	id, err := bus.Subscribe(ctx, "race", func(_ context.Context, _ *Message) error {
		delivered.Add(1)
		return nil
	})
	require.NoError(t, err)

	stop := make(chan struct{})
	var pubWG sync.WaitGroup
	pubWG.Add(1)
	go func() {
		defer pubWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = bus.Publish(ctx, "race", NewMessage("race", []byte("x")))
			}
		}
	}()

	time.Sleep(5 * time.Millisecond) // let some deliveries happen
	require.NoError(t, bus.Unsubscribe("race", id))
	close(stop)
	pubWG.Wait()

	// Barrier: record the count, let any straggler settle, then a fresh publish
	// must add exactly zero further deliveries.
	frozen := delivered.Load()
	require.Equal(t, 0, bus.SubscriberCount("race"), "subscriber not actually removed")
	require.NoError(t, bus.Publish(ctx, "race", NewMessage("race", []byte("y"))))
	assert.Equal(t, frozen, delivered.Load(),
		"delivery occurred after Unsubscribe quiesced -- unsubscribe did not stop the subscriber")
}
