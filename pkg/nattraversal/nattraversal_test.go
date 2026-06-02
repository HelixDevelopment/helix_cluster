package nattraversal

// Test discipline: every test names the one-line source mutation that makes it
// fail. Assertions check concrete values, not just err==nil or len>0.

import (
	"context"
	"encoding/binary"
	"errors"
	"net/netip"
	"testing"
)

// ────────────────────────────────────────────────────────────────────────────
// Helpers: build synthetic STUN responses for use in transport doubles.
// ────────────────────────────────────────────────────────────────────────────

// buildXorMappedResponse constructs a minimal STUN Binding Success response
// carrying one XOR-MAPPED-ADDRESS attribute for the given IPv4 addr/port,
// echoing txID. This mirrors the encoding in pkg/wireguard/stun_test.go so
// the two packages stay consistent.
func buildXorMappedResponse(txID [txIDLen]byte, ip [4]byte, port uint16) []byte {
	// XOR-encode port and address per RFC 5389 §15.2.
	cookieHi := uint16(magicCookie >> 16)
	xport := port ^ cookieHi

	var cookieB [4]byte
	binary.BigEndian.PutUint32(cookieB[:], magicCookie)

	var xip [4]byte
	for i := 0; i < 4; i++ {
		xip[i] = ip[i] ^ cookieB[i]
	}

	// Attribute value: reserved(1) family(1) x-port(2) x-addr(4) = 8 bytes.
	attrVal := make([]byte, 8)
	attrVal[0] = 0x00
	attrVal[1] = familyIPv4
	binary.BigEndian.PutUint16(attrVal[2:4], xport)
	copy(attrVal[4:8], xip[:])

	// Attribute TLV.
	attr := make([]byte, 4+len(attrVal))
	binary.BigEndian.PutUint16(attr[0:2], attrXorMappedAddress)
	binary.BigEndian.PutUint16(attr[2:4], uint16(len(attrVal)))
	copy(attr[4:], attrVal)

	// STUN header.
	msg := make([]byte, headerLen+len(attr))
	binary.BigEndian.PutUint16(msg[0:2], 0x0101) // Binding Success
	binary.BigEndian.PutUint16(msg[2:4], uint16(len(attr)))
	binary.BigEndian.PutUint32(msg[4:8], magicCookie)
	copy(msg[8:20], txID[:])
	copy(msg[headerLen:], attr)
	return msg
}

// ────────────────────────────────────────────────────────────────────────────
// Section 1: encode → decode round-trip.
// ────────────────────────────────────────────────────────────────────────────

// TestEncodeDecodeRoundTrip checks that EncodeBindingRequest produces a
// 20-byte header that DecodeMessage parses back, preserving TxID and the
// magic cookie.
//
// Mutation that kills it: change encodeRequest to write the wrong magic
// cookie → DecodeMessage returns "invalid magic cookie" and t.Fatal fires.
func TestEncodeDecodeRoundTrip(t *testing.T) {
	encoded, txID, err := EncodeBindingRequest()
	if err != nil {
		t.Fatalf("EncodeBindingRequest: %v", err)
	}
	if len(encoded) != headerLen {
		t.Fatalf("encoded len = %d, want %d", len(encoded), headerLen)
	}

	msg, err := DecodeMessage(encoded)
	if err != nil {
		t.Fatalf("DecodeMessage: %v", err)
	}

	// TxID must be preserved verbatim.
	if msg.TxID != txID {
		t.Errorf("TxID mismatch: decoded %v, want %v", msg.TxID, txID)
	}

	// Magic cookie is validated inside DecodeMessage; verify it was encoded.
	cookieInWire := binary.BigEndian.Uint32(encoded[4:8])
	if cookieInWire != magicCookie {
		t.Errorf("magic cookie in wire = 0x%08X, want 0x%08X", cookieInWire, magicCookie)
	}

	// The message type must be Binding Request.
	if msg.Type != msgTypeBindingRequest {
		t.Errorf("Type = 0x%04X, want 0x%04X (BindingRequest)", msg.Type, msgTypeBindingRequest)
	}

	// No attributes in a Binding Request.
	if len(msg.Attributes) != 0 {
		t.Errorf("Attributes len = %d, want 0", len(msg.Attributes))
	}
}

// TestEncodeDecodeWithAttributesRoundTrip builds a response with an
// XOR-MAPPED-ADDRESS attribute, decodes it, and asserts the exact decoded
// IP address and port. This exercises both the attribute presence check
// (DecodeMessage) and the XOR decode path (MappedAddress/decodeAddr).
//
// Mutation that kills it: change the attribute type constant in
// buildXorMappedResponse to 0x0001 → MappedAddress falls back to
// attrMappedAddress which does no XOR, producing a garbage address, and
// the exact ap.Addr()/ap.Port() assertions fail.
// Second mutation: corrupt the XOR step in decodeAddr (e.g. remove the IP
// XOR loop) → ap.Addr() becomes 10.0.0.1 XOR-cookie ≠ 10.0.0.1, assertion
// fails.
func TestEncodeDecodeWithAttributesRoundTrip(t *testing.T) {
	var fixedTxID [txIDLen]byte
	for i := range fixedTxID {
		fixedTxID[i] = byte(i + 1)
	}
	ip := [4]byte{10, 0, 0, 1}
	const wantPort = uint16(4000)
	resp := buildXorMappedResponse(fixedTxID, ip, wantPort)

	msg, err := DecodeMessage(resp)
	if err != nil {
		t.Fatalf("DecodeMessage: %v", err)
	}
	if msg.TxID != fixedTxID {
		t.Errorf("TxID mismatch: got %v, want %v", msg.TxID, fixedTxID)
	}
	if _, ok := msg.Attributes[attrXorMappedAddress]; !ok {
		t.Errorf("attribute 0x0020 not present after round-trip")
	}

	// Assert that MappedAddress correctly XOR-decodes the attribute value.
	// Mutation: corrupt decodeAddr XOR arithmetic → ap mismatches.
	ap, ok := msg.MappedAddress()
	if !ok {
		t.Fatal("MappedAddress returned false; want a valid XOR-MAPPED-ADDRESS")
	}
	wantAddr := netip.AddrFrom4(ip)
	if ap.Addr() != wantAddr {
		t.Errorf("decoded addr = %s, want %s", ap.Addr(), wantAddr)
	}
	if ap.Port() != wantPort {
		t.Errorf("decoded port = %d, want %d", ap.Port(), wantPort)
	}
	const wantStr = "10.0.0.1:4000"
	if ap.String() != wantStr {
		t.Errorf("AddrPort.String() = %q, want %q", ap.String(), wantStr)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Section 2: independent spec proof using a known RFC5769-style byte vector.
// ────────────────────────────────────────────────────────────────────────────

// stunVectorIP203 is a hand-computed Binding Success response (32 bytes) for:
//
//	IP   = 203.0.113.7
//	Port = 54321
//	TxID = {0x01,0x02,...,0x0C}
//
// Construction (verified by Python):
//
//	magic  = 0x2112A442
//	xport  = 54321 ^ (0x2112A442>>16) = 54321 ^ 0x2112 = 0xF523 (= 62755)
//	cookie = [0x21, 0x12, 0xA4, 0x42]
//	xip    = [203^0x21, 0^0x12, 113^0xA4, 7^0x42] = [0xEA, 0x12, 0xD5, 0x45]
//
// Header: type=0x0101 len=0x000C cookie txID
// Attr  : type=0x0020 len=0x0008 0x00 0x01 xport[2] xip[4]
var stunVectorIP203 = []byte{
	// STUN header (20 bytes)
	0x01, 0x01, // type = BindingSuccess (0x0101)
	0x00, 0x0C, // attribute length = 12
	0x21, 0x12, 0xA4, 0x42, // magic cookie
	0x01, 0x02, 0x03, 0x04, 0x05, 0x06, // TxID bytes 0..5
	0x07, 0x08, 0x09, 0x0A, 0x0B, 0x0C, // TxID bytes 6..11
	// XOR-MAPPED-ADDRESS attribute (12 bytes)
	0x00, 0x20, // attribute type
	0x00, 0x08, // attribute value length = 8
	0x00, 0x01, // reserved, family=IPv4
	0xF5, 0x23, // x-port = 54321 ^ 0x2112
	0xEA, 0x12, 0xD5, 0x45, // x-addr = 203.0.113.7 XOR cookie
}

// TestDecodeKnownVector decodes stunVectorIP203 and asserts the EXACT decoded
// XOR-MAPPED-ADDRESS: 203.0.113.7:54321. This is an independent spec proof
// independent of EncodeBindingRequest.
//
// Mutation that kills it: skip the XOR step in decodeAddr (remove the
// `port ^= uint16(magicCookie>>16)` and the IP-XOR loop) → decoded address
// becomes 0xF5.0x23.0xEA.0x12:62755, not 203.0.113.7:54321, and the
// exact-equality assert fails.
func TestDecodeKnownVector(t *testing.T) {
	msg, err := DecodeMessage(stunVectorIP203)
	if err != nil {
		t.Fatalf("DecodeMessage on known vector: %v", err)
	}

	ap, ok := msg.MappedAddress()
	if !ok {
		t.Fatal("MappedAddress returned false; want a valid XOR-MAPPED-ADDRESS")
	}

	wantAddr := netip.MustParseAddr("203.0.113.7")
	wantPort := uint16(54321)

	if ap.Addr() != wantAddr {
		t.Errorf("IP = %s, want 203.0.113.7", ap.Addr())
	}
	if ap.Port() != wantPort {
		t.Errorf("Port = %d, want 54321", ap.Port())
	}

	// Belt-and-suspenders: string form must match exactly.
	const wantStr = "203.0.113.7:54321"
	if ap.String() != wantStr {
		t.Errorf("AddrPort.String() = %q, want %q", ap.String(), wantStr)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Section 3: malformed input rejection.
// ────────────────────────────────────────────────────────────────────────────

// TestDecodeMessageMalformed exercises the three rejection paths: short buffer,
// bad magic cookie, and attribute-overrun.
//
// Mutation that kills it: remove the `len(buf) < headerLen` guard in
// DecodeMessage → the short-buffer case returns nil error and t.Errorf fires.
func TestDecodeMessageMalformed(t *testing.T) {
	cases := []struct {
		name string
		buf  []byte
		// expectFrag is a substring of the expected error message.
		expectFrag string
	}{
		{
			name:       "too short (10 bytes)",
			buf:        make([]byte, 10),
			expectFrag: "too short",
		},
		{
			name: "bad magic cookie",
			buf: func() []byte {
				b := make([]byte, headerLen)
				binary.BigEndian.PutUint16(b[0:2], msgTypeBindingRequest)
				binary.BigEndian.PutUint32(b[4:8], 0xDEADBEEF) // wrong cookie
				return b
			}(),
			expectFrag: "magic cookie",
		},
		{
			name: "attribute length overruns buffer",
			buf: func() []byte {
				b := make([]byte, headerLen)
				binary.BigEndian.PutUint32(b[4:8], magicCookie)
				binary.BigEndian.PutUint16(b[2:4], 200) // claims 200 bytes of attrs
				return b                                 // only 0 bytes follow the header
			}(),
			expectFrag: "overruns",
		},
		{
			name:       "empty buffer",
			buf:        []byte{},
			expectFrag: "too short",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeMessage(tc.buf)
			if err == nil {
				t.Fatalf("DecodeMessage accepted malformed input %q; want error containing %q", tc.name, tc.expectFrag)
			}
			if tc.expectFrag != "" {
				// Simple substring search without importing strings.
				found := false
				haystack := err.Error()
				needle := tc.expectFrag
				for i := 0; i <= len(haystack)-len(needle); i++ {
					if haystack[i:i+len(needle)] == needle {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("error = %q; want substring %q", err.Error(), tc.expectFrag)
				}
			}
		})
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Section 4: ClassifyNAT table-driven tests.
// ────────────────────────────────────────────────────────────────────────────

func addrPort(ip string, port uint16) netip.AddrPort {
	return netip.AddrPortFrom(netip.MustParseAddr(ip), port)
}

// TestClassifyNAT covers every NATType outcome.
//
// Mutation that kills it: make ClassifyNAT always return NATSymmetric
// → the NATOpen, NATFullCone, NATRestrictedCone, and NATPortRestricted rows
// each fail on their exact-equality assert.
func TestClassifyNAT(t *testing.T) {
	cases := []struct {
		name string
		obs  []Observation
		want NATType
	}{
		{
			name: "no observations → Unknown",
			obs:  nil,
			want: NATUnknown,
		},
		{
			name: "mapped == local → Open",
			obs: []Observation{
				{
					Server: addrPort("1.2.3.4", 3478),
					Local:  addrPort("10.0.0.1", 5000),
					Mapped: addrPort("10.0.0.1", 5000), // same as local
				},
			},
			want: NATOpen,
		},
		{
			name: "two servers, same mapped AddrPort → RestrictedCone",
			obs: []Observation{
				{
					Server: addrPort("1.2.3.4", 3478),
					Local:  addrPort("192.168.1.2", 6000),
					Mapped: addrPort("198.51.100.7", 4321),
				},
				{
					Server: addrPort("5.6.7.8", 3478),
					Local:  addrPort("192.168.1.2", 6000),
					Mapped: addrPort("198.51.100.7", 4321), // same mapped
				},
			},
			want: NATRestrictedCone,
		},
		{
			name: "same mapped IP, different mapped ports → PortRestricted",
			obs: []Observation{
				{
					Server: addrPort("1.2.3.4", 3478),
					Local:  addrPort("192.168.1.2", 6000),
					Mapped: addrPort("198.51.100.7", 4321),
				},
				{
					Server: addrPort("5.6.7.8", 3478),
					Local:  addrPort("192.168.1.2", 6000),
					Mapped: addrPort("198.51.100.7", 9999), // same IP, different port
				},
			},
			want: NATPortRestricted,
		},
		{
			name: "different mapped IPs → Symmetric",
			obs: []Observation{
				{
					Server: addrPort("1.2.3.4", 3478),
					Local:  addrPort("192.168.1.2", 6000),
					Mapped: addrPort("203.0.113.5", 4321),
				},
				{
					Server: addrPort("5.6.7.8", 3478),
					Local:  addrPort("192.168.1.2", 6000),
					Mapped: addrPort("203.0.113.9", 4321), // different IP
				},
			},
			want: NATSymmetric,
		},
		{
			name: "symmetric: both IP and port vary",
			obs: []Observation{
				{
					Server: addrPort("1.2.3.4", 3478),
					Local:  addrPort("10.0.0.5", 7000),
					Mapped: addrPort("203.0.113.1", 1000),
				},
				{
					Server: addrPort("5.6.7.8", 3478),
					Local:  addrPort("10.0.0.5", 7000),
					Mapped: addrPort("203.0.113.2", 2000), // IP + port differ
				},
			},
			want: NATSymmetric,
		},
		{
			name: "single observation, mapped != local → FullCone (conservative)",
			obs: []Observation{
				{
					Server: addrPort("1.2.3.4", 3478),
					Local:  addrPort("192.168.1.2", 6000),
					Mapped: addrPort("198.51.100.7", 4321),
				},
			},
			want: NATFullCone,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyNAT(tc.obs)
			if got != tc.want {
				t.Errorf("ClassifyNAT(...) = %s (%d), want %s (%d)",
					got, got, tc.want, tc.want)
			}
		})
	}
}

// TestNATTypeString checks every NATType has a non-"Unknown" String() except
// NATUnknown itself.
//
// Mutation that kills it: swap two labels in NATType.String() → the
// label/value pairs mismatch and the exact-string asserts fail.
func TestNATTypeString(t *testing.T) {
	cases := []struct {
		n    NATType
		want string
	}{
		{NATUnknown, "Unknown"},
		{NATOpen, "Open"},
		{NATFullCone, "FullCone"},
		{NATRestrictedCone, "RestrictedCone"},
		{NATPortRestricted, "PortRestricted"},
		{NATSymmetric, "Symmetric"},
	}
	for _, tc := range cases {
		if got := tc.n.String(); got != tc.want {
			t.Errorf("NATType(%d).String() = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Section 5: Client with default/nil transport → ErrUnsupported (CLAUDE-2).
// ────────────────────────────────────────────────────────────────────────────

// TestClientNilTransportReturnsErrUnsupported proves that constructing a
// Client with no transport makes Discover return exactly ErrUnsupported and a
// zero AddrPort — never a fabricated address.
//
// Mutation that kills it: fabricate an AddrPort instead of returning
// ErrUnsupported in unsupportedTransport.Exchange → errors.Is(err,
// ErrUnsupported) returns false and t.Fatal fires.
func TestClientNilTransportReturnsErrUnsupported(t *testing.T) {
	c := NewClient(nil)
	server := addrPort("1.2.3.4", 3478)

	ap, err := c.Discover(context.Background(), server)

	// Must return ErrUnsupported.
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Discover(nil transport) err = %v; want errors.Is(_, ErrUnsupported)", err)
	}

	// Must return a zero AddrPort, never a fabricated address.
	if ap.IsValid() {
		t.Errorf("Discover(nil transport) returned valid AddrPort %s; want zero", ap)
	}
}

// TestClientExplicitUnsupportedTransport is the same check for an explicit
// unsupportedTransport value (belt and suspenders).
func TestClientExplicitUnsupportedTransport(t *testing.T) {
	c := NewClient(unsupportedTransport{})
	_, err := c.Discover(context.Background(), addrPort("8.8.8.8", 3478))
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v; want ErrUnsupported", err)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Section 6: Client with injected fake transport → decodes real mapped addr.
// ────────────────────────────────────────────────────────────────────────────

// fakeTransport is a test double that returns a pre-built STUN response.
// It is clearly a test double (lives in _test.go) and does NOT fake the
// network; it exists to drive Discover without real UDP.
type fakeTransport struct {
	// respFn is called with the actual request bytes to produce the response.
	// Using a function lets the fake echo the TxID from the request.
	respFn func(req []byte) []byte
}

func (f *fakeTransport) Exchange(_ context.Context, _ netip.AddrPort, req []byte) ([]byte, error) {
	return f.respFn(req), nil
}

// buildResponseFromRequest constructs a Binding Success response that echoes
// the TxID from req and advertises ip:port in XOR-MAPPED-ADDRESS.
func buildResponseFromRequest(req []byte, ip [4]byte, port uint16) []byte {
	var txID [txIDLen]byte
	copy(txID[:], req[8:20])
	return buildXorMappedResponse(txID, ip, port)
}

// TestClientFakeTransportDecodesResponse proves that Client.Discover, backed
// by a fake transport returning the RFC5769-style response bytes, decodes the
// exact expected mapped AddrPort. This proves the seam is real: the path from
// Transport.Exchange through DecodeMessage through MappedAddress is exercised.
//
// Mutation that kills it: skip the XOR step in decodeAddr → decoded address
// becomes garbage, not 203.0.113.7:54321, and the exact-equality assert fails.
func TestClientFakeTransportDecodesResponse(t *testing.T) {
	wantIP := [4]byte{203, 0, 113, 7}
	wantPort := uint16(54321)

	ft := &fakeTransport{
		respFn: func(req []byte) []byte {
			return buildResponseFromRequest(req, wantIP, wantPort)
		},
	}
	c := NewClient(ft)
	server := addrPort("1.2.3.4", 3478)

	ap, err := c.Discover(context.Background(), server)
	if err != nil {
		t.Fatalf("Discover with fake transport: %v", err)
	}

	wantAddr := netip.AddrFrom4(wantIP)
	if ap.Addr() != wantAddr {
		t.Errorf("addr = %s, want %s", ap.Addr(), wantAddr)
	}
	if ap.Port() != wantPort {
		t.Errorf("port = %d, want %d", ap.Port(), wantPort)
	}
	const wantStr = "203.0.113.7:54321"
	if ap.String() != wantStr {
		t.Errorf("AddrPort.String() = %q, want %q", ap.String(), wantStr)
	}
}

// TestDecodeKnownVectorCodec proves that the static hand-computed byte slice
// stunVectorIP203 is correctly XOR-decoded by DecodeMessage + MappedAddress
// to produce exactly 203.0.113.7:54321. This is a pure codec test; it does
// not go through Discover (and therefore does not require TxID echo).
//
// Mutation that kills it: skip the XOR step in decodeAddr (remove the IP XOR
// loop) → decoded address becomes raw XOR bytes, not 203.0.113.7:54321, and
// the exact-equality assert fails.
func TestDecodeKnownVectorCodec(t *testing.T) {
	msg, err := DecodeMessage(stunVectorIP203)
	if err != nil {
		t.Fatalf("DecodeMessage on known vector: %v", err)
	}

	ap, ok := msg.MappedAddress()
	if !ok {
		t.Fatal("MappedAddress returned false on known vector")
	}

	const wantStr = "203.0.113.7:54321"
	if ap.String() != wantStr {
		t.Errorf("AddrPort.String() = %q, want %q", ap.String(), wantStr)
	}
}

// TestClientRejectsMismatchedTxID proves that Discover returns an error when
// the server response carries a TxID that does not match the request TxID.
// This guards against replayed or injected responses per RFC 5389 §7.3.3.
//
// Mutation that kills it: remove the TxID mismatch check in Discover (delete
// the `if msg.TxID != txID` block) → no error is returned and t.Fatal fires.
func TestClientRejectsMismatchedTxID(t *testing.T) {
	// The fake transport returns stunVectorIP203, whose TxID is fixed at
	// {0x01..0x0C}. The live request will have a crypto-random TxID that
	// virtually never matches, making this a reliable mismatch injector.
	ft := &fakeTransport{
		respFn: func(_ []byte) []byte {
			return stunVectorIP203
		},
	}
	c := NewClient(ft)

	_, err := c.Discover(context.Background(), addrPort("1.2.3.4", 3478))
	if err == nil {
		t.Fatal("Discover accepted a response with a mismatched TxID; want error")
	}
	// Verify the error message names TxID explicitly so the rejection reason is
	// clear (not masked by a generic decode error).
	const wantFrag = "TxID mismatch"
	found := false
	haystack := err.Error()
	for i := 0; i <= len(haystack)-len(wantFrag); i++ {
		if haystack[i:i+len(wantFrag)] == wantFrag {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("error = %q; want substring %q", err.Error(), wantFrag)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Section 7: MappedAddress fallback to MAPPED-ADDRESS (plain, no XOR).
// ────────────────────────────────────────────────────────────────────────────

// TestMappedAddressFallback proves that when XOR-MAPPED-ADDRESS is absent but
// MAPPED-ADDRESS is present, MappedAddress falls back to the plain attribute.
//
// Mutation that kills it: remove the fallback branch in MappedAddress (delete
// the attrMappedAddress case) → ok=false and t.Fatal fires.
func TestMappedAddressFallback(t *testing.T) {
	// Build a response with only a plain MAPPED-ADDRESS (no XOR).
	ip := [4]byte{192, 0, 2, 1}
	port := uint16(8080)

	attrVal := make([]byte, 8)
	attrVal[0] = 0x00
	attrVal[1] = familyIPv4
	binary.BigEndian.PutUint16(attrVal[2:4], port) // plain, no XOR
	copy(attrVal[4:8], ip[:])

	attr := make([]byte, 4+len(attrVal))
	binary.BigEndian.PutUint16(attr[0:2], attrMappedAddress) // 0x0001
	binary.BigEndian.PutUint16(attr[2:4], uint16(len(attrVal)))
	copy(attr[4:], attrVal)

	var txID [txIDLen]byte
	buf := make([]byte, headerLen+len(attr))
	binary.BigEndian.PutUint16(buf[0:2], 0x0101)
	binary.BigEndian.PutUint16(buf[2:4], uint16(len(attr)))
	binary.BigEndian.PutUint32(buf[4:8], magicCookie)
	copy(buf[8:20], txID[:])
	copy(buf[headerLen:], attr)

	msg, err := DecodeMessage(buf)
	if err != nil {
		t.Fatalf("DecodeMessage: %v", err)
	}
	ap, ok := msg.MappedAddress()
	if !ok {
		t.Fatal("MappedAddress returned false; want fallback to MAPPED-ADDRESS")
	}
	wantAddr := netip.AddrFrom4(ip)
	if ap.Addr() != wantAddr {
		t.Errorf("addr = %s, want %s", ap.Addr(), wantAddr)
	}
	if ap.Port() != port {
		t.Errorf("port = %d, want %d", ap.Port(), port)
	}
}
