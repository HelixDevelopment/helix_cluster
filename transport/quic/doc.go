// Package quic implements the Helix federation QUIC transport (HXC-1351):
// a UDP-based reliable fallback built on github.com/quic-go/quic-go.
//
// It provides a QUICServer and QUICClient over a real TLS 1.3 handshake with:
//   - Allow0RTT + EnableDatagrams
//   - ALPN NextProtos = ["helix-federation/6.0"]
//   - configurable MaxIdleTimeout and KeepAlivePeriod (default 15s)
//   - ConnectionMigrationStats tracking 0-RTT resumes and path/migration events
//
// Connection migration uses quic-go's real RFC 9000 path-migration API
// (Conn.AddPath -> Path.Probe -> Path.Switch), which rebinds the connection
// onto a new local UDP socket (new Transport) mid-stream without tearing down
// the QUIC connection.
//
// # Security posture (DoS / resource exhaustion)
//
// The transport hardens the same resource-exhaustion classes audited in the
// CoAP and edge/gateway servers:
//
//   - Stream caps: every connection's quic.Config pins MaxIncomingStreams and
//     MaxIncomingUniStreams (DefaultMaxIncomingStreams / *UniStreams). A peer
//     cannot open unbounded concurrent streams; the cap is explicit (not an
//     implicit quic-go default) and is provable by test.
//   - Frame/message size: ReadFramedMessage bounds a single length-prefixed
//     inbound message (DefaultMaxMessageBytes, 8 MiB — parity with the gateway's
//     quicFrameMaxLen) and rejects an over-cap declared length BEFORE allocating,
//     so a hostile length prefix cannot OOM the reader.
//   - Idle/handshake limits: MaxIdleTimeout reaps idle connections and an
//     explicit HandshakeIdleTimeout (DefaultHandshakeIdleTimeout) bounds the
//     resources a half-open/handshaking connection may hold.
//   - Accept-loop backpressure: the server enforces a concurrent-connection cap
//     (DefaultMaxConns); past it Accept refuses (CONNECTION_REFUSED) and returns
//     ErrMaxConnsReached instead of growing goroutines/memory without bound.
//
// # Trust posture (authentication)
//
// The handshake is a REAL TLS 1.3 handshake and the server always presents a
// certificate (NewQUICServer rejects a TLS config with none). Server identity
// is therefore authenticated to the extent the client verifies it (RootCAs /
// ServerName). This transport does NOT itself authenticate the CLIENT: there is
// no mutual-TLS requirement (tls.Config.ClientAuth is not forced) and no
// application-layer peer authorization. Callers that need client authentication
// must supply a server TLS config requiring client certificates, or perform
// application-layer auth on the first frame. Consequently the DoS limits above
// are deliberately enforced against UNAUTHENTICATED peers — an attacker who can
// reach the UDP socket and complete a TLS handshake is assumed possible, and the
// caps bound the damage such a peer can do.
//
// This is an isolated, self-contained Go module
// (github.com/HelixDevelopment/helix_cluster/transport/quic). It does not depend
// on the parent helix_cluster modules and is built/tested standalone:
//
//	cd transport/quic && GOWORK=off go test ./...
package quic
