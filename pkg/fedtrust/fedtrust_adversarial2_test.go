package fedtrust

// Deeper (second-pass) adversarial trust-boundary probes for FederationTrustPolicy.
//
// The first adversarial pass (fedtrust_adversarial_test.go) pinned the fail-open
// footgun, the integer-ordering / no-float-poison contract, determinism, and DoS.
// This second pass attacks angles the first pass did NOT cover, each SINK-SIDE
// (asserts the literal Decision.Allow / Decision.Rule), looking specifically for
// the recurring high-value classes:
//
//	(A) FAIL-OPEN via wrong precedence ORDER — does a later gate's "pass" ever
//	    leak past an earlier gate's "deny"? Does an attacker-controlled `target`
//	    flip a `from` decision? Is an unknown-HIGH MinTrustLevel floor honored
//	    (fail-CLOSED) rather than silently treated as "everyone qualifies"?
//	(C) RETURN-STATE ISOLATION — Evaluate must not mutate its argument identities
//	    nor leak shared mutable state into the returned Decision.
//	(E) CONCURRENCY — a shared CanBind predicate hammered concurrently must see
//	    only unmutated copies and never produce a torn/fail-open verdict (-race).
//
// All probes pass on the code left behind. No production change was required:
// every deny branch is fail-CLOSED and load-bearing; the prior contract holds.

import (
	"sync"
	"sync/atomic"
	"testing"
)

// ---------------------------------------------------------------------------
// (A) PRECEDENCE-ORDER FAIL-OPEN: an earlier DENY must absolutely win over a
//     later gate that would PASS. We construct inputs where every *later* gate
//     would ALLOW, and assert the *earlier* deny still fires. This is the exact
//     profile of a fail-open precedence bug ("a lower/earlier check is skipped
//     when a later check passes").
// ---------------------------------------------------------------------------

// TestDeniedDomainBeatsEveryLaterPass: a denied domain that ALSO (a) is in the
// allowlist, (b) presents the MAX trust level, and (c) would be permitted by
// CanBind — must STILL be denied with rule "denied-domain". If precedence were
// reordered so any later "pass" short-circuited to Allow, this catches it.
//
// Mutation proof: move the denied-domain check below the trust-level/topology
// checks (or make it `else`-guarded) => a CanBind-permitted, max-trust,
// allowlisted-but-denied domain would slip to Allow and this test fails.
func TestDeniedDomainBeatsEveryLaterPass(t *testing.T) {
	canBindAlwaysYes := func(from, to ClusterIdentity) bool { return true }
	policy := NewFederationTrustPolicy(TrustConfig{
		AllowedDomains: []string{"both.example.com"}, // domain is ALSO allowlisted
		DeniedDomains:  []string{"both.example.com"}, // ...and denied
		MinTrustLevel:  TrustHardened,                // highest floor
		CanBind:        canBindAlwaysYes,             // topology would permit
	})
	from := ClusterIdentity{Name: "max-trust-attacker", TrustDomain: "both.example.com", TrustLevel: TrustHardened}
	target := ClusterIdentity{Name: "t", TrustDomain: "both.example.com", TrustLevel: TrustHardened}

	d := policy.Evaluate(from, target)
	if d.Allow {
		t.Fatalf("FAIL-OPEN: a denied domain was ADMITTED despite deny-precedence "+
			"(Rule=%q, Reason=%q)", d.Rule, d.Reason)
	}
	if d.Rule != "denied-domain" {
		t.Fatalf("precedence violation: Rule=%q; want \"denied-domain\" (a later gate's pass "+
			"must not override the denied-domain deny)", d.Rule)
	}
}

// TestAllowlistDenyBeatsTrustAndTopologyPass: an identity NOT in a non-empty
// allowlist must be denied with "domain-not-allowed" even when it presents the
// max trust level AND CanBind would permit it. Catches a reorder where the
// trust/topology pass leaks past the allowlist gate.
//
// Mutation proof: move the allowlist check after the trust-level/topology checks
// => a max-trust, topology-permitted stranger would be Allowed; this fails.
func TestAllowlistDenyBeatsTrustAndTopologyPass(t *testing.T) {
	policy := NewFederationTrustPolicy(TrustConfig{
		AllowedDomains: []string{"insider.example.com"},
		MinTrustLevel:  TrustHardened,
		CanBind:        func(from, to ClusterIdentity) bool { return true },
	})
	from := ClusterIdentity{Name: "outsider", TrustDomain: "stranger.example.com", TrustLevel: TrustHardened}
	target := ClusterIdentity{Name: "t", TrustDomain: "insider.example.com", TrustLevel: TrustHardened}

	d := policy.Evaluate(from, target)
	if d.Allow {
		t.Fatalf("FAIL-OPEN: non-allowlisted stranger ADMITTED at max trust (Rule=%q, Reason=%q)",
			d.Rule, d.Reason)
	}
	if d.Rule != "domain-not-allowed" {
		t.Fatalf("precedence violation: Rule=%q; want \"domain-not-allowed\"", d.Rule)
	}
}

// TestTrustLevelDenyBeatsTopologyPass: a trust-deficient identity must be denied
// with "trust-level" even when CanBind would permit it. Catches a reorder where
// the topology pass leaks past the trust-level floor.
//
// Mutation proof: swap precedence 3 and 4 and make topology short-circuit Allow
// => a sub-floor, topology-permitted identity would be Allowed; this fails.
func TestTrustLevelDenyBeatsTopologyPass(t *testing.T) {
	policy := NewFederationTrustPolicy(TrustConfig{
		AllowedDomains: []string{"ok.example.com"},
		MinTrustLevel:  TrustStandard,
		CanBind:        func(from, to ClusterIdentity) bool { return true }, // topology permits
	})
	from := ClusterIdentity{Name: "weak", TrustDomain: "ok.example.com", TrustLevel: TrustProvisional} // below floor
	target := ClusterIdentity{Name: "t", TrustDomain: "ok.example.com", TrustLevel: TrustHardened}

	d := policy.Evaluate(from, target)
	if d.Allow {
		t.Fatalf("FAIL-OPEN: sub-floor identity ADMITTED because topology permitted it "+
			"(Rule=%q, Reason=%q)", d.Rule, d.Reason)
	}
	if d.Rule != "trust-level" {
		t.Fatalf("precedence violation: Rule=%q; want \"trust-level\"", d.Rule)
	}
}

// TestUnknownHighMinTrustFloorFailsClosed: if a caller configures an
// out-of-range HIGH MinTrustLevel (e.g. a future TrustLevel constant this build
// doesn't know about, modeled as TrustLevel(99)), even a TrustHardened peer must
// be DENIED — the floor is honored as a strict `<` comparison, NOT silently
// reinterpreted as "any named level qualifies". This is the fail-CLOSED dual of
// the "unknown level ordered HIGH" hypothesis: an unknown-high REQUIREMENT must
// keep everyone below it out.
//
// Mutation proof: change `from.TrustLevel < p.minTrustLevel` to clamp
// p.minTrustLevel into the known range (or to `<=` against a named max) =>
// TrustHardened would wrongly satisfy the 99 floor and this test fails.
func TestUnknownHighMinTrustFloorFailsClosed(t *testing.T) {
	policy := NewFederationTrustPolicy(TrustConfig{
		AllowedDomains: []string{"ok.example.com"},
		MinTrustLevel:  TrustLevel(99), // unknown, above every named level
	})
	from := ClusterIdentity{Name: "hardened", TrustDomain: "ok.example.com", TrustLevel: TrustHardened}
	target := ClusterIdentity{Name: "t", TrustDomain: "ok.example.com", TrustLevel: TrustHardened}

	d := policy.Evaluate(from, target)
	if d.Allow {
		t.Fatalf("FAIL-OPEN: TrustHardened admitted against an unknown-HIGH floor TrustLevel(99) "+
			"(Rule=%q, Reason=%q); the `<` floor must keep below-floor levels out", d.Rule, d.Reason)
	}
	if d.Rule != "trust-level" {
		t.Fatalf("Rule=%q; want \"trust-level\"", d.Rule)
	}
}

// TestTargetCannotFlipFromDecision: the admission decision is about `from`. A
// hostile/denied `target` must NOT relax a `from` deny, and a pristine `target`
// must NOT be required to rescue a legitimately-denied `from`. We hold `from`
// denied (sub-floor) and vary `target` across every shape; the verdict must stay
// denied with the same rule. Catches any accidental use of `target`'s attributes
// in the from-gates (an asymmetry bug that could let an attacker pick a target to
// flip the gate).
//
// SINK-SIDE: Allow stays false, Rule stays "trust-level" for all targets.
func TestTargetCannotFlipFromDecision(t *testing.T) {
	policy := NewFederationTrustPolicy(TrustConfig{
		AllowedDomains: []string{"ok.example.com"},
		DeniedDomains:  []string{"bad.example.com"},
		MinTrustLevel:  TrustStandard,
	})
	from := ClusterIdentity{Name: "weak", TrustDomain: "ok.example.com", TrustLevel: TrustUnknown} // sub-floor => trust-level deny

	targets := []ClusterIdentity{
		{}, // empty
		{Name: "t", TrustDomain: "ok.example.com", TrustLevel: TrustHardened},
		{Name: "t", TrustDomain: "bad.example.com", TrustLevel: TrustHardened},   // denied-domain target
		{Name: "t", TrustDomain: "stranger.example.com", TrustLevel: TrustUnknown},
		{Name: "t", TrustDomain: "ok.example.com", TrustLevel: TrustLevel(99)},
	}
	for i, tgt := range targets {
		d := policy.Evaluate(from, tgt)
		if d.Allow {
			t.Fatalf("target[%d] flipped a from-deny to ALLOW (Rule=%q, Reason=%q)", i, d.Rule, d.Reason)
		}
		if d.Rule != "trust-level" {
			t.Fatalf("target[%d]: Rule=%q; want \"trust-level\" (target must not change the from-gate)",
				i, d.Rule)
		}
	}
}

// ---------------------------------------------------------------------------
// (C) RETURN-STATE / ARGUMENT ISOLATION
// ---------------------------------------------------------------------------

// TestEvaluateDoesNotMutateArguments: Evaluate takes identities by value and must
// not mutate the caller's copies. We snapshot the input structs, evaluate across
// every precedence branch, and assert the inputs are byte-identical afterward.
// (Decision carries only strings/bool — no pointer/map aliasing is possible — so
// the relevant isolation risk is argument mutation, which this pins.)
func TestEvaluateDoesNotMutateArguments(t *testing.T) {
	policy := NewFederationTrustPolicy(TrustConfig{
		AllowedDomains: []string{"ok.example.com"},
		DeniedDomains:  []string{"bad.example.com"},
		MinTrustLevel:  TrustStandard,
		CanBind:        func(from, to ClusterIdentity) bool { return from.Name != "nope" },
	})

	inputs := []struct{ from, target ClusterIdentity }{
		{ClusterIdentity{Name: "a", TrustDomain: "bad.example.com", TrustLevel: TrustHardened}, ClusterIdentity{Name: "t", TrustDomain: "ok.example.com", TrustLevel: TrustHardened}},
		{ClusterIdentity{Name: "b", TrustDomain: "stranger.example.com", TrustLevel: TrustHardened}, ClusterIdentity{Name: "t", TrustDomain: "ok.example.com", TrustLevel: TrustHardened}},
		{ClusterIdentity{Name: "c", TrustDomain: "ok.example.com", TrustLevel: TrustUnknown}, ClusterIdentity{Name: "t", TrustDomain: "ok.example.com", TrustLevel: TrustHardened}},
		{ClusterIdentity{Name: "nope", TrustDomain: "ok.example.com", TrustLevel: TrustHardened}, ClusterIdentity{Name: "t", TrustDomain: "ok.example.com", TrustLevel: TrustHardened}},
		{ClusterIdentity{Name: "ok", TrustDomain: "ok.example.com", TrustLevel: TrustStandard}, ClusterIdentity{Name: "t", TrustDomain: "ok.example.com", TrustLevel: TrustHardened}},
	}
	for i := range inputs {
		fromBefore := inputs[i].from
		targetBefore := inputs[i].target
		_ = policy.Evaluate(inputs[i].from, inputs[i].target)
		if inputs[i].from != fromBefore {
			t.Fatalf("case %d: Evaluate mutated `from`: before=%+v after=%+v", i, fromBefore, inputs[i].from)
		}
		if inputs[i].target != targetBefore {
			t.Fatalf("case %d: Evaluate mutated `target`: before=%+v after=%+v", i, targetBefore, inputs[i].target)
		}
	}
}

// ---------------------------------------------------------------------------
// (E) CONCURRENCY — shared CanBind predicate sees only unmutated copies.
// ---------------------------------------------------------------------------

// TestConcurrentCanBindSeesUnmutatedCopies hammers a SHARED policy whose CanBind
// inspects both identities, from many goroutines that each pass a DISTINCT input.
// CanBind asserts (via an atomic breach counter) that it never observes a torn /
// cross-contaminated identity (e.g. from.Name of goroutine A paired with the
// target of goroutine B). Because Evaluate passes value copies, each invocation
// is isolated; -race must stay silent and breaches must be zero.
//
// This extends the existing concurrency suite, which used a constant CanBind that
// only read from.Name; here CanBind reads BOTH identities and cross-checks them,
// so any shared-state leak through the predicate boundary would surface.
func TestConcurrentCanBindSeesUnmutatedCopies(t *testing.T) {
	var breaches int64
	// CanBind is the sink: it permits only when from/target are the matched pair
	// this goroutine constructed (encoded so the from-name embeds the target-name).
	policy := NewFederationTrustPolicy(TrustConfig{
		AllowedDomains: []string{"ok.example.com"},
		MinTrustLevel:  TrustUnknown,
		CanBind: func(from, to ClusterIdentity) bool {
			// Expect from.Name == "from-"+to.Name. A torn/cross read breaks this.
			if from.Name != "from-"+to.Name {
				atomic.AddInt64(&breaches, 1)
				return false
			}
			return true
		},
	})

	const goroutines = 64
	const iters = 2000
	var wg sync.WaitGroup
	var denials int64
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			tname := "t" + itoa2(g)
			from := ClusterIdentity{Name: "from-" + tname, TrustDomain: "ok.example.com", TrustLevel: TrustHardened}
			target := ClusterIdentity{Name: tname, TrustDomain: "ok.example.com", TrustLevel: TrustHardened}
			for i := 0; i < iters; i++ {
				if d := policy.Evaluate(from, target); !d.Allow {
					atomic.AddInt64(&denials, 1)
				}
			}
		}(g)
	}
	wg.Wait()

	if n := atomic.LoadInt64(&breaches); n != 0 {
		t.Fatalf("CanBind observed %d torn/cross-contaminated identity pairs under concurrency "+
			"(value-copy isolation violated)", n)
	}
	if n := atomic.LoadInt64(&denials); n != 0 {
		t.Fatalf("%d legitimate matched pairs were denied under concurrency", n)
	}
}

// itoa2 is a tiny dependency-free int->string helper, named distinctly from the
// itoa in the first adversarial file to avoid a redeclaration within the package.
func itoa2(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
