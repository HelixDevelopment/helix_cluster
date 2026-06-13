package main

import (
	"context"
	"net"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// startServer runs the service on an ephemeral 127.0.0.1 port and returns the
// bound address plus a stop func that cancels run and waits for it to return.
// It fails the test if run() errors or if the listener does not come up.
func startServer(t *testing.T) (addr string, stop func()) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	addrCh := make(chan string, 1)
	runErr := make(chan error, 1)

	cfg := Config{Addr: "127.0.0.1:0", GracefulTimeout: 5 * time.Second}
	go func() {
		runErr <- run(ctx, cfg, func(a net.Addr) { addrCh <- a.String() })
	}()

	select {
	case a := <-addrCh:
		addr = a
	case err := <-runErr:
		t.Fatalf("run returned before ready: %v", err)
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("server did not become ready in time")
	}

	stop = func() {
		cancel()
		select {
		case err := <-runErr:
			require.NoError(t, err, "run must return nil on graceful ctx-cancel")
		case <-time.After(8 * time.Second):
			t.Fatal("run did not return after ctx cancel")
		}
	}
	return addr, stop
}

func dial(t *testing.T, addr string) helixv1.AdvisoryServiceClient {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return helixv1.NewAdvisoryServiceClient(conn)
}

// TestRunServesRealTryLockUnlock proves the wired-up service actually starts,
// binds, and answers real RPCs end-to-end: TryLock grants a lock with a
// non-empty lock ID, a competing TryLock on the same resource is refused, and
// Unlock releases it so a later TryLock succeeds again.
//
// Mutation that kills this: in cmd/helix-advisory/main.go, delete the
// `helixv1.RegisterAdvisoryServiceServer(gs, advisory.NewServer())` line — RPCs
// then fail with codes.Unimplemented and every assertion below breaks.
func TestRunServesRealTryLockUnlock(t *testing.T) {
	addr, stop := startServer(t)
	defer stop()

	client := dial(t, addr)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// First acquisition succeeds with a real lock ID.
	resp1, err := client.TryLock(ctx, &helixv1.TryLockRequest{
		Resource: "db/migration", Holder: "node-a", TtlSeconds: 30,
	})
	require.NoError(t, err)
	require.True(t, resp1.Acquired, "first TryLock must acquire")
	require.NotEmpty(t, resp1.LockId, "granted lock must carry a non-empty ID")

	// Competing acquisition on the same held resource is refused.
	resp2, err := client.TryLock(ctx, &helixv1.TryLockRequest{
		Resource: "db/migration", Holder: "node-b", TtlSeconds: 30,
	})
	require.NoError(t, err)
	assert.False(t, resp2.Acquired, "second TryLock on a held resource must be refused")

	// Releasing by the holder frees the resource.
	rel, err := client.Unlock(ctx, &helixv1.UnlockRequest{
		Resource: "db/migration", Holder: "node-a",
	})
	require.NoError(t, err)
	assert.True(t, rel.Released, "holder Unlock must release the lock")

	// After release, the previously-blocked holder can acquire.
	resp3, err := client.TryLock(ctx, &helixv1.TryLockRequest{
		Resource: "db/migration", Holder: "node-b", TtlSeconds: 30,
	})
	require.NoError(t, err)
	assert.True(t, resp3.Acquired, "TryLock must succeed once the lock is released")
	assert.NotEmpty(t, resp3.LockId)
}

// TestRunRejectsInvalidArgsThroughRealRPC proves the wired service propagates
// the internal validation contract over the wire: an empty holder yields
// codes.InvalidArgument. This is sink-side proof the real handler runs, not a
// stub.
//
// Mutation that kills this: replace advisory.NewServer() with a bare
// &helixv1.UnimplementedAdvisoryServiceServer{} — the code becomes Unimplemented
// instead of InvalidArgument and the assertion fails. (Won't compile as written
// since NewServer is required; the point is the real server is wired.)
func TestRunRejectsInvalidArgsThroughRealRPC(t *testing.T) {
	addr, stop := startServer(t)
	defer stop()

	client := dial(t, addr)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := client.TryLock(ctx, &helixv1.TryLockRequest{
		Resource: "r", Holder: "", TtlSeconds: 10,
	})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err),
		"empty holder must surface InvalidArgument from the real handler")
}

// TestRunGracefulShutdownStopsServing proves ctx cancel triggers a real
// graceful shutdown: run() returns nil AND the port stops accepting new
// connections afterward.
//
// Mutation that kills this: in main.go change the `case <-ctx.Done():` arm to
// `return nil` (skipping gs.GracefulStop()). The server goroutine keeps serving
// on a now-leaked listener; the post-shutdown dial+RPC below would still
// succeed and the "stopped serving" assertion fails. (Also run() would no longer
// drain the serve goroutine.)
func TestRunGracefulShutdownStopsServing(t *testing.T) {
	addr, stop := startServer(t)

	// Prove it serves before shutdown.
	client := dial(t, addr)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	resp, err := client.TryLock(ctx, &helixv1.TryLockRequest{
		Resource: "pre/shutdown", Holder: "h", TtlSeconds: 5,
	})
	cancel()
	require.NoError(t, err)
	require.True(t, resp.Acquired)

	// Trigger graceful shutdown; stop() asserts run() returned nil.
	stop()

	// The port must no longer accept connections / serve RPCs.
	require.Eventually(t, func() bool {
		c, derr := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if derr != nil {
			return true // refused — listener is closed
		}
		_ = c.Close()
		return false
	}, 3*time.Second, 50*time.Millisecond, "port must stop accepting after graceful shutdown")
}

// TestConfigValidateRejectsBadPort proves config validation refuses a
// clearly-bad port before any listener is attempted.
//
// Mutation that kills this: in Validate(), change the range check to
// `if p < 0 || p > 70000` (or delete it) — port 99999 then passes and the
// require.Error fails.
func TestConfigValidateRejectsBadPort(t *testing.T) {
	err := Config{Addr: "127.0.0.1:99999", GracefulTimeout: time.Second}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "out of range")

	// And a non-numeric port is rejected too.
	err = Config{Addr: "127.0.0.1:notaport", GracefulTimeout: time.Second}.Validate()
	require.Error(t, err)
}

// TestConfigValidateRejectsNonPositiveTimeout proves a zero/negative graceful
// timeout is rejected up front.
//
// Mutation that kills this: delete the `if c.GracefulTimeout <= 0` block in
// Validate() — a zero timeout then passes and require.Error fails.
func TestConfigValidateRejectsNonPositiveTimeout(t *testing.T) {
	err := Config{Addr: ":50059", GracefulTimeout: 0}.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "graceful timeout")
}

// TestRunRejectsInvalidConfigBeforeListen proves run() refuses bad config
// without attempting to bind (no listener leaks, fast failure path).
//
// Mutation that kills this: remove the `if err := cfg.Validate(); err != nil`
// guard at the top of run() — net.Listen would be reached and return a
// different (bind) error, so the InvalidArgument-style "out of range" message
// assertion fails.
func TestRunRejectsInvalidConfigBeforeListen(t *testing.T) {
	err := run(context.Background(), Config{Addr: "127.0.0.1:99999", GracefulTimeout: time.Second}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "out of range")
}

// TestLoadConfigDefaultsAndOverrides proves env parsing applies defaults and
// honors overrides with validation.
//
// Mutation that kills this: in LoadConfig, ignore HELIX_ADVISORY_ADDR (drop the
// `cfg.Addr = v` assignment) — the override assertion fails.
func TestLoadConfigDefaultsAndOverrides(t *testing.T) {
	t.Setenv("HELIX_ADVISORY_ADDR", "")
	t.Setenv("HELIX_ADVISORY_GRACEFUL_TIMEOUT", "")
	cfg, err := LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, defaultAddr, cfg.Addr)
	assert.Equal(t, defaultGracefulTimeout, cfg.GracefulTimeout)

	t.Setenv("HELIX_ADVISORY_ADDR", "127.0.0.1:0")
	t.Setenv("HELIX_ADVISORY_GRACEFUL_TIMEOUT", "2s")
	cfg, err = LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:0", cfg.Addr)
	assert.Equal(t, 2*time.Second, cfg.GracefulTimeout)

	// Bad duration is rejected.
	t.Setenv("HELIX_ADVISORY_GRACEFUL_TIMEOUT", "nope")
	_, err = LoadConfig()
	require.Error(t, err)
}
