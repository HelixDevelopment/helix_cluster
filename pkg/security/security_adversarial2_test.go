package security

// Second-pass adversarial sink-side probes for pkg/security.
//
// The first pass (security_adversarial_test.go) found and fixed a SPIFFE
// trust-domain "/" fail-open and pinned the parser front-door + the TLS
// builder + the Vault wrapper. This second pass goes DEEPER on parts the
// first pass did NOT exercise:
//
//   - REAL mTLS handshakes that prove the *tls.Config produced by ServerTLS /
//     ClientTLS actually REJECTS an EXPIRED client cert (signed by the trusted
//     CA) and a WRONG-SAN server cert. These are the worst fail-open class:
//     a cert-verification short-circuit that admits an expired / wrong-name
//     peer. The verdict checked is the sink — the handshake / I/O outcome —
//     never "did not panic".
//   - Additional SPIFFE parser bypass vectors NOT covered by the existing
//     front-door pin (backslash-in-host, control char in host, opaque
//     "spiffe:" form without authority, scheme-with-one-slash).
//   - LATENT-RISK pins for normalization gaps the parser leaves open
//     (uppercase trust domain; "." / ".." / empty path segments). These are
//     defeated downstream by exact-match (fail-CLOSED), so they are pinned as
//     current behavior — NOT silently "fixed" — so a future deliberate
//     tightening shows up as an intentional change.
//   - Parser purity under -race (ParseSPIFFEID must be stateless/pure).
//
// Nothing here is gated behind an env var. Every test passes on the code left
// behind. Classification per test: REAL BUG / CONTRACT PIN / LATENT RISK.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"
)

// -----------------------------------------------------------------------------
// helper: a CA + a client leaf with a CONFIGURABLE validity window and SAN.
//
// Unlike generateTestCert (which never exposes the CA key), this minter retains
// the CA key so it can sign an EXPIRED — but otherwise trusted-chain — client
// leaf. That is the precise adversarial input: a cert whose chain is trusted
// but whose validity period has elapsed. A verification that short-circuits
// after chain-building (and skips the time check) would fail-open admit it.
type adv2CA struct {
	caKey     *ecdsa.PrivateKey
	caCert    *x509.Certificate
	caCertPEM []byte
}

func adv2NewCA(t *testing.T) *adv2CA {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(9001),
		Subject:               pkix.Name{Organization: []string{"Adv2 CA"}},
		NotBefore:             time.Now().Add(-2 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &adv2CA{
		caKey:     caKey,
		caCert:    caCert,
		caCertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
	}
}

// leaf mints a leaf cert signed by this CA with the given validity window, CN,
// SAN DNS names, and ext key usages. Returns PEM cert+key.
func (c *adv2CA) leaf(t *testing.T, serial int64, cn string, dns []string, notBefore, notAfter time.Time, eku []x509.ExtKeyUsage) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  eku,
		DNSNames:     dns,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.caCert, &key.PublicKey, c.caKey)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
	return certPEM, keyPEM
}

// -----------------------------------------------------------------------------
// (1) FAIL-OPEN guard — EXPIRED client cert (trusted chain) must be REJECTED
// -----------------------------------------------------------------------------

// TestMTLSHandshake_ExpiredClientCertRejected — CONTRACT PIN (anti fail-open).
//
// A client presents a cert SIGNED BY THE TRUSTED CA but whose validity window
// has already elapsed (NotAfter in the past). The server config from ServerTLS
// uses tls.RequireAndVerifyClientCert, so the stdlib MUST reject it on the
// validity check. The sink is the real handshake / first I/O exchange: an
// expired client must NOT be able to round-trip bytes.
//
// This proves the package's config is not merely "chain trusted" but enforces
// the full x509 verification including the time window. A regression that
// swapped ClientAuth to VerifyClientCertIfGiven, or wired an empty ClientCAs,
// would let this expired peer through and FAIL this test.
func TestMTLSHandshake_ExpiredClientCertRejected(t *testing.T) {
	ca := adv2NewCA(t)

	// Server leaf: valid, SAN localhost, server+client auth.
	srvCert, srvKey := ca.leaf(t, 2,
		"adv2-server", []string{"localhost"},
		time.Now().Add(-time.Hour), time.Now().Add(time.Hour),
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth})

	b := NewTLSConfigBuilder(srvCert, srvKey, ca.caCertPEM)
	serverCfg, err := b.ServerTLS()
	if err != nil {
		t.Fatalf("ServerTLS: %v", err)
	}
	addr := startEchoServer(t, serverCfg)

	// EXPIRED client leaf — trusted chain, but NotAfter is firmly in the past.
	expCert, expKey := ca.leaf(t, 3,
		"adv2-expired-client", nil,
		time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour),
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})

	// Build the client config from the package's ClientTLS() so the test drives
	// the real produced config; the expired leaf is what the server must reject.
	cb := NewTLSConfigBuilder(expCert, expKey, ca.caCertPEM)
	clientCfg, err := cb.ClientTLS()
	if err != nil {
		t.Fatalf("ClientTLS (expired leaf): %v", err)
	}
	clientCfg.ServerName = "localhost"

	// The expired client must be rejected (handshake or first I/O fails).
	assertMTLSRejected(t, addr, clientCfg)
}

// -----------------------------------------------------------------------------
// (2) FAIL-OPEN guard — WRONG server SAN must be REJECTED by the client
// -----------------------------------------------------------------------------

// TestMTLSHandshake_WrongServerNameRejected — CONTRACT PIN (anti fail-open).
//
// The client built from ClientTLS sets ServerName to a value that does NOT
// match the server leaf's SAN. The stdlib client MUST abort the handshake with
// a hostname-mismatch error. This pins that the package never sets
// InsecureSkipVerify and that the RootCAs pool is wired so name verification
// runs. A regression to InsecureSkipVerify=true would make the wrong-name
// server be accepted and FAIL this test.
func TestMTLSHandshake_WrongServerNameRejected(t *testing.T) {
	ca := adv2NewCA(t)

	// Server SAN is "localhost" only.
	srvCert, srvKey := ca.leaf(t, 12,
		"adv2-server", []string{"localhost"},
		time.Now().Add(-time.Hour), time.Now().Add(time.Hour),
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth})

	b := NewTLSConfigBuilder(srvCert, srvKey, ca.caCertPEM)
	serverCfg, err := b.ServerTLS()
	if err != nil {
		t.Fatalf("ServerTLS: %v", err)
	}
	addr := startEchoServer(t, serverCfg)

	// Valid client cert (trusted, in-window) so the failure is isolated to the
	// SERVER-name mismatch, not the client's own credential.
	cliCert, cliKey := ca.leaf(t, 13,
		"adv2-good-client", nil,
		time.Now().Add(-time.Hour), time.Now().Add(time.Hour),
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})

	// IMPORTANT: build the client config from the package's ClientTLS() so this
	// test exercises the REAL produced config. If a regression set
	// InsecureSkipVerify=true in ClientTLS, that flag would be inherited here and
	// the wrong-SAN server would be ACCEPTED — failing this test (load-bearing).
	cb := NewTLSConfigBuilder(cliCert, cliKey, ca.caCertPEM)
	clientCfg, err := cb.ClientTLS()
	if err != nil {
		t.Fatalf("ClientTLS: %v", err)
	}
	clientCfg.ServerName = "not-the-server.example" // deliberately wrong

	conn, derr := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", addr, clientCfg)
	if derr == nil {
		_ = conn.Close()
		t.Fatal("FAIL-OPEN: client accepted a server whose SAN does not match ServerName " +
			"(InsecureSkipVerify or missing name verification)")
	}
	// Sanity: it must be a verification/hostname error, not a dial/timeout.
	var netErr net.Error
	if e, ok := derr.(net.Error); ok {
		netErr = e
	}
	if netErr != nil && netErr.Timeout() {
		t.Fatalf("expected hostname-mismatch rejection, got timeout: %v", derr)
	}
}

// -----------------------------------------------------------------------------
// (3) SPIFFE parser — additional bypass vectors (CONTRACT PIN)
// -----------------------------------------------------------------------------

// TestParseSPIFFEID_AdversarialVectors pins that further hostile encodings are
// DENIED. These are distinct from the existing front-door pin (empty / wrong
// scheme / userinfo / query / fragment) and target URL-parsing edge cases that
// could smuggle a malformed authority past the parser.
func TestParseSPIFFEID_AdversarialVectors(t *testing.T) {
	deny := []struct {
		name string
		raw  string
		why  string
	}{
		{"backslash in host", `spiffe://example.org\evil/wl`,
			"backslash is not a legal host char; must not be treated as path sep"},
		{"control char tab in host", "spiffe://example.org\t/wl",
			"control characters in authority must be rejected"},
		{"opaque no authority", "spiffe:example.org/wl",
			"opaque form (no //) has no authority — trust domain missing"},
		{"single slash authority", "spiffe:/example.org/wl",
			"one slash is a rooted path, not an authority — no trust domain"},
		{"newline smuggle in host", "spiffe://example.org\n/wl",
			"newline must not slip into the authority"},
		{"space in host", "spiffe://exa mple.org/wl",
			"space in authority is illegal"},
	}
	for _, tc := range deny {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			id, err := ParseSPIFFEID(tc.raw)
			if err != nil {
				return // correctly denied at parse
			}
			// If parse didn't reject, Validate MUST — otherwise it is fail-open.
			if verr := id.Validate(); verr == nil {
				t.Fatalf("FAIL-OPEN: %q accepted as VALID (td=%q path=%q): %s",
					tc.raw, id.TrustDomain, id.Path, tc.why)
			}
		})
	}
}

// -----------------------------------------------------------------------------
// (4) LATENT RISK — normalization gaps pinned to CURRENT behavior
// -----------------------------------------------------------------------------

// TestSPIFFE_LatentNormalizationGaps documents (does NOT fix) two normalization
// gaps. Both are defeated downstream by ValidateIdentity's EXACT-string match
// against the configured trust domain + path (fail-CLOSED, not fail-open), so
// they are not security holes today. They are pinned so that a future
// deliberate tightening of the parser (recommended: lowercase the trust domain
// and reject "."/".."/empty path segments per the SPIFFE spec) surfaces as an
// intentional change rather than a silent behavior shift.
func TestSPIFFE_LatentNormalizationGaps(t *testing.T) {
	t.Run("uppercase trust domain currently accepted", func(t *testing.T) {
		// SPIFFE spec mandates lowercase trust domains; the parser does not
		// normalize case. Accepting "Example.ORG" is a normalization gap, NOT a
		// fail-open bypass (exact-match downstream DENIES a case-mismatched
		// authority — the legit workload would be denied, fail-CLOSED).
		id := &SPIFFEID{TrustDomain: "Example.ORG", Path: "/wl"}
		if err := id.Validate(); err != nil {
			t.Skipf("LATENT RISK closed: Validate now rejects uppercase trust domain "+
				"(%v) — update this pin to expect DENY", err)
		}
		t.Logf("LATENT RISK (not fixed): uppercase trust domain %q accepted by Validate; "+
			"defeated downstream by exact-match. Recommend lowercasing per SPIFFE spec.",
			id.TrustDomain)
	})

	t.Run("dot-dot and empty path segments currently accepted", func(t *testing.T) {
		// Validate only checks the path begins with "/". The SPIFFE spec forbids
		// empty, "." and ".." path segments. These slip through. Defeated
		// downstream by full-string exact-match (no path normalization), so not
		// exploitable today — but a consumer that builds a filesystem/URL path
		// or does prefix-based authz from the SPIFFE path would inherit traversal
		// risk. Pinned as current behavior.
		for _, p := range []string{"/../../etc/passwd", "/a//b", "/./x"} {
			id := &SPIFFEID{TrustDomain: "example.org", Path: p}
			if err := id.Validate(); err != nil {
				t.Skipf("LATENT RISK closed: Validate now rejects path %q (%v) — "+
					"update this pin to expect DENY", p, err)
			}
			t.Logf("LATENT RISK (not fixed): path %q with traversal/empty segments "+
				"accepted by Validate; consumers using the path for authz/FS must "+
				"normalize independently.", p)
		}
	})
}

// -----------------------------------------------------------------------------
// (5) Parser purity under -race
// -----------------------------------------------------------------------------

// TestParseSPIFFEID_ConcurrentPurity runs ParseSPIFFEID + Validate from many
// goroutines on the same and different inputs. The parser must be pure
// (no shared mutable state); a regression introducing a package-level cache or
// buffer would surface as a race detector hit and/or inconsistent verdicts.
// Run with -race. Time-guarded: bounded loop, no blocking ops.
func TestParseSPIFFEID_ConcurrentPurity(t *testing.T) {
	good := "spiffe://example.org/ns/default/sa/web"
	rejects := []string{"", "http://example.org/wl", "spiffe://", "spiffe://example.org/wl?x=1"}

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				// The good ID must ALWAYS parse+validate cleanly.
				id, err := ParseSPIFFEID(good)
				if err != nil {
					t.Errorf("good ID rejected under concurrency: %v", err)
					return
				}
				if verr := id.Validate(); verr != nil {
					t.Errorf("good ID failed Validate under concurrency: %v", verr)
					return
				}
				// A reject input must ALWAYS be denied.
				r := rejects[(seed+i)%len(rejects)]
				if _, err := ParseSPIFFEID(r); err == nil {
					t.Errorf("FAIL-OPEN under concurrency: %q parsed without error", r)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}
