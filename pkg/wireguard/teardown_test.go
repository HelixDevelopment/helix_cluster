package wireguard

import (
	"context"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/cloudspot"
)

// makeTestPeer generates a unique WireGuard PeerConfig with a real key pair so
// that AddPeer and RemovePeer can parse the public key correctly.  The Manager
// in NoOp mode stores and deletes peers purely in memory, so no kernel
// interface is needed.
func makeTestPeer(t *testing.T) *PeerConfig {
	t.Helper()
	_, pub, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	return &PeerConfig{
		PublicKey:  pub,
		AllowedIPs: []string{"10.0.0.1/32"},
	}
}

// newTestCoordinator builds a MeshCoordinator backed by a NoOp Manager, which
// tracks peers purely in memory.  The swim.Protocol is nil — MeshCoordinator
// can be constructed with nil but SyncNow would fail; for teardown tests we
// never call SyncNow.
func newTestCoordinator(t *testing.T) *MeshCoordinator {
	t.Helper()
	mgr := newNoOpManager()
	// Construct the MeshCoordinator using the exported constructor; swim may be
	// nil (it is only needed for SyncNow which we do not call here).
	mc := NewMeshCoordinator(mgr, nil)
	return mc
}

// addPeers adds n freshly-generated peers to the coordinator's manager and
// returns them so tests can inspect the public keys.
func addPeers(t *testing.T, mc *MeshCoordinator, n int) []*PeerConfig {
	t.Helper()
	peers := make([]*PeerConfig, n)
	for i := 0; i < n; i++ {
		p := makeTestPeer(t)
		if err := mc.wgManager.AddPeer(p); err != nil {
			t.Fatalf("AddPeer[%d]: %v", i, err)
		}
		peers[i] = p
	}
	return peers
}

// mustListPeers returns the current peer list, failing the test on error.
func mustListPeers(t *testing.T, mc *MeshCoordinator) []*PeerConfig {
	t.Helper()
	list, err := mc.wgManager.ListPeers()
	if err != nil {
		t.Fatalf("ListPeers: %v", err)
	}
	return list
}

// ---------------------------------------------------------------------------
// Test: AddPeer then ListPeers shows the peers (proves the NoOp manager
// really tracks peers in memory — the test is not a no-op-on-no-op).
// ---------------------------------------------------------------------------

func TestNoOpManager_AddListPeers(t *testing.T) {
	const n = 5
	mc := newTestCoordinator(t)

	_ = addPeers(t, mc, n)

	got := mustListPeers(t, mc)
	if len(got) != n {
		t.Fatalf("ListPeers after %d AddPeer calls: got %d peers, want %d", n, len(got), n)
	}
}

// ---------------------------------------------------------------------------
// Test: TeardownPeers removes all peers (sink-side: ListPeers == 0 after).
// Mutation anchor: if TeardownPeers does not call RemovePeer the in-memory
// map is unchanged and ListPeers still returns N — this test FAILS.
// ---------------------------------------------------------------------------

func TestTeardownPeers_RemovesAllPeers(t *testing.T) {
	const n = 4
	mc := newTestCoordinator(t)
	_ = addPeers(t, mc, n)

	ctx := context.Background()
	removed, err := mc.TeardownPeers(ctx)
	if err != nil {
		t.Fatalf("TeardownPeers: unexpected error: %v", err)
	}
	if removed != n {
		t.Fatalf("TeardownPeers returned removed=%d, want %d", removed, n)
	}

	// Sink-side: the underlying map must now be empty.
	remaining := mustListPeers(t, mc)
	if len(remaining) != 0 {
		t.Fatalf("ListPeers after TeardownPeers: got %d peers, want 0 (peers were NOT actually removed)", len(remaining))
	}
}

// ---------------------------------------------------------------------------
// Test: TeardownPeers is idempotent — second call returns (0, nil) and
// ListPeers is still 0.
// ---------------------------------------------------------------------------

func TestTeardownPeers_Idempotent(t *testing.T) {
	const n = 3
	mc := newTestCoordinator(t)
	_ = addPeers(t, mc, n)

	ctx := context.Background()

	// First call — removes all N peers.
	first, err := mc.TeardownPeers(ctx)
	if err != nil {
		t.Fatalf("TeardownPeers first call: %v", err)
	}
	if first != n {
		t.Fatalf("first TeardownPeers: removed=%d, want %d", first, n)
	}

	// Second call — nothing left to remove; must not error.
	second, err := mc.TeardownPeers(ctx)
	if err != nil {
		t.Fatalf("TeardownPeers second call (idempotent): %v", err)
	}
	if second != 0 {
		t.Fatalf("second TeardownPeers: removed=%d, want 0 (idempotent)", second)
	}

	// Sink still empty.
	remaining := mustListPeers(t, mc)
	if len(remaining) != 0 {
		t.Fatalf("ListPeers after second TeardownPeers: got %d, want 0", len(remaining))
	}
}

// ---------------------------------------------------------------------------
// Test: SpotDrainHook wired into a cloudspot.Handler, driven by a fake
// preemption source, actually removes all peers.
//
// This is the hook-driven drain end-to-end bite:
//   - Add N peers.
//   - Wire mc into a cloudspot.Handler via SpotDrainHook.
//   - Emit a preemption notice via FakeSource.
//   - Wait for the handler to complete.
//   - Assert ListPeers()==0 (peers were REALLY removed through the hook).
//
// Mutation that this kills: if SpotDrainHook.StopAdmission does not call
// TeardownPeers (or calls a no-op), ListPeers remains N and the assertion FAILS.
// ---------------------------------------------------------------------------

func TestSpotDrainHook_DrivesByFakePreemption_RemovesPeers(t *testing.T) {
	const n = 6
	mc := newTestCoordinator(t)
	_ = addPeers(t, mc, n)

	// Confirm peers are present before the hook fires.
	before := mustListPeers(t, mc)
	if len(before) != n {
		t.Fatalf("pre-drain ListPeers: got %d, want %d", len(before), n)
	}

	src := cloudspot.NewFakeSource()
	hooks := SpotDrainHook(mc)
	handler := cloudspot.NewHandler(src, hooks)

	done := make(chan cloudspot.Result, 1)
	go func() {
		done <- handler.Run(context.Background())
	}()

	// Emit a comfortable preemption deadline so the trivial teardown finishes
	// well within it.
	src.Emit(time.Now().Add(5 * time.Second))

	var res cloudspot.Result
	select {
	case res = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler.Run did not complete after preemption notice")
	}

	if res.Outcome != cloudspot.OutcomeDrained {
		t.Fatalf("cloudspot outcome = %q, want %q", res.Outcome, cloudspot.OutcomeDrained)
	}
	if res.Err != nil {
		t.Fatalf("cloudspot drain error: %v", res.Err)
	}

	// Sink-side: peers must be gone after the hook-driven drain.
	after := mustListPeers(t, mc)
	if len(after) != 0 {
		t.Fatalf("ListPeers after hook-driven drain: got %d peers, want 0 (hook did NOT remove peers)", len(after))
	}
}

// ---------------------------------------------------------------------------
// Test: Mutation guard — proves TeardownPeers not calling RemovePeer would be
// detected.  We call RemovePeer manually and verify the count changes, so a
// stub RemovePeer that never deletes from m.peers would leave the map intact.
// ---------------------------------------------------------------------------

func TestNoOpManager_RemovePeerActuallyDeletes(t *testing.T) {
	mc := newTestCoordinator(t)
	peers := addPeers(t, mc, 2)

	// Remove the first peer explicitly.
	if err := mc.wgManager.RemovePeer(peers[0].PublicKey); err != nil {
		t.Fatalf("RemovePeer: %v", err)
	}

	remaining := mustListPeers(t, mc)
	if len(remaining) != 1 {
		t.Fatalf("after RemovePeer: got %d peers, want 1", len(remaining))
	}
	if remaining[0].PublicKey != peers[1].PublicKey {
		t.Fatalf("remaining peer = %q, want %q", remaining[0].PublicKey, peers[1].PublicKey)
	}
}
