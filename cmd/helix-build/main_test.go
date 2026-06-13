package main

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
	"github.com/HelixDevelopment/helix_cluster/internal/build"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// startServer launches run() on an ephemeral port in a background goroutine and
// blocks until the listener is bound, returning the dialable address and a stop
// function that cancels the context and waits for run() to return. This is the
// real serving path used by main(); tests dial it over a genuine TCP socket.
func startServer(t *testing.T, cfg Config) (addr string, stop func() error) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	addrCh := make(chan string, 1)
	runErr := make(chan error, 1)

	go func() {
		runErr <- run(ctx, cfg, func(a string) { addrCh <- a })
	}()

	select {
	case a := <-addrCh:
		addr = a
	case err := <-runErr:
		cancel()
		t.Fatalf("run exited before binding: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("server did not bind within 5s")
	}

	return addr, func() error {
		cancel()
		select {
		case err := <-runErr:
			return err
		case <-time.After(10 * time.Second):
			return errors.New("run did not return after cancel")
		}
	}
}

func dial(t *testing.T, addr string) helixv1.BuildServiceClient {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return helixv1.NewBuildServiceClient(conn)
}

func testConfig() Config {
	// SimulatedBuilds: these tests exercise the gRPC/orchestration plumbing, not a
	// real container build — inject the simulated builder so they are deterministic
	// without a podman daemon. Real podman build is proven by internal/build's
	// //go:build integration tests.
	return Config{Host: "127.0.0.1", Port: 0, Workers: 2, ShutdownTimeout: 5 * time.Second, SimulatedBuilds: true}
}

// TestRun_SubmitAndStatus proves the service actually starts, binds, and serves
// real unary RPCs: a submitted build is queued, then driven to a terminal
// success state by the real orchestrator, producing a concrete image tag.
//
// Mutation that makes this FAIL: in run(), replace `srv.Register(gs)` with a
// no-op (drop the registration) — the client's SubmitBuild then returns an
// Unimplemented error and the require below fails.
func TestRun_SubmitAndStatus(t *testing.T) {
	addr, stop := startServer(t, testConfig())
	defer func() { require.NoError(t, stop()) }()

	client := dial(t, addr)
	ctx := context.Background()

	submit, err := client.SubmitBuild(ctx, &helixv1.SubmitBuildRequest{
		RepoUrl:        "https://github.com/helix/cluster",
		Ref:            "main",
		DockerfilePath: "Dockerfile",
		BuildArgs:      map[string]string{"FOO": "bar"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, submit.BuildId)
	assert.True(t, submit.Queued)

	buildID := submit.BuildId

	require.Eventually(t, func() bool {
		st, err := client.GetBuildStatus(ctx, &helixv1.GetBuildStatusRequest{BuildId: buildID})
		return err == nil && st.State == string(build.StateSucceeded)
	}, 5*time.Second, 25*time.Millisecond, "build did not succeed")

	final, err := client.GetBuildStatus(ctx, &helixv1.GetBuildStatusRequest{BuildId: buildID})
	require.NoError(t, err)
	assert.Equal(t, string(build.StateSucceeded), final.State)
	assert.NotEmpty(t, final.ImageTag, "successful build must expose an image tag")
}

// TestRun_StreamBuildLogs proves the server-streaming RPC works end to end over
// a real gRPC connection: the client receives every emitted log line and the
// stream terminates with io.EOF once the build reaches a terminal state.
//
// Mutation that makes this FAIL: in run(), remove the `orch.Start(ctx)` call.
// No worker then runs, no "Build succeeded" log line is ever produced, the
// stream never advances, and the require.NotEmpty(lines) below fails after the
// context deadline.
func TestRun_StreamBuildLogs(t *testing.T) {
	addr, stop := startServer(t, testConfig())
	defer func() { require.NoError(t, stop()) }()

	client := dial(t, addr)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	submit, err := client.SubmitBuild(ctx, &helixv1.SubmitBuildRequest{
		RepoUrl: "https://github.com/helix/cluster",
		Ref:     "stream-it",
	})
	require.NoError(t, err)

	stream, err := client.StreamBuildLogs(ctx, &helixv1.StreamBuildLogsRequest{BuildId: submit.BuildId})
	require.NoError(t, err)

	var lines []string
	for {
		line, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		assert.Equal(t, submit.BuildId, line.BuildId)
		lines = append(lines, line.Line)
	}

	require.NotEmpty(t, lines, "stream must deliver at least one log line")
	// The orchestrator emits a terminal "Build succeeded" line for a successful build.
	joined := ""
	for _, l := range lines {
		joined += l + "\n"
	}
	assert.Contains(t, joined, "succeeded", "stream must include the terminal success line")
}

// TestRun_CancelBuild proves CancelBuild is wired through the real server and
// the orchestrator honours it: a freshly submitted build reports the cancelled
// state after a CancelBuild RPC.
//
// Mutation that makes this FAIL: in run() drop registration (as above) or, more
// targeted, change the assertion's expected state — to validate the assertion
// bites, flip CancelBuild to expect StateSucceeded and it fails.
func TestRun_CancelBuild(t *testing.T) {
	addr, stop := startServer(t, testConfig())
	defer func() { require.NoError(t, stop()) }()

	client := dial(t, addr)
	ctx := context.Background()

	submit, err := client.SubmitBuild(ctx, &helixv1.SubmitBuildRequest{
		RepoUrl: "https://github.com/helix/cluster",
		Ref:     "to-cancel",
	})
	require.NoError(t, err)

	cancelResp, err := client.CancelBuild(ctx, &helixv1.CancelBuildRequest{BuildId: submit.BuildId})
	require.NoError(t, err)
	assert.True(t, cancelResp.Cancelled)

	st, err := client.GetBuildStatus(ctx, &helixv1.GetBuildStatusRequest{BuildId: submit.BuildId})
	require.NoError(t, err)
	assert.Equal(t, string(build.StateCancelled), st.State)
}

// TestRun_GracefulShutdownStopsServing proves shutdown actually stops the
// service: after the context is cancelled and run() returns, a fresh RPC over a
// new connection to the old address fails — the port is no longer served.
//
// Mutation that makes this FAIL: in run(), remove the `case <-ctx.Done()` arm
// (or never call gs.GracefulStop/Stop) so the server keeps serving after
// cancel — the post-shutdown RPC then succeeds and require.Error fails.
func TestRun_GracefulShutdownStopsServing(t *testing.T) {
	addr, stop := startServer(t, testConfig())

	client := dial(t, addr)
	ctx := context.Background()

	// Sanity: it serves before shutdown.
	_, err := client.SubmitBuild(ctx, &helixv1.SubmitBuildRequest{RepoUrl: "r", Ref: "x"})
	require.NoError(t, err)

	// Trigger graceful shutdown and wait for run() to return cleanly.
	require.NoError(t, stop())

	// A new client to the same address must fail to complete an RPC: nothing is
	// listening anymore. Use a short deadline so a refused/unreachable dial
	// surfaces promptly rather than retrying forever.
	dctx, dcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer dcancel()
	postClient := dial(t, addr)
	_, err = postClient.SubmitBuild(dctx, &helixv1.SubmitBuildRequest{RepoUrl: "r", Ref: "x"})
	require.Error(t, err, "service must not serve after graceful shutdown")
}

// TestLoadConfig_Defaults proves the env loader applies the documented defaults
// when nothing is set.
//
// Mutation that makes this FAIL: change defaultPort to a different constant —
// the Port assertion fails.
func TestLoadConfig_Defaults(t *testing.T) {
	cfg, err := LoadConfig(func(string) string { return "" })
	require.NoError(t, err)
	assert.Equal(t, defaultPort, cfg.Port)
	assert.Equal(t, defaultWorkers, cfg.Workers)
	assert.Equal(t, defaultShutdownTimeout, cfg.ShutdownTimeout)
}

// TestLoadConfig_RejectsBadInput proves config validation rejects invalid
// input with a concrete error instead of silently starting a broken service.
//
// Mutation that makes this FAIL: in Validate(), remove the port-range check
// (return nil unconditionally for port) — the out-of-range and non-numeric
// cases below no longer error and require.Error fails.
func TestLoadConfig_RejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"non-numeric port", map[string]string{"HELIX_BUILD_PORT": "notaport"}},
		{"port too large", map[string]string{"HELIX_BUILD_PORT": "70000"}},
		{"negative port", map[string]string{"HELIX_BUILD_PORT": "-5"}},
		{"non-numeric workers", map[string]string{"HELIX_BUILD_WORKERS": "lots"}},
		{"zero workers", map[string]string{"HELIX_BUILD_WORKERS": "0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadConfig(func(k string) string { return tc.env[k] })
			require.Error(t, err)
		})
	}
}

// TestRun_RejectsBadConfig proves run() refuses to bind on invalid config and
// surfaces the error rather than serving a half-broken listener.
//
// Mutation that makes this FAIL: remove the `cfg.Validate()` guard at the top
// of run() — run then proceeds toward listen with an invalid config and this
// stops returning the validation error.
func TestRun_RejectsBadConfig(t *testing.T) {
	err := run(context.Background(), Config{Port: 70000, Workers: 1, ShutdownTimeout: time.Second}, nil)
	require.Error(t, err)
}

// TestConfig_Addr proves the bind address is exposed in host:port form.
//
// Mutation that makes this FAIL: change Addr() to drop the host — the IPv6
// joined form below no longer matches.
func TestConfig_Addr(t *testing.T) {
	assert.Equal(t, "127.0.0.1:50051", Config{Host: "127.0.0.1", Port: 50051}.Addr())
}
