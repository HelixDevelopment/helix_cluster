package messaging

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func noopHandler(_ context.Context, _ *Message) error { return nil }

// TestBusTopicsReflectsActiveSubscriptions proves Topics() reports exactly the
// set of topics that have live subscribers, and that the set shrinks when the
// last subscriber of a topic unsubscribes.
func TestBusTopicsReflectsActiveSubscriptions(t *testing.T) {
	bus := NewBus()
	ctx := context.Background()

	assert.Empty(t, bus.Topics(), "fresh bus must report no topics")

	_, err := bus.Subscribe(ctx, "alpha", noopHandler)
	require.NoError(t, err)
	subBeta, err := bus.Subscribe(ctx, "beta", noopHandler)
	require.NoError(t, err)

	got := bus.Topics()
	sort.Strings(got)
	assert.Equal(t, []string{"alpha", "beta"}, got)

	// Removing the only subscriber of "beta" must drop it from Topics().
	require.NoError(t, bus.Unsubscribe("beta", subBeta))
	assert.Equal(t, []string{"alpha"}, bus.Topics())
}

// TestBusSubscriberCount proves SubscriberCount tracks the real number of live
// handlers per topic across subscribe and unsubscribe, and reports zero for an
// unknown topic.
func TestBusSubscriberCount(t *testing.T) {
	bus := NewBus()
	ctx := context.Background()

	assert.Equal(t, 0, bus.SubscriberCount("never.seen"))

	s1, err := bus.Subscribe(ctx, "room", noopHandler)
	require.NoError(t, err)
	_, err = bus.Subscribe(ctx, "room", noopHandler)
	require.NoError(t, err)
	assert.Equal(t, 2, bus.SubscriberCount("room"))

	require.NoError(t, bus.Unsubscribe("room", s1))
	assert.Equal(t, 1, bus.SubscriberCount("room"))
}
