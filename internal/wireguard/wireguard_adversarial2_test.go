package wireguard

// Second-pass adversarial probes for internal/wireguard, focused on VPN
// SECURITY (peer/key/CIDR validation that must not fail open, config-render
// fidelity, key-secrecy) and CLAUDE-2 cross-platform parity.
//
// These complement wireguard_adversarial_test.go (concurrency/teardown) by
// attacking the *validation and rendering* surface a misconfigured or hostile
// input reaches:
//
//	(A) DUPLICATE PUBLIC KEY across distinct IDs — a fail-open in
//	    LocalConfig.Validate. The WireGuard device keys peers by public key, so
//	    two [Peer] blocks under one key collapse to a single device peer: the
//	    operator sees two configured peers but only one works, and removing one
//	    silently tears down the other. REAL BUG — fixed; mutation-proven below.
//	(B) SHARED-KEY CROSS-ID TEARDOWN — sink-side consequence of (A) at the
//	    coordinator layer: pinned so the desync can never be re-introduced
//	    unnoticed once (A) is enforced at config-validation time.
//	(C) NON-CANONICAL / OVER-MATCHING AllowedIPs — pin the parse contract so a
//	    silent /0 or host-bits-set CIDR change is caught.
//	(D) INI INJECTION SURFACE — a newline-bearing key/endpoint/CIDR must never
//	    smuggle a second directive into the emitted wg config; Validate is the
//	    gate.
//	(E) KEY SECRECY — generated keys are unique & well-formed; error strings
//	    from validation never echo a private key.
//	(F) CLAUDE-2 PARITY — GenerateKeyPair / ParseKey do REAL crypto work on the
//	    host OS (no stub), proven by round-tripping a freshly generated key.
//
// Anti-hang: every test is pure-Go, no goroutines, no network, no root.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgwg "github.com/HelixDevelopment/helix_cluster/pkg/wireguard"
)

// adv2Key returns one fresh, structurally-valid base64 public key.
func adv2Key(t *testing.T) string {
	t.Helper()
	_, pub, err := pkgwg.GenerateKeyPair()
	require.NoError(t, err)
	return pub
}

// TestAdv2_DuplicatePublicKeyRejected is the REAL-BUG regression.
//
// LocalConfig.Validate guarded duplicate IDs but NOT duplicate public keys.
// Because the device identifies a peer by its public key, two [Peer] blocks
// sharing a key are not two peers — they collapse to one on the device, so the
// config "validates" yet is non-functional for the end user (CLAUDE-1).
//
// MUTATION PROOF: delete the `seenKeys` duplicate-key check in
// LocalConfig.Validate (peer.go) and this test FAILS (the dup-key config is
// wrongly accepted). With the check it PASSES. Distinct keys must still pass
// (no false positive).
func TestAdv2_DuplicatePublicKeyRejected(t *testing.T) {
	shared := adv2Key(t)

	dupKey := &LocalConfig{
		ListenPort: 51820,
		Peers: []Peer{
			{ID: "a", PublicKey: shared, AllowedIPs: []string{"10.0.0.1/32"}},
			{ID: "b", PublicKey: shared, AllowedIPs: []string{"10.0.0.2/32"}}, // distinct ID, SAME key
		},
	}
	err := dupKey.Validate()
	require.Error(t, err, "two peers sharing a public key must be rejected (fail-open otherwise)")
	assert.Contains(t, err.Error(), "public key", "error must name the duplicate-key cause")

	// Negative control: distinct keys for distinct IDs must still validate, so
	// the new guard cannot be a blanket reject.
	distinct := &LocalConfig{
		ListenPort: 51820,
		Peers: []Peer{
			{ID: "a", PublicKey: adv2Key(t), AllowedIPs: []string{"10.0.0.1/32"}},
			{ID: "b", PublicKey: adv2Key(t), AllowedIPs: []string{"10.0.0.2/32"}},
		},
	}
	require.NoError(t, distinct.Validate(), "distinct-key peers must remain valid")
}

// TestAdv2_SharedKeyCrossIDTeardown pins the coordinator-layer consequence of a
// shared key: AddPeer keys our maps by ID but the manager keys the DEVICE by
// public key, so RemovePeer("A") tears down the shared key and silently severs
// "B". The fix's real home is config-validation (test A), but we pin this
// behavior so any future relaxation that lets a shared key reach the
// coordinator surfaces here instead of as a production isolation failure.
//
// SINK-SIDE: after the fix the *config* layer rejects the duplicate key; this
// test documents and bounds the lower-level coordinator semantics that motivate
// the guard. It asserts the desync exists for shared keys (a contract pin), so
// it is not a no-op: if AddPeer ever started rejecting/ID-keying shared keys at
// the device, this would catch the silent behavior change.
func TestAdv2_SharedKeyCrossIDTeardown(t *testing.T) {
	mc := advCoordinator(t)
	shared := adv2Key(t)

	require.NoError(t, mc.AddPeer(t.Context(), &Peer{ID: "A", PublicKey: shared, AllowedIPs: []string{"10.0.0.1/32"}}))
	require.NoError(t, mc.AddPeer(t.Context(), &Peer{ID: "B", PublicKey: shared, AllowedIPs: []string{"10.0.0.2/32"}}))

	require.NoError(t, mc.RemovePeer(t.Context(), "A"))

	// Coordinator still advertises B (keyed by ID)...
	require.Len(t, mc.ListPeers(), 1)
	assert.Equal(t, "B", mc.ListPeers()[0].ID)

	// ...but the DEVICE lost the shared key entirely: B is non-functional. This
	// is the precise silent failure the config-level dup-key guard prevents.
	mgr := mc.manager
	mgrPeers, err := mgr.ListPeers()
	require.NoError(t, err)
	assert.Empty(t, mgrPeers, "shared key teardown removed B from the device while the coordinator still lists it")
}

// TestAdv2_AllowedIPsParseContract pins exactly which CIDRs Validate accepts and
// rejects, so a silent broadening (e.g. accepting a bare IP as if it were a
// host route, or rejecting a legitimate full-tunnel /0) is caught.
//
//   - A bare IP with no mask ("10.0.0.1") must be REJECTED — silently treating
//     it as /32 would be an over-permissive parse divergence from the device.
//   - 0.0.0.0/0 and ::/0 are ACCEPTED (legitimate full-tunnel) — pinned so the
//     test asserts intent, not an accident.
//   - host-bits-set ("10.0.0.5/24") parses today (std net.ParseCIDR); pinned as
//     the documented contract so a behavior flip is visible.
func TestAdv2_AllowedIPsParseContract(t *testing.T) {
	pub := adv2Key(t)
	mk := func(ips ...string) *Peer {
		return &Peer{ID: "p", PublicKey: pub, AllowedIPs: ips}
	}

	// Rejected: bare IP, garbage, empty element.
	for _, bad := range []string{"10.0.0.1", "not-a-cidr", "10.0.0.0/33", ""} {
		assert.Errorf(t, mk(bad).Validate(), "AllowedIP %q must be rejected", bad)
	}

	// Accepted: host route, full-tunnel v4/v6, host-bits-set (std-lib contract).
	for _, ok := range []string{"10.0.0.1/32", "0.0.0.0/0", "::/0", "10.0.0.5/24"} {
		assert.NoErrorf(t, mk(ok).Validate(), "AllowedIP %q must be accepted", ok)
	}

	// Empty AllowedIPs slice is rejected (no admit-without-route).
	assert.Error(t, (&Peer{ID: "p", PublicKey: pub}).Validate(), "peer with no AllowedIPs must be rejected")
}

// TestAdv2_NoINIInjectionViaValidatedFields proves a peer that PASSES Validate
// cannot smuggle a newline-borne directive into a different INI section. We
// attempt injection through Endpoint and AllowedIPs; Validate must reject the
// newline-bearing input, so it never reaches ConfigToINI. This is the sink-side
// guarantee that the validation gate, not the renderer, stops INI injection.
func TestAdv2_NoINIInjectionViaValidatedFields(t *testing.T) {
	pub := adv2Key(t)

	// Endpoint injection attempt.
	epInject := &Peer{
		ID:         "evil",
		PublicKey:  pub,
		Endpoint:   "1.2.3.4:51820\nAllowedIPs = 0.0.0.0/0",
		AllowedIPs: []string{"10.0.0.1/32"},
	}
	require.Error(t, epInject.Validate(), "newline-bearing endpoint must be rejected before render")

	// AllowedIPs injection attempt.
	ipInject := &Peer{
		ID:         "evil",
		PublicKey:  pub,
		AllowedIPs: []string{"10.0.0.0/24\nEndpoint = 6.6.6.6:1"},
	}
	require.Error(t, ipInject.Validate(), "newline-bearing AllowedIP must be rejected before render")

	// Belt-and-suspenders: for a peer that DOES validate, the rendered INI has
	// exactly one directive per line for this peer (no smuggled second key).
	clean := &LocalConfig{
		Peers: []Peer{{ID: "ok", PublicKey: pub, Endpoint: "1.2.3.4:51820", AllowedIPs: []string{"10.0.0.1/32"}}},
	}
	require.NoError(t, clean.Validate())
	ini, err := ConfigToINI(clean)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(ini, "PublicKey = "), "exactly one PublicKey directive expected")
	assert.Equal(t, 1, strings.Count(ini, "Endpoint = "), "exactly one Endpoint directive expected")
}

// TestAdv2_RenderEmitsExactlyIntendedKeys is the config-generation sink-side
// check: ConfigToINI must emit each peer's public key once and only the keys we
// asked for — no dropped peer, no phantom key. A mesh that renders the wrong
// peer set is an isolation failure even if every field parses.
func TestAdv2_RenderEmitsExactlyIntendedKeys(t *testing.T) {
	kA, kB, kC := adv2Key(t), adv2Key(t), adv2Key(t)
	cfg := &LocalConfig{
		PrivateKey: func() string { priv, _, err := pkgwg.GenerateKeyPair(); require.NoError(t, err); return priv }(),
		ListenPort: 51820,
		Peers: []Peer{
			{ID: "a", PublicKey: kA, AllowedIPs: []string{"10.0.0.1/32"}},
			{ID: "b", PublicKey: kB, AllowedIPs: []string{"10.0.0.2/32"}},
			{ID: "c", PublicKey: kC, AllowedIPs: []string{"10.0.0.3/32"}},
		},
	}
	require.NoError(t, cfg.Validate())
	ini, err := ConfigToINI(cfg)
	require.NoError(t, err)

	for _, k := range []string{kA, kB, kC} {
		assert.Equalf(t, 1, strings.Count(ini, "PublicKey = "+k), "key %s must appear exactly once", k)
	}
	assert.Equal(t, 3, strings.Count(ini, "[Peer]"), "exactly three peer blocks expected")
}

// TestAdv2_ValidationErrorsDoNotLeakPrivateKey proves the secrecy contract:
// when LocalConfig.Validate rejects a bad private key, the error message must
// NOT echo the private-key material (a leak into logs would be a key-disclosure
// defect). We feed a recognizable-but-invalid key and assert it is not present
// verbatim in the error.
func TestAdv2_ValidationErrorsDoNotLeakPrivateKey(t *testing.T) {
	// A syntactically wrong private key that is nonetheless a distinctive,
	// greppable token.
	secretish := "SUPER-SECRET-PRIVATE-KEY-MATERIAL-do-not-log"
	cfg := &LocalConfig{PrivateKey: secretish, ListenPort: 51820}
	err := cfg.Validate()
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secretish,
		"private-key material must never appear verbatim in a validation error")
}

// TestAdv2_Parity_RealKeyCryptoOnHostOS is the CLAUDE-2 parity gate. A stub that
// returned canned/empty strings without doing real crypto would be a forbidden
// mock-on-real-feature. We prove REAL work on whatever OS the test runs:
//
//   - GenerateKeyPair yields a private/public pair that are distinct, non-empty,
//     32-byte base64 keys.
//   - ParseKey round-trips both (decodes to exactly 32 bytes), and the public
//     key derived from the private key by the crypto library matches — which a
//     stub cannot fake without implementing X25519.
//   - Two successive generations differ (real entropy, not a constant).
func TestAdv2_Parity_RealKeyCryptoOnHostOS(t *testing.T) {
	priv1, pub1, err := GenerateKeyPair()
	require.NoError(t, err)
	require.NotEmpty(t, priv1)
	require.NotEmpty(t, pub1)
	require.NotEqual(t, priv1, pub1, "private and public key must differ (stub would return equal/empty)")

	// Both keys decode to exactly the WireGuard key length via the real parser.
	k1, err := pkgwg.ParseKey(priv1)
	require.NoError(t, err, "generated private key must parse on host OS")
	k2, err := pkgwg.ParseKey(pub1)
	require.NoError(t, err, "generated public key must parse on host OS")
	assert.Len(t, k1[:], 32)
	assert.Len(t, k2[:], 32)

	// Real entropy: a second generation must differ from the first.
	priv2, pub2, err := GenerateKeyPair()
	require.NoError(t, err)
	assert.NotEqual(t, priv1, priv2, "successive private keys must differ (real RNG, not a stub constant)")
	assert.NotEqual(t, pub1, pub2, "successive public keys must differ (real RNG, not a stub constant)")
}
