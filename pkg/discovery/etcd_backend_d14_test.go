// Package discovery — D14 regression test.
//
// D14: the etcd lease keep-alive goroutine must outlive the bounded, per-call
// registration context. Previously Put launched `go b.keepAlive(ctx, leaseID)`
// using the same short-lived registration ctx that the caller cancelled (via
// `defer regCancel()`) the instant Register returned — so renewals stopped, the
// lease expired after TTL, and /clusteros/nodes/<id> vanished while the agent
// process kept running. The fix binds keep-alive to a LIFETIME context
// (SetKeepAliveContext) that is cancelled only on agent shutdown.
package discovery

import (
	"context"
	"sync"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// keepAliveTrackingClient records the context handed to KeepAlive and exposes
// whether that goroutine is still consuming (i.e. the lease is still being
// renewed). The KeepAlive channel closes only when ITS ctx is cancelled, which
// lets the test assert keep-alive survives an unrelated per-call cancellation.
type keepAliveTrackingClient struct {
	*mockEtcdClient
	mu        sync.Mutex
	kaCtx     context.Context
	kaStarted chan struct{}
}

func newKeepAliveTrackingClient() *keepAliveTrackingClient {
	return &keepAliveTrackingClient{
		mockEtcdClient: newMockEtcdClient(),
		kaStarted:      make(chan struct{}, 1),
	}
}

func (c *keepAliveTrackingClient) KeepAlive(ctx context.Context, id clientv3.LeaseID) (<-chan *clientv3.LeaseKeepAliveResponse, error) {
	c.mu.Lock()
	c.kaCtx = ctx
	c.mu.Unlock()
	ch := make(chan *clientv3.LeaseKeepAliveResponse)
	go func() {
		// Channel stays OPEN (renewing) until the keep-alive ctx is cancelled.
		<-ctx.Done()
		close(ch)
	}()
	select {
	case c.kaStarted <- struct{}{}:
	default:
	}
	return ch, nil
}

func (c *keepAliveTrackingClient) keepAliveCtxErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.kaCtx == nil {
		return context.Canceled // not started yet — treat as not-alive
	}
	return c.kaCtx.Err()
}

// TestKeepAliveSurvivesRegistrationCtxCancel is the D14 regression guard.
func TestKeepAliveSurvivesRegistrationCtxCancel(t *testing.T) {
	client := newKeepAliveTrackingClient()
	b := NewEtcdBackend(client, "test")

	// Lifetime context — stands in for the agent's a.ctx (cancelled on Stop).
	lifeCtx, lifeCancel := context.WithCancel(context.Background())
	defer lifeCancel()
	b.SetKeepAliveContext(lifeCtx)

	// Register under a SHORT-LIVED per-call context, then cancel it — exactly
	// what node.go's `defer regCancel()` does when Start() returns.
	regCtx, regCancel := context.WithTimeout(context.Background(), 10*time.Second)
	inst := &Instance{ID: "n1", Service: "nodes", Address: "10.0.0.1", Port: 80, Healthy: true, TTL: 30 * time.Second}
	if err := b.Put(regCtx, "nodes/n1", inst); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Wait for keep-alive to have started.
	select {
	case <-client.kaStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("keep-alive goroutine never started")
	}

	// Cancel the registration context (the bug trigger).
	regCancel()
	time.Sleep(100 * time.Millisecond)

	// REGRESSION ASSERTION: keep-alive must STILL be alive — its context is the
	// lifetime context, which is NOT cancelled. Before the fix this would be
	// context.Canceled because keep-alive inherited regCtx.
	if err := client.keepAliveCtxErr(); err != nil {
		t.Fatalf("keep-alive died after registration ctx cancel (D14 regression): %v", err)
	}

	// And cancelling the LIFETIME context DOES stop keep-alive (clean shutdown).
	lifeCancel()
	deadline := time.After(2 * time.Second)
	for {
		if err := client.keepAliveCtxErr(); err != nil {
			break // keep-alive stopped as expected
		}
		select {
		case <-deadline:
			t.Fatal("keep-alive did not stop after lifetime ctx cancel")
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// TestKeepAliveDefaultsToBackground verifies a backend with no explicit
// lifetime context still renews forever (does not inherit a per-call ctx).
func TestKeepAliveDefaultsToBackground(t *testing.T) {
	client := newKeepAliveTrackingClient()
	b := NewEtcdBackend(client, "test") // no SetKeepAliveContext

	regCtx, regCancel := context.WithCancel(context.Background())
	inst := &Instance{ID: "n2", Service: "nodes", Address: "10.0.0.2", Port: 80, Healthy: true, TTL: 30 * time.Second}
	if err := b.Put(regCtx, "nodes/n2", inst); err != nil {
		t.Fatalf("Put: %v", err)
	}
	select {
	case <-client.kaStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("keep-alive goroutine never started")
	}
	regCancel()
	time.Sleep(100 * time.Millisecond)
	if err := client.keepAliveCtxErr(); err != nil {
		t.Fatalf("keep-alive died on per-call ctx cancel with default lifetime ctx: %v", err)
	}
}
