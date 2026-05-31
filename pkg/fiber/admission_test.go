package fiber

import (
	"crypto/ed25519"
	"errors"
	"testing"
)

// newPeerIdentity generates a real ed25519 keypair for a test peer. Using the
// stdlib keygen ensures the signatures we produce are genuine, so an admission
// PASS proves the validator really verified a cryptographically valid identity
// (not that it rubber-stamped anything).
func newPeerIdentity(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return pub, priv
}

// TestAdmitValidSignatureSufficientStake proves a peer that signs the validator-
// issued nonce with the private key matching its declared public key AND declares
// stake >= the minimum is ADMITTED, with a nil reason.
//
// MUTATION that makes this FAIL: in Admit, change the stake check from
// `if h.Stake < a.minStake` to `if h.Stake <= a.minStake`. A peer declaring
// exactly the minimum stake (used below) is then denied ErrInsufficientStake and
// the Admit assertion fails.
func TestAdmitValidSignatureSufficientStake(t *testing.T) {
	pub, priv := newPeerIdentity(t)
	adm := NewAdmitter(Ed25519Verifier{}, 100)

	nonce, err := adm.IssueChallenge()
	if err != nil {
		t.Fatalf("IssueChallenge: %v", err)
	}
	sig := ed25519.Sign(priv, nonce)

	dec, err := adm.Admit(Handshake{
		PublicKey: pub,
		Nonce:     nonce,
		Signature: sig,
		Stake:     100, // exactly the inclusive minimum
	})
	if err != nil {
		t.Fatalf("Admit returned error for valid peer: %v", err)
	}
	if !dec.Admit {
		t.Fatalf("valid peer not admitted: reason=%v", dec.Reason)
	}
	if dec.Reason != nil {
		t.Fatalf("admitted peer has non-nil reason: %v", dec.Reason)
	}
}

// TestAdmitForgedSignatureRejected proves a peer presenting a signature that does
// NOT verify under the claimed public key is REJECTED with the specific
// ErrBadSignature. The signature is forged by signing with a DIFFERENT private
// key than the declared public key (classic identity spoof: claim victim's
// pubkey, sign with attacker's key).
//
// MUTATION that makes this FAIL (the mandated anti-bluff mutation): in Admit,
// delete the
//
//	if !a.verifier.Verify(h.PublicKey, h.Nonce, h.Signature) { return deny(ErrBadSignature) }
//
// block (skip signature verification). The forged-identity peer is then admitted
// and this test's ErrBadSignature assertion fails — proving the test actually
// exercises real signature verification, not a no-op.
func TestAdmitForgedSignatureRejected(t *testing.T) {
	victimPub, _ := newPeerIdentity(t)    // identity the attacker claims
	_, attackerPriv := newPeerIdentity(t) // key the attacker actually holds
	adm := NewAdmitter(Ed25519Verifier{}, 100)

	nonce, err := adm.IssueChallenge()
	if err != nil {
		t.Fatalf("IssueChallenge: %v", err)
	}
	// Sign with the attacker's key but claim the victim's public key.
	forged := ed25519.Sign(attackerPriv, nonce)

	dec, err := adm.Admit(Handshake{
		PublicKey: victimPub,
		Nonce:     nonce,
		Signature: forged,
		Stake:     1_000_000, // abundant stake; must NOT rescue a bad signature
	})
	if dec.Admit {
		t.Fatal("forged-identity peer was ADMITTED (signature verification skipped?)")
	}
	if !errors.Is(err, ErrBadSignature) {
		t.Fatalf("want ErrBadSignature for forged signature, got %v", err)
	}
	if !errors.Is(dec.Reason, ErrBadSignature) {
		t.Fatalf("decision reason = %v, want ErrBadSignature", dec.Reason)
	}
}

// TestAdmitTamperedNonceRejected proves a peer that signs a DIFFERENT message
// than the issued nonce (so the signature is valid by its own key but not over
// the challenge) is rejected with ErrBadSignature. This guards the binding of the
// signature to the validator's specific challenge.
//
// MUTATION that makes this FAIL: in Admit, pass an empty/constant message to the
// verifier, e.g. `a.verifier.Verify(h.PublicKey, nil, h.Signature)` instead of
// `h.Nonce`. The signature-over-wrong-data would then be checked against the
// wrong message and the rejection semantics break.
func TestAdmitTamperedNonceRejected(t *testing.T) {
	pub, priv := newPeerIdentity(t)
	adm := NewAdmitter(Ed25519Verifier{}, 0)

	nonce, err := adm.IssueChallenge()
	if err != nil {
		t.Fatalf("IssueChallenge: %v", err)
	}
	// Sign a different message than the issued nonce.
	sigOverWrong := ed25519.Sign(priv, []byte("not-the-issued-nonce"))

	dec, err := adm.Admit(Handshake{
		PublicKey: pub,
		Nonce:     nonce,
		Signature: sigOverWrong,
		Stake:     0,
	})
	if dec.Admit {
		t.Fatal("peer signing the wrong message was ADMITTED")
	}
	if !errors.Is(err, ErrBadSignature) {
		t.Fatalf("want ErrBadSignature for signature over wrong message, got %v", err)
	}
}

// TestAdmitInsufficientStakeRejected proves a peer with a VALID signature but a
// declared stake below the minimum is REJECTED with the specific
// ErrInsufficientStake (distinct from a signature failure).
//
// MUTATION that makes this FAIL: in Admit, delete the
//
//	if h.Stake < a.minStake { return deny(ErrInsufficientStake) }
//
// block. The under-staked peer is then admitted and the ErrInsufficientStake
// assertion fails.
func TestAdmitInsufficientStakeRejected(t *testing.T) {
	pub, priv := newPeerIdentity(t)
	adm := NewAdmitter(Ed25519Verifier{}, 1000)

	nonce, err := adm.IssueChallenge()
	if err != nil {
		t.Fatalf("IssueChallenge: %v", err)
	}
	sig := ed25519.Sign(priv, nonce) // signature is genuine

	dec, err := adm.Admit(Handshake{
		PublicKey: pub,
		Nonce:     nonce,
		Signature: sig,
		Stake:     999, // one below the inclusive minimum of 1000
	})
	if dec.Admit {
		t.Fatal("under-staked peer was ADMITTED")
	}
	if !errors.Is(err, ErrInsufficientStake) {
		t.Fatalf("want ErrInsufficientStake, got %v", err)
	}
	if !errors.Is(dec.Reason, ErrInsufficientStake) {
		t.Fatalf("decision reason = %v, want ErrInsufficientStake", dec.Reason)
	}
}

// TestAdmitReplayedNonceRejected proves that a nonce admitted once cannot be
// replayed: a second, byte-identical handshake (same valid signature, same nonce,
// same ample stake) is rejected with ErrStaleNonce. This is the anti-replay
// guarantee — a captured admission handshake cannot be re-used.
//
// MUTATION that makes this FAIL: in Admit, replace the
//
//	if !a.consume(h.Nonce) { return deny(ErrStaleNonce) }
//
// call with a non-consuming freshness probe (e.g. always treat the nonce as
// fresh). The replayed handshake is then admitted a second time and the
// ErrStaleNonce assertion fails. (Equivalently, deleting the `delete(a.issued,
// key)` line in consume makes the nonce reusable and fails this test.)
func TestAdmitReplayedNonceRejected(t *testing.T) {
	pub, priv := newPeerIdentity(t)
	adm := NewAdmitter(Ed25519Verifier{}, 0)

	nonce, err := adm.IssueChallenge()
	if err != nil {
		t.Fatalf("IssueChallenge: %v", err)
	}
	sig := ed25519.Sign(priv, nonce)
	hs := Handshake{PublicKey: pub, Nonce: nonce, Signature: sig, Stake: 50}

	// First admission must succeed.
	dec1, err := adm.Admit(hs)
	if err != nil || !dec1.Admit {
		t.Fatalf("first admission failed: admit=%v reason=%v err=%v", dec1.Admit, dec1.Reason, err)
	}

	// Replay the exact same handshake — must be rejected as stale.
	dec2, err := adm.Admit(hs)
	if dec2.Admit {
		t.Fatal("replayed handshake was ADMITTED (nonce not consumed)")
	}
	if !errors.Is(err, ErrStaleNonce) {
		t.Fatalf("want ErrStaleNonce on replay, got %v", err)
	}
}

// TestAdmitUnknownNonceRejected proves a handshake carrying a nonce this Admitter
// never issued is rejected as stale (it is indistinguishable from a replay of a
// nonce from another validator). Asserts ErrStaleNonce specifically.
//
// MUTATION that makes this FAIL: in consume, change the membership test
// `if _, ok := a.issued[key]; !ok { return false }` to `return true` — an
// unissued nonce is then accepted and the ErrStaleNonce assertion fails.
func TestAdmitUnknownNonceRejected(t *testing.T) {
	pub, priv := newPeerIdentity(t)
	adm := NewAdmitter(Ed25519Verifier{}, 0)

	// A nonce of the right size that was NEVER issued by adm.
	unissued := make([]byte, nonceSize)
	for i := range unissued {
		unissued[i] = 0xAB
	}
	sig := ed25519.Sign(priv, unissued)

	dec, err := adm.Admit(Handshake{
		PublicKey: pub,
		Nonce:     unissued,
		Signature: sig,
		Stake:     1000,
	})
	if dec.Admit {
		t.Fatal("handshake with unissued nonce was ADMITTED")
	}
	if !errors.Is(err, ErrStaleNonce) {
		t.Fatalf("want ErrStaleNonce for unissued nonce, got %v", err)
	}
}

// TestAdmitMalformedHandshakeRejected proves structurally invalid handshakes are
// rejected with ErrMalformedHandshake and that this is detected BEFORE a nonce is
// consumed (a malformed probe must not burn a live nonce).
//
// MUTATION that makes this FAIL: in Admit, delete the structural-validity guard
//
//	if len(h.Nonce) == 0 || len(h.Signature) == 0 || len(h.PublicKey) != ed25519.PublicKeySize { ... }
//
// A wrong-length public key would then flow into the verifier; Ed25519Verifier
// returns false for it, so the reason becomes ErrBadSignature instead of
// ErrMalformedHandshake and the assertion fails.
func TestAdmitMalformedHandshakeRejected(t *testing.T) {
	adm := NewAdmitter(Ed25519Verifier{}, 0)
	nonce, err := adm.IssueChallenge()
	if err != nil {
		t.Fatalf("IssueChallenge: %v", err)
	}

	dec, err := adm.Admit(Handshake{
		PublicKey: []byte{0x01, 0x02}, // wrong length
		Nonce:     nonce,
		Signature: []byte{0x03},
		Stake:     1000,
	})
	if dec.Admit {
		t.Fatal("malformed handshake was ADMITTED")
	}
	if !errors.Is(err, ErrMalformedHandshake) {
		t.Fatalf("want ErrMalformedHandshake, got %v", err)
	}

	// The malformed probe must NOT have consumed the issued nonce: a well-formed
	// handshake over the same nonce must still be admissible.
	_, priv := newPeerIdentity(t)
	pub := priv.Public().(ed25519.PublicKey)
	good := Handshake{PublicKey: pub, Nonce: nonce, Signature: ed25519.Sign(priv, nonce), Stake: 1000}
	dec2, err := adm.Admit(good)
	if err != nil || !dec2.Admit {
		t.Fatalf("nonce was wrongly consumed by malformed probe: admit=%v err=%v", dec2.Admit, err)
	}
}

// TestAcceptConnNilAdmitterIsOpen proves the gate is OPTIONAL and backward-
// compatible: with a nil Admitter, AcceptConn accepts unconditionally and returns
// a usable *Conn over a real TCP loopback (a message round-trips), exactly as the
// pre-gate behavior.
//
// MUTATION that makes this FAIL: in AcceptConn, change `if admitter != nil` to
// `if admitter == nil` — the nil-admitter path would then call admitter.Admit on
// a nil pointer and panic, so the open-by-default contract breaks.
func TestAcceptConnNilAdmitterIsOpen(t *testing.T) {
	cConn, sConn := tcpLoopback(t)

	sv, err := AcceptConn(sConn, 0, nil, Handshake{})
	if err != nil {
		t.Fatalf("AcceptConn with nil admitter returned error: %v", err)
	}
	if sv == nil {
		t.Fatal("AcceptConn returned nil Conn for open gate")
	}
	cl := NewConn(cConn, 0)

	want := []byte("open-gate-payload")
	errCh := make(chan error, 1)
	go func() { errCh <- cl.WriteMessage(want) }()
	got, err := sv.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage over open-gate Conn: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("round-trip mismatch: got=%q want=%q", got, want)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
}

// TestAcceptConnAdmitsValidPeer proves the gated path returns a usable *Conn for
// an admitted peer AND that an admitted Conn carries application data over a real
// socket.
//
// MUTATION that makes this FAIL: in AcceptConn, after a successful Admit, return
// `nil, ErrMalformedHandshake` instead of `NewConn(...)`. The admitted peer would
// then get no Conn and this test's non-nil/round-trip assertions fail.
func TestAcceptConnAdmitsValidPeer(t *testing.T) {
	cConn, sConn := tcpLoopback(t)
	pub, priv := newPeerIdentity(t)
	adm := NewAdmitter(Ed25519Verifier{}, 10)

	nonce, err := adm.IssueChallenge()
	if err != nil {
		t.Fatalf("IssueChallenge: %v", err)
	}
	hs := Handshake{PublicKey: pub, Nonce: nonce, Signature: ed25519.Sign(priv, nonce), Stake: 10}

	sv, err := AcceptConn(sConn, 0, adm, hs)
	if err != nil {
		t.Fatalf("AcceptConn denied a valid peer: %v", err)
	}
	if sv == nil {
		t.Fatal("AcceptConn returned nil Conn for admitted peer")
	}
	cl := NewConn(cConn, 0)

	want := []byte("admitted-block-#42")
	errCh := make(chan error, 1)
	go func() { errCh <- cl.WriteMessage(want) }()
	got, err := sv.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage over admitted Conn: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("round-trip mismatch: got=%q want=%q", got, want)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
}

// TestAcceptConnRejectsForgedPeerNoConn proves the gated path returns NO *Conn
// and the ErrBadSignature error when a forged-identity peer attempts to connect:
// admission failure prevents the transport from ever being established.
//
// MUTATION that makes this FAIL: in AcceptConn, ignore the Admit error and always
// `return NewConn(rw, maxFrameSize), nil`. The forged peer would then receive a
// live Conn and both the nil-Conn and ErrBadSignature assertions fail.
func TestAcceptConnRejectsForgedPeerNoConn(t *testing.T) {
	_, sConn := tcpLoopback(t)
	victimPub, _ := newPeerIdentity(t)
	_, attackerPriv := newPeerIdentity(t)
	adm := NewAdmitter(Ed25519Verifier{}, 0)

	nonce, err := adm.IssueChallenge()
	if err != nil {
		t.Fatalf("IssueChallenge: %v", err)
	}
	forged := Handshake{
		PublicKey: victimPub,
		Nonce:     nonce,
		Signature: ed25519.Sign(attackerPriv, nonce), // wrong key
		Stake:     1_000_000,
	}

	conn, err := AcceptConn(sConn, 0, adm, forged)
	if conn != nil {
		t.Fatal("AcceptConn returned a live Conn for a forged-identity peer")
	}
	if !errors.Is(err, ErrBadSignature) {
		t.Fatalf("want ErrBadSignature from AcceptConn, got %v", err)
	}
}

// TestEd25519VerifierWrongLengthKeyNoPanic proves the reference verifier rejects
// a wrong-length public key by returning false instead of panicking (the stdlib
// ed25519.Verify would panic on a bad key length).
//
// MUTATION that makes this FAIL: in Ed25519Verifier.Verify, delete the
// `if len(pubKey) != ed25519.PublicKeySize { return false }` guard. ed25519.Verify
// then panics on the short key and the test (which expects a clean false) fails.
func TestEd25519VerifierWrongLengthKeyNoPanic(t *testing.T) {
	var v Ed25519Verifier
	if v.Verify([]byte{0x01}, []byte("msg"), make([]byte, ed25519.SignatureSize)) {
		t.Fatal("wrong-length key verified true")
	}
}
