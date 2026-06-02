// Package nattraversal provides a STUN message codec (RFC 5389) and a
// NAT-type classifier (RFC 3489 §10.2) for use within the HelixCluster
// NAT-traversal subsystem.
//
// CLAUDE-2 boundary: codec and classifier are fully exercisable in-process
// without any network; actual UDP discovery requires a real STUN server and
// returns [ErrUnsupported] when none is available. An injectable [Transport]
// seam allows tests to drive [Client.Discover] deterministically.
package nattraversal

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
)

// ────────────────────────────────────────────────────────────────────────────
// Wire-format constants (RFC 5389 §6, §15).
// ────────────────────────────────────────────────────────────────────────────

const (
	// magicCookie is the fixed 32-bit value at bytes 4..7 of every STUN
	// message, mandated by RFC 5389 §6.
	magicCookie uint32 = 0x2112A442

	// msgTypeBindingRequest is the STUN message type for a Binding Request.
	msgTypeBindingRequest uint16 = 0x0001

	// attrXorMappedAddress is the STUN attribute type for XOR-MAPPED-ADDRESS.
	attrXorMappedAddress uint16 = 0x0020
	// attrMappedAddress is the STUN attribute type for the legacy
	// MAPPED-ADDRESS (RFC 3489).
	attrMappedAddress uint16 = 0x0001

	// familyIPv4 / familyIPv6 are the address-family codes inside
	// (XOR-)MAPPED-ADDRESS attribute values.
	familyIPv4 uint8 = 0x01
	familyIPv6 uint8 = 0x02

	// headerLen is the fixed STUN message header size in bytes.
	headerLen = 20
	// txIDLen is the 96-bit transaction-ID field length.
	txIDLen = 12
)

// ────────────────────────────────────────────────────────────────────────────
// Public sentinel error.
// ────────────────────────────────────────────────────────────────────────────

// ErrUnsupported is returned by [Client.Discover] when no real UDP transport
// is available (e.g. the default nil-transport) and is also returned by the
// package-level no-op default to prevent PASS-bluffs per CLAUDE-2.
var ErrUnsupported = errors.New("nattraversal: STUN discovery unsupported in this environment")

// ────────────────────────────────────────────────────────────────────────────
// Message type.
// ────────────────────────────────────────────────────────────────────────────

// Message is a decoded STUN message: header fields plus a map from attribute
// type to raw attribute value bytes (excluding the 4-byte TLV header).
// Multiple occurrences of the same attribute type are NOT de-duplicated; only
// the last value is retained. This is sufficient for the Binding
// Request/Response exchange.
type Message struct {
	// Type is the 16-bit STUN message type (e.g. 0x0001 = Binding Request).
	Type uint16
	// TxID is the 96-bit transaction identifier echoed by the server.
	TxID [txIDLen]byte
	// Attributes maps attribute type → raw value bytes.
	Attributes map[uint16][]byte
}

// ────────────────────────────────────────────────────────────────────────────
// Codec — encode / decode.
// ────────────────────────────────────────────────────────────────────────────

// EncodeBindingRequest encodes a 20-byte STUN Binding Request with a fresh
// crypto/rand transaction ID and no attributes. It returns the encoded bytes
// and the transaction ID so the caller can verify the server echo.
func EncodeBindingRequest() (encoded []byte, txID [txIDLen]byte, err error) {
	if _, err = rand.Read(txID[:]); err != nil {
		return nil, txID, fmt.Errorf("nattraversal: generate txID: %w", err)
	}
	encoded = encodeRequest(txID)
	return encoded, txID, nil
}

// encodeRequest builds the 20-byte header for a Binding Request with the
// given transaction ID. It is package-private so tests can supply a fixed ID.
func encodeRequest(txID [txIDLen]byte) []byte {
	buf := make([]byte, headerLen)
	binary.BigEndian.PutUint16(buf[0:2], msgTypeBindingRequest)
	binary.BigEndian.PutUint16(buf[2:4], 0) // no attributes
	binary.BigEndian.PutUint32(buf[4:8], magicCookie)
	copy(buf[8:20], txID[:])
	return buf
}

// DecodeMessage validates and parses a raw STUN message. It checks:
//   - minimum length (≥ 20 bytes),
//   - magic cookie at bytes 4..7,
//   - that the declared attribute-section length does not exceed the buffer.
//
// It returns a populated [Message] or a descriptive error.
func DecodeMessage(buf []byte) (*Message, error) {
	if len(buf) < headerLen {
		return nil, fmt.Errorf("nattraversal: message too short: %d bytes (min %d)", len(buf), headerLen)
	}
	cookie := binary.BigEndian.Uint32(buf[4:8])
	if cookie != magicCookie {
		return nil, fmt.Errorf("nattraversal: invalid magic cookie: got 0x%08X, want 0x%08X", cookie, magicCookie)
	}
	attrLen := int(binary.BigEndian.Uint16(buf[2:4]))
	if headerLen+attrLen > len(buf) {
		return nil, fmt.Errorf("nattraversal: attribute length %d overruns buffer (buf=%d)", attrLen, len(buf))
	}

	m := &Message{
		Type:       binary.BigEndian.Uint16(buf[0:2]),
		Attributes: make(map[uint16][]byte),
	}
	copy(m.TxID[:], buf[8:20])

	// Walk the attribute TLV list (4-byte headers, 4-byte-padded values).
	attrs := buf[headerLen : headerLen+attrLen]
	for len(attrs) >= 4 {
		atype := binary.BigEndian.Uint16(attrs[0:2])
		alen := int(binary.BigEndian.Uint16(attrs[2:4]))
		if 4+alen > len(attrs) {
			return nil, fmt.Errorf("nattraversal: attribute 0x%04X length %d overruns attribute section", atype, alen)
		}
		val := make([]byte, alen)
		copy(val, attrs[4:4+alen])
		m.Attributes[atype] = val

		// Advance by header + padded value.
		padded := alen
		if rem := alen % 4; rem != 0 {
			padded += 4 - rem
		}
		next := 4 + padded
		if next > len(attrs) {
			break
		}
		attrs = attrs[next:]
	}
	return m, nil
}

// ────────────────────────────────────────────────────────────────────────────
// MappedAddress decoder.
// ────────────────────────────────────────────────────────────────────────────

// MappedAddress decodes the server-reflexive address from the message. It
// prefers XOR-MAPPED-ADDRESS (0x0020) over the legacy MAPPED-ADDRESS (0x0001).
// The XOR step follows RFC 5389 §15.2: port is XORed with the high 16 bits of
// the magic cookie; IPv4 address bytes are XORed with the cookie; IPv6 with
// cookie||txID.
//
// Returns (addr, true) on success, (zero, false) if neither attribute is
// present or the value is malformed.
func (m *Message) MappedAddress() (netip.AddrPort, bool) {
	if val, ok := m.Attributes[attrXorMappedAddress]; ok {
		if ap, err := decodeAddr(attrXorMappedAddress, val, m.TxID); err == nil {
			return ap, true
		}
	}
	if val, ok := m.Attributes[attrMappedAddress]; ok {
		if ap, err := decodeAddr(attrMappedAddress, val, m.TxID); err == nil {
			return ap, true
		}
	}
	return netip.AddrPort{}, false
}

// decodeAddr decodes one (XOR-)MAPPED-ADDRESS attribute value. attrType
// controls whether XOR decoding is applied.
func decodeAddr(attrType uint16, value []byte, txID [txIDLen]byte) (netip.AddrPort, error) {
	if len(value) < 4 {
		return netip.AddrPort{}, fmt.Errorf("nattraversal: mapped-address value too short (%d bytes)", len(value))
	}
	family := value[1]
	rawPort := binary.BigEndian.Uint16(value[2:4])
	addrBytes := value[4:]

	var port uint16
	if attrType == attrXorMappedAddress {
		port = rawPort ^ uint16(magicCookie>>16)
	} else {
		port = rawPort
	}

	var cookieB [4]byte
	binary.BigEndian.PutUint32(cookieB[:], magicCookie)

	switch family {
	case familyIPv4:
		if len(addrBytes) < 4 {
			return netip.AddrPort{}, fmt.Errorf("nattraversal: IPv4 mapped-address too short (%d bytes)", len(addrBytes))
		}
		var ipb [4]byte
		copy(ipb[:], addrBytes[:4])
		if attrType == attrXorMappedAddress {
			for i := 0; i < 4; i++ {
				ipb[i] ^= cookieB[i]
			}
		}
		addr := netip.AddrFrom4(ipb)
		return netip.AddrPortFrom(addr, port), nil

	case familyIPv6:
		if len(addrBytes) < 16 {
			return netip.AddrPort{}, fmt.Errorf("nattraversal: IPv6 mapped-address too short (%d bytes)", len(addrBytes))
		}
		var ipb [16]byte
		copy(ipb[:], addrBytes[:16])
		if attrType == attrXorMappedAddress {
			var key [16]byte
			copy(key[0:4], cookieB[:])
			copy(key[4:16], txID[:])
			for i := 0; i < 16; i++ {
				ipb[i] ^= key[i]
			}
		}
		addr := netip.AddrFrom16(ipb)
		return netip.AddrPortFrom(addr, port), nil

	default:
		return netip.AddrPort{}, fmt.Errorf("nattraversal: unknown address family 0x%02X", family)
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Transport seam + Client.
// ────────────────────────────────────────────────────────────────────────────

// Transport is the single UDP-exchange seam injected into [Client]. The
// implementation receives the raw encoded STUN request bytes, sends them to
// server over UDP, reads the response, and returns it.
//
// The real default transport (when NewClient is called with nil) always returns
// [ErrUnsupported] — no fabricated addresses, per CLAUDE-2.
type Transport interface {
	// Exchange sends req to server and returns the raw STUN response bytes.
	Exchange(ctx context.Context, server netip.AddrPort, req []byte) (resp []byte, err error)
}

// unsupportedTransport is the default no-op Transport that enforces
// CLAUDE-2: it returns ErrUnsupported instead of fabricating a discovery result.
type unsupportedTransport struct{}

func (unsupportedTransport) Exchange(_ context.Context, _ netip.AddrPort, _ []byte) ([]byte, error) {
	return nil, ErrUnsupported
}

// Client performs STUN Binding exchanges through an injectable [Transport].
// Construct with [NewClient].
type Client struct {
	transport Transport
}

// NewClient returns a Client backed by t. If t is nil, the client uses the
// package-level unsupportedTransport whose [Client.Discover] always returns
// [ErrUnsupported] — no fake addresses are generated.
func NewClient(t Transport) *Client {
	if t == nil {
		t = unsupportedTransport{}
	}
	return &Client{transport: t}
}

// Discover sends a STUN Binding Request to server and returns the
// server-reflexive address decoded from the XOR-MAPPED-ADDRESS attribute.
//
// When the client was constructed with a nil Transport (or the real default),
// Discover returns (zero AddrPort, ErrUnsupported) — never a fabricated
// address — satisfying CLAUDE-2.
func (c *Client) Discover(ctx context.Context, server netip.AddrPort) (netip.AddrPort, error) {
	encoded, txID, err := EncodeBindingRequest()
	if err != nil {
		return netip.AddrPort{}, err
	}

	resp, err := c.transport.Exchange(ctx, server, encoded)
	if err != nil {
		// Propagate ErrUnsupported (and other errors) directly.
		return netip.AddrPort{}, err
	}

	msg, err := DecodeMessage(resp)
	if err != nil {
		return netip.AddrPort{}, err
	}

	// Validate TxID echo: reject replayed or injected responses whose
	// transaction ID does not match our request (RFC 5389 §7.3.3).
	if msg.TxID != txID {
		return netip.AddrPort{}, fmt.Errorf("nattraversal: TxID mismatch: got %x, want %x", msg.TxID, txID)
	}

	ap, ok := msg.MappedAddress()
	if !ok {
		return netip.AddrPort{}, fmt.Errorf("nattraversal: no mapped address in server response")
	}
	return ap, nil
}
