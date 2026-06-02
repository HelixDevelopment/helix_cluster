package costrouter

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"
)

// runUUID returns a random run identifier for observability in test logs.
func runUUID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("runUUID: %v", err)
	}
	return hex.EncodeToString(b)
}

// minByLatency / minByCost are independent oracles that recompute the expected
// pick directly from the inputs (no hardcoded magic), with the SAME ID
// tie-break the implementation must use.
func minByLatency(ps []Provider) Provider {
	best := ps[0]
	for _, p := range ps[1:] {
		if p.LatencyMs < best.LatencyMs || (p.LatencyMs == best.LatencyMs && p.ID < best.ID) {
			best = p
		}
	}
	return best
}

func minByCost(ps []Provider) Provider {
	best := ps[0]
	for _, p := range ps[1:] {
		if p.CostPerHour < best.CostPerHour || (p.CostPerHour == best.CostPerHour && p.ID < best.ID) {
			best = p
		}
	}
	return best
}

// TestWorkloadTypeDrivesChoice covers CLOSURE clause 1.
//
// Over the SAME provider table where the cheapest provider is NOT the fastest,
// an Inference request must pick the lowest-latency provider and a Training
// request must pick the lowest-cost provider — and the two picks must DIFFER,
// proving the workload type (not a single fixed objective) drives selection.
//
// Mutation: if Route used cost for Inference (or latency for Training), the two
// picks would coincide (or be wrong vs the oracles) and this test fails.
func TestWorkloadTypeDrivesChoice(t *testing.T) {
	id := runUUID(t)

	// Shared table. "cheap-slow" is cheapest but slowest; "fast-pricey" is
	// fastest but most expensive. So cost-min != latency-min by construction.
	table := []Provider{
		{ID: "cheap-slow", CostPerHour: 1.00, LatencyMs: 90.0},
		{ID: "fast-pricey", CostPerHour: 9.00, LatencyMs: 10.0},
		{ID: "mid", CostPerHour: 4.00, LatencyMs: 50.0},
	}

	wantInfer := minByLatency(table) // expected: fast-pricey (10ms)
	wantTrain := minByCost(table)    // expected: cheap-slow ($1.00)

	t.Logf("run=%s BEFORE table=%+v wantInfer=%s wantTrain=%s", id, table, wantInfer.ID, wantTrain.ID)

	gotInfer, err := Route(Inference, table)
	if err != nil {
		t.Fatalf("run=%s Route(Inference) unexpected err: %v", id, err)
	}
	gotTrain, err := Route(Training, table)
	if err != nil {
		t.Fatalf("run=%s Route(Training) unexpected err: %v", id, err)
	}

	t.Logf("run=%s AFTER gotInfer=%s gotTrain=%s", id, gotInfer.ID, gotTrain.ID)

	if gotInfer.ID != wantInfer.ID {
		t.Fatalf("run=%s Inference: got %s, want lowest-latency %s", id, gotInfer.ID, wantInfer.ID)
	}
	if gotTrain.ID != wantTrain.ID {
		t.Fatalf("run=%s Training: got %s, want lowest-cost %s", id, gotTrain.ID, wantTrain.ID)
	}

	// The crux: the two picks must differ, proving type drives the objective.
	if gotInfer.ID == gotTrain.ID {
		t.Fatalf("run=%s picks did not differ (both %s): workload type is not driving the objective", id, gotInfer.ID)
	}

	// Sink-side cross-check: the Inference pick is strictly faster than the
	// Training pick, and the Training pick is strictly cheaper — confirming the
	// objectives are genuinely opposed on this table.
	if !(gotInfer.LatencyMs < gotTrain.LatencyMs) {
		t.Fatalf("run=%s expected Inference pick faster: %.1fms vs %.1fms", id, gotInfer.LatencyMs, gotTrain.LatencyMs)
	}
	if !(gotTrain.CostPerHour < gotInfer.CostPerHour) {
		t.Fatalf("run=%s expected Training pick cheaper: $%.2f vs $%.2f", id, gotTrain.CostPerHour, gotInfer.CostPerHour)
	}
}

// TestTieBreakDeterministicAndEmpty covers CLOSURE clause 2.
//
// Deterministic tie-break by ascending ID when objective values are equal, plus
// ErrNoProviders on an empty table.
//
// Mutation: if the tie-break picked the larger ID (or first-seen rather than
// smallest ID), the latency-tie and cost-tie assertions fail.
func TestTieBreakDeterministicAndEmpty(t *testing.T) {
	id := runUUID(t)

	// Empty table -> ErrNoProviders for both workload types.
	if _, err := Route(Inference, nil); !errors.Is(err, ErrNoProviders) {
		t.Fatalf("run=%s Route(Inference, nil): got %v, want ErrNoProviders", id, err)
	}
	if _, err := Route(Training, []Provider{}); !errors.Is(err, ErrNoProviders) {
		t.Fatalf("run=%s Route(Training, empty): got %v, want ErrNoProviders", id, err)
	}

	// Latency tie: two providers share the minimum latency; "p-a" < "p-z" wins.
	latencyTie := []Provider{
		{ID: "p-z", CostPerHour: 2.0, LatencyMs: 15.0},
		{ID: "p-a", CostPerHour: 8.0, LatencyMs: 15.0},
		{ID: "p-m", CostPerHour: 3.0, LatencyMs: 40.0},
	}
	t.Logf("run=%s BEFORE latencyTie=%+v", id, latencyTie)
	gotL, err := Route(Inference, latencyTie)
	if err != nil {
		t.Fatalf("run=%s Route(Inference, latencyTie): %v", id, err)
	}
	t.Logf("run=%s AFTER latencyTie pick=%s", id, gotL.ID)
	if gotL.ID != "p-a" {
		t.Fatalf("run=%s latency tie-break: got %s, want smallest ID p-a", id, gotL.ID)
	}

	// Cost tie: two providers share the minimum cost; "c-a" < "c-b" wins.
	costTie := []Provider{
		{ID: "c-b", CostPerHour: 5.0, LatencyMs: 20.0},
		{ID: "c-a", CostPerHour: 5.0, LatencyMs: 70.0},
		{ID: "c-c", CostPerHour: 6.0, LatencyMs: 10.0},
	}
	t.Logf("run=%s BEFORE costTie=%+v", id, costTie)
	gotC, err := Route(Training, costTie)
	if err != nil {
		t.Fatalf("run=%s Route(Training, costTie): %v", id, err)
	}
	t.Logf("run=%s AFTER costTie pick=%s", id, gotC.ID)
	if gotC.ID != "c-a" {
		t.Fatalf("run=%s cost tie-break: got %s, want smallest ID c-a", id, gotC.ID)
	}

	// Determinism: repeated routing over the same (unmodified) table is stable.
	for i := 0; i < 5; i++ {
		again, err := Route(Inference, latencyTie)
		if err != nil || again.ID != gotL.ID {
			t.Fatalf("run=%s non-deterministic latency route iter %d: %s/%v", id, i, again.ID, err)
		}
	}
}
