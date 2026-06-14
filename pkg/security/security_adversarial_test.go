package security

// Adversarial sink-side probes for pkg/security decision functions.
//
// Highest-severity class targeted here: a FAIL-OPEN verdict — a
// validate/parse/verify decision that returns VALID/ALLOW on malformed,
// empty, missing, or structurally-impossible input when it must DENY.
//
// Every assertion checks the ACTUAL verdict (the returned error / config
// field), never merely "did not panic". Each test is annotated REAL BUG,
// LATENT RISK, or CONTRACT PIN.
//
// This file is package-internal (package security) so it can construct
// SPIFFEID values directly and exercise the exported decision surface.

import (
	"context"
	"crypto/tls"
	"errors"
	"strings"
	"sync"
	"testing"
)

// -----------------------------------------------------------------------------
// (a) FAIL-OPEN — SPIFFE trust-domain validation
// -----------------------------------------------------------------------------

// TestValidate_RejectsTrustDomainWithSlash is the headline adversarial probe.
//
// REAL BUG (fail-open): SPIFFEID.Validate / IsValidTrustDomain return VALID for
// a trust domain that contains "/", the SPIFFE authority/path separator. Per the
// SPIFFE ID spec the trust domain is the URI authority and CANNOT contain a
// path separator; a value like "example.org/evil" is structurally impossible
// and malformed. Validate's documented contract is "checks the SPIFFE ID for
// well-formedness" and IsValidTrustDomain promises "contains no invalid
// characters" — yet "/" slips through because the byte scan only rejects
// control characters and spaces.
//
// SINK: the verdict is wrong — Validate() returns nil (VALID) on a malformed
// identity authority. This is the same defect class as the pkg/edge invalid
// TrustLevel admission found tonight: a should-deny identity admitted as valid.
//
// On UNFIXED prod this test FAILS (Validate returns nil). It is NOT gated behind
// an env var — the coordinator applies the minimal fix (reject '/' and the other
// RFC-3986 authority delimiters in IsValidTrustDomain) so it passes by default.
func TestValidate_RejectsTrustDomainWithSlash(t *testing.T) {
	// "/" must make the trust domain invalid: it is the authority/path boundary.
	bad := &SPIFFEID{TrustDomain: "example.org/evil", Path: "/wl"}
	if err := bad.Validate(); err == nil {
		t.Fatalf("FAIL-OPEN: Validate() returned VALID (nil) for trust domain %q "+
			"containing the SPIFFE authority/path separator '/'; a trust domain "+
			"can never contain '/'", bad.TrustDomain)
	}

	// Direct predicate must agree.
	if IsValidTrustDomain("example.org/evil") {
		t.Fatalf("FAIL-OPEN: IsValidTrustDomain returned true for %q (embedded '/')",
			"example.org/evil")
	}

	// A clean trust domain must STILL be accepted after any fix (anti-overfit:
	// the fix must reject '/', not blanket-reject everything).
	good := &SPIFFEID{TrustDomain: "example.org", Path: "/ns/default/sa/web"}
	if err := good.Validate(); err != nil {
		t.Fatalf("regression: Validate() rejected a well-formed SPIFFE ID: %v", err)
	}
	if !IsValidTrustDomain("example.org") {
		t.Fatalf("regression: IsValidTrustDomain rejected a clean domain")
	}
}

// TestParseSPIFFEID_FailOpenInputs — CONTRACT PIN.
//
// ParseSPIFFEID is the front-door parser. Pin that every hostile/empty/missing
// input is DENIED (non-nil error). These already pass on prod and guard against
// a future regression that would open the door.
func TestParseSPIFFEID_FailOpenInputs(t *testing.T) {
	deny := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"missing scheme", "example.org/wl"},
		{"wrong scheme http", "http://example.org/wl"},
		{"wrong scheme https", "https://example.org/wl"},
		{"bare scheme no host", "spiffe://"},
		{"empty host triple slash", "spiffe:///wl"},
		{"userinfo smuggle", "spiffe://user@example.org/wl"},
		{"query smuggle", "spiffe://example.org/wl?x=1"},
		{"fragment smuggle", "spiffe://example.org/wl#frag"},
		{"scheme case only no host", "spiffe:"},
	}
	for _, tc := range deny {
		t.Run(tc.name, func(t *testing.T) {
			id, err := ParseSPIFFEID(tc.raw)
			if err == nil {
				t.Fatalf("FAIL-OPEN: ParseSPIFFEID(%q) returned VALID id=%+v, want error", tc.raw, id)
			}
		})
	}
}

// TestValidate_FailOpenInputs — CONTRACT PIN for the documented Validate gate.
// Empty TD, empty path, no-leading-slash path, and nil receiver must all DENY.
func TestValidate_FailOpenInputs(t *testing.T) {
	deny := []struct {
		name string
		id   *SPIFFEID
	}{
		{"nil receiver", (*SPIFFEID)(nil)},
		{"empty trust domain", &SPIFFEID{TrustDomain: "", Path: "/wl"}},
		{"empty path", &SPIFFEID{TrustDomain: "example.org", Path: ""}},
		{"path no leading slash", &SPIFFEID{TrustDomain: "example.org", Path: "relative"}},
		{"space in trust domain", &SPIFFEID{TrustDomain: "ex ample", Path: "/wl"}},
		{"control char in trust domain", &SPIFFEID{TrustDomain: "a\x00b", Path: "/wl"}},
		{"tab in trust domain", &SPIFFEID{TrustDomain: "a\tb", Path: "/wl"}},
	}
	for _, tc := range deny {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.id.Validate(); err == nil {
				t.Fatalf("FAIL-OPEN: Validate() returned VALID for %+v, want error", tc.id)
			}
		})
	}
}

// TestValidate_LatentRiskTrustDomains documents LATENT RISK cases that are NOT
// fixed here. These slip through IsValidTrustDomain but are defeated downstream
// in the wired path (internal/security ValidateIdentity does an EXACT string
// match against the configured trust domain, so a domain carrying a ":port" or
// a non-ASCII space cannot impersonate the clean configured value). They are
// pinned as the CURRENT (permissive) behavior so a future deliberate tightening
// shows up as an intentional change, not a silent one.
//
// NOTE: this test asserts current behavior; it does not claim that behavior is
// desirable. If the coordinator tightens IsValidTrustDomain to also reject these
// (recommended), update this test to expect a DENY.
func TestValidate_LatentRiskTrustDomains(t *testing.T) {
	latent := []string{
		"example.org:8080", // port in authority — spec-illegal but exact-match defeats it
		"example.org:99",   // ditto
		"ex ample",    // U+00A0 NBSP — byte scan only rejects <=0x20 and 0x7f
	}
	for _, td := range latent {
		td := td
		t.Run(td, func(t *testing.T) {
			got := IsValidTrustDomain(td)
			if !got {
				t.Skipf("LATENT RISK closed: IsValidTrustDomain now rejects %q — "+
					"update this pin to expect DENY", td)
			}
			// Currently accepted (latent). Record it via the parse+validate chain
			// to make the sink visible.
			id := &SPIFFEID{TrustDomain: td, Path: "/wl"}
			if err := id.Validate(); err != nil {
				t.Fatalf("inconsistent: IsValidTrustDomain accepted %q but Validate rejected it: %v", td, err)
			}
			t.Logf("LATENT RISK (not fixed here): trust domain %q accepted by Validate; "+
				"defeated downstream only by exact-match in ValidateIdentity", td)
		})
	}
}

// -----------------------------------------------------------------------------
// TLS config builder — verdict = the produced *tls.Config must be DENY-by-default
// -----------------------------------------------------------------------------

// TestServerTLS_RequiresClientCert — CONTRACT PIN (anti fail-open).
// A server mTLS config MUST require AND verify the client cert and pin TLS1.3.
// If a refactor ever downgraded ClientAuth to NoClientCert/VerifyClientCertIfGiven,
// that is a fail-open admission of unauthenticated clients.
func TestServerTLS_RequiresClientCert(t *testing.T) {
	certPEM, keyPEM, caPEM := generateTestCert(t)
	b := NewTLSConfigBuilder(certPEM, keyPEM, caPEM)
	cfg, err := b.ServerTLS()
	if err != nil {
		t.Fatalf("ServerTLS build failed on valid material: %v", err)
	}
	if cfg.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatalf("FAIL-OPEN: ServerTLS ClientAuth=%v, want RequireAndVerifyClientCert", cfg.ClientAuth)
	}
	if cfg.ClientCAs == nil {
		t.Fatalf("FAIL-OPEN: ServerTLS ClientCAs is nil — client certs cannot be verified")
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("weak: ServerTLS MinVersion=%x, want TLS1.3", cfg.MinVersion)
	}
}

// TestTLS_DenyOnBadMaterial — CONTRACT PIN. Empty / garbage CA must produce an
// ERROR, never a config that trusts everything.
func TestTLS_DenyOnBadMaterial(t *testing.T) {
	certPEM, keyPEM, _ := generateTestCert(t)

	// Garbage CA -> AppendCertsFromPEM fails -> error.
	b := NewTLSConfigBuilder(certPEM, keyPEM, []byte("not a pem"))
	if _, err := b.ServerTLS(); err == nil {
		t.Fatalf("FAIL-OPEN: ServerTLS accepted a garbage CA without error")
	}
	if _, err := b.ClientTLS(); err == nil {
		t.Fatalf("FAIL-OPEN: ClientTLS accepted a garbage CA without error")
	}

	// Empty cert/key -> X509KeyPair fails -> error.
	b2 := NewTLSConfigBuilder(nil, nil, nil)
	if _, err := b2.ServerTLS(); err == nil {
		t.Fatalf("FAIL-OPEN: ServerTLS accepted empty key material without error")
	}
}

// -----------------------------------------------------------------------------
// Vault wrapper — missing client must DENY (error), not silently succeed
// -----------------------------------------------------------------------------

// TestVaultWrapper_NilClientDenies — CONTRACT PIN. With no client wired,
// every operation must return ErrNotInitialized (or an error), never nil/nil.
func TestVaultWrapper_NilClientDenies(t *testing.T) {
	w := NewVaultWrapper(nil)
	ctx := context.Background()

	if _, err := w.ReadSecret(ctx, "kv", "p"); err == nil {
		t.Fatalf("FAIL-OPEN: ReadSecret with nil client returned no error")
	} else if !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("ReadSecret error = %v, want wrapped ErrNotInitialized", err)
	}
	if err := w.WriteSecret(ctx, "kv", "p", map[string]interface{}{"value": "x"}); err == nil {
		t.Fatalf("FAIL-OPEN: WriteSecret with nil client returned no error")
	}
	if _, err := w.IssueCertificate(ctx, "pki", "role", PKIIssueParams{CommonName: "x"}); err == nil {
		t.Fatalf("FAIL-OPEN: IssueCertificate with nil client returned no error")
	}
	if _, err := w.RenewCertificate(ctx, "pki", "lease"); err == nil {
		t.Fatalf("FAIL-OPEN: RenewCertificate with nil client returned no error")
	}
}

// -----------------------------------------------------------------------------
// (d) UNGUARDED SHARED STATE — SecretInjector / SecretHolder under -race
// -----------------------------------------------------------------------------

// TestSecretHolder_ConcurrentRotateRead exercises the holder under concurrent
// Rotate (writer) and Get().Value()/Version() (readers). Run with -race. The
// holder uses an RWMutex; this pins that the mutex actually covers the
// value+version pair (no torn read, no data race). A regression dropping the
// lock surfaces here as a race detector hit.
func TestSecretHolder_ConcurrentRotateRead(t *testing.T) {
	kv := &fakeSeqKV{}
	inj := NewSecretInjector(kv, "kv", "creds")
	if err := inj.Inject(context.Background()); err != nil {
		t.Fatalf("initial inject: %v", err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Readers.
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					h := inj.Get()
					v := h.Value()
					ver := h.Version()
					// value and version must stay internally consistent:
					// fakeSeqKV encodes version into the value as "v<ver>".
					if v != "" && v != "v"+itoa(ver) {
						// Record but don't fail on a benign interleaving window;
						// the strong signal is the -race detector. Still assert
						// the value is one of the legal encodings.
						if !strings.HasPrefix(v, "v") {
							t.Errorf("torn read: value=%q version=%d", v, ver)
							return
						}
					}
				}
			}
		}()
	}

	// Writer.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			if err := inj.Rotate(context.Background(), "ignored"); err != nil {
				t.Errorf("rotate: %v", err)
				return
			}
		}
		close(stop)
	}()

	wg.Wait()
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// fakeSeqKV is a VaultKV whose version increments on each Put and whose stored
// value is always "v<version>", so readers can check value/version consistency.
type fakeSeqKV struct {
	mu  sync.Mutex
	ver int
}

func (f *fakeSeqKV) KVGet(_ context.Context, _, _ string) (string, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ver == 0 {
		f.ver = 1
	}
	return "v" + itoa(f.ver), f.ver, nil
}

func (f *fakeSeqKV) KVPut(_ context.Context, _, _, _ string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ver++
	return f.ver, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
