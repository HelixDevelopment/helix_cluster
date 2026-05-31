package security

import (
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
)

func generateTestCert(t *testing.T) (certPEM, keyPEM, caPEM []byte) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{Organization: []string{"Test CA"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		IsCA:         true,
		BasicConstraintsValid: true,
	}
	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caCertDER)
	if err != nil {
		t.Fatal(err)
	}

	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "test-server"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     []string{"localhost"},
	}
	serverCertDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverCertDER})
	keyBytes, err := x509.MarshalECPrivateKey(serverKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCertDER})
	return certPEM, keyPEM, caPEM
}

func TestNewTLSConfigBuilder(t *testing.T) {
	cert, key, ca := generateTestCert(t)
	b := NewTLSConfigBuilder(cert, key, ca)
	if b == nil {
		t.Fatal("expected non-nil builder")
	}
}

func TestServerTLS(t *testing.T) {
	cert, key, ca := generateTestCert(t)
	b := NewTLSConfigBuilder(cert, key, ca)

	cfg, err := b.ServerTLS()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if len(cfg.Certificates) != 1 {
		t.Errorf("expected 1 certificate, got %d", len(cfg.Certificates))
	}
	if cfg.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Errorf("expected RequireAndVerifyClientCert, got %v", cfg.ClientAuth)
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("expected TLS 1.3, got %x", cfg.MinVersion)
	}
}

func TestClientTLS(t *testing.T) {
	cert, key, ca := generateTestCert(t)
	b := NewTLSConfigBuilder(cert, key, ca)

	cfg, err := b.ClientTLS()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if len(cfg.Certificates) != 1 {
		t.Errorf("expected 1 certificate, got %d", len(cfg.Certificates))
	}
	if cfg.RootCAs == nil {
		t.Error("expected RootCAs to be set")
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("expected TLS 1.3, got %x", cfg.MinVersion)
	}
}

func TestServerTLS_BadCert(t *testing.T) {
	b := NewTLSConfigBuilder([]byte("not-a-cert"), []byte("not-a-key"), []byte("not-a-ca"))
	_, err := b.ServerTLS()
	if err == nil {
		t.Error("expected error for invalid cert")
	}
}

func TestClientTLS_BadCA(t *testing.T) {
	cert, key, _ := generateTestCert(t)
	b := NewTLSConfigBuilder(cert, key, []byte("not-a-ca"))
	_, err := b.ClientTLS()
	if err == nil {
		t.Error("expected error for invalid CA")
	}
}

// generateForeignClientCert mints a client certificate signed by a brand-new,
// independent CA (different from the one returned by generateTestCert). It is
// used to prove the mTLS server rejects clients whose chain it does not trust.
func generateForeignClientCert(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(100),
		Subject:               pkix.Name{Organization: []string{"Foreign CA"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	caCertDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caCertDER)
	if err != nil {
		t.Fatal(err)
	}

	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(101),
		Subject:      pkix.Name{CommonName: "foreign-client"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientCertDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, caCert, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientCertDER})
	keyBytes, err := x509.MarshalECPrivateKey(clientKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	return certPEM, keyPEM
}

// startEchoServer stands up a real tls.Listener using the package's ServerTLS
// config and serves a single-connection echo loop in a goroutine. It returns
// the listener address. The listener is closed via t.Cleanup.
func startEchoServer(t *testing.T, serverCfg *tls.Config) string {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", serverCfg)
	if err != nil {
		t.Fatalf("failed to start TLS listener: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed
			}
			go func(c net.Conn) {
				defer c.Close()
				// Force the handshake so client-cert verification happens
				// even if no application bytes are exchanged.
				if tc, ok := c.(*tls.Conn); ok {
					if err := tc.Handshake(); err != nil {
						return
					}
				}
				_, _ = io.Copy(c, c) // echo
			}(conn)
		}
	}()
	return ln.Addr().String()
}

// TestMTLSHandshake_ValidClientSucceeds is the sink-side proof that ServerTLS +
// ClientTLS actually negotiate a mutually-authenticated TLS 1.3 session: a
// client holding a same-CA certificate completes the handshake AND a byte
// round-trips through the echo server.
func TestMTLSHandshake_ValidClientSucceeds(t *testing.T) {
	cert, key, ca := generateTestCert(t)
	b := NewTLSConfigBuilder(cert, key, ca)

	serverCfg, err := b.ServerTLS()
	if err != nil {
		t.Fatalf("ServerTLS: %v", err)
	}
	addr := startEchoServer(t, serverCfg)

	clientCfg, err := b.ClientTLS()
	if err != nil {
		t.Fatalf("ClientTLS: %v", err)
	}
	clientCfg.ServerName = "localhost" // matches server cert SAN

	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", addr, clientCfg)
	if err != nil {
		t.Fatalf("valid client handshake failed: %v", err)
	}
	defer conn.Close()

	state := conn.ConnectionState()
	if !state.HandshakeComplete {
		t.Fatal("handshake did not complete")
	}
	if state.Version != tls.VersionTLS13 {
		t.Errorf("expected negotiated TLS 1.3, got %x", state.Version)
	}
	if len(state.PeerCertificates) == 0 {
		t.Error("expected server to present a peer certificate")
	}

	// Sink-side proof: a byte actually round-trips over the mTLS channel.
	want := []byte("helix-mtls-ping")
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(want); err != nil {
		t.Fatalf("write over mTLS conn: %v", err)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read echo over mTLS conn: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("echo mismatch: want %q, got %q", want, got)
	}
}

// assertMTLSRejected dials the mTLS echo server with the given (insufficient)
// client config and asserts the connection is rejected by the server's
// RequireAndVerifyClientCert policy.
//
// Note on TLS 1.3: client authentication is verified by the server AFTER the
// client considers its own handshake complete (post-handshake / CertificateRequest
// flow). Consequently tls.Dial may return nil; the rejection is surfaced as a
// fatal alert on the first application read/write ("certificate required" /
// "unknown certificate authority"). We therefore drive a real I/O exchange and
// require it to fail — this is the genuine end-user-observable rejection.
func assertMTLSRejected(t *testing.T, addr string, clientCfg *tls.Config) {
	t.Helper()
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", addr, clientCfg)
	if err != nil {
		// Rejected outright during the handshake — acceptable.
		return
	}
	defer conn.Close()

	if derr := conn.SetDeadline(time.Now().Add(5 * time.Second)); derr != nil {
		t.Fatal(derr)
	}
	// Attempt an application exchange; the server must refuse to echo because
	// client-cert verification failed.
	if _, werr := conn.Write([]byte("ping")); werr != nil {
		return // write surfaced the server's fatal alert — rejected.
	}
	buf := make([]byte, 4)
	_, rerr := io.ReadFull(conn, buf)
	if rerr == nil {
		t.Fatal("expected mTLS rejection, but the server echoed data to an unauthenticated client")
	}
	// A clean EOF would mean the server accepted then closed without an alert,
	// which would NOT prove client-cert enforcement. Require a real TLS error.
	if rerr == io.EOF {
		t.Fatal("expected a TLS rejection error, got clean EOF (client auth may not be enforced)")
	}
	var netErr net.Error
	if errors.As(rerr, &netErr) && netErr.Timeout() {
		t.Fatalf("expected certificate rejection, got timeout: %v", rerr)
	}
}

// TestMTLSHandshake_NoClientCertRejected proves RequireAndVerifyClientCert is
// actually enforced: a client that trusts the CA but presents NO certificate is
// rejected.
func TestMTLSHandshake_NoClientCertRejected(t *testing.T) {
	cert, key, ca := generateTestCert(t)
	b := NewTLSConfigBuilder(cert, key, ca)

	serverCfg, err := b.ServerTLS()
	if err != nil {
		t.Fatalf("ServerTLS: %v", err)
	}
	addr := startEchoServer(t, serverCfg)

	// Trust the server CA but present no client certificate.
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(ca) {
		t.Fatal("failed to build CA pool")
	}
	noCertCfg := &tls.Config{
		RootCAs:    caPool,
		ServerName: "localhost",
		MinVersion: tls.VersionTLS13,
	}
	assertMTLSRejected(t, addr, noCertCfg)
}

// TestMTLSHandshake_ForeignClientCertRejected proves the server rejects a client
// presenting a syntactically valid certificate signed by an UNTRUSTED CA.
func TestMTLSHandshake_ForeignClientCertRejected(t *testing.T) {
	cert, key, ca := generateTestCert(t)
	b := NewTLSConfigBuilder(cert, key, ca)

	serverCfg, err := b.ServerTLS()
	if err != nil {
		t.Fatalf("ServerTLS: %v", err)
	}
	addr := startEchoServer(t, serverCfg)

	foreignCert, foreignKey := generateForeignClientCert(t)
	clientCert, err := tls.X509KeyPair(foreignCert, foreignKey)
	if err != nil {
		t.Fatalf("load foreign client cert: %v", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(ca) {
		t.Fatal("failed to build CA pool")
	}
	foreignCfg := &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      caPool,
		ServerName:   "localhost",
		MinVersion:   tls.VersionTLS13,
	}
	assertMTLSRejected(t, addr, foreignCfg)
}
