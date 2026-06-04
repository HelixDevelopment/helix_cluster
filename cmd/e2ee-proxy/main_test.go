package main

import (
	"bytes"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"digital.vasic.security/pkg/e2ee"
)

// establishSharedSessions runs the REAL ML-KEM-768 handshake from the canonical
// digital.vasic.security/pkg/e2ee package and returns the two ends of the
// channel: clientSess is held by the client-side EncryptingTransport,
// upstreamSess by the upstream-side DecryptingHandler. Both derive the identical
// session key from the same KEM exchange. If the two ends had derived different
// keys, every Open in the tests below would fail authentication — so a green
// test also proves real key agreement, not a stub.
//
// It additionally asserts key agreement explicitly via a cross seal/open probe
// before returning, so a derivation regression is caught at the seam rather than
// surfacing as an opaque HTTP 400 deep in a later test.
func establishSharedSessions(t *testing.T) (clientSess, upstreamSess *e2ee.Session) {
	t.Helper()
	clientSess, upstreamSess, err := establishProxySessions()
	if err != nil {
		t.Fatalf("establishProxySessions: %v", err)
	}

	// Cross round-trip: client seals, upstream opens. This proves both ends hold
	// the same key (real ML-KEM-768 agreement) before any HTTP path runs.
	probe := []byte("agreement-probe")
	sealed, err := clientSess.Seal(probe, nil)
	if err != nil {
		t.Fatalf("client seal: %v", err)
	}
	got, err := upstreamSess.Open(sealed, nil)
	if err != nil {
		t.Fatalf("key agreement FAILED: upstream could not open client-sealed data: %v", err)
	}
	if !bytes.Equal(got, probe) {
		t.Fatalf("key agreement mismatch: got %q want %q", got, probe)
	}
	// Sanity: the package's own constant-time fingerprint comparison must agree.
	if !e2ee.KeysEqual(clientSess, upstreamSess) {
		t.Fatalf("e2ee.KeysEqual reports the two handshake ends hold different keys")
	}
	return clientSess, upstreamSess
}

// rawCapturingHandler wraps an inner handler and records the RAW request-body
// bytes seen at the socket — i.e. exactly what a passive on-wire capture
// between the proxy and the upstream would observe.
type rawCapturingHandler struct {
	mu      sync.Mutex
	rawBody []byte
	inner   http.Handler
}

func (h *rawCapturingHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	h.mu.Lock()
	h.rawBody = append([]byte(nil), raw...)
	h.mu.Unlock()
	// Re-supply the body to the real (decrypting) handler downstream.
	r.Body = io.NopCloser(bytes.NewReader(raw))
	h.inner.ServeHTTP(w, r)
}

func (h *rawCapturingHandler) captured() []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]byte(nil), h.rawBody...)
}

// echoHandler returns the plaintext request body verbatim. After
// DecryptingHandler opens the sealed request, this sees plaintext; its plaintext
// response is then re-sealed on the way out.
func echoHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
}

// chatCompletionsPayload is a representative client->LLM request body. The proxy
// is content-agnostic, but using a realistic chat-completions payload documents
// the intended end-user traffic and gives the wire-leak assertion a meaningful
// secret to look for (the "messages" content must never appear in cleartext on
// the relay).
const chatCompletionsPayload = `{"model":"helix-large","messages":[` +
	`{"role":"system","content":"You are a helpful assistant."},` +
	`{"role":"user","content":"SECRET-PROMPT: summarize the merger terms."}],` +
	`"temperature":0.2,"max_tokens":512}`

// TestProxy_ChatCompletionsRoundTrip_WireIsCiphertext is the LOAD-BEARING GUARD.
//
// It performs a REAL encrypt -> relay -> decrypt round-trip: a client sends a
// representative chat-completions payload through the EncryptingTransport (real
// e2ee Seal), the proxy relays sealed bytes to the upstream, and the upstream's
// DecryptingHandler (real e2ee Open) recovers the plaintext for the worker.
//
// Two invariants are asserted:
//
//	(a) what crosses the relay is ciphertext + routing metadata only — the
//	    plaintext secret marker is ABSENT from the raw on-wire body and from the
//	    request headers, and the raw bytes are non-empty sealed output.
//	(b) the upstream worker receives the EXACT plaintext and the client receives
//	    the exact decrypted echo.
//
// If EncryptingTransport were mutated to forward plaintext, the raw upstream body
// would contain the marker and assertion (a) FAILS. If Seal/Open used divergent
// keys (the original HXC-934 drift risk), the upstream Open would 400 and the
// client echo would mismatch, failing (b).
func TestProxy_ChatCompletionsRoundTrip_WireIsCiphertext(t *testing.T) {
	const runUUID = "e1f3a7c2-9b40-4d61-8e2a-1532c0de0001"
	const marker = "SECRET-PROMPT: summarize the merger terms."

	clientSess, upstreamSess := establishSharedSessions(t)

	// Upstream: raw capture -> DecryptingHandler(upstreamSess) -> echo worker.
	worker := &workerRecorder{inner: echoHandler()}
	cap := &rawCapturingHandler{inner: DecryptingHandler(upstreamSess, worker)}
	upstream := httptest.NewServer(cap)
	defer upstream.Close()

	// Client drives a chat-completions request through the EncryptingTransport.
	client := &http.Client{Transport: &EncryptingTransport{Session: clientSess}}
	req, err := http.NewRequest(http.MethodPost, upstream.URL+"/v1/chat/completions",
		bytes.NewReader([]byte(chatCompletionsPayload)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do: %v", err)
	}
	defer resp.Body.Close()
	clientGot, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	raw := cap.captured()

	// (a) Wire carries ciphertext + routing metadata ONLY.
	if bytes.Contains(raw, []byte(marker)) {
		t.Fatalf("WIRE LEAK: raw upstream body contains plaintext marker %q — encryption disabled? raw=%q", marker, raw)
	}
	if bytes.Contains(raw, []byte(`"messages"`)) {
		t.Fatalf("WIRE LEAK: raw upstream body contains the cleartext JSON field \"messages\" — payload not sealed")
	}
	if len(raw) == 0 {
		t.Fatalf("expected sealed bytes on the wire, got empty raw body")
	}

	// (b) The worker behind the decrypting handler must have seen the EXACT
	// plaintext payload (sink-side evidence: the decrypted bytes reached the app).
	if workerSaw := worker.seen(); string(workerSaw) != chatCompletionsPayload {
		t.Fatalf("worker plaintext mismatch:\n got=%q\nwant=%q", workerSaw, chatCompletionsPayload)
	}

	// (b cont.) The client must receive the correctly decrypted echo.
	if string(clientGot) != chatCompletionsPayload {
		t.Fatalf("client decrypted echo mismatch:\n got=%q\nwant=%q", clientGot, chatCompletionsPayload)
	}

	t.Logf("run %s: REAL round-trip proven — %d-byte chat payload sealed to %d raw on-wire bytes (ciphertext, marker ABSENT), upstream worker saw exact plaintext, client received exact echo. raw[:16]=%x",
		runUUID, len(chatCompletionsPayload), len(raw), raw[:min(16, len(raw))])
}

// workerRecorder records the plaintext body the upstream worker actually sees
// AFTER DecryptingHandler has opened the sealed request. This is sink-side
// evidence that the decrypted plaintext reached the application, not merely that
// a function returned without error.
type workerRecorder struct {
	mu    sync.Mutex
	body  []byte
	inner http.Handler
}

func (h *workerRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	h.mu.Lock()
	h.body = append([]byte(nil), raw...)
	h.mu.Unlock()
	r.Body = io.NopCloser(bytes.NewReader(raw))
	h.inner.ServeHTTP(w, r)
}

func (h *workerRecorder) seen() []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]byte(nil), h.body...)
}

// TestProxy_RoundTripIntegrityLargeRandomPayload proves the full E2EE proxy
// round-trip is byte-exact for a large random payload (no truncation, no
// corruption), and that the on-wire sealed form differs from the plaintext.
func TestProxy_RoundTripIntegrityLargeRandomPayload(t *testing.T) {
	const runUUID = "e1f3a7c2-9b40-4d61-8e2a-1532c0de0002"

	clientSess, upstreamSess := establishSharedSessions(t)

	payload := make([]byte, 64*1024)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("rand: %v", err)
	}

	cap := &rawCapturingHandler{inner: DecryptingHandler(upstreamSess, echoHandler())}
	upstream := httptest.NewServer(cap)
	defer upstream.Close()

	client := &http.Client{Transport: &EncryptingTransport{Session: clientSess}}
	resp, err := client.Post(upstream.URL+"/blob", "application/octet-stream", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("client.Post: %v", err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	if !bytes.Equal(got, payload) {
		t.Fatalf("round-trip corruption: got %d bytes, want %d (equal=%v)", len(got), len(payload), bytes.Equal(got, payload))
	}

	raw := cap.captured()
	// Sealed wire form = nonce(12)+ciphertext(len)+tag(16) — strictly larger and
	// not byte-equal to plaintext.
	if bytes.Equal(raw, payload) {
		t.Fatalf("WIRE LEAK: raw on-wire body equals plaintext payload — not encrypted")
	}
	if len(raw) != len(payload)+e2ee.NonceSize+16 {
		t.Fatalf("unexpected sealed size: got %d want %d (plaintext %d + nonce %d + tag 16)",
			len(raw), len(payload)+e2ee.NonceSize+16, len(payload), e2ee.NonceSize)
	}

	t.Logf("run %s: delta proven — %d-byte random plaintext round-tripped byte-exact through proxy while wire carried %d sealed bytes (+%d nonce/tag overhead), wire != plaintext",
		runUUID, len(payload), len(raw), e2ee.NonceSize+16)
}

// TestProxy_WrongKeyMakesDecryptFail is the mutation guard required by HXC-934:
// it proves Open genuinely depends on key agreement. A DecryptingHandler keyed
// with an UNRELATED session (a different ML-KEM handshake) must REJECT the
// client's sealed request — the upstream cannot recover the plaintext, so the
// echo round-trip cannot complete and the worker never sees the secret. This is
// exactly the property a real (non-stub) AEAD must hold: swap the key and
// decrypt fails. (Two complementary checks: the raw Session.Open call fails, and
// the end-to-end proxy path never delivers plaintext.)
func TestProxy_WrongKeyMakesDecryptFail(t *testing.T) {
	const runUUID = "e1f3a7c2-9b40-4d61-8e2a-1532c0de0003"

	clientSess, _ := establishSharedSessions(t)

	// A SECOND, unrelated handshake yields a key that does NOT match clientSess.
	_, wrongUpstreamSess := establishSharedSessions(t)
	if e2ee.KeysEqual(clientSess, wrongUpstreamSess) {
		t.Fatalf("precondition failed: two independent handshakes produced equal keys")
	}

	// Direct primitive check: a record sealed under clientSess must NOT open
	// under the unrelated key. This is the load-bearing crypto assertion.
	sealed, err := clientSess.Seal([]byte(chatCompletionsPayload), nil)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := wrongUpstreamSess.Open(sealed, nil); err == nil {
		t.Fatalf("wrong-key Open SUCCEEDED — key agreement not enforced (AEAD is a stub?)")
	}

	// End-to-end check through the proxy: the upstream DecryptingHandler keyed
	// with the wrong session must reject the sealed request (HTTP 400 from the
	// handler), the worker behind it must NEVER see the plaintext, and the
	// client must never receive a successfully decrypted secret echo.
	worker := &workerRecorder{inner: echoHandler()}
	upstream := httptest.NewServer(DecryptingHandler(wrongUpstreamSess, worker))
	defer upstream.Close()

	client := &http.Client{Transport: &EncryptingTransport{Session: clientSess}}
	resp, err := client.Post(upstream.URL+"/v1/chat/completions", "application/json",
		bytes.NewReader([]byte(chatCompletionsPayload)))
	// The handler returns a PLAINTEXT 400 error body (decryption failed before
	// any sealing), so the EncryptingTransport's response-Open fails and surfaces
	// as a client.Do error. Either an error OR a non-200 status is acceptable;
	// the inviolable property is that NO plaintext secret is delivered.
	if err == nil {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusOK && bytes.Contains(body, []byte("SECRET-PROMPT")) {
			t.Fatalf("wrong-key path delivered the decrypted secret to the client: status=%d body=%q", resp.StatusCode, body)
		}
	}
	if workerSaw := worker.seen(); bytes.Contains(workerSaw, []byte("SECRET-PROMPT")) {
		t.Fatalf("wrong-key path leaked plaintext to the upstream worker: %q", workerSaw)
	}
	t.Logf("run %s: mutation proven — sealing under one key and opening under a different (unrelated ML-KEM) key fails: raw Open rejected, worker saw no plaintext, client got no secret echo", runUUID)
}

// TestSession_OpenRejectsTamperedCiphertext proves the AEAD authenticates: a
// single flipped bit in the sealed payload makes Open fail rather than return
// garbage plaintext. It uses the canonical e2ee Session directly to exercise the
// crypto primitive the proxy relies on.
func TestSession_OpenRejectsTamperedCiphertext(t *testing.T) {
	const runUUID = "e1f3a7c2-9b40-4d61-8e2a-1532c0de0004"
	clientSess, upstreamSess := establishSharedSessions(t)

	sealed, err := clientSess.Seal([]byte("authentic"), nil)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := upstreamSess.Open(sealed, nil); err != nil {
		t.Fatalf("untampered open should succeed: %v", err)
	}

	// Re-seal (fresh nonce) so the tamper test is not rejected for nonce reuse.
	tamperedSrc, err := clientSess.Seal([]byte("authentic"), nil)
	if err != nil {
		t.Fatalf("seal2: %v", err)
	}
	tampered := append([]byte(nil), tamperedSrc...)
	tampered[len(tampered)-1] ^= 0x01 // flip a tag bit
	if _, err := upstreamSess.Open(tampered, nil); err == nil {
		t.Fatalf("tampered open must FAIL but succeeded — AEAD authentication not enforced")
	}
	t.Logf("run %s: delta proven — untampered Open OK; 1-bit tag flip -> Open rejected (authenticated)", runUUID)
}
