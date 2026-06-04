package main

import (
	"fmt"

	"github.com/HelixDevelopment/helix_cluster/pkg/dst"
	"github.com/HelixDevelopment/helix_cluster/pkg/porcupine"
)

// ScenarioConfig parameterizes one simulated run.
type ScenarioConfig struct {
	// MaxSteps bounds the deterministic scheduler.
	MaxSteps int
	// Buggy, when true, injects a linearizability defect into the primary's
	// apply path (a stale write wins a race). It is used ONLY by tests to prove
	// the gate is load-bearing — production runs leave it false.
	Buggy bool
}

// SeedResult is the per-seed verdict.
type SeedResult struct {
	Seed      int64
	OK        bool
	Reason    string
	NumOps    int
	Offending porcupine.Operation
}

// --- The representative distributed scenario -------------------------------
//
// We model a single linearizable register replicated through a PRIMARY actor.
// A pool of CLIENT actors issue write(v) and read() requests to the primary.
// The primary serializes them: on a write it durably persists the new value and
// acks; on a read it returns the currently persisted value. The simulated
// NETWORK drops/delays/reorders the request and response messages and the
// simulated DISK can stall — none of which may break linearizability of the
// register, because the primary is the single serialization point.
//
// The client-observed history (each request's call time, its response, and the
// value returned/written) is recorded as a porcupine history and checked against
// the sequential register Model below. A correct primary always yields a
// linearizable history. The Buggy variant lets a stale value overwrite a newer
// one (lost-update / stale-read), which porcupine MUST reject.

const (
	primaryID = dst.ActorID("primary")
	clientID  = dst.ActorID("client")
)

// regModel is the sequential spec of a single read/write register.
// Input encodes the request; Output encodes the response the system returned.
//
//   - write: input = wreq{val}, output = wresp{} ; legal from any state,
//     resulting state = val.
//   - read:  input = rreq{},    output = rresp{val} ; legal iff val == state.
func regModel() porcupine.Model {
	type state = int
	return porcupine.Model{
		Init: func() interface{} { return state(0) },
		Step: func(st, input, output interface{}) (bool, interface{}) {
			cur := st.(state)
			switch in := input.(type) {
			case wreq:
				// A write transitions the register to in.val and must be acked.
				if _, ok := output.(wresp); !ok {
					return false, st
				}
				return true, state(in.val)
			case rreq:
				// A read must return exactly the current register value.
				out, ok := output.(rresp)
				if !ok {
					return false, st
				}
				if out.val != cur {
					return false, st
				}
				return true, st
			default:
				return false, st
			}
		},
		Equal: func(a, b interface{}) bool { return a.(state) == b.(state) },
	}
}

type wreq struct{ val int }
type wresp struct{}
type rreq struct{}
type rresp struct{ val int }

// op is a pending client request tracked from issue to response.
type op struct {
	tok    int64
	isRead bool
	val    int // for writes: value being written
}

// primaryActor is the production-style serialization point. Its logic is the
// model under test; the harness only mocks the network/disk/clock around it.
//
// A CORRECT primary persists each write durably before acking and serves reads
// from that durable value, so the single-register history is always
// linearizable no matter how the network reorders or the disk stalls.
type primaryActor struct {
	buggy bool
	prev  string // previous durable value, retained to model the buggy stale read
}

func (p *primaryActor) Receive(sim *dst.Simulation, msg dst.Message) {
	// Body format: "W <reqid> <val>" or "R <reqid>".
	var kind string
	var reqid, val int
	if _, err := fmt.Sscanf(msg.Body, "%s %d %d", &kind, &reqid, &val); err != nil {
		// Read requests have no value field; reparse.
		if _, err2 := fmt.Sscanf(msg.Body, "%s %d", &kind, &reqid); err2 != nil {
			return
		}
	}
	switch kind {
	case "W":
		// Retain the value we are about to overwrite so the buggy read path can
		// serve it back (a stale read / lost update).
		if cur, ok := sim.DiskValue(primaryID, "reg"); ok {
			p.prev = cur
		}
		_ = sim.Write(primaryID, "reg", fmt.Sprintf("%d", val))
		// Ack carries the request id so the client matches its response.
		sim.Send(primaryID, msg.From, fmt.Sprintf("WACK %d", reqid))
	case "R":
		stored := 0
		if cur, ok := sim.DiskValue(primaryID, "reg"); ok {
			val := cur
			if p.buggy && p.prev != "" {
				// BUG (test-only): serve the PREVIOUS durable value instead of
				// the current one. The write that produced `cur` has already
				// been acked to its client (it completed in real time), so a
				// read that returns the older value is a stale read with no
				// admissible linearization point — porcupine MUST reject it.
				val = p.prev
			}
			if _, e := fmt.Sscanf(val, "%d", &stored); e != nil {
				stored = 0
			}
		}
		sim.Send(primaryID, msg.From, fmt.Sprintf("RACK %d %d", reqid, stored))
	}
}

// clientDriver issues a deterministic sequence of requests and records each
// request's real-time window into the porcupine recorder. It is one actor that
// fans out to several logical client ids so requests overlap in logical time.
type clientDriver struct {
	rec     *porcupine.Recorder
	pending map[int]*op // reqid -> tracked op
	nextReq int
	issued  int
	maxReqs int
	rng     *seededRNG
}

func (c *clientDriver) Receive(sim *dst.Simulation, msg dst.Message) {
	// Body: "WACK <reqid>" or "RACK <reqid> <val>".
	var kind string
	var reqid, val int
	n, _ := fmt.Sscanf(msg.Body, "%s %d %d", &kind, &reqid, &val)
	if n < 2 {
		return
	}
	o, ok := c.pending[reqid]
	if !ok {
		return
	}
	delete(c.pending, reqid)
	if o.isRead {
		c.rec.End(o.tok, rresp{val: val})
	} else {
		c.rec.End(o.tok, wresp{})
	}
	c.issueNext(sim)
}

// issueNext issues the next request (if any remain) and opens its porcupine
// window via Begin. Issuing on each ack keeps a bounded number of in-flight
// requests while producing genuinely overlapping real-time windows.
func (c *clientDriver) issueNext(sim *dst.Simulation) {
	if c.issued >= c.maxReqs {
		return
	}
	c.issued++
	reqid := c.nextReq
	c.nextReq++

	// Alternate reads and writes deterministically; write values increase so a
	// lost update / stale read is detectable by the register model.
	isRead := c.rng.IntN(2) == 0
	var o *op
	if isRead {
		o = &op{isRead: true}
		o.tok = c.rec.Begin(reqid, rreq{})
		sim.Send(clientID, primaryID, fmt.Sprintf("R %d", reqid))
	} else {
		v := c.issued // strictly increasing write values
		o = &op{isRead: false, val: v}
		o.tok = c.rec.Begin(reqid, wreq{val: v})
		sim.Send(clientID, primaryID, fmt.Sprintf("W %d %d", reqid, v))
	}
	c.pending[reqid] = o
}

// seededRNG is a tiny deterministic LCG used only for request-kind selection.
// It is independent of the harness RNG so request shape stays stable even if
// harness internals draw differently; seeded from the run seed for determinism.
type seededRNG struct{ s uint64 }

func newSeededRNG(seed int64) *seededRNG { //gosec:disable G115 -- LCG state uses the seed's bit pattern; int64->uint64 is intentional and value-preserving
	return &seededRNG{s: uint64(seed)*2862933555777941757 + 3037000493}
}

func (r *seededRNG) next() uint64 {
	r.s = r.s*6364136223846793005 + 1442695040888963407
	return r.s
}

func (r *seededRNG) IntN(n int) int {
	if n <= 0 {
		return 0
	}
	return int((r.next() >> 33) % uint64(n)) //gosec:disable G115 -- n>0 (guarded above); the mod result is in [0,n), so the conversions are value-bounded
}

// RunSeed executes one full simulation for `seed`, collects the client history,
// and checks it for linearizability. It is the per-seed PASS/FAIL primitive.
func RunSeed(seed int64, cfg ScenarioConfig) SeedResult {
	sim := dst.New(seed)
	// A representative adversarial environment: messages REORDER via seeded
	// per-message latency and the disk occasionally stalls. None of this may
	// break register linearizability for a correct primary.
	//
	// We deliberately do NOT drop messages here. A dropped request/ack would
	// model a write whose durable effect silently applied while its client
	// never observed completion; such an op is correctly absent from the
	// recorded history, and a later read returning that absent write's value is
	// genuinely non-linearizable against the single-register spec. That is a
	// real modeling artifact, not a primary bug, so it must not be conflated
	// with the safety violation the gate is built to catch. Reordering + disk
	// stalls already provide the adversarial interleaving the checker needs.
	sim.SetNetwork(dst.NetworkConfig{
		DropProbability: 0,
		MinLatency:      1_000_000,  // 1ms
		MaxLatency:      10_000_000, // 10ms
	})
	sim.SetDisk(dst.DiskConfig{
		MinStall: 0,
		MaxStall: 2_000_000, // up to 2ms
	})

	rec := porcupine.NewRecorder()
	driver := &clientDriver{
		rec:     rec,
		pending: make(map[int]*op),
		maxReqs: 40,
		rng:     newSeededRNG(seed),
	}

	sim.Register(primaryID, &primaryActor{buggy: cfg.Buggy})
	sim.Register(clientID, driver)

	// Prime the pipeline with a few concurrent in-flight requests so windows
	// overlap (making the linearizability check non-trivial).
	const inflight = 4
	for i := 0; i < inflight; i++ {
		driver.issueNext(sim)
	}

	sim.Run(cfg.MaxSteps)

	history := rec.History()
	// Drop requests that never received a response (dropped final messages):
	// History() already omits ops whose End was never called, so the history is
	// well-formed. But because dropped requests stall the client's issue chain,
	// a correct run may produce fewer than maxReqs completed ops — that is fine;
	// we check whatever completed.
	model := regModel()
	res := porcupine.CheckOperationsVerbose(model, history)

	out := SeedResult{Seed: seed, OK: res.OK, NumOps: len(history)}
	if !res.OK {
		out.Reason = "client-observed history is not linearizable vs single-register spec"
		out.Offending = res.Offending
	}
	return out
}
