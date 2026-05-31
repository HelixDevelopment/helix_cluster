package advisory

import (
	"context"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestLockValidationCodes proves the Lock RPC rejects empty resource/holder with
// a gRPC InvalidArgument status visible to the client (sink-side). Mutation that
// fails this: changing `codes.InvalidArgument` to `codes.OK`/another code, or
// removing either guard, would change the observed status code.
func TestLockValidationCodes(t *testing.T) {
	_, client, cleanup := setupServer(t)
	defer cleanup()
	ctx := context.Background()

	_, err := client.Lock(ctx, &helixv1.LockRequest{Holder: "h"})
	require.Error(t, err)
	st, _ := status.FromError(err)
	assert.Equal(t, codes.InvalidArgument, st.Code())
	assert.Contains(t, st.Message(), "resource is required")

	_, err = client.Lock(ctx, &helixv1.LockRequest{Resource: "r"})
	require.Error(t, err)
	st, _ = status.FromError(err)
	assert.Equal(t, codes.InvalidArgument, st.Code())
	assert.Contains(t, st.Message(), "holder is required")
}

// TestTryLockValidationCodes proves the same validation on the TryLock RPC.
// Mutation that fails this: removing the `req.Resource == ""` guard in TryLock
// would let an empty resource through and return acquired=true with no error.
func TestTryLockValidationCodes(t *testing.T) {
	_, client, cleanup := setupServer(t)
	defer cleanup()
	ctx := context.Background()

	_, err := client.TryLock(ctx, &helixv1.TryLockRequest{Holder: "h"})
	require.Error(t, err)
	st, _ := status.FromError(err)
	assert.Equal(t, codes.InvalidArgument, st.Code())

	_, err = client.TryLock(ctx, &helixv1.TryLockRequest{Resource: "r"})
	require.Error(t, err)
	st, _ = status.FromError(err)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

// TestUnlockValidationAndPreconditionCodes proves Unlock returns InvalidArgument
// for missing fields and FailedPrecondition when unlocking an unheld resource.
// Mutation that fails this: changing the unlock error mapping from
// `codes.FailedPrecondition` to `codes.InvalidArgument` would flip the code on
// the not-found case.
func TestUnlockValidationAndPreconditionCodes(t *testing.T) {
	_, client, cleanup := setupServer(t)
	defer cleanup()
	ctx := context.Background()

	_, err := client.Unlock(ctx, &helixv1.UnlockRequest{Holder: "h"})
	require.Error(t, err)
	st, _ := status.FromError(err)
	assert.Equal(t, codes.InvalidArgument, st.Code())

	// Unlocking a resource that was never locked => FailedPrecondition.
	_, err = client.Unlock(ctx, &helixv1.UnlockRequest{Resource: "never", Holder: "h"})
	require.Error(t, err)
	st, _ = status.FromError(err)
	assert.Equal(t, codes.FailedPrecondition, st.Code())
	assert.Contains(t, st.Message(), "lock not found")
}

// TestLockNegativeTTLFallsBackToDefault proves a negative TtlSeconds does not
// produce an instantly-expired lock at the RPC boundary: after acquiring with
// TtlSeconds=-5, a competing TryLock must still be refused. Mutation that fails
// this: removing the `if ttl <= 0 { ttl = defaultTTL }` guard in tryLock would
// make the entry expire immediately and let the competitor acquire.
func TestLockNegativeTTLFallsBackToDefault(t *testing.T) {
	_, client, cleanup := setupServer(t)
	defer cleanup()
	ctx := context.Background()

	res, err := client.Lock(ctx, &helixv1.LockRequest{
		Resource: "neg", Holder: "a", TtlSeconds: -5,
	})
	require.NoError(t, err)
	require.True(t, res.Acquired)

	other, err := client.TryLock(ctx, &helixv1.TryLockRequest{
		Resource: "neg", Holder: "b", TtlSeconds: 60,
	})
	require.NoError(t, err)
	assert.False(t, other.Acquired, "negative TTL must fall back to defaultTTL, keeping the lock held")
}

// TestWatchLocksPrefixFiltersNonMatching proves WatchLocks only streams events
// whose resource matches the requested prefix; a lock on a non-matching
// resource must NOT be delivered. Mutation that fails this: changing the
// `strings.HasPrefix(resource, prefix)` filter in notify to an unconditional
// match would deliver the non-matching event and trip the assertion.
func TestWatchLocksPrefixFiltersNonMatching(t *testing.T) {
	_, client, cleanup := setupServer(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream, err := client.WatchLocks(ctx, &helixv1.WatchLocksRequest{ResourcePrefix: "match/"})
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)

	// Non-matching resource: must not appear.
	_, err = client.Lock(ctx, &helixv1.LockRequest{Resource: "other/1", Holder: "h", TtlSeconds: 60})
	require.NoError(t, err)
	// Matching resource: must appear.
	_, err = client.Lock(ctx, &helixv1.LockRequest{Resource: "match/1", Holder: "h", TtlSeconds: 60})
	require.NoError(t, err)

	evtCh := make(chan *helixv1.LockEvent, 1)
	errCh := make(chan error, 1)
	go func() {
		evt, err := stream.Recv()
		if err != nil {
			errCh <- err
			return
		}
		evtCh <- evt
	}()

	select {
	case evt := <-evtCh:
		// The first event delivered to this subscriber must be the matching one;
		// "other/1" must have been filtered out entirely.
		assert.Equal(t, "match/1", evt.Resource)
		assert.Equal(t, eventAcquired, evt.EventType)
	case err := <-errCh:
		t.Fatalf("unexpected stream error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for matching lock event")
	}
}
