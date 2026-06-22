package wireguard

import (
	"context"
	"errors"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/swim"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGenerateKeyPair tests key generation and parsing.
func TestGenerateKeyPair(t *testing.T) {
	priv, pub, err := GenerateKeyPair()
	require.NoError(t, err)
	assert.NotEmpty(t, priv)
	assert.NotEmpty(t, pub)
	assert.NotEqual(t, priv, pub)

	// Parse both keys.
	privKey, err := ParseKey(priv)
	require.NoError(t, err)
	assert.Equal(t, 32, len(privKey))

	pubKey, err := ParseKey(pub)
	require.NoError(t, err)
	assert.Equal(t, 32, len(pubKey))

	// Parse invalid key.
	_, err = ParseKey("invalid-key")
	assert.Error(t, err)

	// Parse key with wrong length.
	_, err = ParseKey("d2F5dG9vc2hvcnQ=")
	assert.Error(t, err)
}

// TestDefaultConfig tests default configuration.
func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	assert.Equal(t, "wg0", cfg.InterfaceName)
	assert.Equal(t, 51820, cfg.ListenPort)
	assert.Equal(t, 1420, cfg.MTU)
	assert.False(t, cfg.EnableUPnP)
	assert.False(t, cfg.EnableNATPMapping)
}

// TestNewManager tests manager creation.
func TestNewManager(t *testing.T) {
	cfg := DefaultConfig()
	mgr, err := NewManager(cfg)
	require.NoError(t, err)
	assert.NotNil(t, mgr)
	assert.NotNil(t, mgr.backend)
	assert.Equal(t, cfg, mgr.config)

	// Nil config.
	_, err = NewManager(nil)
	assert.Error(t, err)
}

// TestManagerStartStop tests starting and stopping the manager.
func TestManagerStartStop(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("WireGuard kernel interface not available on macOS without additional setup")
	}
	if os.Getuid() != 0 {
		t.Skip("requires root privileges to configure WireGuard interfaces")
	}

	priv, _, err := GenerateKeyPair()
	require.NoError(t, err)

	cfg := DefaultConfig()
	cfg.PrivateKey = priv
	cfg.InterfaceName = "wg_test0"

	mgr, err := NewManager(cfg)
	require.NoError(t, err)

	err = mgr.Start()
	require.NoError(t, err)

	err = mgr.Stop()
	require.NoError(t, err)
}

// TestManagerPeerOperations tests peer add, remove, list, get.
func TestManagerPeerOperations(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("WireGuard kernel interface not available on macOS without additional setup")
	}
	if os.Getuid() != 0 {
		t.Skip("requires root privileges to configure WireGuard interfaces")
	}

	priv, pub, err := GenerateKeyPair()
	require.NoError(t, err)

	cfg := DefaultConfig()
	cfg.PrivateKey = priv
	cfg.InterfaceName = "wg_test1"

	mgr, err := NewManager(cfg)
	require.NoError(t, err)

	err = mgr.Start()
	require.NoError(t, err)
	defer mgr.Stop()

	// Add peer.
	peer := &PeerConfig{
		PublicKey:  pub,
		AllowedIPs: []string{"10.0.0.2/32"},
		Endpoint:   "127.0.0.1:51821",
	}
	err = mgr.AddPeer(peer)
	require.NoError(t, err)

	// List peers.
	peers, err := mgr.ListPeers()
	require.NoError(t, err)
	assert.Len(t, peers, 1)

	// Get peer.
	got, err := mgr.GetPeer(pub)
	require.NoError(t, err)
	assert.Equal(t, peer.PublicKey, got.PublicKey)

	// Remove peer.
	err = mgr.RemovePeer(pub)
	require.NoError(t, err)

	peers, err = mgr.ListPeers()
	require.NoError(t, err)
	assert.Len(t, peers, 0)

	// Get removed peer.
	_, err = mgr.GetPeer(pub)
	assert.Error(t, err)
}

// TestManagerAddPeerValidation tests peer validation.
func TestManagerAddPeerValidation(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("WireGuard kernel interface not available on macOS without additional setup")
	}
	if os.Getuid() != 0 {
		t.Skip("requires root privileges to configure WireGuard interfaces")
	}

	priv, _, err := GenerateKeyPair()
	require.NoError(t, err)

	cfg := DefaultConfig()
	cfg.PrivateKey = priv
	cfg.InterfaceName = "wg_test2"

	mgr, err := NewManager(cfg)
	require.NoError(t, err)

	err = mgr.Start()
	require.NoError(t, err)
	defer mgr.Stop()

	// Nil peer.
	err = mgr.AddPeer(nil)
	assert.Error(t, err)

	// Invalid public key.
	err = mgr.AddPeer(&PeerConfig{PublicKey: "bad-key"})
	assert.Error(t, err)

	// Invalid allowed IP.
	_, pub, err := GenerateKeyPair()
	require.NoError(t, err)
	err = mgr.AddPeer(&PeerConfig{
		PublicKey:  pub,
		AllowedIPs: []string{"not-a-cidr"},
	})
	assert.Error(t, err)

	// Invalid endpoint.
	err = mgr.AddPeer(&PeerConfig{
		PublicKey:  pub,
		AllowedIPs: []string{"10.0.0.2/32"},
		Endpoint:   "not-an-endpoint",
	})
	assert.Error(t, err)
}

// TestManagerKeyRotation tests key rotation.
func TestManagerKeyRotation(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("WireGuard kernel interface not available on macOS without additional setup")
	}
	if os.Getuid() != 0 {
		t.Skip("requires root privileges to configure WireGuard interfaces")
	}

	priv, _, err := GenerateKeyPair()
	require.NoError(t, err)

	cfg := DefaultConfig()
	cfg.PrivateKey = priv
	cfg.InterfaceName = "wg_test3"

	mgr, err := NewManager(cfg)
	require.NoError(t, err)

	err = mgr.Start()
	require.NoError(t, err)
	defer mgr.Stop()

	newPub, err := mgr.RotateKeys()
	require.NoError(t, err)
	assert.NotEmpty(t, newPub)

	// Verify config updated.
	assert.NotEqual(t, priv, mgr.config.PrivateKey)
}

// TestManagerKeyRotationPeriodic tests periodic key rotation.
func TestManagerKeyRotationPeriodic(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("WireGuard kernel interface not available on macOS without additional setup")
	}
	if os.Getuid() != 0 {
		t.Skip("requires root privileges to configure WireGuard interfaces")
	}

	priv, _, err := GenerateKeyPair()
	require.NoError(t, err)

	cfg := DefaultConfig()
	cfg.PrivateKey = priv
	cfg.InterfaceName = "wg_test4"

	mgr, err := NewManager(cfg)
	require.NoError(t, err)

	err = mgr.Start()
	require.NoError(t, err)
	defer mgr.Stop()

	oldKey := mgr.config.PrivateKey
	mgr.EnableKeyRotation(100 * time.Millisecond)
	time.Sleep(250 * time.Millisecond)
	mgr.DisableKeyRotation()

	assert.NotEqual(t, oldKey, mgr.config.PrivateKey)
}

// TestManagerInterfaceStats tests interface stats.
func TestManagerInterfaceStats(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("WireGuard kernel interface not available on macOS without additional setup")
	}
	if os.Getuid() != 0 {
		t.Skip("requires root privileges to configure WireGuard interfaces")
	}

	priv, _, err := GenerateKeyPair()
	require.NoError(t, err)

	cfg := DefaultConfig()
	cfg.PrivateKey = priv
	cfg.InterfaceName = "wg_test5"
	cfg.Address = "10.0.0.1/24"

	mgr, err := NewManager(cfg)
	require.NoError(t, err)

	err = mgr.Start()
	require.NoError(t, err)
	defer mgr.Stop()

	stats, err := mgr.InterfaceStats()
	require.NoError(t, err)
	assert.Equal(t, cfg.InterfaceName, stats.Name)
	assert.Equal(t, cfg.Address, stats.Address)
	assert.GreaterOrEqual(t, stats.Peers, 0)
}

// TestNATTraversalDiscovery tests external address discovery (mock-friendly).
func TestNATTraversalDiscovery(t *testing.T) {
	// The real implementation requires network; test the error path.
	addr, err := DiscoverExternalAddress(51820)
	// We expect an error because httpClient is not wired to net/http.
	assert.Error(t, err)
	assert.Empty(t, addr)
}

// TestNATTraversalPortMapping tests port mapping placeholders.
func TestNATTraversalPortMapping(t *testing.T) {
	ctx := context.Background()

	err := SetupPortMapping(ctx, 51820, 51820, time.Hour)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrUnsupported), "SetupPortMapping error must wrap ErrUnsupported")

	err = RemovePortMapping(ctx, 51820)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrUnsupported), "RemovePortMapping error must wrap ErrUnsupported")
}

// TestMeshCoordinatorSync tests mesh coordinator sync logic.
func TestMeshCoordinatorSync(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("WireGuard kernel interface not available on macOS without additional setup")
	}
	if os.Getuid() != 0 {
		t.Skip("requires root privileges to configure WireGuard interfaces")
	}

	priv, _, err := GenerateKeyPair()
	require.NoError(t, err)

	cfg := DefaultConfig()
	cfg.PrivateKey = priv
	cfg.InterfaceName = "wg_test6"

	mgr, err := NewManager(cfg)
	require.NoError(t, err)

	err = mgr.Start()
	require.NoError(t, err)
	defer mgr.Stop()

	// Create a minimal SWIM protocol.
	proto, err := swim.NewProtocol(&swim.Config{
		LocalID:  "node1",
		BindAddr: "127.0.0.1",
		BindPort: 7946,
	})
	require.NoError(t, err)
	mc := NewMeshCoordinator(mgr, proto)
	require.NotNil(t, mc)

	// Sync with empty members.
	err = mc.SyncNow()
	require.NoError(t, err)
}

// TestMeshCoordinatorMemberEvents tests member join/leave callbacks.
func TestMeshCoordinatorMemberEvents(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("WireGuard kernel interface not available on macOS without additional setup")
	}
	if os.Getuid() != 0 {
		t.Skip("requires root privileges to configure WireGuard interfaces")
	}

	priv, pub, err := GenerateKeyPair()
	require.NoError(t, err)

	cfg := DefaultConfig()
	cfg.PrivateKey = priv
	cfg.InterfaceName = "wg_test7"

	mgr, err := NewManager(cfg)
	require.NoError(t, err)

	err = mgr.Start()
	require.NoError(t, err)
	defer mgr.Stop()

	proto, err := swim.NewProtocol(&swim.Config{
		LocalID:  "node1",
		BindAddr: "127.0.0.1",
		BindPort: 7947,
	})
	require.NoError(t, err)
	mc := NewMeshCoordinator(mgr, proto)

	// Member joined with WireGuard metadata.
	member := &swim.Member{
		ID:      pub,
		Address: "10.0.0.2/32",
		State:   swim.StateAlive,
		Metadata: map[string]string{
			"wg_public_key": pub,
			"wg_endpoint":   "192.168.1.2:51820",
		},
	}
	mc.OnMemberJoined(member)

	peers, err := mgr.ListPeers()
	require.NoError(t, err)
	assert.Len(t, peers, 1)

	// Member left.
	mc.OnMemberLeft(pub)

	peers, err = mgr.ListPeers()
	require.NoError(t, err)
	assert.Len(t, peers, 0)

	// Suspect does nothing immediate.
	mc.OnMemberSuspect(pub)
	peers, err = mgr.ListPeers()
	require.NoError(t, err)
	assert.Len(t, peers, 0)
}

// TestMeshCoordinatorStartStop tests coordinator lifecycle.
func TestMeshCoordinatorStartStop(t *testing.T) {
	priv, _, err := GenerateKeyPair()
	require.NoError(t, err)

	cfg := DefaultConfig()
	cfg.PrivateKey = priv
	cfg.InterfaceName = "wg_test8"

	mgr, err := NewManager(cfg)
	require.NoError(t, err)

	proto, err := swim.NewProtocol(&swim.Config{
		LocalID:  "node1",
		BindAddr: "127.0.0.1",
		BindPort: 7948,
	})
	require.NoError(t, err)
	mc := NewMeshCoordinator(mgr, proto)

	err = mc.Start()
	require.NoError(t, err)

	time.Sleep(50 * time.Millisecond)

	err = mc.Stop()
	require.NoError(t, err)
}
