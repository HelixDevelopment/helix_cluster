package coap

import (
	"bytes"
	"fmt"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	"github.com/plgd-dev/go-coap/v3/udp/coder"
)

// WireSizes captures a measured byte comparison of the same dispatch request
// encoded as a real CoAP UDP datagram vs an equivalent HTTP/1.1 request, to
// demonstrate the low-bandwidth benefit of CoAP for constrained links.
type WireSizes struct {
	PayloadBytes int // shared application payload (CBOR for CoAP, same bytes for HTTP)
	CoAPBytes    int // full CoAP message on the wire (header + options + payload)
	CoAPOverhead int // CoAPBytes - PayloadBytes (protocol framing only)
	HTTPBytes    int // full HTTP/1.1 request bytes (request-line + headers + body)
	HTTPOverhead int // HTTPBytes - PayloadBytes
}

// String renders the comparison for evidence logs.
func (w WireSizes) String() string {
	return fmt.Sprintf(
		"payload=%dB | CoAP_total=%dB (overhead=%dB) | HTTP/1.1_total=%dB (overhead=%dB) | CoAP saves %dB (%.1f%% smaller)",
		w.PayloadBytes, w.CoAPBytes, w.CoAPOverhead, w.HTTPBytes, w.HTTPOverhead,
		w.HTTPBytes-w.CoAPBytes, 100*float64(w.HTTPBytes-w.CoAPBytes)/float64(w.HTTPBytes),
	)
}

// MeasureDispatchWireSizes encodes the given work envelope as a real CoAP POST
// /dispatch message (using go-coap's own marshaller) and as an equivalent
// minimal HTTP/1.1 POST, returning the measured on-the-wire byte sizes.
//
// The CoAP size is the actual serialized message bytes go-coap would place in
// the UDP datagram (CoAP version/type/token/code header + Uri-Path +
// Content-Format options + payload marker + payload). The HTTP size is a
// deliberately *lean* HTTP/1.1 request for the same logical operation — already
// minimal, yet still hundreds of bytes from its textual headers.
func MeasureDispatchWireSizes(env WorkEnvelope) (WireSizes, error) {
	payload, err := EncodeEnvelope(env)
	if err != nil {
		return WireSizes{}, err
	}

	coapBytes, err := encodeCoAPDispatchWire(payload)
	if err != nil {
		return WireSizes{}, err
	}

	httpBytes := encodeHTTPDispatchWire(payload)

	return WireSizes{
		PayloadBytes: len(payload),
		CoAPBytes:    len(coapBytes),
		CoAPOverhead: len(coapBytes) - len(payload),
		HTTPBytes:    len(httpBytes),
		HTTPOverhead: len(httpBytes) - len(payload),
	}, nil
}

// encodeCoAPDispatchWire builds a real CoAP CON POST /dispatch message and
// marshals it to its exact UDP-wire bytes using go-coap's own udp/coder — the
// same encoder the library uses to put messages on the socket. The returned
// bytes therefore include the 4-byte CoAP header, the 2-byte token, the
// Uri-Path and Content-Format options, the 0xFF payload marker, and the CBOR
// payload.
func encodeCoAPDispatchWire(payload []byte) ([]byte, error) {
	m := pool.NewMessage(nil)
	m.SetCode(codes.POST)
	m.SetType(message.Confirmable)
	m.SetMessageID(0x1234)
	// A representative 2-byte token (CoAP tokens are 0..8 bytes).
	m.SetToken([]byte{0xAB, 0xCD})
	if err := m.SetPath(PathDispatch); err != nil {
		return nil, fmt.Errorf("coap: set path: %w", err)
	}
	m.SetContentFormat(message.AppCBOR)
	m.SetBody(bytes.NewReader(payload))

	wire, err := m.MarshalWithEncoder(coder.DefaultCoder)
	if err != nil {
		return nil, fmt.Errorf("coap: marshal wire: %w", err)
	}
	// Copy out of the message's reusable buffer.
	return append([]byte(nil), wire...), nil
}

// encodeHTTPDispatchWire builds an equivalent, deliberately lean HTTP/1.1 POST
// request for the same dispatch operation and returns its exact wire bytes.
// Even minimised, the textual request-line and headers dwarf CoAP's 4-byte
// binary header — which is the whole point of CoAP on constrained links.
func encodeHTTPDispatchWire(payload []byte) []byte {
	var b bytes.Buffer
	// Request line.
	b.WriteString("POST /dispatch HTTP/1.1\r\n")
	// The minimal set of headers a real HTTP/1.1 server requires/expects.
	b.WriteString("Host: edge.helix.local\r\n")
	b.WriteString("Content-Type: application/cbor\r\n")
	fmt.Fprintf(&b, "Content-Length: %d\r\n", len(payload))
	b.WriteString("Connection: keep-alive\r\n")
	b.WriteString("\r\n")
	b.Write(payload)
	return b.Bytes()
}
