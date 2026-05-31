package messaging

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBusPublishIsSynchronousAndDelivers proves Publish does not return until
// every handler has actually run (no time.Sleep needed). This is the sink-side
// guarantee: when Publish returns nil, the payload has been delivered.
func TestBusPublishIsSynchronousAndDelivers(t *testing.T) {
	bus := NewBus()
	ctx := context.Background()

	var mu sync.Mutex
	var got []byte
	_, err := bus.Subscribe(ctx, "t", func(_ context.Context, m *Message) error {
		mu.Lock()
		got = append([]byte(nil), m.Payload...)
		mu.Unlock()
		return nil
	})
	require.NoError(t, err)

	require.NoError(t, bus.Publish(ctx, "t", NewMessage("t", []byte("hello-sink"))))

	// No sleep: if Publish were async this read would race/observe nil.
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []byte("hello-sink"), got)
}

// TestBusPublishDeliversToEveryFanoutSubscriber proves the fan-out actually
// reaches all N subscribers with the same payload — not just that one ran.
func TestBusPublishDeliversToEveryFanoutSubscriber(t *testing.T) {
	bus := NewBus()
	ctx := context.Background()

	const n = 5
	var mu sync.Mutex
	deliveries := make([]string, 0, n)
	for i := 0; i < n; i++ {
		_, err := bus.Subscribe(ctx, "fan", func(_ context.Context, m *Message) error {
			mu.Lock()
			deliveries = append(deliveries, string(m.Payload))
			mu.Unlock()
			return nil
		})
		require.NoError(t, err)
	}

	require.NoError(t, bus.Publish(ctx, "fan", NewMessage("fan", []byte("X"))))

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, deliveries, n)
	for _, d := range deliveries {
		assert.Equal(t, "X", d)
	}
}

// TestBusPublishAggregatesMultipleHandlerErrors proves a multi-subscriber
// publish surfaces failures from more than one handler rather than masking all
// but the first.
func TestBusPublishAggregatesMultipleHandlerErrors(t *testing.T) {
	bus := NewBus()
	ctx := context.Background()

	_, err := bus.Subscribe(ctx, "e", func(_ context.Context, _ *Message) error {
		return assertErr("boom-1")
	})
	require.NoError(t, err)
	_, err = bus.Subscribe(ctx, "e", func(_ context.Context, _ *Message) error {
		return assertErr("boom-2")
	})
	require.NoError(t, err)

	err = bus.Publish(ctx, "e", NewMessage("e", []byte("d")))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom-1")
	assert.Contains(t, err.Error(), "boom-2")
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

// TestBusResubscribeAfterTopicGarbageCollected proves that once the last
// subscriber leaves (and the internal topic map entry is deleted) a fresh
// subscribe re-creates the topic and delivery still works.
func TestBusResubscribeAfterTopicGarbageCollected(t *testing.T) {
	bus := NewBus()
	ctx := context.Background()

	sub, err := bus.Subscribe(ctx, "gc", noopHandler)
	require.NoError(t, err)
	require.NoError(t, bus.Unsubscribe("gc", sub))
	assert.Equal(t, 0, bus.SubscriberCount("gc"))

	var delivered bool
	var mu sync.Mutex
	_, err = bus.Subscribe(ctx, "gc", func(_ context.Context, _ *Message) error {
		mu.Lock()
		delivered = true
		mu.Unlock()
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, bus.Publish(ctx, "gc", NewMessage("gc", []byte("again"))))

	mu.Lock()
	defer mu.Unlock()
	assert.True(t, delivered, "delivery broken after topic re-creation")
}
