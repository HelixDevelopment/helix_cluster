// Package dst provides a Deterministic Simulation Testing engine for Helix Cluster OS.
//
// The engine runs a virtual cluster with a seeded PRNG, virtual clock, and
// priority event queue so that every execution is fully reproducible.
package dst

import (
	"container/heap"
	"math/rand"
	"sort"
	"sync"
	"time"
)

// Event represents a scheduled simulation event.
type Event struct {
	ID        int64
	Timestamp int64 // virtual nanoseconds
	Kind      string
	Payload   interface{}
	index     int // for heap.Interface
}

// EventQueue implements a min-heap ordered by virtual timestamp.
type EventQueue []*Event

func (eq EventQueue) Len() int           { return len(eq) }
func (eq EventQueue) Less(i, j int) bool { return eq[i].Timestamp < eq[j].Timestamp }
func (eq EventQueue) Swap(i, j int) {
	eq[i], eq[j] = eq[j], eq[i]
	eq[i].index = i
	eq[j].index = j
}

func (eq *EventQueue) Push(x interface{}) {
	n := len(*eq)
	item := x.(*Event)
	item.index = n
	*eq = append(*eq, item)
}

func (eq *EventQueue) Pop() interface{} {
	old := *eq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil
	item.index = -1
	*eq = old[:n-1]
	return item
}

// Node represents a simulated cluster node.
type Node struct {
	ID      string
	Address string
	Online  bool
}

// Message represents a network message between nodes.
type Message struct {
	From    string
	To      string
	Payload []byte
}

// Engine is the deterministic simulation testing engine.
type Engine struct {
	mu       sync.RWMutex
	rng      *rand.Rand
	clock    int64
	nodes    map[string]*Node
	events   EventQueue
	eventID  int64
	seed     int64
	handlers map[string]func(e *Event)
}

// NewEngine creates a new DST engine with the given seed.
func NewEngine(seed int64) *Engine {
	return &Engine{
		rng:      rand.New(rand.NewSource(seed)),
		seed:     seed,
		nodes:    make(map[string]*Node),
		handlers: make(map[string]func(e *Event)),
	}
}

// Seed returns the engine's PRNG seed.
func (eng *Engine) Seed() int64 {
	return eng.seed
}

// Now returns the current virtual time as a time.Time.
func (eng *Engine) Now() time.Time {
	eng.mu.RLock()
	defer eng.mu.RUnlock()
	return time.Unix(0, eng.clock)
}

// RegisterNode adds a node to the simulation.
func (eng *Engine) RegisterNode(id, address string) {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	eng.nodes[id] = &Node{ID: id, Address: address, Online: true}
}

// GetNode returns a registered node.
func (eng *Engine) GetNode(id string) (*Node, bool) {
	eng.mu.RLock()
	defer eng.mu.RUnlock()
	n, ok := eng.nodes[id]
	return n, ok
}

// ListNodes returns all registered nodes sorted by ID.
func (eng *Engine) ListNodes() []*Node {
	eng.mu.RLock()
	defer eng.mu.RUnlock()
	out := make([]*Node, 0, len(eng.nodes))
	for _, n := range eng.nodes {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// SendMessage schedules a message delivery event.
func (eng *Engine) SendMessage(msg *Message, delay int64) int64 {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	eng.eventID++
	e := &Event{
		ID:        eng.eventID,
		Timestamp: eng.clock + delay,
		Kind:      "message",
		Payload:   msg,
	}
	heap.Push(&eng.events, e)
	return e.ID
}

// DeliverMessage immediately delivers a message by invoking the message handler.
func (eng *Engine) DeliverMessage(msg *Message) {
	eng.mu.Lock()
	h := eng.handlers["message"]
	eng.mu.Unlock()
	if h != nil {
		h(&Event{Kind: "message", Payload: msg})
	}
}

// ScheduleEvent schedules a generic event after a virtual delay.
func (eng *Engine) ScheduleEvent(kind string, payload interface{}, delay int64) int64 {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	eng.eventID++
	e := &Event{
		ID:        eng.eventID,
		Timestamp: eng.clock + delay,
		Kind:      kind,
		Payload:   payload,
	}
	heap.Push(&eng.events, e)
	return e.ID
}

// RegisterHandler registers a callback for a specific event kind.
func (eng *Engine) RegisterHandler(kind string, fn func(e *Event)) {
	eng.mu.Lock()
	defer eng.mu.Unlock()
	eng.handlers[kind] = fn
}

// Run executes events deterministically until the queue is empty or maxEvents is reached.
func (eng *Engine) Run(maxEvents int) int {
	eng.mu.Lock()
	defer eng.mu.Unlock()

	processed := 0
	for processed < maxEvents && eng.events.Len() > 0 {
		e := heap.Pop(&eng.events).(*Event)
		eng.clock = e.Timestamp
		if h, ok := eng.handlers[e.Kind]; ok && h != nil {
			eng.mu.Unlock()
			h(e)
			eng.mu.Lock()
		}
		processed++
	}
	return processed
}

// RNG returns the engine's random source (useful for injecting randomness into simulations).
func (eng *Engine) RNG() *rand.Rand {
	return eng.rng
}

// NodeCount returns the number of registered nodes.
func (eng *Engine) NodeCount() int {
	eng.mu.RLock()
	defer eng.mu.RUnlock()
	return len(eng.nodes)
}
