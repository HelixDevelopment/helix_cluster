package doublecrypt_test

// Adversarial / sink-side probes for pkg/doublecrypt.
//
// SCOPE NOTE (honest): despite the package name, doublecrypt is NOT a byte-level
// layered-AEAD primitive with exported Encrypt/Decrypt/Wrap/Unwrap functions.
// It is a transport double-encryption COMPOSITION: real L7 mTLS (crypto/tls,
// TLS 1.3) over an injected L3 WireGuard net.Conn (DialWireGuard is an honest
// ErrUnsupported stub). The ONLY real cryptography implemented in this package
// is the mTLS layer built by NewMTLSConfig + ClientConn/ServerConn. Therefore
// the adversarial bug-classes from the brief map onto the mTLS layer as:
//
//   - "tamper a layer / flip a byte in ciphertext / auth tag"  -> flip a byte in
//     a live TLS 1.3 application-data record; the AEAD-protected record MUST be
//     REJECTED by the peer's Read (never silently return plaintext/garbage).
//   - "truncated ciphertext rejected"                          -> chop a record in
//     flight; Read MUST NOT yield forged authenticated plaintext.
//   - "round-trip integrity incl. empty + large binary"        -> exact bytes
//     survive encrypt(write)->decrypt(read) across the session.
//   - "outer layer stripped / bypassed"                        -> a server whose
//     cert is NOT chained to the client's trusted CA MUST be rejected by the
//     client (the L7 trust anchor cannot be stripped).
//   - "empty/zero-key accepted"                                -> NewMTLSConfig
//     MUST reject an empty cert/key (no zero-key config).
//   - "nonce reuse across layers / concurrent use"             -> many concurrent
//     independent sessions under -race; each AEAD nonce sequence is per-conn.
//
// Every assertion below is SINK-SIDE: the receiving party's decrypt (TLS Read)
// must REJECT tampered/truncated ciphertext, or the exact plaintext must arrive.
// None of them assert merely "no panic".

import (
	"bytes"
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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/doublecrypt"
)

// ---------------------------------------------------------------------------
// Local cert helpers (independent of the other _test.go files' helpers so this
// file stands alone; duplicated minimal minting on purpose).
// ---------------------------------------------------------------------------

type advBundle struct {
	CertPEM []byte
	KeyPEM  []byte
	Cert    *x509.Certificate
	Key     *ecdsa.PrivateKey
}

func advCA(t *testing.T, cn string) *advBundle {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:              time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CA cert: %v", err)
	}
	parsed, _ := x509.ParseCertificate(der)
	keyDER, _ := x509.MarshalECPrivateKey(key)
	return &advBundle{
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		Cert:    parsed,
		Key:     key,
	}
}

func advLeaf(t *testing.T, cn string, isServer bool, ca *advBundle, serial int64) *advBundle {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("leaf key: %v", err)
	}
	eku := x509.ExtKeyUsageClientAuth
	if isServer {
		eku = x509.ExtKeyUsageServerAuth
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:     time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{eku},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		t.Fatalf("leaf cert: %v", err)
	}
	parsed, _ := x509.ParseCertificate(der)
	keyDER, _ := x509.MarshalECPrivateKey(key)
	return &advBundle{
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		Cert:    parsed,
		Key:     key,
	}
}

// advConfigs mints CA+server+client and returns wired server/client *tls.Config.
func advConfigs(t *testing.T) (srv, cli *tls.Config) {
	t.Helper()
	ca := advCA(t, "adv-CA")
	srvB := advLeaf(t, "adv-server", true, ca, 2)
	cliB := advLeaf(t, "adv-client", false, ca, 3)
	var err error
	srv, err = doublecrypt.NewMTLSConfig(srvB.CertPEM, srvB.KeyPEM, ca.CertPEM, true)
	if err != nil {
		t.Fatalf("server cfg: %v", err)
	}
	cli, err = doublecrypt.NewMTLSConfig(cliB.CertPEM, cliB.KeyPEM, ca.CertPEM, false)
	if err != nil {
		t.Fatalf("client cfg: %v", err)
	}
	cli.ServerName = "localhost"
	return srv, cli
}

// ---------------------------------------------------------------------------
// tamperConn: a net.Conn middlebox that mutates the FIRST application-data byte
// stream chunk written through it after the handshake completes, so we can flip
// a byte inside a live TLS 1.3 record and prove the AEAD rejects it sink-side.
//
// TLS 1.3 record layout on the wire: ContentType(1)=0x17 for app data ||
// LegacyVersion(2) || Length(2) || Encrypted{ ... }. The encrypted body
// (anything past the 5-byte header) is AEAD-protected. We flip a byte deep in
// the body of the first post-handshake record we see, leaving the 5-byte header
// intact so the receiver still frames the record and then fails the AEAD tag.
// ---------------------------------------------------------------------------

type tamperConn struct {
	net.Conn
	armed   atomic.Bool // set true once we want to start tampering writes
	done    atomic.Bool // tampered exactly one record already
	mode    string      // "flip" or "truncate"
	dropLen int
}

func (c *tamperConn) Write(b []byte) (int, error) {
	if !c.armed.Load() || c.done.Load() {
		return c.Conn.Write(b)
	}
	// Only tamper a TLS application-data record (0x17). Handshake (0x16) and
	// alert (0x15) records pass untouched so the handshake still completes.
	if len(b) >= 6 && b[0] == 0x17 {
		c.done.Store(true)
		switch c.mode {
		case "flip":
			cp := make([]byte, len(b))
			copy(cp, b)
			// Flip a byte well inside the AEAD-protected body (index 5 is the
			// first ciphertext byte; pick the middle to avoid header).
			idx := 5 + (len(cp)-5)/2
			cp[idx] ^= 0xFF
			return c.Conn.Write(cp)
		case "truncate":
			// Forward header + a partial body, dropping dropLen trailing bytes.
			// The peer must NOT surface forged authenticated plaintext from a
			// short/again-framed record.
			cut := len(b) - c.dropLen
			if cut < 6 {
				cut = 6
			}
			n, err := c.Conn.Write(b[:cut])
			if err != nil {
				return n, err
			}
			// Pretend the whole thing was written so the writer proceeds.
			return len(b), nil
		}
	}
	return c.Conn.Write(b)
}

// ---------------------------------------------------------------------------
// PROBE 1: TAMPER REJECTION — flip a byte in a live TLS 1.3 app-data record.
// SINK-SIDE: the server's Read MUST return an error (AEAD auth failure) and MUST
// NOT return the plaintext payload.
//
// MUTATION PROOF (positive): with the real crypto/tls AEAD, a flipped record is
// rejected -> readErr != nil and payload never equals want. If the AEAD were
// bypassed (e.g. a fake transport that ignored the MAC), the server would read
// `want` -> the bytes.Equal(got,want) guard fires t.Fatal. We additionally
// assert below that the UNTAMPERED control path DOES deliver want, so the probe
// cannot pass vacuously.
// ---------------------------------------------------------------------------

func TestAdversarial_TamperFlip_AEADRejects(t *testing.T) {
	srvCfg, cliCfg := advConfigs(t)

	cRaw, sRaw := net.Pipe()
	tc := &tamperConn{Conn: cRaw, mode: "flip"}
	defer cRaw.Close()
	defer sRaw.Close()

	// Bound EVERY pipe operation. net.Pipe is fully synchronous: when the server
	// detects the bad AEAD tag it tries to write a bad_record_mac alert, which
	// blocks until the client side reads it. Deadlines guarantee neither side can
	// hang the test even if the other never drains the alert.
	deadline := time.Now().Add(8 * time.Second)
	_ = cRaw.SetDeadline(deadline)
	_ = sRaw.SetDeadline(deadline)

	want := bytes.Repeat([]byte("HELIX-TAMPER-PROBE-payload-0123456789"), 8) // > one block

	readErrCh := make(chan error, 1)
	gotCh := make(chan []byte, 1)

	go func() {
		srv := doublecrypt.ServerConn(sRaw, srvCfg)
		if err := srv.Handshake(); err != nil {
			readErrCh <- err
			gotCh <- nil
			return
		}
		buf := make([]byte, len(want))
		n, err := io.ReadFull(srv, buf)
		readErrCh <- err
		gotCh <- buf[:n]
	}()

	cli := doublecrypt.ClientConn(tc, cliCfg)
	if err := cli.Handshake(); err != nil {
		t.Fatalf("client handshake (should succeed before tamper): %v", err)
	}
	// Arm tampering ONLY now that the handshake is done, then write app data.
	tc.armed.Store(true)
	// Write may surface the peer's alert as an error; that's fine.
	_, _ = cli.Write(want)
	// Drain the server's alert/close concurrently so its alert write cannot block
	// forever on the synchronous pipe. We discard whatever comes back — the
	// load-bearing assertion is purely on the SERVER's read result below.
	go func() { _, _ = io.Copy(io.Discard, cli) }()

	readErr := <-readErrCh
	got := <-gotCh

	// SINK-SIDE: the tampered record must NOT decrypt to the plaintext.
	if readErr == nil && bytes.Equal(got, want) {
		t.Fatalf("SECURITY: server accepted tampered TLS record and returned plaintext %q — AEAD not enforced", got)
	}
	if bytes.Equal(got, want) {
		t.Fatalf("SECURITY: tampered record yielded exact plaintext despite err=%v", readErr)
	}
	if readErr == nil {
		t.Fatalf("expected a decrypt error on a flipped record, got nil (got=%q)", got)
	}
	t.Logf("flipped-record rejected sink-side as expected: %v", readErr)
}

// ---------------------------------------------------------------------------
// PROBE 2: TRUNCATION — drop trailing ciphertext bytes of a record in flight.
// SINK-SIDE: the server's Read MUST NOT return the forged/partial plaintext as
// authenticated data. A truncated AEAD record either errors or blocks for more
// bytes (never authenticates short data); we bound the read with a deadline and
// assert the plaintext `want` was NOT delivered intact.
// ---------------------------------------------------------------------------

func TestAdversarial_Truncate_NoForgedPlaintext(t *testing.T) {
	srvCfg, cliCfg := advConfigs(t)

	cRaw, sRaw := net.Pipe()
	tc := &tamperConn{Conn: cRaw, mode: "truncate", dropLen: 16}
	defer cRaw.Close()
	defer sRaw.Close()

	// Bound every pipe op so a short/again-framed record cannot hang the test.
	deadline := time.Now().Add(8 * time.Second)
	_ = cRaw.SetDeadline(deadline)
	_ = sRaw.SetDeadline(deadline)

	want := bytes.Repeat([]byte("HELIX-TRUNCATE-PROBE-"), 16)

	gotCh := make(chan []byte, 1)
	errCh := make(chan error, 1)

	go func() {
		srv := doublecrypt.ServerConn(sRaw, srvCfg)
		if err := srv.Handshake(); err != nil {
			errCh <- err
			gotCh <- nil
			return
		}
		_ = srv.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, len(want))
		n, err := io.ReadFull(srv, buf)
		errCh <- err
		gotCh <- buf[:n]
	}()

	cli := doublecrypt.ClientConn(tc, cliCfg)
	if err := cli.Handshake(); err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	tc.armed.Store(true)
	_, _ = cli.Write(want)
	go func() { _, _ = io.Copy(io.Discard, cli) }()

	err := <-errCh
	got := <-gotCh

	// SINK-SIDE: the full plaintext must NOT be reconstructed from a truncated
	// AEAD record. (A timeout/short-read error is the correct, safe outcome.)
	if err == nil && bytes.Equal(got, want) {
		t.Fatalf("SECURITY: truncated record yielded full forged plaintext %q", got)
	}
	if bytes.Equal(got, want) {
		t.Fatalf("SECURITY: full plaintext recovered from truncated record despite err=%v", err)
	}
	t.Logf("truncated record did not authenticate full plaintext (err=%v, gotLen=%d)", err, len(got))
}

// ---------------------------------------------------------------------------
// PROBE 3: ROUND-TRIP INTEGRITY (untampered control) — empty + large binary.
// SINK-SIDE: exact bytes survive encrypt(write)->decrypt(read). This also makes
// PROBE 1/2 non-vacuous: the same transport WITHOUT tampering delivers `want`.
// ---------------------------------------------------------------------------

func TestAdversarial_RoundTrip_ExactBytes(t *testing.T) {
	cases := map[string][]byte{
		"empty":        {},
		"one-byte":     {0x00},
		"high-entropy": mustRand(t, 4096),
		"all-0xFF":     bytes.Repeat([]byte{0xFF}, 9000),
		"text":         []byte("helix exact round-trip payload \x00\x01\x02\xff"),
	}
	for name, want := range cases {
		want := want
		t.Run(name, func(t *testing.T) {
			srvCfg, cliCfg := advConfigs(t)
			cRaw, sRaw := net.Pipe()
			defer cRaw.Close()
			defer sRaw.Close()

			errCh := make(chan error, 1)
			gotCh := make(chan []byte, 1)
			go func() {
				srv := doublecrypt.ServerConn(sRaw, srvCfg)
				if err := srv.Handshake(); err != nil {
					errCh <- err
					gotCh <- nil
					return
				}
				buf := make([]byte, len(want))
				if len(want) > 0 {
					if _, err := io.ReadFull(srv, buf); err != nil {
						errCh <- err
						gotCh <- nil
						return
					}
				}
				errCh <- nil
				gotCh <- buf
			}()

			cli := doublecrypt.ClientConn(cRaw, cliCfg)
			if err := cli.Handshake(); err != nil {
				t.Fatalf("client handshake: %v", err)
			}
			if len(want) > 0 {
				if _, err := io.WriteString(cli, string(want)); err != nil {
					t.Fatalf("write: %v", err)
				}
			}
			if err := <-errCh; err != nil {
				t.Fatalf("server read: %v", err)
			}
			got := <-gotCh
			if !bytes.Equal(got, want) {
				t.Fatalf("round-trip mismatch: got %d bytes, want %d", len(got), len(want))
			}
		})
	}
}

func mustRand(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return b
}

// ---------------------------------------------------------------------------
// PROBE 4a: LAYER BYPASS — untrusted SERVER cert must be REJECTED by the client.
// The L7 outer trust anchor (client RootCAs) cannot be stripped: a server whose
// cert is signed by a DIFFERENT CA must fail the client handshake.
//
// SINK-SIDE: client handshake returns a non-nil error AND no app data crosses.
// MUTATION PROOF: if NewMTLSConfig(client) set InsecureSkipVerify or used an
// empty/permissive RootCAs, the client would accept the rogue server -> cliErr
// == nil -> t.Fatal fires.
// ---------------------------------------------------------------------------

func TestAdversarial_RogueServer_ClientRejects(t *testing.T) {
	trustedCA := advCA(t, "trusted-CA")
	rogueCA := advCA(t, "rogue-CA")

	// Server presents a cert from the ROGUE CA.
	rogueSrv := advLeaf(t, "rogue-server", true, rogueCA, 7)
	cliB := advLeaf(t, "good-client", false, trustedCA, 8)

	// Server config: server requires client certs from trustedCA, but ITS OWN
	// identity is rogue. (Server CA pool is irrelevant to the client's check.)
	srvCfg, err := doublecrypt.NewMTLSConfig(rogueSrv.CertPEM, rogueSrv.KeyPEM, trustedCA.CertPEM, true)
	if err != nil {
		t.Fatalf("server cfg: %v", err)
	}
	// Client trusts ONLY trustedCA -> must reject the rogue server cert.
	cliCfg, err := doublecrypt.NewMTLSConfig(cliB.CertPEM, cliB.KeyPEM, trustedCA.CertPEM, false)
	if err != nil {
		t.Fatalf("client cfg: %v", err)
	}
	cliCfg.ServerName = "localhost"

	cPipe, sPipe := net.Pipe()
	// Deadlines so a rejected handshake cannot hang forever flushing its flight
	// into a synchronous pipe whose far side has already errored out.
	deadline := time.Now().Add(8 * time.Second)
	_ = cPipe.SetDeadline(deadline)
	_ = sPipe.SetDeadline(deadline)
	srvErrCh := make(chan error, 1)
	cliErrCh := make(chan error, 1)

	go func() {
		srv := doublecrypt.ServerConn(sPipe, srvCfg)
		e := srv.Handshake()
		srv.Close()
		srvErrCh <- e
	}()
	go func() {
		cli := doublecrypt.ClientConn(cPipe, cliCfg)
		e := cli.Handshake()
		cli.Close()
		cliErrCh <- e
	}()

	cliErr := <-cliErrCh
	srvErr := <-srvErrCh

	// SINK-SIDE: the CLIENT is the enforcing party for server identity; it MUST
	// reject the rogue server.
	if cliErr == nil {
		t.Fatal("SECURITY: client accepted a server cert signed by an untrusted CA — outer L7 trust anchor can be stripped")
	}
	// The rejection must be a real cert-verification failure, NOT the pipe
	// deadline firing (which would prove nothing). crypto/tls surfaces this as a
	// *tls.CertificateVerificationError / "unknown authority"; a timeout must not
	// be what satisfies the assertion.
	if ne, ok := cliErr.(net.Error); ok && ne.Timeout() {
		t.Fatalf("client handshake hit a deadline instead of rejecting the rogue cert: %v", cliErr)
	}
	var certErr *tls.CertificateVerificationError
	if !errors.As(cliErr, &certErr) {
		t.Logf("note: client rejection was not a *tls.CertificateVerificationError (still non-timeout): %v", cliErr)
	}
	t.Logf("client rejected rogue server as expected: %v (srvErr=%v)", cliErr, srvErr)
}

// ---------------------------------------------------------------------------
// PROBE 4b: LAYER BYPASS — client presenting NO cert must be rejected by the
// RequireAndVerifyClientCert server (mTLS inner-auth cannot be bypassed).
//
// SINK-SIDE: server handshake returns non-nil error.
// MUTATION PROOF: if the server cfg used tls.NoClientCert/RequestClientCert,
// the certless client would be accepted -> srvErr == nil -> t.Fatal fires.
// ---------------------------------------------------------------------------

func TestAdversarial_NoClientCert_ServerRejects(t *testing.T) {
	// Build a server (RequireAndVerifyClientCert) and a client that trusts the
	// server CA but presents NO certificate, so we exercise the inner mTLS auth.
	ca := advCA(t, "anon-CA")
	srvB := advLeaf(t, "anon-server", true, ca, 2)
	realSrvCfg, err := doublecrypt.NewMTLSConfig(srvB.CertPEM, srvB.KeyPEM, ca.CertPEM, true)
	if err != nil {
		t.Fatalf("server cfg: %v", err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca.CertPEM) {
		t.Fatalf("append CA")
	}
	certlessClient := &tls.Config{
		MinVersion: tls.VersionTLS13,
		RootCAs:    pool,
		ServerName: "localhost",
		// No Certificates -> client sends an empty certificate list.
	}

	cPipe, sPipe := net.Pipe()
	deadline := time.Now().Add(8 * time.Second)
	_ = cPipe.SetDeadline(deadline)
	_ = sPipe.SetDeadline(deadline)
	srvErrCh := make(chan error, 1)
	cliErrCh := make(chan error, 1)

	go func() {
		srv := doublecrypt.ServerConn(sPipe, realSrvCfg)
		e := srv.Handshake()
		srv.Close()
		srvErrCh <- e
	}()
	go func() {
		cli := doublecrypt.ClientConn(cPipe, certlessClient)
		e := cli.Handshake()
		cli.Close()
		cliErrCh <- e
	}()

	cliErr := <-cliErrCh
	srvErr := <-srvErrCh

	if srvErr == nil {
		t.Fatal("SECURITY: server accepted a client that presented NO certificate — RequireAndVerifyClientCert not enforced")
	}
	// Must be a real auth rejection ("client didn't provide a certificate"),
	// not the deadline firing.
	if ne, ok := srvErr.(net.Error); ok && ne.Timeout() {
		t.Fatalf("server handshake hit a deadline instead of rejecting the certless client: %v", srvErr)
	}
	t.Logf("server rejected certless client as expected: %v (cliErr=%v)", srvErr, cliErr)
}

// ---------------------------------------------------------------------------
// PROBE 5: EMPTY / ZERO-KEY ACCEPTANCE — NewMTLSConfig must reject empty cert or
// key material. A zero-key config would be a silent crypto-disable.
//
// SINK-SIDE: cfg == nil AND err != nil for any empty cert/key input.
// ---------------------------------------------------------------------------

func TestAdversarial_EmptyKeyMaterial_Rejected(t *testing.T) {
	ca := advCA(t, "zk-CA")
	leaf := advLeaf(t, "zk-leaf", true, ca, 5)

	cases := []struct {
		name             string
		certPEM, keyPEM  []byte
	}{
		{"empty cert", nil, leaf.KeyPEM},
		{"empty key", leaf.CertPEM, nil},
		{"both empty", nil, nil},
		{"cert is key", leaf.KeyPEM, leaf.KeyPEM},
		{"key is cert", leaf.CertPEM, leaf.CertPEM},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := doublecrypt.NewMTLSConfig(tc.certPEM, tc.keyPEM, ca.CertPEM, true)
			if err == nil {
				t.Fatalf("SECURITY: NewMTLSConfig accepted empty/mismatched key material (cfg=%v)", cfg)
			}
			if cfg != nil {
				t.Fatalf("expected nil cfg on error, got %+v", cfg)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// PROBE 6: NONCE/KEY ISOLATION + CONCURRENCY — many independent mTLS sessions in
// parallel, each transferring a unique payload. Run with -race. Per-conn AEAD
// nonce sequences must keep payloads isolated; no data race in the package's
// config/conn construction.
//
// SINK-SIDE: every session's server receives EXACTLY its own unique payload.
// ---------------------------------------------------------------------------

func TestAdversarial_ConcurrentSessions_Isolated(t *testing.T) {
	srvCfg, cliCfg := advConfigs(t) // shared *tls.Config across goroutines (read-only use)

	const n = 24
	var wg sync.WaitGroup
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			want := bytes.Repeat([]byte{byte(i), byte(i ^ 0x5A)}, 64+i)
			cRaw, sRaw := net.Pipe()
			defer cRaw.Close()
			defer sRaw.Close()

			innerErr := make(chan error, 1)
			gotCh := make(chan []byte, 1)
			go func() {
				srv := doublecrypt.ServerConn(sRaw, srvCfg)
				if err := srv.Handshake(); err != nil {
					innerErr <- err
					gotCh <- nil
					return
				}
				buf := make([]byte, len(want))
				if _, err := io.ReadFull(srv, buf); err != nil {
					innerErr <- err
					gotCh <- nil
					return
				}
				innerErr <- nil
				gotCh <- buf
			}()

			cli := doublecrypt.ClientConn(cRaw, cliCfg)
			if err := cli.Handshake(); err != nil {
				errs <- err
				return
			}
			if _, err := cli.Write(want); err != nil {
				errs <- err
				return
			}
			if err := <-innerErr; err != nil {
				errs <- err
				return
			}
			got := <-gotCh
			if !bytes.Equal(got, want) {
				errs <- &mismatchError{i: i, got: len(got), want: len(want)}
				return
			}
			errs <- nil
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatalf("concurrent session failure: %v", e)
		}
	}
}

type mismatchError struct {
	i         int
	got, want int
}

func (m *mismatchError) Error() string {
	return "session payload mismatch/cross-talk"
}
