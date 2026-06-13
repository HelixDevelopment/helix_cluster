package stonith

// This file holds the STONITH SAFETY-CONTRACT tests: end-to-end proofs that the
// fencing mechanism genuinely fences, with anti-bluff teeth.
//
// The existing tests (stonith_test.go, cloud_test.go) prove individual drivers
// transition real backing state and that Fence returns ErrNotConfirmed when a
// PowerOff *call* fails. They do NOT prove the two properties that make STONITH
// STONITH rather than best-effort:
//
//   1. Fence is recorded ONLY on POSITIVE confirmation. The dangerous bluff is
//      not "PowerOff returned an error" (already covered by FailingPowerController)
//      but "PowerOff returned nil success yet the node is STILL ALIVE" — i.e. a
//      driver that ticked a box / wrote a map entry without the target actually
//      transitioning. A false "fenced" is the exact condition that causes
//      split-brain double-execution. This file injects that silent-no-op fabric
//      and proves Fence refuses to confirm (ErrNotConfirmed), so the survivor
//      does NOT proceed to take over an un-fenced node.
//
//   2. A confirmed-fenced node is NEUTRALISED: its protected action is rejected
//      and the surviving node can claim the resource without conflict. This file
//      builds a real fence-gated resource lease (the "protected operation") and
//      proves the fenced node cannot acquire/keep it while the survivor can —
//      the property that a map-only fake (mark "fenced" but still let the node
//      act) would FAIL.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// --- A real, fence-gated "protected resource" (the sink we assert against) ---

// errFenced is returned by the resource when a fenced node attempts a protected
// action. Its presence in a rejection is the positive proof that neutralisation
// is driven by the FENCE state, not by some unrelated bookkeeping.
var errFenced = errors.New("protected action refused: holder is fenced")

// errHeldByOther is returned when a node tries to claim a resource currently and
// legitimately held by a different, un-fenced node.
var errHeldByOther = errors.New("protected action refused: resource held by another live node")

// errBMCUnreachable models a power fabric that genuinely cannot reach the target
// (the realistic trigger for an UNCONFIRMED fence in the property-#4 test).
var errBMCUnreachable = errors.New("bmc unreachable")

// fenceGatedResource models a single exclusive resource (a role / lease / shared
// write capability) whose every protected mutation is gated on the cluster's
// authoritative fence view. It consults a live IsFencer (the real
// MultiLevelFencer) on EVERY operation, so a node that has been confirmed fenced
// can never act through it — this is the in-test analogue of "the fenced node's
// epoch/token is invalid; its writes are refused".
//
// It is deliberately NOT a mock: holder state and the write log are real,
// observable state. Asserting on writeLog / holder is sink-side evidence.
type fenceGatedResource struct {
	mu       sync.Mutex
	fencer   IsFencer
	holder   string   // current owner ("" == free)
	writeLog []string // every committed protected write, in order
}

// IsFencer is the minimal slice of MultiLevelFencer the resource depends on,
// so the resource is wired to the SAME authoritative fence view the cluster uses
// (no parallel/forked truth that a bluff could diverge from).
type IsFencer interface {
	IsFenced(ctx context.Context, target string) (bool, error)
}

func newFenceGatedResource(f IsFencer) *fenceGatedResource {
	return &fenceGatedResource{fencer: f}
}

// acquire claims the resource for node. It REFUSES if node is fenced, or if the
// resource is already held by a different node that is NOT fenced (a live
// holder keeps its lease). If the current holder has since been fenced, node may
// take over — that is the survivor-takeover path STONITH exists to enable.
func (r *fenceGatedResource) acquire(ctx context.Context, node string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Gate: a fenced node may never acquire.
	fenced, err := r.fencer.IsFenced(ctx, node)
	if err != nil {
		return fmt.Errorf("fence check for %q: %w", node, err)
	}
	if fenced {
		return fmt.Errorf("%q: %w", node, errFenced)
	}

	if r.holder != "" && r.holder != node {
		// Resource is held. The incoming node may take over ONLY if the current
		// holder has been fenced (confirmed dead) — otherwise both would be live
		// owners == split-brain, which we refuse.
		holderFenced, herr := r.fencer.IsFenced(ctx, r.holder)
		if herr != nil {
			return fmt.Errorf("fence check for holder %q: %w", r.holder, herr)
		}
		if !holderFenced {
			return fmt.Errorf("%q wants resource held by live %q: %w", node, r.holder, errHeldByOther)
		}
		// Holder is fenced — safe to evict and take over.
	}
	r.holder = node
	return nil
}

// write performs a protected mutation. It REFUSES if the caller is not the
// current holder or if the caller is fenced. A fenced former-holder that still
// believes it owns the resource is exactly the split-brain double-writer STONITH
// must stop, so we re-check the fence on every write, not just at acquire time.
func (r *fenceGatedResource) write(ctx context.Context, node, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	fenced, err := r.fencer.IsFenced(ctx, node)
	if err != nil {
		return fmt.Errorf("fence check for %q: %w", node, err)
	}
	if fenced {
		return fmt.Errorf("%q: %w", node, errFenced)
	}
	if r.holder != node {
		return fmt.Errorf("%q is not the holder (%q holds it): %w", node, r.holder, errHeldByOther)
	}
	r.writeLog = append(r.writeLog, node+"="+value)
	return nil
}

func (r *fenceGatedResource) currentHolder() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.holder
}

func (r *fenceGatedResource) writes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]string, len(r.writeLog))
	copy(cp, r.writeLog)
	return cp
}

// --- Silent-no-op fabric: the precise "false fenced" bluff trigger -----------

// silentNoOpPower is a PowerController whose PowerOff SUCCEEDS (returns nil) but
// does NOT actually transition the node: State() keeps reporting PoweredOn. This
// models the single most dangerous STONITH defect — a fence agent that reports
// success while the target is still alive. A driver that merely recorded
// "fenced" in a map (without confirming the transition) would let this through;
// the real NoOpAgent.Fence MUST catch it via its post-condition check and return
// ErrNotConfirmed.
type silentNoOpPower struct{}

func (silentNoOpPower) PowerOff(_ context.Context, _ string) error { return nil } // "success"...
func (silentNoOpPower) State(_ context.Context, _ string) (PowerState, error) {
	return PoweredOn, nil // ...but the node never actually died.
}

// TestSTONITHFalseFencedRejectedOnSilentNoOp is SAFETY PROPERTY #1 (fence
// confirmation). It drives a fence against a fabric that accepts the power-off
// command but leaves the node alive, and asserts:
//   - Fence returns ErrNotConfirmed (NOT a fake success).
//   - The fencer does NOT consider the target fenced.
//   - A fence-gated resource therefore still treats the target as a LIVE node:
//     the survivor cannot evict it, so no split-brain takeover occurs.
//
// ANTI-BLUFF: a map-only implementation that marked the node "fenced" on a
// nil-error PowerOff would report (true) here, the survivor would wrongly take
// over a still-alive node, and this test would fail. STONITH's positive-
// confirmation requirement is what makes the difference.
func TestSTONITHFalseFencedRejectedOnSilentNoOp(t *testing.T) {
	ctx := context.Background()
	const target = "node-silent"

	agent := NewNoOpAgent("silent", silentNoOpPower{})
	fencer, err := NewMultiLevelFencer(agent)
	if err != nil {
		t.Fatalf("NewMultiLevelFencer: %v", err)
	}

	conf, ferr := fencer.Fence(ctx, target)
	if !errors.Is(ferr, ErrNotConfirmed) {
		t.Fatalf("Fence on silent-no-op fabric: got err=%v conf=%+v, want ErrNotConfirmed and nil conf", ferr, conf)
	}
	if conf != nil {
		t.Fatalf("STONITH reported a Confirmation for an un-transitioned node: %+v (false-fenced == split-brain hazard)", conf)
	}

	// The authoritative fence view must NOT show the node fenced.
	fenced, err := fencer.IsFenced(ctx, target)
	if err != nil {
		t.Fatalf("IsFenced: %v", err)
	}
	if fenced {
		t.Fatal("fencer reports target fenced after an UNCONFIRMED fence — this is the false-fenced bug that causes split-brain")
	}

	// Sink-side consequence: a survivor must NOT be able to take over a resource
	// the (still-alive) target holds, because the target was never truly fenced.
	res := newFenceGatedResource(fencer)
	if err := res.acquire(ctx, target); err != nil {
		t.Fatalf("live target should still hold its resource: %v", err)
	}
	const survivor = "node-survivor"
	takeoverErr := res.acquire(ctx, survivor)
	if !errors.Is(takeoverErr, errHeldByOther) {
		t.Fatalf("survivor takeover of an UN-fenced node: got %v, want errHeldByOther (refusing to double-own)", takeoverErr)
	}

	t.Logf("EVIDENCE/anti-bluff: silent-no-op fence NOT confirmed (err=%v); fencer.IsFenced=false; "+
		"survivor takeover correctly REFUSED (%v) — no split-brain on an un-fenced node.", ferr, takeoverErr)
}

// TestSTONITHFencedNodeNeutralisedAndSurvivorTakesOver is the CORE anti-bluff
// test: SAFETY PROPERTY #2 (fenced node cannot act) + #3 (survivor safely takes
// over). It runs the full sequence an incident would:
//
//  1. node-A is the live holder and performs a protected write (baseline: it
//     genuinely can act while healthy).
//  2. node-A is FENCED through the real MultiLevelFencer and the fence is
//     CONFIRMED (positive ack, sink-side power state == powered-off).
//  3. The fenced node-A is NEUTRALISED: every protected action (write, and any
//     re-acquire) is REJECTED with errFenced — it genuinely cannot double-execute.
//  4. The SURVIVOR node-B safely takes over the resource (no conflict, because A
//     is confirmed fenced) and performs its own protected write.
//  5. The committed write log proves no interleaved double-write by the fenced
//     node occurred after the fence.
//
// ANTI-BLUFF — why "fenced node cannot act" BITES a map-only fake: if the
// implementation merely set fenced[node]=true in a map but the resource's gate
// did not actually consult the confirmed fence (or the fence was never truly
// confirmed), step 3's write by node-A would SUCCEED, appending a post-fence
// entry to writeLog while node-B is also writing — the split-brain double-write
// this test explicitly asserts MUST NOT happen.
func TestSTONITHFencedNodeNeutralisedAndSurvivorTakesOver(t *testing.T) {
	ctx := context.Background()
	const (
		nodeA = "node-a" // to be fenced
		nodeB = "node-b" // survivor
	)

	// Real power fabric shared by the agent; fencing genuinely flips state.
	fabric := NewInMemoryPower()
	agent := NewNoOpAgent("power", fabric)
	fencer, err := NewMultiLevelFencer(agent)
	if err != nil {
		t.Fatalf("NewMultiLevelFencer: %v", err)
	}
	res := newFenceGatedResource(fencer)

	// 1) node-A is the live, healthy holder and CAN act.
	if err := res.acquire(ctx, nodeA); err != nil {
		t.Fatalf("healthy node-A acquire: %v", err)
	}
	if err := res.write(ctx, nodeA, "pre-fence-1"); err != nil {
		t.Fatalf("healthy node-A write: %v", err)
	}
	t.Logf("EVIDENCE: healthy node-A acquired resource and wrote 'pre-fence-1' (proves the gate allows a LIVE holder).")

	// Sanity: before the fence, the survivor must NOT be able to steal a live
	// holder's resource (this guards against a vacuous test where takeover always
	// "works").
	if err := res.acquire(ctx, nodeB); !errors.Is(err, errHeldByOther) {
		t.Fatalf("pre-fence survivor acquire: got %v, want errHeldByOther (must not steal from a LIVE holder)", err)
	}

	// 2) FENCE node-A and require POSITIVE confirmation.
	conf, err := fencer.Fence(ctx, nodeA)
	if err != nil {
		t.Fatalf("Fence(node-A): %v", err)
	}
	if conf == nil || conf.Target != nodeA {
		t.Fatalf("bad confirmation: %+v", conf)
	}
	// Independent oracle: the real power fabric must show node-A powered-off.
	if st, serr := fabric.State(ctx, nodeA); serr != nil || st != PoweredOff {
		t.Fatalf("post-fence fabric state: got (%v,%v), want powered-off", st, serr)
	}
	if fenced, ferr := fencer.IsFenced(ctx, nodeA); ferr != nil || !fenced {
		t.Fatalf("fencer.IsFenced(node-A) after confirmed fence: got (%v,%v), want true", fenced, ferr)
	}
	t.Logf("EVIDENCE: node-A fence CONFIRMED by agent %q method %q; fabric state=powered-off; fencer.IsFenced=true.",
		conf.Agent, conf.Method)

	// 3) NEUTRALISATION: the fenced node-A can no longer perform protected
	// actions. A bluff that only marked a map would let these through.
	writeErr := res.write(ctx, nodeA, "GHOST-WRITE-after-fence")
	if !errors.Is(writeErr, errFenced) {
		t.Fatalf("FENCED node-A write was NOT rejected: got %v, want errFenced "+
			"(a fenced node that can still write is split-brain — the core anti-bluff failure)", writeErr)
	}
	reacquireErr := res.acquire(ctx, nodeA)
	if !errors.Is(reacquireErr, errFenced) {
		t.Fatalf("FENCED node-A re-acquire was NOT rejected: got %v, want errFenced", reacquireErr)
	}
	t.Logf("EVIDENCE/anti-bluff: fenced node-A protected write REJECTED (%v) and re-acquire REJECTED (%v) — "+
		"it genuinely cannot act. A map-only 'fenced' fake that still let node-A write would FAIL here.", writeErr, reacquireErr)

	// 4) SURVIVOR takeover: node-B safely claims the resource (A is confirmed
	// fenced, so there is no live conflict) and performs its own write.
	if err := res.acquire(ctx, nodeB); err != nil {
		t.Fatalf("survivor node-B takeover after confirmed fence: %v", err)
	}
	if got := res.currentHolder(); got != nodeB {
		t.Fatalf("holder after takeover = %q, want node-B", got)
	}
	if err := res.write(ctx, nodeB, "post-takeover-1"); err != nil {
		t.Fatalf("survivor node-B write: %v", err)
	}
	t.Logf("EVIDENCE: survivor node-B safely took over (holder=%q) and wrote 'post-takeover-1' — no split-brain.", nodeB)

	// 5) The committed write log proves NO post-fence write by node-A interleaved
	// with node-B's takeover. Exactly: [node-A=pre-fence-1, node-B=post-takeover-1].
	got := res.writes()
	want := []string{"node-a=pre-fence-1", "node-b=post-takeover-1"}
	if len(got) != len(want) {
		t.Fatalf("write log = %v, want %v (any extra entry == a fenced-node double-write)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("write log[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	// Belt-and-braces: assert no committed write is attributed to node-A AFTER the
	// pre-fence one (i.e. exactly one node-A write, and it is the first entry).
	nodeAWrites := 0
	for _, w := range got {
		if len(w) >= len(nodeA)+1 && w[:len(nodeA)+1] == nodeA+"=" {
			nodeAWrites++
		}
	}
	if nodeAWrites != 1 {
		t.Fatalf("node-A committed %d writes, want exactly 1 (its pre-fence write); any extra is a ghost double-write", nodeAWrites)
	}
	t.Logf("EVIDENCE: final write log = %v — exactly one node-A (pre-fence) write and one node-B (post-takeover) write; "+
		"the fenced node committed NO post-fence write. STONITH safety contract upheld.", got)
}

// TestSTONITHUnconfirmedFenceBlocksTakeover is SAFETY PROPERTY #4 (do NOT proceed
// on an unconfirmed fence). The API models fence FAILURE via FailingPowerController
// (PowerOff fails, node stays powered-on), so we exercise it: when the fence is
// NOT confirmed, the fencer must report the target NOT fenced and the survivor
// must be REFUSED takeover. Proceeding to take over an un-fenced node is the
// classic split-brain bug.
//
// (Property #4 is testable here because the public API DOES support simulating a
// fence failure via FailingPowerController, so no skip is needed.)
func TestSTONITHUnconfirmedFenceBlocksTakeover(t *testing.T) {
	ctx := context.Background()
	const (
		target   = "node-stuck"
		survivor = "node-survivor"
	)

	// A single-level fencer whose only agent CANNOT fence (power fabric
	// unreachable). The fence attempt fails; the node remains alive.
	agent := NewNoOpAgent("unreachable", &FailingPowerController{Err: errBMCUnreachable})
	fencer, err := NewMultiLevelFencer(agent)
	if err != nil {
		t.Fatalf("NewMultiLevelFencer: %v", err)
	}
	res := newFenceGatedResource(fencer)

	// The (still-live) target holds the resource.
	if err := res.acquire(ctx, target); err != nil {
		t.Fatalf("target acquire: %v", err)
	}

	// Attempt to fence — MUST fail (unconfirmed).
	conf, ferr := fencer.Fence(ctx, target)
	if ferr == nil {
		t.Fatalf("Fence unexpectedly succeeded on an unreachable fabric: conf=%+v", conf)
	}
	if conf != nil {
		t.Fatalf("got a Confirmation for an unconfirmed fence: %+v", conf)
	}
	// The fence must surface the underlying failure (the unreachable fabric). Note
	// this branch returns the PowerOff error directly; the ErrNotConfirmed wrapping
	// is exercised by the default FailingPowerController path (and the silent-no-op
	// test). The safety-critical fact here is simply: the fence did NOT succeed.
	if !errors.Is(ferr, errBMCUnreachable) {
		t.Fatalf("fence failure error: got %v, want wrapping the unreachable-fabric cause", ferr)
	}

	// The fencer must NOT consider the target fenced.
	if fenced, ierr := fencer.IsFenced(ctx, target); ierr != nil || fenced {
		t.Fatalf("IsFenced after unconfirmed fence: got (%v,%v), want (false,nil)", fenced, ierr)
	}

	// CRITICAL: the survivor must NOT proceed to take over. Doing so on an
	// unconfirmed fence is the split-brain bug this property guards.
	takeoverErr := res.acquire(ctx, survivor)
	if !errors.Is(takeoverErr, errHeldByOther) {
		t.Fatalf("survivor took over despite UNCONFIRMED fence: got %v, want errHeldByOther", takeoverErr)
	}
	if got := res.currentHolder(); got != target {
		t.Fatalf("holder changed to %q despite unconfirmed fence, want %q (no takeover allowed)", got, target)
	}

	t.Logf("EVIDENCE/anti-bluff: fence UNCONFIRMED (err=%v); fencer.IsFenced=false; survivor takeover REFUSED (%v); "+
		"holder remains %q — the system did NOT proceed on an unconfirmed fence (split-brain averted).",
		ferr, takeoverErr, target)
}
