package fedtrust

// Adversarial trust-boundary probes for FederationTrustPolicy.
//
// This file is package-internal so it can inspect the precedence behavior at
// the numeric/identity trust edges directly. Every assertion is SINK-SIDE: it
// pins the actual admission verdict (Decision.Allow / Decision.Rule), never
// merely "no panic" or a non-empty Reason.
//
// Bug classes probed (per the trust-boundary recurring class):
//
//	(a) FAIL-OPEN vs FAIL-CLOSED — does a verify decision default TRUSTED on a
//	    malformed/empty/unknown identity when it should default UNTRUSTED?
//	(b) TOTAL-ORDER AT A TRUST/IDENTITY EDGE — equal/duplicate identity inputs
//	    must resolve deterministically (no map-iteration nondeterminism leaking
//	    into the verdict or the operator-facing Reason).
//	(c) NUMERIC POISON — a NaN/±Inf trust score defeating a threshold. fedtrust
//	    uses an *integer* TrustLevel enum, so no float poison path exists; this
//	    is pinned as a CONTRACT fact, not a bug.
//	(d) HOSTILE-INPUT DoS — attacker-controlled identity strings/sizes must not
//	    cause unbounded recursion/alloc; Evaluate is O(1) map lookups.
//
// FINDINGS SUMMARY (see per-test comments for the discrimination):
//   - NO code-violates-doc bug found. The evaluator's deny branches are all
//     fail-CLOSED and load-bearing (already proven by the existing suite).
//   - ONE LATENT RISK, documented & pinned: a zero-value TrustConfig{} is an
//     ALLOW-ALL policy (empty AllowedDomains skips the allowlist gate AND
//     MinTrustLevel==TrustUnknown is the floor everything meets AND nil CanBind
//     skips topology). A zero-value/empty ClusterIdentity{} (anonymous, no
//     attested trust domain, TrustUnknown) is therefore ADMITTED. This is the
//     documented contract (fedtrust.go lines 86-103), not a code defect — but
//     it is a trust-boundary footgun: a caller who forgets to populate
//     TrustConfig gets a policy that trusts everyone, including unidentified
//     peers. TestZeroConfigIsAllowAll_DocumentedFailOpenFootgun pins the exact
//     current behavior so any future tightening is a deliberate, visible change.

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// (a) FAIL-OPEN vs FAIL-CLOSED
// ---------------------------------------------------------------------------

// TestZeroConfigIsAllowAll_DocumentedFailOpenFootgun pins the LATENT RISK: a
// zero-value config admits a zero-value (anonymous, unknown-trust) identity.
//
// This is NOT a bug-vs-doc: it is exactly what fedtrust.go documents
//   - "When [AllowedDomains is] empty, domain-allowlist checking is skipped"
//   - MinTrustLevel zero == TrustUnknown, and `from.TrustLevel < min` with
//     min==0 is never true.
//   - "A nil CanBind field means no topology gate is applied."
//
// We pin it so the allow-all footgun is a documented, regression-guarded fact:
// if a future change makes a zero config fail CLOSED (the safer default), this
// test will fail and force an explicit, reviewed decision rather than a silent
// behavior flip.
//
// SINK-SIDE: asserts the literal admission verdict (Allow==true, Rule=="allowed")
// for a fully empty identity — the strongest "trusts an unidentified peer" claim.
func TestZeroConfigIsAllowAll_DocumentedFailOpenFootgun(t *testing.T) {
	policy := NewFederationTrustPolicy(TrustConfig{}) // zero value: no boundary expressed

	anon := ClusterIdentity{} // Name="", TrustDomain="", TrustLevel=TrustUnknown(0)
	target := ClusterIdentity{}

	d := policy.Evaluate(anon, target)

	// Current, documented behavior: anonymous peer is ADMITTED.
	if !d.Allow {
		t.Fatalf("zero-config admission changed: got Allow=%v Rule=%q Reason=%q; "+
			"the documented contract is allow-all for a zero config. If this was a "+
			"deliberate security tightening, update this pin.", d.Allow, d.Rule, d.Reason)
	}
	if d.Rule != "allowed" {
		t.Fatalf("zero-config: Rule=%q; want \"allowed\" (Reason=%q)", d.Rule, d.Reason)
	}
}

// TestNonEmptyAllowlistFailsClosedOnUnknownIdentity is the SECURITY-load-bearing
// dual: once the config DOES express a trust boundary (a non-empty allowlist),
// a hostile/empty/unknown identity that presents NO recognized trust domain
// MUST be DENIED (fail-CLOSED). This is the assertion that would catch a real
// fail-open regression (e.g. "unknown domain => allow").
//
// SINK-SIDE: asserts Allow==false AND Rule=="domain-not-allowed" for an empty
// TrustDomain against a non-empty allowlist.
//
// Mutation proof (would catch a real fail-open): if precedence-2 were changed to
// admit absent domains, Allow would flip to true and this test fails.
func TestNonEmptyAllowlistFailsClosedOnUnknownIdentity(t *testing.T) {
	policy := NewFederationTrustPolicy(TrustConfig{
		AllowedDomains: []string{"prod.example.com"},
		MinTrustLevel:  TrustUnknown, // deliberately the WEAKEST floor: isolate the allowlist gate
	})

	cases := []struct {
		label  string
		from   ClusterIdentity
	}{
		{"empty identity", ClusterIdentity{}},
		{"empty domain only", ClusterIdentity{Name: "ghost", TrustDomain: "", TrustLevel: TrustHardened}},
		{"unrecognized domain", ClusterIdentity{Name: "attacker", TrustDomain: "evil.example.com", TrustLevel: TrustHardened}},
		{"whitespace domain (not auto-trimmed)", ClusterIdentity{Name: "sneaky", TrustDomain: " prod.example.com ", TrustLevel: TrustHardened}},
		{"case-variant domain (case-sensitive match)", ClusterIdentity{Name: "case", TrustDomain: "PROD.EXAMPLE.COM", TrustLevel: TrustHardened}},
	}
	target := ClusterIdentity{Name: "tgt", TrustDomain: "prod.example.com", TrustLevel: TrustHardened}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.label, func(t *testing.T) {
			d := policy.Evaluate(tc.from, target)
			if d.Allow {
				t.Fatalf("FAIL-OPEN: %s was ADMITTED (Rule=%q, Reason=%q); a non-allowlisted "+
					"identity must be denied", tc.label, d.Rule, d.Reason)
			}
			if d.Rule != "domain-not-allowed" {
				t.Errorf("%s: Rule=%q; want \"domain-not-allowed\"", tc.label, d.Rule)
			}
		})
	}
}

// TestMinTrustFloorFailsClosedOnUnknown pins that raising MinTrustLevel above
// the zero floor makes an unknown-trust identity fail CLOSED even when its
// domain is allowlisted. This is the trust-level edge: TrustUnknown(0) presented
// against a TrustStandard(2) floor must DENY.
//
// SINK-SIDE: Allow==false, Rule=="trust-level".
func TestMinTrustFloorFailsClosedOnUnknown(t *testing.T) {
	policy := NewFederationTrustPolicy(TrustConfig{
		AllowedDomains: []string{"ok.example.com"},
		MinTrustLevel:  TrustStandard,
	})
	from := ClusterIdentity{Name: "unattested", TrustDomain: "ok.example.com", TrustLevel: TrustUnknown}
	target := ClusterIdentity{Name: "t", TrustDomain: "ok.example.com", TrustLevel: TrustHardened}

	d := policy.Evaluate(from, target)
	if d.Allow {
		t.Fatalf("FAIL-OPEN: unattested (TrustUnknown) identity admitted against TrustStandard "+
			"floor (Rule=%q, Reason=%q)", d.Rule, d.Reason)
	}
	if d.Rule != "trust-level" {
		t.Errorf("Rule=%q; want \"trust-level\"", d.Rule)
	}
}

// ---------------------------------------------------------------------------
// (b) TOTAL-ORDER AT A TRUST/IDENTITY EDGE
// ---------------------------------------------------------------------------

// TestDomainNotAllowedReasonIsDeterministic probes the only place where map
// iteration could leak nondeterminism into operator-facing output: the
// "domain-not-allowed" Reason embeds the allowlist via sortedKeys(). A naive
// implementation that ranged the map directly would produce a different Reason
// ordering across runs (Go randomizes map iteration), i.e. a nondeterministic
// trust-decision artifact at the identity edge.
//
// We build a policy with many allowlisted domains, evaluate the SAME denied
// input many times, and assert the Reason is byte-identical every time AND is
// in sorted order. This is the total-order pin: equal inputs => one canonical
// answer.
//
// SINK-SIDE: asserts the exact Reason string is stable and sorted, not just
// "non-empty".
//
// Mutation proof: replace `sortedKeys(p.allowedDomains)` with a direct
// `range`-collected slice (unsorted) in fedtrust.go => the sorted-order check
// and/or the cross-iteration stability check fails.
func TestDomainNotAllowedReasonIsDeterministic(t *testing.T) {
	// Deliberately NOT pre-sorted; many entries so map randomization would show.
	domains := []string{
		"m.example.com", "a.example.com", "z.example.com", "c.example.com",
		"q.example.com", "b.example.com", "y.example.com", "d.example.com",
		"n.example.com", "e.example.com",
	}
	policy := NewFederationTrustPolicy(TrustConfig{
		AllowedDomains: domains,
		MinTrustLevel:  TrustUnknown,
	})

	from := ClusterIdentity{Name: "outsider", TrustDomain: "not-listed.example.com", TrustLevel: TrustHardened}
	target := ClusterIdentity{Name: "t", TrustDomain: "a.example.com", TrustLevel: TrustHardened}

	first := policy.Evaluate(from, target)
	if first.Allow || first.Rule != "domain-not-allowed" {
		t.Fatalf("setup: expected domain-not-allowed deny, got Allow=%v Rule=%q", first.Allow, first.Rule)
	}

	// The embedded allowlist must appear in sorted order. Verify the sorted
	// sequence is a substring of the Reason (i.e. ordering is canonical).
	wantSorted := `[a.example.com b.example.com c.example.com d.example.com e.example.com ` +
		`m.example.com n.example.com q.example.com y.example.com z.example.com]`
	if !strings.Contains(first.Reason, wantSorted) {
		t.Fatalf("Reason does not embed the allowlist in sorted order.\n got: %q\nwant substring: %q",
			first.Reason, wantSorted)
	}

	// Determinism: the SAME input must yield a byte-identical Reason across many
	// evaluations (catches map-iteration nondeterminism leaking into the verdict
	// artifact).
	for i := 0; i < 256; i++ {
		got := policy.Evaluate(from, target)
		if got.Reason != first.Reason {
			t.Fatalf("nondeterministic Reason at iteration %d:\n first: %q\n  this: %q",
				i, first.Reason, got.Reason)
		}
		if got.Allow != first.Allow || got.Rule != first.Rule {
			t.Fatalf("nondeterministic verdict at iteration %d: first=(%v,%q) this=(%v,%q)",
				i, first.Allow, first.Rule, got.Allow, got.Rule)
		}
	}
}

// TestDuplicatePeerIDsCollapseDeterministically probes the duplicate-identity
// edge: passing the same domain twice in AllowedDomains/DeniedDomains (a
// duplicate "peer ID") must not create ambiguity. The map-based construction
// collapses duplicates; deny-precedence still wins deterministically.
//
// SINK-SIDE: a domain listed twice in DeniedDomains (and also in AllowedDomains)
// is still DENIED with Rule=="denied-domain" — duplicates do not flip the verdict.
func TestDuplicatePeerIDsCollapseDeterministically(t *testing.T) {
	policy := NewFederationTrustPolicy(TrustConfig{
		AllowedDomains: []string{"dup.example.com", "dup.example.com", "other.example.com"},
		DeniedDomains:  []string{"dup.example.com", "dup.example.com"},
		MinTrustLevel:  TrustUnknown,
	})
	from := ClusterIdentity{Name: "c", TrustDomain: "dup.example.com", TrustLevel: TrustHardened}
	target := ClusterIdentity{Name: "t", TrustDomain: "other.example.com", TrustLevel: TrustHardened}

	// Evaluate repeatedly: deny must win every time (deny-precedence), duplicates
	// notwithstanding.
	for i := 0; i < 64; i++ {
		d := policy.Evaluate(from, target)
		if d.Allow {
			t.Fatalf("iter %d: duplicate-listed denied domain was ADMITTED (Rule=%q, Reason=%q)",
				i, d.Rule, d.Reason)
		}
		if d.Rule != "denied-domain" {
			t.Fatalf("iter %d: Rule=%q; want \"denied-domain\"", i, d.Rule)
		}
	}
}

// ---------------------------------------------------------------------------
// (c) NUMERIC POISON — pinned as a CONTRACT FACT (no float path exists)
// ---------------------------------------------------------------------------

// TestTrustLevelIsIntegerOrdered_NoFloatPoison pins that TrustLevel is a
// totally-ordered integer enum: there is no float trust score, hence no
// NaN/±Inf path that could defeat the MinTrustLevel threshold. We assert the
// ordering is a strict, total numeric order and that an out-of-range (large)
// level is treated as ABOVE the floor (still ordered, not poisoned).
//
// This is class (c): because the score is an int, the classic "NaN < x is
// false AND NaN >= x is false, so it slips past both a < guard and a >= guard"
// poison cannot occur. Pinned, not fixed (nothing to fix).
//
// SINK-SIDE: a synthetic high TrustLevel(99) meets a TrustHardened floor (Allow);
// TrustUnknown does not (Deny). Strict total order, no NaN-like middle ground.
func TestTrustLevelIsIntegerOrdered_NoFloatPoison(t *testing.T) {
	// Strict total order of the named levels.
	ordered := []TrustLevel{TrustUnknown, TrustProvisional, TrustStandard, TrustHardened}
	for i := 1; i < len(ordered); i++ {
		if !(ordered[i-1] < ordered[i]) {
			t.Fatalf("TrustLevel ordering broken: %v !< %v", ordered[i-1], ordered[i])
		}
	}

	policy := NewFederationTrustPolicy(TrustConfig{
		AllowedDomains: []string{"ok.example.com"},
		MinTrustLevel:  TrustHardened, // highest named floor
	})
	target := ClusterIdentity{Name: "t", TrustDomain: "ok.example.com", TrustLevel: TrustHardened}

	// An out-of-range high level (would be an "Inf-like" extreme for a float
	// score) is simply ordered ABOVE the floor — admitted, not poisoned.
	high := ClusterIdentity{Name: "superhardened", TrustDomain: "ok.example.com", TrustLevel: TrustLevel(99)}
	if d := policy.Evaluate(high, target); !d.Allow || d.Rule != "allowed" {
		t.Fatalf("out-of-range-high TrustLevel(99) should meet a Hardened floor; got Allow=%v Rule=%q",
			d.Allow, d.Rule)
	}

	// A negative level (the only "below everything" extreme an int can express)
	// is ordered BELOW the floor — denied. No float NaN can sit between.
	low := ClusterIdentity{Name: "negative", TrustDomain: "ok.example.com", TrustLevel: TrustLevel(-1)}
	if d := policy.Evaluate(low, target); d.Allow || d.Rule != "trust-level" {
		t.Fatalf("negative TrustLevel(-1) must be below the floor and DENIED; got Allow=%v Rule=%q",
			d.Allow, d.Rule)
	}
}

// ---------------------------------------------------------------------------
// (d) HOSTILE-INPUT DoS
// ---------------------------------------------------------------------------

// TestHostileLargeIdentityInputNoDoS feeds attacker-sized identity strings and
// a large denylist/allowlist. Evaluate must remain O(1) map lookups (no
// unbounded recursion/alloc) and still return the CORRECT, fail-closed verdict.
//
// SINK-SIDE: a megabyte-scale unrecognized domain is DENIED (not admitted, not
// hung). The test completing AND asserting Allow==false is the proof.
func TestHostileLargeIdentityInputNoDoS(t *testing.T) {
	// Large allow/deny lists (attacker-controlled config size is also a vector).
	allowed := make([]string, 0, 5000)
	denied := make([]string, 0, 5000)
	for i := 0; i < 5000; i++ {
		allowed = append(allowed, "allow-"+strings.Repeat("x", 8)+itoa(i)+".example.com")
		denied = append(denied, "deny-"+strings.Repeat("y", 8)+itoa(i)+".example.com")
	}
	policy := NewFederationTrustPolicy(TrustConfig{
		AllowedDomains: allowed,
		DeniedDomains:  denied,
		MinTrustLevel:  TrustStandard,
	})

	// Megabyte-scale hostile domain + name; not in allowlist => must be denied.
	huge := strings.Repeat("A", 1<<20) + ".attacker.example.com"
	from := ClusterIdentity{Name: strings.Repeat("N", 1<<20), TrustDomain: huge, TrustLevel: TrustHardened}
	target := ClusterIdentity{Name: "t", TrustDomain: allowed[0], TrustLevel: TrustHardened}

	d := policy.Evaluate(from, target)
	if d.Allow {
		t.Fatalf("FAIL-OPEN under hostile input: a giant unrecognized domain was admitted (Rule=%q)", d.Rule)
	}
	if d.Rule != "domain-not-allowed" {
		t.Errorf("hostile input: Rule=%q; want \"domain-not-allowed\"", d.Rule)
	}

	// A hostile domain that IS in the denylist must be denied with deny-precedence.
	fromDenied := ClusterIdentity{Name: "x", TrustDomain: denied[2500], TrustLevel: TrustHardened}
	d2 := policy.Evaluate(fromDenied, target)
	if d2.Allow || d2.Rule != "denied-domain" {
		t.Fatalf("hostile denied-domain input: got Allow=%v Rule=%q; want denied-domain", d2.Allow, d2.Rule)
	}
}

// itoa is a tiny dependency-free int->string helper (avoids importing strconv
// just for test-data generation).
func itoa(n int) string {
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
