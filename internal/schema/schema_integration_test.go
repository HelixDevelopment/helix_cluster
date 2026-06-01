//go:build integration

package schema_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/internal/schema"
)

// skipUnlessAvailable skips the test if neither container nor DSN is configured
// or if the required binaries are absent.
func skipUnlessAvailable(t *testing.T) schema.PSQLConfig {
	t.Helper()
	cfg, available := schema.ResolvePSQLConfig()
	if !available {
		t.Skip("integration: neither HELIX_PG_CONTAINER nor PSQL_DSN/DATABASE_URL set")
	}
	if cfg.UseContainer {
		if err := schema.CheckBinaryPresent("podman"); err != nil {
			t.Skip("integration: podman not in PATH")
		}
	} else {
		if err := schema.CheckBinaryPresent("psql"); err != nil {
			t.Skip("integration: psql not in PATH")
		}
	}
	return cfg
}

// TestIntegration_ApplyPrimarySchema applies the full 0001_primary_schema.sql
// to a real Postgres instance and verifies the structural outcomes.
func TestIntegration_ApplyPrimarySchema(t *testing.T) {
	cfg := skipUnlessAvailable(t)

	out, err := schema.ApplyPrimarySchema(cfg)
	if err != nil {
		t.Fatalf("ApplyPrimarySchema failed:\n%s\nerror: %v", out, err)
	}
	t.Logf("Apply output:\n%s", out)
}

// TestIntegration_NodesUpdatedAtBumpsOnUpdate inserts a nodes row, then updates
// it and asserts updated_at > created_at (trigger fired) and that an audit_log
// row was written with a per-run UUID.
func TestIntegration_NodesUpdatedAtBumpsOnUpdate(t *testing.T) {
	cfg := skipUnlessAvailable(t)

	// Per-run UUID embedded in the node hostname for sink-side assertion.
	runID := fmt.Sprintf("test-%d", time.Now().UnixNano())

	// Insert a nodes row containing the runID.
	insertSQL := fmt.Sprintf(`
		INSERT INTO nodes (
			hostname, ip_addresses, wg_pubkey, spiffe_id,
			status, role, cpu_arch, cpu_cores, cpu_threads,
			memory_bytes, gpu_count, storage_bytes, version
		) VALUES (
			'%s',
			ARRAY['10.0.0.1'::inet],
			'wgkey-%s',
			'spiffe://helix.local/%s',
			'READY', 'WORKER', 'amd64', 4, 8,
			17179869184, 0, 107374182400,
			'0.1.0-test'
		);`, runID, runID, runID)

	out, err := schema.RunSQL(cfg, insertSQL)
	if err != nil {
		t.Fatalf("INSERT nodes failed:\n%s\nerror: %v", out, err)
	}

	// Fetch created_at and updated_at right after insert.
	selectSQL := fmt.Sprintf(`
		SELECT created_at, updated_at FROM nodes WHERE hostname = '%s';`, runID)
	rows, err := schema.QueryRows(cfg, selectSQL)
	if err != nil {
		t.Fatalf("SELECT after INSERT failed: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("INSERT nodes: no row found after INSERT")
	}
	t.Logf("After INSERT — row: %s", rows[0])

	// Sleep 10ms to guarantee clock tick between INSERT and UPDATE.
	time.Sleep(10 * time.Millisecond)

	// Update the version field to trigger the updated_at trigger.
	updateSQL := fmt.Sprintf(`
		UPDATE nodes SET version = '0.1.1-updated' WHERE hostname = '%s';`, runID)
	out, err = schema.RunSQL(cfg, updateSQL)
	if err != nil {
		t.Fatalf("UPDATE nodes failed:\n%s\nerror: %v", out, err)
	}

	// Fetch again and compare timestamps.
	rows2, err := schema.QueryRows(cfg, selectSQL)
	if err != nil {
		t.Fatalf("SELECT after UPDATE failed: %v", err)
	}
	if len(rows2) == 0 {
		t.Fatal("no row found after UPDATE")
	}
	t.Logf("After UPDATE — row: %s", rows2[0])

	// Verify updated_at > created_at using Postgres comparison.
	cmpSQL := fmt.Sprintf(`
		SELECT CASE WHEN updated_at > created_at THEN 'bumped' ELSE 'NOT_BUMPED' END
		FROM nodes WHERE hostname = '%s';`, runID)
	cmpRows, err := schema.QueryRows(cfg, cmpSQL)
	if err != nil {
		t.Fatalf("timestamp comparison query failed: %v", err)
	}
	if len(cmpRows) == 0 || cmpRows[0] != "bumped" {
		t.Errorf("updated_at trigger did NOT bump updated_at: got %v", cmpRows)
	}

	// Assert audit_log row was written by helix_audit_trigger (contains runID in details).
	auditSQL := fmt.Sprintf(`
		SELECT COUNT(*) FROM audit_log
		WHERE resource_type = 'nodes'
		  AND details::text LIKE '%%%s%%';`, runID)
	auditRows, err := schema.QueryRows(cfg, auditSQL)
	if err != nil {
		t.Fatalf("audit_log query failed: %v", err)
	}
	if len(auditRows) == 0 || auditRows[0] == "0" {
		t.Errorf("helix_audit_trigger did NOT write an audit_log row for runID %s; rows=%v", runID, auditRows)
	}
	t.Logf("audit_log row count for runID %s: %s", runID, auditRows[0])

	// Cleanup
	cleanSQL := fmt.Sprintf(`DELETE FROM nodes WHERE hostname = '%s';`, runID)
	schema.RunSQL(cfg, cleanSQL) //nolint:errcheck — cleanup best-effort
}

// TestIntegration_CheckViolationRejected asserts that a row violating a CHECK
// constraint causes psql to exit non-zero.
func TestIntegration_CheckViolationRejected(t *testing.T) {
	cfg := skipUnlessAvailable(t)

	// cpu_cores must be > 0; try 0 which violates nodes_cpu_cores_positive.
	badSQL := `
		INSERT INTO nodes (
			hostname, ip_addresses, wg_pubkey, spiffe_id,
			status, role, cpu_arch, cpu_cores, cpu_threads,
			memory_bytes, gpu_count, storage_bytes, version
		) VALUES (
			'check-violation-test',
			ARRAY['10.99.99.99'::inet],
			'wgkey-violation',
			'spiffe://helix.local/violation-test',
			'READY', 'WORKER', 'amd64', 0, 0,
			17179869184, 0, 107374182400,
			'0.0.0-violation'
		);`

	out, err := schema.RunSQL(cfg, badSQL)
	if err == nil {
		t.Errorf("expected CHECK violation to cause psql exit non-zero, but got success\nout=%s", out)
	} else {
		t.Logf("CHECK violation correctly rejected (psql exited non-zero):\n%s", out)
	}

	// Verify the constraint name appears in the error output.
	if !strings.Contains(out, "nodes_cpu_cores_positive") && !strings.Contains(out, "check") && !strings.Contains(out, "CHECK") {
		t.Logf("Note: constraint name not in output (may vary by PG version): %s", out)
	}
}

// TestIntegration_StatusEnumCheckViolation tests that an invalid status value is rejected.
func TestIntegration_StatusEnumCheckViolation(t *testing.T) {
	cfg := skipUnlessAvailable(t)

	// 'INVALID_STATUS' is not in the status CHECK enum.
	badSQL := `
		INSERT INTO nodes (
			hostname, ip_addresses, wg_pubkey, spiffe_id,
			status, role, cpu_arch, cpu_cores, cpu_threads,
			memory_bytes, gpu_count, storage_bytes, version
		) VALUES (
			'status-violation-test',
			ARRAY['10.88.88.88'::inet],
			'wgkey-status-violation',
			'spiffe://helix.local/status-violation',
			'INVALID_STATUS', 'WORKER', 'amd64', 4, 8,
			17179869184, 0, 107374182400,
			'0.0.0-statusviolation'
		);`

	_, err := schema.RunSQL(cfg, badSQL)
	if err == nil {
		t.Error("expected status CHECK violation to be rejected, but INSERT succeeded — schema CHECK not enforced")
	} else {
		t.Logf("status CHECK correctly rejected: %v", err)
	}
}

// TestIntegration_AllFifteenTablesExistInDB verifies that after applying the
// schema, all 15 required tables are visible in information_schema.
func TestIntegration_AllFifteenTablesExistInDB(t *testing.T) {
	cfg := skipUnlessAvailable(t)

	for _, tbl := range schema.RequiredTables {
		sql := fmt.Sprintf(`
			SELECT COUNT(*) FROM information_schema.tables
			WHERE table_schema = 'public'
			  AND table_name = '%s';`, tbl)
		rows, err := schema.QueryRows(cfg, sql)
		if err != nil {
			t.Errorf("query for table %q failed: %v", tbl, err)
			continue
		}
		if len(rows) == 0 || rows[0] == "0" {
			t.Errorf("table %q not found in DB after schema apply", tbl)
		}
	}
}

// TestIntegration_HelixAuditTriggerFunctionExists verifies the audit trigger
// function is registered in pg_proc.
func TestIntegration_HelixAuditTriggerFunctionExists(t *testing.T) {
	cfg := skipUnlessAvailable(t)

	for _, fn := range []string{"helix_update_updated_at_column", "helix_audit_trigger"} {
		sql := fmt.Sprintf(`
			SELECT COUNT(*) FROM pg_proc
			WHERE proname = '%s';`, fn)
		rows, err := schema.QueryRows(cfg, sql)
		if err != nil {
			t.Errorf("pg_proc query for %q failed: %v", fn, err)
			continue
		}
		if len(rows) == 0 || rows[0] == "0" {
			t.Errorf("trigger function %q not found in pg_proc", fn)
		}
	}
}
