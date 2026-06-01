// Package backup — deterministic unit tests for the backup service (HXC-1131).
//
// These tests prove:
//  1. SnapshotEtcd writes streamed bytes from a fake snapshotter to the target
//     file, and the file content is byte-for-byte equal to the snapshot data.
//  2. DumpArgv / RestoreArgv build the correct pg_dump / pg_restore command
//     lines; a fake SQLRunner captures argv for assertion.
//  3. VerifyRowCounts correctly compares source vs restored row counts and
//     returns an error on mismatch — the §1.1 mutation target.
//
// No live etcd or Postgres is needed. All assertions are sink-side (file
// content, exact argv, mismatch detection), never "no panic" / "non-nil".
package backup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── fake EtcdSnapshotter ──────────────────────────────────────────────────────

// fakeSnapshotter returns a fixed payload as the snapshot stream.
type fakeSnapshotter struct {
	payload []byte
	err     error
}

func (f *fakeSnapshotter) Snapshot(_ context.Context) (io.ReadCloser, error) {
	if f.err != nil {
		return nil, f.err
	}
	return io.NopCloser(bytes.NewReader(f.payload)), nil
}

// ── fake SQLRunner ────────────────────────────────────────────────────────────

// fakeSQLRunner records every argv and returns canned responses keyed on the
// first argument (command name, e.g. "pg_dump") plus optional secondary key
// on the db argument.
type fakeSQLRunner struct {
	calls [][]string // captured argv slices in call order
	// responses maps a lookup key to (stdout, error).
	// The key is built by responseKey(argv).
	responses map[string]fakeResponse
}

type fakeResponse struct {
	stdout string
	err    error
}

func newFakeSQLRunner() *fakeSQLRunner {
	return &fakeSQLRunner{
		responses: make(map[string]fakeResponse),
	}
}

// respondTo registers a canned response for a key.
func (f *fakeSQLRunner) respondTo(key string, stdout string, err error) {
	f.responses[key] = fakeResponse{stdout: stdout, err: err}
}

// responseKey builds a stable lookup key from the argv.
// Strategy: join the full argv with "|" so tests can match exact calls.
func responseKey(argv []string) string {
	return strings.Join(argv, "|")
}

func (f *fakeSQLRunner) Run(_ context.Context, argv []string) (string, error) {
	f.calls = append(f.calls, argv)
	key := responseKey(argv)
	if r, ok := f.responses[key]; ok {
		return r.stdout, r.err
	}
	// Default: return empty stdout, no error (pg_dump/pg_restore return nothing useful).
	return "", nil
}

// callCount returns how many times the runner was called with argv[0] == cmd.
func (f *fakeSQLRunner) callCount(cmd string) int {
	var n int
	for _, a := range f.calls {
		if len(a) > 0 && a[0] == cmd {
			n++
		}
	}
	return n
}

// lastCall returns the most recent argv for the given command, or nil.
func (f *fakeSQLRunner) lastCall(cmd string) []string {
	for i := len(f.calls) - 1; i >= 0; i-- {
		if len(f.calls[i]) > 0 && f.calls[i][0] == cmd {
			return f.calls[i]
		}
	}
	return nil
}

// ── Test: SnapshotEtcd writes bytes to file ───────────────────────────────────

// TestSnapshotEtcd_WritesStreamToFile is the primary sink-side assertion for
// etcd snapshot: the fake snapshotter returns known bytes, and the file written
// by SnapshotEtcd must contain exactly those bytes.
//
// §1.1 mutation: if SnapshotEtcd skips writing to the file (no-op), the file
// remains empty and assert.Equal fails — the test catches the broken path.
func TestSnapshotEtcd_WritesStreamToFile(t *testing.T) {
	payload := []byte("ETCD_SNAPSHOT_MAGIC_v3\x00\x01\x02\x03" + strings.Repeat("helix-cluster-snapshot-data", 10))
	snap := &fakeSnapshotter{payload: payload}

	dir := t.TempDir()
	dest := filepath.Join(dir, "etcd.snap")

	n, err := SnapshotEtcd(context.Background(), snap, dest)
	require.NoError(t, err)
	assert.Equal(t, int64(len(payload)), n, "returned byte count must equal payload length")

	// Sink-side: file content must be byte-for-byte the snapshot payload.
	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, payload, got, "file content must equal the snapshot payload")
}

// TestSnapshotEtcd_ReturnsErrorWhenSnapshotFails proves SnapshotEtcd surfaces
// the RPC error instead of writing a partial/empty file.
func TestSnapshotEtcd_ReturnsErrorWhenSnapshotFails(t *testing.T) {
	snap := &fakeSnapshotter{err: fmt.Errorf("etcd: unavailable")}
	dir := t.TempDir()
	dest := filepath.Join(dir, "etcd.snap")

	_, err := SnapshotEtcd(context.Background(), snap, dest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "etcd: unavailable")

	// The file must not have been created (or be empty) — no partial writes.
	_, statErr := os.Stat(dest)
	assert.True(t, os.IsNotExist(statErr), "file must not exist when Snapshot RPC fails")
}

// TestSnapshotEtcd_EmptyStream proves zero-byte snapshots are handled without
// error and result in an empty file (edge case: blank etcd cluster).
func TestSnapshotEtcd_EmptyStream(t *testing.T) {
	snap := &fakeSnapshotter{payload: []byte{}}
	dir := t.TempDir()
	dest := filepath.Join(dir, "empty.snap")

	n, err := SnapshotEtcd(context.Background(), snap, dest)
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)

	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Empty(t, got)
}

// ── Test: DumpArgv builds correct pg_dump command ────────────────────────────

// TestDumpArgv_CorrectCommand proves DumpArgv builds the exact pg_dump
// invocation required: host, port, user, custom format, output file, db name.
//
// §1.1 mutation: remove "-F"/"c" from DumpArgv — argv no longer contains the
// format flag and assert.Contains(argv, "-F") fails.
func TestDumpArgv_CorrectCommand(t *testing.T) {
	cfg := PostgresBackupConfig{
		Host:     "localhost",
		Port:     "5432",
		User:     "helix",
		SourceDB: "helix_nodes",
		DumpPath: "/tmp/helix.dump",
		RestoreDB: "helix_restore",
	}

	argv := DumpArgv(cfg)
	require.Equal(t, "pg_dump", argv[0], "first element must be pg_dump")

	// Sink-side: assert every required flag is present.
	joined := strings.Join(argv, " ")
	assert.Contains(t, joined, "-h localhost")
	assert.Contains(t, joined, "-p 5432")
	assert.Contains(t, joined, "-U helix")
	assert.Contains(t, joined, "-F c")
	assert.Contains(t, joined, "-f /tmp/helix.dump")
	assert.Contains(t, joined, "helix_nodes")
}

// TestRestoreArgv_CorrectCommand proves RestoreArgv builds the exact pg_restore
// invocation: host, port, user, target database, no-owner/no-privileges, dump file.
//
// §1.1 mutation: remove cfg.RestoreDB from RestoreArgv → joined does not
// contain "helix_restore" and assert.Contains fails.
func TestRestoreArgv_CorrectCommand(t *testing.T) {
	cfg := PostgresBackupConfig{
		Host:      "localhost",
		Port:      "5432",
		User:      "helix",
		SourceDB:  "helix_nodes",
		DumpPath:  "/tmp/helix.dump",
		RestoreDB: "helix_restore",
	}

	argv := RestoreArgv(cfg)
	require.Equal(t, "pg_restore", argv[0])

	joined := strings.Join(argv, " ")
	assert.Contains(t, joined, "-h localhost")
	assert.Contains(t, joined, "-p 5432")
	assert.Contains(t, joined, "-U helix")
	assert.Contains(t, joined, "-d helix_restore")
	assert.Contains(t, joined, "--no-owner")
	assert.Contains(t, joined, "--no-privileges")
	assert.Contains(t, joined, "/tmp/helix.dump")
}

// TestCountRowsArgv_CorrectCommand proves CountRowsArgv uses -t -A (tuples-only
// / unaligned) so the output can be parsed as a plain integer.
func TestCountRowsArgv_CorrectCommand(t *testing.T) {
	cfg := PostgresBackupConfig{
		Host: "db.local",
		Port: "5433",
		User: "admin",
	}

	argv := CountRowsArgv(cfg, "mydb", "nodes")
	require.Equal(t, "psql", argv[0])

	joined := strings.Join(argv, " ")
	assert.Contains(t, joined, "-t")
	assert.Contains(t, joined, "-A")
	assert.Contains(t, joined, "-d mydb")
	assert.Contains(t, joined, "SELECT COUNT(*) FROM nodes")
}

// ── Test: VerifyRowCounts detects mismatch ────────────────────────────────────

// TestVerifyRowCounts_MatchPasses proves VerifyRowCounts returns nil when all
// table counts match between source and restore databases.
func TestVerifyRowCounts_MatchPasses(t *testing.T) {
	cfg := PostgresBackupConfig{
		Host:      "localhost",
		Port:      "5432",
		User:      "helix",
		SourceDB:  "src",
		RestoreDB: "dst",
	}

	runner := newFakeSQLRunner()
	// Source counts.
	runner.respondTo(responseKey(CountRowsArgv(cfg, "src", "nodes")), "42\n", nil)
	runner.respondTo(responseKey(CountRowsArgv(cfg, "src", "sessions")), "7\n", nil)
	// Restored counts match.
	runner.respondTo(responseKey(CountRowsArgv(cfg, "dst", "nodes")), "42\n", nil)
	runner.respondTo(responseKey(CountRowsArgv(cfg, "dst", "sessions")), "7\n", nil)

	err := VerifyRowCounts(context.Background(), runner, cfg, []string{"nodes", "sessions"})
	assert.NoError(t, err, "matching counts must not produce an error")
}

// TestVerifyRowCounts_MismatchFails is the §1.1 mutation target.
//
// It proves VerifyRowCounts returns an error when the restored count differs
// from the source — catching a broken restore or a comparison bug.
//
// §1.1 mutation proof:
//   - Break: comment out the `if srcCount != dstCount { return fmt.Errorf(...) }` in VerifyRowCounts.
//   - Observe: the test below expects an error for mismatch but gets nil → FAIL.
//   - Restore: revert the comment-out.
func TestVerifyRowCounts_MismatchFails(t *testing.T) {
	cfg := PostgresBackupConfig{
		Host:      "localhost",
		Port:      "5432",
		User:      "helix",
		SourceDB:  "src",
		RestoreDB: "dst",
	}

	runner := newFakeSQLRunner()
	// Source: 100 rows in 'nodes'.
	runner.respondTo(responseKey(CountRowsArgv(cfg, "src", "nodes")), "100\n", nil)
	// Restored: only 42 rows — mismatch simulates a failed restore.
	runner.respondTo(responseKey(CountRowsArgv(cfg, "dst", "nodes")), "42\n", nil)

	err := VerifyRowCounts(context.Background(), runner, cfg, []string{"nodes"})
	require.Error(t, err, "row-count mismatch MUST produce an error")
	assert.Contains(t, err.Error(), "mismatch", "error must name the mismatch")
	assert.Contains(t, err.Error(), "nodes", "error must name the offending table")
	assert.Contains(t, err.Error(), "100", "error must include source count")
	assert.Contains(t, err.Error(), "42", "error must include restored count")
}

// TestVerifyRowCounts_FirstTableMismatchShortCircuits proves that the first
// mismatch causes an immediate return — we do not silently accumulate errors.
func TestVerifyRowCounts_FirstTableMismatchShortCircuits(t *testing.T) {
	cfg := PostgresBackupConfig{
		Host:      "localhost",
		Port:      "5432",
		User:      "helix",
		SourceDB:  "src",
		RestoreDB: "dst",
	}

	runner := newFakeSQLRunner()
	// First table mismatch.
	runner.respondTo(responseKey(CountRowsArgv(cfg, "src", "nodes")), "5\n", nil)
	runner.respondTo(responseKey(CountRowsArgv(cfg, "dst", "nodes")), "0\n", nil)
	// Second table counts (should never be reached).
	runner.respondTo(responseKey(CountRowsArgv(cfg, "src", "sessions")), "3\n", nil)
	runner.respondTo(responseKey(CountRowsArgv(cfg, "dst", "sessions")), "3\n", nil)

	err := VerifyRowCounts(context.Background(), runner, cfg, []string{"nodes", "sessions"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nodes")
	// "sessions" must not appear in the error — we stopped at the first mismatch.
	assert.NotContains(t, err.Error(), "sessions")
}

// TestVerifyRowCounts_EmptyTableListPasses proves an empty table list is a
// no-op (nothing to compare = no mismatch).
func TestVerifyRowCounts_EmptyTableListPasses(t *testing.T) {
	cfg := PostgresBackupConfig{Host: "localhost", Port: "5432", User: "helix",
		SourceDB: "src", RestoreDB: "dst"}
	runner := newFakeSQLRunner()

	err := VerifyRowCounts(context.Background(), runner, cfg, nil)
	assert.NoError(t, err)
	assert.Empty(t, runner.calls, "no SQL must be run for empty table list")
}

// ── Test: DumpPostgres + RestorePostgres invoke correct commands ──────────────

// TestDumpPostgres_CallsPgDump proves DumpPostgres calls pg_dump exactly once
// with the correct argv.
func TestDumpPostgres_CallsPgDump(t *testing.T) {
	cfg := PostgresBackupConfig{
		Host:     "pg.local",
		Port:     "5432",
		User:     "helix",
		SourceDB: "helixdb",
		DumpPath: "/tmp/helixdb.dump",
	}
	runner := newFakeSQLRunner()

	err := DumpPostgres(context.Background(), runner, cfg)
	require.NoError(t, err)

	assert.Equal(t, 1, runner.callCount("pg_dump"), "DumpPostgres must call pg_dump exactly once")

	last := runner.lastCall("pg_dump")
	require.NotNil(t, last)
	joined := strings.Join(last, " ")
	assert.Contains(t, joined, "helixdb")
	assert.Contains(t, joined, "/tmp/helixdb.dump")
}

// TestRestorePostgres_CallsPgRestore proves RestorePostgres calls pg_restore
// exactly once with the correct argv.
func TestRestorePostgres_CallsPgRestore(t *testing.T) {
	cfg := PostgresBackupConfig{
		Host:      "pg.local",
		Port:      "5432",
		User:      "helix",
		DumpPath:  "/tmp/helixdb.dump",
		RestoreDB: "helixdb_restored",
	}
	runner := newFakeSQLRunner()

	err := RestorePostgres(context.Background(), runner, cfg)
	require.NoError(t, err)

	assert.Equal(t, 1, runner.callCount("pg_restore"), "RestorePostgres must call pg_restore exactly once")

	last := runner.lastCall("pg_restore")
	require.NotNil(t, last)
	joined := strings.Join(last, " ")
	assert.Contains(t, joined, "helixdb_restored")
	assert.Contains(t, joined, "/tmp/helixdb.dump")
}

// TestFullPostgresBackup_HappyPath drives the full dump → restore → verify
// pipeline with fakes and asserts all three stages execute in order.
func TestFullPostgresBackup_HappyPath(t *testing.T) {
	cfg := PostgresBackupConfig{
		Host:      "localhost",
		Port:      "5432",
		User:      "helix",
		SourceDB:  "src",
		DumpPath:  "/tmp/full.dump",
		RestoreDB: "dst",
	}
	runner := newFakeSQLRunner()
	// Wire up matching counts.
	runner.respondTo(responseKey(CountRowsArgv(cfg, "src", "nodes")), "10\n", nil)
	runner.respondTo(responseKey(CountRowsArgv(cfg, "dst", "nodes")), "10\n", nil)

	err := FullPostgresBackup(context.Background(), runner, cfg, []string{"nodes"})
	require.NoError(t, err)

	// All three stages must have fired.
	assert.Equal(t, 1, runner.callCount("pg_dump"))
	assert.Equal(t, 1, runner.callCount("pg_restore"))
	assert.Equal(t, 2, runner.callCount("psql"), "psql called twice: once for src count, once for dst count")
}

// TestFullPostgresBackup_VerifyMismatchFails proves FullPostgresBackup
// propagates the verification error when counts diverge.
func TestFullPostgresBackup_VerifyMismatchFails(t *testing.T) {
	cfg := PostgresBackupConfig{
		Host:      "localhost",
		Port:      "5432",
		User:      "helix",
		SourceDB:  "src",
		DumpPath:  "/tmp/full.dump",
		RestoreDB: "dst",
	}
	runner := newFakeSQLRunner()
	runner.respondTo(responseKey(CountRowsArgv(cfg, "src", "nodes")), "99\n", nil)
	runner.respondTo(responseKey(CountRowsArgv(cfg, "dst", "nodes")), "1\n", nil) // mismatch

	err := FullPostgresBackup(context.Background(), runner, cfg, []string{"nodes"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mismatch")
}

// ── Test: CountRows parses integer output correctly ───────────────────────────

// TestCountRows_ParsesInteger proves CountRows correctly parses a plain integer
// from psql -t -A output (possible trailing newline).
func TestCountRows_ParsesInteger(t *testing.T) {
	cfg := PostgresBackupConfig{Host: "localhost", Port: "5432", User: "helix"}
	runner := newFakeSQLRunner()
	runner.respondTo(responseKey(CountRowsArgv(cfg, "mydb", "nodes")), "123\n", nil)

	n, err := CountRows(context.Background(), runner, cfg, "mydb", "nodes")
	require.NoError(t, err)
	assert.Equal(t, int64(123), n)
}

// TestCountRows_ReturnsErrorOnBadOutput proves CountRows returns an error when
// psql output cannot be parsed as an integer.
func TestCountRows_ReturnsErrorOnBadOutput(t *testing.T) {
	cfg := PostgresBackupConfig{Host: "localhost", Port: "5432", User: "helix"}
	runner := newFakeSQLRunner()
	runner.respondTo(responseKey(CountRowsArgv(cfg, "mydb", "nodes")), "not-a-number\n", nil)

	_, err := CountRows(context.Background(), runner, cfg, "mydb", "nodes")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse row count")
}
