package fedtrust

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// This file holds the adversarial concurrency + security-correctness coverage
// for FederationTrustPolicy.
//
// CONTRACT (verbatim, fedtrust.go lines 126-127):
//
//	"All fields are immutable after construction; FederationTrustPolicy is safe
//	 for concurrent use."
//
// This is an EXPLICIT positive concurrent-safety guarantee — NOT a
// "synchronize externally" / "not safe for concurrent use" disclaimer. The
// guarantee is structurally true: every write to the policy maps
// (p.deniedDomains[d]=…, p.allowedDomains[d]=…) happens inside
// NewFederationTrustPolicy BEFORE the *FederationTrustPolicy pointer is
// returned/published. Evaluate is strictly read-only (map *reads* only; no
// element assignment, no delete, no field mutation). There is NO runtime
// policy-update path (no Deny()/Allow()/SetTrust()/Update() method exists), so
// there is no write that races a concurrent read.
//
// Therefore the documented guarantee is HONEST and a sync.RWMutex would be
// dead weight, not a fix. These tests PROVE the guarantee with the -race
// detector: many goroutines hammer Evaluate on one shared policy, and -race
// must stay silent. They also assert the SECURITY-relevant policy semantics
// hold identically under concurrency (a denied domain is NEVER permitted).
//
// If a future change adds a real mutating method (write path) without a lock,
// TestConcurrentEvaluateRaceFree will surface it under -race.

// fixedSharedPolicy builds the single policy shared by all concurrency tests.
// It exercises every precedence branch so concurrent readers traverse the full
// decision tree, not just one short-circuit.
func fixedSharedPolicy() *FederationTrustPolicy {
	return NewFederationTrustPolicy(TrustConfig{
		AllowedDomains: []string{"ok.example.com", "also-ok.example.com"},
		DeniedDomains:  []string{"bad.example.com", "overlap.example.com"},
		MinTrustLevel:  TrustStandard,
		CanBind: func(from, to ClusterIdentity) bool {
			// Topology gate: forbid one specific cluster name.
			return from.Name != "forbidden-cluster"
		},
	})
}

// concCase pairs an input with the exact decision the policy MUST return,
// regardless of how many goroutines evaluate it simultaneously.
type concCase struct {
	label     string
	from      ClusterIdentity
	target    ClusterIdentity
	wantAllow bool
	wantRule  string
	reasonSub string
}

// concCases returns the full matrix of (input -> required decision) covering
// all five precedence rules. These are the invariants every goroutine asserts.
func concCases() []concCase {
	target := ClusterIdentity{Name: "tgt", TrustDomain: "ok.example.com", TrustLevel: TrustHardened}
	return []concCase{
		{
			label:     "denied-domain",
			from:      ClusterIdentity{Name: "c-denied", TrustDomain: "bad.example.com", TrustLevel: TrustHardened},
			target:    target,
			wantAllow: false,
			wantRule:  "denied-domain",
			reasonSub: "bad.example.com",
		},
		{
			// SECURITY: in both allow- and deny-list; deny MUST win even concurrently.
			label:     "deny-precedence-overlap",
			from:      ClusterIdentity{Name: "c-overlap", TrustDomain: "overlap.example.com", TrustLevel: TrustHardened},
			target:    target,
			wantAllow: false,
			wantRule:  "denied-domain",
			reasonSub: "overlap.example.com",
		},
		{
			label:     "domain-not-allowed",
			from:      ClusterIdentity{Name: "c-stranger", TrustDomain: "stranger.example.com", TrustLevel: TrustHardened},
			target:    target,
			wantAllow: false,
			wantRule:  "domain-not-allowed",
			reasonSub: "stranger.example.com",
		},
		{
			label:     "trust-level",
			from:      ClusterIdentity{Name: "c-low", TrustDomain: "ok.example.com", TrustLevel: TrustUnknown},
			target:    target,
			wantAllow: false,
			wantRule:  "trust-level",
			reasonSub: "Unknown",
		},
		{
			label:     "topology",
			from:      ClusterIdentity{Name: "forbidden-cluster", TrustDomain: "ok.example.com", TrustLevel: TrustHardened},
			target:    target,
			wantAllow: false,
			wantRule:  "topology",
			reasonSub: "forbidden-cluster",
		},
		{
			label:     "allowed",
			from:      ClusterIdentity{Name: "c-good", TrustDomain: "ok.example.com", TrustLevel: TrustStandard},
			target:    target,
			wantAllow: true,
			wantRule:  "allowed",
			reasonSub: "c-good",
		},
	}
}

// TestConcurrentEvaluateRaceFree is the load-bearing adversarial -race test.
//
// Many goroutines call Evaluate on ONE shared policy at the same time. Because
// the policy is immutable after construction and Evaluate only reads, the race
// detector MUST stay silent. Each goroutine also re-asserts the exact decision
// for its case on every iteration — so a torn read or any nondeterministic
// answer would fail the assertion in addition to (or instead of) -race firing.
//
// Run under: go test -race -count=10 -run TestConcurrentEvaluateRaceFree ./pkg/fedtrust/
//
// Proof it is load-bearing for the documented guarantee: if a future mutating
// method wrote p.deniedDomains[d] concurrently with these readers (the bug
// profile that the explicit "safe for concurrent use" doc promises does NOT
// exist), -race here would report a data race on the map. Today it is clean
// because no such write path exists.
func TestConcurrentEvaluateRaceFree(t *testing.T) {
	policy := fixedSharedPolicy()
	cases := concCases()

	const goroutines = 64
	const iters = 2000

	var wg sync.WaitGroup
	var mismatches int64 // any wrong decision across all goroutines

	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				// Rotate through every precedence branch so concurrent
				// readers touch deniedDomains, allowedDomains, minTrustLevel
				// and canBind simultaneously.
				tc := cases[(g+i)%len(cases)]
				d := policy.Evaluate(tc.from, tc.target)
				if d.Allow != tc.wantAllow || d.Rule != tc.wantRule {
					atomic.AddInt64(&mismatches, 1)
					continue
				}
				if !strings.Contains(d.Reason, tc.reasonSub) {
					atomic.AddInt64(&mismatches, 1)
				}
			}
		}(g)
	}
	wg.Wait()

	if n := atomic.LoadInt64(&mismatches); n != 0 {
		t.Fatalf("concurrent Evaluate produced %d incorrect decisions; "+
			"policy is not behaving deterministically under concurrency", n)
	}
}

// TestConcurrentDeniedDomainNeverPermitted is the SECURITY-focused concurrency
// test: under heavy concurrent load, a denied trust domain must NEVER be
// granted Allow=true. A single Allow for a denied domain is a security breach
// and fails the test immediately.
//
// It mixes denied-domain readers with allowed-domain readers on the SAME shared
// policy so that, if any shared mutable state existed, a concurrent allow could
// "leak" into a denied-domain evaluation. It cannot, because the policy is
// immutable — and this test proves it under -race.
func TestConcurrentDeniedDomainNeverPermitted(t *testing.T) {
	policy := fixedSharedPolicy()

	denied := ClusterIdentity{Name: "attacker", TrustDomain: "bad.example.com", TrustLevel: TrustHardened}
	overlap := ClusterIdentity{Name: "attacker2", TrustDomain: "overlap.example.com", TrustLevel: TrustHardened}
	allowed := ClusterIdentity{Name: "legit", TrustDomain: "ok.example.com", TrustLevel: TrustStandard}
	target := ClusterIdentity{Name: "tgt", TrustDomain: "ok.example.com", TrustLevel: TrustHardened}

	const goroutines = 48
	const iters = 3000

	var wg sync.WaitGroup
	var breaches int64      // denied domain wrongly Allowed (SECURITY failure)
	var allowFailures int64 // legit domain wrongly Denied (availability failure)

	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				// Interleave denied / overlap / allowed evaluations.
				switch (g + i) % 3 {
				case 0:
					if d := policy.Evaluate(denied, target); d.Allow || d.Rule != "denied-domain" {
						atomic.AddInt64(&breaches, 1)
					}
				case 1:
					if d := policy.Evaluate(overlap, target); d.Allow || d.Rule != "denied-domain" {
						atomic.AddInt64(&breaches, 1)
					}
				case 2:
					if d := policy.Evaluate(allowed, target); !d.Allow || d.Rule != "allowed" {
						atomic.AddInt64(&allowFailures, 1)
					}
				}
			}
		}(g)
	}
	wg.Wait()

	if n := atomic.LoadInt64(&breaches); n != 0 {
		t.Fatalf("SECURITY BREACH: a denied trust domain was permitted (or wrong rule) "+
			"%d times under concurrency", n)
	}
	if n := atomic.LoadInt64(&allowFailures); n != 0 {
		t.Fatalf("a legitimately allowed domain was denied %d times under concurrency", n)
	}
}

// TestConcurrentDistinctPoliciesIsolated verifies that constructing many
// policies concurrently (each in its own goroutine) and evaluating them yields
// correct, isolated results — i.e. NewFederationTrustPolicy's per-call map
// allocation has no shared state across constructions.
//
// This guards the construction path (the only place writes occur) under
// concurrency, complementing the read-path tests above.
func TestConcurrentDistinctPoliciesIsolated(t *testing.T) {
	const goroutines = 50

	var wg sync.WaitGroup
	var failures int64

	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			// Each goroutine builds a policy that denies its own unique domain
			// and allows a shared one.
			ownDenied := "denied-" + string(rune('A'+g%26)) + ".net"
			p := NewFederationTrustPolicy(TrustConfig{
				AllowedDomains: []string{"shared.net"},
				DeniedDomains:  []string{ownDenied},
				MinTrustLevel:  TrustUnknown,
			})
			to := ClusterIdentity{Name: "t", TrustDomain: "shared.net", TrustLevel: TrustUnknown}

			// Its own denied domain must be denied.
			fromDenied := ClusterIdentity{Name: "x", TrustDomain: ownDenied, TrustLevel: TrustUnknown}
			if d := p.Evaluate(fromDenied, to); d.Allow || d.Rule != "denied-domain" {
				atomic.AddInt64(&failures, 1)
			}
			// The shared allowed domain must be allowed.
			fromOK := ClusterIdentity{Name: "y", TrustDomain: "shared.net", TrustLevel: TrustUnknown}
			if d := p.Evaluate(fromOK, to); !d.Allow || d.Rule != "allowed" {
				atomic.AddInt64(&failures, 1)
			}
		}(g)
	}
	wg.Wait()

	if n := atomic.LoadInt64(&failures); n != 0 {
		t.Fatalf("concurrent policy construction/evaluation produced %d incorrect "+
			"decisions; constructions are not isolated", n)
	}
}
