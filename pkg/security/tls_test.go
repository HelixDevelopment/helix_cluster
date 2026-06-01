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

// TestNewTLSConfigBuilder verifies that the PEM bytes supplied to
// NewTLSConfigBuilder are actually USED in the produced *tls.Config — not just
// stored and ignored. Three behavioral properties are asserted:
//
//  1. ServerTLS: the ClientCAs pool carries exactly the subject of the CA cert
//     minted for this test run (proves caPEM was parsed into the pool).
//  2. ClientTLS: the RootCAs pool carries exactly the same CA subject.
//  3. Both configs expose the leaf certificate (certPEM/keyPEM) via
//     Certificates[0] with the exact serial number of the minted leaf cert
//     (proves the cert+key PEM pair was loaded, not a default/zero value).
//
// §1.1 mutation pair: removing the caPool.AppendCertsFromPEM call in
// ServerTLS / ClientTLS (or swapping certPEM for nil) causes this test to
// FAIL because the pool subjects / leaf serial become wrong.
func TestNewTLSConfigBuilder(t *testing.T) {
	cert, key, ca := generateTestCert(t)

	// Parse the CA cert so we can compare subjects and serial numbers below.
	caBlock, _ := pem.Decode(ca)
	if caBlock == nil {
		t.Fatal("failed to decode CA PEM block")
	}
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatalf("failed to parse CA certificate: %v", err)
	}

	// Parse the leaf cert so we can compare the serial number below.
	leafBlock, _ := pem.Decode(cert)
	if leafBlock == nil {
		t.Fatal("failed to decode leaf cert PEM block")
	}
	leafCert, err := x509.ParseCertificate(leafBlock.Bytes)
	if err != nil {
		t.Fatalf("failed to parse leaf certificate: %v", err)
	}

	b := NewTLSConfigBuilder(cert, key, ca)

	t.Run("ServerTLS_CAPoolContainsSuppliedCA", func(t *testing.T) {
		cfg, err := b.ServerTLS()
		if err != nil {
			t.Fatalf("ServerTLS: %v", err)
		}
		if cfg.ClientCAs == nil {
			t.Fatal("ClientCAs pool is nil — caPEM was not wired")
		}

		// Verify the supplied CA cert is trusted by confirming the minted CA
		// cert itself passes verification against the pool.
		verifyOpts := x509.VerifyOptions{
			Roots:     cfg.ClientCAs,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		}
		// Create a copy of caCert with extended key usages required for Verify.
		// Since caCert is a CA (IsCA=true) we verify the leaf cert's chain.
		leafCopy, _ := x509.ParseCertificate(leafBlock.Bytes)
		verifyOpts2 := x509.VerifyOptions{
			Roots:     cfg.ClientCAs,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		}
		if _, verr := leafCopy.Verify(verifyOpts2); verr != nil {
			t.Errorf("leaf cert does not verify against ClientCAs pool — wrong CA wired: %v", verr)
		}
		_ = verifyOpts // consumed above

		// Also check the CA subject is present in the pool's subjects list to
		// directly prove the supplied caPEM was parsed into the pool.
		subjects := cfg.ClientCAs.Subjects() //nolint:staticcheck // pool inspection
		if len(subjects) == 0 {
			t.Fatal("ClientCAs pool is empty — caPEM was not appended")
		}
		found := false
		for _, raw := range subjects {
			if string(raw) == string(caCert.RawSubject) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("CA subject %q not found in ClientCAs pool subjects — wrong CA wired", caCert.Subject)
		}

		// Assert the leaf certificate (certPEM/keyPEM) is wired: the serial
		// number of the first Certificates entry must match our minted leaf.
		if len(cfg.Certificates) == 0 {
			t.Fatal("Certificates slice is empty — certPEM/keyPEM not wired")
		}
		wiredLeaf, parseErr := x509.ParseCertificate(cfg.Certificates[0].Certificate[0])
		if parseErr != nil {
			t.Fatalf("failed to parse wired leaf cert DER: %v", parseErr)
		}
		if wiredLeaf.SerialNumber.Cmp(leafCert.SerialNumber) != 0 {
			t.Errorf("wired leaf serial %v != supplied leaf serial %v — wrong cert loaded",
				wiredLeaf.SerialNumber, leafCert.SerialNumber)
		}
	})

	t.Run("ClientTLS_CAPoolContainsSuppliedCA", func(t *testing.T) {
		cfg, err := b.ClientTLS()
		if err != nil {
			t.Fatalf("ClientTLS: %v", err)
		}
		if cfg.RootCAs == nil {
			t.Fatal("RootCAs pool is nil — caPEM was not wired")
		}

		// Verify the supplied CA is trusted by confirming the leaf cert's chain.
		verifyOpts := x509.VerifyOptions{
			Roots:     cfg.RootCAs,
			DNSName:   "localhost",
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}
		leafCopy, _ := x509.ParseCertificate(leafBlock.Bytes)
		if _, verr := leafCopy.Verify(verifyOpts); verr != nil {
			t.Errorf("leaf cert does not verify against RootCAs pool — wrong CA wired: %v", verr)
		}

		subjects := cfg.RootCAs.Subjects() //nolint:staticcheck
		if len(subjects) == 0 {
			t.Fatal("RootCAs pool is empty — caPEM was not appended")
		}
		found := false
		for _, raw := range subjects {
			if string(raw) == string(caCert.RawSubject) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("CA subject %q not found in RootCAs pool subjects — wrong CA wired", caCert.Subject)
		}

		// Assert the leaf certificate (certPEM/keyPEM) is wired.
		if len(cfg.Certificates) == 0 {
			t.Fatal("Certificates slice is empty — certPEM/keyPEM not wired")
		}
		wiredLeaf, parseErr := x509.ParseCertificate(cfg.Certificates[0].Certificate[0])
		if parseErr != nil {
			t.Fatalf("failed to parse wired leaf cert DER: %v", parseErr)
		}
		if wiredLeaf.SerialNumber.Cmp(leafCert.SerialNumber) != 0 {
			t.Errorf("wired leaf serial %v != supplied leaf serial %v — wrong cert loaded",
				wiredLeaf.SerialNumber, leafCert.SerialNumber)
		}
	})

	t.Run("RealHandshake_PEMBytesActuallyUsed", func(t *testing.T) {
		// Sink-side behavioral proof: a real TLS listener + dial using ONLY
		// the configs produced from the supplied PEM bytes must complete a
		// mutually-authenticated handshake. If certPEM/keyPEM/caPEM were
		// ignored (e.g. replaced with zero values) the handshake would fail.
		serverCfg, err := b.ServerTLS()
		if err != nil {
			t.Fatalf("ServerTLS: %v", err)
		}
		ln, lnErr := tls.Listen("tcp", "127.0.0.1:0", serverCfg)
		if lnErr != nil {
			t.Fatalf("listen: %v", lnErr)
		}
		t.Cleanup(func() { _ = ln.Close() })

		done := make(chan struct{})
		go func() {
			defer close(done)
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			defer conn.Close()
			tc, ok := conn.(*tls.Conn)
			if ok {
				_ = tc.Handshake()
			}
		}()

		clientCfg, err := b.ClientTLS()
		if err != nil {
			t.Fatalf("ClientTLS: %v", err)
		}
		clientCfg.ServerName = "localhost"

		conn, dialErr := tls.DialWithDialer(
			&net.Dialer{Timeout: 5 * time.Second},
			"tcp", ln.Addr().String(), clientCfg,
		)
		if dialErr != nil {
			t.Fatalf("handshake failed — PEM bytes not wired correctly: %v", dialErr)
		}
		defer conn.Close()

		st := conn.ConnectionState()
		if !st.HandshakeComplete {
			t.Fatal("handshake did not complete")
		}
		if st.Version != tls.VersionTLS13 {
			t.Errorf("expected negotiated TLS 1.3, got %x", st.Version)
		}
		// Verify the server presented OUR minted leaf cert (by serial).
		if len(st.PeerCertificates) == 0 {
			t.Fatal("server presented no peer certificate")
		}
		serverPresentedSerial := st.PeerCertificates[0].SerialNumber
		if serverPresentedSerial.Cmp(leafCert.SerialNumber) != 0 {
			t.Errorf("server presented cert serial %v != expected leaf serial %v — wrong PEM wired",
				serverPresentedSerial, leafCert.SerialNumber)
		}
		<-done
	})
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
