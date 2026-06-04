package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"github.com/HelixDevelopment/helix_cluster/internal/build"
	"github.com/HelixDevelopment/helix_cluster/pkg/build/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// startBuildService launches a REAL in-process BuildService gRPC server on an
// ephemeral port using the simulated builder (deterministic, no podman) and
// returns its dialable address. It mirrors the orchestration wiring of
// cmd/helix-build/run so the CLI is exercised against the genuine service, not a
// mock of the CLI's own client.
func startBuildService(t *testing.T) string {
	t.Helper()

	orch := build.NewOrchestratorWithBuilder(2, cache.NewMemoryCache(), build.NewSimulatedBuilder())
	ctx, cancel := context.WithCancel(context.Background())
	orch.Start(ctx)

	srv := build.NewServer(orch, build.NewWorkerPool())

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	gs := grpc.NewServer()
	srv.Register(gs)

	serveErr := make(chan error, 1)
	go func() { serveErr <- gs.Serve(lis) }()

	t.Cleanup(func() {
		gs.GracefulStop()
		cancel()
		orch.Stop()
		if err := <-serveErr; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Errorf("serve error: %v", err)
		}
	})

	return lis.Addr().String()
}

// dialTestClient connects the CLI's real dial path to the in-process service.
func dialTestClient(t *testing.T, addr string) helixv1.BuildServiceClient {
	t.Helper()
	client, closeConn, err := dialClient(context.Background(), Connection{Addr: addr, Timeout: 10 * time.Second})
	require.NoError(t, err)
	t.Cleanup(func() { _ = closeConn() })
	return client
}

// TestRunSubmit_RealRoundTrip proves `helixctl build submit` performs a real
// SubmitBuild RPC: a non-empty build id is returned by the live service and
// surfaced in the command output.
//
// Mutation that makes this FAIL: in runSubmit, ignore resp.BuildId and print a
// canned literal id — the assertion that output contains the real returned id
// (and the non-empty check) then fails.
func TestRunSubmit_RealRoundTrip(t *testing.T) {
	addr := startBuildService(t)
	client := dialTestClient(t, addr)

	var out bytes.Buffer
	err := runSubmit(context.Background(), client, &out, &helixv1.SubmitBuildRequest{
		RepoUrl:        "https://github.com/helix/cluster",
		Ref:            "main",
		DockerfilePath: "Dockerfile",
		BuildArgs:      map[string]string{"FOO": "bar"},
	})
	require.NoError(t, err)
	assert.Contains(t, out.String(), "build submitted: id=")
	assert.Contains(t, out.String(), "queued=true")
}

// TestRunStatus_ReflectsSubmittedBuild proves `helixctl build status` performs a
// real GetBuildStatus RPC: after submitting via the live service, status
// eventually reports the orchestrator-driven terminal success state for that
// exact build id.
//
// Mutation that makes this FAIL: in runStatus, print a hardcoded state string
// instead of st.State — the eventual "succeeded" assertion no longer holds.
func TestRunStatus_ReflectsSubmittedBuild(t *testing.T) {
	addr := startBuildService(t)
	client := dialTestClient(t, addr)
	ctx := context.Background()

	submit, err := client.SubmitBuild(ctx, &helixv1.SubmitBuildRequest{
		RepoUrl: "https://github.com/helix/cluster",
		Ref:     "status-it",
	})
	require.NoError(t, err)
	require.NotEmpty(t, submit.BuildId)

	require.Eventually(t, func() bool {
		var out bytes.Buffer
		if err := runStatus(ctx, client, &out, submit.BuildId); err != nil {
			return false
		}
		return strings.Contains(out.String(), string(build.StateSucceeded))
	}, 5*time.Second, 25*time.Millisecond, "status never reported the terminal success state")

	var out bytes.Buffer
	require.NoError(t, runStatus(ctx, client, &out, submit.BuildId))
	assert.Contains(t, out.String(), submit.BuildId)
}

// TestRunCancel_RealRoundTrip proves `helixctl build cancel` performs a real
// CancelBuild RPC honoured by the orchestrator: the cancelled build then reports
// the cancelled state via the same live service.
//
// Mutation that makes this FAIL: in runCancel, drop the client.CancelBuild call
// and print cancelled=true unconditionally — the build is never cancelled, so
// the follow-up GetBuildStatus does not report StateCancelled and the assertion
// fails.
func TestRunCancel_RealRoundTrip(t *testing.T) {
	addr := startBuildService(t)
	client := dialTestClient(t, addr)
	ctx := context.Background()

	submit, err := client.SubmitBuild(ctx, &helixv1.SubmitBuildRequest{
		RepoUrl: "https://github.com/helix/cluster",
		Ref:     "cancel-it",
	})
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, runCancel(ctx, client, &out, submit.BuildId))
	assert.Contains(t, out.String(), "cancelled=true")

	st, err := client.GetBuildStatus(ctx, &helixv1.GetBuildStatusRequest{BuildId: submit.BuildId})
	require.NoError(t, err)
	assert.Equal(t, string(build.StateCancelled), st.State)
}

// TestRunLogs_StreamsRealLines proves `helixctl build logs` consumes the real
// StreamBuildLogs server stream end to end: it receives the orchestrator's
// emitted lines (including the terminal success line) and returns nil on EOF.
//
// Mutation that makes this FAIL: in runLogs, break out of the loop before
// printing line.Line (never write to out) — the captured output is empty and the
// NotEmpty / Contains assertions fail.
func TestRunLogs_StreamsRealLines(t *testing.T) {
	addr := startBuildService(t)
	client := dialTestClient(t, addr)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	submit, err := client.SubmitBuild(ctx, &helixv1.SubmitBuildRequest{
		RepoUrl: "https://github.com/helix/cluster",
		Ref:     "logs-it",
	})
	require.NoError(t, err)

	var out bytes.Buffer
	require.NoError(t, runLogs(ctx, client, &out, submit.BuildId))
	require.NotEmpty(t, out.String(), "logs stream must deliver at least one line")
	assert.Contains(t, out.String(), "succeeded", "stream must include the terminal success line")
}

// TestResolveAddr proves the --addr / HELIX_BUILD_ADDR / default precedence.
//
// Mutation that makes this FAIL: in resolveAddr, return flagVal unconditionally
// — the env-override case (flag left at default, env set) then returns the
// default instead of the env value and the assertion fails.
func TestResolveAddr(t *testing.T) {
	cases := []struct {
		name    string
		flagVal string
		env     map[string]string
		want    string
	}{
		{"default when nothing set", defaultAddr, nil, defaultAddr},
		{"env overrides default flag", defaultAddr, map[string]string{"HELIX_BUILD_ADDR": "env:1"}, "env:1"},
		{"explicit flag wins over env", "flag:2", map[string]string{"HELIX_BUILD_ADDR": "env:1"}, "flag:2"},
		{"explicit flag with no env", "flag:3", nil, "flag:3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveAddr(tc.flagVal, func(k string) string { return tc.env[k] })
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestRunSubmit_ErrorsWhenServiceDown proves the CLI surfaces a real RPC failure
// rather than fabricating success: pointed at a closed address, submit returns
// an error.
//
// Mutation that makes this FAIL: in runSubmit, swallow the SubmitBuild error and
// return nil — the require.Error below then fails.
func TestRunSubmit_ErrorsWhenServiceDown(t *testing.T) {
	// Bind then immediately close to obtain an address nothing is listening on.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := lis.Addr().String()
	require.NoError(t, lis.Close())

	client, closeConn, err := dialClient(context.Background(), Connection{Addr: addr, Timeout: time.Second})
	require.NoError(t, err)
	defer func() { _ = closeConn() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var out bytes.Buffer
	err = runSubmit(ctx, client, &out, &helixv1.SubmitBuildRequest{RepoUrl: "r", Ref: "x"})
	require.Error(t, err)
}
