// Package coap implements the Helix edge CoAP transport (HXC-1201): a real
// RFC 7252 Constrained Application Protocol transport over UDP, used as the
// low-bandwidth alternative to MQTT/HTTP for constrained edge devices and
// lossy radio links.
//
// It is built on the maintained github.com/plgd-dev/go-coap/v3 library and
// provides:
//
//   - Server: a CoAP server exposing edge resources over real UDP —
//     POST /dispatch  (accept a work envelope) and
//     GET/Observe /status (read or subscribe to device status).
//   - Client: a CoAP client that POSTs a work envelope to /dispatch and
//     GETs or Observes /status.
//
// Payloads are encoded as CBOR (RFC 8949, CoAP content-format
// application/cbor = 60), the compact binary format CoAP favors for
// constrained links. See envelope.go for the wire codec.
//
// Reliability over lossy links uses Confirmable (CON) messages: go-coap sends
// CON by default for unicast requests and retransmits with exponential backoff
// per RFC 7252 §4.2 until an ACK is received, so a dropped UDP datagram does
// not lose the request.
//
// Low-bandwidth benefit: a CoAP request header is 4 bytes plus compact options,
// versus an HTTP/1.1 request's request-line + headers (hundreds of bytes) for
// the same payload. wiresize.go measures this concretely.
//
// This is an isolated, self-contained Go module
// (github.com/HelixDevelopment/helix_cluster/transport/coap). It does not
// depend on the parent helix_cluster modules and is built/tested standalone:
//
//	cd transport/coap && GOWORK=off go test ./...
//
// On-device constrained-radio operation (6LoWPAN/802.15.4/LoRa) is deferred
// honestly; the CoAP protocol round-trip over real UDP and the wire-size
// benefit are fully proven here.
package coap
