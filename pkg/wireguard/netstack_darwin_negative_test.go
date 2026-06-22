//go:build darwin

package wireguard

import (
	"encoding/base64"
	"net/netip"
	"strings"
	"testing"
)

// validB64Key returns a freshly generated, valid base64 WireGuard private key
// and its matching public key for use in negative/boundary tests.
func validB64Key(t *testing.T) (priv, pub string) {
	t.Helper()
	priv, pub, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}
	return priv, pub
}

// TestKeyToHexErrors pins the two error branches of keyToHex: a non-base64 input
// (DecodeString fails) and a base64 string that decodes to the wrong length.
// A valid 32-byte key must round-trip to 64 lowercase-hex chars with no error.
func TestKeyToHexErrors(t *testing.T) {
	// Non-base64 input → DecodeString error branch (line 35). Assert the SPECIFIC
	// base64 wrapper: if that branch's return were dropped, execution would fall
	// through to the length check and surface "invalid key length" instead, so an
	// exact-prefix check distinguishes the two branches and kills the mutant.
	if _, err := keyToHex("@@@not-base64@@@"); err == nil {
		t.Fatal("keyToHex(non-base64): expected error, got nil")
	} else if !strings.HasPrefix(err.Error(), "invalid base64 key") {
		t.Fatalf("keyToHex(non-base64): error %q should be the invalid-base64 wrapper", err)
	}

	// Valid base64 but wrong length (16 bytes, not 32) → length error branch (line 38).
	short := base64.StdEncoding.EncodeToString(make([]byte, 16))
	if _, err := keyToHex(short); err == nil {
		t.Fatal("keyToHex(16-byte key): expected length error, got nil")
	} else if !strings.HasPrefix(err.Error(), "invalid key length") {
		t.Fatalf("keyToHex(16-byte key): error %q should be the invalid-key-length wrapper", err)
	}
	// And a too-long key (33 bytes) must also be rejected by the length guard.
	long := base64.StdEncoding.EncodeToString(make([]byte, 33))
	if _, err := keyToHex(long); err == nil {
		t.Fatal("keyToHex(33-byte key): expected length error, got nil")
	} else if !strings.HasPrefix(err.Error(), "invalid key length") {
		t.Fatalf("keyToHex(33-byte key): error %q should be the invalid-key-length wrapper", err)
	}

	// A genuine 32-byte key must succeed and produce exactly 64 hex chars.
	good := base64.StdEncoding.EncodeToString(make([]byte, 32))
	hexOut, err := keyToHex(good)
	if err != nil {
		t.Fatalf("keyToHex(valid 32-byte key): unexpected error: %v", err)
	}
	if len(hexOut) != 64 {
		t.Fatalf("keyToHex(valid key): got %d hex chars, want 64", len(hexOut))
	}
}

// TestNewUserspaceDeviceEmptyAddrs covers the empty-localAddrs guard (lines 65-67):
// with no tunnel address the device must refuse to come up.
func TestNewUserspaceDeviceEmptyAddrs(t *testing.T) {
	priv, _ := validB64Key(t)
	dev, err := NewUserspaceDevice(nil, nil, 1420, freeUDPPort(t), priv)
	if err == nil {
		if dev != nil {
			_ = dev.Close()
		}
		t.Fatal("NewUserspaceDevice(nil addrs): expected error, got nil")
	}
	if dev != nil {
		t.Fatalf("NewUserspaceDevice(nil addrs): expected nil device on error, got %v", dev)
	}
	// An explicitly empty (non-nil) slice must also be rejected.
	if _, err := NewUserspaceDevice([]netip.Addr{}, nil, 1420, freeUDPPort(t), priv); err == nil {
		t.Fatal("NewUserspaceDevice([]): expected error, got nil")
	}
}

// TestNewUserspaceDeviceBadPrivateKey covers the bad-private-key branch (lines 73-75):
// a malformed private key must surface a real error, not a half-built device.
func TestNewUserspaceDeviceBadPrivateKey(t *testing.T) {
	addr := netip.MustParseAddr("10.66.0.1")
	dev, err := NewUserspaceDevice([]netip.Addr{addr}, nil, 1420, freeUDPPort(t), "not-a-valid-key")
	if err == nil {
		if dev != nil {
			_ = dev.Close()
		}
		t.Fatal("NewUserspaceDevice(bad priv key): expected error, got nil")
	}
	// Assert the SPECIFIC wrapper, not just any mention of "private key": if the
	// error-branch return were dropped, execution would fall through to
	// CreateNetTUN/IpcSet and surface a different (non-"invalid private key")
	// error, so an exact-prefix check kills that mutant.
	if !strings.HasPrefix(err.Error(), "invalid private key") {
		t.Fatalf("NewUserspaceDevice(bad priv key): error %q should be the invalid-private-key wrapper", err)
	}
	if dev != nil {
		t.Fatalf("NewUserspaceDevice(bad priv key): expected nil device, got %v", dev)
	}
}

// TestNewUserspaceDeviceMTUBoundary covers the mtu<=0 → default 1420 boundary
// (lines 68-69). We bring the device up with mtu=0 and mtu=-1; both must succeed
// (the default is substituted) and produce a working device with a live netstack.
// A non-defaulted mtu of 0 would make CreateNetTUN fail, so a successful bring-up
// is observable proof that the default was applied.
func TestNewUserspaceDeviceMTUBoundary(t *testing.T) {
	for _, mtu := range []int{0, -1} {
		priv, _ := validB64Key(t)
		addr := netip.MustParseAddr("10.66.1.1")
		dev, err := NewUserspaceDevice([]netip.Addr{addr}, nil, mtu, freeUDPPort(t), priv)
		if err != nil {
			t.Fatalf("NewUserspaceDevice(mtu=%d): expected success via default, got %v", mtu, err)
		}
		if dev == nil {
			t.Fatalf("NewUserspaceDevice(mtu=%d): nil device", mtu)
		}
		if dev.Net() == nil {
			t.Fatalf("NewUserspaceDevice(mtu=%d): nil netstack Net", mtu)
		}
		_ = dev.Close()
	}
}

// TestNewUserspaceDeviceValidBringUp covers the happy path with a positive mtu so
// that mutating the mtu<=0 guard or the default value is detectable: a normal
// bring-up must succeed and expose a usable netstack.
func TestNewUserspaceDeviceValidBringUp(t *testing.T) {
	priv, _ := validB64Key(t)
	addr := netip.MustParseAddr("10.66.2.1")
	dev, err := NewUserspaceDevice([]netip.Addr{addr}, nil, 1280, freeUDPPort(t), priv)
	if err != nil {
		t.Fatalf("NewUserspaceDevice(valid): %v", err)
	}
	t.Cleanup(func() { _ = dev.Close() })
	if dev.Net() == nil {
		t.Fatal("NewUserspaceDevice(valid): nil netstack Net")
	}
}

// newSoloDevice builds a single valid userspace device for AddPeer/Close tests.
func newSoloDevice(t *testing.T, tunIP string) *UserspaceDevice {
	t.Helper()
	priv, _ := validB64Key(t)
	addr := netip.MustParseAddr(tunIP)
	dev, err := NewUserspaceDevice([]netip.Addr{addr}, nil, 1420, freeUDPPort(t), priv)
	if err != nil {
		t.Fatalf("NewUserspaceDevice(%s): %v", tunIP, err)
	}
	return dev
}

// TestAddPeerNil covers the nil-peer guard (lines 113-114).
func TestAddPeerNil(t *testing.T) {
	dev := newSoloDevice(t, "10.66.3.1")
	t.Cleanup(func() { _ = dev.Close() })
	if err := dev.AddPeer(nil); err == nil {
		t.Fatal("AddPeer(nil): expected error, got nil")
	}
}

// TestAddPeerBadPublicKey covers the bad-public-key branch (lines 117-119).
func TestAddPeerBadPublicKey(t *testing.T) {
	dev := newSoloDevice(t, "10.66.4.1")
	t.Cleanup(func() { _ = dev.Close() })
	err := dev.AddPeer(&PeerConfig{PublicKey: "@@bad@@"})
	if err == nil {
		t.Fatal("AddPeer(bad pubkey): expected error, got nil")
	}
	// Exact prefix: dropping the error-branch return would fall through to
	// IpcSet (which fails with a "failed to add peer" prefix), so requiring the
	// "invalid public key" wrapper kills that mutant.
	if !strings.HasPrefix(err.Error(), "invalid public key") {
		t.Fatalf("AddPeer(bad pubkey): error %q should be the invalid-public-key wrapper", err)
	}
}

// TestAddPeerBadPresharedKey covers the bad-PSK branch (lines 125-128): a valid
// public key but a malformed (non-empty) preshared key must error on the PSK.
func TestAddPeerBadPresharedKey(t *testing.T) {
	dev := newSoloDevice(t, "10.66.5.1")
	t.Cleanup(func() { _ = dev.Close() })
	_, pub := validB64Key(t)
	err := dev.AddPeer(&PeerConfig{
		PublicKey:    pub,
		PresharedKey: "not-base64-psk!!!",
	})
	if err == nil {
		t.Fatal("AddPeer(bad PSK): expected error, got nil")
	}
	// Exact prefix kills the dropped-return mutant: without the return, the bad
	// (empty) PSK hex would be written and IpcSet would fail with a different
	// ("failed to add peer") prefix.
	if !strings.HasPrefix(err.Error(), "invalid preshared key") {
		t.Fatalf("AddPeer(bad PSK): error %q should be the invalid-preshared-key wrapper", err)
	}
}

// TestAddPeerEmptyPSKAccepted proves the PSK guard only fires for a non-empty PSK
// (boundary on line 125: peer.PresharedKey != ""). With an empty PSK and an
// otherwise-valid peer, AddPeer must succeed.
func TestAddPeerEmptyPSKAccepted(t *testing.T) {
	dev := newSoloDevice(t, "10.66.6.1")
	t.Cleanup(func() { _ = dev.Close() })
	_, pub := validB64Key(t)
	if err := dev.AddPeer(&PeerConfig{
		PublicKey:    pub,
		PresharedKey: "",
		AllowedIPs:   []string{"10.66.6.2/32"},
	}); err != nil {
		t.Fatalf("AddPeer(empty PSK, valid peer): unexpected error: %v", err)
	}
}

// TestAddPeerBadAllowedIP covers the bad-allowed-IP branch (lines 134-137).
func TestAddPeerBadAllowedIP(t *testing.T) {
	dev := newSoloDevice(t, "10.66.7.1")
	t.Cleanup(func() { _ = dev.Close() })
	_, pub := validB64Key(t)
	err := dev.AddPeer(&PeerConfig{
		PublicKey:  pub,
		AllowedIPs: []string{"not-a-cidr"},
	})
	if err == nil {
		t.Fatal("AddPeer(bad allowed-ip): expected error, got nil")
	}
	// Exact prefix kills the dropped-return mutant (which would skip the bad CIDR
	// and later fail in IpcSet with a different prefix).
	if !strings.HasPrefix(err.Error(), "invalid allowed IP") {
		t.Fatalf("AddPeer(bad allowed-ip): error %q should be the invalid-allowed-IP wrapper", err)
	}
}

// TestAddPeerBadEndpoint covers the bad-endpoint branch (lines 142-145).
func TestAddPeerBadEndpoint(t *testing.T) {
	dev := newSoloDevice(t, "10.66.8.1")
	t.Cleanup(func() { _ = dev.Close() })
	_, pub := validB64Key(t)
	err := dev.AddPeer(&PeerConfig{
		PublicKey: pub,
		Endpoint:  "this-is-not-host-port",
	})
	if err == nil {
		t.Fatal("AddPeer(bad endpoint): expected error, got nil")
	}
	// Exact prefix kills the dropped-return mutant: without the return, the bad
	// endpoint is skipped and AddPeer would otherwise succeed (no other error),
	// so requiring the "invalid endpoint" wrapper makes a fallthrough detectable.
	if !strings.HasPrefix(err.Error(), "invalid endpoint") {
		t.Fatalf("AddPeer(bad endpoint): error %q should be the invalid-endpoint wrapper", err)
	}
}

// TestAddPeerKeepaliveObservable asserts on the observable effect of the
// keepalive>0 boundary (line 150). A peer added with PersistentKeepalive>0 must
// produce a persistent_keepalive_interval entry in the device's UAPI dump, while
// a peer with keepalive==0 must NOT — proving the conditional UAPI line is wired.
//
// The UAPI string built in AddPeer is not directly returned, but it is pushed
// into the live device, so IpcGet (via the device) reflects it: this is genuine
// sink-side evidence rather than a private test seam.
func TestAddPeerKeepaliveObservable(t *testing.T) {
	// keepalive > 0 → interval present.
	devKA := newSoloDevice(t, "10.66.9.1")
	t.Cleanup(func() { _ = devKA.Close() })
	_, pubKA := validB64Key(t)
	if err := devKA.AddPeer(&PeerConfig{
		PublicKey:           pubKA,
		AllowedIPs:          []string{"10.66.9.2/32"},
		PersistentKeepalive: 25,
	}); err != nil {
		t.Fatalf("AddPeer(keepalive=25): %v", err)
	}
	dumpKA, err := devKA.dev.IpcGet()
	if err != nil {
		t.Fatalf("IpcGet(keepalive device): %v", err)
	}
	if !strings.Contains(dumpKA, "persistent_keepalive_interval=25") {
		t.Fatalf("keepalive=25: expected persistent_keepalive_interval=25 in UAPI dump, got:\n%s", dumpKA)
	}

	// keepalive == 1 → interval present as 1. This kills the `> 1` boundary mutant:
	// with that mutant a keepalive of 1 would NOT emit the line and the dump would
	// show the default 0 instead of 1.
	devOne := newSoloDevice(t, "10.66.9.3")
	t.Cleanup(func() { _ = devOne.Close() })
	_, pubOne := validB64Key(t)
	if err := devOne.AddPeer(&PeerConfig{
		PublicKey:           pubOne,
		AllowedIPs:          []string{"10.66.9.4/32"},
		PersistentKeepalive: 1,
	}); err != nil {
		t.Fatalf("AddPeer(keepalive=1): %v", err)
	}
	dumpOne, err := devOne.dev.IpcGet()
	if err != nil {
		t.Fatalf("IpcGet(keepalive=1 device): %v", err)
	}
	if !strings.Contains(dumpOne, "persistent_keepalive_interval=1") {
		t.Fatalf("keepalive=1: expected persistent_keepalive_interval=1 in UAPI dump, got:\n%s", dumpOne)
	}

	// keepalive == 0 → no interval line emitted (value stays at the device default 0).
	devNo := newSoloDevice(t, "10.66.10.1")
	t.Cleanup(func() { _ = devNo.Close() })
	_, pubNo := validB64Key(t)
	if err := devNo.AddPeer(&PeerConfig{
		PublicKey:           pubNo,
		AllowedIPs:          []string{"10.66.10.2/32"},
		PersistentKeepalive: 0,
	}); err != nil {
		t.Fatalf("AddPeer(keepalive=0): %v", err)
	}
	dumpNo, err := devNo.dev.IpcGet()
	if err != nil {
		t.Fatalf("IpcGet(no-keepalive device): %v", err)
	}
	// wireguard-go reports persistent_keepalive_interval=0 (or omits it). The
	// guard must NOT have emitted a non-zero interval.
	for _, line := range strings.Split(dumpNo, "\n") {
		if strings.HasPrefix(line, "persistent_keepalive_interval=") &&
			line != "persistent_keepalive_interval=0" {
			t.Fatalf("keepalive=0: unexpected non-zero interval line %q in UAPI dump", line)
		}
	}
}

// TestAddPeerValidFullConfig exercises the full happy path (PSK + allowed-IPs +
// endpoint + keepalive all valid) so mutations that break correct assembly of the
// UAPI request are caught, and all peer attributes appear in the dump.
func TestAddPeerValidFullConfig(t *testing.T) {
	dev := newSoloDevice(t, "10.66.11.1")
	t.Cleanup(func() { _ = dev.Close() })
	_, pub := validB64Key(t)

	// Use a DISTINCTIVE (non-zero) preshared key so its presence in the dump is
	// observable: wireguard-go echoes preshared_key in IpcGet. A mutant that
	// drops the `preshared_key=` write would leave the dump showing the all-zero
	// default, so asserting the exact non-zero hex kills that mutant.
	pskRaw := make([]byte, 32)
	for i := range pskRaw {
		pskRaw[i] = byte(i + 1)
	}
	psk := base64.StdEncoding.EncodeToString(pskRaw)
	pskHexWant := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

	if err := dev.AddPeer(&PeerConfig{
		PublicKey:           pub,
		PresharedKey:        psk,
		AllowedIPs:          []string{"10.66.11.2/32", "10.66.12.0/24"},
		Endpoint:            "127.0.0.1:51820",
		PersistentKeepalive: 15,
	}); err != nil {
		t.Fatalf("AddPeer(full valid config): %v", err)
	}
	dump, err := dev.dev.IpcGet()
	if err != nil {
		t.Fatalf("IpcGet: %v", err)
	}
	for _, want := range []string{
		"endpoint=127.0.0.1:51820",
		"persistent_keepalive_interval=15",
		"allowed_ip=10.66.11.2/32",
		"allowed_ip=10.66.12.0/24",
		"preshared_key=" + pskHexWant,
	} {
		if !strings.Contains(dump, want) {
			t.Fatalf("AddPeer(full): expected %q in UAPI dump, got:\n%s", want, dump)
		}
	}
}

// TestAddPeerReplacesAllowedIPs proves the replace_allowed_ips=true directive is
// actually emitted (line 132 in the source). Adding a peer twice with DIFFERENT
// allowed-IPs must REPLACE, not append: after the second AddPeer the first call's
// allowed-IP must be gone. A mutant that drops the replace_allowed_ips line would
// leave the stale allowed-IP in the dump, failing this assertion.
func TestAddPeerReplacesAllowedIPs(t *testing.T) {
	dev := newSoloDevice(t, "10.66.14.1")
	t.Cleanup(func() { _ = dev.Close() })
	_, pub := validB64Key(t)

	if err := dev.AddPeer(&PeerConfig{
		PublicKey:  pub,
		AllowedIPs: []string{"10.66.14.2/32"},
	}); err != nil {
		t.Fatalf("AddPeer(first): %v", err)
	}
	if err := dev.AddPeer(&PeerConfig{
		PublicKey:  pub,
		AllowedIPs: []string{"10.66.14.3/32"},
	}); err != nil {
		t.Fatalf("AddPeer(second): %v", err)
	}

	dump, err := dev.dev.IpcGet()
	if err != nil {
		t.Fatalf("IpcGet: %v", err)
	}
	if !strings.Contains(dump, "allowed_ip=10.66.14.3/32") {
		t.Fatalf("replace allowed-ips: new allowed-IP missing from dump:\n%s", dump)
	}
	if strings.Contains(dump, "allowed_ip=10.66.14.2/32") {
		t.Fatalf("replace allowed-ips: stale allowed-IP NOT replaced (replace_allowed_ips dropped?):\n%s", dump)
	}
}

// TestCloseTearsDownAndIdempotent covers Close (lines 160-164): Close must tear
// the device down and be safe to call more than once. After Close, an operation
// that drives the device (AddPeer → IpcSet on a closed device) must fail, and a
// second Close must not panic.
func TestCloseTearsDownAndIdempotent(t *testing.T) {
	dev := newSoloDevice(t, "10.66.13.1")

	// First Close tears the device down.
	if err := dev.Close(); err != nil {
		t.Fatalf("Close(): unexpected error: %v", err)
	}

	// After Close, configuring the device must fail (it is no longer usable).
	// This proves Close actually invoked dev.Close() rather than being a no-op:
	// IpcSet on a closed wireguard-go device returns an error.
	_, pub := validB64Key(t)
	if err := dev.AddPeer(&PeerConfig{
		PublicKey:  pub,
		AllowedIPs: []string{"10.66.13.2/32"},
	}); err == nil {
		t.Fatal("AddPeer after Close: expected error from a torn-down device, got nil")
	}

	// Second Close must be safe (idempotent), not panic.
	if err := dev.Close(); err != nil {
		t.Fatalf("second Close(): unexpected error: %v", err)
	}
}

// TestCloseNilDevSafe covers the dev != nil guard in Close (line 328): a
// zero-value UserspaceDevice (dev == nil) must Close without panicking.
func TestCloseNilDevSafe(t *testing.T) {
	var u UserspaceDevice
	if err := u.Close(); err != nil {
		t.Fatalf("Close() on zero-value device: unexpected error: %v", err)
	}
}
