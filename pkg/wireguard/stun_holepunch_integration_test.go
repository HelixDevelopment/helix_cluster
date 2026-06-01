//go:build integration

package wireguard

// Integration tests for HXC-1090: real STUN client (RFC 5389) + UDP
// hole-punching over loopback.
//
// Build tag: //go:build integration — NOT run by the hermetic unit command.
// The orchestrator runs these against the real host OS.
//
// Policy (per CLAUDE-1 + stream brief):
//   - Hard-FAIL (t.Fatalf) on any unexpected error; never t.Skip on an error
//     that implies the feature is broken.
//   - t.Skip is used ONLY for genuine environmental absence (e.g. no network
//     interface — impossible on any real host, so there is none here).
//   - Every test embeds a per-run UUID and asserts it on the SINK SIDE,
//     proving THIS run's data reached the destination.
//
// All I/O is loopback (127.0.0.1); no external network is contacted.

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// In-process STUN responder
// ---------------------------------------------------------------------------

// startInProcSTUNServer starts an RFC 5389–compliant STUN responder on a random
// loopback UDP port. It reflects the sender's actual local (loopback) address
// back via the XOR-MAPPED-ADDRESS attribute, encoding it per RFC 5389 §15.2.
// The server processes multiple requests until its context is cancelled.
// Returns the server address and a stop func.
func startInProcSTUNServer(t *testing.T, ctx context.Context) string {
	t.Helper()
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("startInProcSTUNServer: ListenUDP: %v", err)
	}

	serverAddr := pc.LocalAddr().String()

	go func() {
		defer pc.Close()
		buf := make([]byte, 1500)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			_ = pc.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
			n, raddr, err := pc.ReadFromUDP(buf)
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					continue
				}
				return // context cancelled or closed
			}

			msg, err := parseSTUNMessage(buf[:n])
			if err != nil || msg.Type != stunBindingRequest {
				continue // ignore malformed
			}

			resp := buildXorMappedResponse(msg.TxID, raddr.IP, raddr.Port)
			_, _ = pc.WriteToUDP(resp, raddr)
		}
	}()

	return serverAddr
}

// buildXorMappedResponse encodes a STUN Binding Success Response carrying a
// single XOR-MAPPED-ADDRESS attribute for the given IPv4 address/port, using
// the request's transaction ID. This duplicates the test helper in stun_test.go
// but is intentionally independent so integration tests compile standalone.
func buildXorMappedResponse(txID [stunTxIDLen]byte, ip net.IP, port int) []byte {
	ip4 := ip.To4()
	if ip4 == nil {
		ip4 = net.IPv4(127, 0, 0, 1)
	}

	// Attribute value: reserved(1) family(1) x-port(2) x-addr(4) = 8 bytes.
	val := make([]byte, 8)
	val[0] = 0x00
	val[1] = stunFamilyIPv4
	binary.BigEndian.PutUint16(val[2:4], uint16(port)^uint16(stunMagicCookie>>16))
	cookie := make([]byte, 4)
	binary.BigEndian.PutUint32(cookie, stunMagicCookie)
	for i := 0; i < 4; i++ {
		val[4+i] = ip4[i] ^ cookie[i]
	}

	attr := make([]byte, 4+len(val))
	binary.BigEndian.PutUint16(attr[0:2], stunAttrXorMappedAddress)
	binary.BigEndian.PutUint16(attr[2:4], uint16(len(val)))
	copy(attr[4:], val)

	msg := make([]byte, stunHeaderLen+len(attr))
	binary.BigEndian.PutUint16(msg[0:2], stunBindingSuccess)
	binary.BigEndian.PutUint16(msg[2:4], uint16(len(attr)))
	binary.BigEndian.PutUint32(msg[4:8], stunMagicCookie)
	copy(msg[8:20], txID[:])
	copy(msg[stunHeaderLen:], attr)
	return msg
}

// ---------------------------------------------------------------------------
// Integration test 1: real STUN client vs in-process responder
// ---------------------------------------------------------------------------

// TestIntegration_STUNClientReturnsLoopbackMappedAddress starts an in-process
// STUN server on 127.0.0.1, runs the real STUNClient against it over a real
// loopback UDP socket, and asserts that the returned XOR-MAPPED-ADDRESS
// exactly matches the client socket's actual local address.
//
// Per-run UUID: embedded as a DNS label in a tag printed to t.Log so the
// orchestrator can correlate this run's log line with the run ID.
// The true sink-side assertion is: decoded mapped IP == client's local IP AND
// mapped port == client's local port.
//
// Hard-fail policy: t.Fatalf on any error (loopback + in-process — no env
// dependency can be absent).
func TestIntegration_STUNClientReturnsLoopbackMappedAddress(t *testing.T) {
	runID := uuid.New().String()
	t.Logf("run-id=%s TestIntegration_STUNClientReturnsLoopbackMappedAddress", runID)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	serverAddr := startInProcSTUNServer(t, ctx)

	// Dial the STUN server from a random ephemeral local port so we know the
	// exact local address that will be reflected.
	raddr, err := net.ResolveUDPAddr("udp4", serverAddr)
	if err != nil {
		t.Fatalf("ResolveUDPAddr(%q): %v", serverAddr, err)
	}
	conn, err := net.DialUDP("udp4", nil, raddr)
	if err != nil {
		t.Fatalf("DialUDP to in-proc STUN server: %v", err)
	}
	defer conn.Close()

	// Capture the client's actual local address BEFORE sending the request.
	localAddr := conn.LocalAddr().(*net.UDPAddr)
	t.Logf("run-id=%s client local addr: %s", runID, localAddr)

	client := &STUNClient{Timeout: 5 * time.Second}
	mapped, err := client.exchange(conn)
	if err != nil {
		t.Fatalf("STUN exchange failed: %v", err)
	}

	t.Logf("run-id=%s mapped address returned: %s", runID, mapped)

	// SINK-SIDE ASSERTION: the XOR-decoded mapped address must equal the real
	// local address of the client socket — byte-for-byte, not just "non-nil".
	if !mapped.IP.Equal(localAddr.IP) {
		t.Fatalf("run-id=%s mapped IP %s != client local IP %s",
			runID, mapped.IP, localAddr.IP)
	}
	if mapped.Port != localAddr.Port {
		t.Fatalf("run-id=%s mapped port %d != client local port %d",
			runID, mapped.Port, localAddr.Port)
	}

	// Formatted string must match.
	require.Equal(t, localAddr.String(), mapped.String(),
		"run-id=%s formatted mapped address must equal client local addr", runID)
}

// ---------------------------------------------------------------------------
// Integration test 2: hole-punch + UUID datagram exchange both ways
// ---------------------------------------------------------------------------

// TestIntegration_HolePunchExchangesUUIDDatagramBothWays creates two real
// loopback UDP sockets, performs a symmetric hole-punch, then has each side
// send a datagram containing the per-run UUID to the other and asserts that
// BOTH sides receive the datagram.
//
// Sink-side assertions:
//   - Both sides reach HolePunchConnected (not pending/timed-out/relaying).
//   - Side A receives a datagram from side B containing runID.
//   - Side B receives a datagram from side A containing runID.
//
// Hard-fail policy: t.Fatalf on any step failure (loopback — no external dep).
func TestIntegration_HolePunchExchangesUUIDDatagramBothWays(t *testing.T) {
	runID := uuid.New().String()
	t.Logf("run-id=%s TestIntegration_HolePunchExchangesUUIDDatagramBothWays", runID)

	pp, err := NewPeerPuncher()
	if err != nil {
		t.Fatalf("run-id=%s NewPeerPuncher: %v", runID, err)
	}
	defer pp.Close()

	addrA := pp.AddrA()
	addrB := pp.AddrB()
	t.Logf("run-id=%s socket A: %s  socket B: %s", runID, addrA, addrB)

	// --- Phase 1: perform the hole-punch ---
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resA, resB, err := pp.PunchBoth(ctx, 5*time.Second)
	if err != nil {
		t.Fatalf("run-id=%s PunchBoth: %v", runID, err)
	}
	if resA.State != HolePunchConnected {
		t.Fatalf("run-id=%s side A state = %s, want connected", runID, resA.State)
	}
	if resB.State != HolePunchConnected {
		t.Fatalf("run-id=%s side B state = %s, want connected", runID, resB.State)
	}
	t.Logf("run-id=%s hole-punch connected: A RTT=%s B RTT=%s", runID, resA.RTT, resB.RTT)

	// --- Phase 2: exchange per-run UUID datagrams both ways ---
	//
	// After a successful hole-punch the NAT bindings are open; we now use the
	// same sockets to exchange application datagrams and assert each side
	// receives the exact run UUID from the other.

	msgFromA := fmt.Sprintf("from-A run-id=%s", runID)
	msgFromB := fmt.Sprintf("from-B run-id=%s", runID)

	recvAtB := make(chan string, 1)
	recvAtA := make(chan string, 1)

	// Receiver at B.
	go func() {
		buf := make([]byte, 512)
		_ = pp.connB.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			n, _, err := pp.connB.ReadFromUDP(buf)
			if err != nil {
				return
			}
			text := string(buf[:n])
			// Skip hole-punch probes; we want the application-layer message.
			if !isProbe(buf[:n]) {
				select {
				case recvAtB <- text:
				default:
				}
				return
			}
		}
	}()

	// Receiver at A.
	go func() {
		buf := make([]byte, 512)
		_ = pp.connA.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			n, _, err := pp.connA.ReadFromUDP(buf)
			if err != nil {
				return
			}
			text := string(buf[:n])
			if !isProbe(buf[:n]) {
				select {
				case recvAtA <- text:
				default:
				}
				return
			}
		}
	}()

	// Give receivers a moment to start.
	time.Sleep(5 * time.Millisecond)

	// Send A→B.
	_ = pp.connA.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := pp.connA.WriteToUDP([]byte(msgFromA), addrB); err != nil {
		t.Fatalf("run-id=%s WriteToUDP A→B: %v", runID, err)
	}

	// Send B→A.
	_ = pp.connB.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := pp.connB.WriteToUDP([]byte(msgFromB), addrA); err != nil {
		t.Fatalf("run-id=%s WriteToUDP B→A: %v", runID, err)
	}

	// Assert B received from A.
	select {
	case got := <-recvAtB:
		// Sink-side: the exact run UUID must be present in the datagram body.
		assert.Contains(t, got, runID,
			"run-id=%s B must receive datagram containing run UUID", runID)
		assert.Contains(t, got, "from-A",
			"run-id=%s B must receive datagram from A", runID)
		t.Logf("run-id=%s B received: %q", runID, got)
	case <-time.After(5 * time.Second):
		t.Fatalf("run-id=%s B did not receive datagram from A within timeout", runID)
	}

	// Assert A received from B.
	select {
	case got := <-recvAtA:
		assert.Contains(t, got, runID,
			"run-id=%s A must receive datagram containing run UUID", runID)
		assert.Contains(t, got, "from-B",
			"run-id=%s A must receive datagram from B", runID)
		t.Logf("run-id=%s A received: %q", runID, got)
	case <-time.After(5 * time.Second):
		t.Fatalf("run-id=%s A did not receive datagram from B within timeout", runID)
	}
}

// ---------------------------------------------------------------------------
// Integration test 3: XOR decode correctness via round-trip
// ---------------------------------------------------------------------------

// TestIntegration_XORDecodeRoundTripViaRealSocket proves that the XOR
// decode path in xorMappedAddress produces the correct port — not the
// raw (XOR-encoded) value — by comparing the returned mapped port against the
// real socket port measured from conn.LocalAddr().
//
// This is the mutation-sentinel test for the XOR decode: if the XOR step is
// skipped the port will be: raw_port ^ 0x2112 which differs from the real port
// except by astronomically unlikely coincidence.
//
// Per-run UUID is logged so the orchestrator can correlate this run.
func TestIntegration_XORDecodeRoundTripViaRealSocket(t *testing.T) {
	runID := uuid.New().String()
	t.Logf("run-id=%s TestIntegration_XORDecodeRoundTripViaRealSocket", runID)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	serverAddr := startInProcSTUNServer(t, ctx)

	raddr, err := net.ResolveUDPAddr("udp4", serverAddr)
	if err != nil {
		t.Fatalf("run-id=%s ResolveUDPAddr: %v", runID, err)
	}
	conn, err := net.DialUDP("udp4", nil, raddr)
	if err != nil {
		t.Fatalf("run-id=%s DialUDP: %v", runID, err)
	}
	defer conn.Close()

	realLocal := conn.LocalAddr().(*net.UDPAddr)
	t.Logf("run-id=%s real local port: %d", runID, realLocal.Port)

	client := &STUNClient{Timeout: 5 * time.Second}
	mapped, err := client.exchange(conn)
	if err != nil {
		t.Fatalf("run-id=%s STUN exchange: %v", runID, err)
	}

	t.Logf("run-id=%s mapped port from STUN: %d", runID, mapped.Port)

	// SINK-SIDE MUTATION GATE: if XOR decode were skipped, mapped.Port would
	// equal realLocal.Port ^ 0x2112, which is almost certainly ≠ realLocal.Port.
	if mapped.Port != realLocal.Port {
		t.Fatalf("run-id=%s XOR decode wrong: mapped port %d != real port %d (XOR skipped?)",
			runID, mapped.Port, realLocal.Port)
	}
}
