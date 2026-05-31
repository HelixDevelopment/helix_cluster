package wireguard

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"time"
)

// STUN (RFC 5389) constants.
const (
	// stunMagicCookie is the fixed 32-bit magic cookie that appears at bytes
	// 4..8 of every STUN message (RFC 5389 §6).
	stunMagicCookie uint32 = 0x2112A442

	// stunBindingRequest is the message type for a Binding Request
	// (class=request 0b00, method=Binding 0x001) => 0x0001.
	stunBindingRequest uint16 = 0x0001
	// stunBindingSuccess is the message type for a Binding Success Response
	// (class=success 0b10) => 0x0101.
	stunBindingSuccess uint16 = 0x0101

	// Attribute types.
	stunAttrMappedAddress    uint16 = 0x0001
	stunAttrXorMappedAddress uint16 = 0x0020

	// Address families inside (XOR-)MAPPED-ADDRESS attributes.
	stunFamilyIPv4 uint8 = 0x01
	stunFamilyIPv6 uint8 = 0x02

	// stunHeaderLen is the fixed STUN header length in bytes (RFC 5389 §6).
	stunHeaderLen = 20
	// stunTxIDLen is the length of the 96-bit transaction ID.
	stunTxIDLen = 12
)

// STUNClient is a minimal RFC 5389 STUN Binding client used to discover the
// public (server-reflexive) UDP endpoint of a local socket. It implements just
// enough of the protocol to encode a Binding Request and decode the
// XOR-MAPPED-ADDRESS attribute of the reply.
//
// The client performs real UDP I/O; tests exercise it against a local fake
// STUN server bound to 127.0.0.1, so no external STUN service is contacted.
type STUNClient struct {
	// Timeout bounds the read of the single Binding Response. Zero means a
	// 5-second default is applied.
	Timeout time.Duration
}

// stunMessage is a decoded STUN message header plus raw attribute bytes.
type stunMessage struct {
	Type  uint16
	TxID  [stunTxIDLen]byte
	Attrs []byte
}

// newTxID returns a fresh random 96-bit transaction ID.
func newTxID() ([stunTxIDLen]byte, error) {
	var id [stunTxIDLen]byte
	if _, err := rand.Read(id[:]); err != nil {
		return id, fmt.Errorf("failed to read random transaction id: %w", err)
	}
	return id, nil
}

// encodeBindingRequest builds a 20-byte STUN Binding Request (no attributes)
// with the supplied transaction ID and the fixed magic cookie.
func encodeBindingRequest(txID [stunTxIDLen]byte) []byte {
	msg := make([]byte, stunHeaderLen)
	binary.BigEndian.PutUint16(msg[0:2], stunBindingRequest)
	binary.BigEndian.PutUint16(msg[2:4], 0) // message length: zero attributes
	binary.BigEndian.PutUint32(msg[4:8], stunMagicCookie)
	copy(msg[8:20], txID[:])
	return msg
}

// parseSTUNMessage validates the fixed header (length, magic cookie, declared
// attribute length) and returns the decoded header plus the attribute bytes.
func parseSTUNMessage(buf []byte) (*stunMessage, error) {
	if len(buf) < stunHeaderLen {
		return nil, fmt.Errorf("stun message too short: got %d bytes, want >= %d", len(buf), stunHeaderLen)
	}
	cookie := binary.BigEndian.Uint32(buf[4:8])
	if cookie != stunMagicCookie {
		return nil, fmt.Errorf("invalid stun magic cookie: got 0x%08x, want 0x%08x", cookie, stunMagicCookie)
	}
	attrLen := int(binary.BigEndian.Uint16(buf[2:4]))
	if stunHeaderLen+attrLen > len(buf) {
		return nil, fmt.Errorf("stun attribute length %d exceeds buffer (%d bytes after header)", attrLen, len(buf)-stunHeaderLen)
	}
	m := &stunMessage{
		Type:  binary.BigEndian.Uint16(buf[0:2]),
		Attrs: buf[stunHeaderLen : stunHeaderLen+attrLen],
	}
	copy(m.TxID[:], buf[8:20])
	return m, nil
}

// xorMappedAddress XOR-decodes a (XOR-)MAPPED-ADDRESS attribute value into a
// net.UDPAddr. attrType selects whether the value is XOR-encoded
// (stunAttrXorMappedAddress) or plain (stunAttrMappedAddress). For the XOR
// variant the port is XORed with the high 16 bits of the magic cookie and the
// address is XORed with the cookie (IPv4) or cookie||txID (IPv6), per RFC 5389
// §15.2.
func xorMappedAddress(attrType uint16, value []byte, txID [stunTxIDLen]byte) (*net.UDPAddr, error) {
	if len(value) < 4 {
		return nil, fmt.Errorf("mapped-address attribute too short: %d bytes", len(value))
	}
	family := value[1]
	port := binary.BigEndian.Uint16(value[2:4])
	addrBytes := value[4:]

	cookieBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(cookieBytes, stunMagicCookie)

	if attrType == stunAttrXorMappedAddress {
		// X-Port = Port XOR (most-significant 16 bits of the magic cookie).
		port ^= uint16(stunMagicCookie >> 16)
	}

	var ip net.IP
	switch family {
	case stunFamilyIPv4:
		if len(addrBytes) < 4 {
			return nil, fmt.Errorf("ipv4 mapped-address too short: %d bytes", len(addrBytes))
		}
		ipb := make([]byte, 4)
		copy(ipb, addrBytes[:4])
		if attrType == stunAttrXorMappedAddress {
			for i := 0; i < 4; i++ {
				ipb[i] ^= cookieBytes[i]
			}
		}
		ip = net.IP(ipb)
	case stunFamilyIPv6:
		if len(addrBytes) < 16 {
			return nil, fmt.Errorf("ipv6 mapped-address too short: %d bytes", len(addrBytes))
		}
		ipb := make([]byte, 16)
		copy(ipb, addrBytes[:16])
		if attrType == stunAttrXorMappedAddress {
			var key [16]byte
			copy(key[0:4], cookieBytes)
			copy(key[4:16], txID[:])
			for i := 0; i < 16; i++ {
				ipb[i] ^= key[i]
			}
		}
		ip = net.IP(ipb)
	default:
		return nil, fmt.Errorf("unknown address family: 0x%02x", family)
	}

	return &net.UDPAddr{IP: ip, Port: int(port)}, nil
}

// parseMappedAddress walks the attribute TLVs and returns the first
// XOR-MAPPED-ADDRESS (preferred) or MAPPED-ADDRESS it finds, decoded into a
// UDP address. Attributes are 4-byte aligned with 4-byte TLV headers (RFC 5389
// §15).
func parseMappedAddress(m *stunMessage) (*net.UDPAddr, error) {
	attrs := m.Attrs
	var fallback *net.UDPAddr
	for len(attrs) >= 4 {
		atype := binary.BigEndian.Uint16(attrs[0:2])
		alen := int(binary.BigEndian.Uint16(attrs[2:4]))
		if 4+alen > len(attrs) {
			return nil, fmt.Errorf("attribute 0x%04x length %d overruns buffer", atype, alen)
		}
		value := attrs[4 : 4+alen]

		switch atype {
		case stunAttrXorMappedAddress:
			return xorMappedAddress(atype, value, m.TxID)
		case stunAttrMappedAddress:
			if fallback == nil {
				addr, err := xorMappedAddress(atype, value, m.TxID)
				if err != nil {
					return nil, err
				}
				fallback = addr
			}
		}

		// Advance past this TLV, padding the value to a 4-byte boundary.
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
	if fallback != nil {
		return fallback, nil
	}
	return nil, fmt.Errorf("no (XOR-)MAPPED-ADDRESS attribute present in response")
}

// DiscoverPublicEndpoint sends a STUN Binding Request to serverAddr (host:port)
// over UDP and returns the public (server-reflexive) endpoint decoded from the
// reply's XOR-MAPPED-ADDRESS attribute, formatted as "ip:port".
//
// It validates the magic cookie and that the response transaction ID matches
// the request, rejecting spoofed or malformed replies.
func (c *STUNClient) DiscoverPublicEndpoint(serverAddr string) (string, error) {
	addr, err := c.DiscoverPublicUDPAddr(serverAddr)
	if err != nil {
		return "", err
	}
	return addr.String(), nil
}

// DiscoverPublicUDPAddr is like DiscoverPublicEndpoint but returns the decoded
// *net.UDPAddr.
func (c *STUNClient) DiscoverPublicUDPAddr(serverAddr string) (*net.UDPAddr, error) {
	raddr, err := net.ResolveUDPAddr("udp", serverAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid stun server address %q: %w", serverAddr, err)
	}

	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		return nil, fmt.Errorf("failed to dial stun server: %w", err)
	}
	defer conn.Close()

	return c.exchange(conn)
}

// exchange performs one Binding Request/Response round trip on an already-
// connected UDP socket. It is separated out so tests can drive it with a
// loopback connection.
func (c *STUNClient) exchange(conn net.Conn) (*net.UDPAddr, error) {
	txID, err := newTxID()
	if err != nil {
		return nil, err
	}
	req := encodeBindingRequest(txID)

	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	_ = conn.SetWriteDeadline(deadline)
	if _, err := conn.Write(req); err != nil {
		return nil, fmt.Errorf("failed to send binding request: %w", err)
	}

	_ = conn.SetReadDeadline(deadline)
	buf := make([]byte, 1500)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("failed to read binding response: %w", err)
	}

	msg, err := parseSTUNMessage(buf[:n])
	if err != nil {
		return nil, err
	}
	if msg.Type != stunBindingSuccess {
		return nil, fmt.Errorf("unexpected stun message type: got 0x%04x, want 0x%04x", msg.Type, stunBindingSuccess)
	}
	if msg.TxID != txID {
		return nil, fmt.Errorf("stun transaction id mismatch: response does not match request")
	}

	return parseMappedAddress(msg)
}
