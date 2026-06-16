package messaging

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

// Handler is a function that processes a Message.
type Handler func(ctx context.Context, msg *Message) error

// subscription represents a single subscription to a topic.
type subscription struct {
	id      string
	handler Handler
}

var subCounter atomic.Uint64

// Bus is an in-memory pub/sub message bus.
type Bus struct {
	mu   sync.RWMutex
	topics map[string]map[string]*subscription
}

// NewBus creates a new Bus.
func NewBus() *Bus {
	return &Bus{
		topics: make(map[string]map[string]*subscription),
	}
}

// Subscribe registers a handler for the given topic and returns a subscription ID.
func (b *Bus) Subscribe(_ context.Context, topic string, handler Handler) (string, error) {
	if topic == "" {
		return "", fmt.Errorf("topic cannot be empty")
	}
	if handler == nil {
		return "", fmt.Errorf("handler cannot be nil")
	}

	sub := &subscription{
		id:      fmt.Sprintf("sub-%d", subCounter.Add(1)),
		handler: handler,
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.topics[topic] == nil {
		b.topics[topic] = make(map[string]*subscription)
	}
	b.topics[topic][sub.id] = sub
	return sub.id, nil
}

// Topics returns the names of all topics that currently have at least one
// subscriber. The result is a snapshot taken under the read lock and is safe
// to call concurrently with Publish/Subscribe/Unsubscribe.
func (b *Bus) Topics() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	topics := make([]string, 0, len(b.topics))
	for t := range b.topics {
		topics = append(topics, t)
	}
	return topics
}

// SubscriberCount returns the number of active subscriptions for the given
// topic. A topic with no subscribers (or that was never subscribed to) reports
// zero.
func (b *Bus) SubscriberCount(topic string) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.topics[topic])
}

// Unsubscribe removes a subscription from a topic.
func (b *Bus) Unsubscribe(topic, subID string) error {
	if topic == "" {
		return fmt.Errorf("topic cannot be empty")
	}
	if subID == "" {
		return fmt.Errorf("subID cannot be empty")
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	subs, ok := b.topics[topic]
	if !ok {
		return fmt.Errorf("topic %q not found", topic)
	}
	if _, ok := subs[subID]; !ok {
		return fmt.Errorf("subscription %q not found for topic %q", subID, topic)
	}
	delete(subs, subID)
	if len(subs) == 0 {
		delete(b.topics, topic)
	}
	return nil
}

// Publish sends a message to all subscribers of the given topic.
// Each handler is invoked in its own goroutine to avoid blocking.
// Errors from handlers are collected and returned as a combined error.
func (b *Bus) Publish(ctx context.Context, topic string, msg *Message) error {
	if topic == "" {
		return fmt.Errorf("topic cannot be empty")
	}
	if msg == nil {
		return fmt.Errorf("message cannot be nil")
	}

	b.mu.RLock()
	subs, ok := b.topics[topic]
	if !ok {
		b.mu.RUnlock()
		return nil
	}
	// Copy subscriptions to avoid holding the lock during handler execution.
	copied := make([]*subscription, 0, len(subs))
	for _, s := range subs {
		copied = append(copied, s)
	}
	b.mu.RUnlock()

	var wg sync.WaitGroup
	errCh := make(chan error, len(copied))

	for _, sub := range copied {
		wg.Add(1)
		go func(s *subscription) {
			defer wg.Done()
			// Recover a panicking handler so one misbehaving subscriber cannot
			// crash the process (and thereby kill delivery to every other
			// subscriber and topic). A panic is surfaced as that handler's
			// error, exactly like a returned error, so Publish stays sink-side
			// honest and the bus keeps running.
			defer func() {
				if r := recover(); r != nil {
					errCh <- fmt.Errorf("handler %s panicked: %v", s.id, r)
				}
			}()
			if err := s.handler(ctx, msg); err != nil {
				errCh <- fmt.Errorf("handler %s failed: %w", s.id, err)
			}
		}(sub)
	}

	wg.Wait()
	close(errCh)

	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return fmt.Errorf("publish errors: %v", errs)
	}
	return nil
}
