package gateway

import (
	"crypto/tls"
	"strings"
	"testing"
	"time"

	quic "github.com/HelixDevelopment/helix_cluster/transport/quic"
)

// wsURL converts an httptest http:// base URL to a ws:// URL pointing at path.
// Shared by both the default-lane (security, crosstransport) and the
// integration-tagged tests, so it lives in an untagged helper file (moved out of
// integration_test.go when that file gained the //go:build integration tag).
func wsURL(base, path string) string {
	return "ws" + strings.TrimPrefix(base, "http") + path
}

// waitForRoutes polls the routing table until the expected per-node connection
// counts are present, so the test does not race the server-side registration.
// Shared by both the default-lane (security, crosstransport) and the
// integration-tagged tests, so it lives in an untagged helper file (moved out of
// integration_test.go when that file gained the //go:build integration tag).
func waitForRoutes(t *testing.T, gw *Gateway, want map[string]int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ok := true
		for node, n := range want {
			if len(gw.table.lookup(node)) < n {
				ok = false
				break
			}
		}
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("routes not established within deadline; want %v", want)
}

// quicTestConfigs builds matched server/client QUIC configs backed by a real
// self-signed TLS 1.3 cert over loopback (no InsecureSkipVerify). Shared by both
// the default-lane (crosstransport) and the integration-tagged tests, so it lives
// in an untagged helper file (moved out of quic_integration_test.go when that file
// gained the //go:build integration tag).
func quicTestConfigs(t *testing.T) (server quic.Config, client quic.Config) {
	t.Helper()
	cert, pool, err := quic.GenerateSelfSignedCert()
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	server = quic.Config{
		Allow0RTT:       true,
		KeepAlivePeriod: 15 * time.Second,
		MaxIdleTimeout:  30 * time.Second,
		TLSConfig:       &tls.Config{Certificates: []tls.Certificate{cert}},
	}
	client = quic.Config{
		Allow0RTT:       true,
		KeepAlivePeriod: 15 * time.Second,
		TLSConfig:       &tls.Config{RootCAs: pool, ServerName: "localhost"},
	}
	return server, client
}
