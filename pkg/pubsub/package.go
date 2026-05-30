// Package pubsub provides publish/subscribe utilities for Helix Cluster OS.
package pubsub

import "sync"

// Broker is a simple in-memory pub/sub broker.
type Broker struct {
	mu       sync.RWMutex
	subjects map[string][]chan string
}

// NewBroker creates a new Broker.
func NewBroker() *Broker {
	return &Broker{subjects: make(map[string][]chan string)}
}

// Subscribe subscribes to a subject.
func (b *Broker) Subscribe(subject string) chan string {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan string, 10)
	b.subjects[subject] = append(b.subjects[subject], ch)
	return ch
}

// Publish publishes a message to a subject.
func (b *Broker) Publish(subject, msg string) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subjects[subject] {
		select {
		case ch <- msg:
		default:
		}
	}
}
