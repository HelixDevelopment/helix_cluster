package health

import (
	"context"
	"sync"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
)

// fakeWatchStream is a minimal grpc.ServerStreamingServer[helixv1.HealthEvent]
// that captures sent events. It implements the full grpc.ServerStream surface
// with no-ops where the health server never exercises them.
type fakeWatchStream struct {
	ctx  context.Context
	mu   sync.Mutex
	sent []*helixv1.HealthEvent
	// sendErr, if set, is returned by Send to exercise the error path.
	sendErr error
	// onSend is signalled once per successful Send so tests can wait without
	// racing on the internal slice.
	onSend chan struct{}
}

func newFakeWatchStream(ctx context.Context) *fakeWatchStream {
	return &fakeWatchStream{ctx: ctx, onSend: make(chan struct{}, 16)}
}

func (f *fakeWatchStream) Send(e *helixv1.HealthEvent) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	f.mu.Lock()
	f.sent = append(f.sent, e)
	f.mu.Unlock()
	select {
	case f.onSend <- struct{}{}:
	default:
	}
	return nil
}

func (f *fakeWatchStream) events() []*helixv1.HealthEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*helixv1.HealthEvent, len(f.sent))
	copy(out, f.sent)
	return out
}

func (f *fakeWatchStream) Context() context.Context     { return f.ctx }
func (f *fakeWatchStream) SetHeader(metadata.MD) error  { return nil }
func (f *fakeWatchStream) SendHeader(metadata.MD) error { return nil }
func (f *fakeWatchStream) SetTrailer(metadata.MD)       {}
func (f *fakeWatchStream) SendMsg(m interface{}) error  { return nil }
func (f *fakeWatchStream) RecvMsg(m interface{}) error  { return nil }

// TestServer_Watch_InitialSnapshotAndBroadcast proves Watch (1) emits an initial
// snapshot reflecting the aggregator's current status, then (2) forwards events
// broadcast via ReportHealth, then (3) returns when the context is cancelled.
// Mutation: delete the sendCurrentStatus call in Watch — the initial snapshot
// assertion fails. Mutation: remove stream.Send(evt) in the loop — the broadcast
// assertion fails.
func TestServer_Watch_InitialSnapshotAndBroadcast(t *testing.T) {
	agg := NewAggregator()
	agg.RegisterCheck("svc", &mockCheck{status: Healthy})
	agg.RunChecks(context.Background())
	srv := NewServer(agg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newFakeWatchStream(ctx)

	done := make(chan error, 1)
	go func() {
		done <- srv.Watch(&helixv1.WatchRequest{NodeId: "node-7"}, stream)
	}()

	// Wait for the initial snapshot.
	select {
	case <-stream.onSend:
	case <-time.After(2 * time.Second):
		t.Fatal("initial snapshot not sent")
	}
	first := stream.events()
	require.Len(t, first, 1)
	assert.Equal(t, "node-7", first[0].NodeId)
	assert.Equal(t, "healthy", first[0].Status)

	// Trigger a broadcast via ReportHealth and expect it forwarded.
	_, err := srv.ReportHealth(context.Background(), &helixv1.ReportHealthRequest{
		NodeId: "node-9",
		Score:  &helixv1.HealthScore{NodeId: "node-9", Overall: 42},
	})
	require.NoError(t, err)

	select {
	case <-stream.onSend:
	case <-time.After(2 * time.Second):
		t.Fatal("broadcast event not forwarded to watcher")
	}
	evs := stream.events()
	require.GreaterOrEqual(t, len(evs), 2)
	assert.Equal(t, "node-9", evs[len(evs)-1].NodeId)
	require.NotNil(t, evs[len(evs)-1].Score)
	assert.EqualValues(t, 42, evs[len(evs)-1].Score.Overall)

	// Cancel and confirm Watch unwinds with the context error.
	cancel()
	select {
	case werr := <-done:
		assert.ErrorIs(t, werr, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("Watch did not return after context cancellation")
	}
}

// TestServer_Watch_DeregistersWatcher proves Watch removes its channel from the
// watcher set on return, so a later broadcast does not attempt to use a closed
// channel (which would panic) and the watcher count returns to zero. Mutation:
// remove the delete(s.watchers, ch) in the deferred cleanup — broadcast after
// return sends on a closed channel and panics, failing the test.
func TestServer_Watch_DeregistersWatcher(t *testing.T) {
	agg := NewAggregator()
	srv := NewServer(agg)

	ctx, cancel := context.WithCancel(context.Background())
	stream := newFakeWatchStream(ctx)

	done := make(chan error, 1)
	go func() { done <- srv.Watch(&helixv1.WatchRequest{NodeId: "n"}, stream) }()

	// Wait for initial snapshot so the watcher is definitely registered.
	select {
	case <-stream.onSend:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher never registered")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Watch did not return")
	}

	srv.mu.RLock()
	n := len(srv.watchers)
	srv.mu.RUnlock()
	assert.Equal(t, 0, n, "watcher must be deregistered after Watch returns")

	// A broadcast after deregistration must not panic on a closed channel.
	assert.NotPanics(t, func() {
		srv.broadcast(&helixv1.HealthEvent{NodeId: "after"})
	})
}

// TestServer_Watch_InitialSendError proves that if the initial snapshot send
// fails, Watch returns that error and does not block. Mutation: change Watch to
// ignore the sendCurrentStatus error (drop the `if err != nil { return err }`)
// — Watch would proceed and block until timeout, failing this test.
func TestServer_Watch_InitialSendError(t *testing.T) {
	agg := NewAggregator()
	srv := NewServer(agg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newFakeWatchStream(ctx)
	stream.sendErr = assert.AnError

	done := make(chan error, 1)
	go func() { done <- srv.Watch(&helixv1.WatchRequest{NodeId: "n"}, stream) }()

	select {
	case err := <-done:
		require.ErrorIs(t, err, assert.AnError)
	case <-time.After(2 * time.Second):
		t.Fatal("Watch did not return on initial send error")
	}

	srv.mu.RLock()
	n := len(srv.watchers)
	srv.mu.RUnlock()
	assert.Equal(t, 0, n, "watcher must be cleaned up even when initial send fails")
}
