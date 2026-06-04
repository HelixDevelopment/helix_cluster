// Package schema provides a thin applier and structural verifier for the
// Helix Cluster OS authoritative Postgres 16+ primary schema.
//
// The SQL migration lives at migrations/postgresql/0001_primary_schema.sql.
// All DB operations are performed via os/exec → psql (no Go DB driver).
package schema

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// RequiredTables is the canonical set of 15 table names in the primary schema.
var RequiredTables = []string{
	"nodes",
	"gpu_devices",
	"sessions",
	"session_windows",
	"session_panes",
	"reservations",
	"migration_history",
	"audit_log",
	"users",
	"health_snapshots",
	"llm_advisories",
	"build_jobs",
	"build_artifacts",
	"network_policies",
	"cluster_config",
}

// RequiredFunctions are the two trigger functions whose presence is asserted.
var RequiredFunctions = []string{
	"helix_update_updated_at_column",
	"helix_audit_trigger",
}

// RequiredTriggers are the per-table trigger names that must be present.
var RequiredTriggers = []string{
	"helix_nodes_updated_at",
	"helix_nodes_audit",
	"helix_gpu_devices_updated_at",
	"helix_sessions_updated_at",
	"helix_reservations_updated_at",
	"helix_users_updated_at",
	"helix_llm_advisories_updated_at",
	"helix_build_jobs_updated_at",
	"helix_network_policies_updated_at",
	"helix_cluster_config_updated_at",
}

// RequiredIndexPrefixes are sub-strings that must appear in the SQL for index coverage.
// These are representative index names to confirm ~53 indexes were written.
var RequiredIndexPrefixes = []string{
	"idx_nodes_status",
	"idx_nodes_role",
	"idx_nodes_region",
	"idx_nodes_labels",
	"idx_nodes_last_seen",
	"idx_nodes_hostname",
	"idx_gpu_devices_node_id",
	"idx_gpu_devices_status",
	"idx_sessions_owner",
	"idx_sessions_status",
	"idx_session_windows_session_id",
	"idx_session_panes_window_id",
	"idx_reservations_session_id",
	"idx_reservations_status",
	"idx_migration_history_session_id",
	"idx_audit_log_timestamp",
	"idx_audit_log_event_type",
	"idx_users_spiffe_id",
	"idx_health_snapshots_node_id",
	"idx_llm_advisories_status",
	"idx_build_jobs_status",
	"idx_build_artifacts_job_id",
	"idx_network_policies_direction",
	"idx_cluster_config_key",
}

// RequiredCheckConstraints are sub-strings that must appear in the SQL for CHECK coverage.
var RequiredCheckConstraints = []string{
	"CHECK (cpu_cores > 0)",
	"CHECK (memory_bytes > 0)",
	"CHECK (status IN",
	"CHECK (role IN",
	"BETWEEN 0 AND 100",
	"CHECK (confidence >= 0.0",
	"CHECK (priority >= 0",
}

// SchemaPath returns the absolute path to the primary SQL migration file,
// resolved relative to the caller's source file location (works regardless of
// working directory).
func SchemaPath() (string, error) {
	_, callerFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("schema: unable to resolve caller path via runtime.Caller")
	}
	// callerFile is .../internal/schema/schema.go
	// migration is at .../migrations/postgresql/0001_primary_schema.sql
	repoRoot := filepath.Join(filepath.Dir(callerFile), "..", "..")
	p := filepath.Join(repoRoot, "migrations", "postgresql", "0001_primary_schema.sql")
	return filepath.Clean(p), nil
}

// ReadSQL reads the primary schema SQL file and returns its contents.
func ReadSQL() (string, error) {
	p, err := SchemaPath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("schema: read %s: %w", p, err)
	}
	return string(data), nil
}

// MigrationsDir returns the absolute path to the golang-migrate chain directory
// (migrations/postgresql/), resolved relative to this source file so it works
// regardless of the process working directory.
func MigrationsDir() (string, error) {
	_, callerFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("schema: unable to resolve caller path via runtime.Caller")
	}
	repoRoot := filepath.Join(filepath.Dir(callerFile), "..", "..")
	return filepath.Clean(filepath.Join(repoRoot, "migrations", "postgresql")), nil
}

// ChainUpFiles returns the ordered list of golang-migrate "*.up.sql" files in the
// numbered chain (001_*.up.sql … NNN_*.up.sql), sorted by version. The
// consolidated 0001_primary_schema.sql is deliberately EXCLUDED — it is the
// derived artifact, not a member of the chain.
func ChainUpFiles() ([]string, error) {
	dir, err := MigrationsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("schema: read dir %s: %w", dir, err)
	}
	var files []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		// Exclude the consolidated derived artifact (it has no ".up.sql" suffix,
		// but guard explicitly in case of future renames).
		if name == "0001_primary_schema.sql" {
			continue
		}
		files = append(files, filepath.Join(dir, name))
	}
	sort.Strings(files)
	return files, nil
}

// ReadChainSQL reads and concatenates every chain "*.up.sql" file in version
// order, returning the combined SQL text. This is the canonical schema as the
// golang-migrate runner applies it (see scripts/run-migrations.sh).
func ReadChainSQL() (string, error) {
	files, err := ChainUpFiles()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return "", fmt.Errorf("schema: read %s: %w", f, err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// StructuralFact describes one structural assertion about the SQL text.
type StructuralFact struct {
	Name   string
	Passed bool
	Detail string
}

// VerifyStructure parses the SQL text and returns a list of structural facts.
// All facts must have Passed=true for a valid schema.
func VerifyStructure(sql string) []StructuralFact {
	upper := strings.ToUpper(sql)
	var facts []StructuralFact

	// Fact 1: exactly 15 required CREATE TABLE statements.
	// We match "CREATE TABLE IF NOT EXISTS <name> (" or "CREATE TABLE IF NOT EXISTS <name>\n"
	// to avoid false positives from tables whose names are prefixes of other names.
	tableCount := 0
	missingTables := []string{}
	for _, tbl := range RequiredTables {
		upperTbl := strings.ToUpper(tbl)
		// Match with trailing space or newline (word boundary after table name)
		needle1 := "CREATE TABLE IF NOT EXISTS " + upperTbl + " "
		needle2 := "CREATE TABLE IF NOT EXISTS " + upperTbl + "\n"
		needle3 := "CREATE TABLE IF NOT EXISTS " + upperTbl + "\r"
		if strings.Contains(upper, needle1) || strings.Contains(upper, needle2) || strings.Contains(upper, needle3) {
			tableCount++
		} else {
			missingTables = append(missingTables, tbl)
		}
	}
	facts = append(facts, StructuralFact{
		Name:   "15 required tables",
		Passed: tableCount == 15,
		Detail: fmt.Sprintf("found %d/15 CREATE TABLE statements; missing: %v", tableCount, missingTables),
	})

	// Fact 2: helix_update_updated_at_column function present
	for _, fn := range RequiredFunctions {
		upperFn := strings.ToUpper(fn)
		facts = append(facts, StructuralFact{
			Name:   "function: " + fn,
			Passed: strings.Contains(upper, upperFn),
			Detail: fmt.Sprintf("function %q must appear in SQL", fn),
		})
	}

	// Fact 3: required trigger names present
	for _, trig := range RequiredTriggers {
		upperTrig := strings.ToUpper(trig)
		facts = append(facts, StructuralFact{
			Name:   "trigger: " + trig,
			Passed: strings.Contains(upper, upperTrig),
			Detail: fmt.Sprintf("trigger %q must appear in SQL", trig),
		})
	}

	// Fact 4: representative index names
	for _, idx := range RequiredIndexPrefixes {
		upperIdx := strings.ToUpper(idx)
		facts = append(facts, StructuralFact{
			Name:   "index: " + idx,
			Passed: strings.Contains(upper, upperIdx),
			Detail: fmt.Sprintf("index %q must appear in SQL", idx),
		})
	}

	// Fact 5: CHECK constraints present
	for _, chk := range RequiredCheckConstraints {
		upperChk := strings.ToUpper(chk)
		facts = append(facts, StructuralFact{
			Name:   "CHECK: " + chk,
			Passed: strings.Contains(upper, upperChk),
			Detail: fmt.Sprintf("CHECK constraint fragment %q must appear in SQL", chk),
		})
	}

	// Fact 6: audit_log is partitioned
	facts = append(facts, StructuralFact{
		Name:   "audit_log PARTITION BY RANGE",
		Passed: strings.Contains(upper, "PARTITION BY RANGE"),
		Detail: "audit_log must be range-partitioned",
	})

	// Fact 7: PARTITION OF audit_log (at least 1 child partition)
	facts = append(facts, StructuralFact{
		Name:   "audit_log child partitions",
		Passed: strings.Contains(upper, "PARTITION OF AUDIT_LOG"),
		Detail: "at least one audit_log child partition must be defined",
	})

	// Fact 8: helix_nodes_audit trigger (immutable audit trigger for nodes)
	facts = append(facts, StructuralFact{
		Name:   "helix_nodes_audit trigger definition",
		Passed: strings.Contains(upper, "HELIX_NODES_AUDIT"),
		Detail: "the immutable audit trigger on nodes must be present",
	})

	return facts
}

// AllFactsPassed returns true if every StructuralFact has Passed=true.
func AllFactsPassed(facts []StructuralFact) (bool, []string) {
	var failures []string
	for _, f := range facts {
		if !f.Passed {
			failures = append(failures, fmt.Sprintf("[FAIL] %s: %s", f.Name, f.Detail))
		}
	}
	return len(failures) == 0, failures
}

// PSQLConfig holds the connection parameters resolved from the environment.
type PSQLConfig struct {
	// UseContainer indicates psql should be invoked inside a podman container.
	UseContainer  bool
	ContainerName string
	Database      string
	// DSN is the full connection string for local psql (used when UseContainer=false).
	DSN string
}

// ResolvePSQLConfig reads environment variables to determine how to connect.
// Returns (config, available) where available=false means neither container nor DSN is configured.
func ResolvePSQLConfig() (PSQLConfig, bool) {
	container := os.Getenv("HELIX_PG_CONTAINER")
	dsn := os.Getenv("PSQL_DSN")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	db := os.Getenv("PGDATABASE")
	if db == "" {
		db = "helix"
	}

	if container != "" {
		return PSQLConfig{UseContainer: true, ContainerName: container, Database: db}, true
	}
	if dsn != "" {
		return PSQLConfig{UseContainer: false, DSN: dsn, Database: db}, true
	}
	return PSQLConfig{}, false
}

// RunSQL executes a SQL string via psql according to PSQLConfig.
// It returns the combined stdout/stderr output and any error.
func RunSQL(cfg PSQLConfig, sql string) (string, error) {
	return runPsql(cfg, "-c", sql)
}

// RunSQLFile executes a SQL file via psql according to PSQLConfig.
func RunSQLFile(cfg PSQLConfig, filePath string) (string, error) {
	return runPsql(cfg, "-f", filePath)
}

// ApplyPrimarySchema applies the 0001_primary_schema.sql migration via psql.
//
// It reads the migration file on the HOST and pipes its contents to psql over
// stdin (psql -f -). This is required for container mode: `podman exec psql -f
// <hostpath>` would run psql inside the container, which cannot see the host
// filesystem. Streaming the bytes over stdin works identically for both the
// container and local-DSN transports.
func ApplyPrimarySchema(cfg PSQLConfig) (string, error) {
	sqlText, err := ReadSQL()
	if err != nil {
		return "", err
	}
	return RunSQLStdin(cfg, sqlText)
}

// RunSQLStdin executes SQL piped to psql over stdin (psql -f -), so the SQL
// bytes do not need to exist as a file inside a target container.
func RunSQLStdin(cfg PSQLConfig, sqlText string) (string, error) {
	var (
		name    string
		cmdArgs []string
	)
	if cfg.UseContainer {
		name = "podman"
		cmdArgs = []string{
			"exec", "-i", cfg.ContainerName,
			"psql", "-U", "postgres", "-d", cfg.Database,
			"-v", "ON_ERROR_STOP=1", "-f", "-",
		}
	} else {
		name = "psql"
		cmdArgs = []string{cfg.DSN, "-v", "ON_ERROR_STOP=1", "-f", "-"}
	}
	cmd := exec.Command(name, cmdArgs...)
	cmd.Stdin = strings.NewReader(sqlText)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return buf.String(), fmt.Errorf("psql -f -: %w\noutput: %s", err, buf.String())
	}
	return buf.String(), nil
}

// QueryRows runs a SQL query and returns tab-separated rows (one per line).
func QueryRows(cfg PSQLConfig, sql string) ([]string, error) {
	out, err := runPsqlQuery(cfg, sql)
	if err != nil {
		return nil, err
	}
	var rows []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			rows = append(rows, line)
		}
	}
	return rows, nil
}

// runPsql builds and executes the psql command with the given extra args.
func runPsql(cfg PSQLConfig, extraArgs ...string) (string, error) {
	args, err := buildPsqlArgs(cfg, extraArgs...)
	if err != nil {
		return "", err
	}
	var cmdArgs []string
	var name string
	if cfg.UseContainer {
		name = "podman"
		cmdArgs = append([]string{"exec", "-i", cfg.ContainerName, "psql", "-U", "postgres", "-d", cfg.Database, "-v", "ON_ERROR_STOP=1"}, extraArgs...)
	} else {
		name = "psql"
		cmdArgs = args
	}
	_ = args
	cmd := exec.Command(name, cmdArgs...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return buf.String(), fmt.Errorf("psql: %w\noutput: %s", err, buf.String())
	}
	return buf.String(), nil
}

// buildPsqlArgs assembles the psql argument list for local (non-container) runs.
func buildPsqlArgs(cfg PSQLConfig, extraArgs ...string) ([]string, error) {
	if cfg.UseContainer {
		// Container mode builds differently — handled inline in runPsql.
		return nil, nil
	}
	args := []string{cfg.DSN, "-v", "ON_ERROR_STOP=1"}
	args = append(args, extraArgs...)
	return args, nil
}

// runPsqlQuery runs a SQL string and returns the -t -A (tuple-only, unaligned) output.
func runPsqlQuery(cfg PSQLConfig, sql string) (string, error) {
	var (
		name    string
		cmdArgs []string
	)
	if cfg.UseContainer {
		name = "podman"
		cmdArgs = []string{
			"exec", "-i", cfg.ContainerName,
			"psql", "-U", "postgres", "-d", cfg.Database,
			"-v", "ON_ERROR_STOP=1",
			"-t", "-A",
			"-c", sql,
		}
	} else {
		name = "psql"
		cmdArgs = []string{
			cfg.DSN,
			"-v", "ON_ERROR_STOP=1",
			"-t", "-A",
			"-c", sql,
		}
	}
	cmd := exec.Command(name, cmdArgs...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return buf.String(), fmt.Errorf("psql query: %w\noutput: %s", err, buf.String())
	}
	return buf.String(), nil
}

// CheckBinaryPresent returns an error if the named binary is not found in PATH.
func CheckBinaryPresent(name string) error {
	_, err := exec.LookPath(name)
	return err
}
