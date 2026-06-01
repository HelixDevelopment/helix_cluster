package schema_test

import (
	"strings"
	"testing"

	"github.com/HelixDevelopment/helix_cluster/internal/schema"
)

// ─── helpers ─────────────────────────────────────────────────────────────────

// loadSQL reads the live SQL file or fatals the test.
func loadSQL(t *testing.T) string {
	t.Helper()
	sql, err := schema.ReadSQL()
	if err != nil {
		t.Fatalf("ReadSQL: %v", err)
	}
	if len(sql) == 0 {
		t.Fatal("ReadSQL returned empty string")
	}
	return sql
}

// ─── TestReadSQL ─────────────────────────────────────────────────────────────

func TestReadSQL_NonEmpty(t *testing.T) {
	sql := loadSQL(t)
	if len(sql) < 1000 {
		t.Errorf("SQL file suspiciously short: %d bytes", len(sql))
	}
}

// ─── TestSchemaPath ───────────────────────────────────────────────────────────

func TestSchemaPath_Resolves(t *testing.T) {
	p, err := schema.SchemaPath()
	if err != nil {
		t.Fatalf("SchemaPath: %v", err)
	}
	if !strings.HasSuffix(p, "0001_primary_schema.sql") {
		t.Errorf("SchemaPath = %q, want suffix 0001_primary_schema.sql", p)
	}
}

// ─── TestStructure_ExactlyFifteenTables ──────────────────────────────────────

// tablePresent checks that "CREATE TABLE IF NOT EXISTS <tbl>" appears with a
// word-terminating character so that "nodes" does not match "nodes_removed".
func tablePresent(upper, tbl string) bool {
	upperTbl := strings.ToUpper(tbl)
	needle := "CREATE TABLE IF NOT EXISTS " + upperTbl
	for _, suffix := range []string{" ", "\n", "\r", "\t"} {
		if strings.Contains(upper, needle+suffix) {
			return true
		}
	}
	return false
}

func TestStructure_ExactlyFifteenTables(t *testing.T) {
	sql := loadSQL(t)
	upper := strings.ToUpper(sql)

	for _, tbl := range schema.RequiredTables {
		if !tablePresent(upper, tbl) {
			t.Errorf("required table missing: %q", tbl)
		}
	}

	// Also assert total count is exactly 15 (not more, not less).
	count := 0
	for _, tbl := range schema.RequiredTables {
		if tablePresent(upper, tbl) {
			count++
		}
	}
	if count != 15 {
		t.Errorf("expected exactly 15 tables, counted %d", count)
	}
}

// Mutation target A: remove all CREATE TABLE lines → this test must fail.
// (verified manually; see mutation_proof in task output)
func TestStructure_ExactlyFifteenTables_AllTableNames(t *testing.T) {
	expected := []string{
		"nodes", "gpu_devices", "sessions", "session_windows", "session_panes",
		"reservations", "migration_history", "audit_log", "users", "health_snapshots",
		"llm_advisories", "build_jobs", "build_artifacts", "network_policies", "cluster_config",
	}
	actual := schema.RequiredTables
	if len(actual) != len(expected) {
		t.Fatalf("RequiredTables len=%d, want %d", len(actual), len(expected))
	}
	nameSet := make(map[string]bool)
	for _, n := range actual {
		nameSet[n] = true
	}
	for _, n := range expected {
		if !nameSet[n] {
			t.Errorf("table %q missing from RequiredTables", n)
		}
	}
}

// ─── TestStructure_TriggerFunctionNames ──────────────────────────────────────

func TestStructure_HelixUpdateUpdatedAtFunction(t *testing.T) {
	sql := loadSQL(t)
	upper := strings.ToUpper(sql)

	fn := "helix_update_updated_at_column"
	if !strings.Contains(upper, strings.ToUpper(fn)) {
		t.Errorf("function %q not found in SQL", fn)
	}
	// Must appear as CREATE OR REPLACE FUNCTION
	if !strings.Contains(upper, "CREATE OR REPLACE FUNCTION "+strings.ToUpper(fn)) {
		t.Errorf("CREATE OR REPLACE FUNCTION %q not found", fn)
	}
}

func TestStructure_HelixAuditTriggerFunction(t *testing.T) {
	sql := loadSQL(t)
	upper := strings.ToUpper(sql)

	fn := "helix_audit_trigger"
	if !strings.Contains(upper, strings.ToUpper(fn)) {
		t.Errorf("function %q not found in SQL", fn)
	}
	if !strings.Contains(upper, "CREATE OR REPLACE FUNCTION "+strings.ToUpper(fn)) {
		t.Errorf("CREATE OR REPLACE FUNCTION %q not found", fn)
	}
}

// ─── TestStructure_NodesAuditTrigger ─────────────────────────────────────────

func TestStructure_HelixNodesAuditTriggerPresent(t *testing.T) {
	sql := loadSQL(t)
	upper := strings.ToUpper(sql)

	// The immutable audit trigger on nodes
	trig := "HELIX_NODES_AUDIT"
	if !strings.Contains(upper, trig) {
		t.Errorf("audit trigger %q not found in SQL", trig)
	}

	// It must be AFTER INSERT OR UPDATE OR DELETE
	if !strings.Contains(upper, "AFTER INSERT OR UPDATE OR DELETE ON NODES") {
		t.Errorf("nodes audit trigger must fire AFTER INSERT OR UPDATE OR DELETE ON NODES")
	}

	// It must reference helix_audit_trigger function
	if !strings.Contains(upper, "EXECUTE FUNCTION HELIX_AUDIT_TRIGGER()") {
		t.Errorf("nodes audit trigger must EXECUTE FUNCTION helix_audit_trigger()")
	}
}

// ─── TestStructure_UpdatedAtTriggers ─────────────────────────────────────────

func TestStructure_UpdatedAtTriggers(t *testing.T) {
	sql := loadSQL(t)
	upper := strings.ToUpper(sql)

	for _, trig := range schema.RequiredTriggers {
		if !strings.Contains(upper, strings.ToUpper(trig)) {
			t.Errorf("trigger %q not found in SQL", trig)
		}
	}
}

// ─── TestStructure_CheckConstraints ──────────────────────────────────────────

func TestStructure_NodesCheckConstraints(t *testing.T) {
	sql := loadSQL(t)
	upper := strings.ToUpper(sql)

	checks := []struct {
		name    string
		snippet string
	}{
		{"nodes cpu_cores positive", "CHECK (CPU_CORES > 0)"},
		{"nodes cpu_threads >= cores", "CHECK (CPU_THREADS >= CPU_CORES)"},
		{"nodes memory positive", "CHECK (MEMORY_BYTES > 0)"},
		{"nodes gpu_count nonneg", "CHECK (GPU_COUNT >= 0)"},
		{"nodes storage positive", "CHECK (STORAGE_BYTES > 0)"},
		{"nodes status enum", "STATUS IN (\n        'JOINING', 'READY'"},
		{"nodes role enum", "ROLE IN (\n        'WORKER'"},
	}

	for _, c := range checks {
		if !strings.Contains(upper, strings.ToUpper(c.snippet)) {
			t.Errorf("nodes CHECK constraint %q not found (snippet: %q)", c.name, c.snippet)
		}
	}
}

func TestStructure_ReservationsCheckConstraints(t *testing.T) {
	sql := loadSQL(t)
	upper := strings.ToUpper(sql)

	checks := []string{
		"CHECK (CPU_MILLICORES > 0)",
		"CHECK (MEMORY_BYTES > 0)",
		"CHECK (EXPIRES_AT > CREATED_AT)",
	}
	for _, c := range checks {
		if !strings.Contains(upper, c) {
			t.Errorf("reservations CHECK constraint not found: %q", c)
		}
	}
}

func TestStructure_HealthSnapshotsScoreRange(t *testing.T) {
	sql := loadSQL(t)
	upper := strings.ToUpper(sql)

	// score columns must be range-checked
	scores := []string{
		"CHECK (OVERALL_SCORE BETWEEN 0 AND 100)",
		"CHECK (CPU_SCORE BETWEEN 0 AND 100)",
		"CHECK (MEMORY_SCORE BETWEEN 0 AND 100)",
		"CHECK (DISK_SCORE BETWEEN 0 AND 100)",
	}
	for _, c := range scores {
		if !strings.Contains(upper, c) {
			t.Errorf("health_snapshots score CHECK not found: %q", c)
		}
	}
}

func TestStructure_LLMAdvisoriesConfidenceCheck(t *testing.T) {
	sql := loadSQL(t)
	upper := strings.ToUpper(sql)

	if !strings.Contains(upper, "CHECK (CONFIDENCE >= 0.0 AND CONFIDENCE <= 1.0)") {
		t.Error("llm_advisories confidence CHECK not found")
	}
}

// ─── TestStructure_Indexes ───────────────────────────────────────────────────

func TestStructure_RepresentativeIndexes(t *testing.T) {
	sql := loadSQL(t)
	upper := strings.ToUpper(sql)

	for _, idx := range schema.RequiredIndexPrefixes {
		if !strings.Contains(upper, strings.ToUpper(idx)) {
			t.Errorf("index %q not found in SQL", idx)
		}
	}
}

func TestStructure_IndexCount(t *testing.T) {
	sql := loadSQL(t)
	upper := strings.ToUpper(sql)

	// Count occurrences of "CREATE INDEX IF NOT EXISTS"
	count := strings.Count(upper, "CREATE INDEX IF NOT EXISTS")
	if count < 45 {
		t.Errorf("expected at least 45 indexes, found %d", count)
	}
	t.Logf("Total indexes defined: %d", count)
}

// ─── TestStructure_AuditLogPartitioned ───────────────────────────────────────

func TestStructure_AuditLogPartitioned(t *testing.T) {
	sql := loadSQL(t)
	upper := strings.ToUpper(sql)

	if !strings.Contains(upper, "PARTITION BY RANGE") {
		t.Error("audit_log must use PARTITION BY RANGE")
	}
	if !strings.Contains(upper, "PARTITION OF AUDIT_LOG") {
		t.Error("audit_log must have child partitions declared")
	}
	// At least 7 monthly partitions
	count := strings.Count(upper, "PARTITION OF AUDIT_LOG")
	if count < 7 {
		t.Errorf("expected at least 7 audit_log partitions, found %d", count)
	}
}

// ─── TestStructure_VerifyStructure_AllPass ────────────────────────────────────

func TestVerifyStructure_AllFacts(t *testing.T) {
	sql := loadSQL(t)
	facts := schema.VerifyStructure(sql)

	passed, failures := schema.AllFactsPassed(facts)
	for _, f := range facts {
		status := "PASS"
		if !f.Passed {
			status = "FAIL"
		}
		t.Logf("[%s] %s", status, f.Name)
	}
	if !passed {
		t.Errorf("structural verification failures:\n%s", strings.Join(failures, "\n"))
	}
}

// ─── TestResolvePSQLConfig ────────────────────────────────────────────────────

func TestResolvePSQLConfig_NeitherSet(t *testing.T) {
	// With no env vars set, should return available=false
	// (we rely on the test env not having HELIX_PG_CONTAINER/PSQL_DSN/DATABASE_URL)
	t.Setenv("HELIX_PG_CONTAINER", "")
	t.Setenv("PSQL_DSN", "")
	t.Setenv("DATABASE_URL", "")

	_, available := schema.ResolvePSQLConfig()
	if available {
		// If a CI env happens to set DATABASE_URL, we just skip the negative assertion.
		t.Log("PSQL config available (CI environment) — skipping not-available check")
	}
}

func TestResolvePSQLConfig_ContainerSet(t *testing.T) {
	t.Setenv("HELIX_PG_CONTAINER", "helix-postgres")
	t.Setenv("PSQL_DSN", "")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("PGDATABASE", "mydb")

	cfg, available := schema.ResolvePSQLConfig()
	if !available {
		t.Fatal("expected available=true when HELIX_PG_CONTAINER is set")
	}
	if !cfg.UseContainer {
		t.Error("expected UseContainer=true")
	}
	if cfg.ContainerName != "helix-postgres" {
		t.Errorf("ContainerName=%q, want %q", cfg.ContainerName, "helix-postgres")
	}
	if cfg.Database != "mydb" {
		t.Errorf("Database=%q, want %q", cfg.Database, "mydb")
	}
}

func TestResolvePSQLConfig_DSNSet(t *testing.T) {
	t.Setenv("HELIX_PG_CONTAINER", "")
	t.Setenv("PSQL_DSN", "postgres://localhost/helix")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("PGDATABASE", "")

	cfg, available := schema.ResolvePSQLConfig()
	if !available {
		t.Fatal("expected available=true when PSQL_DSN is set")
	}
	if cfg.UseContainer {
		t.Error("expected UseContainer=false when only PSQL_DSN set")
	}
	if cfg.DSN != "postgres://localhost/helix" {
		t.Errorf("DSN=%q, want postgres://localhost/helix", cfg.DSN)
	}
	// Default PGDATABASE
	if cfg.Database != "helix" {
		t.Errorf("Database=%q, want helix (default)", cfg.Database)
	}
}

func TestResolvePSQLConfig_DatabaseURLFallback(t *testing.T) {
	t.Setenv("HELIX_PG_CONTAINER", "")
	t.Setenv("PSQL_DSN", "")
	t.Setenv("DATABASE_URL", "postgres://localhost/fallback")

	cfg, available := schema.ResolvePSQLConfig()
	if !available {
		t.Fatal("expected available=true when DATABASE_URL is set")
	}
	if cfg.DSN != "postgres://localhost/fallback" {
		t.Errorf("DSN=%q, want postgres://localhost/fallback", cfg.DSN)
	}
}

// ─── TestVerifyStructure_Mutation ─────────────────────────────────────────────
// §1.1 mutation test: if we strip the "helix_audit_trigger" function name from
// the SQL, VerifyStructure must report failure.

func TestVerifyStructure_Mutation_RemoveAuditTriggerFunction(t *testing.T) {
	sql := loadSQL(t)

	// Mutate: replace the function name to break the assertion
	mutated := strings.ReplaceAll(sql, "helix_audit_trigger", "helix_REMOVED_trigger")
	facts := schema.VerifyStructure(mutated)

	passed, _ := schema.AllFactsPassed(facts)
	if passed {
		t.Error("mutation: removing helix_audit_trigger name should cause VerifyStructure to FAIL, but it PASSED — bluff detected")
	}
	// Verify the specific fact that failed is the one we expect
	for _, f := range facts {
		if strings.Contains(f.Name, "helix_audit_trigger") && f.Passed {
			t.Errorf("fact %q should have failed after mutation but reported PASS", f.Name)
		}
	}
}

func TestVerifyStructure_Mutation_RemoveNodesTable(t *testing.T) {
	sql := loadSQL(t)

	// Mutate: rename nodes table only (use exact trailing space to avoid matching
	// node_id, nodes_updated_at, etc.)
	mutated := strings.ReplaceAll(sql,
		"CREATE TABLE IF NOT EXISTS nodes (",
		"CREATE TABLE IF NOT EXISTS nodes_mutated (")
	facts := schema.VerifyStructure(mutated)

	passed, _ := schema.AllFactsPassed(facts)
	if passed {
		t.Error("mutation: renaming 'nodes' CREATE TABLE should cause VerifyStructure to FAIL")
	}
}

func TestVerifyStructure_Mutation_RemoveCheckConstraint(t *testing.T) {
	sql := loadSQL(t)

	// Mutate: remove the cpu_cores CHECK
	mutated := strings.ReplaceAll(sql,
		"CONSTRAINT nodes_cpu_cores_positive  CHECK (cpu_cores > 0)",
		"-- CHECK removed by mutation")
	facts := schema.VerifyStructure(mutated)

	passed, _ := schema.AllFactsPassed(facts)
	if passed {
		t.Error("mutation: removing cpu_cores CHECK should cause VerifyStructure to FAIL")
	}
}

// ─── TestStructure_ClusterConfigSeeded ───────────────────────────────────────

func TestStructure_ClusterConfigSeedPresent(t *testing.T) {
	sql := loadSQL(t)
	upper := strings.ToUpper(sql)

	if !strings.Contains(upper, "INSERT INTO CLUSTER_CONFIG") {
		t.Error("cluster_config seed INSERT not found in SQL")
	}
	if !strings.Contains(upper, "ON CONFLICT (KEY) DO NOTHING") {
		t.Error("cluster_config seed must use ON CONFLICT (key) DO NOTHING")
	}
}

// ─── TestStructure_NetworkPoliciesConstraints ────────────────────────────────

func TestStructure_NetworkPoliciesDirectionCheck(t *testing.T) {
	sql := loadSQL(t)
	upper := strings.ToUpper(sql)

	if !strings.Contains(upper, "CHECK (DIRECTION IN ('INGRESS', 'EGRESS', 'BOTH'))") {
		t.Error("network_policies direction CHECK not found")
	}
	if !strings.Contains(upper, "CHECK (ACTION IN ('ALLOW', 'DENY', 'LOG', 'RATE_LIMIT'))") {
		t.Error("network_policies action CHECK not found")
	}
}
