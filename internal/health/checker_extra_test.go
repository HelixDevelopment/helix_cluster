package health

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withStatfs temporarily swaps the package statfsFunc for a stub and restores it
// on cleanup. This lets DiskCheck be exercised deterministically without relying
// on the real filesystem's free space.
func withStatfs(t *testing.T, fn func(path string, buf *syscallStatfs) error) {
	t.Helper()
	orig := statfsFunc
	statfsFunc = fn
	t.Cleanup(func() { statfsFunc = orig })
}

// TestDiskCheck_Healthy proves a disk well under threshold reports Healthy.
// Mutation: invert the `used >= c.Threshold` comparison in DiskCheck.Check —
// this returns Critical and the test fails.
func TestDiskCheck_Healthy(t *testing.T) {
	// 1000 blocks total, 900 available -> 10% used, threshold 90%.
	withStatfs(t, func(path string, buf *syscallStatfs) error {
		buf.Bsize = 4096
		buf.Blocks = 1000
		buf.Bavail = 900
		return nil
	})
	c := &DiskCheck{Path: "/", Threshold: 0.9}
	st, err := c.Check(context.Background())
	require.NoError(t, err)
	assert.Equal(t, Healthy, st)
}

// TestDiskCheck_Critical proves usage at/above threshold reports Critical with a
// descriptive error. Mutation: change the Critical branch to return Healthy —
// the status assertion fails.
func TestDiskCheck_Critical(t *testing.T) {
	// 1000 blocks total, 50 available -> 95% used, threshold 90%.
	withStatfs(t, func(path string, buf *syscallStatfs) error {
		buf.Bsize = 4096
		buf.Blocks = 1000
		buf.Bavail = 50
		return nil
	})
	c := &DiskCheck{Path: "/", Threshold: 0.9}
	st, err := c.Check(context.Background())
	require.Error(t, err)
	assert.Equal(t, Critical, st)
	assert.Contains(t, err.Error(), "exceeds threshold")
}

// TestDiskCheck_Degraded proves the warning band (>=80% of threshold but below
// it) reports Degraded. With threshold 0.9 the warning band starts at 0.72.
// Mutation: remove the Degraded band (return Healthy) — this fails.
func TestDiskCheck_Degraded(t *testing.T) {
	// 1000 total, 200 available -> 80% used. 80% >= 0.72 and < 0.9 -> Degraded.
	withStatfs(t, func(path string, buf *syscallStatfs) error {
		buf.Bsize = 4096
		buf.Blocks = 1000
		buf.Bavail = 200
		return nil
	})
	c := &DiskCheck{Path: "/", Threshold: 0.9}
	st, err := c.Check(context.Background())
	require.Error(t, err)
	assert.Equal(t, Degraded, st)
	assert.Contains(t, err.Error(), "approaching threshold")
}

// TestDiskCheck_StatfsError proves a syscall error surfaces as Critical with the
// underlying error. Mutation: change the error branch to return Healthy/nil —
// this fails.
func TestDiskCheck_StatfsError(t *testing.T) {
	sentinel := errors.New("statfs boom")
	withStatfs(t, func(path string, buf *syscallStatfs) error {
		return sentinel
	})
	c := &DiskCheck{Path: "/nope", Threshold: 0.9}
	st, err := c.Check(context.Background())
	assert.Equal(t, Critical, st)
	assert.ErrorIs(t, err, sentinel)
}

// TestDiskCheck_ZeroTotal proves a zero-sized filesystem reports Unknown rather
// than dividing by zero. Mutation: drop the `total == 0` guard — this panics or
// returns NaN-driven Critical, failing the Unknown assertion.
func TestDiskCheck_ZeroTotal(t *testing.T) {
	withStatfs(t, func(path string, buf *syscallStatfs) error {
		buf.Bsize = 4096
		buf.Blocks = 0
		buf.Bavail = 0
		return nil
	})
	c := &DiskCheck{Path: "/", Threshold: 0.9}
	st, err := c.Check(context.Background())
	require.Error(t, err)
	assert.Equal(t, Unknown, st)
	assert.Contains(t, err.Error(), "unable to determine disk size")
}

// TestMemoryCheck_Healthy proves a generous threshold reports Healthy against
// real runtime memory stats. Mutation: invert the threshold comparison — a live
// process with a 99.99% threshold would no longer be Healthy.
func TestMemoryCheck_Healthy(t *testing.T) {
	c := &MemoryCheck{Threshold: 0.9999}
	st, err := c.Check(context.Background())
	require.NoError(t, err)
	assert.Equal(t, Healthy, st)
}

// TestMemoryCheck_Critical proves a near-zero threshold trips Critical against
// real heap usage (HeapAlloc/Sys is always > 0 for a running Go process).
// Mutation: change the Critical branch to return Healthy — this fails.
func TestMemoryCheck_Critical(t *testing.T) {
	c := &MemoryCheck{Threshold: 0.0000001}
	st, err := c.Check(context.Background())
	require.Error(t, err)
	assert.Equal(t, Critical, st)
	assert.Contains(t, err.Error(), "exceeds threshold")
}

// TestHTTPCheck_ConnError proves an unreachable URL maps to Critical with the
// dial error. Mutation: change the Do-error branch to return Healthy/nil — this
// fails. Uses a reserved-for-docs TEST-NET address that will not connect.
func TestHTTPCheck_ConnError(t *testing.T) {
	c := &HTTPCheck{URL: "http://127.0.0.1:1/never", Timeout: 500 * time.Millisecond}
	st, err := c.Check(context.Background())
	require.Error(t, err)
	assert.Equal(t, Critical, st)
}

// TestHTTPCheck_BadURL proves a malformed URL fails at request construction and
// returns Critical. Mutation: change the NewRequestWithContext error branch to
// return Healthy — this fails.
func TestHTTPCheck_BadURL(t *testing.T) {
	c := &HTTPCheck{URL: "://bad url", Timeout: time.Second}
	st, err := c.Check(context.Background())
	require.Error(t, err)
	assert.Equal(t, Critical, st)
}
