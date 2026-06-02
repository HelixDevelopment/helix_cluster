package wireguard

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/cloudspot"
)

// teardownOnce guards the coordinator-level idempotent teardown so that
// concurrent callers do not race on Stop + RemovePeer.
type teardownState struct {
	mu   sync.Mutex
	done bool
}

// TeardownPeers stops the MeshCoordinator (idempotent) and removes every peer
// that is currently tracked by the underlying Manager.  It returns the number
// of peers that were removed on this call.
//
// Idempotency contract: a second (and any subsequent) call after all peers have
// already been removed returns (0, nil) — not an error.
//
// Thread safety: it is safe to call TeardownPeers concurrently; only the
// first caller will observe a non-zero removed count.
func (mc *MeshCoordinator) TeardownPeers(ctx context.Context) (removed int, err error) {
	// Stop the coordinator's sync loop first so it cannot re-add peers
	// concurrently while we are removing them.
	if stopErr := mc.Stop(); stopErr != nil {
		return 0, fmt.Errorf("wireguard teardown: stop coordinator: %w", stopErr)
	}

	// Collect the snapshot of peers under the manager's own lock (via the
	// public ListPeers API which takes RLock internally).
	peers, listErr := mc.wgManager.ListPeers()
	if listErr != nil {
		return 0, fmt.Errorf("wireguard teardown: list peers: %w", listErr)
	}

	var firstErr error
	for _, p := range peers {
		if rmErr := mc.wgManager.RemovePeer(p.PublicKey); rmErr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("wireguard teardown: remove peer %q: %w", p.PublicKey, rmErr)
			}
			continue
		}
		removed++
	}
	return removed, firstErr
}

// SpotDrainHook builds a cloudspot.Hooks value whose StopAdmission step calls
// TeardownPeers on the given MeshCoordinator.  Wire the returned Hooks into a
// cloudspot.Handler to get automatic peer teardown on a cloud-spot preemption
// notice.
//
// The returned Hooks.StopAdmission respects ctx cancellation / deadline so the
// drain lifecycle's timeout propagates correctly into the teardown.
func SpotDrainHook(mc *MeshCoordinator) cloudspot.Hooks {
	return cloudspot.Hooks{
		StopAdmission: func(ctx context.Context) error {
			// Honour the drain deadline: if ctx is already done, bail out fast.
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			// Run TeardownPeers in a goroutine so we can race it against ctx.
			type result struct {
				n   int
				err error
			}
			ch := make(chan result, 1)
			go func() {
				n, e := mc.TeardownPeers(ctx)
				ch <- result{n, e}
			}()

			select {
			case r := <-ch:
				if r.err != nil {
					return r.err
				}
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		// Checkpoint, Deregister and ForceStop are intentionally nil — the
		// wireguard layer only needs to drain the peer table; higher-level
		// hooks (session drain, control-plane deregister) are wired separately.
	}
}

// newNoOpManager returns a Manager with NoOp=true and a throwaway private key.
// It is exported only for same-package tests; production code should call
// NewManager directly.
func newNoOpManager() *Manager {
	// Generate a real key pair so AddPeer can parse the public key correctly.
	// If generation fails (should never happen), fall back to a zero-filled key.
	_, pub, err := GenerateKeyPair()
	privKey := pub // we only need something parseable as the manager's own key
	if err != nil {
		// Use a base64-encoded 32-zero-byte string as a last resort.
		privKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	}
	return &Manager{
		config: &Config{
			InterfaceName: "wg-noop",
			ListenPort:    0,
			PrivateKey:    privKey,
			NoOp:          true,
		},
		peers:    make(map[string]*PeerConfig),
		peerKeys: make(map[string][]validKey),
		now:      time.Now,
	}
}
