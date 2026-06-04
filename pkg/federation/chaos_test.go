//go:build chaos

package federation

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/swim"
)

// chaos_test.go exercises cross-cell suspicion propagation under a faulty
// inter-cell relay (HXC-937, Constitution §11.4.85). The GatewayRelay seam is
// the real cross-cell transport injection point; here we wrap it with a fault
// model that drops / delays / reorders Forward calls (modeling a partitioned or
// lossy cell-to-cell link). Two CrossCellSuspicion tables stand in for two
// cells; the chaos relay bridges them. We assert the REAL convergence invariant:
// once the link heals and all observations are re-driven, both cells' suspicion
// tables agree on the final (state, incarnation) for every member — no member is
// left stuck at a stale state, and a stale (lower-incarnation) update never
// "wins" even when delivered out of order.
//
// Run with: go test -tags chaos ./pkg/federation/...

// chaosRelay is a fault-injecting GatewayRelay. While partitioned it BUFFERS
// (does not deliver) forwarded events and returns an error to the caller (the
// honest "not delivered" contract from GatewayRelay.Forward). On heal it replays
// the buffered events into the peer cell — optionally REORDERED — so we can prove
// monotonic-incarnation conflict resolution survives reordering.
type chaosRelay struct {
	mu          sync.Mutex
	peer        *CrossCellSuspicion // the OTHER cell that should receive forwards
	partitioned bool
	buffer      []SuspicionEvent
	delivered   int
	failed      int
}

func (r *chaosRelay) Forward(ctx context.Context, ev SuspicionEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.partitioned {
		// Link down: buffer for later replay and report non-delivery (honest).
		r.buffer = append(r.buffer, ev)
		r.failed++
		return fmt.Errorf("chaosRelay: link partitioned, event for %s/%s buffered", ev.CellID, ev.MemberID)
	}
	r.deliverLocked(ev)
	return nil
}

// deliverLocked applies ev to the peer cell's table. The peer applies it through
// its own Observe path, but with a NO-OP relay so it doesn't recurse forever.
func (r *chaosRelay) deliverLocked(ev SuspicionEvent) {
	if r.peer != nil {
		_, _ = r.peer.Observe(context.Background(), ev)
	}
	r.delivered++
}

// healAndReplay reconnects the link and replays buffered events in the given
// order (caller may reverse the slice to simulate reordering).
func (r *chaosRelay) healAndReplay(reorder bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.partitioned = false
	buf := r.buffer
	r.buffer = nil
	if reorder {
		for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
			buf[i], buf[j] = buf[j], buf[i]
		}
	}
	for _, ev := range buf {
		r.deliverLocked(ev)
	}
}

func (r *chaosRelay) partition() {
	r.mu.Lock()
	r.partitioned = true
	r.mu.Unlock()
}

// noopRelay lets the peer cell apply forwarded events without re-forwarding.
type noopRelay struct{}

func (noopRelay) Forward(context.Context, SuspicionEvent) error { return nil }

// TestChaosCrossCellPartitionHealConverges drops the inter-cell relay during a
// burst of suspicion observations, then heals and replays. SINK-SIDE INVARIANT:
// after heal, the remote cell's suspicion table converges to the same final
// (state, incarnation) as the originating cell for every observed member.
func TestChaosCrossCellPartitionHealConverges(t *testing.T) {
	// cellB is the remote cell that receives forwards via the relay.
	cellB := NewCrossCellSuspicion("cell-b", noopRelay{})
	relay := &chaosRelay{peer: cellB}
	cellA := NewCrossCellSuspicion("cell-a", relay)

	type obs struct {
		member string
		state  swim.MemberState
		inc    uint32
	}
	// A monotonic escalation per member: Alive(1) -> Suspect(2) -> Dead(3).
	plan := []obs{
		{"m1", swim.StateAlive, 1},
		{"m2", swim.StateAlive, 1},
		{"m1", swim.StateSuspect, 2},
		{"m2", swim.StateSuspect, 2},
		{"m1", swim.StateDead, 3},
		{"m2", swim.StateDead, 3},
	}

	// FAULT: partition the inter-cell link before the escalation burst.
	relay.partition()
	for _, o := range plan {
		// Observe on cell A always updates A's LOCAL state (Observe updates local
		// even when Forward fails). The relay buffers the forward.
		changed, err := cellA.Observe(context.Background(), SuspicionEvent{
			CellID: "cell-a", MemberID: o.member, State: o.state, Incarnation: o.inc,
		})
		if !changed {
			t.Fatalf("observe %s inc=%d should be a change", o.member, o.inc)
		}
		// During partition Forward MUST report non-delivery (honest error seam).
		if err == nil {
			t.Fatalf("observe %s inc=%d: expected partition error from relay, got nil", o.member, o.inc)
		}
	}

	// Prove the fault had a real effect: cell B has NOT yet seen the escalations.
	if _, _, known := cellB.State("cell-a", "m1"); known {
		t.Fatal("cell B saw m1 during partition — fault was not actually exercising the lost link")
	}
	if relay.failed == 0 {
		t.Fatal("relay recorded zero failed forwards — partition fault never triggered")
	}

	// HEAL + replay REORDERED to stress monotonic conflict resolution.
	relay.healAndReplay(true)

	// SINK-SIDE INVARIANT: both cells agree on final state for every member.
	for _, member := range []string{"m1", "m2"} {
		sA, iA, okA := cellA.State("cell-a", member)
		sB, iB, okB := cellB.State("cell-a", member)
		if !okA || !okB {
			t.Fatalf("member %s: convergence failed (knownA=%v knownB=%v)", member, okA, okB)
		}
		if sA != sB || iA != iB {
			t.Fatalf("member %s diverged after heal: A=(%v,%d) B=(%v,%d)", member, sA, iA, sB, iB)
		}
		if sB != swim.StateDead || iB != 3 {
			t.Fatalf("member %s did not converge to final Dead/inc=3: got (%v,%d)", member, sB, iB)
		}
	}
}

// TestChaosStaleReorderDoesNotRegress proves that even when the healed relay
// replays events OUT OF ORDER, a stale lower-incarnation observation can never
// overwrite a newer one on the remote cell. This is the safety half of
// convergence: reordering must not corrupt the converged state.
func TestChaosStaleReorderDoesNotRegress(t *testing.T) {
	cellB := NewCrossCellSuspicion("cell-b", noopRelay{})
	relay := &chaosRelay{peer: cellB}
	cellA := NewCrossCellSuspicion("cell-a", relay)

	relay.partition()
	// Observe newest-last on A: Alive@1 then Dead@5. Buffer order = [Alive@1, Dead@5].
	for _, ev := range []SuspicionEvent{
		{CellID: "cell-a", MemberID: "m", State: swim.StateAlive, Incarnation: 1},
		{CellID: "cell-a", MemberID: "m", State: swim.StateDead, Incarnation: 5},
	} {
		if _, err := cellA.Observe(context.Background(), ev); err == nil {
			t.Fatalf("expected partition error during buffering for inc=%d", ev.Incarnation)
		}
	}

	// Heal + replay REVERSED => B sees Dead@5 FIRST, then the stale Alive@1.
	relay.healAndReplay(true)

	// SINK-SIDE INVARIANT: the stale Alive@1 must NOT regress B's state. B must
	// hold Dead@5 — the highest incarnation wins regardless of arrival order.
	sB, iB, known := cellB.State("cell-a", "m")
	if !known {
		t.Fatal("cell B never received member m")
	}
	if sB != swim.StateDead || iB != 5 {
		t.Fatalf("stale reordered Alive@1 regressed B: got (%v,%d), want (Dead,5)", sB, iB)
	}
}
