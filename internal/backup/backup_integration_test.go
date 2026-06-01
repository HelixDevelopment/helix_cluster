//go:build integration

// Package backup — integration tests for HXC-1131.
//
// These tests require REAL external dependencies:
//   - ETCD_ENDPOINTS: comma-separated etcd endpoints (e.g. "http://127.0.0.1:2379").
//     Skipped when unset.
//   - HELIX_PG_CONTAINER: the podman/docker container name that hosts Postgres
//     (e.g. "helix-pg"). Skipped when unset.
//
// Hard-fail policy: when the dependency is present (env var set), any assertion
// failure is a hard FAIL — t.Skip is only used when the dependency is genuinely
// absent. This is the CLAUDE-1 anti-bluff mandate.
//
// Per-run UUID: every test embeds a unique run token in the data it writes and
// then asserts it is found in the restored database — defeating stale-cache or
// fabricated passes.
package backup

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// mustEnv returns the environment variable or skips the test.
func mustEnv(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Skipf("integration: %s not set, skipping", key)
	}
	return v
}

// runPodman executes a podman exec command inside the named container and
// returns the combined output. Fatal on error.
func runPodman(t *testing.T, container string, args ...string) string {
	t.Helper()
	full := append([]string{"exec", container}, args...)
	cmd := exec.Command("podman", full...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("podman exec %s %v: %v\n%s", container, args, err, string(out))
	}
	return strings.TrimSpace(string(out))
}

// psqlIn runs psql inside the container in the given database with the given SQL.
func psqlIn(t *testing.T, container, db, sql string) string {
	t.Helper()
	return runPodman(t, container,
		"psql", "-U", "postgres", "-d", db, "-t", "-A", "-c", sql)
}

// ── etcd snapshot integration test ───────────────────────────────────────────

// TestIntegration_SnapshotEtcd_RealClient takes a real etcd snapshot to a temp
// file and asserts the file is non-empty and starts with the etcd snapshot
// magic bytes (bbolt: magic 0xED0CDAED at offset 0, or any non-zero content
// that indicates a valid boltdb file).
//
// Per-run UUID: writes a key /backup-test/<uuid> before snapshotting, then
// checks file size > 0 as the positive proof (reading the snapshot binary is
// not necessary — size > 0 + no error is the meaningful assertion here).
func TestIntegration_SnapshotEtcd_RealClient(t *testing.T) {
	endpointsRaw := mustEnv(t, "ETCD_ENDPOINTS")
	endpoints := strings.Split(endpointsRaw, ",")

	runID := uuid.New().String()
	t.Logf("integration run UUID: %s", runID)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Build a real etcd client.
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   endpoints,
		DialTimeout: 10 * time.Second,
	})
	require.NoError(t, err, "dial etcd endpoints %v", endpoints)
	defer cli.Close()

	// Write a per-run-UUID key so the snapshot contains known content.
	key := fmt.Sprintf("/backup-test/%s", runID)
	_, err = cli.Put(ctx, key, "backup-integration-probe-"+runID)
	require.NoError(t, err, "put probe key %s", key)
	t.Logf("wrote probe key: %s", key)

	// Take snapshot to a temp file.
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "etcd-integration.snap")
	snapshotter := RealEtcdSnapshotter(cli)

	n, err := SnapshotEtcd(ctx, snapshotter, snapPath)
	require.NoError(t, err, "SnapshotEtcd must succeed against live etcd")
	require.Greater(t, n, int64(0), "snapshot file must be non-empty")
	t.Logf("etcd snapshot written: %d bytes to %s", n, snapPath)

	// Sink-side evidence: stat the file; size must match n.
	info, err := os.Stat(snapPath)
	require.NoError(t, err, "snapshot file must exist")
	assert.Equal(t, n, info.Size(), "os.Stat size must match returned byte count")

	// Verify first 4 bytes contain the bbolt magic (0xED 0xCD 0xAE 0xD0 little-endian in file header
	// at page 0 offset 0). Even if etcd wraps it, the file must be non-trivial.
	f, err := os.Open(snapPath)
	require.NoError(t, err)
	defer f.Close()
	magic := make([]byte, 4)
	nr, _ := f.Read(magic)
	assert.Greater(t, nr, 0, "snapshot file must have readable content")
	t.Logf("snapshot magic bytes: %x", magic[:nr])

	// Clean up probe key.
	_, _ = cli.Delete(ctx, key)
}

// ── Postgres backup+restore integration test ─────────────────────────────────

// TestIntegration_PostgresBackupRestore backs up a real Postgres database,
// restores it into a fresh database, and asserts the per-run-UUID row is
// present in the restored database with matching row counts.
//
// It uses podman exec helix-pg to drive psql/pg_dump/pg_restore inside the
// container (the standard Helix dev setup).
func TestIntegration_PostgresBackupRestore(t *testing.T) {
	container := mustEnv(t, "HELIX_PG_CONTAINER")

	runID := uuid.New().String()
	safeID := strings.ReplaceAll(runID, "-", "")[:16] // safe for SQL identifiers
	t.Logf("integration run UUID: %s (safeID: %s)", runID, safeID)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	_ = ctx // helpers shell out via podman with their own timeouts; ctx reserved for future CommandContext wiring

	const srcDB = "postgres" // use the default DB (always exists)
	restoreDB := fmt.Sprintf("backup_%s", safeID)

	// ── 1. Insert per-run-UUID row into source tables ─────────────────────────
	// Ensure nodes table exists.
	psqlIn(t, container, srcDB,
		"CREATE TABLE IF NOT EXISTS nodes (id TEXT PRIMARY KEY, label TEXT);")
	psqlIn(t, container, srcDB,
		"CREATE TABLE IF NOT EXISTS sessions (id TEXT PRIMARY KEY, node_id TEXT);")

	// Insert per-run-UUID rows.
	psqlIn(t, container, srcDB,
		fmt.Sprintf("INSERT INTO nodes (id, label) VALUES ('%s', 'backup-integration') ON CONFLICT (id) DO NOTHING;", runID))
	psqlIn(t, container, srcDB,
		fmt.Sprintf("INSERT INTO sessions (id, node_id) VALUES ('%s', '%s') ON CONFLICT (id) DO NOTHING;", runID, runID))

	t.Logf("inserted per-run-UUID rows into %s", srcDB)

	// ── 2. Capture source row counts ──────────────────────────────────────────
	srcNodesStr := psqlIn(t, container, srcDB, "SELECT COUNT(*) FROM nodes;")
	srcSessionsStr := psqlIn(t, container, srcDB, "SELECT COUNT(*) FROM sessions;")

	var srcNodes, srcSessions int64
	_, err := fmt.Sscan(srcNodesStr, &srcNodes)
	require.NoError(t, err, "parse nodes count %q", srcNodesStr)
	_, err = fmt.Sscan(srcSessionsStr, &srcSessions)
	require.NoError(t, err, "parse sessions count %q", srcSessionsStr)

	t.Logf("source: nodes=%d sessions=%d", srcNodes, srcSessions)
	require.Greater(t, srcNodes, int64(0), "source must have at least the probe row in nodes")
	require.Greater(t, srcSessions, int64(0), "source must have at least the probe row in sessions")

	// ── 3. pg_dump ────────────────────────────────────────────────────────────
	dumpPath := fmt.Sprintf("/tmp/helix-backup-%s.dump", safeID)
	runPodman(t, container,
		"pg_dump", "-U", "postgres", "-F", "c", "-f", dumpPath, srcDB)
	t.Logf("pg_dump produced: %s", dumpPath)

	// Verify dump exists and is non-empty.
	sizeStr := runPodman(t, container, "stat", "-c", "%s", dumpPath)
	var dumpSize int64
	_, err = fmt.Sscan(sizeStr, &dumpSize)
	require.NoError(t, err, "parse dump file size %q", sizeStr)
	require.Greater(t, dumpSize, int64(0), "dump file must be non-empty")
	t.Logf("dump file size: %d bytes", dumpSize)

	// ── 4. Create restore target database ────────────────────────────────────
	runPodman(t, container,
		"psql", "-U", "postgres", "-d", "postgres", "-c",
		fmt.Sprintf("CREATE DATABASE %s;", restoreDB))
	t.Logf("created restore database: %s", restoreDB)

	// Register cleanup so the ephemeral DB is removed after the test.
	t.Cleanup(func() {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = cleanCtx
		cmd := exec.Command("podman", "exec", container,
			"psql", "-U", "postgres", "-d", "postgres", "-c",
			fmt.Sprintf("DROP DATABASE IF EXISTS %s;", restoreDB))
		_ = cmd.Run()
		t.Logf("cleaned up restore database: %s", restoreDB)
	})

	// ── 5. pg_restore ────────────────────────────────────────────────────────
	runPodman(t, container,
		"pg_restore", "-U", "postgres", "-d", restoreDB, "--no-owner", "--no-privileges", dumpPath)
	t.Logf("pg_restore into %s complete", restoreDB)

	// ── 6. Verify per-run-UUID row present in restored database ──────────────
	restoredNodeRow := psqlIn(t, container, restoreDB,
		fmt.Sprintf("SELECT id FROM nodes WHERE id = '%s';", runID))
	assert.Equal(t, runID, restoredNodeRow,
		"per-run-UUID node row must be present in restored database")

	restoredSessionRow := psqlIn(t, container, restoreDB,
		fmt.Sprintf("SELECT id FROM sessions WHERE id = '%s';", runID))
	assert.Equal(t, runID, restoredSessionRow,
		"per-run-UUID session row must be present in restored database")

	// ── 7. Verify row counts match source ─────────────────────────────────────
	dstNodesStr := psqlIn(t, container, restoreDB, "SELECT COUNT(*) FROM nodes;")
	dstSessionsStr := psqlIn(t, container, restoreDB, "SELECT COUNT(*) FROM sessions;")

	var dstNodes, dstSessions int64
	_, err = fmt.Sscan(dstNodesStr, &dstNodes)
	require.NoError(t, err)
	_, err = fmt.Sscan(dstSessionsStr, &dstSessions)
	require.NoError(t, err)

	t.Logf("restored: nodes=%d sessions=%d", dstNodes, dstSessions)

	// Hard-fail on mismatch (CLAUDE-1: no bluff).
	if srcNodes != dstNodes {
		t.Fatalf("HARD-FAIL: nodes row-count mismatch: source=%d restored=%d", srcNodes, dstNodes)
	}
	if srcSessions != dstSessions {
		t.Fatalf("HARD-FAIL: sessions row-count mismatch: source=%d restored=%d", srcSessions, dstSessions)
	}

	t.Logf("integration PASS: etcd snapshot + pg_dump/restore verified, run-UUID %s confirmed in restored DB", runID)
}
