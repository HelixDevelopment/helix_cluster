package quic

import (
	"fmt"
	"sync"
)

// ConnectionMigrationStats tracks observable events on the QUIC transport that
// prove real-world operation: 0-RTT resumptions and path/migration events.
//
// All methods are safe for concurrent use.
type ConnectionMigrationStats struct {
	mu sync.Mutex

	// Resumes0RTT counts handshakes where 0-RTT early data was actually used
	// (ConnectionState().Used0RTT == true).
	resumes0RTT uint64

	// SessionTicketsStored counts TLS session tickets cached for later 0-RTT.
	sessionTicketsStored uint64

	// MigrationsProbed counts path-validation (PATH_CHALLENGE) probes started.
	migrationsProbed uint64

	// MigrationsSwitched counts successful Path.Switch() calls — the connection
	// is now sending on a new local UDP socket/address.
	migrationsSwitched uint64
}

// Record0RTTResume increments the 0-RTT resume counter.
func (s *ConnectionMigrationStats) Record0RTTResume() {
	s.mu.Lock()
	s.resumes0RTT++
	s.mu.Unlock()
}

// RecordSessionTicket increments the stored-session-ticket counter.
func (s *ConnectionMigrationStats) RecordSessionTicket() {
	s.mu.Lock()
	s.sessionTicketsStored++
	s.mu.Unlock()
}

// RecordMigrationProbe increments the path-probe counter.
func (s *ConnectionMigrationStats) RecordMigrationProbe() {
	s.mu.Lock()
	s.migrationsProbed++
	s.mu.Unlock()
}

// RecordMigrationSwitch increments the successful-migration counter.
func (s *ConnectionMigrationStats) RecordMigrationSwitch() {
	s.mu.Lock()
	s.migrationsSwitched++
	s.mu.Unlock()
}

// Snapshot returns a point-in-time copy of the counters.
func (s *ConnectionMigrationStats) Snapshot() StatsSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return StatsSnapshot{
		Resumes0RTT:          s.resumes0RTT,
		SessionTicketsStored: s.sessionTicketsStored,
		MigrationsProbed:     s.migrationsProbed,
		MigrationsSwitched:   s.migrationsSwitched,
	}
}

// StatsSnapshot is an immutable view of ConnectionMigrationStats.
type StatsSnapshot struct {
	Resumes0RTT          uint64
	SessionTicketsStored uint64
	MigrationsProbed     uint64
	MigrationsSwitched   uint64
}

// String renders the snapshot for log/evidence dumps.
func (s StatsSnapshot) String() string {
	return fmt.Sprintf(
		"ConnectionMigrationStats{0rtt_resumes=%d session_tickets=%d migrations_probed=%d migrations_switched=%d}",
		s.Resumes0RTT, s.SessionTicketsStored, s.MigrationsProbed, s.MigrationsSwitched,
	)
}
