package agentprovision

import (
	"fmt"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/pkg/ratelimit"
)

// newZeroRefillLimiter builds a PerKeyLimiter whose per-key buckets hold
// exactly capacity tokens and never refill (refillPerSec == 0).  This makes
// token exhaustion deterministic without any sleep.
func newZeroRefillLimiter(capacity int) *ratelimit.PerKeyLimiter {
	return ratelimit.NewPerKeyLimiter(func() interface {
		Allow() bool
		AllowN(int) bool
	} {
		return ratelimit.NewTokenBucket(capacity, 0)
	})
}

// ─── Rate limit tests ──────────────────────────────────────────────────────

// TestRateLimit_DenyOnExhaustion verifies that the (capacity+1)th Admit for
// the same user is denied by the rate limiter.
//
// Anti-bluff: if the rate-limit check inside Admit were removed, the limiter
// bucket would silently allow calls beyond capacity and the assertion
// `admitted == false` with reason "rate limited" would fail.
func TestRateLimit_DenyOnExhaustion(t *testing.T) {
	const cap = 5 // per-user bucket capacity
	limiter := newZeroRefillLimiter(cap)
	// Use a large node cap so the node-capacity check never fires.
	aa := NewAgentAdmission(1000, limiter)

	user := "user-alice"
	node := "node-1"

	for i := 0; i < cap; i++ {
		a := Agent{ID: fmt.Sprintf("agent-%d", i), UserID: user}
		admitted, reason := aa.Admit(node, a)
		if !admitted {
			t.Fatalf("admit %d: expected admitted=true, got reason=%q", i, reason)
		}
	}

	// The (cap+1)th attempt must be denied by the rate limiter.
	extra := Agent{ID: "agent-extra", UserID: user}
	admitted, reason := aa.Admit(node, extra)
	if admitted {
		t.Fatalf("expected admitted=false for exhausted user bucket, but got admitted=true")
	}
	if reason != fmt.Sprintf("rate limited: %s", user) {
		t.Fatalf("expected reason %q, got %q", fmt.Sprintf("rate limited: %s", user), reason)
	}
}

// TestRateLimit_DifferentUserIsIndependent verifies that exhausting one user's
// bucket does not affect another user's bucket.
func TestRateLimit_DifferentUserIsIndependent(t *testing.T) {
	const cap = 2
	limiter := newZeroRefillLimiter(cap)
	aa := NewAgentAdmission(1000, limiter)

	node := "node-1"

	// Exhaust user-a.
	for i := 0; i < cap; i++ {
		aa.Admit(node, Agent{ID: fmt.Sprintf("a-%d", i), UserID: "user-a"}) //nolint:errcheck
	}
	admitted, _ := aa.Admit(node, Agent{ID: "a-extra", UserID: "user-a"})
	if admitted {
		t.Fatal("user-a should be rate limited")
	}

	// user-b must still be admitted.
	admitted, reason := aa.Admit(node, Agent{ID: "b-0", UserID: "user-b"})
	if !admitted {
		t.Fatalf("user-b should not be rate limited, got reason=%q", reason)
	}
}

// ─── Cap + queue tests ─────────────────────────────────────────────────────

// TestCapQueue_QueueOnFull verifies the cap-and-queue behavior:
//
//  1. cap=2: first two agents are admitted.
//  2. Third agent is queued (admitted=false, QueueLen==1).
//  3. After Complete, LiveCount drops and QueueLen drops (slot popped).
//
// Anti-bluff: if the cap check inside Admit were removed, the 3rd agent would
// be admitted (admitted=true) and the assertion `admitted == false` would fail.
func TestCapQueue_QueueOnFull(t *testing.T) {
	const nodeCap = 2
	// Give users a large budget so the rate limiter never fires.
	limiter := newZeroRefillLimiter(1000)
	aa := NewAgentAdmission(nodeCap, limiter)

	node := "node-x"
	user := "user-bob"

	a1 := Agent{ID: "ag-1", UserID: user}
	a2 := Agent{ID: "ag-2", UserID: user}
	a3 := Agent{ID: "ag-3", UserID: user}

	// Admit two agents — both should succeed.
	ok1, r1 := aa.Admit(node, a1)
	if !ok1 {
		t.Fatalf("agent 1 should be admitted, reason=%q", r1)
	}
	ok2, r2 := aa.Admit(node, a2)
	if !ok2 {
		t.Fatalf("agent 2 should be admitted, reason=%q", r2)
	}

	if lc := aa.LiveCount(node); lc != 2 {
		t.Fatalf("LiveCount want 2, got %d", lc)
	}

	// Third agent must be queued.
	ok3, r3 := aa.Admit(node, a3)
	if ok3 {
		t.Fatal("agent 3 should NOT be admitted (cap reached), but got admitted=true")
	}
	if r3 != "queued: cap reached" {
		t.Fatalf("expected reason %q, got %q", "queued: cap reached", r3)
	}
	if ql := aa.QueueLen(node); ql != 1 {
		t.Fatalf("QueueLen want 1, got %d", ql)
	}

	// Complete the first agent → live count must drop and queue must drain.
	aa.Complete(node, a1.ID)

	if lc := aa.LiveCount(node); lc != 1 {
		t.Fatalf("LiveCount after Complete want 1, got %d", lc)
	}
	if ql := aa.QueueLen(node); ql != 0 {
		t.Fatalf("QueueLen after Complete want 0, got %d", ql)
	}
}

// TestCapQueue_CompleteFreeCapacity confirms that Complete decrements LiveCount.
//
// Anti-bluff: if Complete did not decrement the live counter, LiveCount would
// remain 1 after completion, failing the == 0 assertion.
func TestCapQueue_CompleteFreeCapacity(t *testing.T) {
	limiter := newZeroRefillLimiter(1000)
	aa := NewAgentAdmission(1, limiter)

	node := "node-y"
	a := Agent{ID: "sole-agent", UserID: "user-c"}

	admitted, _ := aa.Admit(node, a)
	if !admitted {
		t.Fatal("single agent should be admitted on empty node")
	}
	if lc := aa.LiveCount(node); lc != 1 {
		t.Fatalf("LiveCount want 1, got %d", lc)
	}

	aa.Complete(node, a.ID)

	if lc := aa.LiveCount(node); lc != 0 {
		t.Fatalf("LiveCount after Complete want 0, got %d", lc)
	}
}

// ─── ContextRegistry tests ─────────────────────────────────────────────────

// TestContextRegistry_RegisterLookup verifies that a registered context can
// be looked up and returns the expected values.
func TestContextRegistry_RegisterLookup(t *testing.T) {
	reg := NewContextRegistry()
	shared := map[string]string{"API_KEY": "secret", "REGION": "us-east-1"}
	reg.Register("agent-1", shared)

	got, ok := reg.Lookup("agent-1")
	if !ok {
		t.Fatal("Lookup returned ok=false for a registered agent")
	}
	if got["API_KEY"] != "secret" {
		t.Fatalf("API_KEY want %q, got %q", "secret", got["API_KEY"])
	}
	if got["REGION"] != "us-east-1" {
		t.Fatalf("REGION want %q, got %q", "us-east-1", got["REGION"])
	}
}

// TestContextRegistry_CopyIsolation proves that mutating the map returned by
// Lookup does NOT alter a subsequent Lookup's result.
//
// Anti-bluff: if Lookup returned the stored map directly (no copy), the
// mutation would corrupt stored state and the second Lookup would return the
// mutated value, causing the `secondLookup["API_KEY"] != "secret"` assertion
// to pass on a broken implementation.  With copy isolation, the second Lookup
// still returns "secret" — the assertion catches any regression where the copy
// is removed.
func TestContextRegistry_CopyIsolation(t *testing.T) {
	reg := NewContextRegistry()
	reg.Register("agent-1", map[string]string{"API_KEY": "secret"})

	first, _ := reg.Lookup("agent-1")
	// Mutate the returned map.
	first["API_KEY"] = "CORRUPTED"

	second, ok := reg.Lookup("agent-1")
	if !ok {
		t.Fatal("second Lookup returned ok=false")
	}
	// The stored context must still hold the original value.
	if second["API_KEY"] != "secret" {
		t.Fatalf("copy isolation broken: stored API_KEY was mutated to %q", second["API_KEY"])
	}
}

// TestContextRegistry_Deregister verifies that after Deregister, Lookup
// returns ok=false.
//
// Anti-bluff: if Deregister were a no-op, the second Lookup would return
// ok=true and the assertion would fail.
func TestContextRegistry_Deregister(t *testing.T) {
	reg := NewContextRegistry()
	reg.Register("agent-2", map[string]string{"TOKEN": "abc"})

	reg.Deregister("agent-2")

	_, ok := reg.Lookup("agent-2")
	if ok {
		t.Fatal("Lookup should return ok=false after Deregister, but got ok=true")
	}
}

// TestContextRegistry_LookupMissingAgent verifies that Lookup returns ok=false
// for an agent that was never registered.
func TestContextRegistry_LookupMissingAgent(t *testing.T) {
	reg := NewContextRegistry()
	_, ok := reg.Lookup("ghost-agent")
	if ok {
		t.Fatal("Lookup of unregistered agent should return ok=false")
	}
}

// ─── Workload type constant ────────────────────────────────────────────────

// TestWorkloadInteractiveAgentConst verifies the constant value is the
// expected string and is distinct from known other workload types.
func TestWorkloadInteractiveAgentConst(t *testing.T) {
	const want = "interactive-agent"
	if WorkloadInteractiveAgent != want {
		t.Fatalf("WorkloadInteractiveAgent want %q, got %q", want, WorkloadInteractiveAgent)
	}
	// Must not collide with known sibling types.
	for _, other := range []string{"batch", "inference"} {
		if WorkloadInteractiveAgent == other {
			t.Fatalf("WorkloadInteractiveAgent must not equal %q", other)
		}
	}
}
