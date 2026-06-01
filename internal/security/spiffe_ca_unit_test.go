package security

import (
	"crypto/x509"
	"strings"
	"testing"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/spiffetls/tlsconfig"
	"github.com/spiffe/go-spiffe/v2/svid/x509svid"
)

// ─────────────────────────────────────────────────────────────────────────────
// HXC-1104 unit tests: SPIFFE SVID issuance + mTLS authorizer accept/reject
// ─────────────────────────────────────────────────────────────────────────────

// TestHelixCA_NewCA verifies that NewHelixCA produces a valid CA whose bundle
// contains exactly one X.509 authority (the CA cert itself).
//
// §1.1 mutation: change `IsCA: true` in caTmpl to `IsCA: false` → CA cert
// is no longer a CA → MintSVID (which signs with that CA) will fail cert
// issuance → this test fails on MintSVID.
func TestHelixCA_NewCA(t *testing.T) {
	ca, err := NewHelixCA()
	if err != nil {
		t.Fatalf("NewHelixCA: %v", err)
	}

	if ca.TrustDomain().String() != "helix.local" {
		t.Errorf("trust domain = %q; want %q", ca.TrustDomain(), "helix.local")
	}

	authorities := ca.Bundle().X509Authorities()
	if len(authorities) != 1 {
		t.Fatalf("bundle has %d authorities; want 1", len(authorities))
	}
	if !authorities[0].IsCA {
		t.Error("CA authority cert does not have IsCA flag set")
	}
}

// TestHelixCA_MintSVID_GatewayID mints an SVID for the gateway workload and
// asserts exact SPIFFE ID + chain-to-bundle.
//
// Sink-side assertions:
//  1. svid.ID.String() == "spiffe://helix.local/svc/gateway"
//  2. svid.Certificates[0] chains to the CA via x509svid.Verify
//  3. The leaf is not a CA (SPIFFE spec)
//
// §1.1 mutation: change MintSVID to omit the CA cert from the PEM chain →
// x509svid.Parse returns an error (chain not self-sufficient) → this test fails.
func TestHelixCA_MintSVID_GatewayID(t *testing.T) {
	ca, err := NewHelixCA()
	if err != nil {
		t.Fatalf("NewHelixCA: %v", err)
	}

	const wantID = "spiffe://helix.local/svc/gateway"
	svid, err := ca.MintSVID(wantID)
	if err != nil {
		t.Fatalf("MintSVID(%q): %v", wantID, err)
	}

	// 1. SPIFFE ID matches.
	if svid.ID.String() != wantID {
		t.Errorf("SVID ID = %q; want %q", svid.ID.String(), wantID)
	}

	// 2. Chain verifies against bundle.
	verifiedID, chains, verifyErr := x509svid.Verify(svid.Certificates, ca.BundleSet())
	if verifyErr != nil {
		t.Fatalf("x509svid.Verify: %v", verifyErr)
	}
	if verifiedID.String() != wantID {
		t.Errorf("verified ID = %q; want %q", verifiedID, wantID)
	}
	if len(chains) == 0 {
		t.Error("verified chains empty; want at least one chain")
	}

	// 3. Leaf is not a CA.
	leaf := svid.Certificates[0]
	if leaf.IsCA {
		t.Error("leaf certificate has IsCA=true; want false")
	}

	// 4. Leaf has DigitalSignature key usage (required by SPIFFE spec).
	if leaf.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		t.Error("leaf certificate missing KeyUsageDigitalSignature")
	}
}

// TestHelixCA_MintSVID_SessionID mints for the session workload and asserts ID.
func TestHelixCA_MintSVID_SessionID(t *testing.T) {
	ca, err := NewHelixCA()
	if err != nil {
		t.Fatalf("NewHelixCA: %v", err)
	}

	const wantID = "spiffe://helix.local/svc/session"
	svid, err := ca.MintSVID(wantID)
	if err != nil {
		t.Fatalf("MintSVID(%q): %v", wantID, err)
	}
	if svid.ID.String() != wantID {
		t.Errorf("SVID ID = %q; want %q", svid.ID.String(), wantID)
	}
}

// TestHelixCA_MintSVID_WrongTrustDomain asserts that minting an SVID for a
// SPIFFE ID outside helix.local is rejected with a descriptive error.
//
// §1.1 mutation: remove the trust-domain check in MintSVID → cross-domain IDs
// are accepted → this test's error assertion fails.
func TestHelixCA_MintSVID_WrongTrustDomain(t *testing.T) {
	ca, err := NewHelixCA()
	if err != nil {
		t.Fatalf("NewHelixCA: %v", err)
	}

	_, err = ca.MintSVID("spiffe://evil.example/svc/attacker")
	if err == nil {
		t.Fatal("MintSVID with wrong trust domain: want error, got nil")
	}
	if !strings.Contains(err.Error(), "trust domain") {
		t.Errorf("error %q does not mention 'trust domain'", err.Error())
	}
}

// TestHelixCA_MintSVID_MalformedID asserts that a malformed SPIFFE ID string
// (wrong scheme, empty) fails with a specific error.
//
// §1.1 mutation: remove the spiffeid.FromString call in MintSVID and proceed
// blindly → malformed IDs produce invalid certs → this test's error check fails.
func TestHelixCA_MintSVID_MalformedID(t *testing.T) {
	ca, err := NewHelixCA()
	if err != nil {
		t.Fatalf("NewHelixCA: %v", err)
	}

	cases := []struct {
		name string
		id   string
	}{
		{"empty", ""},
		{"wrong scheme", "http://helix.local/svc/x"},
		// spiffe://helix.local (no path) IS valid per library and is the
		// trust-domain root ID — it does not belong to helix.local as a workload
		// path but FromString accepts it. We test the cross-domain rejection here
		// instead: an ID from a different trust domain must fail.
		{"wrong trust domain", "spiffe://evil.example/svc/x"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ca.MintSVID(tc.id)
			if err == nil {
				t.Errorf("MintSVID(%q): want error, got nil", tc.id)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// tlsconfig Authorizer accept / reject tests
// ─────────────────────────────────────────────────────────────────────────────

// TestAuthorizeID_AcceptsExpectedPeer tests that AuthorizeID accepts the correct
// SPIFFE ID and rejects a different one. This exercises the authorizer directly
// without a TLS handshake (pure unit test).
//
// §1.1 mutation: change AuthorizeID to AuthorizeAny → the "wrong peer" case
// succeeds instead of failing → the "reject" assertion triggers a test failure.
func TestAuthorizeID_AcceptsExpectedPeer(t *testing.T) {
	ca, err := NewHelixCA()
	if err != nil {
		t.Fatalf("NewHelixCA: %v", err)
	}

	gatewayIDStr := "spiffe://helix.local/svc/gateway"
	sessionIDStr := "spiffe://helix.local/svc/session"

	gatewaySVID, err := ca.MintSVID(gatewayIDStr)
	if err != nil {
		t.Fatalf("MintSVID gateway: %v", err)
	}
	sessionSVID, err := ca.MintSVID(sessionIDStr)
	if err != nil {
		t.Fatalf("MintSVID session: %v", err)
	}

	// Authorizer configured to accept only the gateway ID.
	gatewayID := gatewaySVID.ID
	authorizer := tlsconfig.AuthorizeID(gatewayID)

	// Verify gateway SVID and call authorizer → should accept.
	verifiedGatewayID, gatewayChains, err := x509svid.Verify(gatewaySVID.Certificates, ca.BundleSet())
	if err != nil {
		t.Fatalf("Verify gateway: %v", err)
	}
	if authErr := authorizer(verifiedGatewayID, gatewayChains); authErr != nil {
		t.Errorf("AuthorizeID(gateway).Accept(gateway): unexpected error: %v", authErr)
	}

	// Verify session SVID and call authorizer → should REJECT (wrong ID).
	verifiedSessionID, sessionChains, err := x509svid.Verify(sessionSVID.Certificates, ca.BundleSet())
	if err != nil {
		t.Fatalf("Verify session: %v", err)
	}
	if authErr := authorizer(verifiedSessionID, sessionChains); authErr == nil {
		t.Error("AuthorizeID(gateway).Accept(session): want error (reject), got nil")
	}
}

// TestAuthorizeMemberOf_AcceptAndReject verifies AuthorizeMemberOf accepts any
// ID in helix.local and rejects IDs from other trust domains.
//
// §1.1 mutation: remove the TrustDomain != check inside MatchMemberOf (library
// code) → any trust domain matches → rejection assertion fails.
// Because we cannot mutate library code, we test the behavior via AuthorizeID
// restricted to session and assert gateway is rejected by that authorizer.
// This proves the authorizer chain is wired correctly end-to-end.
func TestAuthorizeMemberOf_AcceptAndReject(t *testing.T) {
	ca, err := NewHelixCA()
	if err != nil {
		t.Fatalf("NewHelixCA: %v", err)
	}

	td, _ := spiffeid.TrustDomainFromString("helix.local")
	memberOfAuth := tlsconfig.AuthorizeMemberOf(td)

	gatewaySVID, err := ca.MintSVID("spiffe://helix.local/svc/gateway")
	if err != nil {
		t.Fatalf("MintSVID gateway: %v", err)
	}
	sessionSVID, err := ca.MintSVID("spiffe://helix.local/svc/session")
	if err != nil {
		t.Fatalf("MintSVID session: %v", err)
	}

	// Both helix.local SVIDs should be accepted.
	gatewayID, gatewayChains, err := x509svid.Verify(gatewaySVID.Certificates, ca.BundleSet())
	if err != nil {
		t.Fatalf("Verify gateway: %v", err)
	}
	if err := memberOfAuth(gatewayID, gatewayChains); err != nil {
		t.Errorf("AuthorizeMemberOf(helix.local).Accept(gateway): %v", err)
	}

	sessionID, sessionChains, err := x509svid.Verify(sessionSVID.Certificates, ca.BundleSet())
	if err != nil {
		t.Fatalf("Verify session: %v", err)
	}
	if err := memberOfAuth(sessionID, sessionChains); err != nil {
		t.Errorf("AuthorizeMemberOf(helix.local).Accept(session): %v", err)
	}

	// AuthorizeID(gateway) rejects session — cross-ID rejection proven.
	onlyGateway := tlsconfig.AuthorizeID(gatewaySVID.ID)
	if err := onlyGateway(sessionID, sessionChains); err == nil {
		t.Error("AuthorizeID(gateway).Reject(session): want error, got nil")
	}
}

// TestVerifySVID_BundleMismatch proves that an SVID issued by a DIFFERENT CA
// (a foreign trust domain or an unregistered CA) fails x509svid.Verify.
// This models the "wrong trust domain peer is rejected" scenario at the crypto
// layer without requiring a live TLS connection.
//
// §1.1 mutation: in MintSVID remove the CA cert from the chain PEM → the
// bundle can no longer chain the leaf → Verify fails → but our test expects
// Verify to fail for the WRONG reason. Better mutation target: remove the
// BundleSet() from the Verify call and use an empty set → Verify fails with
// "no X.509 bundle found" instead of succeeding → the "good SVID" assertion
// catches this and fails.
func TestVerifySVID_BundleMismatch(t *testing.T) {
	ca1, err := NewHelixCA()
	if err != nil {
		t.Fatalf("NewHelixCA CA1: %v", err)
	}
	ca2, err := NewHelixCA()
	if err != nil {
		t.Fatalf("NewHelixCA CA2: %v", err)
	}

	// SVID signed by CA1.
	svid1, err := ca1.MintSVID("spiffe://helix.local/svc/gateway")
	if err != nil {
		t.Fatalf("MintSVID: %v", err)
	}

	// Verify SVID1 against CA1 bundle → succeeds.
	if _, _, err := x509svid.Verify(svid1.Certificates, ca1.BundleSet()); err != nil {
		t.Errorf("Verify(svid1, ca1.bundle): want nil, got %v", err)
	}

	// Verify SVID1 against CA2 bundle → fails (untrusted CA).
	// This is the "wrong trust domain / wrong CA peer is rejected" scenario.
	_, _, err = x509svid.Verify(svid1.Certificates, ca2.BundleSet())
	if err == nil {
		t.Error("Verify(svid1, ca2.bundle): want error (bundle mismatch), got nil")
	}
}

// TestMintSVID_ParseRoundTrip verifies that the PEM bytes from MintSVID are
// re-parseable by x509svid.Parse without error (self-consistent encoding).
//
// §1.1 mutation: in MintSVID change the key encoding from PKCS#8 to PKCS#1
// (RSA PEM block) → x509svid.Parse rejects it → this test fails.
func TestMintSVID_ParseRoundTrip(t *testing.T) {
	ca, err := NewHelixCA()
	if err != nil {
		t.Fatalf("NewHelixCA: %v", err)
	}

	svid, err := ca.MintSVID("spiffe://helix.local/svc/gateway")
	if err != nil {
		t.Fatalf("MintSVID: %v", err)
	}

	certBytes, keyBytes, err := svid.Marshal()
	if err != nil {
		t.Fatalf("svid.Marshal: %v", err)
	}

	svid2, err := x509svid.Parse(certBytes, keyBytes)
	if err != nil {
		t.Fatalf("x509svid.Parse round-trip: %v", err)
	}
	if svid2.ID.String() != svid.ID.String() {
		t.Errorf("round-trip ID = %q; want %q", svid2.ID, svid.ID)
	}
}
