package spiffefed

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net/url"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test-certificate helpers
// ─────────────────────────────────────────────────────────────────────────────

// newCA mints a self-signed CA certificate.  Returns the certificate and the
// corresponding private key so callers can sign leaf SVIDs with it.
func newCA(t *testing.T, cn string) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("newCA(%q): generate key: %v", cn, err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("newCA(%q): create cert: %v", cn, err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("newCA(%q): parse cert: %v", cn, err)
	}
	return cert, key
}

// newSVID mints a leaf SVID signed by caKey.  The spiffeURI is embedded as a
// URI SAN so that Verify can check the SPIFFE trust-domain.
func newSVID(t *testing.T, cn string, spiffeURI *url.URL, caCert *x509.Certificate, caKey *ecdsa.PrivateKey) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("newSVID(%q): generate key: %v", cn, err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
		URIs:         []*url.URL{spiffeURI},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("newSVID(%q): create cert: %v", cn, err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("newSVID(%q): parse cert: %v", cn, err)
	}
	return cert
}

// fixture bundles a CA cert + SVID for one trust domain.
type fixture struct {
	caCert *x509.Certificate
	caKey  *ecdsa.PrivateKey
	svid   *x509.Certificate
}

// newFixture creates a CA and a signed leaf SVID for the given trust domain.
func newFixture(t *testing.T, trustDomain string) *fixture {
	t.Helper()
	ca, key := newCA(t, "ca."+trustDomain)
	spiffeURI := &url.URL{Scheme: "spiffe", Host: trustDomain, Path: "/workload"}
	svid := newSVID(t, "workload."+trustDomain, spiffeURI, ca, key)
	return &fixture{caCert: ca, caKey: key, svid: svid}
}

// ─────────────────────────────────────────────────────────────────────────────
// Table-driven correctness tests
// ─────────────────────────────────────────────────────────────────────────────

// TestAddBundle_RejectsEmpty verifies that AddBundle rejects empty root slices
// and empty trust domain names.
//
// MUTATION: remove the len(roots)==0 guard in AddBundle → empty-roots case
// returns nil → "want error for empty roots" subtest fails.
func TestAddBundle_RejectsEmpty(t *testing.T) {
	ca, _ := newCA(t, "ca.test")
	cases := []struct {
		name        string
		trustDomain string
		roots       []*x509.Certificate
	}{
		{"empty-roots", "a.example", []*x509.Certificate{}},
		{"nil-roots", "a.example", nil},
		{"empty-domain", "", []*x509.Certificate{ca}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewFederationStore()
			if err := s.AddBundle(tc.trustDomain, tc.roots); err == nil {
				t.Fatalf("AddBundle(%q, %v): want error, got nil", tc.trustDomain, tc.roots)
			}
		})
	}
}

// TestTrustDomains_Sorted verifies that TrustDomains returns a sorted slice.
//
// MUTATION: remove sort.Strings in TrustDomains → the elements arrive in
// arbitrary map-iteration order; because Go's map iteration is intentionally
// randomised, the equality check against the fixed sorted want slice fails on
// every run in practice (probability of an accidental match with 10 domains is
// 1/10! ≈ 2.76e-7, effectively zero). Domains are inserted in strict reverse
// lexicographic order so that even a deterministic fallback that preserved
// insertion order would produce the inverse of the expected result.
func TestTrustDomains_Sorted(t *testing.T) {
	ca, _ := newCA(t, "ca.test")
	s := NewFederationStore()
	// Insert in reverse-sorted order so that insertion-order preservation would
	// produce exactly the reverse of what we want, making the mutation trivially
	// detectable.
	insert := []string{
		"z.example", "y.example", "x.example", "w.example", "v.example",
		"u.example", "t.example", "s.example", "r.example", "a.example",
	}
	for _, td := range insert {
		if err := s.AddBundle(td, []*x509.Certificate{ca}); err != nil {
			t.Fatalf("AddBundle(%q): %v", td, err)
		}
	}
	got := s.TrustDomains()
	want := []string{
		"a.example", "r.example", "s.example", "t.example", "u.example",
		"v.example", "w.example", "x.example", "y.example", "z.example",
	}
	if len(got) != len(want) {
		t.Fatalf("TrustDomains len = %d, want %d", len(got), len(want))
	}
	for i, td := range got {
		if td != want[i] {
			t.Fatalf("TrustDomains[%d] = %q, want %q", i, td, want[i])
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Anti-bluff test 1 — BEFORE Exchange: unknown trust domain
// ─────────────────────────────────────────────────────────────────────────────

// TestVerify_UnknownDomain_BeforeExchange proves that store A cannot verify a
// leaf signed by store B's CA before the Exchange has occurred. The error must
// wrap ErrUnknownTrustDomain (not merely return some other error).
//
// MUTATION: remove the "!ok" domain-existence check in Verify and return nil
// immediately → the test receives nil instead of ErrUnknownTrustDomain → fails.
func TestVerify_UnknownDomain_BeforeExchange(t *testing.T) {
	fixA := newFixture(t, "a.example")
	fixB := newFixture(t, "b.example")

	storeA := NewFederationStore()
	if err := storeA.AddBundle("a.example", []*x509.Certificate{fixA.caCert}); err != nil {
		t.Fatalf("AddBundle: %v", err)
	}
	// storeB's leaf, but storeA has never heard of "b.example".
	err := storeA.Verify("b.example", fixB.svid, nil)
	if !errors.Is(err, ErrUnknownTrustDomain) {
		t.Fatalf("Verify before exchange: want errors.Is(err, ErrUnknownTrustDomain), got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Anti-bluff test 2 — AFTER Exchange: real chain verification succeeds
// ─────────────────────────────────────────────────────────────────────────────

// TestVerify_AfterExchange_SinkSide proves that after A.Exchange(B), calling
// A.Verify("b.example", bSVID, nil) succeeds using a REAL x509 chain
// verification against B's root that was copied into A during Exchange. This is
// the sink-side proof: the bytes that were actually produced by B's CA are
// accepted by A's post-exchange pool.
//
// MUTATION: in Exchange, replace the inner `for` that adds roots to the new
// pool with a no-op (don't call newPool.AddCert) → the pool is empty → x509
// chain verification fails → Verify returns non-nil → test fails.
func TestVerify_AfterExchange_SinkSide(t *testing.T) {
	fixA := newFixture(t, "a.example")
	fixB := newFixture(t, "b.example")

	storeA := NewFederationStore()
	storeB := NewFederationStore()

	if err := storeA.AddBundle("a.example", []*x509.Certificate{fixA.caCert}); err != nil {
		t.Fatalf("AddBundle a: %v", err)
	}
	if err := storeB.AddBundle("b.example", []*x509.Certificate{fixB.caCert}); err != nil {
		t.Fatalf("AddBundle b: %v", err)
	}

	merged := storeA.Exchange(storeB)
	if merged != 1 {
		t.Fatalf("Exchange: want 1 merged domain, got %d", merged)
	}

	// REAL chain verification: bSVID was signed by fixB.caCert; storeA now
	// holds that root after Exchange; Verify must succeed.
	if err := storeA.Verify("b.example", fixB.svid, nil); err != nil {
		t.Fatalf("Verify after exchange: want nil, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Anti-bluff test 3 — untrusted leaf (wrong CA) is rejected even after exchange
// ─────────────────────────────────────────────────────────────────────────────

// TestVerify_UntrustedLeaf_RejectedAfterExchange proves that a leaf signed by
// an entirely foreign CA (one registered in neither store) is rejected by
// Verify even after A.Exchange(B). This confirms that the x509 chain check is
// real and not bypassed.
//
// MUTATION: replace the leaf.Verify(opts) call in Verify with an unconditional
// nil return → the foreign-CA leaf is accepted → test fails.
func TestVerify_UntrustedLeaf_RejectedAfterExchange(t *testing.T) {
	fixA := newFixture(t, "a.example")
	fixB := newFixture(t, "b.example")
	// A completely foreign CA — not in either store.
	foreignCA, foreignKey := newCA(t, "ca.foreign.example")
	// Mint a leaf that looks like it belongs to b.example but is signed by the
	// foreign CA. Its SPIFFE URI matches so only the chain check catches it.
	foreignSVID := newSVID(t, "evil.b.example",
		&url.URL{Scheme: "spiffe", Host: "b.example", Path: "/evil"},
		foreignCA, foreignKey)

	storeA := NewFederationStore()
	storeB := NewFederationStore()
	if err := storeA.AddBundle("a.example", []*x509.Certificate{fixA.caCert}); err != nil {
		t.Fatalf("AddBundle a: %v", err)
	}
	if err := storeB.AddBundle("b.example", []*x509.Certificate{fixB.caCert}); err != nil {
		t.Fatalf("AddBundle b: %v", err)
	}
	storeA.Exchange(storeB)

	err := storeA.Verify("b.example", foreignSVID, nil)
	if err == nil {
		t.Fatal("Verify with foreign-CA leaf: want chain error, got nil")
	}
	// Must NOT wrap ErrUnknownTrustDomain — domain is known; chain is invalid.
	if errors.Is(err, ErrUnknownTrustDomain) {
		t.Fatalf("Verify with foreign-CA leaf: error must not be ErrUnknownTrustDomain, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Anti-bluff test 4 — URI SAN host mismatch is rejected even if chain is valid
// ─────────────────────────────────────────────────────────────────────────────

// TestVerify_SpiffeURIMismatch_RejectedEvenWithValidChain proves that a leaf
// whose SPIFFE URI host does NOT equal trustDomain is rejected by Verify even
// when the x509 chain is otherwise valid. This tests the second verification
// gate (URI SAN check) independently from the chain check.
//
// MUTATION: remove the assertSpiffeURI call in Verify (or make it always return
// nil) → the mismatched URI is accepted → test fails.
func TestVerify_SpiffeURIMismatch_RejectedEvenWithValidChain(t *testing.T) {
	// CA and leaf for b.example but the leaf carries spiffe://b.example/... URI.
	fixB := newFixture(t, "b.example")

	// Separately, forge a leaf signed by fixB.caCert but with a URI that points
	// at a.example instead of b.example — valid chain, wrong trust domain.
	wrongURI := &url.URL{Scheme: "spiffe", Host: "a.example", Path: "/imposter"}
	mismatchedSVID := newSVID(t, "imposter", wrongURI, fixB.caCert, fixB.caKey)

	storeA := NewFederationStore()
	if err := storeA.AddBundle("b.example", []*x509.Certificate{fixB.caCert}); err != nil {
		t.Fatalf("AddBundle: %v", err)
	}

	// x509 chain is valid (signed by fixB.caCert), but SPIFFE URI host is
	// "a.example", not "b.example".
	err := storeA.Verify("b.example", mismatchedSVID, nil)
	if err == nil {
		t.Fatal("Verify with mismatched SPIFFE URI: want error, got nil")
	}
	if errors.Is(err, ErrUnknownTrustDomain) {
		t.Fatalf("error should not be ErrUnknownTrustDomain for URI mismatch, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Anti-bluff test 5 — ErrUnknownTrustDomain sentinel on unregistered domain
// ─────────────────────────────────────────────────────────────────────────────

// TestVerify_UnregisteredDomain_WrapsErrUnknownTrustDomain verifies the sentinel
// is reachable via errors.Is for any completely unregistered domain.
//
// MUTATION: change the error returned for missing domain to
// errors.New("some other error") (not wrapping ErrUnknownTrustDomain) →
// errors.Is returns false → test fails.
func TestVerify_UnregisteredDomain_WrapsErrUnknownTrustDomain(t *testing.T) {
	fixA := newFixture(t, "a.example")
	s := NewFederationStore()
	if err := s.AddBundle("a.example", []*x509.Certificate{fixA.caCert}); err != nil {
		t.Fatalf("AddBundle: %v", err)
	}
	err := s.Verify("never.registered", fixA.svid, nil)
	if !errors.Is(err, ErrUnknownTrustDomain) {
		t.Fatalf("want errors.Is(err, ErrUnknownTrustDomain), got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Exchange idempotency and count
// ─────────────────────────────────────────────────────────────────────────────

// TestExchange_Idempotent verifies that running Exchange a second time for
// already-known domains returns 0 (no double-import, no overwrite).
//
// MUTATION: remove the `if _, exists := s.bundles[td]; exists { continue }`
// guard in Exchange → counter keeps incrementing on repeated calls → test fails.
func TestExchange_Idempotent(t *testing.T) {
	fixA := newFixture(t, "a.example")
	fixB := newFixture(t, "b.example")

	storeA := NewFederationStore()
	storeB := NewFederationStore()
	if err := storeA.AddBundle("a.example", []*x509.Certificate{fixA.caCert}); err != nil {
		t.Fatalf("AddBundle a: %v", err)
	}
	if err := storeB.AddBundle("b.example", []*x509.Certificate{fixB.caCert}); err != nil {
		t.Fatalf("AddBundle b: %v", err)
	}

	first := storeA.Exchange(storeB)
	if first != 1 {
		t.Fatalf("first Exchange: want 1, got %d", first)
	}
	second := storeA.Exchange(storeB)
	if second != 0 {
		t.Fatalf("second Exchange (idempotent): want 0, got %d", second)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SpiffeID helper
// ─────────────────────────────────────────────────────────────────────────────

// TestSpiffeID_Constructs verifies the SpiffeID helper produces correct URLs
// and rejects invalid inputs.
//
// MUTATION: change the scheme in SpiffeID from "spiffe" to "http" → the scheme
// assertion fails → test fails.
func TestSpiffeID_Constructs(t *testing.T) {
	cases := []struct {
		name       string
		domain     string
		path       string
		wantErr    bool
		wantScheme string
		wantHost   string
		wantPath   string
	}{
		{
			name:       "valid",
			domain:     "cluster.helix",
			path:       "/ns/default/sa/worker",
			wantErr:    false,
			wantScheme: "spiffe",
			wantHost:   "cluster.helix",
			wantPath:   "/ns/default/sa/worker",
		},
		{name: "empty-domain", domain: "", path: "/x", wantErr: true},
		{name: "path-no-slash", domain: "x.example", path: "noSlash", wantErr: true},
		{name: "empty-path", domain: "x.example", path: "", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := SpiffeID(tc.domain, tc.path)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("SpiffeID(%q, %q): want error, got nil", tc.domain, tc.path)
				}
				return
			}
			if err != nil {
				t.Fatalf("SpiffeID(%q, %q): unexpected error: %v", tc.domain, tc.path, err)
			}
			if u.Scheme != tc.wantScheme {
				t.Errorf("Scheme = %q, want %q", u.Scheme, tc.wantScheme)
			}
			if u.Host != tc.wantHost {
				t.Errorf("Host = %q, want %q", u.Host, tc.wantHost)
			}
			if u.Path != tc.wantPath {
				t.Errorf("Path = %q, want %q", u.Path, tc.wantPath)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Concurrency smoke-test
// ─────────────────────────────────────────────────────────────────────────────

// TestFederationStore_ConcurrentAccess runs concurrent Verify, AddBundle, and
// TrustDomains calls under -race to confirm RWMutex protection is correct.
// Beyond race-freedom it also asserts sink-side correctness:
//
//   - Verify("a.example", …) must return nil because "a.example" was registered
//     before the goroutines launch and the mutex ensures the read always sees a
//     coherent pool.
//   - TrustDomains must return a non-empty slice.
//
// MUTATION: remove the s.mu.Lock / s.mu.RUnlock calls in AddBundle / Verify →
// race detector reports DATA RACE → test fails under -race.
// MUTATION (sink-side): make Verify always return errors.New("broken") →
// t.Errorf fires inside each goroutine → test fails.
func TestFederationStore_ConcurrentAccess(t *testing.T) {
	const goroutines = 8
	fixA := newFixture(t, "a.example")

	s := NewFederationStore()
	// "a.example" is registered before any goroutine starts; every concurrent
	// Verify call must therefore succeed (nil error).
	if err := s.AddBundle("a.example", []*x509.Certificate{fixA.caCert}); err != nil {
		t.Fatalf("AddBundle: %v", err)
	}

	done := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		go func() {
			defer func() { done <- struct{}{} }()

			// Sink-side assertion: Verify must return nil for a pre-registered domain.
			// t.Errorf is goroutine-safe; t.Fatalf is not.
			if err := s.Verify("a.example", fixA.svid, nil); err != nil {
				t.Errorf("concurrent Verify(a.example): unexpected error: %v", err)
			}

			// Concurrent re-registration of the same domain (must not panic / race).
			if err := s.AddBundle("a.example", []*x509.Certificate{fixA.caCert}); err != nil {
				t.Errorf("concurrent AddBundle(a.example): unexpected error: %v", err)
			}

			// TrustDomains must always return at least the one pre-registered domain.
			if tds := s.TrustDomains(); len(tds) == 0 {
				t.Errorf("concurrent TrustDomains: want non-empty slice, got empty")
			}
		}()
	}
	for i := 0; i < goroutines; i++ {
		<-done
	}
}
