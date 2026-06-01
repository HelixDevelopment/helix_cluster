//go:build integration

package policy

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestIntegrationDecisionLogAuthzDenyScopePolicy exercises the full HXC-1220
// feature end-to-end against the real OPA rego engine with a per-run UUID:
//
//  1. The deny-scope authz policy correctly DENIES scheduler:write.
//  2. The deny-scope authz policy correctly ALLOWS scheduler:read.
//  3. The in-memory decision log captures BOTH entries with the per-run UUID.
//  4. RunUUID, Allow, PolicyName, and At are correct on each log entry.
//
// This test MUST hard-FAIL on any wrong decision — it is not a smoke test.
// OPA evaluation is deterministic and in-process; no external service skip.
//
// §1.1 mutation scenarios (documented, not executed here):
//   - Skip appendDecisionLog call in Evaluate → DecisionLog() len=0 → fails.
//   - Invert Allow in the appended log entry → log[0].Allow=true for
//     scheduler:write → the Allow assertion fails.
//   - Replace "run_uuid" with "run_id" in extractRunUUID (primary key) →
//     RunUUID extraction fails when input only has run_uuid → fails.
func TestIntegrationDecisionLogAuthzDenyScopePolicy(t *testing.T) {
	runUUID := uuid.New().String()
	ctx := context.Background()

	e := NewEngineWithOptions(WithDecisionLog())
	if err := e.LoadPolicy(ctx, "authz", authzDenyScopeRego); err != nil {
		t.Fatalf("LoadPolicy authz: %v", err)
	}

	// --- DENY: scheduler:write ---
	denyInput := map[string]interface{}{
		"run_uuid": runUUID,
		"scope":    "scheduler:write",
	}
	dDeny, err := e.Evaluate(ctx, "authz", denyInput)
	if err != nil {
		t.Fatalf("Evaluate deny: %v", err)
	}
	if dDeny.Allow {
		t.Fatalf("DENY: Allow=true for scope=scheduler:write — rego allow rule must NOT fire for write")
	}
	if dDeny.PolicyName != "authz" {
		t.Fatalf("DENY: PolicyName=%q, want %q", dDeny.PolicyName, "authz")
	}

	// --- ALLOW: scheduler:read ---
	allowInput := map[string]interface{}{
		"run_uuid": runUUID,
		"scope":    "scheduler:read",
	}
	dAllow, err := e.Evaluate(ctx, "authz", allowInput)
	if err != nil {
		t.Fatalf("Evaluate allow: %v", err)
	}
	if !dAllow.Allow {
		t.Fatalf("ALLOW: Allow=false for scope=scheduler:read — rego allow rule MUST fire for read")
	}
	if dAllow.PolicyName != "authz" {
		t.Fatalf("ALLOW: PolicyName=%q, want %q", dAllow.PolicyName, "authz")
	}

	// --- Sink-side: DecisionLog must capture both entries ---
	log := e.DecisionLog()
	if len(log) != 2 {
		t.Fatalf("DecisionLog len=%d, want 2 (deny + allow)", len(log))
	}

	// First entry: DENY for scheduler:write.
	e0 := log[0]
	if e0.RunUUID != runUUID {
		t.Fatalf("log[0].RunUUID=%q, want %q (run_uuid must round-trip)", e0.RunUUID, runUUID)
	}
	if e0.Allow {
		t.Fatalf("log[0].Allow=true, want false (scheduler:write must be denied)")
	}
	if e0.PolicyName != "authz" {
		t.Fatalf("log[0].PolicyName=%q, want %q", e0.PolicyName, "authz")
	}
	if e0.At.IsZero() {
		t.Fatal("log[0].At must not be zero")
	}
	if !strings.Contains(e0.Reason, "deny") && !strings.Contains(e0.Reason, "authz") {
		t.Fatalf("log[0].Reason=%q expected to mention deny or authz", e0.Reason)
	}

	// Second entry: ALLOW for scheduler:read.
	e1 := log[1]
	if e1.RunUUID != runUUID {
		t.Fatalf("log[1].RunUUID=%q, want %q", e1.RunUUID, runUUID)
	}
	if !e1.Allow {
		t.Fatalf("log[1].Allow=false, want true (scheduler:read must be allowed)")
	}
	if e1.PolicyName != "authz" {
		t.Fatalf("log[1].PolicyName=%q, want %q", e1.PolicyName, "authz")
	}
	if e1.At.IsZero() {
		t.Fatal("log[1].At must not be zero")
	}

	// Chronological order.
	if e1.At.Before(e0.At) {
		t.Fatalf("log[1].At (%v) is before log[0].At (%v) — entries must be in order", e1.At, e0.At)
	}

	t.Logf("integration run_uuid=%s: deny=%v allow=%v log_len=%d OK",
		runUUID, e0.Allow, e1.Allow, len(log))
}

// TestIntegrationDecisionLogExternalSinkRoundTrip proves that a caller-provided
// external SinkFunc receives each entry with the correct per-run UUID and Allow
// flag, independently of the in-memory ring.
func TestIntegrationDecisionLogExternalSinkRoundTrip(t *testing.T) {
	runUUID := uuid.New().String()
	ctx := context.Background()

	var collected []DecisionLogEntry
	e := NewEngineWithOptions(WithDecisionLogSink(func(entry DecisionLogEntry) {
		collected = append(collected, entry)
	}))
	if err := e.LoadPolicy(ctx, "authz", authzDenyScopeRego); err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}

	// Write → deny.
	if _, err := e.Evaluate(ctx, "authz", map[string]interface{}{
		"run_uuid": runUUID,
		"scope":    "scheduler:write",
	}); err != nil {
		t.Fatalf("Evaluate write: %v", err)
	}
	// Read → allow.
	if _, err := e.Evaluate(ctx, "authz", map[string]interface{}{
		"run_uuid": runUUID,
		"scope":    "scheduler:read",
	}); err != nil {
		t.Fatalf("Evaluate read: %v", err)
	}

	if len(collected) != 2 {
		t.Fatalf("external sink collected len=%d, want 2", len(collected))
	}
	if collected[0].RunUUID != runUUID {
		t.Fatalf("collected[0].RunUUID=%q, want %q", collected[0].RunUUID, runUUID)
	}
	if collected[0].Allow {
		t.Fatal("collected[0].Allow=true, want false for scheduler:write")
	}
	if !collected[1].Allow {
		t.Fatal("collected[1].Allow=false, want true for scheduler:read")
	}

	t.Logf("integration external-sink run_uuid=%s: 2 entries collected OK", runUUID)
}
