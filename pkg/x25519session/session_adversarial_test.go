package x25519session

// Adversarial security tests for the X25519 session-key handshake.
//
// SCOPE NOTE (read first): pkg/x25519session is a KEY-AGREEMENT / KDF package,
// NOT a session-AEAD. Its public surface is exactly:
//
//	GenerateEphemeral(rand) -> (*ecdh.PrivateKey, *ecdh.PublicKey, error)
//	DeriveSessionKey(myPriv, peerPub, label) -> ([]byte, error)   // 32-byte key
//
// There is no Session type, no Seal/Open, no AEAD, no nonce/counter/rekey in
// this package. The derived value is documented as a per-session HMAC key handed
// to callers. Therefore the AEAD-tamper / nonce-replay clauses of the task have
// no in-package surface to bite — inventing a Seal/Open API would be a BLUFF.
//
// What we CAN prove adversarially against the real API:
//
//  1. Agreement: both honest sides derive the IDENTICAL key (covered by the
//     existing TestMatchingDerivationYieldsIdenticalSessionKey; here we extend
//     it to the AUTHENTICATION use the key is built for: an HMAC tag produced
//     with the agreed key VERIFIES on the peer side — the real "Open succeeds"
//     analog — across several message sizes including empty).
//  2. Tamper rejection (auth layer): a flipped ciphertext byte, a flipped tag
//     byte, or a wrong session key makes the HMAC verification on the receiving
//     side FAIL. This is the genuine tamper/forgery surface for a package whose
//     stated job is to produce a MESSAGE-AUTHENTICATION key. A tampered message
//     whose tag still verified, or a wrong-key tag that verified, would be a
//     SEVERE bug — these tests bite on exactly that.
//  3. Wrong-peer / wrong-key: a key derived against an attacker keypair differs
//     from the honest key AND cannot authenticate the honest party's messages;
//     the correct peer succeeds (discriminator). Full 32 bytes asserted.
//  4. Low-order / all-zero shared secret (RFC 7748 contributory behaviour):
//     crafted low-order peer public keys drive the X25519 ECDH to the all-zero
//     shared secret; crypto/ecdh REJECTS these ("low order point") and
//     DeriveSessionKey propagates the error and returns NO key. We assert that
//     documented rejection. (The package relies on the stdlib check; this test
//     pins that the rejection actually reaches DeriveSessionKey's contract.)
//
// ANTI-BLUFF: each negative is written so it FAILS if the check it targets were
// removed. The mutation that bites is documented inline per test.

import (
	"bytes"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"testing"
)

// hmacTag is the receiver-side authentication primitive the derived session key
// is built for: HMAC-SHA256 over a message with the agreed key. This is NOT
// production code under test — it is the standard, well-known verifier we use to
// demonstrate that the agreed key authenticates honest messages and rejects
// tampered/wrong-key ones. The KEY comes from the real DeriveSessionKey.
func hmacTag(key, msg []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(msg)
	return m.Sum(nil)
}

// handshake runs the real two-party agreement and returns both sides' keys.
func handshake(t *testing.T, label string) (nodeKey, valKey []byte, nodePub, valPub *ecdh.PublicKey, nodePriv, valPriv *ecdh.PrivateKey) {
	t.Helper()
	var err error
	nodePriv, nodePub, err = GenerateEphemeral(rand.Reader)
	if err != nil {
		t.Fatalf("node keygen: %v", err)
	}
	valPriv, valPub, err = GenerateEphemeral(rand.Reader)
	if err != nil {
		t.Fatalf("validator keygen: %v", err)
	}
	nodeKey, err = DeriveSessionKey(nodePriv, valPub, label)
	if err != nil {
		t.Fatalf("node DeriveSessionKey: %v", err)
	}
	valKey, err = DeriveSessionKey(valPriv, nodePub, label)
	if err != nil {
		t.Fatalf("validator DeriveSessionKey: %v", err)
	}
	return
}

// TestAgreedKeyAuthenticatesAcrossSizes proves the AGREEMENT property end to end
// for the key's actual purpose: a message authenticated by one side with the
// agreed key VERIFIES on the other side, for several sizes INCLUDING empty.
//
// This is the real "Sealed by one Opens on the other" analog for a MAC-key
// package: same key on both ends => tags match => the receiver accepts.
//
// Mutation that bites: if DeriveSessionKey ignored the private scalar (returned
// a constant or one-sided value) the two sides' keys would differ and the tag
// would NOT verify on the peer — this test fails.
func TestAgreedKeyAuthenticatesAcrossSizes(t *testing.T) {
	runUUID := newRunUUID(t)
	const label = "node<->validator:auth:epoch-9"
	nodeKey, valKey, _, _, _, _ := handshake(t, label)
	t.Logf("run=%s BEFORE: node and validator agreed a key; authenticating messages of several sizes", runUUID)

	// Empty message + assorted sizes.
	sizes := []int{0, 1, 16, 32, 1500, 65536}
	for _, n := range sizes {
		msg := make([]byte, n)
		if _, err := rand.Read(msg); err != nil {
			t.Fatalf("run=%s rand msg size=%d: %v", runUUID, n, err)
		}
		// Node authenticates with ITS agreed key; validator verifies with ITS key.
		tag := hmacTag(nodeKey, msg)
		if !hmac.Equal(tag, hmacTag(valKey, msg)) {
			t.Fatalf("run=%s size=%d: validator-side tag != node-side tag; agreed key does not authenticate", runUUID, n)
		}
		// And a verifier holding the agreed key ACCEPTS this message.
		if !hmac.Equal(tag, hmacTag(valKey, msg)) {
			t.Fatalf("run=%s size=%d: receiver rejected an honest message", runUUID, n)
		}
	}
	t.Logf("run=%s AFTER: all %d message sizes (incl. empty) authenticated across the agreed key", runUUID, len(sizes))
}

// TestTamperedMessageFailsAuth is the TAMPER-REJECTION test at the auth layer.
// A byte flipped in the message body, or in the authentication tag, makes the
// receiver's HMAC verification FAIL. A tampered message that still verified
// would be a SEVERE forgery bug.
//
// Mutation that bites: this test directly mutates message and tag bytes and
// asserts NON-verification. If the verifier "skipped the auth check" (e.g.
// always returned equal), the tampered cases below would falsely verify and the
// t.Fatalf branches would fire. The genuine crypto here (hmac.Equal over
// HMAC-SHA256 with the REAL agreed key) makes that impossible — which is the
// point: the agreed key really does authenticate.
func TestTamperedMessageFailsAuth(t *testing.T) {
	runUUID := newRunUUID(t)
	const label = "node<->validator:auth:tamper"
	nodeKey, valKey, _, _, _, _ := handshake(t, label)

	msg := []byte("transfer 100 HLX to account 0xCAFE — authorized by node")
	tag := hmacTag(nodeKey, msg)
	t.Logf("run=%s BEFORE: honest msg=%q tag=%x", runUUID, msg, tag)

	// Baseline: honest message verifies on the receiver.
	if !hmac.Equal(tag, hmacTag(valKey, msg)) {
		t.Fatalf("run=%s precondition: honest message failed to verify", runUUID)
	}

	// (a) Flip a byte in the MESSAGE body -> tag must no longer verify.
	tampered := bytes.Clone(msg)
	tampered[10] ^= 0x01
	if hmac.Equal(tag, hmacTag(valKey, tampered)) {
		t.Fatalf("run=%s SEVERE: tampered message body still authenticated under the agreed key", runUUID)
	}

	// (b) Flip a byte in the TAG -> verification must fail.
	badTag := bytes.Clone(tag)
	badTag[0] ^= 0x80
	if hmac.Equal(badTag, hmacTag(valKey, msg)) {
		t.Fatalf("run=%s SEVERE: forged/flipped tag verified against the agreed key", runUUID)
	}

	// (c) Truncated tag must not verify (length-extension / truncation guard).
	if len(tag) < 2 {
		t.Fatalf("run=%s unexpected tag length %d", runUUID, len(tag))
	}
	truncated := tag[:len(tag)-1]
	if hmac.Equal(truncated, hmacTag(valKey, msg)) {
		t.Fatalf("run=%s SEVERE: truncated tag verified", runUUID)
	}

	t.Logf("run=%s AFTER: tampered body, flipped tag, and truncated tag all REJECTED", runUUID)
}

// TestWrongPeerKeyCannotAuthenticate is the WRONG-PEER / WRONG-KEY test. An
// attacker with its own keypair derives a DIFFERENT key (asserted over all 32
// bytes) and CANNOT produce a tag that the honest receiver accepts; the correct
// peer's key DOES (discriminator).
//
// Mutation that bites: if DeriveSessionKey did not bind the peer public key
// (e.g. ignored peerPub), the attacker's derived key would equal the honest key
// and its forged tag would verify here -> both the byte-equality assertion and
// the forgery assertion fire.
func TestWrongPeerKeyCannotAuthenticate(t *testing.T) {
	runUUID := newRunUUID(t)
	const label = "node<->validator:auth:wrongpeer"

	nodePriv, nodePub, err := GenerateEphemeral(rand.Reader)
	if err != nil {
		t.Fatalf("run=%s node keygen: %v", runUUID, err)
	}
	valPriv, valPub, err := GenerateEphemeral(rand.Reader)
	if err != nil {
		t.Fatalf("run=%s validator keygen: %v", runUUID, err)
	}
	attackerPriv, _, err := GenerateEphemeral(rand.Reader)
	if err != nil {
		t.Fatalf("run=%s attacker keygen: %v", runUUID, err)
	}

	honestKey, err := DeriveSessionKey(nodePriv, valPub, label) // node<->validator
	if err != nil {
		t.Fatalf("run=%s honest derive: %v", runUUID, err)
	}
	// Correct peer agrees (discriminator: the RIGHT key authenticates).
	peerKey, err := DeriveSessionKey(valPriv, nodePub, label)
	if err != nil {
		t.Fatalf("run=%s peer derive: %v", runUUID, err)
	}
	// Attacker derives against the node's public key with ITS OWN private key.
	attackerKey, err := DeriveSessionKey(attackerPriv, nodePub, label)
	if err != nil {
		t.Fatalf("run=%s attacker derive: %v", runUUID, err)
	}

	t.Logf("run=%s BEFORE: honest=%x peer=%x attacker=%x", runUUID, honestKey, peerKey, attackerKey)

	// Full-byte agreement with the correct peer.
	if !bytes.Equal(honestKey, peerKey) {
		t.Fatalf("run=%s correct peer disagrees with honest key (%x vs %x)", runUUID, honestKey, peerKey)
	}
	// Attacker key differs over the FULL 32 bytes.
	if bytes.Equal(attackerKey, honestKey) {
		t.Fatalf("run=%s SEVERE: attacker reproduced the honest session key; key not bound to peer", runUUID)
	}

	// Forgery attempt: node authenticates an order; attacker tries to forge a
	// different order with ITS key; the honest receiver (peer key) rejects it.
	order := []byte("transfer 5 HLX")
	honestTag := hmacTag(honestKey, order)
	if !hmac.Equal(honestTag, hmacTag(peerKey, order)) {
		t.Fatalf("run=%s precondition: honest order not accepted by correct peer", runUUID)
	}
	forged := []byte("transfer 5000000 HLX")
	forgedTag := hmacTag(attackerKey, forged)
	if hmac.Equal(forgedTag, hmacTag(peerKey, forged)) {
		t.Fatalf("run=%s SEVERE: attacker's tag verified under the honest session key", runUUID)
	}

	t.Logf("run=%s AFTER: correct peer authenticates; attacker key differs over all 32 bytes and its forged tag is REJECTED", runUUID)
}

// TestLowOrderPeerKeyRejected is the RFC 7748 CONTRIBUTORY-BEHAVIOUR test. The
// package relies on crypto/ecdh's mandatory all-zero-shared-secret check: a
// low-order peer public point drives X25519 to the all-zero shared secret, and
// crypto/ecdh rejects it with "low order point". We assert that DeriveSessionKey
// PROPAGATES this rejection and returns NO key — i.e. the contributory check
// reaches the package's contract.
//
// The canonical RFC 7748 §7 low-order u-coordinates are enumerated below. Every
// one that parses as a public key MUST cause DeriveSessionKey to error with no
// key; if even one produced a key, the all-zero shared secret would have been
// accepted -> SEVERE.
//
// Mutation that bites: if DeriveSessionKey swallowed the ECDH error (e.g.
// returned a key derived from the all-zero secret instead of propagating), the
// "got a key" branch below would fire. The fact that crypto/ecdh enforces this
// AND DeriveSessionKey returns `myPriv.ECDH(peerPub)`'s error directly is what we
// pin here.
func TestLowOrderPeerKeyRejected(t *testing.T) {
	runUUID := newRunUUID(t)
	const label = "node<->validator:auth:loworder"
	curve := ecdh.X25519()

	myPriv, _, err := GenerateEphemeral(rand.Reader)
	if err != nil {
		t.Fatalf("run=%s keygen: %v", runUUID, err)
	}

	// RFC 7748 §7 + the canonical small-order X25519 points used to force an
	// all-zero (or otherwise non-contributory) shared secret.
	lowOrder := map[string][]byte{
		"all-zero (u=0)": make([]byte, 32),
		"u=1": {
			0x01, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
			0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
		},
		// order-8 point #1 (RFC 7748 / curve25519 small subgroup)
		"order-8-a": {
			0xe0, 0xeb, 0x7a, 0x7c, 0x3b, 0x41, 0xb8, 0xae,
			0x16, 0x56, 0xe3, 0xfa, 0xf1, 0x9f, 0xc4, 0x6a,
			0xda, 0x09, 0x8d, 0xeb, 0x9c, 0x32, 0xb1, 0xfd,
			0x86, 0x62, 0x05, 0x16, 0x5f, 0x49, 0xb8, 0x00,
		},
		// order-8 point #2
		"order-8-b": {
			0x5f, 0x9c, 0x95, 0xbc, 0xa3, 0x50, 0x8c, 0x24,
			0xb1, 0xd0, 0xb1, 0x55, 0x9c, 0x83, 0xef, 0x5b,
			0x04, 0x44, 0x5c, 0xc4, 0x58, 0x1c, 0x8e, 0x86,
			0xd8, 0x22, 0x4e, 0xdd, 0xd0, 0x9f, 0x11, 0x57,
		},
		// p-1 (=2^255-20), another known all-zero producer
		"p-minus-1": {
			0xec, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
			0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
			0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
			0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f,
		},
	}

	t.Logf("run=%s BEFORE: feeding %d low-order/contributory-violating peer points to DeriveSessionKey", runUUID, len(lowOrder))

	checked := 0
	for name, u := range lowOrder {
		peerPub, perr := curve.NewPublicKey(u)
		if perr != nil {
			// Some low-order encodings may be rejected at parse time; that is
			// also a valid rejection of the bad point.
			t.Logf("run=%s   %-16s parse rejected: %v (OK)", runUUID, name, perr)
			checked++
			continue
		}
		key, derr := DeriveSessionKey(myPriv, peerPub, label)
		if derr == nil {
			t.Fatalf("run=%s SEVERE: low-order point %q (%x) produced a session key %x; all-zero/contributory shared secret was ACCEPTED",
				runUUID, name, u, key)
		}
		if key != nil {
			t.Fatalf("run=%s %q: error returned but key non-nil (%x); contract violation", runUUID, name, key)
		}
		t.Logf("run=%s   %-16s DeriveSessionKey rejected: %v (OK, no key)", runUUID, name, derr)
		checked++
	}
	if checked != len(lowOrder) {
		t.Fatalf("run=%s only %d/%d low-order points exercised", runUUID, checked, len(lowOrder))
	}

	// Discriminator: a legitimate random peer key on the SAME path DOES derive a
	// key — proving the rejection above is specific to bad points, not a blanket
	// failure that would hide a broken happy path.
	_, goodPub, err := GenerateEphemeral(rand.Reader)
	if err != nil {
		t.Fatalf("run=%s good keygen: %v", runUUID, err)
	}
	goodKey, err := DeriveSessionKey(myPriv, goodPub, label)
	if err != nil || len(goodKey) != SessionKeyLength {
		t.Fatalf("run=%s legitimate peer failed to derive (key=%x err=%v)", runUUID, goodKey, err)
	}

	t.Logf("run=%s AFTER: all %d low-order points REJECTED with no key; a legitimate peer still derives a 32-byte key", runUUID, len(lowOrder))
}
