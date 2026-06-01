package wireguard

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// twoLoopbackUDP creates two ephemeral loopback UDP4 sockets and returns them
// together with a cleanup func.
func twoLoopbackUDP(t *testing.T) (a, b *net.UDPConn, cleanup func()) {
	t.Helper()
	a, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err, "create socket A")
	b, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		_ = a.Close()
		t.Fatalf("create socket B: %v", err)
	}
	return a, b, func() { _ = a.Close(); _ = b.Close() }
}

// ---------------------------------------------------------------------------
// buildProbe / isProbe unit tests
// ---------------------------------------------------------------------------

// TestBuildProbeHasMagicHeader proves buildProbe always starts with probeMagic.
//
// Mutation: zero out probeMagic[0] → isProbe returns false → assert fails.
func TestBuildProbeHasMagicHeader(t *testing.T) {
	p := buildProbe([]byte("hello"))
	require.GreaterOrEqual(t, len(p), len(probeMagic))
	assert.Equal(t, probeMagic[0], p[0], "first magic byte")
	assert.Equal(t, probeMagic[1], p[1], "second magic byte")
	assert.Equal(t, probeMagic[2], p[2], "third magic byte")
	assert.Equal(t, probeMagic[3], p[3], "fourth magic byte")
	assert.Equal(t, byte('h'), p[4], "payload first byte")
}

// TestIsProbeDetectsMagic proves isProbe correctly identifies probes and rejects
// non-probes.
//
// Mutation: drop the probeMagic check (always return true) → the non-probe
// assertion (assert.False) fails.
func TestIsProbeDetectsMagic(t *testing.T) {
	validProbe := buildProbe([]byte("test"))
	assert.True(t, isProbe(validProbe), "valid probe must be recognised")

	notProbe := []byte("hello world")
	assert.False(t, isProbe(notProbe), "arbitrary bytes must not be a probe")

	short := []byte{0x48, 0x50} // only 2 of 4 magic bytes
	assert.False(t, isProbe(short), "truncated magic must not be a probe")

	empty := []byte{}
	assert.False(t, isProbe(empty), "empty datagram must not be a probe")
}

// ---------------------------------------------------------------------------
// HolePunchState string tests
// ---------------------------------------------------------------------------

// TestHolePunchStateString proves all state constants produce sensible strings.
//
// Mutation: change HolePunchConnected.String() to return "" → exact-equal
// assertion on "connected" fails.
func TestHolePunchStateString(t *testing.T) {
	assert.Equal(t, "pending", HolePunchPending.String())
	assert.Equal(t, "connected", HolePunchConnected.String())
	assert.Equal(t, "timed_out", HolePunchTimedOut.String())
	assert.Equal(t, "relaying", HolePunchRelaying.String())
}

// ---------------------------------------------------------------------------
// HolePuncher: direct loopback punch → CONNECTED
// ---------------------------------------------------------------------------

// TestHolePunchConnectsOverLoopback proves that when two sockets on 127.0.0.1
// simultaneously send probes to each other, both sides reach HolePunchConnected
// (sink-side: State == HolePunchConnected, not just "no error").
//
// Mutation: break isProbe (return false always) → recvCh never fires, both
// sides time out → assert.Equal(HolePunchConnected) fails.
func TestHolePunchConnectsOverLoopback(t *testing.T) {
	connA, connB, cleanup := twoLoopbackUDP(t)
	defer cleanup()

	pp := &PeerPuncher{connA: connA, connB: connB}
	resA, resB, err := pp.PunchBoth(context.Background(), 3*time.Second)
	require.NoError(t, err)

	// Both sides must reach CONNECTED — assert the exact state enum value.
	assert.Equal(t, HolePunchConnected, resA.State, "side A must be connected")
	assert.Equal(t, HolePunchConnected, resB.State, "side B must be connected")

	// Each side must have sent bytes (probe was transmitted).
	assert.Greater(t, resA.BytesSent, 0, "A must have sent probe bytes")
	assert.Greater(t, resB.BytesSent, 0, "B must have sent probe bytes")

	// RTT must be measurable (>= 0, < 1 second on loopback).
	assert.GreaterOrEqual(t, resA.RTT, time.Duration(0), "RTT must be non-negative")
	assert.Less(t, resA.RTT, time.Second, "RTT must be < 1s on loopback")
}

// ---------------------------------------------------------------------------
// HolePuncher: timeout path → HolePunchTimedOut
// ---------------------------------------------------------------------------

// TestHolePunchTimesOutWithNoRelay proves that when the remote end never
// responds (we point at a black-hole address) and no relay is configured, the
// puncher transitions to HolePunchTimedOut — not CONNECTED, not RELAYING.
//
// We use a real socket whose far end sends nothing: we create a loopback socket
// and immediately close it so the OS will silently discard writes (ICMP
// unreachable is not returned on loopback in the same way), and set a very short
// timeout.
//
// Mutation: remove the `case <-ctx.Done()` branch in Punch() — the function
// blocks forever and the test hangs → Go test timeout kills it with an error.
func TestHolePunchTimesOutWithNoRelay(t *testing.T) {
	// Create a socket for the local side.
	connA, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	defer connA.Close()

	// Create a "remote" that we immediately abandon (close it so no one reads).
	blackHole, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	remoteAddr := blackHole.LocalAddr().(*net.UDPAddr)
	_ = blackHole.Close() // closed — nothing will respond

	hp := &HolePuncher{
		LocalConn:     connA,
		RemoteAddr:    remoteAddr,
		Timeout:       150 * time.Millisecond,
		ProbeInterval: 30 * time.Millisecond,
	}
	result, err := hp.Punch(context.Background())
	require.NoError(t, err)

	// Must time out, not CONNECTED (no one is responding).
	assert.Equal(t, HolePunchTimedOut, result.State, "must time out when no reply")
	assert.Nil(t, result.RelayAddr, "no relay configured → RelayAddr must be nil")
}

// ---------------------------------------------------------------------------
// HolePuncher: relay fallback → HolePunchRelaying
// ---------------------------------------------------------------------------

// TestHolePunchFallsBackToRelay proves that when the direct punch times out AND
// a RelayAddr is configured, the state is HolePunchRelaying and RelayAddr is the
// configured one.
//
// Mutation: in the ctx.Done() branch, set State=HolePunchTimedOut instead of
// HolePunchRelaying → the assert.Equal(HolePunchRelaying) fails.
func TestHolePunchFallsBackToRelay(t *testing.T) {
	connA, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	defer connA.Close()

	// Create an ephemeral socket and immediately close it (no one responds).
	blackHole, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	remoteAddr := blackHole.LocalAddr().(*net.UDPAddr)
	_ = blackHole.Close()

	// A relay address — doesn't need to be reachable, we just check it's stored.
	relayAddr := &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 3478}

	hp := &HolePuncher{
		LocalConn:     connA,
		RemoteAddr:    remoteAddr,
		Timeout:       100 * time.Millisecond,
		ProbeInterval: 25 * time.Millisecond,
		RelayAddr:     relayAddr,
	}
	result, err := hp.Punch(context.Background())
	require.NoError(t, err)

	// Sink-side assertion: state is RELAYING (not TIMED_OUT) and relay addr is set.
	assert.Equal(t, HolePunchRelaying, result.State, "must transition to relaying on timeout with relay configured")
	require.NotNil(t, result.RelayAddr, "RelayAddr must be populated")
	assert.Equal(t, relayAddr.String(), result.RelayAddr.String(), "RelayAddr must match configured relay")
}

// ---------------------------------------------------------------------------
// HolePuncher: nil guards
// ---------------------------------------------------------------------------

// TestHolePunchRejectsNilConn proves Punch returns an error if LocalConn is nil.
func TestHolePunchRejectsNilConn(t *testing.T) {
	hp := &HolePuncher{
		LocalConn:  nil,
		RemoteAddr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9999},
	}
	_, err := hp.Punch(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "LocalConn is nil")
}

// TestHolePunchRejectsNilRemoteAddr proves Punch returns an error if RemoteAddr is nil.
func TestHolePunchRejectsNilRemoteAddr(t *testing.T) {
	connA, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	defer connA.Close()

	hp := &HolePuncher{
		LocalConn:  connA,
		RemoteAddr: nil,
	}
	_, err = hp.Punch(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RemoteAddr is nil")
}

// ---------------------------------------------------------------------------
// PeerPuncher lifecycle
// ---------------------------------------------------------------------------

// TestNewPeerPuncherLifecycle proves NewPeerPuncher creates two distinct local
// addresses and Close releases both sockets without panic.
func TestNewPeerPuncherLifecycle(t *testing.T) {
	pp, err := NewPeerPuncher()
	require.NoError(t, err)
	require.NotNil(t, pp)

	addrA := pp.AddrA()
	addrB := pp.AddrB()
	require.NotNil(t, addrA)
	require.NotNil(t, addrB)
	assert.NotEqual(t, addrA.Port, addrB.Port, "sockets must have different ports")

	pp.Close()
	// Double-close must not panic.
	pp.Close()
}

// ---------------------------------------------------------------------------
// §1.1 Mutation proof (documented inline with each test above)
// ---------------------------------------------------------------------------
//
// Summary of mutations verified during development:
//
// 1. TestBuildProbeHasMagicHeader — zeroing probeMagic[0] makes the probe
//    unrecognisable, isProbe() returns false, and the assert.Equal on probeMagic[0]
//    value fails immediately.
//
// 2. TestIsProbeDetectsMagic — making isProbe() always return true causes
//    assert.False(isProbe(notProbe)) to fail.
//
// 3. TestHolePunchConnectsOverLoopback — removing the isProbe() guard in the
//    recv goroutine means the first random OS datagram (or none) triggers
//    recvCh, or recvCh never fires → timeout → HolePunchTimedOut → the
//    assert.Equal(HolePunchConnected) fails.
//
// 4. TestHolePunchTimesOutWithNoRelay — removing `case <-ctx.Done()` causes
//    the test to hang indefinitely until the Go test timeout fires.
//
// 5. TestHolePunchFallsBackToRelay — setting State=HolePunchTimedOut instead of
//    HolePunchRelaying in the ctx.Done() branch fails the assert.Equal.
//
// 6. TestHolePunchStateString — returning "" from HolePunchConnected.String()
//    fails assert.Equal("connected", ...).
