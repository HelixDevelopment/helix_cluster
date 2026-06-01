package build

import (
	"context"
	"sync"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"github.com/HelixDevelopment/helix_cluster/pkg/build/cache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// fakeLogStream implements helixv1.BuildService_StreamBuildLogsServer
// (grpc.ServerStreamingServer[BuildLogLine]) for unit testing StreamBuildLogs
// without a network transport. A real gRPC stream cannot be exercised in a unit
// test (§11.4.27), so this captures sink-side Send calls for assertion.
type fakeLogStream struct {
	grpc.ServerStream // unused methods; never called by StreamBuildLogs
	ctx               context.Context
	mu                sync.Mutex
	sent              []*helixv1.BuildLogLine
	sendErr           error
}

func (f *fakeLogStream) Context() context.Context { return f.ctx }

func (f *fakeLogStream) Send(line *helixv1.BuildLogLine) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, line)
	return nil
}

func (f *fakeLogStream) received() []*helixv1.BuildLogLine {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*helixv1.BuildLogLine, len(f.sent))
	copy(out, f.sent)
	return out
}

// StreamBuildLogs must deliver every log line of a build to the client exactly
// once and terminate when the build reaches a terminal state. This is the core
// end-user-visible behavior of the streaming RPC.
func TestServer_StreamBuildLogs_DeliversAllLinesThenStops(t *testing.T) {
	// Use fake builder so the job completes deterministically without real podman.
	orch := newFakeOrchestrator(1, cache.NewMemoryCache())
	orch.Start(context.Background())
	defer orch.Stop()
	srv := NewServer(orch, NewWorkerPool())

	ctx := context.Background()
	_, err := orch.SubmitBuild(ctx, BuildSpec{ID: "stream-ok", RepoURL: "r", Ref: "main"})
	require.NoError(t, err)

	stream := &fakeLogStream{ctx: ctx}

	done := make(chan error, 1)
	go func() {
		done <- srv.StreamBuildLogs(&helixv1.StreamBuildLogsRequest{BuildId: "stream-ok"}, stream)
	}()

	// The stream must return nil once the build is terminal.
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("StreamBuildLogs did not return after build completed")
	}

	// All emitted log lines must reach the sink, in order, tagged with the build ID.
	final, err := orch.GetBuildStatus(ctx, "stream-ok")
	require.NoError(t, err)
	want := final.GetLogs()
	require.NotEmpty(t, want)

	got := stream.received()
	require.Len(t, got, len(want), "stream must deliver every produced log line exactly once")
	for i := range want {
		assert.Equal(t, want[i], got[i].Line)
		assert.Equal(t, "stream-ok", got[i].BuildId)
	}
}

// When the client context is cancelled, StreamBuildLogs must return nil promptly
// and stop sending, rather than blocking forever.
func TestServer_StreamBuildLogs_ClientCancel(t *testing.T) {
	// 0 workers: job stays queued (never terminal), exercising client-cancel path.
	orch := newFakeOrchestrator(0, cache.NewMemoryCache())
	orch.Start(context.Background())
	defer orch.Stop()
	srv := NewServer(orch, NewWorkerPool())

	_, err := orch.SubmitBuild(context.Background(), BuildSpec{ID: "stream-cancel", RepoURL: "r"})
	require.NoError(t, err)

	streamCtx, cancel := context.WithCancel(context.Background())
	stream := &fakeLogStream{ctx: streamCtx}

	done := make(chan error, 1)
	go func() {
		done <- srv.StreamBuildLogs(&helixv1.StreamBuildLogsRequest{BuildId: "stream-cancel"}, stream)
	}()

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err, "client cancel must yield a clean nil return")
	case <-time.After(2 * time.Second):
		t.Fatal("StreamBuildLogs did not return after client cancel")
	}
}

// Streaming logs for an unknown build must surface the not-found error.
func TestServer_StreamBuildLogs_UnknownBuild(t *testing.T) {
	orch := newFakeOrchestrator(1, cache.NewMemoryCache())
	orch.Start(context.Background())
	defer orch.Stop()
	srv := NewServer(orch, NewWorkerPool())

	stream := &fakeLogStream{ctx: context.Background()}
	err := srv.StreamBuildLogs(&helixv1.StreamBuildLogsRequest{BuildId: "ghost"}, stream)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
