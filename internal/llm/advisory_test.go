package llm

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// deterministicAdvisor is a REAL in-repo Advisor: it is not a mock of an
// external LLM but a genuine, deterministic implementation of the Advisor seam
// that derives every advisory field from the Observation it is given. Tests
// assert sink-side on the persisted record, proving the engine ran the proposal
// end-to-end through this seam rather than fabricating a record. The advisor's
// risk grading is driven by the observed severity so we can exercise both the
// LOW (auto-applicable) and non-LOW (approval-gated) paths.
type deterministicAdvisor struct {
	kind AdvisoryKind
}

func (d deterministicAdvisor) Propose(ctx context.Context, obs Observation) (Advisory, error) {
	risk := RiskLow
	switch {
	case obs.Severity >= 0.9:
		risk = RiskCritical
	case obs.Severity >= 0.7:
		risk = RiskHigh
	case obs.Severity >= 0.4:
		risk = RiskMedium
	}
	// Rationale is a chain-of-thought string derived from the observation so we
	// can assert it was persisted verbatim (not a canned constant).
	rationale := fmt.Sprintf(
		"observed %s on %s at severity %.2f; therefore proposing %s action",
		obs.Signal, obs.Target, obs.Severity, d.kind,
	)
	return Advisory{
		Kind:       d.kind,
		Target:     obs.Target,
		Action:     fmt.Sprintf("apply %s to %s", d.kind, obs.Target),
		Rationale:  rationale,
		Confidence: 0.5 + obs.Severity/2, // in [0.5,1] for severity in [0,1]
		RiskLevel:  risk,
	}, nil
}

// errAdvisor always fails; used to prove Generate surfaces advisor errors rather
// than persisting a fabricated record.
type errAdvisor struct{}

func (errAdvisor) Propose(ctx context.Context, obs Observation) (Advisory, error) {
	return Advisory{}, fmt.Errorf("advisor unavailable")
}

// TestGenerate_PersistsPendingWithRationaleAndConfidence is the primary
// closure-criterion test: generate an advisory and assert it is persisted in
// PENDING with the rationale and confidence the Advisor produced — read back
// from the Store sink-side via Get/List, not from the Propose return value.
//
// Load-bearing: flipping the persisted State away from StatePending, or
// dropping the rationale/confidence on persistence, fails this test.
func TestGenerate_PersistsPendingWithRationaleAndConfidence(t *testing.T) {
	v := NewVerifier("node-7")
	s := NewStore(deterministicAdvisor{kind: KindScaling}, v)

	obs := Observation{Target: "node-7", Signal: "cpu_saturation", Severity: 0.3, Detail: "p99 climbing"}
	got, err := s.Generate(context.Background(), obs)
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if got.ID == "" {
		t.Fatalf("persisted advisory has no ID")
	}

	// Sink-side read: the record actually lives in the store.
	rec, ok := s.Get(got.ID)
	if !ok {
		t.Fatalf("advisory %s not found in store after Generate", got.ID)
	}
	if rec.State != StatePending {
		t.Fatalf("persisted state = %q, want PENDING", rec.State)
	}
	wantRationale := "observed cpu_saturation on node-7 at severity 0.30; therefore proposing SCALING action"
	if rec.Rationale != wantRationale {
		t.Fatalf("persisted rationale = %q, want %q", rec.Rationale, wantRationale)
	}
	// severity 0.3 -> confidence 0.5 + 0.15 = 0.65
	if rec.Confidence < 0.649 || rec.Confidence > 0.651 {
		t.Fatalf("persisted confidence = %v, want ~0.65", rec.Confidence)
	}
	if rec.RiskLevel != RiskLow {
		t.Fatalf("persisted risk = %q, want LOW for severity 0.3", rec.RiskLevel)
	}
	if rec.CreatedAt.IsZero() {
		t.Fatalf("persisted advisory has zero CreatedAt")
	}

	// And it is visible in the List feed.
	all := s.List()
	if len(all) != 1 || all[0].ID != got.ID {
		t.Fatalf("List = %+v, want exactly the one persisted advisory", all)
	}
}

// TestApply_NonLowRiskRequiresApproval proves the binding guarantee: a non-LOW
// advisory is NEVER auto-applied without an explicit approval. Apply must reject
// with ErrApprovalRequired while PENDING, then succeed only after Approve.
//
// Load-bearing: removing the approval gate in Apply (auto-applying a non-LOW
// advisory) fails this test on the first Apply call.
func TestApply_NonLowRiskRequiresApproval(t *testing.T) {
	v := NewVerifier("svc-payments")
	s := NewStore(deterministicAdvisor{kind: KindMigration}, v)

	// severity 0.95 -> CRITICAL risk.
	obs := Observation{Target: "svc-payments", Signal: "disk_failing", Severity: 0.95}
	adv, err := s.Generate(context.Background(), obs)
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if adv.RiskLevel != RiskCritical {
		t.Fatalf("risk = %q, want CRITICAL", adv.RiskLevel)
	}

	// Auto-apply MUST be refused while PENDING.
	if _, err := s.Apply(adv.ID); !IsApprovalRequired(err) {
		t.Fatalf("Apply of non-LOW PENDING advisory: err = %v, want ErrApprovalRequired", err)
	}
	// It must NOT have been applied.
	if rec, _ := s.Get(adv.ID); rec.State == StateApplied {
		t.Fatalf("non-LOW advisory was applied without approval; state=%q", rec.State)
	}

	// After explicit approval, Apply succeeds and the record is APPLIED.
	if err := s.Approve(adv.ID); err != nil {
		t.Fatalf("Approve returned error: %v", err)
	}
	applied, err := s.Apply(adv.ID)
	if err != nil {
		t.Fatalf("Apply after Approve returned error: %v", err)
	}
	if applied.State != StateApplied {
		t.Fatalf("post-apply state = %q, want APPLIED", applied.State)
	}
}

// TestApply_LowRiskAutoApplies proves the symmetric case: a LOW-risk advisory is
// auto-applicable without approval (the gate is risk-scoped, not blanket).
//
// Load-bearing: making Apply require approval for ALL risk levels fails here.
func TestApply_LowRiskAutoApplies(t *testing.T) {
	v := NewVerifier("node-1")
	s := NewStore(deterministicAdvisor{kind: KindOptimization}, v)

	obs := Observation{Target: "node-1", Signal: "idle_cache", Severity: 0.1}
	adv, err := s.Generate(context.Background(), obs)
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if adv.RiskLevel != RiskLow {
		t.Fatalf("risk = %q, want LOW", adv.RiskLevel)
	}
	applied, err := s.Apply(adv.ID)
	if err != nil {
		t.Fatalf("Apply of LOW advisory returned error: %v", err)
	}
	if applied.State != StateApplied {
		t.Fatalf("state = %q, want APPLIED", applied.State)
	}
}

// TestVerifier_RejectsHallucinatedTarget proves the mandatory LLMsVerifier gate
// REJECTS an action referencing a non-existent target. The advisor confidently
// proposes an action on "ghost-node" which is NOT a known cluster target;
// Generate must return a typed ErrVerificationFailed AND persist the advisory as
// REJECTED (never PENDING/applicable).
//
// Load-bearing: removing the known-target existence check in Verifier.Verify
// (accepting any target) fails this test — the advisory would be PENDING and
// err would be nil.
func TestVerifier_RejectsHallucinatedTarget(t *testing.T) {
	v := NewVerifier("node-1", "node-2") // ghost-node deliberately absent
	s := NewStore(deterministicAdvisor{kind: KindConfig}, v)

	obs := Observation{Target: "ghost-node", Signal: "phantom", Severity: 0.2}
	rec, err := s.Generate(context.Background(), obs)
	if !IsVerificationFailed(err) {
		t.Fatalf("Generate for hallucinated target: err = %v, want ErrVerificationFailed", err)
	}
	if rec == nil {
		t.Fatalf("expected the rejected record to be returned for audit")
	}
	if rec.State != StateRejected {
		t.Fatalf("rejected advisory state = %q, want REJECTED", rec.State)
	}
	if !strings.Contains(err.Error(), "hallucinated") || !strings.Contains(err.Error(), "ghost-node") {
		t.Fatalf("verifier reason = %q, want it to cite the hallucinated ghost-node", err.Error())
	}

	// A rejected advisory must never be applicable.
	if _, aerr := s.Apply(rec.ID); aerr == nil {
		t.Fatalf("Apply of a verifier-rejected advisory must fail, got nil")
	}
}

// TestVerifier_RejectsConstraintViolation proves the verifier enforces named
// constitutional constraints beyond target existence: here a constraint forbids
// MIGRATION away from a protected target. The advisory references a REAL target
// (so it passes the existence check) but violates the constraint.
//
// Load-bearing: dropping constraint evaluation from Verify accepts the advisory
// and fails this test.
func TestVerifier_RejectsConstraintViolation(t *testing.T) {
	v := NewVerifier("primary-db")
	v.AddConstraint("no-migrate-primary", func(a *Advisory) string {
		if a.Kind == KindMigration && a.Target == "primary-db" {
			return "migration of the primary database is forbidden"
		}
		return ""
	})
	s := NewStore(deterministicAdvisor{kind: KindMigration}, v)

	obs := Observation{Target: "primary-db", Signal: "rebalance", Severity: 0.5}
	rec, err := s.Generate(context.Background(), obs)
	if !IsVerificationFailed(err) {
		t.Fatalf("Generate violating constraint: err = %v, want ErrVerificationFailed", err)
	}
	if rec.State != StateRejected {
		t.Fatalf("state = %q, want REJECTED", rec.State)
	}
	if !strings.Contains(err.Error(), "no-migrate-primary") {
		t.Fatalf("reason = %q, want it to name the violated constraint", err.Error())
	}
}

// TestGenerate_AdvisorErrorNotPersisted proves a failing Advisor surfaces the
// error and persists nothing (no fabricated record).
func TestGenerate_AdvisorErrorNotPersisted(t *testing.T) {
	s := NewStore(errAdvisor{}, NewVerifier("node-1"))
	rec, err := s.Generate(context.Background(), Observation{Target: "node-1", Severity: 0.1})
	if err == nil {
		t.Fatalf("expected error from failing advisor")
	}
	if rec != nil {
		t.Fatalf("no record should be returned on advisor failure, got %+v", rec)
	}
	if len(s.List()) != 0 {
		t.Fatalf("store should be empty after advisor failure, got %d records", len(s.List()))
	}
}

// TestApprove_OnlyPending proves Approve refuses non-PENDING advisories, so an
// already-applied or rejected advisory cannot be retroactively approved.
func TestApprove_OnlyPending(t *testing.T) {
	v := NewVerifier("node-1")
	s := NewStore(deterministicAdvisor{kind: KindAlert}, v)
	adv, err := s.Generate(context.Background(), Observation{Target: "node-1", Severity: 0.1})
	if err != nil {
		t.Fatalf("Generate error: %v", err)
	}
	if _, err := s.Apply(adv.ID); err != nil { // LOW -> applies
		t.Fatalf("Apply error: %v", err)
	}
	if err := s.Approve(adv.ID); err == nil {
		t.Fatalf("Approve of an APPLIED advisory must fail, got nil")
	}
}
