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
// This is an isolated, self-contained Go module
// (github.com/HelixDevelopment/helix_cluster/transport/quic). It does not depend
// on the parent helix_cluster modules and is built/tested standalone:
//
//	cd transport/quic && GOWORK=off go test ./...
package quic
