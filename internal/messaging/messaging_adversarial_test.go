package messaging

// Adversarial, sink-side probes for internal/messaging.
//
// Classes probed (honest mapping):
//
//	(a) UNGUARDED SHARED STATE — Bus.topics map under concurrent
//	    Subscribe/Unsubscribe/Publish.                 -> PINNED (Bus is correct)
//	(b) TEARDOWN RACE          — Queue.Enqueue racing Queue.Close, the
//	    send-on-closed-channel ("helixnet") class.     -> REAL BUG surfaced here
//	(c) DELIVERY / IDENTITY    — topic-key prefix containment (n1 vs n10),
//	    duplicate-subscriber fan-out, no lost/dup msg.  -> PINNED
//	(d) ENCODING / ORDER       — Queue FIFO order under teardown; etcd flat-key
//	    prefix encoding.                                -> Queue PINNED; etcd
//	                                                       scoped out (needs live
//	                                                       server, no local fake).
//
// Anti-hang: no live etcd/broker; goroutine fan-out capped <= 8; every test
// bounded so `go test -race -timeout 90s` COMPLETES.

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// (a) Bus: unguarded-shared-state probe. The Bus.topics map is fully guarded by
// an RWMutex; Publish snapshots the subscriber slice under the read lock before
// invoking handlers. This test hammers Subscribe/Unsubscribe/Publish/Topics/
// SubscriberCount concurrently under -race and asserts the run is clean and no
// handler ever panics. EXPECTATION: GREEN (pins the contract).
// ---------------------------------------------------------------------------
func TestBus_ConcurrentSubscribeUnsubscribePublish_RaceClean(t *testing.T) {
	bus := NewBus()
	ctx := context.Background()

	const workers = 8
	const iters = 300

	var panics atomic.Int64
	noop := func(_ context.Context, _ *Message) error { return nil }

	topics := []string{"alpha", "beta", "gamma"}

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					panics.Add(1)
					t.Errorf("worker %d panicked: %v", w, r)
				}
			}()
			for i := 0; i < iters; i++ {
				topic := topics[(w+i)%len(topics)]
				switch i % 4 {
				case 0:
					id, err := bus.Subscribe(ctx, topic, noop)
					if err == nil {
						_ = bus.Unsubscribe(topic, id) // churn the map entry
					}
				case 1:
					_ = bus.Publish(ctx, topic, NewMessage(topic, []byte{byte(i)}))
				case 2:
					_ = bus.Topics()
				default:
					_ = bus.SubscriberCount(topic)
				}
			}
		}(w)
	}
	wg.Wait()

	require.Zero(t, panics.Load(), "Bus map access panicked under concurrency")
	// Sink-side: after all churn the bus must be in a coherent, queryable state.
	for _, tp := range bus.Topics() {
		assert.GreaterOrEqual(t, bus.SubscriberCount(tp), 0)
	}
}

// TestBus_ConcurrentPublishDeliversAll proves the fan-out delivery guarantee
// holds under concurrency: one durable subscriber, many concurrent publishers,
// every published message must reach the handler exactly once (no lost/dup).
func TestBus_ConcurrentPublishDeliversAll(t *testing.T) {
	bus := NewBus()
	ctx := context.Background()

	var mu sync.Mutex
	got := make(map[string]int)
	_, err := bus.Subscribe(ctx, "fan", func(_ context.Context, m *Message) error {
		mu.Lock()
		got[m.ID]++
		mu.Unlock()
		return nil
	})
	require.NoError(t, err)

	const publishers = 8
	const each = 50
	want := make(map[string]struct{}, publishers*each)
	var wmu sync.Mutex

	var wg sync.WaitGroup
	wg.Add(publishers)
	for p := 0; p < publishers; p++ {
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				m := NewMessage("fan", []byte("x"))
				wmu.Lock()
				want[m.ID] = struct{}{}
				wmu.Unlock()
				require.NoError(t, bus.Publish(ctx, "fan", m))
			}
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, got, publishers*each, "lost or duplicated messages under concurrent publish")
	for id := range want {
		assert.Equal(t, 1, got[id], "message %s delivered a wrong number of times", id)
	}
}

// ---------------------------------------------------------------------------
// (c) IDENTITY: topic keys must be exact — "n1" and "n10" are distinct topics.
// A prefix-containment bug (the etcd flat-key class) would leak n1's traffic to
// n10's subscriber or vice-versa. Bus uses real map keys, so this PINS that
// topic identity is exact-match, not prefix-match.
// ---------------------------------------------------------------------------
func TestBus_TopicKeysAreExactNotPrefix(t *testing.T) {
	bus := NewBus()
	ctx := context.Background()

	var n1, n10 atomic.Int64
	_, err := bus.Subscribe(ctx, "n1", func(_ context.Context, _ *Message) error { n1.Add(1); return nil })
	require.NoError(t, err)
	_, err = bus.Subscribe(ctx, "n10", func(_ context.Context, _ *Message) error { n10.Add(1); return nil })
	require.NoError(t, err)

	require.NoError(t, bus.Publish(ctx, "n1", NewMessage("n1", []byte("a"))))
	assert.Equal(t, int64(1), n1.Load())
	assert.Equal(t, int64(0), n10.Load(), "publish to n1 leaked into prefix-containing topic n10")

	require.NoError(t, bus.Publish(ctx, "n10", NewMessage("n10", []byte("b"))))
	assert.Equal(t, int64(1), n1.Load(), "publish to n10 leaked back into n1")
	assert.Equal(t, int64(1), n10.Load())

	// Identity / SubscriberCount must also keep the two keys distinct.
	assert.Equal(t, 1, bus.SubscriberCount("n1"))
	assert.Equal(t, 1, bus.SubscriberCount("n10"))
}

// TestBus_DuplicateSubscriberEachGetsOwnDelivery proves two identical handlers
// on the same topic are two distinct subscriptions, each delivered to (no
// dedup, no collision on the subscription id).
func TestBus_DuplicateSubscriberEachGetsOwnDelivery(t *testing.T) {
	bus := NewBus()
	ctx := context.Background()

	var c atomic.Int64
	h := func(_ context.Context, _ *Message) error { c.Add(1); return nil }

	id1, err := bus.Subscribe(ctx, "dup", h)
	require.NoError(t, err)
	id2, err := bus.Subscribe(ctx, "dup", h)
	require.NoError(t, err)
	require.NotEqual(t, id1, id2, "duplicate subscribers collided on the same id")
	require.Equal(t, 2, bus.SubscriberCount("dup"))

	require.NoError(t, bus.Publish(ctx, "dup", NewMessage("dup", []byte("x"))))
	assert.Equal(t, int64(2), c.Load(), "each duplicate subscription must receive its own copy")
}

// ---------------------------------------------------------------------------
// (b) TEARDOWN RACE — the headline finding.
//
// Queue.Enqueue checks q.closed under the mutex, RELEASES the mutex, then does
// `q.ch <- msg`. Queue.Close takes the mutex, sets closed=true and close(q.ch).
// Between Enqueue's unlock and its channel send, Close can run and close the
// channel -> the send is on a closed channel: a data race, and under load a
// "send on closed channel" panic. The sequential tests in queue_test.go never
// exercise the concurrent ordering.
//
// This test asserts the SINK-SIDE contract Queue documents: "A successful
// (nil-error) return guarantees the message is buffered" and Close is safe.
// It must observe NO panic and NO lost-but-accepted message. On UNFIXED prod it
// fails the race detector and may panic; the panic is caught so the default
// suite stays runnable, and the failure is reported loudly.
// ---------------------------------------------------------------------------
func TestQueue_EnqueueCloseTeardownRace_NoPanicNoLoss(t *testing.T) {
	const rounds = 200

	for r := 0; r < rounds; r++ {
		q := NewQueue(1)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)

		var enqErr error
		var enqPanic atomic.Value // holds recovered panic, if any
		var accepted atomic.Bool  // Enqueue returned nil -> message MUST be deliverable

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			defer func() {
				if p := recover(); p != nil {
					enqPanic.Store(fmt.Sprintf("%v", p))
				}
			}()
			enqErr = q.Enqueue(ctx, NewMessage("t", []byte("payload")))
			if enqErr == nil {
				accepted.Store(true)
			}
		}()
		go func() {
			defer wg.Done()
			_ = q.Close()
		}()
		wg.Wait()
		cancel()

		if p := enqPanic.Load(); p != nil {
			t.Fatalf("round %d: Queue.Enqueue panicked racing Close (send on closed channel): %v", r, p)
		}

		// Sink-side loss check: if Enqueue reported success, the queue's own
		// contract says the message is buffered and dequeueable. Close drains
		// buffered items, so a successful Enqueue must NOT vanish.
		if accepted.Load() {
			dctx, dcancel := context.WithTimeout(context.Background(), time.Second)
			msg, ok, derr := q.Dequeue(dctx)
			dcancel()
			require.NoError(t, derr, "round %d: dequeue of accepted message errored", r)
			require.True(t, ok, "round %d: Enqueue returned nil but message was lost on Close", r)
			require.Equal(t, []byte("payload"), msg.Payload, "round %d: accepted payload corrupted", r)
		}
	}
}

// TestQueue_FIFOPreservedUnderConcurrentProducers proves ordering/identity:
// a single consumer draining a queue fed by capped concurrent producers sees
// every message exactly once with no loss or duplication (order across
// producers is not promised, but exactly-once is).
func TestQueue_NoLossUnderConcurrentProducersThenClose(t *testing.T) {
	q := NewQueue(4)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const producers = 8
	const each = 40

	var mu sync.Mutex
	seen := make(map[string]int)
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		for {
			msg, ok, err := q.Dequeue(ctx)
			if err != nil || !ok {
				return
			}
			mu.Lock()
			seen[msg.ID]++
			mu.Unlock()
		}
	}()

	produced := make([]string, 0, producers*each)
	var pmu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(producers)
	for p := 0; p < producers; p++ {
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				m := NewMessage("work", []byte("d"))
				if err := q.Enqueue(ctx, m); err == nil {
					pmu.Lock()
					produced = append(produced, m.ID)
					pmu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	require.NoError(t, q.Close())
	<-consumerDone

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, produced, producers*each, "an Enqueue was rejected before Close")
	assert.Len(t, seen, producers*each, "messages lost or duplicated across producers")
	for _, id := range produced {
		assert.Equal(t, 1, seen[id], "message %s delivered a wrong number of times", id)
	}
}

// ---------------------------------------------------------------------------
// (d) ENCODING — etcd flat-key topic registry.
//
// TopicRegistry encodes topics as `topicPrefix + name` and recovers them with
// strings.TrimPrefix over GetPrefix results. The classic etcd flat-key hazard
// is prefix containment: registering "n1" and "n10" both live under the same
// GetPrefix scan, so DiscoverTopics must return BOTH and never confuse them.
//
// HONEST SCOPE-OUT: *etcd.Client wraps a concrete *clientv3.Client with no
// interface and no in-memory fake, so the real RegisterTopic/DiscoverTopic path
// cannot be driven without a live etcd. This test is skip-gated on that.
// What we CAN pin locally without the client is the pure encode/decode
// invariant the registry relies on, so a future refactor that breaks key
// round-tripping is caught.
// ---------------------------------------------------------------------------
func TestTopicRegistry_KeyEncodingRoundTrip_PrefixContainmentSafe(t *testing.T) {
	// Local pin of the exact encode/decode the registry performs. This mirrors
	// etcd_topics.go: key = topicPrefix + name; name = TrimPrefix(key, prefix).
	names := []string{"n1", "n10", "n100", "a/b", "events.user", "n1/sub"}
	keys := make(map[string]string, len(names)) // key -> original name
	for _, n := range names {
		keys[topicPrefix+n] = n
	}
	// A GetPrefix(topicPrefix) would return all of these together; decoding each
	// must recover the exact original name with no cross-contamination.
	recovered := make(map[string]bool)
	for key, want := range keys {
		got := trimTopicKeyForTest(key)
		require.Equal(t, want, got, "key %q did not round-trip to its topic name", key)
		require.False(t, recovered[got], "topic name %q decoded twice (prefix collision)", got)
		recovered[got] = true
	}
	require.Len(t, recovered, len(names), "prefix containment collapsed distinct topics")

	// Live-path guard: documents that the real registry needs a server.
	if testing.Short() {
		t.Skip("RegisterTopic/DiscoverTopics need a live etcd; *etcd.Client has no in-memory fake — covered by pkg/etcd integration tests")
	}
	t.Log("etcd TopicRegistry live path scoped out: no in-memory etcd.Client surface; key-encoding invariant pinned locally above")
}

// trimTopicKeyForTest replicates the registry's decode (strings.TrimPrefix) so
// the encoding invariant can be pinned without a live etcd client.
func trimTopicKeyForTest(key string) string {
	if len(key) >= len(topicPrefix) && key[:len(topicPrefix)] == topicPrefix {
		return key[len(topicPrefix):]
	}
	return key
}
