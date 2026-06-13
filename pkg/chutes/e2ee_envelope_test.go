package chutes

// HXC-1431 closure tests: prove the E2EE inference envelope between pkg/chutes and
// the REAL digital.vasic.security/pkg/e2ee package round-trips byte-exact, encrypts
// (ciphertext != plaintext), and FAILS on tamper / wrong key / replay — using the
// real ML-KEM-768 + AEAD primitives, not a placeholder.

import (
	"bytes"
	"testing"

	"digital.vasic.security/pkg/e2ee"
)

// realInferenceRequest is a representative inference request payload (JSON the
// chutes serving path would carry).
var realInferenceRequest = []byte(`{"model":"helix-llm-7b","prompt":"Summarize the cluster health report.","max_tokens":256,"temperature":0.2,"stream":false}`)

// realInferenceResponse is a representative inference response payload.
var realInferenceResponse = []byte(`{"id":"infer-abc123","model":"helix-llm-7b","choices":[{"text":"All 14 control-plane services are healthy; Raft quorum stable."}],"usage":{"prompt_tokens":12,"completion_tokens":18}}`)

// TestEnvelopeRoundTrip is CLOSURE item 1: seal a real request on the client,
// transmit in-memory, open on the server byte-exact; same for the response.
func TestEnvelopeRoundTrip(t *testing.T) {
	aad := []byte("helix-llm-7b")
	client, server, err := EstablishEnvelopePair(aad)
	if err != nil {
		t.Fatalf("EstablishEnvelopePair: %v", err)
	}

	// --- Request: client seals -> wire -> server opens ---
	sealedReq, err := client.SealRequest(realInferenceRequest)
	if err != nil {
		t.Fatalf("SealRequest: %v", err)
	}
	// Confidentiality: the sealed request is NOT the plaintext.
	if bytes.Equal(sealedReq, realInferenceRequest) {
		t.Fatal("ANTI-BLUFF FAIL: sealed request equals plaintext (not encrypted)")
	}
	if bytes.Contains(sealedReq, []byte("Summarize the cluster health report")) {
		t.Fatal("ANTI-BLUFF FAIL: plaintext prompt visible inside sealed request ciphertext")
	}

	wireReq := transmit(sealedReq) // simulate transit
	openedReq, err := server.OpenRequest(wireReq)
	if err != nil {
		t.Fatalf("OpenRequest: %v", err)
	}
	if !bytes.Equal(openedReq, realInferenceRequest) {
		t.Fatalf("request round-trip mismatch:\n got: %q\nwant: %q", openedReq, realInferenceRequest)
	}

	// --- Response: server seals -> wire -> client opens ---
	sealedResp, err := server.SealResponse(realInferenceResponse)
	if err != nil {
		t.Fatalf("SealResponse: %v", err)
	}
	if bytes.Equal(sealedResp, realInferenceResponse) {
		t.Fatal("ANTI-BLUFF FAIL: sealed response equals plaintext (not encrypted)")
	}
	wireResp := transmit(sealedResp)
	openedResp, err := client.OpenResponse(wireResp)
	if err != nil {
		t.Fatalf("OpenResponse: %v", err)
	}
	if !bytes.Equal(openedResp, realInferenceResponse) {
		t.Fatalf("response round-trip mismatch:\n got: %q\nwant: %q", openedResp, realInferenceResponse)
	}

	t.Logf("round-trip OK: request %dB -> %dB sealed -> opened byte-exact; response %dB -> %dB sealed -> opened byte-exact",
		len(realInferenceRequest), len(sealedReq), len(realInferenceResponse), len(sealedResp))
}

// TestEnvelopeTamperFails is CLOSURE item 2 (integrity): flipping a byte of the
// sealed request makes OpenRequest FAIL — the AEAD tag is verified, not ignored.
func TestEnvelopeTamperFails(t *testing.T) {
	aad := []byte("helix-llm-7b")
	client, server, err := EstablishEnvelopePair(aad)
	if err != nil {
		t.Fatalf("EstablishEnvelopePair: %v", err)
	}

	sealed, err := client.SealRequest(realInferenceRequest)
	if err != nil {
		t.Fatalf("SealRequest: %v", err)
	}

	// Flip one byte near the end (inside the ciphertext/tag region).
	tampered := make([]byte, len(sealed))
	copy(tampered, sealed)
	idx := len(tampered) - 1
	tampered[idx] ^= 0x01

	opened, err := server.OpenRequest(tampered)
	if err == nil {
		t.Fatalf("ANTI-BLUFF FAIL: OpenRequest accepted a tampered record and returned %q (AEAD integrity not enforced)", opened)
	}
	if opened != nil {
		t.Fatalf("tampered Open returned non-nil plaintext %q alongside error", opened)
	}
	t.Logf("tamper correctly rejected: flipped byte[%d], Open failed with: %v", idx, err)
}

// TestEnvelopeWrongKeyFails is CLOSURE item 3 (wrong key): a record sealed under
// one session does not open under an unrelated session (different key material).
func TestEnvelopeWrongKeyFails(t *testing.T) {
	aad := []byte("helix-llm-7b")
	client, _, err := EstablishEnvelopePair(aad)
	if err != nil {
		t.Fatalf("EstablishEnvelopePair (channel A): %v", err)
	}
	// An entirely separate handshake -> a different key. Its server cannot open
	// channel A's sealed request.
	_, wrongServer, err := EstablishEnvelopePair(aad)
	if err != nil {
		t.Fatalf("EstablishEnvelopePair (channel B): %v", err)
	}

	sealed, err := client.SealRequest(realInferenceRequest)
	if err != nil {
		t.Fatalf("SealRequest: %v", err)
	}
	opened, err := wrongServer.OpenRequest(sealed)
	if err == nil {
		t.Fatalf("ANTI-BLUFF FAIL: wrong-key server opened the record and returned %q", opened)
	}
	t.Logf("wrong-key correctly rejected: Open failed with: %v", err)
}

// TestEnvelopeNonceUniqueness is CLOSURE item 3 (nonce/replay): two seals of the
// SAME plaintext produce DIFFERENT records (unique per-message nonce), and the
// e2ee Session rejects a replayed record on Open.
func TestEnvelopeNonceUniqueness(t *testing.T) {
	// Use a directly keyed pair of sessions so both seals share one sealing
	// session (the scenario where nonce uniqueness must hold per-session).
	key := bytes.Repeat([]byte{0x42}, e2ee.SessionKeySize)
	sealSess, err := e2ee.NewSessionFromKey(key, false) // random-nonce mode
	if err != nil {
		t.Fatalf("NewSessionFromKey (seal): %v", err)
	}
	openSess, err := e2ee.NewSessionFromKey(key, false)
	if err != nil {
		t.Fatalf("NewSessionFromKey (open): %v", err)
	}
	sealer, err := NewE2EESessionEncryptor(sealSess)
	if err != nil {
		t.Fatalf("NewE2EESessionEncryptor (seal): %v", err)
	}
	opener, err := NewE2EESessionEncryptor(openSess)
	if err != nil {
		t.Fatalf("NewE2EESessionEncryptor (open): %v", err)
	}

	aad := []byte("helix-llm-7b")
	rec1, err := sealer.Seal(realInferenceRequest, aad)
	if err != nil {
		t.Fatalf("seal 1: %v", err)
	}
	rec2, err := sealer.Seal(realInferenceRequest, aad)
	if err != nil {
		t.Fatalf("seal 2: %v", err)
	}
	if bytes.Equal(rec1, rec2) {
		t.Fatal("ANTI-BLUFF FAIL: two seals of identical plaintext produced identical records (nonce reuse)")
	}

	// First record opens fine.
	if _, err := opener.Open(rec1, aad); err != nil {
		t.Fatalf("open rec1: %v", err)
	}
	// Replaying the SAME record must be rejected (single-use nonce enforcement).
	if _, err := opener.Open(rec1, aad); err == nil {
		t.Fatal("ANTI-BLUFF FAIL: replayed record opened twice (no replay protection)")
	} else {
		t.Logf("replay correctly rejected on second Open of same record: %v", err)
	}
	t.Logf("nonce uniqueness OK: identical plaintext -> distinct records (%dB each)", len(rec1))
}

// TestSessionEncryptorImplementsSeam confirms the real e2ee adapter satisfies the
// same Encryptor seam the EncryptedInferenceProxy consumes, so the serving path
// adopts real E2EE with no proxy change.
func TestSessionEncryptorImplementsSeam(t *testing.T) {
	key := bytes.Repeat([]byte{0x11}, e2ee.SessionKeySize)
	sess, err := e2ee.NewSessionFromKey(key, false)
	if err != nil {
		t.Fatalf("NewSessionFromKey: %v", err)
	}
	enc, err := NewE2EESessionEncryptor(sess)
	if err != nil {
		t.Fatalf("NewE2EESessionEncryptor: %v", err)
	}
	var _ Encryptor = enc
	if enc.Algorithm() == "" {
		t.Fatal("Algorithm() empty")
	}

	// Drive it through the real proxy to prove end-to-end adoption.
	sink := &MemoryAuditSink{}
	proxy, err := NewEncryptedInferenceProxy(ProxyConfig{Encryptor: enc, Sink: sink})
	if err != nil {
		t.Fatalf("NewEncryptedInferenceProxy: %v", err)
	}
	ct, err := proxy.SealRequest("principal-a", "helix-llm-7b", realInferenceRequest)
	if err != nil {
		t.Fatalf("proxy.SealRequest: %v", err)
	}
	if bytes.Equal(ct, realInferenceRequest) {
		t.Fatal("ANTI-BLUFF FAIL: proxy sealed request equals plaintext")
	}
	if got := sink.Entries(); len(got) != 1 || got[0].Algorithm != enc.Algorithm() {
		t.Fatalf("audit entry not recorded with e2ee algorithm: %+v", got)
	}
	t.Logf("proxy adoption OK: e2ee SessionEncryptor secured a proxied request (alg=%q)", enc.Algorithm())
}

// TestNilSessionRejected confirms a misconfigured envelope cannot silently skip
// real encryption.
func TestNilSessionRejected(t *testing.T) {
	if _, err := NewE2EESessionEncryptor(nil); err == nil {
		t.Fatal("expected error for nil session")
	}
}

// transmit copies bytes to model an in-memory wire hop (no aliasing of the
// sealer's buffer), so the open side genuinely decrypts received bytes.
func transmit(b []byte) []byte {
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
