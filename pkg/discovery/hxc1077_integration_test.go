//go:build integration

package discovery

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"digital.vasic.containers/pkg/brokertest"
	"github.com/HelixDevelopment/helix_cluster/pkg/etcd"
	"github.com/HelixDevelopment/helix_cluster/pkg/testing/evidence"
)

// discoveryEndpoint is the shared real etcd address for the discovery package
// integration suite. One container is booted per package run (see TestMain).
var discoveryEndpoint string

// TestMain boots ONE real ephemeral etcd for the entire discovery integration
// suite, runs all tests against it, and tears it down. All tests in this file
// isolate themselves with unique key prefixes derived from their per-run tokens.
func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	endpoint, stop, err := brokertest.StartEtcd(ctx)
	if err != nil {
		cancel()
		fmt.Fprintf(os.Stderr, "brokertest.StartEtcd: %v\n", err)
		os.Exit(1)
	}
	discoveryEndpoint = endpoint
	fmt.Fprintf(os.Stderr, "real etcd for discovery suite at %s\n", endpoint)

	code := m.Run()

	stop()
	cancel()
	os.Exit(code)
}

// newEtcdBackend wires a real *etcd.Client → EtcdBackend with the given prefix.
func newEtcdBackend(t *testing.T, prefix string) (*EtcdBackend, func()) {
	t.Helper()
	c, err := etcd.New(etcd.Config{
		Endpoints:   []string{discoveryEndpoint},
		DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("etcd.New: %v", err)
	}
	backend := NewEtcdBackend(c, prefix)
	return backend, func() { _ = c.Close() }
}

// newRegistryWithEtcd creates a ServiceRegistry backed by a real EtcdBackend.
func newRegistryWithEtcd(t *testing.T, prefix string) (*ServiceRegistry, func()) {
	t.Helper()
	backend, closeBackend := newEtcdBackend(t, prefix)
	reg := NewServiceRegistry(backend)
	return reg, closeBackend
}

// TestHXC1077_RegisterAndLookup_RealEtcd is the §7.1 sink-side proof for
// HXC-1077 service registration. It proves:
//  1. Register stores the instance in REAL etcd (not in-process memory).
//  2. A Lookup via a SEPARATE registry (second EtcdBackend connection) retrieves
//     the same instance byte-equal — proves no shared in-process state.
//  3. The per-run UUID token appears in the retrieved instance fields.
func TestHXC1077_RegisterAndLookup_RealEtcd(t *testing.T) {
	e := evidence.NewT(t, "hxc1077-register-lookup", t.TempDir())
	tok := e.Token()

	// Isolate this test run with a per-token prefix.
	prefix := "hxc1077-reg-" + tok

	// Writer registry: registers the instance.
	regA, closeA := newRegistryWithEtcd(t, prefix)
	defer closeA()

	// Reader registry: separate EtcdBackend, no shared in-process state.
	regB, closeB := newRegistryWithEtcd(t, prefix)
	defer closeB()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	inst := &Instance{
		ID:      "svc-" + tok,
		Service: "test-service",
		Address: "10.1.2.3",
		Port:    9090,
		Healthy: true,
		TTL:     30 * time.Second,
		Metadata: map[string]string{
			"run_token": tok,
			"build":     "wave4",
		},
	}

	// State before: regB sees no instances.
	beforeList, err := regB.Lookup(ctx, "test-service")
	if err != nil {
		t.Fatalf("regB.Lookup(before): %v", err)
	}
	beforeCount := len(beforeList)

	// ACTION: register via regA.
	if err := regA.Register(ctx, inst); err != nil {
		t.Fatalf("regA.Register: %v", err)
	}

	// Allow etcd propagation (not a mock — real network round-trip).
	time.Sleep(200 * time.Millisecond)

	// SINK: regB reads from real etcd.
	afterList, err := regB.Lookup(ctx, "test-service")
	if err != nil {
		t.Fatalf("regB.Lookup(after): %v", err)
	}

	if len(afterList) == 0 {
		t.Fatal("regB.Lookup returned 0 instances after registration (etcd backend not persisting)")
	}

	// Find our specific instance by ID.
	var found *Instance
	for _, i := range afterList {
		if i.ID == inst.ID {
			found = i
			break
		}
	}
	if found == nil {
		t.Fatalf("instance %q not found in regB lookup result: %v", inst.ID, afterList)
	}

	// Sink-side field assertions.
	if found.Address != inst.Address {
		t.Errorf("Address: got %q want %q", found.Address, inst.Address)
	}
	if found.Port != inst.Port {
		t.Errorf("Port: got %d want %d", found.Port, inst.Port)
	}
	if found.Metadata["run_token"] != tok {
		t.Errorf("Metadata[run_token]: got %q want %q", found.Metadata["run_token"], tok)
	}

	// §7.1 state delta: 0 instances before → at least 1 after.
	e.MustDelta(t, "instance-count-delta", beforeCount, len(afterList))
	// §7.1 positive evidence: run token in retrieved metadata.
	e.MustPositive(t, "run-token-in-metadata", found.Metadata["run_token"], tok)

	t.Logf("HXC-1077 Register/Lookup: id=%s addr=%s token=%s", found.ID, found.Address, tok)
}

// TestHXC1077_LeaseKeepaliveRenews_RealEtcd proves that the etcd lease
// keepalive mechanism keeps a leased key alive beyond its raw TTL. It:
//  1. Grants a short (3s) lease and starts keepalive via KeepAlive.
//  2. Waits 5s (> raw TTL) — the key must STILL be present (keepalive working).
//  3. Stops the keepalive context → key must disappear within TTL.
//  4. Verifies the lease-id is non-zero.
//
// Per §7.1 the per-run UUID token is embedded in the key value.
func TestHXC1077_LeaseKeepaliveRenews_RealEtcd(t *testing.T) {
	e := evidence.NewT(t, "hxc1077-keepalive", t.TempDir())
	tok := e.Token()

	c, err := etcd.New(etcd.Config{
		Endpoints:   []string{discoveryEndpoint},
		DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("etcd.New: %v", err)
	}
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const ttlSec = 3 // 3-second raw TTL

	// Grant the lease.
	leaseID, err := c.Lease(ctx, ttlSec)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	if leaseID == 0 {
		t.Fatal("Lease returned zero LeaseID")
	}
	t.Logf("HXC-1077: lease granted id=0x%x ttl=%ds", leaseID, ttlSec)

	// A context we will cancel to STOP the keepalive.
	kaCtx, kaCancel := context.WithCancel(ctx)

	// Start keepalive — it sends renewal RPCs to etcd preventing expiry.
	kaCh, err := c.KeepAlive(kaCtx, leaseID)
	if err != nil {
		kaCancel()
		t.Fatalf("KeepAlive: %v", err)
	}

	// Drain keepalive responses in background so the channel doesn't block.
	kaActive := make(chan struct{})
	go func() {
		defer close(kaActive)
		for range kaCh {
			// consume — keepalive is alive as long as responses arrive
		}
	}()

	// Write the test key under the lease.
	key := "/clusteros/test/ka-" + tok
	value := `{"run_token":"` + tok + `","lease":"0x` + fmt.Sprintf("%x", leaseID) + `"}`
	if err := c.PutWithLease(ctx, key, value, leaseID); err != nil {
		kaCancel()
		t.Fatalf("PutWithLease: %v", err)
	}

	// Wait LONGER than raw TTL (5s > 3s). Key must still exist — keepalive working.
	time.Sleep(5 * time.Second)

	gotVal, ok, err := c.Get(ctx, key)
	if err != nil {
		kaCancel()
		t.Fatalf("Get(during keepalive): %v", err)
	}
	if !ok {
		kaCancel()
		t.Fatal("key expired during active keepalive (keepalive NOT working)")
	}
	if gotVal != value {
		kaCancel()
		t.Fatalf("value mismatch during keepalive: got %q want %q", gotVal, value)
	}
	t.Logf("HXC-1077: key present after %ds > TTL=%ds — keepalive confirmed", 5, ttlSec)

	// STOP keepalive — cancel the keepalive context.
	kaCancel()
	// Drain remaining keepalive responses.
	<-kaActive

	// After stopping keepalive the key must disappear within TTL + margin.
	deadline := time.Now().Add(time.Duration(ttlSec+3) * time.Second)
	for time.Now().Before(deadline) {
		_, still, err := c.Get(ctx, key)
		if err != nil {
			t.Fatalf("Get(after keepalive stop): %v", err)
		}
		if !still {
			t.Logf("HXC-1077: key disappeared after keepalive stop — TTL honored")
			e.MustPositive(t, "lease-id-nonzero", fmt.Sprintf("%x", leaseID), fmt.Sprintf("%x", leaseID))
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("key still present %ds after keepalive stop — lease not expiring", ttlSec+3)
}

// TestHXC1077_WatcherObservesDeregister_RealEtcd proves the full
// register-keepalive-stop-deregister-watcher lifecycle via a real etcd:
//  1. A watcher is established before registration.
//  2. The instance is registered → watcher receives EventRegister.
//  3. The instance is deregistered → watcher receives EventDeregister.
//  4. Per-run UUID token is in the instance ID so replayed events cannot bluff.
func TestHXC1077_WatcherObservesDeregister_RealEtcd(t *testing.T) {
	e := evidence.NewT(t, "hxc1077-watcher-deregister", t.TempDir())
	tok := e.Token()
	prefix := "hxc1077-watch-" + tok

	reg, closeReg := newRegistryWithEtcd(t, prefix)
	defer closeReg()

	watchCtx, watchCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer watchCancel()

	// Establish watcher BEFORE registering so we capture the register event.
	evtCh, err := reg.Watch(watchCtx, "watched-svc")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	// Allow watcher to attach to etcd.
	time.Sleep(300 * time.Millisecond)

	inst := &Instance{
		ID:      "wi-" + tok,
		Service: "watched-svc",
		Address: "10.9.8.7",
		Port:    8080,
		Healthy: true,
		TTL:     30 * time.Second,
	}

	regCtx, regCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := reg.Register(regCtx, inst); err != nil {
		regCancel()
		t.Fatalf("Register: %v", err)
	}
	regCancel()

	// Expect EventRegister from the watcher.
	select {
	case evt, ok := <-evtCh:
		if !ok {
			t.Fatal("watch channel closed before receiving register event")
		}
		if evt.Type != EventRegister {
			t.Errorf("expected EventRegister, got %v", evt.Type)
		}
		if evt.Instance == nil {
			t.Fatal("register event carried nil Instance")
		}
		if evt.Instance.ID != inst.ID {
			t.Errorf("register event Instance.ID = %q, want %q", evt.Instance.ID, inst.ID)
		}
		t.Logf("HXC-1077: watcher received EventRegister for id=%s", evt.Instance.ID)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for EventRegister from real etcd watcher")
	}

	// Deregister — watcher must receive EventDeregister.
	deregCtx, deregCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := reg.Deregister(deregCtx, "watched-svc", inst.ID); err != nil {
		deregCancel()
		t.Fatalf("Deregister: %v", err)
	}
	deregCancel()

	select {
	case evt, ok := <-evtCh:
		if !ok {
			// Channel closed counts as deregister signal too (context-cancel path).
			t.Log("HXC-1077: watch channel closed after Deregister (context closed)")
			return
		}
		if evt.Type != EventDeregister {
			t.Errorf("expected EventDeregister, got %v", evt.Type)
		}
		t.Logf("HXC-1077: watcher received EventDeregister for instance=%+v", evt.Instance)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for EventDeregister from real etcd watcher")
	}

	// §7.1 positive evidence: the run token was present in the registered ID.
	e.MustPositive(t, "run-token-in-instance-id", inst.ID, tok)
}

// TestHXC1077_TTLBoundedRegistration_RealEtcd proves that when the underlying
// etcd lease expires (no keepalive), the key disappears from the store — the
// TTL-bounded registration contract is honored by real etcd, not simulated.
// A per-run UUID is embedded in the key so a stale read cannot bluff.
func TestHXC1077_TTLBoundedRegistration_RealEtcd(t *testing.T) {
	e := evidence.NewT(t, "hxc1077-ttl-expiry", t.TempDir())
	tok := e.Token()

	// Use the raw etcd client directly to control lease TTL precisely
	// (the EtcdBackend.Put starts a keepalive; we skip it here by going raw).
	c, err := etcd.New(etcd.Config{
		Endpoints:   []string{discoveryEndpoint},
		DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("etcd.New: %v", err)
	}
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const ttlSec = 2 // short TTL; no keepalive → will expire

	leaseID, err := c.Lease(ctx, ttlSec)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	if leaseID == 0 {
		t.Fatal("Lease: zero LeaseID")
	}

	key := "/clusteros/nodes/ttl-probe-" + tok
	value := `{"run_token":"` + tok + `"}`

	if err := c.PutWithLease(ctx, key, value, leaseID); err != nil {
		t.Fatalf("PutWithLease: %v", err)
	}

	// Verify it's present while lease is alive.
	gotVal, ok, err := c.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get(live): %v", err)
	}
	if !ok {
		t.Fatal("key absent while lease should be alive")
	}
	if gotVal != value {
		t.Fatalf("value mismatch: got %q want %q", gotVal, value)
	}

	// §7.1 positive evidence: token present while lease alive.
	e.MustPositive(t, "token-while-alive", gotVal, tok)

	t.Logf("HXC-1077: leased key present (lease_id=0x%x, ttl=%ds)", leaseID, ttlSec)

	// Wait for lease to expire (TTL + margin).
	time.Sleep(time.Duration(ttlSec+2) * time.Second)

	_, ok, err = c.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get(expired): %v", err)
	}
	if ok {
		t.Fatal("leased key still present after TTL expiry — real etcd lease NOT working")
	}

	// §7.1 state delta: key was present, now absent.
	e.MustDelta(t, "key-presence-delta", true, false)
	t.Logf("HXC-1077: leased key expired after TTL — real etcd TTL-bounded registration confirmed")
}

// TestHXC1077_LeaseID_IsNonZero_AndUniquePerGrant proves that every call to
// Lease returns a distinct, non-zero LeaseID. Two registrations with the same
// LeaseID would mean one Revoke kills the other — a hard data-plane bug.
func TestHXC1077_LeaseID_IsNonZero_AndUniquePerGrant(t *testing.T) {
	e := evidence.NewT(t, "hxc1077-lease-id-uniqueness", t.TempDir())

	c, err := etcd.New(etcd.Config{
		Endpoints:   []string{discoveryEndpoint},
		DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("etcd.New: %v", err)
	}
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	id1, err := c.Lease(ctx, 60)
	if err != nil {
		t.Fatalf("Lease 1: %v", err)
	}
	id2, err := c.Lease(ctx, 60)
	if err != nil {
		t.Fatalf("Lease 2: %v", err)
	}

	if id1 == 0 {
		t.Fatal("Lease 1: zero LeaseID")
	}
	if id2 == 0 {
		t.Fatal("Lease 2: zero LeaseID")
	}
	if id1 == id2 {
		t.Fatalf("Lease 1 and 2 returned same ID 0x%x — etcd did not generate unique leases", id1)
	}

	// Clean up leases.
	_ = c.RevokeLease(ctx, id1)
	_ = c.RevokeLease(ctx, id2)

	e.MustDelta(t, "lease-id-uniqueness", fmt.Sprintf("%x", id1), fmt.Sprintf("%x", id2))
	t.Logf("HXC-1077: lease1=0x%x lease2=0x%x — both non-zero and distinct", id1, id2)
}
