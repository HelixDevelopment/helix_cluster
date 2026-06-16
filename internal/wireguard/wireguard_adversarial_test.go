package wireguard

// Adversarial sink-side probes for internal/wireguard (the wired mesh/peer
// layer). These target the failure classes the package's concurrency contract
// must withstand:
//
//	(a) TEARDOWN RACE: concurrent peer add/remove churn vs send/apply readers
//	    (ListPeers / GetPeerConfig / SyncMesh) — must be race-free under -race
//	    AND never observe a torn/partial peer (send-on-closed analogue).
//	(b) UNGUARDED SHARED STATE / CONSERVATION: a pure add workload of N distinct
//	    peer IDs must leave exactly N peers visible — no lost update under a
//	    racing map.
//	(c) KEY VALIDATION: empty / short / malformed public keys must be REJECTED
//	    into the peer table (no silent state leak: not in ListPeers, no cached
//	    config).
//	(d) IDENTITY / TOTAL-ORDER: many concurrent adds of the SAME id must collapse
//	    to exactly one peer (identity conservation), and ListPeers must enumerate
//	    a deterministic membership SET (no duplicate / no phantom id).
//
// Anti-hang: concurrency is capped (<=8 live goroutines), no real network/root
// (NoOp manager), and every goroutine is join-bounded. `go test -race` over
// this file completes well within the 90s budget.

import (
	"context"
	"encoding/base64"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgwg "github.com/HelixDevelopment/helix_cluster/pkg/wireguard"
)

// advCoordinator builds a coordinator over a real (NoOp) manager so the full
// validate→manager.AddPeer→map-write path is exercised, exactly as a caller
// hits it. NoOp keeps it root/network-free without weakening any guard.
func advCoordinator(t *testing.T) *MeshCoordinator {
	t.Helper()
	cfg := pkgwg.DefaultConfig()
	cfg.NoOp = true
	mgr, err := pkgwg.NewManager(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Stop() })
	return NewMeshCoordinator(mgr)
}

// advKeys returns n distinct, structurally valid public keys.
func advKeys(t *testing.T, n int) []string {
	t.Helper()
	keys := make([]string, n)
	for i := range keys {
		_, pub, err := pkgwg.GenerateKeyPair()
		require.NoError(t, err)
		keys[i] = pub
	}
	return keys
}

// TestAdv_TeardownRace_AddRemoveVsReaders drives concurrent add/remove churn
// against the "send/apply" readers (ListPeers, GetPeerConfig, SyncMesh) — the
// teardown-race class. Under -race this must surface NO data race, and every
// peer ever observed by a reader must be internally consistent (ID, key, and a
// cached config all present together) — the sink-side analogue of "never send
// on a half-torn peer".
//
// Concurrency is capped at 8 live goroutines via a semaphore; all join before
// the assertions run.
//
// SINK-SIDE assertion: -race clean (enforced by `-race`) AND no torn peer
// observed by any reader.
func TestAdv_TeardownRace_AddRemoveVsReaders(t *testing.T) {
	mc := advCoordinator(t)
	ctx := context.Background()

	const distinct = 24
	keys := advKeys(t, distinct)
	id := func(i int) string { return "node-" + string(rune('A'+i%26)) + "-" + string(rune('0'+i/26)) }

	const maxLive = 8
	sem := make(chan struct{}, maxLive)
	var wg sync.WaitGroup

	var tornObserved int32
	var tornMu sync.Mutex
	recordTorn := func() { tornMu.Lock(); tornObserved++; tornMu.Unlock() }

	launch := func(fn func()) {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			fn()
		}()
	}

	const rounds = 40
	for r := 0; r < rounds; r++ {
		i := r % distinct
		peerID := id(i)
		pub := keys[i]

		// add
		launch(func() {
			_ = mc.AddPeer(ctx, &Peer{
				ID:         peerID,
				PublicKey:  pub,
				AllowedIPs: []string{"10.0.0.1/32"},
			})
		})
		// remove (racing the add for the same id)
		launch(func() {
			_ = mc.RemovePeer(ctx, peerID)
		})
		// reader: ListPeers — every returned peer must be internally whole.
		// A *Peer is fully constructed by the caller before AddPeer inserts it
		// under the lock, so a reader must never observe a half-built peer
		// (empty id/key/ips). NOTE: we deliberately do NOT cross-check
		// GetPeerConfig(p.ID) here — ListPeers and GetPeerConfig take the lock
		// independently, so a concurrent RemovePeer legitimately deletes the
		// peer between the two calls. That is a benign cross-call TOCTOU, not a
		// torn write: the product updates peers/configs atomically together
		// under one lock (verified by the -race clean run and the terminal
		// consistency sweep below), and the API exposes no atomic both-maps
		// snapshot to assert across. Checking it would test the test, not prod.
		launch(func() {
			for _, p := range mc.ListPeers() {
				if p == nil || p.ID == "" || p.PublicKey == "" || len(p.AllowedIPs) == 0 {
					recordTorn()
				}
			}
		})
		// reader: SyncMesh — concurrent reconcile against the live device.
		launch(func() {
			_ = mc.SyncMesh(ctx)
		})
	}

	wg.Wait()

	tornMu.Lock()
	torn := tornObserved
	tornMu.Unlock()
	assert.Zero(t, torn, "readers observed torn/partial peers during add/remove churn")

	// Drain to a known terminal state and assert consistency between the two
	// internal maps for every surviving peer.
	for _, p := range mc.ListPeers() {
		cfg, err := mc.GetPeerConfig(p.ID)
		require.NoErrorf(t, err, "surviving peer %s missing config (map desync)", p.ID)
		assert.Equalf(t, p.PublicKey, cfg.PublicKey, "peer/config key mismatch for %s", p.ID)
	}
}

// TestAdv_Conservation_PureAddNoLostUpdate is the unguarded-shared-state probe.
// N distinct peer IDs are added concurrently (capped at 8 live goroutines) with
// NO removes. The conservation law: all N must be present afterward. A racing /
// unguarded map would lose updates and yield < N.
//
// SINK-SIDE assertion: exactly N distinct peers, each with the key it was added
// under (no clobber), under -race.
func TestAdv_Conservation_PureAddNoLostUpdate(t *testing.T) {
	mc := advCoordinator(t)
	ctx := context.Background()

	const n = 32
	keys := advKeys(t, n)
	want := make(map[string]string, n) // id -> key
	for i := 0; i < n; i++ {
		want["peer-"+string(rune('a'+i%26))+"-"+string(rune('0'+i/26))] = keys[i]
	}

	const maxLive = 8
	sem := make(chan struct{}, maxLive)
	var wg sync.WaitGroup
	for idStr, pub := range want {
		idStr, pub := idStr, pub
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			require.NoError(t, mc.AddPeer(ctx, &Peer{
				ID:         idStr,
				PublicKey:  pub,
				AllowedIPs: []string{"10.0.0.1/32"},
			}))
		}()
	}
	wg.Wait()

	got := mc.ListPeers()
	assert.Len(t, got, n, "lost-update: not all concurrently-added peers are present")

	gotMap := make(map[string]string, len(got))
	for _, p := range got {
		gotMap[p.ID] = p.PublicKey
	}
	assert.Equal(t, want, gotMap, "peer set/keys diverge from the concurrently-added set")
}

// TestAdv_KeyValidation_RejectsBadKeysNoLeak proves empty / short / malformed
// public keys are REJECTED by AddPeer and leak NO state: the rejected id must
// not appear in ListPeers and must have no cached config.
//
// SINK-SIDE assertion: AddPeer errors AND the peer table stays empty for each
// bad key.
func TestAdv_KeyValidation_RejectsBadKeysNoLeak(t *testing.T) {
	ctx := context.Background()

	bad := []struct {
		name string
		key  string
	}{
		{"empty", ""},
		{"short", "AAAA"},
		{"malformed-base64", "not valid base64 !!!"},
		{"too-long", base64.StdEncoding.EncodeToString(make([]byte, 33))},
	}

	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			mc := advCoordinator(t)
			err := mc.AddPeer(ctx, &Peer{
				ID:         "k",
				PublicKey:  tc.key,
				AllowedIPs: []string{"10.0.0.1/32"},
			})
			require.Error(t, err, "AddPeer must reject %s public key", tc.name)

			assert.Empty(t, mc.ListPeers(), "rejected %s key leaked into ListPeers", tc.name)
			_, cfgErr := mc.GetPeerConfig("k")
			assert.Error(t, cfgErr, "rejected %s key leaked a cached config", tc.name)
		})
	}
}

// TestAdv_Identity_DuplicateIDCollapses is the identity / total-order probe.
// Many concurrent adds of the SAME id under DIFFERENT keys must collapse to
// exactly one peer (identity conservation) — duplicate ids must never produce
// two list entries. The surviving key must be one of the offered keys, and
// ListPeers must enumerate a deterministic membership SET with no duplicate id.
//
// SINK-SIDE assertion: exactly one entry for the id, key ∈ offered set, no
// duplicate id in the listing, under -race.
func TestAdv_Identity_DuplicateIDCollapses(t *testing.T) {
	mc := advCoordinator(t)
	ctx := context.Background()

	const variants = 16
	keys := advKeys(t, variants)
	offered := make(map[string]struct{}, variants)
	for _, k := range keys {
		offered[k] = struct{}{}
	}

	const maxLive = 8
	sem := make(chan struct{}, maxLive)
	var wg sync.WaitGroup
	for _, k := range keys {
		k := k
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			// All under the SAME id "dup".
			_ = mc.AddPeer(ctx, &Peer{
				ID:         "dup",
				PublicKey:  k,
				AllowedIPs: []string{"10.0.0.1/32"},
			})
		}()
	}
	wg.Wait()

	list := mc.ListPeers()

	// Membership is a set: no duplicate id, exactly one "dup".
	ids := make([]string, 0, len(list))
	for _, p := range list {
		ids = append(ids, p.ID)
	}
	sort.Strings(ids)
	for i := 1; i < len(ids); i++ {
		assert.NotEqualf(t, ids[i-1], ids[i], "duplicate id %q in ListPeers", ids[i])
	}
	require.Len(t, list, 1, "concurrent same-id adds must collapse to one peer")
	assert.Equal(t, "dup", list[0].ID)

	// The surviving key must be one we actually offered, and the cached config
	// must agree with the surviving peer (no cross-write between the two maps).
	_, ok := offered[list[0].PublicKey]
	assert.True(t, ok, "surviving key is not one of the offered keys (torn write)")
	cfg, err := mc.GetPeerConfig("dup")
	require.NoError(t, err)
	assert.Equal(t, list[0].PublicKey, cfg.PublicKey, "peer/config key desync after dup-id race")
}
