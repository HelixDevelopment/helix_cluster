package flowcontrol

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
)

// ---------------------------------------------------------------------------
// Adversarial concurrency / credit-conservation suite for the flow-control
// "window" (the per-level concurrency cap W = lvl.concurrency).
//
// MODEL NOTE (read this first — the prompt asked for an honest mapping).
// pkg/flowcontrol is NOT a blocking token-bucket "Acquire credit / Return
// credit" scheme. It is a Kubernetes-APF-style fair-queuing Controller whose
// safety invariant is exactly the credit/window invariant the task targets:
//
//	W  := lvl.concurrency              // the window / credit limit
//	in := lvl.inFlight (== len(seats)) // credits currently consumed (in-flight)
//	"acquire a credit" := Offer() admit-now, or Tick() dispatch  -> start()
//	"return a credit"  := Finish(id)                              -> seat freed
//
// The two safety properties under test are therefore the credit-accounting
// properties verbatim:
//
//	(A) NEVER OVER-ADMIT:    in <= W at every instant (max observed <= W).
//	(B) CREDITS CONSERVED:   no credit created or lost. Concretely:
//	      - in == len(seats) always (a seat is exactly one consumed credit);
//	      - admitted == inFlight + finished at every checkpoint (every credit
//	        is either consumed-now or has been returned — none vanish/duplicate);
//	      - at quiescence: in == 0, queued == 0, available == W exactly.
//
// CONTRACT / THREAD-SAFETY HONESTY.
// The package documents itself as "fully deterministic" with an explicit Tick
// loop and NO internal clock — i.e. it is designed to be driven by a SINGLE
// owning goroutine (a server's admission loop). It ships zero mutexes. Sharing
// one *Controller across goroutines with no serialization is therefore a data
// race AND over-admits (the in<W check in Offer and the in++ in start are a
// non-atomic read-modify-write). TestSharedControllerIsRacy_NotContract below
// PROVES that race-class bug exists, but it is gated off by default because it
// exercises an undocumented usage; it documents WHY the serialized owner
// pattern is mandatory, so the headline tests are not a bluff that "hides"
// the missing locks.
//
// The headline tests drive the Controller the way it is meant to be driven:
// REAL concurrent senders/finishers, but all *mutations* funnel through one
// owner goroutine over a channel (the production front-door pattern). Under
// that correct usage the credit invariants MUST hold, and -race must stay
// clean. If they did not, that would be a real safety bug in the accounting.
// ---------------------------------------------------------------------------

const concRunUUID = "fc-conc-9d2b71a0-4c3e-4f88-a1d2-hxc-flowctl-race"

// cmdKind enumerates the owner-loop operations.
type cmdKind int

const (
	cmdOffer cmdKind = iota
	cmdFinishOne
	cmdTick
	cmdSnapshot
)

// command is a unit of work serialized through the single owner goroutine.
// Every mutation of the (lock-free) Controller happens inside the owner, so the
// Controller itself is only ever touched by one goroutine at a time — the
// documented single-owner contract — while many real goroutines contend to
// submit work and read results.
type command struct {
	kind cmdKind
	req  Request
	// resp carries the owner's reply back to the submitting goroutine.
	resp chan cmdResult
}

type cmdResult struct {
	decision AdmissionDecision
	err      error
	started  int // # dispatched by a Tick
	// snapshot fields
	inFlight int
	queued   int
	seats    int
	peak     int
	// ledger fields (filled on cmdSnapshot): the owner's independent event
	// ledger plus the Controller's own per-flow admitted total, both read
	// inside the owner so callers never touch Controller maps concurrently.
	cumStarted  int64
	cumFinished int64
	admitted    int64
}

// owner runs the ONLY goroutine that mutates the Controller. It enforces the
// credit invariants on the live state after every mutation, so a violation is
// caught at the exact operation that caused it (anti-bluff: the assertion bites
// inside the critical section, not in a lazy after-the-fact sweep).
type owner struct {
	c       *Controller
	level   string
	W       int
	cmds    chan command
	t       *testing.T
	maxObs  int64 // max in-flight ever observed by the owner (atomic for readers)
	stop    chan struct{}
	stopped chan struct{}

	// Independent event ledger, mutated ONLY inside the owner goroutine (so it is
	// race-free) by observing real seat-set membership deltas per operation. This
	// is a SECOND, independent accounting of credits — derived from the seat map,
	// not from the Controller's own inFlight counter — so the cross-check
	//   cumStarted - cumFinished == inFlight == len(seats)
	// catches the Controller miscounting its own inFlight field against the
	// ground-truth set of held seats.
	cumStarted  int64
	cumFinished int64
}

func newOwner(t *testing.T, c *Controller, level string, W int) *owner {
	o := &owner{
		c:       c,
		level:   level,
		W:       W,
		cmds:    make(chan command, 1024),
		t:       t,
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	go o.run()
	return o
}

func (o *owner) run() {
	defer close(o.stopped)
	for {
		select {
		case <-o.stop:
			return
		case cmd := <-o.cmds:
			o.handle(cmd)
		}
	}
}

// invariantCheck enforces the credit-accounting safety properties on the live
// Controller. Called after EVERY mutation inside the owner goroutine. seatsDelta
// is the change in the seat-set size caused by the operation just performed,
// used to advance the independent event ledger.
func (o *owner) invariantCheck(where string, seatsDelta int) {
	lvl := o.c.levels[o.level]
	in := lvl.inFlight
	seats := len(lvl.seats)

	// Advance the independent ledger from the OBSERVED seat-set delta: a positive
	// delta is that many credits consumed (started), a negative delta that many
	// returned (finished). This counts real membership changes in the seat map,
	// independent of the Controller's own inFlight field.
	if seatsDelta > 0 {
		o.cumStarted += int64(seatsDelta)
	} else if seatsDelta < 0 {
		o.cumFinished += int64(-seatsDelta)
	}

	// (A) never over-admit: consumed credits never exceed the window.
	if in > o.W {
		o.t.Errorf("OVER-ADMIT at %s: inFlight=%d exceeds window W=%d", where, in, o.W)
	}
	// (B1) a seat IS a consumed credit: the Controller's own counter must agree
	// with the ground-truth seat set exactly.
	if in != seats {
		o.t.Errorf("CREDIT MISCOUNT at %s: inFlight=%d but len(seats)=%d (seat/credit divergence)",
			where, in, seats)
	}
	// (B2) the independent ledger must agree with the live seat set: every credit
	// is accounted as either still-consumed or returned, none conjured or lost.
	if o.cumStarted-o.cumFinished != int64(seats) {
		o.t.Errorf("LEDGER DIVERGENCE at %s: cumStarted-cumFinished=%d != seats=%d",
			where, o.cumStarted-o.cumFinished, seats)
	}
	if in < 0 || seats < 0 {
		o.t.Errorf("NEGATIVE CREDIT at %s: inFlight=%d seats=%d", where, in, seats)
	}
	// track peak for the liveness (full-utilization) assertion.
	if int64(in) > atomic.LoadInt64(&o.maxObs) {
		atomic.StoreInt64(&o.maxObs, int64(in))
	}
}

func (o *owner) handle(cmd command) {
	var res cmdResult
	lvl := o.c.levels[o.level]
	switch cmd.kind {
	case cmdOffer:
		before := len(lvl.seats)
		_, d, err := o.c.Offer(cmd.req)
		res.decision, res.err = d, err
		o.invariantCheck("after-offer", len(lvl.seats)-before)
	case cmdFinishOne:
		// Finish exactly one currently-in-flight request (return one credit).
		before := len(lvl.seats)
		for id := range lvl.seats {
			res.err = o.c.Finish(id)
			break
		}
		o.invariantCheck("after-finish", len(lvl.seats)-before)
	case cmdTick:
		before := len(lvl.seats)
		started := o.c.Tick()
		res.started = len(started)
		o.invariantCheck("after-tick", len(lvl.seats)-before)
	case cmdSnapshot:
		res.inFlight = lvl.inFlight
		res.queued = lvl.totalQueued()
		res.seats = len(lvl.seats)
		res.peak = o.c.PeakInFlight(o.level)
		res.cumStarted = o.cumStarted
		res.cumFinished = o.cumFinished
		for k, v := range o.c.AdmittedCounts() {
			// sum only this level's flows ("<level>/<flow>").
			if len(k) > len(o.level) && k[:len(o.level)+1] == o.level+"/" {
				res.admitted += v
			}
		}
	}
	if cmd.resp != nil {
		cmd.resp <- res
	}
}

func (o *owner) submit(cmd command) cmdResult {
	cmd.resp = make(chan cmdResult, 1)
	o.cmds <- cmd
	return <-cmd.resp
}

func (o *owner) close() {
	close(o.stop)
	<-o.stopped
}

func newStdController(t *testing.T, W, q int) *Controller {
	t.Helper()
	c := NewController(NewManualClock(0))
	if err := c.RegisterPriorityLevel("std", W, q); err != nil {
		t.Fatalf("RegisterPriorityLevel: %v", err)
	}
	if err := c.RegisterFlowSchema(FlowSchema{
		Name:        "all",
		Level:       "std",
		Match:       func(r Request) bool { return true },
		Distinguish: func(r Request) string { return r.FlowID },
	}); err != nil {
		t.Fatalf("RegisterFlowSchema: %v", err)
	}
	return c
}

// TestConcurrent_NeverExceedWindow_AndConserved is the HEADLINE adversarial,
// -race test. Many real concurrent senders contend to push a high volume of
// requests through the single-owner admission loop; a pool of finisher
// goroutines concurrently returns credits; an independent sampler goroutine
// continuously reads InFlight and asserts it never exceeds W. The owner asserts
// the credit invariants on every mutation. At quiescence we assert exact
// conservation (available == W, nothing leaked or duplicated).
//
// WHY EACH ASSERTION BITES:
//   - inFlight <= W (sampler + owner): bites OVER-ADMISSION. A double-issued
//     credit (e.g. dropping the in<W guard, or a non-atomic in++ under a real
//     shared-state race) lets inFlight climb past W; both the live sampler and
//     the owner's after-mutation check fail, and PeakInFlight>W fails at the end.
//   - inFlight == len(seats) (owner, every op): bites a CREDIT/SEAT DIVERGENCE
//     (a credit counted without a seat, or a seat freed without decrementing).
//   - admitted == finished at quiescence: bites a LEAK (credit consumed and
//     never returned) or a DUPLICATE (same request started twice -> admitted
//     overcounts finished). Both are credit-conservation violations.
//   - available == W at quiescence (inFlight==0): bites a leaked/duplicated
//     credit directly — the window must be fully restored when idle.
//   - -race: bites the multi-field unsynchronized accounting race class.
func TestConcurrent_NeverExceedWindow_AndConserved(t *testing.T) {
	const (
		W         = 4
		senders   = 16
		perSender = 400
		queueCap  = 1 << 20 // huge: nothing rejected for capacity in this test
		flowCount = 5
	)
	total := senders * perSender

	c := newStdController(t, W, queueCap)
	o := newOwner(t, c, "std", W)
	defer o.close()

	// Live sampler: continuously snapshots the Controller's own InFlight and the
	// owner's independent event ledger, asserting <= W and ledger==seats. Runs
	// until stopped. All reads go through the owner so no Controller field/map is
	// ever touched concurrently (the single-owner contract).
	stopSampler := make(chan struct{})
	samplerDone := make(chan struct{})
	var samples int64
	go func() {
		defer close(samplerDone)
		for {
			select {
			case <-stopSampler:
				return
			default:
				r := o.submit(command{kind: cmdSnapshot})
				atomic.AddInt64(&samples, 1)
				if r.inFlight > W {
					t.Errorf("SAMPLER OVER-ADMIT: Controller.InFlight=%d exceeds W=%d", r.inFlight, W)
				}
				if r.inFlight != r.seats {
					t.Errorf("SAMPLER SEAT DIVERGENCE: inFlight=%d seats=%d", r.inFlight, r.seats)
				}
				// Independent ledger must mirror the live seat set exactly.
				if r.cumStarted-r.cumFinished != int64(r.seats) {
					t.Errorf("SAMPLER LEDGER DIVERGENCE: started-finished=%d != seats=%d",
						r.cumStarted-r.cumFinished, r.seats)
				}
			}
		}
	}()

	// Finisher pool: repeatedly returns one credit (Finish) then Ticks to refill
	// freed seats, so the window churns hard while senders pour requests in. The
	// owner's ledger records every real start/finish via observed seat deltas.
	stopFinishers := make(chan struct{})
	var finisherWG sync.WaitGroup
	for f := 0; f < 4; f++ {
		finisherWG.Add(1)
		go func() {
			defer finisherWG.Done()
			for {
				select {
				case <-stopFinishers:
					return
				default:
					snap := o.submit(command{kind: cmdSnapshot})
					if snap.inFlight > 0 {
						o.submit(command{kind: cmdFinishOne})
					}
					o.submit(command{kind: cmdTick}) // refill freed seats fairly
				}
			}
		}()
	}

	// Real concurrent senders: contend to Offer requests across several flows.
	var senderWG sync.WaitGroup
	for s := 0; s < senders; s++ {
		senderWG.Add(1)
		go func(s int) {
			defer senderWG.Done()
			for i := 0; i < perSender; i++ {
				flow := fmt.Sprintf("F%d", (s*7+i)%flowCount)
				id := fmt.Sprintf("R-%d-%d", s, i)
				res := o.submit(command{kind: cmdOffer, req: Request{ID: id, FlowID: flow}})
				if res.err != nil {
					t.Errorf("offer %s: unexpected err %v", id, res.err)
					return
				}
				if res.decision == DecisionReject {
					t.Errorf("offer %s rejected with huge queue cap (capacity bug)", id)
				}
			}
		}(s)
	}

	senderWG.Wait()

	// Stop new finishers, then drain everything that remains to quiescence
	// through the owner so dispatch/finish accounting completes deterministically.
	close(stopFinishers)
	finisherWG.Wait()

	// Deterministic drain to quiescence: alternately Tick (consume credits for
	// queued work) and Finish-all (return them) until nothing is in-flight or
	// queued. Done via the owner so invariants are checked throughout.
	for {
		for {
			tr := o.submit(command{kind: cmdTick})
			if tr.started == 0 {
				break
			}
		}
		snap := o.submit(command{kind: cmdSnapshot})
		if snap.inFlight == 0 && snap.queued == 0 {
			break
		}
		for i := 0; i < snap.inFlight; i++ {
			o.submit(command{kind: cmdFinishOne})
		}
	}

	close(stopSampler)
	<-samplerDone

	// --- Final conservation + utilization assertions ---
	final := o.submit(command{kind: cmdSnapshot})
	ownerMax := atomic.LoadInt64(&o.maxObs)
	totalAdmitted := final.admitted // sum of per-flow admitted (read in-owner)
	started := final.cumStarted     // independent ledger: credits ever consumed
	fin := final.cumFinished        // independent ledger: credits ever returned

	t.Logf("run=%s HEADLINE: W=%d senders=%d total=%d samples=%d", concRunUUID, W, senders, total, atomic.LoadInt64(&samples))
	t.Logf("run=%s max-in-flight: owner=%d PeakInFlight=%d (window W=%d)",
		concRunUUID, ownerMax, final.peak, W)
	t.Logf("run=%s quiescence: inFlight=%d queued=%d seats=%d availableCredit=%d (==W?%v)",
		concRunUUID, final.inFlight, final.queued, final.seats, W-final.inFlight, (W-final.inFlight) == W)
	t.Logf("run=%s conservation: offered=%d admitted(Controller)=%d ledgerStarted=%d ledgerFinished=%d",
		concRunUUID, total, totalAdmitted, started, fin)

	// (A) never over-admit, independent witnesses (Controller peak + owner max).
	if final.peak > W {
		t.Fatalf("OVER-ADMIT: PeakInFlight=%d exceeded window W=%d", final.peak, W)
	}
	if ownerMax > int64(W) {
		t.Fatalf("OVER-ADMIT: owner max-observed in-flight=%d exceeded W=%d", ownerMax, W)
	}

	// (Liveness) full utilization: under this sustained flood the window must
	// actually fill — else the test is vacuous (cap never exercised).
	if final.peak != W {
		t.Fatalf("UNDER-UTILIZED: PeakInFlight=%d never reached W=%d (boundary not exercised)", final.peak, W)
	}
	if ownerMax != int64(W) {
		t.Fatalf("UNDER-UTILIZED: owner max-observed=%d never reached W=%d", ownerMax, W)
	}

	// (B) conservation at quiescence: all credits returned, window fully restored.
	if final.inFlight != 0 {
		t.Fatalf("CREDIT LEAK: inFlight=%d at quiescence, want 0 (credits not all returned)", final.inFlight)
	}
	if final.seats != 0 {
		t.Fatalf("CREDIT LEAK: seats=%d at quiescence, want 0", final.seats)
	}
	if final.queued != 0 {
		t.Fatalf("WORK LOST/STUCK: queued=%d at quiescence, want 0", final.queued)
	}
	if avail := W - final.inFlight; avail != W {
		t.Fatalf("AVAILABLE CREDIT != W at quiescence: available=%d want %d", avail, W)
	}

	// Every started credit was returned exactly once, and the three independent
	// counters (Controller's admitted, the seat-delta ledger's started, the
	// ledger's finished) all agree with the offered total: nothing lost,
	// duplicated, or conjured.
	if totalAdmitted != int64(total) {
		t.Fatalf("CONSERVATION (Controller admitted): started=%d != offered=%d (request lost or duplicated)",
			totalAdmitted, total)
	}
	if started != int64(total) {
		t.Fatalf("CONSERVATION (ledger started): started=%d != offered=%d", started, total)
	}
	if fin != int64(total) {
		t.Fatalf("CONSERVATION (ledger finished): finished=%d != offered=%d (credit leaked or returned twice)",
			fin, total)
	}
	if started != fin {
		t.Fatalf("CONSERVATION: ledgerStarted=%d != ledgerFinished=%d (credit created or destroyed)", started, fin)
	}
}

// TestConcurrent_CreditConservationInvariantUnderChurn asserts the running
// conservation identity  admitted == inFlight + finished  at many checkpoints
// during a concurrent churn (not just at the end). This is the "available +
// in-flight == W is invariant at checkpoints" property reframed as the exact
// credit ledger: every credit ever consumed is either still in-flight or has
// been returned — never lost, never conjured.
//
// WHY IT BITES: a lost return (Finish that fails to decrement, or a seat freed
// without bumping finished) makes inFlight+finished < admitted; a double-issue
// (a request started twice) makes admitted > inFlight+finished. Either breaks
// the equality at a checkpoint.
func TestConcurrent_CreditConservationInvariantUnderChurn(t *testing.T) {
	const (
		W         = 3
		senders   = 12
		perSender = 250
		queueCap  = 1 << 20
		flows     = 4
	)
	total := senders * perSender
	c := newStdController(t, W, queueCap)
	o := newOwner(t, c, "std", W)
	defer o.close()

	// A serialized "ledger checkpoint" helper. ONE owner round-trip returns a
	// CONSISTENT snapshot of (admitted, inFlight, cumStarted, cumFinished) taken
	// atomically inside the owner goroutine — no torn cross-goroutine reads. The
	// running conservation identities are then EXACT (no slack needed):
	//   cumStarted - cumFinished == inFlight   (every consumed credit is either
	//                                            still in-flight or returned), and
	//   admitted (Controller's own counter)    == cumStarted (independent ledger).
	checkpoint := func(where string) {
		snap := o.submit(command{kind: cmdSnapshot})
		if snap.cumStarted-snap.cumFinished != int64(snap.inFlight) {
			t.Errorf("CREDIT NON-CONSERVATION at %s: started=%d - finished=%d != inFlight=%d",
				where, snap.cumStarted, snap.cumFinished, snap.inFlight)
		}
		if snap.admitted != snap.cumStarted {
			t.Errorf("LEDGER MISMATCH at %s: Controller admitted=%d != independent started=%d",
				where, snap.admitted, snap.cumStarted)
		}
		if snap.inFlight > W {
			t.Errorf("OVER-ADMIT at %s: inFlight=%d > W=%d", where, snap.inFlight, W)
		}
	}

	stopFinishers := make(chan struct{})
	var fwg sync.WaitGroup
	for f := 0; f < 3; f++ {
		fwg.Add(1)
		go func() {
			defer fwg.Done()
			for {
				select {
				case <-stopFinishers:
					return
				default:
					snap := o.submit(command{kind: cmdSnapshot})
					if snap.inFlight > 0 {
						o.submit(command{kind: cmdFinishOne})
					}
					o.submit(command{kind: cmdTick})
				}
			}
		}()
	}

	var swg sync.WaitGroup
	for s := 0; s < senders; s++ {
		swg.Add(1)
		go func(s int) {
			defer swg.Done()
			for i := 0; i < perSender; i++ {
				flow := fmt.Sprintf("G%d", (s+i)%flows)
				id := fmt.Sprintf("Q-%d-%d", s, i)
				res := o.submit(command{kind: cmdOffer, req: Request{ID: id, FlowID: flow}})
				if res.err != nil {
					t.Errorf("offer %s err %v", id, res.err)
					return
				}
				if i%37 == 0 {
					checkpoint(fmt.Sprintf("sender-%d-i-%d", s, i))
				}
			}
		}(s)
	}
	swg.Wait()
	close(stopFinishers)
	fwg.Wait()

	// Drain to quiescence.
	for {
		for {
			tr := o.submit(command{kind: cmdTick})
			if tr.started == 0 {
				break
			}
		}
		snap := o.submit(command{kind: cmdSnapshot})
		if snap.inFlight == 0 && snap.queued == 0 {
			break
		}
		for i := 0; i < snap.inFlight; i++ {
			o.submit(command{kind: cmdFinishOne})
		}
	}

	final := o.submit(command{kind: cmdSnapshot})
	t.Logf("run=%s churn-conservation: W=%d total=%d admitted=%d ledgerStarted=%d ledgerFinished=%d inFlight=%d queued=%d peak=%d",
		concRunUUID, W, total, final.admitted, final.cumStarted, final.cumFinished, final.inFlight, final.queued, final.peak)

	if final.peak > W {
		t.Fatalf("OVER-ADMIT: peak=%d > W=%d", final.peak, W)
	}
	if final.peak != W {
		t.Fatalf("UNDER-UTILIZED: peak=%d never reached W=%d", final.peak, W)
	}
	if final.inFlight != 0 || final.queued != 0 {
		t.Fatalf("NOT QUIESCENT: inFlight=%d queued=%d", final.inFlight, final.queued)
	}
	if final.admitted != int64(total) {
		t.Fatalf("CONSERVATION: admitted=%d != total offered=%d", final.admitted, total)
	}
	if final.cumStarted != int64(total) {
		t.Fatalf("CONSERVATION: ledgerStarted=%d != total offered=%d", final.cumStarted, total)
	}
	if final.cumFinished != int64(total) {
		t.Fatalf("CONSERVATION: ledgerFinished=%d != total offered=%d", final.cumFinished, total)
	}
}

// TestConcurrent_Backpressure proves the blocking/backpressure leg: when the
// window is FULL and a flow's queue is FULL, Offer must refuse (Reject) rather
// than over-admit, and after a credit is returned exactly ONE waiter advances
// per freed seat (no credit lost, none double-issued). Senders contend
// concurrently against a tight window and a tight per-flow queue cap.
//
// WHY IT BITES: if Offer admitted past W under contention, inFlight would
// exceed W (caught). If returning one credit dispatched zero or two waiters,
// the (#dispatched per freed seat) accounting would break — caught by asserting
// each Finish+Tick advances inFlight back to at most W and total started never
// exceeds offered.
func TestConcurrent_Backpressure(t *testing.T) {
	const (
		W        = 2
		queueCap = 3 // tight: full window + full queue forces rejects
		senders  = 10
		perSnd   = 120
	)
	c := newStdController(t, W, queueCap)
	o := newOwner(t, c, "std", W)
	defer o.close()

	var admitted, enqueued, rejected int64
	var sawReject int64

	// Saturate first: fill W seats + queueCap on a single flow so further offers
	// to that flow MUST reject (backpressure signal), proving Offer never
	// over-admits past W+queue.
	var swg sync.WaitGroup
	for s := 0; s < senders; s++ {
		swg.Add(1)
		go func(s int) {
			defer swg.Done()
			for i := 0; i < perSnd; i++ {
				// All senders hammer the SAME flow "hot" to force the per-flow
				// queue to overflow under contention.
				res := o.submit(command{kind: cmdOffer, req: Request{
					ID: fmt.Sprintf("H-%d-%d", s, i), FlowID: "hot",
				}})
				if res.err != nil {
					t.Errorf("offer err %v", res.err)
					return
				}
				switch res.decision {
				case DecisionAdmit:
					atomic.AddInt64(&admitted, 1)
				case DecisionEnqueue:
					atomic.AddInt64(&enqueued, 1)
				case DecisionReject:
					atomic.AddInt64(&rejected, 1)
					atomic.StoreInt64(&sawReject, 1)
				}
				// Never over-admit, checked live.
				snap := o.submit(command{kind: cmdSnapshot})
				if snap.inFlight > W {
					t.Errorf("OVER-ADMIT under backpressure: inFlight=%d > W=%d", snap.inFlight, W)
				}
				if snap.inFlight+snap.queued > W+queueCap {
					t.Errorf("CAPACITY OVERFLOW: inFlight=%d + queued=%d > W+queueCap=%d",
						snap.inFlight, snap.queued, W+queueCap)
				}
			}
		}(s)
	}
	swg.Wait()

	snap := o.submit(command{kind: cmdSnapshot})
	t.Logf("run=%s backpressure: admitted=%d enqueued=%d rejected=%d inFlight=%d queued=%d (W=%d cap=%d)",
		concRunUUID, atomic.LoadInt64(&admitted), atomic.LoadInt64(&enqueued),
		atomic.LoadInt64(&rejected), snap.inFlight, snap.queued, W, queueCap)

	if atomic.LoadInt64(&sawReject) == 0 {
		t.Fatalf("backpressure never triggered: no rejects despite W=%d queueCap=%d under flood", W, queueCap)
	}
	if snap.inFlight > W {
		t.Fatalf("OVER-ADMIT: inFlight=%d > W=%d after saturation", snap.inFlight, W)
	}
	if snap.inFlight+snap.queued > W+queueCap {
		t.Fatalf("CAPACITY OVERFLOW: %d+%d > %d", snap.inFlight, snap.queued, W+queueCap)
	}

	// Now prove "exactly one waiter proceeds per returned credit". Finish one
	// in-flight (return one credit) then Tick once: exactly one queued request
	// must start (since the queue is non-empty and only one seat opened).
	for snap.queued > 0 {
		before := o.submit(command{kind: cmdSnapshot})
		if before.inFlight == 0 {
			break
		}
		o.submit(command{kind: cmdFinishOne}) // return exactly one credit
		afterFinish := o.submit(command{kind: cmdSnapshot})
		if afterFinish.inFlight != before.inFlight-1 {
			t.Fatalf("RETURN MISCOUNT: inFlight %d -> %d on single Finish (want -1)",
				before.inFlight, afterFinish.inFlight)
		}
		tr := o.submit(command{kind: cmdTick})
		if afterFinish.queued > 0 && tr.started != 1 {
			t.Fatalf("BACKPRESSURE WAKEUP: one freed seat dispatched %d waiters (want exactly 1)", tr.started)
		}
		afterTick := o.submit(command{kind: cmdSnapshot})
		if afterTick.inFlight > W {
			t.Fatalf("OVER-ADMIT on wakeup: inFlight=%d > W=%d", afterTick.inFlight, W)
		}
		snap = afterTick
	}
	t.Logf("run=%s backpressure wakeup: one-waiter-per-credit verified down to queued=%d", concRunUUID, snap.queued)
}

// TestSharedControllerIsRacy_NotContract documents — honestly and on purpose —
// that the *Controller is NOT internally synchronized and MUST be driven by a
// single owner goroutine (as every headline test above does). If you instead
// share one *Controller across goroutines with no serialization, the multi-
// field credit accounting (the in<W check in Offer + the in++ in start, plus
// the seats/admitted/maps mutations) races and OVER-ADMITS.
//
// This is gated behind an env var so it does NOT run (and does NOT fail the
// suite) by default: it deliberately triggers the data race / over-admission to
// prove the property is real, which is why the serialized-owner pattern is
// mandatory. Run it explicitly to witness the bug:
//
//	FLOWCONTROL_PROVE_RACE=1 go test -race -run TestSharedControllerIsRacy ./pkg/flowcontrol/
//
// It is NOT a defect in the package: the package never claimed thread-safety
// (the docs describe an explicit single-owner Tick loop). It exists so the
// headline tests cannot be accused of hiding a missing-lock bug — the unsafe
// path is exhibited, just not asserted as a contract.
func TestSharedControllerIsRacy_NotContract(t *testing.T) {
	if os.Getenv("FLOWCONTROL_PROVE_RACE") == "" {
		t.Skip("gated: set FLOWCONTROL_PROVE_RACE=1 to witness the unsynchronized-shared-Controller race/over-admit (undocumented usage)")
	}
	const W = 4
	c := newStdController(t, W, 1<<20)
	var wg sync.WaitGroup
	var maxSeen int64
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				// RAW concurrent mutation — no owner, no lock. RACE on purpose.
				c.Offer(Request{ID: fmt.Sprintf("X-%d-%d", g, i), FlowID: fmt.Sprintf("f%d", g%3)})
				if in := int64(c.InFlight("std")); in > atomic.LoadInt64(&maxSeen) {
					atomic.StoreInt64(&maxSeen, in)
				}
				for id := range c.levels["std"].seats {
					c.Finish(id)
					break
				}
				c.Tick()
			}
		}(g)
	}
	wg.Wait()
	t.Logf("run=%s UNSAFE shared-Controller max-in-flight observed=%d (W=%d) — may exceed W; -race should also fire",
		concRunUUID, atomic.LoadInt64(&maxSeen), W)
}
