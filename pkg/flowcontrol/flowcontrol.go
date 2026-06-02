// Package flowcontrol implements Kubernetes API Priority and Fairness style
// fair queuing in pure Go, fully deterministic and free of any wall-clock,
// network, or host dependency.
//
// The model follows the chain FlowSchema -> PriorityLevel -> Queue:
//
//   - A FlowSchema classifies an incoming request (by a flow identifier such as
//     a tenant or user string) onto a PriorityLevel and produces a flow
//     distinguisher (the key used to fan a level's traffic into per-flow queues).
//   - Each PriorityLevel owns a concurrency limit (max in-flight) and a set of
//     per-flow queues. Requests from different flows in the same level are
//     dispatched fair (round-robin across non-empty flow queues) so one flow
//     cannot starve another.
//   - Admission decides admit-now / enqueue / reject for an arriving request;
//     Dispatch picks the next waiting request fair across flows, honoring the
//     level concurrency cap.
//
// The dispatch loop is driven explicitly via Tick (no internal clock) so tests
// can advance it step-by-step. Time, where it matters, is injected through the
// Clock interface; nothing in the logic calls time.Now.
package flowcontrol

import (
	"errors"
	"sort"
)

// Clock is an injected, deterministic time source. Logic under test never calls
// time.Now; it reads ticks from a Clock so behavior is fully reproducible.
type Clock interface {
	// Now returns the current logical tick. Monotonic, advanced explicitly.
	Now() int64
}

// ManualClock is an in-memory Clock advanced explicitly by tests.
type ManualClock struct{ t int64 }

// NewManualClock returns a ManualClock starting at t0.
func NewManualClock(t0 int64) *ManualClock { return &ManualClock{t: t0} }

// Now returns the current logical tick.
func (c *ManualClock) Now() int64 { return c.t }

// Advance moves the clock forward by d ticks and returns the new value.
func (c *ManualClock) Advance(d int64) int64 { c.t += d; return c.t }

// AdmissionDecision is the outcome of offering a request to the Controller.
type AdmissionDecision int

const (
	// DecisionAdmit means a concurrency seat was free and the request started
	// immediately (it is now in-flight).
	DecisionAdmit AdmissionDecision = iota
	// DecisionEnqueue means no seat was free but the request was queued and
	// will be dispatched later, fair, by Tick.
	DecisionEnqueue
	// DecisionReject means the request could not be admitted or queued (no
	// matching FlowSchema, or the target flow queue is full).
	DecisionReject
)

func (d AdmissionDecision) String() string {
	switch d {
	case DecisionAdmit:
		return "admit"
	case DecisionEnqueue:
		return "enqueue"
	case DecisionReject:
		return "reject"
	default:
		return "unknown"
	}
}

// Errors returned by Controller configuration methods.
var (
	ErrLevelExists     = errors.New("flowcontrol: priority level already registered")
	ErrUnknownLevel    = errors.New("flowcontrol: priority level not registered")
	ErrSchemaExists    = errors.New("flowcontrol: flow schema already registered")
	ErrBadConcurrency  = errors.New("flowcontrol: concurrency limit must be >= 1")
	ErrBadQueueLength  = errors.New("flowcontrol: queue length limit must be >= 1")
	ErrNilClassifier   = errors.New("flowcontrol: flow schema classifier must not be nil")
	ErrNoMatchingFlow  = errors.New("flowcontrol: no flow schema matched request")
	ErrUnknownRequest  = errors.New("flowcontrol: request id not in flight")
)

// Request is an incoming unit of work to be admitted/queued/dispatched.
type Request struct {
	// ID is a unique, caller-supplied identifier (used to release in-flight
	// seats via Finish). Must be unique among live requests.
	ID string
	// FlowID is the raw flow identifier (e.g. tenant or user). FlowSchemas
	// classify on this.
	FlowID string
}

// Classification is the result of running a Request through the FlowSchemas.
type Classification struct {
	// Level is the matched PriorityLevel name.
	Level string
	// Flow is the flow distinguisher: the key into the level's per-flow queues.
	Flow string
}

// FlowSchema classifies a Request to a PriorityLevel and a flow distinguisher.
// MatchOrder gives deterministic precedence: lower MatchOrder is evaluated
// first; the first matching schema wins (Kubernetes uses a similar precedence).
type FlowSchema struct {
	Name       string
	Level      string
	MatchOrder int
	// Match reports whether this schema applies to the request.
	Match func(r Request) bool
	// Distinguish derives the flow key for fair-queuing within the level. If
	// nil, the raw FlowID is used.
	Distinguish func(r Request) string
}

// priorityLevel holds the runtime state for one PriorityLevel.
type priorityLevel struct {
	name        string
	concurrency int // max in-flight
	maxQueueLen int // per-flow queue cap

	inFlight int // current in-flight count (never exceeds concurrency)

	// queues maps flow distinguisher -> ordered waiting requests for that flow.
	queues map[string][]queued
	// rrOrder is the stable ordering of flow keys for round-robin fairness.
	rrOrder []string
	// rrCursor is the index into rrOrder of the flow to try next.
	rrCursor int

	// seats maps in-flight request ID -> flow key, so Finish can free a seat
	// and we can prove which flow a release belongs to.
	seats map[string]string
}

type queued struct {
	req     Request
	flow    string
	arrival int64 // tick at which it was enqueued (FIFO within a flow)
	seq     uint64
}

// Controller is the flow-control front door: register levels and schemas,
// classify+offer requests, and drive a deterministic fair dispatch loop.
type Controller struct {
	clock   Clock
	levels  map[string]*priorityLevel
	schemas []*FlowSchema

	seq uint64 // monotonic sequence for stable tie-breaking

	// admitted counts successful starts (admit-now + later dispatch) per flow,
	// keyed "level/flow". Exposed read-only via AdmittedCount/AdmittedCounts.
	admitted map[string]int64

	// maxInFlightSeen records the peak in-flight per level, so tests can assert
	// the concurrency cap was honored across the whole run.
	maxInFlightSeen map[string]int
}

// NewController returns an empty Controller using the supplied Clock.
func NewController(clock Clock) *Controller {
	return &Controller{
		clock:           clock,
		levels:          make(map[string]*priorityLevel),
		admitted:        make(map[string]int64),
		maxInFlightSeen: make(map[string]int),
	}
}

// RegisterPriorityLevel adds a new PriorityLevel. concurrency is the max
// in-flight; maxQueueLen is the per-flow queue cap (requests beyond it for a
// given flow are rejected, never dropping another flow's backlog).
func (c *Controller) RegisterPriorityLevel(name string, concurrency, maxQueueLen int) error {
	if _, ok := c.levels[name]; ok {
		return ErrLevelExists
	}
	if concurrency < 1 {
		return ErrBadConcurrency
	}
	if maxQueueLen < 1 {
		return ErrBadQueueLength
	}
	c.levels[name] = &priorityLevel{
		name:        name,
		concurrency: concurrency,
		maxQueueLen: maxQueueLen,
		queues:      make(map[string][]queued),
		seats:       make(map[string]string),
	}
	c.maxInFlightSeen[name] = 0
	return nil
}

// RegisterFlowSchema adds a FlowSchema. The schema's Level must already be
// registered. Schemas are kept sorted by (MatchOrder, Name) for deterministic
// precedence.
func (c *Controller) RegisterFlowSchema(fs FlowSchema) error {
	if _, ok := c.levels[fs.Level]; !ok {
		return ErrUnknownLevel
	}
	if fs.Match == nil {
		return ErrNilClassifier
	}
	for _, existing := range c.schemas {
		if existing.Name == fs.Name {
			return ErrSchemaExists
		}
	}
	copied := fs
	c.schemas = append(c.schemas, &copied)
	sort.SliceStable(c.schemas, func(i, j int) bool {
		if c.schemas[i].MatchOrder != c.schemas[j].MatchOrder {
			return c.schemas[i].MatchOrder < c.schemas[j].MatchOrder
		}
		return c.schemas[i].Name < c.schemas[j].Name
	})
	return nil
}

// Classify runs r through the registered FlowSchemas in precedence order and
// returns the matched level and flow distinguisher.
func (c *Controller) Classify(r Request) (Classification, error) {
	for _, fs := range c.schemas {
		if fs.Match(r) {
			flow := r.FlowID
			if fs.Distinguish != nil {
				flow = fs.Distinguish(r)
			}
			return Classification{Level: fs.Level, Flow: flow}, nil
		}
	}
	return Classification{}, ErrNoMatchingFlow
}

// Offer classifies r and tries to admit it. If a concurrency seat is free it is
// admitted immediately (in-flight); otherwise it is enqueued onto its flow's
// queue, unless that queue is full (then rejected). A request that matches no
// FlowSchema is rejected.
func (c *Controller) Offer(r Request) (Classification, AdmissionDecision, error) {
	cls, err := c.Classify(r)
	if err != nil {
		return Classification{}, DecisionReject, err
	}
	lvl := c.levels[cls.Level]

	// Admit immediately only if a seat is free AND no one is already waiting in
	// this level (preserve fairness: don't let a late arrival jump the queue).
	if lvl.inFlight < lvl.concurrency && lvl.totalQueued() == 0 {
		c.start(lvl, r, cls.Flow)
		return cls, DecisionAdmit, nil
	}

	// Otherwise queue it on its flow, respecting the per-flow cap.
	if len(lvl.queues[cls.Flow]) >= lvl.maxQueueLen {
		return cls, DecisionReject, nil
	}
	lvl.enqueue(queued{
		req:     r,
		flow:    cls.Flow,
		arrival: c.clock.Now(),
		seq:     c.nextSeq(),
	})
	return cls, DecisionEnqueue, nil
}

// start marks a request in-flight, freeing logic shared by admit-now and
// dispatch. It updates per-flow admitted counters and peak-in-flight tracking.
func (c *Controller) start(lvl *priorityLevel, r Request, flow string) {
	lvl.inFlight++
	lvl.seats[r.ID] = flow
	if lvl.inFlight > c.maxInFlightSeen[lvl.name] {
		c.maxInFlightSeen[lvl.name] = lvl.inFlight
	}
	c.admitted[lvl.name+"/"+flow]++
}

// Finish releases the in-flight seat held by request id (found by scanning the
// levels). It does NOT auto-dispatch; call Tick to fill freed seats fair. This
// keeps the dispatch loop explicit and test-drivable.
func (c *Controller) Finish(id string) error {
	for _, lvl := range c.levels {
		if _, ok := lvl.seats[id]; ok {
			delete(lvl.seats, id)
			lvl.inFlight--
			return nil
		}
	}
	return ErrUnknownRequest
}

// Tick runs one deterministic dispatch pass over every level: while a level has
// a free seat and a non-empty queue, it dispatches the next request chosen
// fair (round-robin across non-empty flow queues, FIFO within a flow). It
// returns the classifications that were started this pass, in dispatch order.
//
// Levels are processed independently and in name order; a flooded low-priority
// level cannot consume a high-priority level's seats because each level has its
// own concurrency budget.
func (c *Controller) Tick() []Classification {
	var started []Classification
	for _, name := range c.sortedLevelNames() {
		lvl := c.levels[name]
		for lvl.inFlight < lvl.concurrency {
			q, ok := lvl.dispatchNext()
			if !ok {
				break
			}
			c.start(lvl, q.req, q.flow)
			started = append(started, Classification{Level: name, Flow: q.flow})
		}
	}
	return started
}

func (c *Controller) sortedLevelNames() []string {
	names := make([]string, 0, len(c.levels))
	for n := range c.levels {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (c *Controller) nextSeq() uint64 { c.seq++; return c.seq }

// AdmittedCount returns the number of requests started (admitted now or later
// dispatched) for a given level and flow.
func (c *Controller) AdmittedCount(level, flow string) int64 {
	return c.admitted[level+"/"+flow]
}

// AdmittedCounts returns a copy of all per-flow admitted counters, keyed
// "level/flow".
func (c *Controller) AdmittedCounts() map[string]int64 {
	out := make(map[string]int64, len(c.admitted))
	for k, v := range c.admitted {
		out[k] = v
	}
	return out
}

// InFlight returns the current in-flight count for a level (0 if unknown).
func (c *Controller) InFlight(level string) int {
	if lvl, ok := c.levels[level]; ok {
		return lvl.inFlight
	}
	return 0
}

// PeakInFlight returns the maximum in-flight ever observed for a level. Used to
// assert the concurrency cap was honored across an entire run.
func (c *Controller) PeakInFlight(level string) int { return c.maxInFlightSeen[level] }

// QueuedTotal returns the number of requests currently waiting (across all
// flows) in a level.
func (c *Controller) QueuedTotal(level string) int {
	if lvl, ok := c.levels[level]; ok {
		return lvl.totalQueued()
	}
	return 0
}

// --- priorityLevel queue mechanics ---

func (l *priorityLevel) totalQueued() int {
	n := 0
	for _, q := range l.queues {
		n += len(q)
	}
	return n
}

// enqueue appends q to its flow's FIFO queue, registering the flow in the
// round-robin order on first sight.
func (l *priorityLevel) enqueue(q queued) {
	if _, ok := l.queues[q.flow]; !ok {
		l.queues[q.flow] = nil
		l.rrOrder = append(l.rrOrder, q.flow)
		sort.Strings(l.rrOrder) // stable, deterministic flow ordering
	}
	l.queues[q.flow] = append(l.queues[q.flow], q)
}

// dispatchNext picks the next request fair: starting from rrCursor it scans the
// flow order for the first non-empty flow queue, pops its head (FIFO within the
// flow), and advances the cursor past that flow. This round-robin is what
// prevents a flooding flow from starving a quiet one. Returns ok=false when the
// level has no queued requests.
func (l *priorityLevel) dispatchNext() (queued, bool) {
	if len(l.rrOrder) == 0 {
		return queued{}, false
	}
	n := len(l.rrOrder)
	for i := 0; i < n; i++ {
		idx := (l.rrCursor + i) % n
		flow := l.rrOrder[idx]
		fq := l.queues[flow]
		if len(fq) == 0 {
			continue
		}
		head := fq[0]
		l.queues[flow] = fq[1:]
		// Advance cursor to the flow AFTER the one we just served, so the next
		// dispatch goes to a different flow first (true round-robin).
		l.rrCursor = (idx + 1) % n
		return head, true
	}
	return queued{}, false
}
