package spiffefed

// Adversarial cross-trust-domain identity tests.
//
// SECURITY PROPERTY UNDER TEST (from spiffefed.go doc + assertSpiffeURI):
//   Verify(trustDomain, leaf, intermediates) accepts a leaf ONLY when
//     (1) the leaf forms a real x509 chain rooted at a CA registered for
//         trustDomain in this store, AND
//     (2) the leaf carries a URI SAN with Scheme=="spiffe" AND Host==trustDomain,
//         where the host comparison is an EXACT string equality.
//
// The pre-existing suite (spiffefed_test.go) covers: unknown domain, valid chain
// after exchange, foreign-CA rejection, a clean a.example-vs-b.example URI host
// mismatch, the ErrUnknownTrustDomain sentinel, Exchange idempotency, SpiffeID
// helper, and a concurrency smoke test.
//
// GAP these tests close — identity-confusion / spoofing-resistance:
//   - lookalike / substring trust-domain hosts (suffix "b.example.evil.com",
//     embedded "evil.b.example", prefix "xb.example", "ab.example") that would
//     pass a Contains / HasSuffix / HasPrefix match but MUST fail exact match;
//   - wrong URI scheme (http://) with a matching host;
//   - port-suffixed host ("b.example:8443");
//   - case-folding host ("B.EXAMPLE") — SPIFFE matching is exact, no folding;
//   - path-traversal-style authority ("spiffe://evil.com/../b.example") where the
//     trusted name appears only in the path, never in the host;
//   - multi-SAN where the trusted host appears ONLY as a non-matching decoy
//     (every SAN is an untrusted lookalike) — must reject; and the legitimate
//     mirror (a trusted SAN present alongside an untrusted one) — must accept;
//   - empty / nil leaf inputs and an empty requested trust domain — clean reject,
//     no panic.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net/url"
	"strings"
	"testing"
	"time"
)

// trustedStore builds a store that trusts exactly "b.example" (rooted at fixB's
// CA), plus a separate fixture whose CA is NOT registered. Returned: the store,
// the trusted fixture, and the foreign (untrusted) CA + key for minting forgeries
// that chain to a CA the store has never seen.
func trustedBStore(t *testing.T) (*FederationStore, *fixture) {
	t.Helper()
	fixB := newFixture(t, "b.example")
	s := NewFederationStore()
	if err := s.AddBundle("b.example", []*x509.Certificate{fixB.caCert}); err != nil {
		t.Fatalf("AddBundle(b.example): %v", err)
	}
	return s, fixB
}

// ─────────────────────────────────────────────────────────────────────────────
// 1. Genuine accept (baseline) — a properly-federated identity validates OK.
// ─────────────────────────────────────────────────────────────────────────────

// TestAdv_GenuineFederatedIdentity_Accepted is the positive control: an SVID
// signed by b.example's registered CA, carrying spiffe://b.example/..., is
// accepted. If this ever fails, every negative below is meaningless (a Verify
// that rejects everything would "pass" the negatives by accident).
func TestAdv_GenuineFederatedIdentity_Accepted(t *testing.T) {
	s, fixB := trustedBStore(t)
	if err := s.Verify("b.example", fixB.svid, nil); err != nil {
		t.Fatalf("genuine federated identity: want nil, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 2 + 3. Lookalike / substring trust-domain confusion — THE CORE.
//
// Each forged leaf is signed by the SAME trusted CA (fixB.caCert) so the x509
// chain is VALID; only the SPIFFE URI host differs from "b.example" by a
// lookalike trick. A correct exact-match Verify MUST reject every one. A buggy
// Verify using strings.Contains / HasSuffix / HasPrefix instead of == would
// ACCEPT one or more → that is the classic identity-confusion vulnerability and
// would be a SEVERE bug.
//
// MUTATION that bites: change assertSpiffeURI's `u.Host == trustDomain` to
// `strings.Contains(u.Host, trustDomain)` (or HasSuffix/HasPrefix) → the
// matching lookalike host(s) are accepted → the corresponding subtest fails with
// "SECURITY: untrusted lookalike ... ACCEPTED".
// ─────────────────────────────────────────────────────────────────────────────

func TestAdv_LookalikeTrustDomain_Rejected(t *testing.T) {
	s, fixB := trustedBStore(t)
	const want = "b.example"

	// Every host here is NOT equal to "b.example" but is engineered to fool a
	// substring/affix matcher. Annotated with which broken matcher each defeats.
	lookalikes := []struct {
		name string
		host string
		note string
	}{
		{"suffix-attacker-domain", "b.example.evil.com", "defeats HasPrefix(host, td)"},
		{"embedded-subdomain", "evil.b.example", "defeats HasSuffix(host, td)"},
		{"prefix-lookalike", "xb.example", "defeats HasSuffix(host, td)"},
		{"prefix-lookalike2", "ab.example", "defeats HasSuffix(host, td)"},
		{"suffix-no-dot", "b.exampleX", "defeats HasPrefix(host, td)"},
		{"embedded-mid", "not-b.example-real", "defeats Contains(host, td)"},
		{"port-suffix", "b.example:8443", "defeats HasPrefix(host, td)"},
	}

	for _, lk := range lookalikes {
		t.Run(lk.name, func(t *testing.T) {
			// Sanity: confirm the host genuinely is NOT the trusted domain, but
			// WOULD satisfy a naive substring matcher (so the test truly exercises
			// the confusion vector and is not a tautology).
			if lk.host == want {
				t.Fatalf("test bug: lookalike host equals trusted domain %q", want)
			}
			if !strings.Contains(lk.host, want) {
				t.Fatalf("test bug: lookalike host %q does not even contain %q; not a confusion vector", lk.host, want)
			}

			// Forge a leaf signed by the TRUSTED CA (valid chain) but with a
			// lookalike SPIFFE URI host.
			forged := newSVID(t, "forged."+lk.name,
				&url.URL{Scheme: "spiffe", Host: lk.host, Path: "/wl"},
				fixB.caCert, fixB.caKey)

			err := s.Verify("b.example", forged, nil)
			if err == nil {
				t.Fatalf("SECURITY: untrusted lookalike SPIFFE host %q ACCEPTED as %q (%s) — trust-domain confusion bug",
					lk.host, want, lk.note)
			}
			// Domain IS registered (b.example) and chain IS valid, so the rejection
			// must come from the URI-host gate, not from ErrUnknownTrustDomain.
			if errors.Is(err, ErrUnknownTrustDomain) {
				t.Fatalf("rejection reason wrong: got ErrUnknownTrustDomain for valid-chain lookalike %q; want URI-host mismatch. err=%v", lk.host, err)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 3b. Path-traversal authority — trusted name only in the PATH, host is attacker.
//
// "spiffe://evil.com/../b.example" parses to host="evil.com", path="/../b.example".
// The trusted string "b.example" appears only in the path and must NEVER be
// matched as the trust domain.
//
// MUTATION: a matcher that consulted u.String() or u.Path with Contains would
// accept this → subtest fails.
// ─────────────────────────────────────────────────────────────────────────────

func TestAdv_PathTraversalAuthority_Rejected(t *testing.T) {
	s, fixB := trustedBStore(t)

	// Build the URI explicitly via url.Parse to mirror how a real cert encodes it.
	u, err := url.Parse("spiffe://evil.com/../b.example")
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	if u.Host == "b.example" {
		t.Fatalf("test bug: parsed host unexpectedly %q", u.Host)
	}
	if !strings.Contains(u.Path, "b.example") {
		t.Fatalf("test bug: expected trusted name in path; path=%q", u.Path)
	}

	forged := newSVID(t, "traversal", u, fixB.caCert, fixB.caKey)
	if err := s.Verify("b.example", forged, nil); err == nil {
		t.Fatal("SECURITY: path-traversal authority spiffe://evil.com/../b.example ACCEPTED as b.example")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 4. Wrong scheme — http:// with the exact trusted host must be rejected.
//
// MUTATION: drop the `u.Scheme == "spiffe"` clause in assertSpiffeURI (match on
// host alone) → the http leaf is accepted → this test fails.
// ─────────────────────────────────────────────────────────────────────────────

func TestAdv_WrongScheme_Rejected(t *testing.T) {
	s, fixB := trustedBStore(t)

	// NOTE on case: a "SPIFFE" (uppercase) scheme is intentionally NOT tested
	// here. When a URI SAN is serialized into a certificate's DER and re-parsed,
	// crypto/x509 runs it through url.Parse which lowercases the scheme — so a
	// real wire certificate can never present "SPIFFE"; by the time Verify sees
	// it, it is already "spiffe". Testing it would assert an unreachable state,
	// not a vulnerability. The genuine wrong-scheme attack surface is a different
	// scheme entirely (http/https/uri), which is what we exercise.
	for _, scheme := range []string{"http", "https", "uri"} {
		t.Run(scheme, func(t *testing.T) {
			// Host is EXACTLY the trusted domain; only the scheme is wrong.
			forged := newSVID(t, "scheme."+scheme,
				&url.URL{Scheme: scheme, Host: "b.example", Path: "/wl"},
				fixB.caCert, fixB.caKey)
			err := s.Verify("b.example", forged, nil)
			if err == nil {
				t.Fatalf("SECURITY: URI scheme %q with trusted host ACCEPTED — scheme check bypassed", scheme)
			}
			if errors.Is(err, ErrUnknownTrustDomain) {
				t.Fatalf("rejection reason wrong: ErrUnknownTrustDomain for wrong-scheme leaf; want URI-SAN mismatch. err=%v", err)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 5. Case normalization — "B.EXAMPLE" must NOT match "b.example" (exact, no fold).
//
// SPIFFE trust domains are lowercase and matching is byte-exact; assertSpiffeURI
// uses ==, so an uppercase host does not match. This pins the documented
// (no-case-folding) behavior so a future "helpful" strings.EqualFold change would
// be caught as a regression.
//
// MUTATION: replace `u.Host == trustDomain` with
// `strings.EqualFold(u.Host, trustDomain)` → the uppercase leaf is accepted →
// this test fails.
// ─────────────────────────────────────────────────────────────────────────────

func TestAdv_CaseFoldingHost_Rejected(t *testing.T) {
	s, fixB := trustedBStore(t)

	forged := newSVID(t, "uppercase",
		&url.URL{Scheme: "spiffe", Host: "B.EXAMPLE", Path: "/wl"},
		fixB.caCert, fixB.caKey)

	err := s.Verify("b.example", forged, nil)
	if err == nil {
		t.Fatal("SECURITY: case-folded host B.EXAMPLE ACCEPTED for trust domain b.example — matching is not exact")
	}
	if errors.Is(err, ErrUnknownTrustDomain) {
		t.Fatalf("rejection reason wrong: ErrUnknownTrustDomain for case-fold leaf; want URI-host mismatch. err=%v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 6. Multi-SAN decoy semantics.
//
//   (a) untrusted-only: every URI SAN is an untrusted lookalike → REJECT.
//   (b) legitimate mirror: a correct spiffe://b.example SAN present ALONGSIDE an
//       untrusted SAN → ACCEPT (presence of the trusted ID anywhere is valid per
//       assertSpiffeURI's any-match semantics). This guards against an
//       over-correction that would reject a legitimate identity merely because it
//       also carries an extra (e.g. legacy) SAN.
//
// MUTATION (for (a)): swap == to Contains → the lookalike-only leaf is accepted
// → subtest (a) fails. (b) protects the genuine-accept invariant.
// ─────────────────────────────────────────────────────────────────────────────

func TestAdv_MultiSAN_DecoyVsLegitimate(t *testing.T) {
	s, fixB := trustedBStore(t)

	t.Run("untrusted-only-rejected", func(t *testing.T) {
		// Two SANs, neither equal to b.example (one embedded-subdomain decoy, one
		// suffix-attacker decoy).
		forged := newSVIDMulti(t, "multidecoy",
			[]*url.URL{
				{Scheme: "spiffe", Host: "evil.com", Path: "/a"},
				{Scheme: "spiffe", Host: "b.example.evil.com", Path: "/b"},
			},
			fixB.caCert, fixB.caKey)
		if err := s.Verify("b.example", forged, nil); err == nil {
			t.Fatal("SECURITY: leaf with only untrusted lookalike SANs ACCEPTED for b.example")
		}
	})

	t.Run("trusted-SAN-among-decoys-accepted", func(t *testing.T) {
		good := newSVIDMulti(t, "mixed",
			[]*url.URL{
				{Scheme: "spiffe", Host: "evil.com", Path: "/a"},  // decoy
				{Scheme: "spiffe", Host: "b.example", Path: "/b"}, // genuine
			},
			fixB.caCert, fixB.caKey)
		if err := s.Verify("b.example", good, nil); err != nil {
			t.Fatalf("legitimate identity carrying an extra SAN should be accepted; got %v", err)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// 7. Empty / nil inputs — clean rejection, NO panic.
// ─────────────────────────────────────────────────────────────────────────────

// TestAdv_EmptyAndNil_NoPanic asserts Verify rejects degenerate inputs without
// panicking: empty requested trust domain, a leaf with no URI SANs, and (chain
// path) a nil intermediates slice with a leaf that has no SANs.
func TestAdv_EmptyAndNil_NoPanic(t *testing.T) {
	s, fixB := trustedBStore(t)

	t.Run("empty-trust-domain", func(t *testing.T) {
		// "" was never registered → ErrUnknownTrustDomain, no panic.
		err := s.Verify("", fixB.svid, nil)
		if !errors.Is(err, ErrUnknownTrustDomain) {
			t.Fatalf("empty trust domain: want ErrUnknownTrustDomain, got %v", err)
		}
	})

	t.Run("leaf-without-uri-san", func(t *testing.T) {
		// Valid chain to the trusted CA but ZERO URI SANs → must be rejected by
		// the URI gate, not panic on an empty leaf.URIs slice.
		noURI := newSVIDMulti(t, "nouri", nil, fixB.caCert, fixB.caKey) // no URIs at all
		err := s.Verify("b.example", noURI, nil)
		if err == nil {
			t.Fatal("SECURITY: leaf with no SPIFFE URI SAN ACCEPTED for b.example")
		}
		if errors.Is(err, ErrUnknownTrustDomain) {
			t.Fatalf("rejection reason wrong: ErrUnknownTrustDomain; want missing-URI-SAN. err=%v", err)
		}
	})

	t.Run("nil-leaf-does-not-panic", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Verify panicked on nil leaf for unknown domain: %v", r)
			}
		}()
		// Unknown domain is checked before the leaf is dereferenced, so a nil leaf
		// against an unregistered domain must return ErrUnknownTrustDomain cleanly.
		if err := s.Verify("never.registered", nil, nil); !errors.Is(err, ErrUnknownTrustDomain) {
			t.Fatalf("nil leaf / unknown domain: want ErrUnknownTrustDomain, got %v", err)
		}
	})
}

// newSVIDMulti mints a leaf SVID signed by caKey carrying the supplied set of URI
// SANs (which may be empty/nil for the no-SAN case, or hold several for the
// multi-SAN decoy cases). It mirrors newSVID from spiffefed_test.go but accepts a
// slice of URIs instead of exactly one — needed by the adversarial multi-SAN and
// no-SAN scenarios. Production code is untouched.
func newSVIDMulti(t *testing.T, cn string, uris []*url.URL, caCert *x509.Certificate, caKey *ecdsa.PrivateKey) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("newSVIDMulti(%q): generate key: %v", cn, err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
		URIs:         uris,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("newSVIDMulti(%q): create cert: %v", cn, err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("newSVIDMulti(%q): parse cert: %v", cn, err)
	}
	return cert
}
