package doublecrypt_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/doublecrypt"
)

// ---------------------------------------------------------------------------
// Test-local helpers: real ECDSA P-256 CA + leaf certs via crypto/x509
// ---------------------------------------------------------------------------

// certBundle holds PEM-encoded cert + key.
type certBundle struct {
	CertPEM []byte
	KeyPEM  []byte
	Cert    *x509.Certificate
	Key     *ecdsa.PrivateKey
}

// newCA mints a self-signed CA certificate (ECDSA P-256).
// NotBefore / NotAfter are set explicitly so no time.Now dependency leaks into
// logic under test.
func newCA(t *testing.T) *certBundle {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("CA key gen: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "helix-test-CA"},
		NotBefore:             time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:              time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CA cert creation: %v", err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("CA cert parse: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("CA key marshal: %v", err)
	}
	return &certBundle{
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		Cert:    parsed,
		Key:     key,
	}
}

// newLeaf mints a leaf certificate signed by ca, intended for server or client use.
func newLeaf(t *testing.T, cn string, isServer bool, ca *certBundle, serial int64) *certBundle {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("leaf key gen: %v", err)
	}
	extUsage := x509.ExtKeyUsageClientAuth
	if isServer {
		extUsage = x509.ExtKeyUsageServerAuth
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:     time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{extUsage},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		t.Fatalf("leaf cert creation: %v", err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("leaf cert parse: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("leaf key marshal: %v", err)
	}
	return &certBundle{
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		Cert:    parsed,
		Key:     key,
	}
}

// ---------------------------------------------------------------------------
// Table-driven test 1: happy path — real mTLS over net.Pipe()
//
// MUTATION PROOF: change ServerConn to return tls.Client(inner, cfg) (wrong
// role) — Handshake hangs / fails with a role-mismatch error; srvErr != nil →
// the "got bytes" assertion is never reached and the test fails.
// ---------------------------------------------------------------------------

func TestHappyPath_RealMTLS_SinkSideDataTransfer(t *testing.T) {
	// Mint one CA + one server cert + one client cert, all chained to the same CA.
	ca := newCA(t)
	srvBundle := newLeaf(t, "helix-server", true, ca, 2)
	cliBundle := newLeaf(t, "helix-client", false, ca, 3)

	serverCfg, err := doublecrypt.NewMTLSConfig(srvBundle.CertPEM, srvBundle.KeyPEM, ca.CertPEM, true)
	if err != nil {
		t.Fatalf("NewMTLSConfig(server): %v", err)
	}
	clientCfg, err := doublecrypt.NewMTLSConfig(cliBundle.CertPEM, cliBundle.KeyPEM, ca.CertPEM, false)
	if err != nil {
		t.Fatalf("NewMTLSConfig(client): %v", err)
	}
	// ServerName must match the leaf's DNSName for the client to verify the server.
	clientCfg.ServerName = "localhost"

	// Use net.Pipe() as the inner conn — the injected L3 stand-in.
	cPipe, sPipe := net.Pipe()
	defer cPipe.Close()
	defer sPipe.Close()

	// Unique plaintext payload — the sink-side oracle.
	want := []byte("helix-doublecrypt-integration-token:abc123unique")

	srvErrCh := make(chan error, 1)
	srvGotCh := make(chan []byte, 1)

	go func() {
		srv := doublecrypt.ServerConn(sPipe, serverCfg)
		if err := srv.Handshake(); err != nil {
			srvErrCh <- err
			return
		}
		// io.ReadFull guarantees exactly len(want) bytes are read before the
		// equality assertion — not structurally dependent on net.Pipe()'s
		// non-contractual single-Write = single-Read behaviour.
		// Mutation: replace io.ReadFull with io.ReadAtLeast(srv, buf, 1) →
		// on a fragmented transport a partial read makes bytes.Equal fail with a
		// misleading error; and replacing want with []byte("wrong") → bytes.Equal
		// returns false → t.Fatal fires.
		buf := make([]byte, len(want))
		if _, err := io.ReadFull(srv, buf); err != nil {
			srvErrCh <- err
			return
		}
		srvGotCh <- buf
		srvErrCh <- nil
	}()

	cli := doublecrypt.ClientConn(cPipe, clientCfg)
	if err := cli.Handshake(); err != nil {
		t.Fatalf("client Handshake failed (should succeed): %v", err)
	}
	if _, err := cli.Write(want); err != nil {
		t.Fatalf("client Write: %v", err)
	}

	// Wait for server goroutine.
	if err := <-srvErrCh; err != nil {
		t.Fatalf("server error: %v", err)
	}
	got := <-srvGotCh

	// SINK-SIDE assertion: the EXACT bytes must arrive decrypted at the server.
	// Mutation: replace `want` with `[]byte("wrong")` → bytes.Equal fails.
	if !bytes.Equal(got, want) {
		t.Fatalf("sink-side mismatch: server got %q, want %q", got, want)
	}
	// Confirm the TLS version used is TLS 1.3 (not a downgrade).
	state := cli.ConnectionState()
	if state.Version != tls.VersionTLS13 {
		t.Fatalf("expected TLS 1.3, got 0x%04x", state.Version)
	}
}

// ---------------------------------------------------------------------------
// Table-driven test 2: mutual-auth REAL — untrusted client cert must be REJECTED
//
// MUTATION PROOF: set serverCfg.ClientAuth = tls.NoClientCert → the server no
// longer rejects the untrusted client cert → srvErr == nil → the srvErr == nil
// branch fires → t.Fatal.  The assertion is deliberately SERVER-SIDE only
// because the server is the enforcing party for RequireAndVerifyClientCert;
// cliErr may or may not be non-nil (TLS alert propagation on net.Pipe() is
// timing-dependent) and must NOT be the load-bearing assertion.
// ---------------------------------------------------------------------------

func TestMutualAuth_UntrustedClientCert_HandshakeFails(t *testing.T) {
	// Trusted CA used by the server.
	trustedCA := newCA(t)
	srvBundle := newLeaf(t, "helix-server", true, trustedCA, 2)

	// A DIFFERENT, untrusted CA — the client presents a cert signed by this.
	untrustedCA := newCA(t)
	badClientBundle := newLeaf(t, "bad-client", false, untrustedCA, 10)

	serverCfg, err := doublecrypt.NewMTLSConfig(srvBundle.CertPEM, srvBundle.KeyPEM, trustedCA.CertPEM, true)
	if err != nil {
		t.Fatalf("NewMTLSConfig(server): %v", err)
	}
	// Client config: its RootCAs point to trustedCA so it trusts the server; but
	// its certificate is signed by the untrustedCA which the server does not accept.
	clientCfg, err := doublecrypt.NewMTLSConfig(badClientBundle.CertPEM, badClientBundle.KeyPEM, trustedCA.CertPEM, false)
	if err != nil {
		t.Fatalf("NewMTLSConfig(bad-client): %v", err)
	}
	clientCfg.ServerName = "localhost"

	cPipe, sPipe := net.Pipe()

	// Both sides MUST run concurrently: net.Pipe is synchronous (unbuffered), so
	// TLS alert writes on one side block until the other side reads.  Running both
	// Handshakes in goroutines prevents deadlock while still capturing all errors.
	srvErrCh := make(chan error, 1)
	cliErrCh := make(chan error, 1)

	go func() {
		srv := doublecrypt.ServerConn(sPipe, serverCfg)
		err := srv.Handshake()
		srv.Close()
		srvErrCh <- err
	}()
	go func() {
		cli := doublecrypt.ClientConn(cPipe, clientCfg)
		err := cli.Handshake()
		cli.Close()
		cliErrCh <- err
	}()

	cliErr := <-cliErrCh
	srvErr := <-srvErrCh

	// The SERVER is the enforcing party for RequireAndVerifyClientCert; it MUST
	// reject the untrusted client cert with a non-nil error.
	// Mutation: set serverCfg.ClientAuth = tls.NoClientCert → srvErr == nil →
	// t.Fatal fires.  This assertion correctly does NOT fire in the real case
	// (server errors with "certificate signed by unknown authority").
	if srvErr == nil {
		t.Fatal("server accepted untrusted client cert — RequireAndVerifyClientCert is not enforcing real chain verification")
	}
	// cliErr may or may not be non-nil depending on TLS alert propagation timing
	// on net.Pipe(); log it for diagnosis but do not assert on it.
	if cliErr != nil {
		t.Logf("client also got error (expected on net.Pipe alert propagation): %v", cliErr)
	}
	// NOTE: On net.Pipe() with synchronous TLS alert delivery, the 5-second
	// duration observed in this test is due to the TLS close_notify/alert
	// round-trip and the net.Pipe() synchronous-close wait — this is expected
	// behaviour, not a timeout bug.
}

// ---------------------------------------------------------------------------
// Table-driven test 3: NewMTLSConfig rejects malformed / empty caPEM
//
// MUTATION PROOF: remove the empty-check / pem.Decode guard in NewMTLSConfig →
// pool.AppendCertsFromPEM(nil) returns false → now returns a non-nil error as
// before — or if the check is removed entirely, garbage DER is parsed and
// x509.ParseCertificate returns an error → still non-nil.
// Simpler mutation: always return nil error from NewMTLSConfig → the != nil
// assertion fails.
// ---------------------------------------------------------------------------

func TestNewMTLSConfig_MalformedCAPEM_ReturnsError(t *testing.T) {
	ca := newCA(t)
	srvBundle := newLeaf(t, "helix-server", true, ca, 2)

	cases := []struct {
		name  string
		caPEM []byte
	}{
		{"empty", []byte{}},
		{"garbage", []byte("not a pem block")},
		{"wrong type", []byte("-----BEGIN RSA PRIVATE KEY-----\nYWJj\n-----END RSA PRIVATE KEY-----\n")},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := doublecrypt.NewMTLSConfig(srvBundle.CertPEM, srvBundle.KeyPEM, tc.caPEM, true)
			// SINK-SIDE assertion: the config must be nil and the error non-nil.
			if err == nil {
				t.Fatalf("expected error for caPEM=%q, got nil err and cfg=%v", tc.name, cfg)
			}
			if cfg != nil {
				t.Fatalf("expected nil cfg on error, got non-nil: %+v", cfg)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Test 4: DialWireGuard always returns ErrUnsupported and a nil conn
//
// MUTATION PROOF: change DialWireGuard to return (net.Pipe())[0], nil →
// errors.Is(err, ErrUnsupported) is false → t.Fatal fires; AND conn != nil →
// second assertion also fires.  Both together enforce CLAUDE-2: no fake conn.
// ---------------------------------------------------------------------------

func TestDialWireGuard_ReturnsErrUnsupported(t *testing.T) {
	conn, err := doublecrypt.DialWireGuard(context.Background(), "10.0.0.1:51820")

	// CLAUDE-2 / §7.1 sink-side: the conn MUST be nil.
	if conn != nil {
		t.Fatalf("expected nil conn from DialWireGuard, got %T — fabricated conn is a PASS-bluff", conn)
	}
	// The error MUST wrap ErrUnsupported.
	if !errors.Is(err, doublecrypt.ErrUnsupported) {
		t.Fatalf("expected errors.Is(err, ErrUnsupported)=true, got err=%v", err)
	}
}

// ---------------------------------------------------------------------------
// Test 5: NewMTLSConfig server sets RequireAndVerifyClientCert; client does not
//
// This test verifies the configuration values are wired correctly by inspecting
// the resulting *tls.Config fields directly — a structural check on top of the
// behavioral proof in tests 1 and 2.
//
// MUTATION PROOF: swap isServer=true/false arguments → ClientAuth is
// tls.NoClientCert on the "server" cfg → assertion fails.
// ---------------------------------------------------------------------------

func TestNewMTLSConfig_ServerVsClientFields(t *testing.T) {
	ca := newCA(t)
	srvBundle := newLeaf(t, "s", true, ca, 2)
	cliBundle := newLeaf(t, "c", false, ca, 3)

	srvCfg, err := doublecrypt.NewMTLSConfig(srvBundle.CertPEM, srvBundle.KeyPEM, ca.CertPEM, true)
	if err != nil {
		t.Fatalf("server config: %v", err)
	}
	cliCfg, err := doublecrypt.NewMTLSConfig(cliBundle.CertPEM, cliBundle.KeyPEM, ca.CertPEM, false)
	if err != nil {
		t.Fatalf("client config: %v", err)
	}

	// Server must require + verify the client cert.
	if srvCfg.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatalf("server ClientAuth = %v, want RequireAndVerifyClientCert", srvCfg.ClientAuth)
	}
	if srvCfg.ClientCAs == nil {
		t.Fatal("server ClientCAs must be non-nil")
	}
	if srvCfg.InsecureSkipVerify {
		t.Fatal("InsecureSkipVerify must never be set")
	}
	if srvCfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("server MinVersion = 0x%04x, want TLS 1.3", srvCfg.MinVersion)
	}

	// Client must use RootCAs (not ClientCAs) and not set InsecureSkipVerify.
	if cliCfg.RootCAs == nil {
		t.Fatal("client RootCAs must be non-nil")
	}
	if cliCfg.InsecureSkipVerify {
		t.Fatal("client InsecureSkipVerify must never be set")
	}
	if cliCfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("client MinVersion = 0x%04x, want TLS 1.3", cliCfg.MinVersion)
	}
}
