package messaging

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewMessage(t *testing.T) {
	msg := NewMessage("test.topic", []byte("hello"))
	require.NotNil(t, msg)
	assert.NotEmpty(t, msg.ID)
	assert.Equal(t, "test.topic", msg.Topic)
	assert.Equal(t, []byte("hello"), msg.Payload)
	assert.False(t, msg.Timestamp.IsZero())
	assert.NotNil(t, msg.Headers)
}

func TestMessageEncodeDecode(t *testing.T) {
	original := NewMessage("events.user", []byte(`{"name":"alice"}`))
	original.Headers["content-type"] = "application/json"

	data, err := original.Encode()
	require.NoError(t, err)

	decoded, err := DecodeMessage(data)
	require.NoError(t, err)
	assert.Equal(t, original.ID, decoded.ID)
	assert.Equal(t, original.Topic, decoded.Topic)
	assert.Equal(t, original.Payload, decoded.Payload)
	assert.Equal(t, original.Headers, decoded.Headers)
}

func TestBusPublishSubscribe(t *testing.T) {
	bus := NewBus()
	ctx := context.Background()

	var received *Message
	var mu sync.Mutex

	handler := func(_ context.Context, msg *Message) error {
		mu.Lock()
		defer mu.Unlock()
		received = msg
		return nil
	}

	subID, err := bus.Subscribe(ctx, "test.topic", handler)
	require.NoError(t, err)
	assert.NotEmpty(t, subID)

	msg := NewMessage("test.topic", []byte("payload"))
	err = bus.Publish(ctx, "test.topic", msg)
	require.NoError(t, err)

	// Allow goroutine handlers to run.
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	require.NotNil(t, received)
	assert.Equal(t, msg.ID, received.ID)
	mu.Unlock()
}

func TestBusMultipleSubscribers(t *testing.T) {
	bus := NewBus()
	ctx := context.Background()

	var count atomic.Int32
	handler := func(_ context.Context, _ *Message) error {
		count.Add(1)
		return nil
	}

	_, err := bus.Subscribe(ctx, "multi.topic", handler)
	require.NoError(t, err)
	_, err = bus.Subscribe(ctx, "multi.topic", handler)
	require.NoError(t, err)

	msg := NewMessage("multi.topic", []byte("data"))
	err = bus.Publish(ctx, "multi.topic", msg)
	require.NoError(t, err)

	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(2), count.Load())
}

func TestBusUnsubscribe(t *testing.T) {
	bus := NewBus()
	ctx := context.Background()

	var called atomic.Bool
	handler := func(_ context.Context, _ *Message) error {
		called.Store(true)
		return nil
	}

	subID, err := bus.Subscribe(ctx, "unsub.topic", handler)
	require.NoError(t, err)

	err = bus.Unsubscribe("unsub.topic", subID)
	require.NoError(t, err)

	msg := NewMessage("unsub.topic", []byte("data"))
	err = bus.Publish(ctx, "unsub.topic", msg)
	require.NoError(t, err)

	time.Sleep(50 * time.Millisecond)
	assert.False(t, called.Load())
}

func TestBusUnsubscribeNonExistent(t *testing.T) {
	bus := NewBus()
	err := bus.Unsubscribe("missing", "sub1")
	assert.Error(t, err)
}

func TestBusPublishNoSubscribers(t *testing.T) {
	bus := NewBus()
	ctx := context.Background()

	msg := NewMessage("nobody.listening", []byte("data"))
	err := bus.Publish(ctx, "nobody.listening", msg)
	assert.NoError(t, err)
}

func TestBusPublishHandlerError(t *testing.T) {
	bus := NewBus()
	ctx := context.Background()

	handler := func(_ context.Context, _ *Message) error {
		return errors.New("handler failed")
	}

	_, err := bus.Subscribe(ctx, "error.topic", handler)
	require.NoError(t, err)

	msg := NewMessage("error.topic", []byte("data"))
	err = bus.Publish(ctx, "error.topic", msg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "handler failed")
}

func TestBusSubscribeValidation(t *testing.T) {
	bus := NewBus()
	ctx := context.Background()

	_, err := bus.Subscribe(ctx, "", func(_ context.Context, _ *Message) error { return nil })
	assert.Error(t, err)

	_, err = bus.Subscribe(ctx, "topic", nil)
	assert.Error(t, err)
}

func TestBusConcurrentPublish(t *testing.T) {
	bus := NewBus()
	ctx := context.Background()

	var count atomic.Int32
	handler := func(_ context.Context, _ *Message) error {
		count.Add(1)
		return nil
	}

	_, err := bus.Subscribe(ctx, "concurrent.topic", handler)
	require.NoError(t, err)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			msg := NewMessage("concurrent.topic", []byte{byte(i)})
			_ = bus.Publish(ctx, "concurrent.topic", msg)
		}(i)
	}
	wg.Wait()

	// Allow handlers to finish.
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(100), count.Load())
}

func TestBusPublishNilMessage(t *testing.T) {
	bus := NewBus()
	ctx := context.Background()
	err := bus.Publish(ctx, "topic", nil)
	assert.Error(t, err)
}

func TestBusPublishEmptyTopic(t *testing.T) {
	bus := NewBus()
	ctx := context.Background()
	err := bus.Publish(ctx, "", NewMessage("", []byte("x")))
	assert.Error(t, err)
}
