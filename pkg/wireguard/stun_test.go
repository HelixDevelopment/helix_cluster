package wireguard

import (
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// encodeXorMappedResponse builds a STUN Binding Success Response carrying a
// single XOR-MAPPED-ADDRESS attribute for the given IPv4 address/port, echoing
// the request's transaction ID. This is the canonical RFC 5389 §15.2 encoding
// the real client must decode.
func encodeXorMappedResponse(txID [stunTxIDLen]byte, ip net.IP, port int) []byte {
	ip4 := ip.To4()

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

	// Attribute TLV: type(2) length(2) value(8).
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

// fakeSTUNServer runs a goroutine that reads one Binding Request and replies
// with an XOR-MAPPED-ADDRESS naming reflectIP:reflectPort. It echoes the
// request transaction ID. The supplied reply function lets a test corrupt the
// reply for negative cases. Returns the server's UDP address and a stop func.
func fakeSTUNServer(t *testing.T, reply func(txID [stunTxIDLen]byte) []byte) (string, func()) {
	t.Helper()
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 1500)
		_ = pc.SetReadDeadline(time.Now().Add(3 * time.Second))
		n, raddr, err := pc.ReadFromUDP(buf)
		if err != nil {
			return
		}
		// Parse the incoming request so the server validates it like a real one.
		msg, err := parseSTUNMessage(buf[:n])
		if err != nil || msg.Type != stunBindingRequest {
			return
		}
		resp := reply(msg.TxID)
		_, _ = pc.WriteToUDP(resp, raddr)
	}()

	return pc.LocalAddr().String(), func() {
		_ = pc.Close()
		<-done
	}
}

// TestSTUNClientDecodesXorMappedAddress proves the client performs a real
// Binding exchange against a local fake server and XOR-decodes the public
// endpoint to the EXACT advertised IP and port.
//
// Mutation that kills it: in xorMappedAddress, skip the XOR step (return the
// raw bytes/port) — the decoded IP becomes 1.18.32.49-style garbage and the
// exact-equality assert on "203.0.113.7:54321" fails.
func TestSTUNClientDecodesXorMappedAddress(t *testing.T) {
	wantIP := net.ParseIP("203.0.113.7")
	wantPort := 54321

	server, stop := fakeSTUNServer(t, func(txID [stunTxIDLen]byte) []byte {
		return encodeXorMappedResponse(txID, wantIP, wantPort)
	})
	defer stop()

	client := &STUNClient{Timeout: 2 * time.Second}
	got, err := client.DiscoverPublicUDPAddr(server)
	require.NoError(t, err)

	assert.Equal(t, "203.0.113.7", got.IP.String())
	assert.Equal(t, wantPort, got.Port)
	// The formatted endpoint is exactly correct.
	assert.Equal(t, "203.0.113.7:54321", got.String())
}

// TestSTUNClientRejectsShortResponse proves a truncated/malformed response is
// rejected rather than silently mis-decoded.
//
// Mutation that kills it: drop the `len(buf) < stunHeaderLen` guard in
// parseSTUNMessage (or the magic-cookie check) — a short buffer would slice
// out of range / be accepted, and the require.Error below fails.
func TestSTUNClientRejectsShortResponse(t *testing.T) {
	server, stop := fakeSTUNServer(t, func(txID [stunTxIDLen]byte) []byte {
		// 10 bytes: shorter than the 20-byte STUN header.
		return []byte{0x01, 0x01, 0x00, 0x00, 0x21, 0x12, 0xA4, 0x42, 0x00, 0x00}
	})
	defer stop()

	client := &STUNClient{Timeout: 2 * time.Second}
	_, err := client.DiscoverPublicUDPAddr(server)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too short")
}

// TestSTUNClientRejectsBadMagicCookie proves a response with the wrong magic
// cookie is rejected (anti-spoof).
//
// Mutation that kills it: remove the cookie comparison in parseSTUNMessage —
// the bogus response is accepted and require.Error fails.
func TestSTUNClientRejectsBadMagicCookie(t *testing.T) {
	server, stop := fakeSTUNServer(t, func(txID [stunTxIDLen]byte) []byte {
		resp := encodeXorMappedResponse(txID, net.ParseIP("198.51.100.9"), 5000)
		// Corrupt the magic cookie.
		resp[4] ^= 0xFF
		return resp
	})
	defer stop()

	client := &STUNClient{Timeout: 2 * time.Second}
	_, err := client.DiscoverPublicUDPAddr(server)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "magic cookie")
}

// TestSTUNClientRejectsTxIDMismatch proves the client rejects a reply whose
// transaction ID does not match the request it sent.
//
// Mutation that kills it: remove the `msg.TxID != txID` check in exchange() —
// the mismatched reply is accepted and require.Error fails.
func TestSTUNClientRejectsTxIDMismatch(t *testing.T) {
	server, stop := fakeSTUNServer(t, func(txID [stunTxIDLen]byte) []byte {
		var wrong [stunTxIDLen]byte
		for i := range wrong {
			wrong[i] = txID[i] ^ 0xFF
		}
		return encodeXorMappedResponse(wrong, net.ParseIP("198.51.100.9"), 5000)
	})
	defer stop()

	client := &STUNClient{Timeout: 2 * time.Second}
	_, err := client.DiscoverPublicUDPAddr(server)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "transaction id")
}

// TestEncodeBindingRequestFormat proves the encoded request matches RFC 5389:
// 20 bytes, Binding-Request type, zero length, magic cookie, embedded txid.
//
// Mutation that kills it: write the wrong message type or omit the cookie in
// encodeBindingRequest — these byte-exact asserts fail.
func TestEncodeBindingRequestFormat(t *testing.T) {
	var txID [stunTxIDLen]byte
	for i := range txID {
		txID[i] = byte(i + 1)
	}
	req := encodeBindingRequest(txID)

	require.Len(t, req, stunHeaderLen)
	assert.Equal(t, stunBindingRequest, binary.BigEndian.Uint16(req[0:2]))
	assert.Equal(t, uint16(0), binary.BigEndian.Uint16(req[2:4]))
	assert.Equal(t, stunMagicCookie, binary.BigEndian.Uint32(req[4:8]))
	assert.Equal(t, txID[:], req[8:20])
}

// TestDiscoverExternalAddressVia proves the nat_traversal wrapper drives a real
// STUN exchange end to end against the local fake server.
//
// Mutation that kills it: have xorMappedAddress skip XOR-decoding — the exact
// endpoint assertion fails.
func TestDiscoverExternalAddressVia(t *testing.T) {
	server, stop := fakeSTUNServer(t, func(txID [stunTxIDLen]byte) []byte {
		return encodeXorMappedResponse(txID, net.ParseIP("192.0.2.33"), 41641)
	})
	defer stop()

	endpoint, err := DiscoverExternalAddressVia(server, 0, 2*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "192.0.2.33:41641", endpoint)
}

// TestDiscoverExternalAddressLegacyErrors proves the legacy no-server entry
// point honestly errors instead of pretending to discover an address.
func TestDiscoverExternalAddressLegacyErrors(t *testing.T) {
	addr, err := DiscoverExternalAddress(51820)
	require.Error(t, err)
	assert.Empty(t, addr)
}
