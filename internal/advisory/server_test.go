package advisory

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	helixv1 "github.com/HelixDevelopment/helix_cluster/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func setupServer(t *testing.T) (*Server, helixv1.AdvisoryServiceClient, func()) {
	srv := NewServer()
	lis := newBufListener()
	gs := grpc.NewServer()
	helixv1.RegisterAdvisoryServiceServer(gs, srv)

	go func() {
		if err := gs.Serve(lis); err != nil {
			// expected on close
		}
	}()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(lis.Dialer()),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	client := helixv1.NewAdvisoryServiceClient(conn)

	cleanup := func() {
		conn.Close()
		gs.Stop()
		lis.Close()
	}
	return srv, client, cleanup
}

func TestLockUnlockLifecycle(t *testing.T) {
	_, client, cleanup := setupServer(t)
	defer cleanup()

	ctx := context.Background()
	res, err := client.Lock(ctx, &helixv1.LockRequest{
		Resource:   "res-1",
		Holder:     "holder-a",
		TtlSeconds: 60,
	})
	require.NoError(t, err)
	assert.True(t, res.Acquired)
	assert.NotEmpty(t, res.LockId)

	unres, err := client.Unlock(ctx, &helixv1.UnlockRequest{
		Resource: "res-1",
		Holder:   "holder-a",
	})
	require.NoError(t, err)
	assert.True(t, unres.Released)
}

func TestTryLockSuccessAndFailure(t *testing.T) {
	_, client, cleanup := setupServer(t)
	defer cleanup()

	ctx := context.Background()

	// First holder acquires
	res1, err := client.TryLock(ctx, &helixv1.TryLockRequest{
		Resource:   "res-2",
		Holder:     "holder-x",
		TtlSeconds: 60,
	})
	require.NoError(t, err)
	assert.True(t, res1.Acquired)

	// Second holder fails
	res2, err := client.TryLock(ctx, &helixv1.TryLockRequest{
		Resource:   "res-2",
		Holder:     "holder-y",
		TtlSeconds: 60,
	})
	require.NoError(t, err)
	assert.False(t, res2.Acquired)
	assert.Empty(t, res2.LockId)
}

func TestLockExpiration(t *testing.T) {
	_, client, cleanup := setupServer(t)
	defer cleanup()

	ctx := context.Background()

	// Acquire with 1-second TTL
	res1, err := client.TryLock(ctx, &helixv1.TryLockRequest{
		Resource:   "res-3",
		Holder:     "holder-a",
		TtlSeconds: 1,
	})
	require.NoError(t, err)
	assert.True(t, res1.Acquired)

	// Should fail immediately
	res2, err := client.TryLock(ctx, &helixv1.TryLockRequest{
		Resource:   "res-3",
		Holder:     "holder-b",
		TtlSeconds: 60,
	})
	require.NoError(t, err)
	assert.False(t, res2.Acquired)

	// Wait for expiration
	time.Sleep(2 * time.Second)

	res3, err := client.TryLock(ctx, &helixv1.TryLockRequest{
		Resource:   "res-3",
		Holder:     "holder-b",
		TtlSeconds: 60,
	})
	require.NoError(t, err)
	assert.True(t, res3.Acquired)
}

func TestWatchLocks(t *testing.T) {
	_, client, cleanup := setupServer(t)
	defer cleanup()

	ctx := context.Background()

	stream, err := client.WatchLocks(ctx, &helixv1.WatchLocksRequest{
		ResourcePrefix: "watch-res",
	})
	require.NoError(t, err)

	// Give stream time to establish
	time.Sleep(100 * time.Millisecond)

	// Acquire lock
	_, err = client.Lock(ctx, &helixv1.LockRequest{
		Resource:   "watch-res-1",
		Holder:     "holder-w",
		TtlSeconds: 60,
	})
	require.NoError(t, err)

	eventCh := make(chan *helixv1.LockEvent, 1)
	go func() {
		evt, err := stream.Recv()
		if err == nil {
			eventCh <- evt
		}
	}()

	select {
	case evt := <-eventCh:
		assert.Equal(t, "watch-res-1", evt.Resource)
		assert.Equal(t, "holder-w", evt.Holder)
		assert.Equal(t, eventAcquired, evt.EventType)
		assert.Greater(t, evt.Timestamp, int64(0))
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for lock event")
	}
}

func TestConcurrentLockAcquisition(t *testing.T) {
	_, client, cleanup := setupServer(t)
	defer cleanup()

	ctx := context.Background()
	resource := "concurrent-res"

	var wg sync.WaitGroup
	acquired := make(map[string]bool)
	var mu sync.Mutex

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			res, err := client.TryLock(ctx, &helixv1.TryLockRequest{
				Resource:   resource,
				Holder:     fmt.Sprintf("holder-%d", idx),
				TtlSeconds: 60,
			})
			if err != nil {
				t.Errorf("lock error: %v", err)
				return
			}
			if res.Acquired {
				mu.Lock()
				acquired[fmt.Sprintf("holder-%d", idx)] = true
				mu.Unlock()
			}
		}(i)
	}

	wg.Wait()
	mu.Lock()
	count := len(acquired)
	mu.Unlock()
	assert.Equal(t, 1, count, "exactly one concurrent caller should acquire the lock")
}

func TestOnlyHolderCanUnlock(t *testing.T) {
	_, client, cleanup := setupServer(t)
	defer cleanup()

	ctx := context.Background()

	_, err := client.Lock(ctx, &helixv1.LockRequest{
		Resource:   "res-4",
		Holder:     "holder-a",
		TtlSeconds: 60,
	})
	require.NoError(t, err)

	_, err = client.Unlock(ctx, &helixv1.UnlockRequest{
		Resource: "res-4",
		Holder:   "holder-b",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not own lock")

	unres, err := client.Unlock(ctx, &helixv1.UnlockRequest{
		Resource: "res-4",
		Holder:   "holder-a",
	})
	require.NoError(t, err)
	assert.True(t, unres.Released)
}

func TestLockBlocksUntilAvailable(t *testing.T) {
	_, client, cleanup := setupServer(t)
	defer cleanup()

	ctx := context.Background()

	// First lock
	res1, err := client.Lock(ctx, &helixv1.LockRequest{
		Resource:   "res-5",
		Holder:     "holder-first",
		TtlSeconds: 60,
	})
	require.NoError(t, err)
	assert.True(t, res1.Acquired)

	// Second lock should block
	lockCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()

	_, err = client.Lock(lockCtx, &helixv1.LockRequest{
		Resource:   "res-5",
		Holder:     "holder-second",
		TtlSeconds: 60,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context deadline exceeded")

	// Unlock first
	_, err = client.Unlock(ctx, &helixv1.UnlockRequest{
		Resource: "res-5",
		Holder:   "holder-first",
	})
	require.NoError(t, err)

	// Now second can lock
	res2, err := client.Lock(ctx, &helixv1.LockRequest{
		Resource:   "res-5",
		Holder:     "holder-second",
		TtlSeconds: 60,
	})
	require.NoError(t, err)
	assert.True(t, res2.Acquired)
}

// bufListener is an in-memory gRPC listener for testing.

type bufListener struct {
	addr   string
	ch     chan net.Conn
	closed chan struct{}
}

func newBufListener() *bufListener {
	return &bufListener{
		addr:   "bufnet",
		ch:     make(chan net.Conn),
		closed: make(chan struct{}),
	}
}

func (l *bufListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.closed:
		return nil, io.EOF
	}
}

func (l *bufListener) Addr() net.Addr { return &bufAddr{addr: l.addr} }

func (l *bufListener) Close() error {
	select {
	case <-l.closed:
		return nil
	default:
		close(l.closed)
		return nil
	}
}

func (l *bufListener) Dialer() func(context.Context, string) (net.Conn, error) {
	return func(ctx context.Context, addr string) (net.Conn, error) {
		clientR, serverW := io.Pipe()
		serverR, clientW := io.Pipe()
		clientConn := &pipeConn{reader: clientR, writer: clientW}
		serverConn := &pipeConn{reader: serverR, writer: serverW}
		select {
		case l.ch <- serverConn:
			return clientConn, nil
		case <-l.closed:
			return nil, io.EOF
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

type bufAddr struct {
	addr string
}

func (a *bufAddr) Network() string { return "bufnet" }
func (a *bufAddr) String() string  { return a.addr }

type pipeConn struct {
	reader *io.PipeReader
	writer *io.PipeWriter
}

func (c *pipeConn) Read(b []byte) (int, error)  { return c.reader.Read(b) }
func (c *pipeConn) Write(b []byte) (int, error) { return c.writer.Write(b) }
func (c *pipeConn) Close() error {
	c.reader.Close()
	c.writer.Close()
	return nil
}
func (c *pipeConn) LocalAddr() net.Addr                { return &bufAddr{addr: "local"} }
func (c *pipeConn) RemoteAddr() net.Addr               { return &bufAddr{addr: "remote"} }
func (c *pipeConn) SetDeadline(t time.Time) error      { return nil }
func (c *pipeConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *pipeConn) SetWriteDeadline(t time.Time) error { return nil }
