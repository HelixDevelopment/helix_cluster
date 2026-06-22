package quic

import (
	"crypto/tls"
	"testing"
	"time"

	"github.com/google/uuid"
)

// runID is emitted once per test run for evidence correlation. Shared by both the
// default-lane (security, zerortt_migration) and the integration-tagged tests, so
// it lives in an untagged helper file (moved out of integration_test.go when that
// file gained the //go:build integration tag).
var runID = uuid.NewString()

// newTestServerConfig builds a server Config with a real self-signed cert and
// the Helix defaults (0-RTT + datagrams on). Shared by both the default-lane
// (security, zerortt_migration) and the integration-tagged tests, so it lives in
// an untagged helper file (moved out of integration_test.go when that file gained
// the //go:build integration tag).
func newTestServerConfig(t *testing.T) (Config, *tls.Config) {
	t.Helper()
	cert, pool, err := GenerateSelfSignedCert()
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	serverTLS := &tls.Config{Certificates: []tls.Certificate{cert}}
	clientTLS := &tls.Config{RootCAs: pool, ServerName: "localhost"}
	srvCfg := Config{
		Allow0RTT:       true,
		EnableDatagrams: true,
		KeepAlivePeriod: 15 * time.Second,
		MaxIdleTimeout:  30 * time.Second,
		TLSConfig:       serverTLS,
	}
	return srvCfg, clientTLS
}
