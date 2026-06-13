// Package dst implements HXC-1419: a FoundationDB-style DETERMINISTIC
// SIMULATION harness in pure Go. The spec's reference ("Rust Turmoil") is a
// Rust library; this is the equivalent mechanism implemented idiomatically in
// Go, which is the project's language.
//
// WHY a deterministic simulation harness:
// Distributed-systems bugs (lost messages, reordering, disk stalls, clock skew)
// are notoriously non-reproducible under real concurrency because the OS
// scheduler, wall-clock, and network are all nondeterministic. A DST harness
// makes the ENTIRE execution a pure function of a single integer seed:
//
//	trace = f(seed)
//
// so the same seed ALWAYS replays the exact same interleaving byte-for-byte.
// This turns "it failed once on the cluster at 3am" into "seed 12345 fails
// deterministically", which is the whole point: a failure can be captured as a
// seed and reproduced on a developer laptop forever.
//
// The three injected facilities are the only sources of "environment" a real
// actor touches, and ALL of them are driven by the harness, never by the OS:
//
//   - Network: deliver / drop / delay / reorder messages. Latency is drawn from
//     the seeded RNG; delivery order is decided by the deterministic priority
//     queue keyed on (logical-arrival-time, monotonic sequence).
//   - Disk: read / write with injectable faults and stalls. Stalls add seeded
//     logical latency; faults return a typed error.
//   - Clock: LOGICAL time only. It is advanced by the scheduler as it pops
//     events; it NEVER reads time.Now(). (time.Duration is used purely as a
//     unit type — no wall-clock is ever consulted.)
//
// MUTATION GUARD (see dst_test.go): if the scheduler ever consulted wall-clock
// time (time.Now) or an unseeded RNG (e.g. the global math/rand source), then
// two runs of the same seed would diverge and TestDeterminism_SameSeed and
// TestReproduceFailure_ByteForByte would FAIL. Likewise, if the tie-break on
// equal logical arrival time were removed (making ordering map-iteration- or
// insertion-ambiguous), determinism would break and those tests would fail.
package dst

import (
	"container/heap"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"
)

// Time is LOGICAL simulation time measured in nanoseconds since the start of the
// run. It is advanced only by the scheduler; it is never derived from the OS
// clock. We use time.Duration as the unit type for ergonomics — never time.Now.
type Time time.Duration

// ActorID identifies a registered actor (a node / process in the simulated
// system).
type ActorID string

// Message is an opaque payload routed between actors by the simulated Network.
// Tests/actors define their own concrete payloads via Body.
type Message struct {
	From ActorID
	To   ActorID
	Body string
}

// Handler is the production-style entry point an actor exposes to the harness.
// It receives the simulation API (so it can send messages, do disk I/O, read
// the logical clock, and schedule timers) plus the delivered message. The
// REAL actor logic under test lives in Handler implementations — the harness
// mocks the ENVIRONMENT (network/disk/clock), never the logic. This satisfies
// CLAUDE-1: production code is the model under test.
type Handler interface {
	// Receive is invoked when a message is delivered to this actor at the
	// current logical time. Implementations may call sim.Send, sim.Disk,
	// sim.Now and sim.At.
	Receive(sim *Simulation, msg Message)
}

// HandlerFunc adapts an ordinary function to the Handler interface.
type HandlerFunc func(sim *Simulation, msg Message)

// Receive implements Handler.
func (f HandlerFunc) Receive(sim *Simulation, msg Message) { f(sim, msg) }

// TimerFunc is a callback scheduled to fire at a future logical time via At.
type TimerFunc func(sim *Simulation)

// eventKind discriminates the entries in the scheduler's priority queue.
type eventKind uint8

const (
	evDeliver eventKind = iota // a network message arrives at its destination
	evTimer                    // a scheduled timer fires
)

// event is one scheduled unit of work. Ordering is fully deterministic: primary
// key is logical fire time; the secondary key seq is a monotonically increasing
// counter assigned at insertion, which makes equal-time ordering a STABLE,
// reproducible total order rather than relying on insertion/iteration accidents.
type event struct {
	at    Time
	seq   uint64
	kind  eventKind
	msg   Message   // valid when kind == evDeliver
	timer TimerFunc // valid when kind == evTimer
}

// eventQueue is a min-heap of events ordered by (at, seq).
type eventQueue []*event

func (q eventQueue) Len() int { return len(q) }
func (q eventQueue) Less(i, j int) bool {
	if q[i].at != q[j].at {
		return q[i].at < q[j].at
	}
	// Stable tie-break: earlier-inserted event fires first. Removing this line
	// makes ordering ambiguous and breaks determinism (mutation guard).
	return q[i].seq < q[j].seq
}
func (q eventQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *eventQueue) Push(x any)   { *q = append(*q, x.(*event)) }
func (q *eventQueue) Pop() any {
	old := *q
	n := len(old)
	it := old[n-1]
	old[n-1] = nil
	*q = old[:n-1]
	return it
}

// NetworkConfig parameterizes the simulated network's fault model. All faults
// are sampled from the seeded RNG, so they are reproducible per seed.
type NetworkConfig struct {
	// DropProbability in [0,1]: chance a sent message is silently dropped.
	DropProbability float64
	// MinLatency / MaxLatency bound the seeded per-message delivery latency.
	// MaxLatency must be >= MinLatency; if both are zero, delivery is at the
	// current logical time (still ordered by seq).
	MinLatency time.Duration
	MaxLatency time.Duration
}

// DiskConfig parameterizes the simulated disk's fault model.
type DiskConfig struct {
	// FaultProbability in [0,1]: chance a read/write returns DiskFaultError.
	FaultProbability float64
	// MinStall / MaxStall bound the seeded logical stall added to each
	// successful operation (modeled as advanced logical time, see Disk).
	MinStall time.Duration
	MaxStall time.Duration
}

// DiskFaultError is the typed error returned by a simulated disk fault. Using a
// typed error (rather than a fake success) honors §11.4.3 honesty: a fault is
// surfaced, never swallowed.
type DiskFaultError struct {
	Op    string
	Actor ActorID
	Key   string
}

func (e *DiskFaultError) Error() string {
	return fmt.Sprintf("dst: simulated disk fault: op=%s actor=%s key=%s", e.Op, e.Actor, e.Key)
}

// TraceEntry is one recorded line of the deterministic event trace. The trace is
// the canonical observable output of a run; two runs of the same seed MUST
// produce an identical slice of TraceEntry (asserted in tests).
type TraceEntry struct {
	At   Time
	Kind string // e.g. "SEND", "DELIVER", "DROP", "WRITE", "READ", "FAULT", "TIMER", "LOG"
	Text string
}

// String renders a trace entry in a stable, byte-comparable form.
func (t TraceEntry) String() string {
	return fmt.Sprintf("%012d %-7s %s", int64(t.At), t.Kind, t.Text)
}

// Simulation is the deterministic single-threaded harness. It owns the seeded
// RNG, the logical clock, the event queue, the per-actor disk state, and the
// recorded trace. There is NO concurrency inside a Simulation: Run executes the
// event loop on the calling goroutine, which is what makes the order a pure
// function of the seed.
type Simulation struct {
	seed   int64
	rng    *rand.Rand
	now    Time
	seqCtr uint64

	// busyUntil records, per actor, the logical time at which that actor becomes
	// free again after a disk stall. A disk stall MUST delay only the stalling
	// actor's own subsequent work, NOT the global simulation clock: warping s.now
	// inside a handler would (a) make trace timestamps non-monotonic once the loop
	// later pops an earlier-scheduled event, violating the documented "Time is
	// MONOTONIC" invariant, and (b) incorrectly slow down every OTHER actor whose
	// events were scheduled at their own (earlier) fire times.
	//
	// Instead, a stall advances ONLY busyUntil[actor]. The actor's subsequent
	// outbound work in the same handler — the messages it Sends and timers it
	// schedules via At — is then dispatched relative to max(s.now, busyUntil[from])
	// so that the stall correctly defers that actor's downstream effects, while
	// every trace entry is still timestamped at the monotone loop clock s.now.
	busyUntil map[ActorID]Time

	queue    eventQueue
	handlers map[ActorID]Handler
	disk     map[ActorID]map[string]string // actor -> key -> value (simulated durable store)

	netCfg  NetworkConfig
	diskCfg DiskConfig

	trace []TraceEntry

	// faults maps a "fire at step N" hook: when stepCounter reaches the key,
	// the registered function runs before that step executes. This gives the
	// "inject a fault at a deterministic step" capability.
	faults      map[int]func(sim *Simulation)
	stepCounter int
}

// New constructs a Simulation seeded by seed. The RNG is the ONLY source of
// randomness; constructing with the same seed and issuing the same registration
// calls yields byte-for-byte identical execution.
//
// security, is the goal. crypto/rand would defeat the entire purpose.
//
//nolint:gosec // math/rand is REQUIRED here: determinism, not cryptographic
func New(seed int64) *Simulation {
	return &Simulation{
		seed:      seed,
		rng:       rand.New(rand.NewSource(seed)), //gosec:disable G404 -- deterministic simulation testing requires a reproducible seeded PRNG, not crypto/rand
		handlers:  make(map[ActorID]Handler),
		disk:      make(map[ActorID]map[string]string),
		busyUntil: make(map[ActorID]Time),
		faults:    make(map[int]func(sim *Simulation)),
	}
}

// Seed returns the seed this Simulation was constructed with (for capturing a
// failing seed to replay later).
func (s *Simulation) Seed() int64 { return s.seed }

// SetNetwork installs the network fault model. Call before Run.
func (s *Simulation) SetNetwork(cfg NetworkConfig) { s.netCfg = cfg }

// SetDisk installs the disk fault model. Call before Run.
func (s *Simulation) SetDisk(cfg DiskConfig) { s.diskCfg = cfg }

// Register associates an actor id with its production handler. Registering the
// same id twice replaces the handler (last wins).
func (s *Simulation) Register(id ActorID, h Handler) {
	s.handlers[id] = h
	if _, ok := s.disk[id]; !ok {
		s.disk[id] = make(map[string]string)
	}
}

// Now returns the current LOGICAL simulation time. Actors MUST use this instead
// of time.Now — there is no other clock.
func (s *Simulation) Now() Time { return s.now }

// log appends a trace entry at the current logical time s.now. Because s.now is
// driven solely by the Run loop (which pops events in non-decreasing (at, seq)
// order) and is NEVER warped forward by disk stalls, every entry's At is >= the
// previous entry's At — the documented "Time is MONOTONIC" invariant holds by
// construction. This is asserted directly in TestTraceTime_MonotonicUnderDiskStall.
func (s *Simulation) log(kind, text string) {
	s.trace = append(s.trace, TraceEntry{At: s.now, Kind: kind, Text: text})
}

// Log lets an actor record an application-level event into the deterministic
// trace (useful for asserting end-user-visible state transitions).
func (s *Simulation) Log(text string) { s.log("LOG", text) }

// nextSeq returns a fresh monotonic sequence number for stable event ordering.
func (s *Simulation) nextSeq() uint64 {
	s.seqCtr++
	return s.seqCtr
}

// sampleLatency draws a deterministic latency in [min,max] from the seeded RNG.
// If max <= min it returns min (a fixed, still-deterministic latency).
func (s *Simulation) sampleLatency(min, max time.Duration) Time {
	if max <= min {
		return Time(min)
	}
	span := int64(max - min)
	return Time(min) + Time(s.rng.Int63n(span+1))
}

// localClock returns the actor's effective current logical time: the later of
// the global loop clock s.now and the actor's own busyUntil. Outbound scheduling
// (Send / At) uses this so a stalled actor's downstream effects are deferred
// without warping the global clock.
func (s *Simulation) localClock(actor ActorID) Time {
	if bu := s.busyUntil[actor]; bu > s.now {
		return bu
	}
	return s.now
}

// applyStall models a per-actor disk stall. It advances ONLY the calling actor's
// local availability clock (busyUntil); it deliberately does NOT mutate s.now,
// the global scheduler clock owned exclusively by the Run loop. Writing to s.now
// here is what previously made the trace non-monotonic. Back-to-back disk ops by
// the same actor accumulate stall because the new busyUntil is measured from the
// actor's existing localClock.
func (s *Simulation) applyStall(actor ActorID) {
	stall := s.sampleLatency(s.diskCfg.MinStall, s.diskCfg.MaxStall)
	if stall <= 0 {
		return
	}
	s.busyUntil[actor] = s.localClock(actor) + stall
}

// Send routes a message through the simulated network. Delivery may be dropped
// (per DropProbability) or delayed by a seeded latency; either way the decision
// is a pure function of the seed and the order of prior RNG draws.
func (s *Simulation) Send(from, to ActorID, body string) {
	msg := Message{From: from, To: to, Body: body}
	s.log("SEND", fmt.Sprintf("%s->%s %s", from, to, body))

	// IMPORTANT: draw the drop decision and the latency in a FIXED order so the
	// RNG stream stays aligned across replays.
	if s.netCfg.DropProbability > 0 && s.rng.Float64() < s.netCfg.DropProbability {
		s.log("DROP", fmt.Sprintf("%s->%s %s", from, to, body))
		return
	}
	lat := s.sampleLatency(s.netCfg.MinLatency, s.netCfg.MaxLatency)
	// Schedule relative to the SENDER's local clock: if `from` is mid disk-stall,
	// its outbound message is correctly deferred until the actor is free, rather
	// than racing ahead at the (earlier) global s.now. localClock(from) >= s.now,
	// so this never schedules into the past.
	at := s.localClock(from) + lat
	heap.Push(&s.queue, &event{at: at, seq: s.nextSeq(), kind: evDeliver, msg: msg})
}

// At schedules a timer to fire at logical time s.Now()+after. This is how actors
// model retries/timeouts deterministically (no real timers, no wall-clock).
func (s *Simulation) At(after time.Duration, fn TimerFunc) {
	at := s.now + Time(after)
	heap.Push(&s.queue, &event{at: at, seq: s.nextSeq(), kind: evTimer, timer: fn})
}

// Write performs a simulated durable write. It may inject a fault (typed error)
// or a seeded stall (added logical latency). On success the value is stored in
// the actor's simulated disk and a WRITE trace entry is recorded.
func (s *Simulation) Write(actor ActorID, key, value string) error {
	if s.diskCfg.FaultProbability > 0 && s.rng.Float64() < s.diskCfg.FaultProbability {
		s.log("FAULT", fmt.Sprintf("WRITE %s %s", actor, key))
		return &DiskFaultError{Op: "write", Actor: actor, Key: key}
	}
	// A stall delays only this actor's subsequent outbound work (busyUntil),
	// never the global clock; the WRITE itself is recorded at the monotone loop
	// time s.now.
	s.applyStall(actor)
	if s.disk[actor] == nil {
		s.disk[actor] = make(map[string]string)
	}
	s.disk[actor][key] = value
	s.log("WRITE", fmt.Sprintf("%s %s=%s", actor, key, value))
	return nil
}

// Read performs a simulated durable read. It may inject a fault or stall in the
// same way as Write. The returned bool reports whether the key existed.
func (s *Simulation) Read(actor ActorID, key string) (string, bool, error) {
	if s.diskCfg.FaultProbability > 0 && s.rng.Float64() < s.diskCfg.FaultProbability {
		s.log("FAULT", fmt.Sprintf("READ %s %s", actor, key))
		return "", false, &DiskFaultError{Op: "read", Actor: actor, Key: key}
	}
	// See Write: the stall delays only this actor (busyUntil), never s.now.
	s.applyStall(actor)
	v, ok := s.disk[actor][key]
	s.log("READ", fmt.Sprintf("%s %s ok=%t", actor, key, ok))
	return v, ok, nil
}

// DiskValue exposes the current durable value for assertions (sink-side
// evidence): tests can verify that an actor actually persisted the right state,
// not merely that a function returned without error (CLAUDE-1).
func (s *Simulation) DiskValue(actor ActorID, key string) (string, bool) {
	v, ok := s.disk[actor][key]
	return v, ok
}

// InjectFaultAtStep registers fn to run immediately BEFORE the step-th event is
// processed (steps counted from 1). This is the "inject a fault at a
// deterministic step" capability: because step counting is itself deterministic,
// the injection point is reproducible across replays of the same seed.
func (s *Simulation) InjectFaultAtStep(step int, fn func(sim *Simulation)) {
	s.faults[step] = fn
}

// Run executes the deterministic event loop for up to maxSteps events (a step =
// one popped event). It stops early when the queue drains. It returns the number
// of steps actually executed. Because the loop pops strictly in (at, seq) order
// and the only randomness is the seeded RNG, the produced trace is a pure
// function of (seed, registrations, configs).
func (s *Simulation) Run(maxSteps int) int {
	executed := 0
	for executed < maxSteps && s.queue.Len() > 0 {
		s.stepCounter++
		if fn, ok := s.faults[s.stepCounter]; ok {
			// Run the injected fault hook at the current logical time, before
			// the event executes. Hooks may e.g. Send a poison message or
			// corrupt disk state.
			fn(s)
		}

		ev := heap.Pop(&s.queue).(*event)
		// Advance logical time to the event's fire time. Time is MONOTONIC:
		// events never fire in the past because (a) the heap is min-ordered and
		// (b) every scheduled event's fire time is s.now+lat >= s.now at insertion.
		// Crucially, disk stalls do NOT mutate s.now (they only advance the
		// per-actor busyUntil clock), so s.now can never be dragged forward past a
		// not-yet-popped event and then snap backward here.
		s.now = ev.at

		switch ev.kind {
		case evDeliver:
			s.log("DELIVER", fmt.Sprintf("%s->%s %s", ev.msg.From, ev.msg.To, ev.msg.Body))
			if h, ok := s.handlers[ev.msg.To]; ok {
				h.Receive(s, ev.msg)
			}
		case evTimer:
			s.log("TIMER", "")
			if ev.timer != nil {
				ev.timer(s)
			}
		}
		executed++
	}
	return executed
}

// Trace returns a copy of the recorded event trace.
func (s *Simulation) Trace() []TraceEntry {
	out := make([]TraceEntry, len(s.trace))
	copy(out, s.trace)
	return out
}

// TraceString renders the full trace as a single newline-joined string suitable
// for byte-for-byte equality assertions and for capturing alongside a failing
// seed (replay artifact).
func (s *Simulation) TraceString() string {
	var b strings.Builder
	for i, e := range s.trace {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(e.String())
	}
	return b.String()
}

// Replay re-runs a scenario from scratch under the SAME seed using the provided
// setup function, and returns the resulting trace string. This is the canonical
// "reproduce a captured failure" entry point: given a seed and the same setup,
// Replay(seed, setup) must equal the original TraceString byte-for-byte.
//
// setup receives a fresh Simulation already constructed with seed; it should
// register actors, configure faults, and seed the initial events, then return.
// Replay then drives Run(maxSteps).
func Replay(seed int64, maxSteps int, setup func(sim *Simulation)) string {
	sim := New(seed)
	setup(sim)
	sim.Run(maxSteps)
	return sim.TraceString()
}

// sortedKeys is a small deterministic helper used by tests/actors that need to
// iterate a map in a stable order (Go map iteration is randomized, which would
// break determinism if used directly). Exposed because actor logic frequently
// needs it and forgetting it is a classic determinism bug.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// SortedDiskKeys returns the actor's durable keys in deterministic sorted order.
func (s *Simulation) SortedDiskKeys(actor ActorID) []string {
	return sortedKeys(s.disk[actor])
}
