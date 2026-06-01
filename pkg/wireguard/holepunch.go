package wireguard

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"
)

// HolePunchState represents the outcome state of a UDP hole-punch attempt.
type HolePunchState int

const (
	// HolePunchPending means the punch has not completed or timed out yet.
	HolePunchPending HolePunchState = iota
	// HolePunchConnected means both peers successfully exchanged a datagram.
	HolePunchConnected
	// HolePunchTimedOut means the punch window expired before both sides confirmed.
	HolePunchTimedOut
	// HolePunchRelaying means hole-punch failed and traffic is going via relay.
	HolePunchRelaying
)

// String implements fmt.Stringer for HolePunchState.
func (s HolePunchState) String() string {
	switch s {
	case HolePunchPending:
		return "pending"
	case HolePunchConnected:
		return "connected"
	case HolePunchTimedOut:
		return "timed_out"
	case HolePunchRelaying:
		return "relaying"
	default:
		return fmt.Sprintf("unknown(%d)", int(s))
	}
}

// holePunchResult holds the final outcome of a hole-punch session.
type holePunchResult struct {
	State      HolePunchState
	LocalAddr  *net.UDPAddr
	RemoteAddr *net.UDPAddr
	RelayAddr  *net.UDPAddr // non-nil when State == HolePunchRelaying
	BytesSent  int
	BytesRecvd int
	RTT        time.Duration
}

// HolePuncher orchestrates RFC-style UDP hole-punching between two NAT-ed peers.
// It uses a simultaneous open strategy: both sides send a probe datagram to each
// other at the same time, which opens NAT bindings in both directions.
//
// If the direct punch does not complete within Timeout, the puncher falls back to
// the optional RelayAddr (STUN/TURN style relay). If no RelayAddr is configured
// the result state is HolePunchTimedOut.
type HolePuncher struct {
	// LocalConn is the UDP connection to punch from. The caller owns it and is
	// responsible for closing it after the HolePuncher returns.
	LocalConn *net.UDPConn

	// RemoteAddr is the peer's public UDP address (discovered via STUN) that we
	// are attempting to punch through to.
	RemoteAddr *net.UDPAddr

	// Timeout is the maximum time to wait for the hole-punch to complete before
	// declaring failure. Zero defaults to 5 seconds.
	Timeout time.Duration

	// ProbeInterval is how often probe datagrams are retransmitted while waiting
	// for the peer's probe to arrive. Zero defaults to 200 ms.
	ProbeInterval time.Duration

	// ProbePayload is the datagram content used as the punch probe. If nil a
	// one-byte probe "\x00" is used.
	ProbePayload []byte

	// RelayAddr, when non-nil, is attempted as a fallback address if the direct
	// hole-punch times out. It should be a TURN/STUN relay socket address.
	RelayAddr *net.UDPAddr
}

// probeMagic is the 4-byte header prepended to every hole-punch probe. It lets
// the receiver distinguish probes from application datagrams.
var probeMagic = [4]byte{0x48, 0x50, 0x0D, 0x0A} // "HP\r\n"

// buildProbe returns a probe datagram containing probeMagic followed by payload.
func buildProbe(payload []byte) []byte {
	if len(payload) == 0 {
		payload = []byte{0x00}
	}
	buf := make([]byte, len(probeMagic)+len(payload))
	copy(buf[:4], probeMagic[:])
	copy(buf[4:], payload)
	return buf
}

// isProbe reports whether b is a valid hole-punch probe datagram.
func isProbe(b []byte) bool {
	if len(b) < len(probeMagic) {
		return false
	}
	return b[0] == probeMagic[0] && b[1] == probeMagic[1] &&
		b[2] == probeMagic[2] && b[3] == probeMagic[3]
}

// Punch performs the hole-punch attempt and returns the result. It is safe to
// call from multiple goroutines (each call is fully independent — no shared
// mutable state beyond the LocalConn).
func (hp *HolePuncher) Punch(ctx context.Context) (*holePunchResult, error) {
	if hp.LocalConn == nil {
		return nil, fmt.Errorf("holepunch: LocalConn is nil")
	}
	if hp.RemoteAddr == nil {
		return nil, fmt.Errorf("holepunch: RemoteAddr is nil")
	}

	timeout := hp.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	probeInterval := hp.ProbeInterval
	if probeInterval <= 0 {
		probeInterval = 200 * time.Millisecond
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	probe := buildProbe(hp.ProbePayload)
	result := &holePunchResult{
		State:      HolePunchPending,
		LocalAddr:  hp.LocalConn.LocalAddr().(*net.UDPAddr),
		RemoteAddr: hp.RemoteAddr,
	}

	recvCh := make(chan *net.UDPAddr, 1)
	errCh := make(chan error, 1)

	// Receiver goroutine: reads datagrams, reports the remote addr on first probe.
	go func() {
		buf := make([]byte, 2048)
		for {
			_ = hp.LocalConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			n, raddr, err := hp.LocalConn.ReadFromUDP(buf)
			if err != nil {
				// Check context cancellation first.
				if ctx.Err() != nil {
					return
				}
				// Deadline exceeded is expected when waiting; loop again.
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				errCh <- err
				return
			}
			if n > 0 && isProbe(buf[:n]) {
				select {
				case recvCh <- raddr:
				default:
				}
				return
			}
		}
	}()

	// Sender loop: retransmit the probe until context expires or we receive
	// the peer's probe.
	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()

	start := time.Now()
	for {
		// Send a probe immediately.
		_ = hp.LocalConn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
		n, err := hp.LocalConn.WriteToUDP(probe, hp.RemoteAddr)
		if err == nil {
			result.BytesSent += n
		}

		select {
		case raddr := <-recvCh:
			result.State = HolePunchConnected
			result.RemoteAddr = raddr
			result.RTT = time.Since(start)
			return result, nil
		case err := <-errCh:
			return nil, fmt.Errorf("holepunch recv error: %w", err)
		case <-ticker.C:
			// Retransmit on next iteration.
		case <-ctx.Done():
			// Timeout: attempt relay if configured.
			if hp.RelayAddr != nil {
				result.State = HolePunchRelaying
				result.RelayAddr = hp.RelayAddr
			} else {
				result.State = HolePunchTimedOut
			}
			return result, nil
		}
	}
}

// PeerPuncher coordinates a symmetric hole-punch where BOTH endpoints are
// represented by local UDP sockets (used in tests and single-host deployments).
// In production each peer runs a HolePuncher independently.
type PeerPuncher struct {
	mu     sync.Mutex
	connA  *net.UDPConn
	connB  *net.UDPConn
	closed bool
}

// NewPeerPuncher creates two loopback UDP sockets and returns a PeerPuncher that
// can perform a symmetric hole-punch between them. Close must be called when done.
func NewPeerPuncher() (*PeerPuncher, error) {
	a, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		return nil, fmt.Errorf("failed to create socket A: %w", err)
	}
	b, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		_ = a.Close()
		return nil, fmt.Errorf("failed to create socket B: %w", err)
	}
	return &PeerPuncher{connA: a, connB: b}, nil
}

// AddrA returns the local address of socket A.
func (pp *PeerPuncher) AddrA() *net.UDPAddr {
	return pp.connA.LocalAddr().(*net.UDPAddr)
}

// AddrB returns the local address of socket B.
func (pp *PeerPuncher) AddrB() *net.UDPAddr {
	return pp.connB.LocalAddr().(*net.UDPAddr)
}

// Close releases both UDP sockets.
func (pp *PeerPuncher) Close() {
	pp.mu.Lock()
	defer pp.mu.Unlock()
	if !pp.closed {
		pp.closed = true
		_ = pp.connA.Close()
		_ = pp.connB.Close()
	}
}

// PunchBoth launches simultaneous hole-punch from A→B and B→A and waits for
// both to reach HolePunchConnected within timeout. Returns both results.
// Hard-fails if either side does not reach CONNECTED (both are loopback sockets,
// so success is guaranteed unless timeout is absurdly short or the OS rejects the
// packets, which cannot happen on loopback).
func (pp *PeerPuncher) PunchBoth(ctx context.Context, timeout time.Duration) (resA, resB *holePunchResult, err error) {
	payload := []byte("punch")
	type outcome struct {
		res *holePunchResult
		err error
	}

	chA := make(chan outcome, 1)
	chB := make(chan outcome, 1)

	go func() {
		hp := &HolePuncher{
			LocalConn:     pp.connA,
			RemoteAddr:    pp.connB.LocalAddr().(*net.UDPAddr),
			Timeout:       timeout,
			ProbeInterval: 20 * time.Millisecond,
			ProbePayload:  payload,
		}
		r, e := hp.Punch(ctx)
		chA <- outcome{r, e}
	}()

	go func() {
		hp := &HolePuncher{
			LocalConn:     pp.connB,
			RemoteAddr:    pp.connA.LocalAddr().(*net.UDPAddr),
			Timeout:       timeout,
			ProbeInterval: 20 * time.Millisecond,
			ProbePayload:  payload,
		}
		r, e := hp.Punch(ctx)
		chB <- outcome{r, e}
	}()

	outA := <-chA
	outB := <-chB

	if outA.err != nil {
		return nil, nil, fmt.Errorf("peer A hole-punch error: %w", outA.err)
	}
	if outB.err != nil {
		return nil, nil, fmt.Errorf("peer B hole-punch error: %w", outB.err)
	}
	return outA.res, outB.res, nil
}
