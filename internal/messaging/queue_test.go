package messaging

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestQueueFIFODelivery proves messages come back out in the exact order they
// went in and that the payloads survive the round-trip (sink-side check, not
// merely "Enqueue returned nil").
func TestQueueFIFODelivery(t *testing.T) {
	q := NewQueue(8)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		require.NoError(t, q.Enqueue(ctx, NewMessage("jobs", []byte{byte(i)})))
	}
	require.Equal(t, 5, q.Len())

	for i := 0; i < 5; i++ {
		msg, ok, err := q.Dequeue(ctx)
		require.NoError(t, err)
		require.True(t, ok)
		require.Len(t, msg.Payload, 1)
		assert.Equal(t, byte(i), msg.Payload[0], "FIFO order violated at index %d", i)
	}
}

// TestQueueExactlyOnceAmongConsumers is the core competing-consumer guarantee:
// with N messages and M concurrent consumers, every message is delivered to
// exactly one consumer — no loss, no duplication.
func TestQueueExactlyOnceAmongConsumers(t *testing.T) {
	q := NewQueue(0) // unbuffered: forces real hand-off rendezvous
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const total = 200
	var mu sync.Mutex
	seen := make(map[string]int)

	var consumers sync.WaitGroup
	for c := 0; c < 4; c++ {
		consumers.Add(1)
		go func() {
			defer consumers.Done()
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
	}

	produced := make([]string, 0, total)
	for i := 0; i < total; i++ {
		m := NewMessage("work", []byte{byte(i)})
		produced = append(produced, m.ID)
		require.NoError(t, q.Enqueue(ctx, m))
	}
	require.NoError(t, q.Close())
	consumers.Wait()

	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, seen, total, "every message must be delivered exactly once")
	for _, id := range produced {
		assert.Equal(t, 1, seen[id], "message %s delivered a wrong number of times", id)
	}
}

// TestQueueEnqueueBlocksWhenFullAndUnblocksOnDequeue proves Enqueue genuinely
// applies backpressure on a full buffer instead of silently dropping or
// returning early.
func TestQueueEnqueueBlocksWhenFullAndUnblocksOnDequeue(t *testing.T) {
	q := NewQueue(1)
	ctx := context.Background()

	require.NoError(t, q.Enqueue(ctx, NewMessage("t", []byte("a")))) // fills buffer

	done := make(chan struct{})
	go func() {
		_ = q.Enqueue(ctx, NewMessage("t", []byte("b"))) // must block
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("Enqueue returned while buffer was full; backpressure not applied")
	case <-time.After(50 * time.Millisecond):
	}

	// Draining one item must unblock the pending Enqueue.
	_, ok, err := q.Dequeue(ctx)
	require.NoError(t, err)
	require.True(t, ok)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Enqueue did not unblock after a Dequeue freed buffer space")
	}
}

// TestQueueEnqueueContextCancel proves a blocked Enqueue respects context
// cancellation rather than blocking forever.
func TestQueueEnqueueContextCancel(t *testing.T) {
	q := NewQueue(0)
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() { errCh <- q.Enqueue(ctx, NewMessage("t", []byte("x"))) }()

	cancel()
	select {
	case err := <-errCh:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("Enqueue ignored context cancellation")
	}
}

// TestQueueCloseDrainsBufferedItems proves Close does not discard already
// accepted messages: a consumer reading after Close still receives them, then
// observes ok=false exactly once the buffer is empty.
func TestQueueCloseDrainsBufferedItems(t *testing.T) {
	q := NewQueue(4)
	ctx := context.Background()

	require.NoError(t, q.Enqueue(ctx, NewMessage("t", []byte("1"))))
	require.NoError(t, q.Enqueue(ctx, NewMessage("t", []byte("2"))))
	require.NoError(t, q.Close())

	// Buffered messages survive Close.
	m1, ok, err := q.Dequeue(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, []byte("1"), m1.Payload)

	m2, ok, err := q.Dequeue(ctx)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, []byte("2"), m2.Payload)

	// Now drained + closed -> ok=false, no error.
	_, ok, err = q.Dequeue(ctx)
	require.NoError(t, err)
	assert.False(t, ok, "drained closed queue must report ok=false")
}

// TestQueueEnqueueAfterCloseFails proves a closed queue rejects new work
// instead of panicking on a send to a closed channel.
func TestQueueEnqueueAfterCloseFails(t *testing.T) {
	q := NewQueue(2)
	require.NoError(t, q.Close())

	err := q.Enqueue(context.Background(), NewMessage("t", []byte("x")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
}

// TestQueueCloseIdempotent proves a second Close does not panic (double channel
// close) and reports no error.
func TestQueueCloseIdempotent(t *testing.T) {
	q := NewQueue(1)
	require.NoError(t, q.Close())
	require.NoError(t, q.Close())
}

func TestQueueEnqueueNilMessage(t *testing.T) {
	q := NewQueue(1)
	err := q.Enqueue(context.Background(), nil)
	require.Error(t, err)
}

func TestQueueNegativeCapacity(t *testing.T) {
	q := NewQueue(-5) // must not panic; treated as unbuffered
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Unbuffered: an Enqueue with no consumer blocks, so cancel proves it.
	errCh := make(chan error, 1)
	go func() { errCh <- q.Enqueue(ctx, NewMessage("t", []byte("x"))) }()
	cancel()
	select {
	case err := <-errCh:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("negative capacity was not treated as unbuffered")
	}
}
