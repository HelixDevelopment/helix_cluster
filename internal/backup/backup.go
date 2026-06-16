// Package backup implements the Helix Cluster OS backup service.
//
// It provides two core capabilities:
//  1. etcd snapshot — streams a point-in-time snapshot from a live etcd cluster
//     via the clientv3 Maintenance.Snapshot RPC and persists it to a file.
//  2. Postgres backup — drives pg_dump to produce a dump file, then pg_restore
//     (or psql) to load it into a fresh database, and finally verifies that
//     per-table row counts in the restored database match the source.
//
// Production paths:
//   - etcd: real clientv3.Client.Maintenance.Snapshot(ctx) streaming bytes to disk.
//   - Postgres: os/exec driving pg_dump / pg_restore / psql.
//
// Both capabilities expose injected seams (EtcdSnapshotter, SQLRunner) so unit
// tests can drive deterministic fakes without a live server.
package backup

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// ── etcd snapshot seam ────────────────────────────────────────────────────────

// EtcdSnapshotter is the seam unit tests inject to avoid a live etcd.
// In production it is satisfied by etcdMaintenanceSnapshotter, which calls
// the real clientv3 Maintenance.Snapshot RPC.
type EtcdSnapshotter interface {
	// Snapshot returns a ReadCloser for the binary etcd snapshot stream.
	Snapshot(ctx context.Context) (io.ReadCloser, error)
}

// etcdMaintenanceSnapshotter wraps a real *clientv3.Client and satisfies
// EtcdSnapshotter by delegating to Maintenance.Snapshot.
type etcdMaintenanceSnapshotter struct {
	cli *clientv3.Client
}

func (s *etcdMaintenanceSnapshotter) Snapshot(ctx context.Context) (io.ReadCloser, error) {
	return clientv3.NewMaintenance(s.cli).Snapshot(ctx)
}

// RealEtcdSnapshotter constructs an EtcdSnapshotter backed by a live etcd client.
// The caller is responsible for closing the client when done.
func RealEtcdSnapshotter(cli *clientv3.Client) EtcdSnapshotter {
	return &etcdMaintenanceSnapshotter{cli: cli}
}

// ── SnapshotEtcd ──────────────────────────────────────────────────────────────

// SnapshotEtcd streams the etcd snapshot from snapshotter and writes it to
// destPath. It returns the number of bytes written.
//
// CLAUDE-1 §7.1: the file at destPath is the sink-side artifact; callers MUST
// assert the file content equals the expected snapshot bytes.
//
// Atomicity (§11.4 WIRED hardening): the snapshot is streamed to a sibling temp
// file (destPath+".tmp"), fsync'd and closed, and only on FULL success renamed
// over destPath via os.Rename (atomic on the same filesystem). On ANY error the
// temp file is removed and destPath is left UNTOUCHED — so a mid-stream failure
// leaves no partial canonical file, and a failed retry to the same destPath does
// NOT clobber a previously-good backup.
func SnapshotEtcd(ctx context.Context, snapshotter EtcdSnapshotter, destPath string) (int64, error) {
	rc, err := snapshotter.Snapshot(ctx)
	if err != nil {
		return 0, fmt.Errorf("backup: etcd snapshot RPC: %w", err)
	}
	defer rc.Close()

	tmpPath := destPath + ".tmp"

	f, err := os.Create(tmpPath)
	if err != nil {
		return 0, fmt.Errorf("backup: create snapshot temp file %s: %w", tmpPath, err)
	}

	// Until the rename succeeds, ensure the temp file never lingers on disk and
	// destPath is never touched on any error path. Set to false only after the
	// atomic rename promotes the temp file to destPath.
	committed := false
	defer func() {
		if !committed {
			_ = f.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	n, err := io.Copy(f, rc)
	if err != nil {
		return n, fmt.Errorf("backup: write snapshot to %s: %w", tmpPath, err)
	}
	if err := f.Sync(); err != nil {
		return n, fmt.Errorf("backup: sync snapshot temp file %s: %w", tmpPath, err)
	}
	if err := f.Close(); err != nil {
		return n, fmt.Errorf("backup: close snapshot temp file %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		return n, fmt.Errorf("backup: rename snapshot %s -> %s: %w", tmpPath, destPath, err)
	}
	committed = true
	return n, nil
}

// ── SQL runner seam ───────────────────────────────────────────────────────────

// SQLRunner is the seam unit tests inject instead of running real os/exec
// processes. Each Run call receives the full argv slice and returns (stdout, error).
type SQLRunner interface {
	Run(ctx context.Context, argv []string) (stdout string, err error)
}

// execSQLRunner implements SQLRunner by executing the argv as a real subprocess.
type execSQLRunner struct{}

func (e *execSQLRunner) Run(ctx context.Context, argv []string) (string, error) {
	if len(argv) == 0 {
		return "", fmt.Errorf("sqlrunner: empty argv")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("sqlrunner %v: %w", argv, err)
	}
	return string(out), nil
}

// RealSQLRunner returns a SQLRunner that executes real subprocesses.
func RealSQLRunner() SQLRunner { return &execSQLRunner{} }

// ── PostgresBackup ────────────────────────────────────────────────────────────

// PostgresBackupConfig carries connection and naming parameters for a
// pg_dump / restore operation.
type PostgresBackupConfig struct {
	// Host and Port for the Postgres server.
	Host string
	Port string
	// User to authenticate as (no password — assumes pg_hba or PGPASSWORD).
	User string
	// SourceDB is the database to dump.
	SourceDB string
	// DumpPath is where the pg_dump file is written.
	DumpPath string
	// RestoreDB is the fresh database to restore into. The caller must
	// create it before calling RestorePostgres (or use EnsureRestoreDB).
	RestoreDB string
}

// DumpArgv returns the argv slice for the pg_dump command.
// This is a pure function: unit tests assert the exact argv without running it.
func DumpArgv(cfg PostgresBackupConfig) []string {
	return []string{
		"pg_dump",
		"-h", cfg.Host,
		"-p", cfg.Port,
		"-U", cfg.User,
		"-F", "c", // custom format, compatible with pg_restore
		"-f", cfg.DumpPath,
		cfg.SourceDB,
	}
}

// RestoreArgv returns the argv slice for the pg_restore command.
func RestoreArgv(cfg PostgresBackupConfig) []string {
	return []string{
		"pg_restore",
		"-h", cfg.Host,
		"-p", cfg.Port,
		"-U", cfg.User,
		"-d", cfg.RestoreDB,
		"--no-owner",
		"--no-privileges",
		cfg.DumpPath,
	}
}

// CreateDBArgv returns the argv to create the restore target database via psql.
func CreateDBArgv(cfg PostgresBackupConfig) []string {
	return []string{
		"psql",
		"-h", cfg.Host,
		"-p", cfg.Port,
		"-U", cfg.User,
		"-d", "postgres",
		"-c", fmt.Sprintf("CREATE DATABASE %s;", cfg.RestoreDB),
	}
}

// CountRowsArgv returns the argv to count rows in a table via psql, printing
// only the integer (using -t -A for tuples-only / unaligned output).
func CountRowsArgv(cfg PostgresBackupConfig, db, table string) []string {
	return []string{
		"psql",
		"-h", cfg.Host,
		"-p", cfg.Port,
		"-U", cfg.User,
		"-d", db,
		"-t", "-A",
		"-c", fmt.Sprintf("SELECT COUNT(*) FROM %s;", table),
	}
}

// DumpPostgres runs pg_dump for the source database.
func DumpPostgres(ctx context.Context, runner SQLRunner, cfg PostgresBackupConfig) error {
	_, err := runner.Run(ctx, DumpArgv(cfg))
	if err != nil {
		return fmt.Errorf("backup: pg_dump: %w", err)
	}
	return nil
}

// RestorePostgres runs pg_restore into the target database.
func RestorePostgres(ctx context.Context, runner SQLRunner, cfg PostgresBackupConfig) error {
	_, err := runner.Run(ctx, RestoreArgv(cfg))
	if err != nil {
		return fmt.Errorf("backup: pg_restore: %w", err)
	}
	return nil
}

// CountRows queries the number of rows in table within db via psql and parses
// the integer output.
func CountRows(ctx context.Context, runner SQLRunner, cfg PostgresBackupConfig, db, table string) (int64, error) {
	out, err := runner.Run(ctx, CountRowsArgv(cfg, db, table))
	if err != nil {
		return 0, fmt.Errorf("backup: count rows in %s.%s: %w", db, table, err)
	}
	cleaned := strings.TrimSpace(out)
	var n int64
	if _, err := fmt.Sscan(cleaned, &n); err != nil {
		return 0, fmt.Errorf("backup: parse row count %q from %s.%s: %w", cleaned, db, table, err)
	}
	return n, nil
}

// VerifyRowCounts compares per-table row counts between the source database and
// the restored database. It returns an error if ANY table count differs.
//
// §1.1 mutation gate: disabling this comparison causes a mismatch to go
// unreported; the unit test for this function breaks the comparison and asserts
// the error is returned.
func VerifyRowCounts(ctx context.Context, runner SQLRunner, cfg PostgresBackupConfig, tables []string) error {
	for _, table := range tables {
		srcCount, err := CountRows(ctx, runner, cfg, cfg.SourceDB, table)
		if err != nil {
			return fmt.Errorf("backup: count source rows for %s: %w", table, err)
		}
		dstCount, err := CountRows(ctx, runner, cfg, cfg.RestoreDB, table)
		if err != nil {
			return fmt.Errorf("backup: count restored rows for %s: %w", table, err)
		}
		if srcCount != dstCount {
			return fmt.Errorf("backup: row-count mismatch for table %s: source=%d restored=%d",
				table, srcCount, dstCount)
		}
	}
	return nil
}

// FullPostgresBackup is a convenience wrapper: dump → restore → verify.
func FullPostgresBackup(ctx context.Context, runner SQLRunner, cfg PostgresBackupConfig, tables []string) error {
	if err := DumpPostgres(ctx, runner, cfg); err != nil {
		return err
	}
	if err := RestorePostgres(ctx, runner, cfg); err != nil {
		return err
	}
	return VerifyRowCounts(ctx, runner, cfg, tables)
}
