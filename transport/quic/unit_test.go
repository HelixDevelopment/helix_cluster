package quic

import (
	"crypto/tls"
	"crypto/x509"
	"testing"
	"time"
)

// TestGenerateSelfSignedCert proves the cert helper produces a real, parseable
// ECDSA leaf usable for a TLS 1.3 handshake, with the expected SANs.
func TestGenerateSelfSignedCert(t *testing.T) {
	cert, pool, err := GenerateSelfSignedCert("helix.local")
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert: %v", err)
	}
	if cert.Leaf == nil {
		t.Fatal("expected populated cert.Leaf")
	}
	if cert.Leaf.PublicKeyAlgorithm != x509.ECDSA {
		t.Fatalf("expected ECDSA public key, got %v", cert.Leaf.PublicKeyAlgorithm)
	}
	if pool == nil {
		t.Fatal("expected non-nil cert pool")
	}
	// SAN coverage.
	wantDNS := map[string]bool{"localhost": false, "helix.local": false}
	for _, d := range cert.Leaf.DNSNames {
		if _, ok := wantDNS[d]; ok {
			wantDNS[d] = true
		}
	}
	for name, seen := range wantDNS {
		if !seen {
			t.Errorf("missing DNS SAN %q", name)
		}
	}
	if time.Now().After(cert.Leaf.NotAfter) {
		t.Error("certificate already expired")
	}
}

// TestConfigDefaults verifies zero-value fallbacks and the ALPN guarantee.
func TestConfigDefaults(t *testing.T) {
	c := &Config{}
	if got := c.nextProtos(); len(got) != 1 || got[0] != ALPNHelixFederation {
		t.Fatalf("default ALPN = %v, want [%s]", got, ALPNHelixFederation)
	}
	if got := c.keepAlive(); got != DefaultKeepAlivePeriod {
		t.Fatalf("default keepAlive = %v, want %v", got, DefaultKeepAlivePeriod)
	}
	if DefaultKeepAlivePeriod != 15*time.Second {
		t.Fatalf("KeepAlivePeriod constant = %v, want 15s", DefaultKeepAlivePeriod)
	}
	if got := c.maxIdle(); got != DefaultMaxIdleTimeout {
		t.Fatalf("default maxIdle = %v, want %v", got, DefaultMaxIdleTimeout)
	}

	// Overrides honored.
	c2 := &Config{NextProtos: []string{"x"}, KeepAlivePeriod: time.Second, MaxIdleTimeout: 2 * time.Second}
	if got := c2.nextProtos(); got[0] != "x" {
		t.Errorf("override ALPN not honored: %v", got)
	}
	if got := c2.keepAlive(); got != time.Second {
		t.Errorf("override keepAlive not honored: %v", got)
	}
}

// TestQUICConfigConstruction verifies the quic.Config carries Allow0RTT,
// EnableDatagrams and the configured timeouts.
func TestQUICConfigConstruction(t *testing.T) {
	c := &Config{Allow0RTT: true, EnableDatagrams: true, KeepAlivePeriod: 15 * time.Second, MaxIdleTimeout: 7 * time.Second}
	qc := c.quicConfig()
	if !qc.Allow0RTT {
		t.Error("Allow0RTT not propagated")
	}
	if !qc.EnableDatagrams {
		t.Error("EnableDatagrams not propagated")
	}
	if qc.KeepAlivePeriod != 15*time.Second {
		t.Errorf("KeepAlivePeriod = %v, want 15s", qc.KeepAlivePeriod)
	}
	if qc.MaxIdleTimeout != 7*time.Second {
		t.Errorf("MaxIdleTimeout = %v, want 7s", qc.MaxIdleTimeout)
	}
}

// TestClientTLSConfigForces verifies the client TLS config pins TLS 1.3, the
// Helix ALPN, and always installs a ClientSessionCache (the 0-RTT prereq).
func TestClientTLSConfigForces(t *testing.T) {
	c := &Config{}
	tc := c.clientTLSConfig()
	if tc.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %x, want TLS1.3", tc.MinVersion)
	}
	if len(tc.NextProtos) != 1 || tc.NextProtos[0] != ALPNHelixFederation {
		t.Errorf("NextProtos = %v", tc.NextProtos)
	}
	if tc.ClientSessionCache == nil {
		t.Error("client TLS config must have a ClientSessionCache for 0-RTT")
	}
}

// TestStatsAccounting verifies concurrent-safe counter accounting and the
// snapshot/stringer used in evidence dumps.
func TestStatsAccounting(t *testing.T) {
	s := &ConnectionMigrationStats{}
	s.Record0RTTResume()
	s.Record0RTTResume()
	s.RecordSessionTicket()
	s.RecordMigrationProbe()
	s.RecordMigrationProbe()
	s.RecordMigrationProbe()
	s.RecordMigrationSwitch()

	snap := s.Snapshot()
	if snap.Resumes0RTT != 2 {
		t.Errorf("Resumes0RTT = %d, want 2", snap.Resumes0RTT)
	}
	if snap.SessionTicketsStored != 1 {
		t.Errorf("SessionTicketsStored = %d, want 1", snap.SessionTicketsStored)
	}
	if snap.MigrationsProbed != 3 {
		t.Errorf("MigrationsProbed = %d, want 3", snap.MigrationsProbed)
	}
	if snap.MigrationsSwitched != 1 {
		t.Errorf("MigrationsSwitched = %d, want 1", snap.MigrationsSwitched)
	}
	if got := snap.String(); got == "" {
		t.Error("snapshot String() empty")
	}
}

// TestStatsConcurrent runs the counters under -race from many goroutines.
func TestStatsConcurrent(t *testing.T) {
	s := &ConnectionMigrationStats{}
	const n = 200
	done := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() {
			s.Record0RTTResume()
			s.RecordMigrationProbe()
			done <- struct{}{}
		}()
	}
	for i := 0; i < n; i++ {
		<-done
	}
	snap := s.Snapshot()
	if snap.Resumes0RTT != n || snap.MigrationsProbed != n {
		t.Fatalf("race accounting wrong: %+v", snap)
	}
}
