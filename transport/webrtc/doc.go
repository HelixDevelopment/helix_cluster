// Package webrtc implements the Helix edge WebRTC peer-to-peer data-channel
// transport (HXC-1202): a reliable, ordered DataChannel over a real
// RTCPeerConnection built on github.com/pion/webrtc/v4 (pure-Go, no cgo).
//
// It provides:
//
//   - Peer: a thin wrapper around a pion *webrtc.PeerConnection that creates
//     (or accepts) a single reliable+ordered DataChannel and exposes
//     Send/Receive of WorkEnvelope messages over it.
//   - Signaler: an in-process signaling helper that exchanges the SDP
//     offer/answer and ICE candidates directly between two Peer objects over
//     loopback — no external signaling server is required. This exercises the
//     real ICE/DTLS/SCTP handshake on the host.
//   - WorkEnvelope codec: a compact, deterministic length-prefixed binary
//     encoding for the unit of work dispatched between edge peers.
//
// On-device NAT traversal over the public internet (STUN/TURN against real
// servers) is deferred honestly; the WebRTC data-channel transport and the
// loopback round-trip handshake are fully proven here against the real pion
// ICE/DTLS/SCTP stack.
//
// This is an isolated, self-contained Go module
// (github.com/HelixDevelopment/helix_cluster/transport/webrtc). It does not
// depend on the parent helix_cluster modules and is built/tested standalone:
//
//	cd transport/webrtc && GOWORK=off go test ./...
package webrtc
