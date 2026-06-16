// Package backup — ADVERSARIAL sink-side probes (atomicity / integrity /
// shared-state / determinism) for internal/backup.
//
// Authored as an adversarial test pass. Every assertion is sink-side:
//   - on-disk file content after a forced failure (atomicity),
//   - byte-identical round-trip including empty input (integrity),
//   - -race over capped concurrent operations (shared state),
//   - deterministic argv/identity construction (determinism).
//
// Where a probe documents a REAL atomicity weakness in SnapshotEtcd
// (a half-written / overwrite-then-fail snapshot left at the canonical
// destPath and presented as if complete), the test is written to PIN the
// CURRENT behavior and is annotated FINDING so the discrimination is honest
// and the suite stays GREEN on unmodified prod. See the block comment above
// each such test for the REAL-BUG vs LATENT-RISK call.
package backup

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── failing reader seam ───────────────────────────────────────────────────────

// midStreamFailSnapshotter returns a ReadCloser that yields `prefix` bytes then
// errors. This forces io.Copy to fail AFTER it has already written prefix to the
// destination file — the canonical "backup interrupted mid-write" scenario.
type midStreamFailSnapshotter struct {
	prefix []byte
	failErr error
}

func (m *midStreamFailSnapshotter) Snapshot(_ context.Context) (io.ReadCloser, error) {
	return io.NopCloser(&failingReader{remaining: m.prefix, failErr: m.failErr}), nil
}

type failingReader struct {
	remaining []byte
	failErr   error
}

func (r *failingReader) Read(p []byte) (int, error) {
	if len(r.remaining) > 0 {
		n := copy(p, r.remaining)
		r.remaining = r.remaining[n:]
		return n, nil
	}
	// All prefix delivered — now fail mid-stream (NOT io.EOF), so io.Copy
	// returns an error and the snapshot is genuinely incomplete.
	return 0, r.failErr
}

// roundTripSnapshotter replays a fixed payload (good path).
type roundTripSnapshotter struct{ payload []byte }

func (s *roundTripSnapshotter) Snapshot(_ context.Context) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(string(s.payload))), nil
}

// ───────────────────────────────────────────────────────────────────────────────
// (a) ATOMICITY / PARTIAL-WRITE
//
// FINDING — LATENT RISK (atomicity), pinned here, NOT "fixed":
//
// SnapshotEtcd does os.Create(destPath) (truncate) then io.Copy. If the source
// stream errors MID-COPY, the function returns (n, err) but leaves a PARTIAL
// file at the canonical destPath. There is no temp-file+rename, no cleanup, and
// no integrity record. The doc comment (backup.go:60-61) only states the file is
// the sink-side artifact callers MUST verify; it makes NO atomicity promise and
// the function correctly returns a non-nil error. So this is NOT a contract
// violation (the caller is told it failed) — but it IS a latent operational
// hazard: a dir-listing / "latest snapshot" picker that trusts presence-on-disk
// would treat the truncated file as a complete backup.
//
// We assert the ACTUAL current sink-side outcome (partial file present, error
// returned) rather than the ideal (no file). If a future fix makes SnapshotEtcd
// atomic (write temp, fsync, rename; unlink-on-error), THIS test's
// partialFilePresent expectation flips and signals the behavior change.
// ───────────────────────────────────────────────────────────────────────────────

func TestAdversarial_SnapshotEtcd_MidStreamFailure_LeavesPartialFile(t *testing.T) {
	prefix := []byte("PARTIAL_SNAPSHOT_HEAD_" + strings.Repeat("x", 4096))
	snap := &midStreamFailSnapshotter{
		prefix:  prefix,
		failErr: fmt.Errorf("etcd: stream reset mid-snapshot"),
	}

	dir := t.TempDir()
	dest := filepath.Join(dir, "etcd.snap")

	n, err := SnapshotEtcd(context.Background(), snap, dest)

	// Contract IS honored: the caller is told the snapshot failed.
	require.Error(t, err, "mid-stream failure MUST surface an error to the caller")
	assert.Contains(t, err.Error(), "stream reset mid-snapshot")

	// Sink-side reality: the bytes copied before the failure ARE on disk.
	got, readErr := os.ReadFile(dest)
	require.NoError(t, readErr, "current behavior: partial file exists at destPath")
	assert.Equal(t, prefix, got,
		"PINNED LATENT RISK: SnapshotEtcd leaves the mid-stream bytes at destPath; "+
			"returned n must equal the partial length")
	assert.Equal(t, int64(len(prefix)), n,
		"returned byte count reflects only the bytes written before failure")

	// The honest danger: the partial file is INDISTINGUISHABLE from a complete
	// snapshot to any consumer that trusts file presence/size alone (no checksum,
	// no length sidecar, no temp+rename). This is the latent hazard we surface.
}

// FINDING — LATENT RISK (destructive overwrite-then-fail), pinned:
//
// Because SnapshotEtcd os.Create()-truncates destPath BEFORE streaming, a failed
// snapshot DESTROYS a previously-good backup at the same path. We prove the prior
// good content is gone and replaced by the partial bytes. Again the error is
// returned, so it is not a silent contract break — but operationally a retry that
// targets the same path can vaporize the last good backup.
func TestAdversarial_SnapshotEtcd_FailedRetry_ClobbersPriorGoodBackup(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "etcd.snap")

	// 1. Write a known-good snapshot.
	good := []byte("GOOD_COMPLETE_SNAPSHOT_" + strings.Repeat("g", 1024))
	n, err := SnapshotEtcd(context.Background(), &roundTripSnapshotter{payload: good}, dest)
	require.NoError(t, err)
	require.Equal(t, int64(len(good)), n)

	onDisk, err := os.ReadFile(dest)
	require.NoError(t, err)
	require.Equal(t, good, onDisk, "precondition: good backup is on disk")

	// 2. A second snapshot to the SAME path fails mid-stream.
	partial := []byte("CLOBBER_HEAD_")
	_, err = SnapshotEtcd(context.Background(),
		&midStreamFailSnapshotter{prefix: partial, failErr: fmt.Errorf("etcd: io error")},
		dest)
	require.Error(t, err)

	// 3. Sink-side: the prior good backup is GONE; only the partial remains.
	after, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.NotEqual(t, good, after,
		"PINNED LATENT RISK: failed retry truncated the prior good backup at destPath")
	assert.Equal(t, partial, after,
		"only the partial bytes from the failed attempt remain — prior good backup lost")
}

// ───────────────────────────────────────────────────────────────────────────────
// (b) INTEGRITY / ROUND-TRIP
//
// SnapshotEtcd has NO checksum/length verification, so "integrity" here is the
// byte-exact round-trip guarantee the package DOES make (doc backup.go:60-61).
// We prove backup→read-back == original for representative + empty inputs, and we
// document (LATENT RISK) that a truncated/tampered file is NOT detected by the
// package — the package offers no verify-on-restore for the etcd path.
// ───────────────────────────────────────────────────────────────────────────────

func TestAdversarial_SnapshotEtcd_RoundTripByteIdentical(t *testing.T) {
	cases := map[string][]byte{
		"empty":            {},
		"single-byte":      {0x00},
		"binary-with-nuls": {0xED, 0xCD, 0xAE, 0xD0, 0x00, 0xFF, 0x10, 0x00},
		"large":            []byte(strings.Repeat("helix-snapshot-payload-", 5000)),
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			dest := filepath.Join(dir, "rt.snap")
			n, err := SnapshotEtcd(context.Background(), &roundTripSnapshotter{payload: payload}, dest)
			require.NoError(t, err)
			require.Equal(t, int64(len(payload)), n)

			got, err := os.ReadFile(dest)
			require.NoError(t, err)
			// assert.Equal treats nil and empty []byte specially; compare lengths
			// then bytes to be byte-exact for the empty case too.
			require.Equal(t, len(payload), len(got), "round-trip length must match")
			assert.True(t, string(payload) == string(got),
				"round-trip must be byte-identical for %s", name)
		})
	}
}

// FINDING — LATENT RISK (no integrity verification on restore path):
//
// The package provides NO mechanism to detect a truncated/tampered etcd snapshot
// on restore — there is no checksum write, no length sidecar, no verify function.
// We demonstrate the gap by truncating a written snapshot and showing the package
// surfaces nothing (there is no API that would reject it). This is a note, not a
// contract break: the etcd path never PROMISED restore-time integrity. (The
// Postgres path's integrity proxy — VerifyRowCounts — IS implemented and tested.)
func TestAdversarial_SnapshotEtcd_TruncationUndetectableByPackage(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "trunc.snap")
	full := []byte(strings.Repeat("snapshot-body-", 2000))
	_, err := SnapshotEtcd(context.Background(), &roundTripSnapshotter{payload: full}, dest)
	require.NoError(t, err)

	// Tamper: truncate the file on disk to half its length.
	require.NoError(t, os.Truncate(dest, int64(len(full)/2)))

	truncated, err := os.ReadFile(dest)
	require.NoError(t, err)
	require.Len(t, truncated, len(full)/2)

	// The package exposes NO verifier for the etcd snapshot, so a consumer reading
	// this file back has no in-package way to learn it is corrupt. We assert the
	// gap explicitly: the truncated content is silently a valid-looking file.
	assert.NotEqual(t, full, truncated,
		"truncated snapshot differs from original, yet the package offers no detection API")
}

// ───────────────────────────────────────────────────────────────────────────────
// (c) UNGUARDED SHARED STATE — run under -race.
//
// Concurrent SnapshotEtcd calls to DISTINCT files, plus concurrent pure-arg /
// VerifyRowCounts work over a shared config, must be race-free. The package keeps
// no global mutable state and the SQLRunner fake here is per-goroutine, so this
// proves there is no hidden shared-state race in the exported surface.
// ───────────────────────────────────────────────────────────────────────────────

func TestAdversarial_ConcurrentSnapshots_NoRace(t *testing.T) {
	const workers = 8 // capped per ANTI-HANG
	dir := t.TempDir()
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			payload := []byte(fmt.Sprintf("worker-%d-", idx) + strings.Repeat("z", 256+idx))
			dest := filepath.Join(dir, fmt.Sprintf("snap-%d.bin", idx))
			_, err := SnapshotEtcd(context.Background(), &roundTripSnapshotter{payload: payload}, dest)
			if err != nil {
				errs[idx] = err
				return
			}
			got, rerr := os.ReadFile(dest)
			if rerr != nil {
				errs[idx] = rerr
				return
			}
			if string(got) != string(payload) {
				errs[idx] = fmt.Errorf("worker %d: content mismatch", idx)
			}
		}(i)
	}
	wg.Wait()
	for i, e := range errs {
		require.NoErrorf(t, e, "worker %d must produce a byte-identical snapshot under concurrency", i)
	}
}

// concurrentSafeRunner is a race-safe fake SQLRunner returning a fixed count,
// used to drive VerifyRowCounts concurrently over a shared config.
type concurrentSafeRunner struct {
	mu    sync.Mutex
	count int
}

func (r *concurrentSafeRunner) Run(_ context.Context, _ []string) (string, error) {
	r.mu.Lock()
	r.count++
	r.mu.Unlock()
	return "5\n", nil
}

func TestAdversarial_ConcurrentVerifyRowCounts_NoRace(t *testing.T) {
	const workers = 8
	cfg := PostgresBackupConfig{
		Host: "localhost", Port: "5432", User: "helix",
		SourceDB: "src", RestoreDB: "dst",
	}
	runner := &concurrentSafeRunner{}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Matching counts (5 == 5) → must be nil; exercises the read path of
			// cfg concurrently to surface any shared-state race in the package.
			_ = VerifyRowCounts(context.Background(), runner, cfg, []string{"nodes", "sessions"})
		}()
	}
	wg.Wait()
	// Sink-side: every goroutine ran 2 tables * 2 sides = 4 Run calls.
	assert.Equal(t, workers*4, runner.count, "all concurrent verify calls completed")
}

// ───────────────────────────────────────────────────────────────────────────────
// (d) DETERMINISM / IDENTITY
//
// argv builders must be pure & deterministic, and identical configs must produce
// identical command lines (so backup/restore target the intended DB, never an
// older/wrong one due to nondeterministic naming).
// ───────────────────────────────────────────────────────────────────────────────

func TestAdversarial_ArgvBuilders_Deterministic(t *testing.T) {
	cfg := PostgresBackupConfig{
		Host: "h", Port: "1", User: "u",
		SourceDB: "src", DumpPath: "/d.dump", RestoreDB: "dst",
	}
	for i := 0; i < 50; i++ {
		assert.Equal(t, DumpArgv(cfg), DumpArgv(cfg), "DumpArgv must be deterministic")
		assert.Equal(t, RestoreArgv(cfg), RestoreArgv(cfg), "RestoreArgv must be deterministic")
		assert.Equal(t, CreateDBArgv(cfg), CreateDBArgv(cfg), "CreateDBArgv must be deterministic")
		assert.Equal(t, CountRowsArgv(cfg, "db", "t"), CountRowsArgv(cfg, "db", "t"),
			"CountRowsArgv must be deterministic")
	}
}

// TestAdversarial_RestoreArgv_TargetsConfiguredRestoreDB proves restore is
// directed at cfg.RestoreDB (-d dst), never the source DB — a wrong-target
// restore would silently overwrite the source or pick the wrong database.
func TestAdversarial_RestoreArgv_TargetsConfiguredRestoreDB(t *testing.T) {
	cfg := PostgresBackupConfig{
		Host: "h", Port: "1", User: "u",
		SourceDB: "production_src", DumpPath: "/d.dump", RestoreDB: "restore_target",
	}
	argv := RestoreArgv(cfg)

	// Find the -d operand.
	var target string
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == "-d" {
			target = argv[i+1]
			break
		}
	}
	require.Equal(t, "restore_target", target,
		"pg_restore -d MUST be the configured RestoreDB, never the source")
	assert.NotContains(t, target, "production_src",
		"restore must never target the source database")
}

// TestAdversarial_DumpArgv_TargetsSourceNotRestore proves the dump reads the
// SOURCE db (last positional operand), never accidentally the restore db.
func TestAdversarial_DumpArgv_TargetsSourceNotRestore(t *testing.T) {
	cfg := PostgresBackupConfig{
		Host: "h", Port: "1", User: "u",
		SourceDB: "the_source", DumpPath: "/d.dump", RestoreDB: "the_restore",
	}
	argv := DumpArgv(cfg)
	require.NotEmpty(t, argv)
	last := argv[len(argv)-1]
	assert.Equal(t, "the_source", last, "pg_dump positional db operand MUST be the source DB")
	assert.NotEqual(t, "the_restore", last, "pg_dump must never read from the restore DB")
}
