package swim

import (
	"math/rand"
	"sync"
	"time"
)

// GossipBuffer holds recent membership changes for dissemination.
type GossipBuffer struct {
	mu      sync.RWMutex
	events  []GossipEvent
	maxSize int
}

// GossipEvent represents a single membership change event.
type GossipEvent struct {
	MemberID    string
	State       MemberState
	Incarnation uint32
	Timestamp   time.Time
}

// NewGossipBuffer creates a gossip buffer.
func NewGossipBuffer(maxSize int) *GossipBuffer {
	if maxSize <= 0 {
		maxSize = 100
	}
	return &GossipBuffer{
		events:  make([]GossipEvent, 0, maxSize),
		maxSize: maxSize,
	}
}

// Add adds an event to the buffer.
func (gb *GossipBuffer) Add(event GossipEvent) {
	gb.mu.Lock()
	defer gb.mu.Unlock()

	if len(gb.events) >= gb.maxSize {
		gb.events = gb.events[1:]
	}
	gb.events = append(gb.events, event)
}

// Recent returns the N most recent events.
func (gb *GossipBuffer) Recent(n int) []GossipEvent {
	gb.mu.RLock()
	defer gb.mu.RUnlock()

	if n <= 0 {
		return nil
	}
	if n > len(gb.events) {
		n = len(gb.events)
	}
	result := make([]GossipEvent, n)
	copy(result, gb.events[len(gb.events)-n:])
	return result
}

// RandomMembers selects k random members for gossip target.
func RandomMembers(members []*Member, k int, excludeID string) []*Member {
	if k <= 0 || len(members) == 0 {
		return nil
	}

	filtered := make([]*Member, 0, len(members))
	for _, m := range members {
		if m.ID != excludeID && m.State == StateAlive {
			filtered = append(filtered, m)
		}
	}

	if len(filtered) == 0 {
		return nil
	}

	rand.Shuffle(len(filtered), func(i, j int) {
		filtered[i], filtered[j] = filtered[j], filtered[i]
	})

	if k > len(filtered) {
		k = len(filtered)
	}
	return filtered[:k]
}
