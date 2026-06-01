// Package pubsub – in-memory broker implementation.
// This file contains InMemoryBroker, the lightweight, race-safe implementation
// used in unit tests and as a reference. For real Kafka connectivity see
// kafka_broker.go.
package pubsub

import (
	"context"
	"sync"
)

// Compile-time assertion: InMemoryBroker must satisfy Broker.
var _ Broker = (*InMemoryBroker)(nil)

// InMemoryBroker is a simple in-memory pub/sub broker using buffered channels.
// It retains the original fan-out semantics: every subscriber on a topic
// receives every published message.  The channel buffer is fixed at 10 slots;
// overflow is silently dropped (non-blocking contract).
//
// group is accepted for API compatibility but ignored – in-memory delivery
// does not model consumer-group exclusivity.
type InMemoryBroker struct {
	mu       sync.RWMutex
	subjects map[string][]chan Message
}

// NewInMemoryBroker creates a ready-to-use InMemoryBroker.
func NewInMemoryBroker() *InMemoryBroker {
	return &InMemoryBroker{subjects: make(map[string][]chan Message)}
}

// Publish sends msg to all subscribers of topic. ctx is honoured: if it is
// already cancelled, Publish returns ctx.Err() without delivering.
func (b *InMemoryBroker) Publish(ctx context.Context, topic, key, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	msg := Message{Topic: topic, Key: []byte(key), Value: []byte(value)}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subjects[topic] {
		select {
		case ch <- msg:
		default:
			// buffer full – drop (non-blocking contract)
		}
	}
	return nil
}

// Subscribe returns a buffered channel that receives all messages published to
// topic while ctx is alive. The channel is closed when ctx is cancelled.
func (b *InMemoryBroker) Subscribe(ctx context.Context, topic, _ string) (<-chan Message, error) {
	ch := make(chan Message, 10)
	b.mu.Lock()
	b.subjects[topic] = append(b.subjects[topic], ch)
	b.mu.Unlock()

	// Background goroutine closes the channel on ctx cancellation so callers
	// can range over it safely.
	go func() {
		<-ctx.Done()
		// Remove the channel from the subject list to prevent future writes.
		b.mu.Lock()
		subs := b.subjects[topic]
		for i, s := range subs {
			if s == ch {
				b.subjects[topic] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		b.mu.Unlock()
		close(ch)
	}()

	return ch, nil
}

// Close is a no-op for the in-memory broker.
func (b *InMemoryBroker) Close() error { return nil }

// ---------------------------------------------------------------------------
// Legacy surface – kept for backward compatibility with pre-interface callers.
// These wrappers around an embedded InMemoryBroker preserve the original API
// (Subscribe returns a plain chan string; Publish takes just subject+msg).
// ---------------------------------------------------------------------------

// Broker is a backward-compatible wrapper.  New code should use InMemoryBroker
// directly via the Broker interface.
type legacyBroker struct {
	mu       sync.RWMutex
	subjects map[string][]chan string
}

// NewBroker creates a new backward-compatible Broker.
func NewBroker() *legacyBroker { //nolint:revive // unexported type intentional
	return &legacyBroker{subjects: make(map[string][]chan string)}
}

// Subscribe subscribes to a subject and returns a plain string channel.
func (b *legacyBroker) Subscribe(subject string) chan string {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan string, 10)
	b.subjects[subject] = append(b.subjects[subject], ch)
	return ch
}

// Publish publishes a message to a subject.
func (b *legacyBroker) Publish(subject, msg string) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subjects[subject] {
		select {
		case ch <- msg:
		default:
		}
	}
}
