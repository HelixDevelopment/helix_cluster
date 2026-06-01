package policy

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// authzDenyScopeRego is the deny-scope Rego policy for HXC-1220.
// It allows scheduler:read and denies everything else (default deny).
const authzDenyScopeRego = `
package authz

default allow = false

allow {
	input.scope == "scheduler:read"
}
`

// ---------------------------------------------------------------------------
// HXC-1220: DecisionLogEntry type and Engine decision-log sink
// ---------------------------------------------------------------------------

// TestDecisionLogEntryFields verifies that DecisionLogEntry has all required
// fields: RunUUID, PolicyName, Allow, Reason, At.
//
// Mutation proof: remove the At field from DecisionLogEntry → this test fails
// to compile (field reference is missing).
func TestDecisionLogEntryFields(t *testing.T) {
	entry := DecisionLogEntry{
		RunUUID:    "test-uuid",
		PolicyName: "authz",
		Allow:      true,
		Reason:     "test reason",
		At:         time.Now(),
	}
	if entry.RunUUID == "" {
		t.Fatal("RunUUID must not be empty")
	}
	if entry.PolicyName == "" {
		t.Fatal("PolicyName must not be empty")
	}
	if entry.At.IsZero() {
		t.Fatal("At must not be zero")
	}
}

// TestDecisionLogCapturesDenyAndAllow is the primary HXC-1220 test.
// It loads the deny-scope authz policy, evaluates a DENY (scheduler:write) and
// an ALLOW (scheduler:read), each carrying a per-run UUID in input.run_uuid,
// and asserts:
//   1. The OPA engine returns the correct Allow flag for each.
//   2. DecisionLog() captures BOTH entries in order.
//   3. Each entry has the correct RunUUID, Allow flag, and PolicyName.
//
// Mutation proof: if the decision-log append is skipped → DecisionLog() length
// is 0 → the length assertion fails.
// Mutation proof 2: if Allow is inverted in the log entry → the first entry's
// Allow is true for scheduler:write → the Allow assertion fails.
func TestDecisionLogCapturesDenyAndAllow(t *testing.T) {
	runUUID := uuid.New().String()

	e := NewEngineWithOptions(WithDecisionLog())

	if err := e.LoadPolicy(bg(), "authz", authzDenyScopeRego); err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}

	// DENY: scheduler:write is not in the allow list.
	denyInput := map[string]interface{}{
		"run_uuid": runUUID,
		"scope":    "scheduler:write",
	}
	dDeny, err := e.Evaluate(bg(), "authz", denyInput)
	if err != nil {
		t.Fatalf("Evaluate deny: %v", err)
	}
	if dDeny.Allow {
		t.Fatalf("DENY case: Allow=true for scope=scheduler:write, want false")
	}

	// ALLOW: scheduler:read is explicitly allowed.
	allowInput := map[string]interface{}{
		"run_uuid": runUUID,
		"scope":    "scheduler:read",
	}
	dAllow, err := e.Evaluate(bg(), "authz", allowInput)
	if err != nil {
		t.Fatalf("Evaluate allow: %v", err)
	}
	if !dAllow.Allow {
		t.Fatalf("ALLOW case: Allow=false for scope=scheduler:read, want true")
	}

	// Sink-side: DecisionLog must capture both entries.
	log := e.DecisionLog()
	if len(log) != 2 {
		t.Fatalf("DecisionLog len=%d, want 2 (one deny + one allow)", len(log))
	}

	// First entry: the DENY for scheduler:write.
	e0 := log[0]
	if e0.RunUUID != runUUID {
		t.Fatalf("log[0].RunUUID=%q, want %q", e0.RunUUID, runUUID)
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

	// Second entry: the ALLOW for scheduler:read.
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

	// Entries must be in chronological order (not future-backdated).
	if e1.At.Before(e0.At) {
		t.Fatalf("log[1].At (%v) is before log[0].At (%v), want chronological order",
			e1.At, e0.At)
	}
}

// TestDecisionLogRunUUIDExtractedFromInput verifies that run_uuid is extracted
// from input.run_uuid (primary key) when present.
//
// Mutation proof: if run_uuid extraction uses run_id instead of run_uuid → the
// extracted UUID does not match runUUID → the RunUUID assertion fails.
func TestDecisionLogRunUUIDExtractedFromInput(t *testing.T) {
	runUUID := uuid.New().String()

	e := NewEngineWithOptions(WithDecisionLog())
	if err := e.LoadPolicy(bg(), "authz", authzDenyScopeRego); err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}

	_, err := e.Evaluate(bg(), "authz", map[string]interface{}{
		"run_uuid": runUUID,
		"scope":    "scheduler:read",
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	log := e.DecisionLog()
	if len(log) != 1 {
		t.Fatalf("DecisionLog len=%d, want 1", len(log))
	}
	if log[0].RunUUID != runUUID {
		t.Fatalf("RunUUID=%q, want %q", log[0].RunUUID, runUUID)
	}
}

// TestDecisionLogRunIDFallback verifies that run_id is used as a fallback when
// run_uuid is absent.
//
// Mutation proof: remove the run_id fallback branch → RunUUID is empty when
// only run_id is provided → the assertion fails.
func TestDecisionLogRunIDFallback(t *testing.T) {
	runID := uuid.New().String()

	e := NewEngineWithOptions(WithDecisionLog())
	if err := e.LoadPolicy(bg(), "authz", authzDenyScopeRego); err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}

	_, err := e.Evaluate(bg(), "authz", map[string]interface{}{
		"run_id": runID, // no run_uuid key — fallback path
		"scope":  "scheduler:read",
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	log := e.DecisionLog()
	if len(log) != 1 {
		t.Fatalf("DecisionLog len=%d, want 1", len(log))
	}
	if log[0].RunUUID != runID {
		t.Fatalf("RunUUID=%q, want %q (run_id fallback)", log[0].RunUUID, runID)
	}
}

// TestDecisionLogDisabledByDefault verifies that a plain NewEngine() engine
// does NOT accumulate decision-log entries (opt-in only).
//
// Mutation proof: if all engines always record to an internal log → DecisionLog()
// returns a non-empty slice on a vanilla engine → the length assertion fails.
func TestDecisionLogDisabledByDefault(t *testing.T) {
	e := NewEngine() // plain engine — no WithDecisionLog()
	if err := e.LoadPolicy(bg(), "authz", authzDenyScopeRego); err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}

	_, err := e.Evaluate(bg(), "authz", map[string]interface{}{
		"run_uuid": uuid.New().String(),
		"scope":    "scheduler:write",
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	log := e.DecisionLog()
	if len(log) != 0 {
		t.Fatalf("DecisionLog len=%d on plain engine, want 0 (log disabled by default)",
			len(log))
	}
}

// TestDecisionLogWithExternalSink verifies the WithDecisionLog(sink) variant
// where the caller provides an explicit sink channel/slice.
//
// Mutation proof: if the external sink is not called → sink slice stays empty
// → the length assertion fails.
func TestDecisionLogWithExternalSink(t *testing.T) {
	var sink []DecisionLogEntry
	var sinkMu = new(sinkCollector)

	e := NewEngineWithOptions(WithDecisionLogSink(func(entry DecisionLogEntry) {
		sinkMu.add(entry)
	}))
	if err := e.LoadPolicy(bg(), "authz", authzDenyScopeRego); err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}

	runUUID := uuid.New().String()
	_, err := e.Evaluate(bg(), "authz", map[string]interface{}{
		"run_uuid": runUUID,
		"scope":    "scheduler:write",
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	sink = sinkMu.entries()
	if len(sink) != 1 {
		t.Fatalf("external sink len=%d, want 1", len(sink))
	}
	if sink[0].RunUUID != runUUID {
		t.Fatalf("sink[0].RunUUID=%q, want %q", sink[0].RunUUID, runUUID)
	}
	if sink[0].Allow {
		t.Fatal("sink[0].Allow=true, want false for scheduler:write")
	}
}

// TestAuthzDenyScopePolicyCorrectness verifies the Rego policy itself in
// isolation — independent of the decision log — to prove the engine evaluates
// scope-based rules correctly.
//
// Mutation proof: change `input.scope == "scheduler:read"` to
// `input.scope == "scheduler:write"` → allow fires for write, deny for read →
// both assertions flip and the test fails.
func TestAuthzDenyScopePolicyCorrectness(t *testing.T) {
	e := NewEngine()
	if err := e.LoadPolicy(bg(), "authz", authzDenyScopeRego); err != nil {
		t.Fatalf("LoadPolicy: %v", err)
	}

	cases := []struct {
		scope     string
		wantAllow bool
	}{
		{"scheduler:read", true},
		{"scheduler:write", false},
		{"scheduler:admin", false},
		{"", false},
	}
	for _, c := range cases {
		d, err := e.Evaluate(bg(), "authz", map[string]interface{}{"scope": c.scope})
		if err != nil {
			t.Fatalf("scope=%q Evaluate: %v", c.scope, err)
		}
		if d.Allow != c.wantAllow {
			t.Fatalf("scope=%q: Allow=%v, want %v", c.scope, d.Allow, c.wantAllow)
		}
	}
}

// ---------------------------------------------------------------------------
// Helper: thread-safe sink collector for TestDecisionLogWithExternalSink
// ---------------------------------------------------------------------------

// sinkCollector is a simple thread-safe accumulator for DecisionLogEntry values
// used in tests that pass an external sink function to WithDecisionLogSink.
type sinkCollector struct {
	mu   sync.RWMutex
	buf  []DecisionLogEntry
}

func (s *sinkCollector) add(e DecisionLogEntry) {
	s.mu.Lock()
	s.buf = append(s.buf, e)
	s.mu.Unlock()
}

func (s *sinkCollector) entries() []DecisionLogEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]DecisionLogEntry, len(s.buf))
	copy(out, s.buf)
	return out
}
