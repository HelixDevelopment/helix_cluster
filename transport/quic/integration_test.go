package quic

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	quicgo "github.com/quic-go/quic-go"
)

// runID is emitted once per test run for evidence correlation.
var runID = uuid.NewString()

// newTestServerConfig builds a server Config with a real self-signed cert and
// the Helix defaults (0-RTT + datagrams on).
func newTestServerConfig(t *testing.T) (Config, *tls.Config) {
	t.Helper()
	cert, pool, err := GenerateSelfSignedCert()
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	serverTLS := &tls.Config{Certificates: []tls.Certificate{cert}}
	clientTLS := &tls.Config{RootCAs: pool, ServerName: "localhost"}
	srvCfg := Config{
		Allow0RTT:       true,
		EnableDatagrams: true,
		KeepAlivePeriod: 15 * time.Second,
		MaxIdleTimeout:  30 * time.Second,
		TLSConfig:       serverTLS,
	}
	return srvCfg, clientTLS
}

// echoServe accepts one connection and echoes every stream's bytes back. It
// also echoes one datagram if datagrams are enabled. Returns when ctx is done.
func echoServe(ctx context.Context, t *testing.T, srv *QUICServer, wantDatagram bool) {
	t.Helper()
	conn, err := srv.Accept(ctx)
	if err != nil {
		if ctx.Err() == nil {
			t.Errorf("server accept: %v", err)
		}
		return
	}

	if wantDatagram {
		go func() {
			dg, derr := conn.ReceiveDatagram(ctx)
			if derr != nil {
				return
			}
			_ = conn.SendDatagram(append([]byte("echo:"), dg...))
		}()
	}

	for {
		stream, serr := conn.AcceptStream(ctx)
		if serr != nil {
			return
		}
		go func(s *quicgo.Stream) {
			buf, _ := io.ReadAll(s)
			_, _ = s.Write(buf)
			_ = s.Close()
		}(stream)
	}
}

// TestIntegration_RoundTrip establishes a REAL QUIC connection over loopback
// UDP, opens a stream, sends a payload and asserts a byte-exact round-trip
// through the real TLS 1.3 handshake. It also exercises an unreliable DATAGRAM.
func TestIntegration_RoundTrip(t *testing.T) {
	t.Logf("run-id=%s test=RoundTrip", runID)
	srvCfg, clientTLS := newTestServerConfig(t)

	srv, err := NewQUICServer("127.0.0.1:0", srvCfg)
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); echoServe(ctx, t, srv, true) }()

	cli, err := NewQUICClient(Config{
		Allow0RTT:       true,
		EnableDatagrams: true,
		KeepAlivePeriod: 15 * time.Second,
		TLSConfig:       clientTLS,
	})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer cli.Close()

	conn, used0RTT, err := cli.Dial(ctx, srv.Addr())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if used0RTT {
		t.Fatal("first dial must NOT be 0-RTT (no prior session)")
	}

	// Assert the negotiated ALPN is the Helix protocol — proves the handshake
	// actually completed over the intended protocol.
	if got := conn.ConnectionState().TLS.NegotiatedProtocol; got != ALPNHelixFederation {
		t.Fatalf("negotiated ALPN = %q, want %q", got, ALPNHelixFederation)
	}

	// Stream round-trip.
	payload := []byte("HXC-1351 quic reliable fallback payload \x00\x01\x02 byte-exact")
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	if _, err := stream.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = stream.Close() // signal EOF so the server's io.ReadAll returns.

	got := make([]byte, len(payload))
	if _, err := io.ReadFull(stream, got); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("round-trip mismatch:\n got=%q\nwant=%q", got, payload)
	}
	t.Logf("stream round-trip OK (%d bytes byte-exact)", len(got))

	// Unreliable DATAGRAM round-trip.
	if conn.ConnectionState().SupportsDatagrams.Local && conn.ConnectionState().SupportsDatagrams.Remote {
		if err := conn.SendDatagram([]byte("ping")); err != nil {
			t.Fatalf("send datagram: %v", err)
		}
		dgCtx, dgCancel := context.WithTimeout(ctx, 5*time.Second)
		defer dgCancel()
		dg, derr := conn.ReceiveDatagram(dgCtx)
		if derr != nil {
			t.Fatalf("receive datagram: %v", derr)
		}
		if !bytes.Equal(dg, []byte("echo:ping")) {
			t.Fatalf("datagram echo = %q, want %q", dg, "echo:ping")
		}
		t.Logf("datagram round-trip OK: %q", dg)
	} else {
		t.Log("DATAGRAM not mutually supported; skipped datagram assertion")
	}

	_ = conn.CloseWithError(0, "done")
	cancel()
	wg.Wait()
}

// TestIntegration_WrongALPN is the ANTI-BLUFF mutation: a client offering a
// different ALPN must FAIL the handshake. If this passed, the round-trip test
// would be proving nothing.
func TestIntegration_WrongALPN(t *testing.T) {
	t.Logf("run-id=%s test=WrongALPN(anti-bluff)", runID)
	srvCfg, clientTLS := newTestServerConfig(t)

	srv, err := NewQUICServer("127.0.0.1:0", srvCfg)
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go func() { _, _ = srv.Accept(ctx) }()

	// Client deliberately negotiates a non-Helix ALPN.
	cli, err := NewQUICClient(Config{
		NextProtos: []string{"wrong-proto/9.9"},
		TLSConfig:  clientTLS,
	})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer cli.Close()

	_, _, err = cli.Dial(ctx, srv.Addr())
	if err == nil {
		t.Fatal("ANTI-BLUFF FAILURE: handshake succeeded with mismatched ALPN")
	}
	var alpnErr *quicgo.TransportError
	if errors.As(err, &alpnErr) {
		t.Logf("anti-bluff OK: handshake rejected (transport error: %v)", alpnErr)
	} else {
		t.Logf("anti-bluff OK: handshake rejected: %v", err)
	}
}
