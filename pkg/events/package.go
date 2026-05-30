// Package events provides event handling for Helix Cluster OS.
package events

import (
	"sync"
	"time"
)

// Event is a domain event.
type Event struct {
	ID        string
	Type      string
	Payload   []byte
	Timestamp time.Time
}

// Bus is an in-memory event bus.
type Bus struct {
	mu       sync.RWMutex
	handlers map[string][]func(Event)
}

// NewBus creates a new Bus.
func NewBus() *Bus {
	return &Bus{handlers: make(map[string][]func(Event))}
}

// Subscribe registers a handler for an event type.
func (b *Bus) Subscribe(eventType string, handler func(Event)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[eventType] = append(b.handlers[eventType], handler)
}

// Publish publishes an event.
func (b *Bus) Publish(e Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, h := range b.handlers[e.Type] {
		go h(e)
	}
}
