// Package turn implements the Helix TURN relay allocation and fallback chain
// (HXC-1350): the NAT-traversal fallback that complements the ICE/WebRTC
// transport (HXC-1202) for the case where a direct or STUN-reflexive path
// between two peers cannot be established and traffic must be relayed through
// a TURN server (RFC 5766 / RFC 8656).
//
// It is built on github.com/pion/turn/v4 (pure-Go TURN, no cgo) and provides:
//
//   - RelayManager: given an ordered list of TURN server endpoints plus
//     long-term credentials, it allocates a relayed transport address from the
//     first reachable server and transparently falls back to the next server on
//     failure. The first server that grants an allocation wins; the chain is
//     tried in order so operators can express preference (e.g. nearest region
//     first, geo-redundant fallback last).
//   - Allocation: the result of a successful allocation — a relayed
//     net.PacketConn whose LocalAddr() is the server-granted relayed transport
//     address, the underlying *turn.Client (so callers can install peer
//     permissions and create channel bindings), and the endpoint that served it.
//
// Real-relay guarantee (CLAUDE-1, no bluff): the allocation path drives a real
// pion/turn client against a real TURN server over real UDP. Traffic sent to a
// peer's relayed address is genuinely forwarded by the server's relay; the
// integration tests prove byte-exact delivery THROUGH the relay between two
// independent clients, prove fail-over across a genuinely dead endpoint, and
// prove that wrong credentials are rejected (401) by the real server.
//
// Cross-internet TURN against a managed cloud service (coturn-as-a-service,
// Twilio NTS, etc.) is deferred honestly: the relay-allocation, fallback-chain,
// and auth mechanics are fully proven here against a real local pion/turn
// server over loopback UDP.
//
// This is an isolated, self-contained Go module
// (github.com/HelixDevelopment/helix_cluster/transport/turn). It does not depend
// on the parent helix_cluster modules and is built/tested standalone:
//
//	cd transport/turn && GOWORK=off go test ./...
package turn
