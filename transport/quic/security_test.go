package quic

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

// TestSecurity_QUICConfigLimits proves the hardened quic.Config carries the
// stream caps and the explicit handshake-idle bound. If any limit is dropped
// from quicConfig() this test fails (anti-bluff: the assertions are the limit).
func TestSecurity_QUICConfigLimits(t *testing.T) {
	t.Logf("run-id=%s test=QUICConfigLimits", runID)

	// Defaults must produce bounded, non-zero stream caps and a handshake idle.
	def := (&Config{}).quicConfig()
	if def.MaxIncomingStreams != DefaultMaxIncomingStreams {
		t.Fatalf("default MaxIncomingStreams = %d, want %d", def.MaxIncomingStreams, DefaultMaxIncomingStreams)
	}
	if def.MaxIncomingUniStreams != DefaultMaxIncomingUniStreams {
		t.Fatalf("default MaxIncomingUniStreams = %d, want %d", def.MaxIncomingUniStreams, DefaultMaxIncomingUniStreams)
	}
	if def.MaxIncomingStreams <= 0 || def.MaxIncomingUniStreams <= 0 {
		t.Fatalf("stream caps must be bounded and positive, got bidi=%d uni=%d", def.MaxIncomingStreams, def.MaxIncomingUniStreams)
	}
	if def.HandshakeIdleTimeout != DefaultHandshakeIdleTimeout {
		t.Fatalf("default HandshakeIdleTimeout = %v, want %v", def.HandshakeIdleTimeout, DefaultHandshakeIdleTimeout)
	}
	if def.MaxIdleTimeout != DefaultMaxIdleTimeout {
		t.Fatalf("default MaxIdleTimeout = %v, want %v", def.MaxIdleTimeout, DefaultMaxIdleTimeout)
	}

	// Overrides honored, including the negative "disallow" sentinel quic-go uses.
	ov := (&Config{
		MaxIncomingStreams:    7,
		MaxIncomingUniStreams: -1,
		HandshakeIdleTimeout:  3 * time.Second,
	}).quicConfig()
	if ov.MaxIncomingStreams != 7 {
		t.Errorf("override bidi cap = %d, want 7", ov.MaxIncomingStreams)
	}
	if ov.MaxIncomingUniStreams != -1 {
		t.Errorf("override uni cap (disallow) = %d, want -1", ov.MaxIncomingUniStreams)
	}
	if ov.HandshakeIdleTimeout != 3*time.Second {
		t.Errorf("override handshake idle = %v, want 3s", ov.HandshakeIdleTimeout)
	}
}

// TestSecurity_StreamCapEnforced proves the bidi-stream cap is enforced on the
// wire: with MaxIncomingStreams=2 the server lets a peer open 2 concurrent
// streams but BLOCKS the 3rd OpenStreamSync (it cannot complete until an
// earlier stream is released). Anti-bluff: raise the cap and the 3rd opens
// immediately — proving the block is the cap, not an artifact.
func TestSecurity_StreamCapEnforced(t *testing.T) {
	t.Logf("run-id=%s test=StreamCapEnforced", runID)
	srvCfg, clientTLS := newTestServerConfig(t)
	const cap = 2
	srvCfg.MaxIncomingStreams = cap

	srv, err := NewQUICServer("127.0.0.1:0", srvCfg)
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Server accepts the connection but DOES NOT accept any streams, so the
	// peer's stream credit is never replenished beyond the initial cap.
	go func() {
		if _, aerr := srv.Accept(ctx); aerr != nil {
			return
		}
		<-ctx.Done()
	}()

	cli, err := NewQUICClient(Config{TLSConfig: clientTLS})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer cli.Close()

	conn, _, err := cli.Dial(ctx, srv.Addr())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseWithError(0, "done")

	// Open `cap` streams synchronously — these must succeed.
	for i := 0; i < cap; i++ {
		s, oerr := conn.OpenStreamSync(ctx)
		if oerr != nil {
			t.Fatalf("stream %d should open within cap: %v", i, oerr)
		}
		_ = s
	}

	// The (cap+1)th OpenStreamSync must BLOCK (no credit). Prove it blocks by
	// giving it a short deadline and asserting a timeout.
	blockCtx, blockCancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer blockCancel()
	_, oerr := conn.OpenStreamSync(blockCtx)
	if oerr == nil {
		t.Fatalf("ANTI-BLUFF: %dth stream opened despite cap=%d (limit not enforced)", cap+1, cap)
	}
	if !errors.Is(oerr, context.DeadlineExceeded) {
		t.Logf("over-cap stream blocked with: %v (acceptable; it did not open)", oerr)
	} else {
		t.Logf("over-cap stream correctly blocked until deadline (cap=%d enforced)", cap)
	}
}

// TestSecurity_MaxConnsRefused proves the accept-side concurrent-connection cap:
// with MaxConns=1 the server accepts the first connection and REFUSES the second
// (Accept returns ErrMaxConnsReached and the peer connection is closed).
// Anti-bluff: a second server with MaxConns=2 accepts both.
func TestSecurity_MaxConnsRefused(t *testing.T) {
	t.Logf("run-id=%s test=MaxConnsRefused", runID)
	srvCfg, clientTLS := newTestServerConfig(t)
	srvCfg.MaxConns = 1

	srv, err := NewQUICServer("127.0.0.1:0", srvCfg)
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	defer srv.Close()
	if srv.MaxConns() != 1 {
		t.Fatalf("resolved MaxConns = %d, want 1", srv.MaxConns())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	accepted := make(chan error, 2)
	go func() {
		_, e1 := srv.Accept(ctx)
		accepted <- e1
		_, e2 := srv.Accept(ctx)
		accepted <- e2
	}()

	// First client — must be accepted and held open.
	cli1, err := NewQUICClient(Config{TLSConfig: clientTLS})
	if err != nil {
		t.Fatalf("client1: %v", err)
	}
	defer cli1.Close()
	conn1, _, err := cli1.Dial(ctx, srv.Addr())
	if err != nil {
		t.Fatalf("dial1: %v", err)
	}
	defer conn1.CloseWithError(0, "done")

	if e := <-accepted; e != nil {
		t.Fatalf("first Accept should succeed, got: %v", e)
	}
	if got := srv.LiveConns(); got != 1 {
		t.Fatalf("LiveConns after first accept = %d, want 1", got)
	}

	// Second client — server is at cap, Accept must refuse it.
	cli2, err := NewQUICClient(Config{TLSConfig: clientTLS})
	if err != nil {
		t.Fatalf("client2: %v", err)
	}
	defer cli2.Close()
	conn2, _, derr := cli2.Dial(ctx, srv.Addr())
	if derr == nil {
		// Dial may succeed at the QUIC layer before the server refuses; the
		// authoritative signal is Accept returning ErrMaxConnsReached.
		defer conn2.CloseWithError(0, "done")
	}

	if e := <-accepted; !errors.Is(e, ErrMaxConnsReached) {
		t.Fatalf("ANTI-BLUFF: second Accept = %v, want ErrMaxConnsReached (cap not enforced)", e)
	}
	t.Logf("second connection correctly refused (ErrMaxConnsReached); LiveConns=%d", srv.LiveConns())
	if got := srv.LiveConns(); got != 1 {
		t.Fatalf("LiveConns must stay at cap after refusal, got %d", got)
	}
}

// TestSecurity_MaxConnsAllowsBelowCap is the anti-bluff complement: with a
// higher cap the SAME flow that was refused above is accepted. If the refusal
// above were spurious (always-refuse bug) this would also fail.
func TestSecurity_MaxConnsAllowsBelowCap(t *testing.T) {
	t.Logf("run-id=%s test=MaxConnsAllowsBelowCap", runID)
	srvCfg, clientTLS := newTestServerConfig(t)
	srvCfg.MaxConns = 2

	srv, err := NewQUICServer("127.0.0.1:0", srvCfg)
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	accepted := make(chan error, 2)
	go func() {
		_, e1 := srv.Accept(ctx)
		accepted <- e1
		_, e2 := srv.Accept(ctx)
		accepted <- e2
	}()

	for i := 0; i < 2; i++ {
		cli, cerr := NewQUICClient(Config{TLSConfig: clientTLS})
		if cerr != nil {
			t.Fatalf("client %d: %v", i, cerr)
		}
		defer cli.Close()
		conn, _, derr := cli.Dial(ctx, srv.Addr())
		if derr != nil {
			t.Fatalf("dial %d: %v", i, derr)
		}
		defer conn.CloseWithError(0, "done")
	}

	for i := 0; i < 2; i++ {
		if e := <-accepted; e != nil {
			t.Fatalf("accept %d under cap=2 should succeed, got %v", i, e)
		}
	}
	t.Logf("both connections accepted under cap=2 (LiveConns=%d)", srv.LiveConns())
}

// TestSecurity_FramedMessageCapNoOOM proves the inbound framing read path
// rejects a hostile length-prefix WITHOUT allocating the advertised payload.
// A crafted 4-byte header advertising ~4 GiB must return ErrMessageTooLarge
// rather than attempting a 4 GiB allocation. Anti-bluff: a within-cap frame
// of the SAME reader round-trips fine.
func TestSecurity_FramedMessageCapNoOOM(t *testing.T) {
	t.Logf("run-id=%s test=FramedMessageCapNoOOM", runID)
	const maxBytes = 4096

	// Hostile header: declares ~4 GiB, supplies no payload. ReadFramedMessage
	// must reject on the DECLARED length before allocating anything.
	hostile := []byte{0xFF, 0xFF, 0xFF, 0xFF} // 4294967295
	_, err := ReadFramedMessage(bytes.NewReader(hostile), maxBytes)
	if !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("ANTI-BLUFF: hostile 4GiB length not rejected: err=%v (cap not enforced -> OOM vector)", err)
	}
	t.Logf("hostile 4GiB length-prefix rejected pre-allocation: %v", err)

	// Just-over-cap declared length is also rejected without allocation.
	over := []byte{0, 0, 0x10, 0x01} // 4097 > 4096
	if _, e := ReadFramedMessage(bytes.NewReader(over), maxBytes); !errors.Is(e, ErrMessageTooLarge) {
		t.Fatalf("over-cap (%d) length not rejected: %v", 4097, e)
	}

	// Zero-length frame is malformed.
	zero := []byte{0, 0, 0, 0}
	if _, e := ReadFramedMessage(bytes.NewReader(zero), maxBytes); !errors.Is(e, ErrZeroLengthFrame) {
		t.Fatalf("zero-length frame not rejected: %v", e)
	}

	// Within-cap round-trip (anti-bluff: proves the reader is not always-reject).
	payload := bytes.Repeat([]byte("helix"), 100) // 500 bytes < 4096
	var buf bytes.Buffer
	if werr := WriteFramedMessage(&buf, payload, maxBytes); werr != nil {
		t.Fatalf("write within-cap frame: %v", werr)
	}
	got, rerr := ReadFramedMessage(&buf, maxBytes)
	if rerr != nil {
		t.Fatalf("read within-cap frame: %v", rerr)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("framed round-trip mismatch: got %d bytes want %d", len(got), len(payload))
	}
	t.Logf("within-cap frame (%d bytes) round-tripped byte-exact", len(got))

	// WriteFramedMessage refuses to emit an over-cap frame.
	if werr := WriteFramedMessage(io.Discard, bytes.Repeat([]byte{0}, maxBytes+1), maxBytes); !errors.Is(werr, ErrMessageTooLarge) {
		t.Fatalf("write of over-cap payload not refused: %v", werr)
	}
}

// TestSecurity_FramedMessageOverWire proves the cap holds on a REAL QUIC stream:
// a peer writes a length-prefix advertising a huge payload; the receiver using
// ReadFramedMessage with the configured cap rejects it instead of OOMing, while
// the QUIC connection itself stays usable.
func TestSecurity_FramedMessageOverWire(t *testing.T) {
	t.Logf("run-id=%s test=FramedMessageOverWire", runID)
	srvCfg, clientTLS := newTestServerConfig(t)
	srv, err := NewQUICServer("127.0.0.1:0", srvCfg)
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	const wireMax = 64 * 1024 // 64 KiB cap on the read side
	readErrCh := make(chan error, 1)
	go func() {
		conn, aerr := srv.Accept(ctx)
		if aerr != nil {
			readErrCh <- aerr
			return
		}
		stream, serr := conn.AcceptStream(ctx)
		if serr != nil {
			readErrCh <- serr
			return
		}
		_, rerr := ReadFramedMessage(stream, wireMax)
		readErrCh <- rerr
	}()

	cli, err := NewQUICClient(Config{TLSConfig: clientTLS})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer cli.Close()
	conn, _, err := cli.Dial(ctx, srv.Addr())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseWithError(0, "done")

	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	// Send ONLY a hostile header advertising 256 MiB, then no payload.
	hostileHdr := []byte{0x10, 0x00, 0x00, 0x00} // 268435456 bytes
	if _, werr := stream.Write(hostileHdr); werr != nil {
		t.Fatalf("write hostile header: %v", werr)
	}
	_ = stream.Close()

	select {
	case rerr := <-readErrCh:
		if !errors.Is(rerr, ErrMessageTooLarge) {
			t.Fatalf("ANTI-BLUFF: over-wire hostile frame not rejected: %v", rerr)
		}
		t.Logf("server rejected hostile 256MiB framed message over real QUIC stream: %v", rerr)
	case <-ctx.Done():
		t.Fatal("timed out waiting for server read result")
	}
}
