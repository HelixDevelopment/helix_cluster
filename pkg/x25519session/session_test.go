package x25519session

import (
	"bytes"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"testing"
)

// newRunUUID returns a fresh random 16-byte hex identifier used to tag each
// test run in the logs (CLAUDE-1 observability requirement).
func newRunUUID(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand.Read for run-uuid: %v", err)
	}
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 32)
	for i, v := range b {
		out[i*2] = hexdigits[v>>4]
		out[i*2+1] = hexdigits[v&0x0f]
	}
	return string(out)
}

// TestMatchingDerivationYieldsIdenticalSessionKey covers CLOSURE clause 1:
// node and validator each generate ephemeral keys, exchange public keys, and
// independently derive the per-session HMAC key. The two keys MUST be equal.
//
// Mutation: if DeriveSessionKey ignored the private scalar (e.g. derived from
// only one public key or a constant), the two sides would not agree here.
func TestMatchingDerivationYieldsIdenticalSessionKey(t *testing.T) {
	runUUID := newRunUUID(t)
	const label = "node<->validator:handshake:epoch-7"
	t.Logf("run=%s BEFORE: generating ephemeral keypairs for node and validator, label=%q", runUUID, label)

	nodePriv, nodePub, err := GenerateEphemeral(rand.Reader)
	if err != nil {
		t.Fatalf("run=%s node GenerateEphemeral: %v", runUUID, err)
	}
	valPriv, valPub, err := GenerateEphemeral(rand.Reader)
	if err != nil {
		t.Fatalf("run=%s validator GenerateEphemeral: %v", runUUID, err)
	}

	// Distinct keypairs are expected from independent generation.
	if bytes.Equal(nodePub.Bytes(), valPub.Bytes()) {
		t.Fatalf("run=%s node and validator generated identical public keys; keygen broken", runUUID)
	}

	// Node derives with its OWN private key against the validator's public key.
	nodeKey, err := DeriveSessionKey(nodePriv, valPub, label)
	if err != nil {
		t.Fatalf("run=%s node DeriveSessionKey: %v", runUUID, err)
	}
	// Validator derives with its OWN private key against the node's public key.
	valKey, err := DeriveSessionKey(valPriv, nodePub, label)
	if err != nil {
		t.Fatalf("run=%s validator DeriveSessionKey: %v", runUUID, err)
	}

	if len(nodeKey) != SessionKeyLength {
		t.Fatalf("run=%s derived key length = %d, want %d", runUUID, len(nodeKey), SessionKeyLength)
	}
	// The core handshake property: both sides agree on the SAME secret key.
	if !bytes.Equal(nodeKey, valKey) {
		t.Fatalf("run=%s node-side key %x != validator-side key %x; ECDH symmetry broken", runUUID, nodeKey, valKey)
	}

	// Independent oracle: recompute the expected key directly from the raw ECDH
	// secret + HKDF, proving the derived value is genuinely the HKDF output of
	// the X25519 shared secret (not some unrelated/guessable constant). The
	// expected value is DERIVED from the inputs, not hardcoded.
	rawShared, err := nodePriv.ECDH(valPub)
	if err != nil {
		t.Fatalf("run=%s oracle ECDH: %v", runUUID, err)
	}
	wantKey, err := hkdf.Key(sha256.New, rawShared, hkdfSalt, label, SessionKeyLength)
	if err != nil {
		t.Fatalf("run=%s oracle hkdf.Key: %v", runUUID, err)
	}
	if !bytes.Equal(nodeKey, wantKey) {
		t.Fatalf("run=%s derived key %x != independently computed HKDF(ECDH) %x", runUUID, nodeKey, wantKey)
	}
	// And the raw shared secret alone must NOT equal the derived key: HKDF must
	// actually be applied (mutation: skipping HKDF and returning the raw secret
	// would be detected here).
	if bytes.Equal(rawShared, nodeKey) {
		t.Fatalf("run=%s derived key equals raw ECDH secret; HKDF not applied", runUUID)
	}

	t.Logf("run=%s AFTER: agreed session key=%x (len=%d), matches HKDF(ECDH) oracle, differs from raw secret",
		runUUID, nodeKey, len(nodeKey))
}

// TestThirdPartyDerivesDifferentKey covers CLOSURE clause 2: an attacker with a
// DIFFERENT keypair, deriving against the same node public key, gets a
// DIFFERENT key and cannot reproduce the honest session key.
//
// Mutation: if derivation ignored the private scalar (constant/one-sided), the
// attacker's key would match the honest key and this test would fail.
func TestThirdPartyDerivesDifferentKey(t *testing.T) {
	runUUID := newRunUUID(t)
	const label = "node<->validator:handshake:epoch-7"
	t.Logf("run=%s BEFORE: node+validator agree a key; a third party attacks the same node pub, label=%q", runUUID, label)

	nodePriv, nodePub, err := GenerateEphemeral(rand.Reader)
	if err != nil {
		t.Fatalf("run=%s node keygen: %v", runUUID, err)
	}
	valPriv, valPub, err := GenerateEphemeral(rand.Reader)
	if err != nil {
		t.Fatalf("run=%s validator keygen: %v", runUUID, err)
	}
	attackerPriv, attackerPub, err := GenerateEphemeral(rand.Reader)
	if err != nil {
		t.Fatalf("run=%s attacker keygen: %v", runUUID, err)
	}

	// The honest, agreed key.
	honestKey, err := DeriveSessionKey(valPriv, nodePub, label)
	if err != nil {
		t.Fatalf("run=%s validator derive: %v", runUUID, err)
	}
	// Sanity: node side agrees (ties this test to the real handshake).
	nodeKey, err := DeriveSessionKey(nodePriv, valPub, label)
	if err != nil {
		t.Fatalf("run=%s node derive: %v", runUUID, err)
	}
	if !bytes.Equal(honestKey, nodeKey) {
		t.Fatalf("run=%s honest parties disagree; precondition broken", runUUID)
	}

	// Attacker derives with its OWN private key against the node's public key.
	attackerKey, err := DeriveSessionKey(attackerPriv, nodePub, label)
	if err != nil {
		t.Fatalf("run=%s attacker derive: %v", runUUID, err)
	}
	if bytes.Equal(attackerKey, honestKey) {
		t.Fatalf("run=%s attacker reproduced the honest session key %x; ECDH does not bind the private scalar", runUUID, honestKey)
	}

	// The attacker also cannot match by impersonating the node toward the
	// validator: validator derives against the attacker's pub, which differs.
	valVsAttacker, err := DeriveSessionKey(valPriv, attackerPub, label)
	if err != nil {
		t.Fatalf("run=%s validator-vs-attacker derive: %v", runUUID, err)
	}
	if bytes.Equal(valVsAttacker, honestKey) {
		t.Fatalf("run=%s validator+attacker accidentally equals honest key; keys not bound to peer", runUUID)
	}

	t.Logf("run=%s AFTER: honest=%x attacker=%x (differ=%t)", runUUID, honestKey, attackerKey, !bytes.Equal(honestKey, attackerKey))
}

// TestDifferentLabelDerivesDifferentKey covers CLOSURE clause 3: from the SAME
// ECDH shared secret, two different session labels (HKDF info) produce DIFFERENT
// keys.
//
// Mutation: if the session label were dropped from the HKDF info, both labels
// would yield the same key and this test would fail.
func TestDifferentLabelDerivesDifferentKey(t *testing.T) {
	runUUID := newRunUUID(t)
	const labelA = "session:alpha"
	const labelB = "session:beta"
	t.Logf("run=%s BEFORE: same keypair pair, two labels %q vs %q", runUUID, labelA, labelB)

	nodePriv, nodePub, err := GenerateEphemeral(rand.Reader)
	if err != nil {
		t.Fatalf("run=%s node keygen: %v", runUUID, err)
	}
	valPriv, valPub, err := GenerateEphemeral(rand.Reader)
	if err != nil {
		t.Fatalf("run=%s validator keygen: %v", runUUID, err)
	}

	keyA, err := DeriveSessionKey(nodePriv, valPub, labelA)
	if err != nil {
		t.Fatalf("run=%s derive labelA: %v", runUUID, err)
	}
	keyB, err := DeriveSessionKey(nodePriv, valPub, labelB)
	if err != nil {
		t.Fatalf("run=%s derive labelB: %v", runUUID, err)
	}

	if bytes.Equal(keyA, keyB) {
		t.Fatalf("run=%s different labels produced identical keys %x; HKDF info not honoured", runUUID, keyA)
	}

	// Cross-check: the validator side, using the SAME label, still agrees with
	// the node for each label respectively — domain separation does not break
	// the matching property.
	valKeyA, err := DeriveSessionKey(valPriv, nodePub, labelA)
	if err != nil {
		t.Fatalf("run=%s validator derive labelA: %v", runUUID, err)
	}
	if !bytes.Equal(keyA, valKeyA) {
		t.Fatalf("run=%s labelA: node and validator disagree %x vs %x", runUUID, keyA, valKeyA)
	}

	t.Logf("run=%s AFTER: keyA=%x keyB=%x (differ=%t), validator agrees on labelA",
		runUUID, keyA, keyB, !bytes.Equal(keyA, keyB))
}

// TestInputValidation pins the error contract for empty labels and nil keys.
//
// Mutation: removing either guard would change these from errors to either a
// derived key or a panic, failing this test.
func TestInputValidation(t *testing.T) {
	runUUID := newRunUUID(t)
	priv, pub, err := GenerateEphemeral(rand.Reader)
	if err != nil {
		t.Fatalf("run=%s keygen: %v", runUUID, err)
	}
	t.Logf("run=%s BEFORE: validating empty-label and nil-key rejection", runUUID)

	if _, err := DeriveSessionKey(priv, pub, ""); err != ErrEmptyLabel {
		t.Fatalf("run=%s empty label: got err=%v, want ErrEmptyLabel", runUUID, err)
	}
	if _, err := DeriveSessionKey(nil, pub, "x"); err != ErrNilKey {
		t.Fatalf("run=%s nil priv: got err=%v, want ErrNilKey", runUUID, err)
	}
	if _, err := DeriveSessionKey(priv, nil, "x"); err != ErrNilKey {
		t.Fatalf("run=%s nil pub: got err=%v, want ErrNilKey", runUUID, err)
	}

	t.Logf("run=%s AFTER: ErrEmptyLabel and ErrNilKey returned as specified", runUUID)
}

// ensure ecdh import is genuinely exercised at the type level.
var _ = ecdh.X25519
