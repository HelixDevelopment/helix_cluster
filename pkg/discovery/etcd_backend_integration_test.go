//go:build integration

package discovery

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/testing/evidence"
)

// TestHXC1151_ServiceRegistry_TTLExpiry_RealEtcd drives the high-level
// ServiceRegistry (backed by a real EtcdBackend) through the full
// Register → Lookup → TTL-expiry-excludes → re-Register-restores lifecycle
// against the real ephemeral etcd booted by TestMain.
//
// Mutation proof (orchestrator runs):
//
//	If EtcdBackend.Put drops the lease/TTL argument (calls c.Put instead of
//	c.PutWithLease), the etcd key is not bound to any lease and therefore never
//	expires. The "Lookup EXCLUDES after TTL" assertion then FAILS because the
//	expired instance wrongly persists in etcd.
func TestHXC1151_ServiceRegistry_TTLExpiry_RealEtcd(t *testing.T) {
	e := evidence.NewT(t, "hxc1151-ttl-lifecycle", t.TempDir())
	tok := e.Token()

	// Unique prefix per run — no bleed between parallel runs.
	prefix := "hxc1151-" + tok

	// Single registry: Register + Lookup go through the same EtcdBackend.
	reg, closeReg := newRegistryWithEtcd(t, prefix)
	defer closeReg()

	// Use a short TTL so expiry is observable in a test.
	// 4 seconds raw TTL: keepalive is started by EtcdBackend.Put (using the
	// registration context). We cancel that context to stop keepalive so the
	// lease is allowed to expire.
	const ttlSec = 4

	// --- Phase 1: Register -----------------------------------------------
	// We need a cancellable context for the Register call so we can stop the
	// background keepalive that EtcdBackend launches.
	regCtx, regCancel := context.WithTimeout(context.Background(), 10*time.Second)

	inst := &Instance{
		ID:      "svc-" + tok,
		Service: "hxc1151-svc",
		Address: "192.168.1.100",
		Port:    7070,
		Healthy: true,
		TTL:     time.Duration(ttlSec) * time.Second,
		Metadata: map[string]string{
			"run_token": tok,
			"wave":      "12",
		},
	}

	if err := reg.Register(regCtx, inst); err != nil {
		regCancel()
		t.Fatalf("Register: %v", err)
	}

	// Allow etcd write to propagate.
	time.Sleep(300 * time.Millisecond)

	// --- Phase 2: Lookup INCLUDES the registered instance ----------------
	lookupCtx, lookupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer lookupCancel()

	list1, err := reg.Lookup(lookupCtx, "hxc1151-svc")
	if err != nil {
		regCancel()
		t.Fatalf("Lookup(after register): %v", err)
	}
	var found1 *Instance
	for _, i := range list1 {
		if i.ID == inst.ID {
			found1 = i
			break
		}
	}
	if found1 == nil {
		regCancel()
		t.Fatalf("instance %q not found after Register — list: %v", inst.ID, list1)
	}
	if found1.Metadata["run_token"] != tok {
		regCancel()
		t.Errorf("Metadata[run_token] = %q, want %q", found1.Metadata["run_token"], tok)
	}
	e.MustPositive(t, "registered-token-in-metadata", found1.Metadata["run_token"], tok)
	t.Logf("HXC-1151: instance %q found after Register (token=%s)", inst.ID, tok)

	// --- Phase 3: Stop keepalive + wait for TTL expiry -------------------
	// Cancelling the registration context terminates the background keepAlive
	// goroutine in EtcdBackend. Once keepalive stops, the etcd lease expires
	// after its TTL and the key disappears.
	regCancel()

	// Wait for TTL + margin so etcd can evict the leased key.
	waitFor := time.Duration(ttlSec+3) * time.Second
	t.Logf("HXC-1151: waiting %s for TTL expiry...", waitFor)
	time.Sleep(waitFor)

	// --- Phase 4: Lookup EXCLUDES the expired instance -------------------
	expCtx, expCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer expCancel()

	list2, err := reg.Lookup(expCtx, "hxc1151-svc")
	if err != nil {
		t.Fatalf("Lookup(after TTL expiry): %v", err)
	}
	for _, i := range list2 {
		if i.ID == inst.ID {
			t.Fatalf("instance %q still present after TTL expiry — lease/TTL not applied (mutation: lease dropped on Put)", inst.ID)
		}
	}
	t.Logf("HXC-1151: instance %q correctly absent after TTL expiry", inst.ID)

	// §7.1 state delta: instance was present, now absent.
	beforeCount := 1 // we just confirmed it was found
	afterCount := 0  // we confirmed it is now absent
	e.MustDelta(t, "ttl-expiry-removes-instance", beforeCount, afterCount)

	// --- Phase 5: Re-register and assert Lookup RESTORES it --------------
	reregCtx, reregCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer reregCancel()

	// Fresh instance with same ID — use a new LastSeen so TTL clock resets.
	inst2 := &Instance{
		ID:      inst.ID,
		Service: "hxc1151-svc",
		Address: "192.168.1.100",
		Port:    7070,
		Healthy: true,
		TTL:     30 * time.Second, // long TTL so it stays alive during the test
		Metadata: map[string]string{
			"run_token": tok,
			"rereg":     "true",
		},
	}
	if err := reg.Register(reregCtx, inst2); err != nil {
		t.Fatalf("Re-register: %v", err)
	}

	time.Sleep(300 * time.Millisecond)

	list3, err := reg.Lookup(reregCtx, "hxc1151-svc")
	if err != nil {
		t.Fatalf("Lookup(after re-register): %v", err)
	}
	var found3 *Instance
	for _, i := range list3 {
		if i.ID == inst.ID {
			found3 = i
			break
		}
	}
	if found3 == nil {
		t.Fatalf("instance %q not found after re-register — Lookup failed to restore it", inst.ID)
	}
	// Verify the run token is still present.
	if !strings.Contains(found3.Metadata["run_token"], tok) {
		t.Errorf("re-registered Metadata[run_token] = %q, want to contain %q", found3.Metadata["run_token"], tok)
	}
	e.MustPositive(t, "re-registered-token-in-metadata", found3.Metadata["run_token"], tok)
	e.MustDelta(t, "restore-delta", afterCount, 1)

	t.Logf("HXC-1151: instance %q restored after re-register — TTL lifecycle verified", inst.ID)
}
