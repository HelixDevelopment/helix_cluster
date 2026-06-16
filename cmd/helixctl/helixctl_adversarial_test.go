package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/apiv1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// withTimeoutGuard runs fn and fails the test if it does not return within d.
// Every test below that drives a real RPC path is wrapped so a hang surfaces as
// a failure, never an indefinitely blocked CI run.
func withTimeoutGuard(t *testing.T, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("operation did not complete within %s (possible hang)", d)
	}
}

// ---------------------------------------------------------------------------
// resolveAddr: precedence beyond the four cases the existing test covers.
// ---------------------------------------------------------------------------

// TestResolveAddr_EdgeCases probes the corners of the documented contract
// (main.go:57-59): "An explicit non-default flag value always wins; otherwise
// the env var (if set) is used; otherwise the default."
func TestResolveAddr_EdgeCases(t *testing.T) {
	noEnv := func(string) string { return "" }

	t.Run("empty flag and empty env falls back to default", func(t *testing.T) {
		// Programmatic zero-value flag (never set) with no env must not return
		// the empty string — an empty addr would dial nothing.
		got := resolveAddr("", noEnv)
		assert.Equal(t, defaultAddr, got)
		assert.NotEmpty(t, got, "resolved addr must never be empty")
	})

	t.Run("empty flag with env set uses env", func(t *testing.T) {
		got := resolveAddr("", func(k string) string {
			if k == "HELIX_BUILD_ADDR" {
				return "env-host:9000"
			}
			return ""
		})
		assert.Equal(t, "env-host:9000", got)
	})

	t.Run("set-but-empty env is treated as unset", func(t *testing.T) {
		// os.Getenv cannot distinguish unset from set-empty; resolveAddr must
		// not return "" just because the env var is present-but-empty.
		got := resolveAddr(defaultAddr, func(string) string { return "" })
		assert.Equal(t, defaultAddr, got)
	})

	t.Run("DOCUMENTED CONTRACT: explicit default flag is overridden by env", func(t *testing.T) {
		// Cobra cannot distinguish an explicit "--addr localhost:50051" from an
		// unset default, so env wins here. This pins the known, documented
		// limitation so a future change that silently alters it is caught.
		got := resolveAddr(defaultAddr, func(k string) string {
			if k == "HELIX_BUILD_ADDR" {
				return "env-wins:1"
			}
			return ""
		})
		assert.Equal(t, "env-wins:1", got,
			"explicit-default flag does not beat env (documented cobra limitation)")
	})

	t.Run("explicit non-default flag beats env", func(t *testing.T) {
		got := resolveAddr("explicit:7", func(k string) string {
			if k == "HELIX_BUILD_ADDR" {
				return "env:1"
			}
			return ""
		})
		assert.Equal(t, "explicit:7", got)
	})

	t.Run("PIN: flag value is not trimmed or normalised", func(t *testing.T) {
		// Whitespace-padded addrs pass through verbatim; resolveAddr is a
		// precedence resolver, not a validator.
		got := resolveAddr(" host:1 ", noEnv)
		assert.Equal(t, " host:1 ", got)
	})
}

// ---------------------------------------------------------------------------
// Subcommand dispatch + argument guards. These run with NO server, so a
// correctly-guarded CLI must reject malformed invocations BEFORE any dial.
// ---------------------------------------------------------------------------

// execRoot drives the real cobra tree with the given args, capturing output and
// returning the error. Output is redirected so nothing leaks to the test runner.
func execRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs(args)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := root.ExecuteContext(ctx)
	return buf.String(), err
}

// TestArgGuards_RejectWrongArity proves the ExactArgs(1) guards on the
// build-id-taking subcommands reject zero and two args cleanly (a real error,
// no panic) WITHOUT attempting any RPC. A typo'd invocation must fail fast.
func TestArgGuards_RejectWrongArity(t *testing.T) {
	for _, sub := range []string{"status", "cancel", "logs"} {
		sub := sub
		t.Run(sub+" with no build-id errors", func(t *testing.T) {
			withTimeoutGuard(t, 3*time.Second, func() {
				_, err := execRoot(t, "build", sub)
				require.Error(t, err, "%s with 0 args must error", sub)
				assert.Contains(t, strings.ToLower(err.Error()), "arg",
					"error should be an argument-count error, not a dial failure")
			})
		})
		t.Run(sub+" with two build-ids errors", func(t *testing.T) {
			withTimeoutGuard(t, 3*time.Second, func() {
				_, err := execRoot(t, "build", sub, "id-a", "id-b")
				require.Error(t, err, "%s with 2 args must error", sub)
				assert.Contains(t, strings.ToLower(err.Error()), "arg")
			})
		})
	}
}

// TestSubmitGuard_RequiresRepoAndRef proves `build submit` refuses to dispatch a
// SubmitBuild RPC when the required identity flags are absent: the required-flag
// gate fires before any dial. Without it, an empty build request would be sent.
func TestSubmitGuard_RequiresRepoAndRef(t *testing.T) {
	withTimeoutGuard(t, 3*time.Second, func() {
		_, err := execRoot(t, "build", "submit")
		require.Error(t, err, "submit with no flags must error")
		msg := strings.ToLower(err.Error())
		assert.True(t, strings.Contains(msg, "required") &&
			(strings.Contains(msg, "repo-url") || strings.Contains(msg, "ref")),
			"expected a required-flag error naming repo-url/ref, got: %q", err.Error())
	})

	withTimeoutGuard(t, 3*time.Second, func() {
		// repo-url present but ref missing must still be rejected.
		_, err := execRoot(t, "build", "submit", "--repo-url", "https://x/y")
		require.Error(t, err)
		assert.Contains(t, strings.ToLower(err.Error()), "ref",
			"missing --ref must be reported")
	})
}

// TestUnknownSubcommand_Rejected proves a mistyped subcommand is rejected rather
// than silently dispatching to a default action.
func TestUnknownSubcommand_Rejected(t *testing.T) {
	withTimeoutGuard(t, 3*time.Second, func() {
		_, err := execRoot(t, "build", "submt") // typo
		require.Error(t, err, "unknown subcommand must error")
		assert.Contains(t, strings.ToLower(err.Error()), "unknown")
	})
}

// TestBuildGroup_NoDefaultDestructiveAction proves the `build` group with no
// leaf subcommand does not run any RPC-bearing action: it has no RunE, so it
// must print help / return without dialing. This guards against a future change
// that wires a default (possibly destructive) action onto the bare group.
func TestBuildGroup_NoDefaultDestructiveAction(t *testing.T) {
	withTimeoutGuard(t, 3*time.Second, func() {
		out, err := execRoot(t, "build")
		// cobra returns nil and prints usage for a group with no Run and no args.
		require.NoError(t, err)
		assert.Contains(t, out, "Available Commands",
			"bare `build` should show subcommand help, not perform an action")
		// Sink-side: none of the leaf actions' output markers may appear.
		assert.NotContains(t, out, "build submitted:")
		assert.NotContains(t, out, "cancelled=")
	})
}

// ---------------------------------------------------------------------------
// Output formatting: hostile / edge input must be rendered verbatim and safely.
// ---------------------------------------------------------------------------

// fakeStatusClient returns a canned BuildStatus from GetBuildStatus and panics
// on every other method (those must not be reached by runStatus).
type fakeStatusClient struct {
	helixv1.BuildServiceClient
	status *helixv1.BuildStatus
}

func (f *fakeStatusClient) GetBuildStatus(_ context.Context, _ *helixv1.GetBuildStatusRequest, _ ...grpc.CallOption) (*helixv1.BuildStatus, error) {
	return f.status, nil
}

// TestRunStatus_OmitsEmptyOptionalFields proves runStatus only emits the
// image=/started=/completed= segments when the corresponding fields are
// populated — an unstarted build must not print "started=" with a bogus epoch.
func TestRunStatus_OmitsEmptyOptionalFields(t *testing.T) {
	client := &fakeStatusClient{status: &helixv1.BuildStatus{
		BuildId: "b-1",
		State:   "queued",
		// ImageTag empty, StartedAt/CompletedAt zero.
	}}

	var out bytes.Buffer
	require.NoError(t, runStatus(context.Background(), client, &out, "b-1"))
	s := out.String()
	assert.Contains(t, s, "build b-1: state=queued")
	assert.NotContains(t, s, "image=", "empty image tag must be omitted")
	assert.NotContains(t, s, "started=", "zero StartedAt must be omitted (not epoch 1970)")
	assert.NotContains(t, s, "completed=", "zero CompletedAt must be omitted")
	assert.True(t, strings.HasSuffix(s, "\n"), "status line must be newline-terminated")
}

// TestRunStatus_RendersPopulatedFields proves the optional segments appear when
// populated, with timestamps formatted as RFC3339 UTC.
func TestRunStatus_RendersPopulatedFields(t *testing.T) {
	client := &fakeStatusClient{status: &helixv1.BuildStatus{
		BuildId:     "b-2",
		State:       "succeeded",
		ImageTag:    "registry/img:tag",
		StartedAt:   1_600_000_000,
		CompletedAt: 1_600_000_100,
	}}
	var out bytes.Buffer
	require.NoError(t, runStatus(context.Background(), client, &out, "b-2"))
	s := out.String()
	assert.Contains(t, s, "image=registry/img:tag")
	assert.Contains(t, s, "started=2020-09-13T12:26:40Z")
	assert.Contains(t, s, "completed=2020-09-13T12:28:20Z")
}

// TestRunStatus_HostileBuildIDNotFormatInterpreted proves a build id containing
// printf verbs (%s, %d) is rendered verbatim and never interpreted as a format
// directive (no "%!s(MISSING)" garbage, no panic). The id flows from the
// service response through Fprintf as an ARGUMENT, so this must hold.
func TestRunStatus_HostileBuildIDNotFormatInterpreted(t *testing.T) {
	hostile := "b-%s-%d-%n-100%"
	client := &fakeStatusClient{status: &helixv1.BuildStatus{
		BuildId: hostile,
		State:   "queued",
	}}
	var out bytes.Buffer
	withTimeoutGuard(t, 2*time.Second, func() {
		require.NoError(t, runStatus(context.Background(), client, &out, "ignored"))
	})
	s := out.String()
	assert.Contains(t, s, hostile, "hostile build id must appear verbatim")
	assert.NotContains(t, s, "%!", "no printf error markers may leak into output")
}

// fakeCancelClient returns a canned CancelBuildResponse.
type fakeCancelClient struct {
	helixv1.BuildServiceClient
	resp *helixv1.CancelBuildResponse
}

func (f *fakeCancelClient) CancelBuild(_ context.Context, _ *helixv1.CancelBuildRequest, _ ...grpc.CallOption) (*helixv1.CancelBuildResponse, error) {
	return f.resp, nil
}

// TestRunCancel_HostileBuildIDNotFormatInterpreted proves runCancel echoes the
// INPUT build id (not a response field) verbatim even when it contains printf
// verbs — confirming it is passed as an argument, not a format string.
func TestRunCancel_HostileBuildIDNotFormatInterpreted(t *testing.T) {
	hostile := "id-%s-%w-50%"
	client := &fakeCancelClient{resp: &helixv1.CancelBuildResponse{Cancelled: true}}
	var out bytes.Buffer
	withTimeoutGuard(t, 2*time.Second, func() {
		require.NoError(t, runCancel(context.Background(), client, &out, hostile))
	})
	s := out.String()
	assert.Contains(t, s, "build "+hostile+": cancelled=true")
	assert.NotContains(t, s, "%!")
}

// TestRunCancel_ReportsServiceFalse proves runCancel reports the service's
// actual cancelled=false outcome rather than assuming success.
func TestRunCancel_ReportsServiceFalse(t *testing.T) {
	client := &fakeCancelClient{resp: &helixv1.CancelBuildResponse{Cancelled: false}}
	var out bytes.Buffer
	require.NoError(t, runCancel(context.Background(), client, &out, "b-9"))
	assert.Contains(t, out.String(), "cancelled=false")
}
