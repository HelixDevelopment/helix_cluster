package security

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/testing/evidence"
)

// generateIdentityCerts mints a CA, a server cert (CN "test-server", SAN
// localhost) and a client cert whose CommonName embeds the supplied per-run
// evidence token. All three share the same CA so the mTLS handshake can verify
// the client. The client CN is the load-bearing assertion: the server-side
// handshake exposes it via the verified peer chain, so we can prove the
// NEGOTIATED PEER IDENTITY rather than merely "some cert was presented".
func generateIdentityCerts(t *testing.T, clientCN string) (serverCertPEM, serverKeyPEM, clientCertPEM, clientKeyPEM, caPEM []byte) {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{Organization: []string{"Helix Test CA"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}

	mint := func(serial int64, cn string, dns []string, eku []x509.ExtKeyUsage) (certPEM, keyPEM []byte) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(serial),
			Subject:      pkix.Name{CommonName: cn},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(24 * time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  eku,
			DNSNames:     dns,
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		keyBytes, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
			pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	}

	serverCertPEM, serverKeyPEM = mint(2, "test-server", []string{"localhost"},
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth})
	clientCertPEM, clientKeyPEM = mint(3, clientCN, nil,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	return
}

// TestMTLS_NegotiatedPeerIdentity_Evidence is the §7.1-evidence proof that the
// package's ServerTLS/ClientTLS perform a REAL mutually-authenticated handshake
// and that the SERVER actually learns the client's verified identity. The
// per-run evidence token is embedded into the client certificate CommonName;
// the server captures the verified peer CN during the handshake and we prove
// that the recovered identity contains the token (positive sink evidence) and
// that the channel transitions from "no peer identity" to "token identity"
// (state delta). A non-functional mTLS (e.g. client auth not enforced, or peer
// chain discarded) cannot produce this evidence.
func TestMTLS_NegotiatedPeerIdentity_Evidence(t *testing.T) {
	e := evidence.NewT(t, "mtls-negotiated-peer-identity", t.TempDir())
	token := e.Token()
	clientCN := "spiffe-client-" + token

	serverCert, serverKey, clientCert, clientKey, ca := generateIdentityCerts(t, clientCN)

	serverB := NewTLSConfigBuilder(serverCert, serverKey, ca)
	serverCfg, err := serverB.ServerTLS()
	if err != nil {
		t.Fatalf("ServerTLS: %v", err)
	}

	// Channel the server uses to publish the verified client identity it
	// negotiated, so the test can assert it sink-side.
	peerIdentity := make(chan string, 1)

	ln, err := tls.Listen("tcp", "127.0.0.1:0", serverCfg)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		defer conn.Close()
		tc, ok := conn.(*tls.Conn)
		if !ok {
			return
		}
		if herr := tc.Handshake(); herr != nil {
			peerIdentity <- "" // empty => rejected/failed (positive-evidence FAIL)
			return
		}
		st := tc.ConnectionState()
		cn := ""
		if len(st.VerifiedChains) > 0 && len(st.VerifiedChains[0]) > 0 {
			// The leaf of the verified chain is the authenticated client.
			cn = st.VerifiedChains[0][0].Subject.CommonName
		}
		peerIdentity <- cn
		_, _ = io.Copy(tc, tc) // echo, so the client can also confirm liveness
	}()

	clientB := NewTLSConfigBuilder(clientCert, clientKey, ca)
	clientCfg, err := clientB.ClientTLS()
	if err != nil {
		t.Fatalf("ClientTLS: %v", err)
	}
	clientCfg.ServerName = "localhost"

	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", ln.Addr().String(), clientCfg)
	if err != nil {
		t.Fatalf("valid client handshake failed: %v", err)
	}
	defer conn.Close()

	st := conn.ConnectionState()
	if !st.HandshakeComplete {
		t.Fatal("handshake did not complete")
	}
	if st.Version != tls.VersionTLS13 {
		t.Errorf("expected negotiated TLS 1.3, got %x", st.Version)
	}

	var negotiatedCN string
	select {
	case negotiatedCN = <-peerIdentity:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for server to report negotiated peer identity")
	}

	// §7.1 #2 state DELTA: before the handshake the server knew no peer
	// identity (""); after, it negotiated the token-bearing identity.
	e.MustDelta(t, "peer-identity-negotiated", "", negotiatedCN)

	// §7.1 #3 POSITIVE evidence: the negotiated identity actually carries the
	// unique per-run token — proving the server read the REAL client cert, not
	// a stale/echoed value.
	e.MustPositive(t, "peer-cn-contains-token", negotiatedCN, token)

	if negotiatedCN != clientCN {
		t.Fatalf("negotiated peer CN mismatch: got %q, want %q", negotiatedCN, clientCN)
	}

	// Capture the verified peer certificate as a forensic artifact.
	if len(st.PeerCertificates) == 0 {
		t.Fatal("client did not receive server peer certificate")
	}
	if _, err := e.Artifact("negotiated_peer_cn.txt", []byte(negotiatedCN)); err != nil {
		t.Fatalf("artifact: %v", err)
	}
}
