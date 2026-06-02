package main

import (
	"bytes"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// establishSharedSession runs the real ML-KEM-768 handshake and returns the
// single shared Session held by BOTH endpoints (client transport + upstream
// handler). If the two endpoints derived different keys, every Open below would
// fail authentication — so a green test also proves key agreement.
func establishSharedSession(t *testing.T) *Session {
	t.Helper()
	server, err := NewServerSession()
	if err != nil {
		t.Fatalf("NewServerSession: %v", err)
	}
	client, err := NewClientSession(server.EncapsulationKeyBytes())
	if err != nil {
		t.Fatalf("NewClientSession: %v", err)
	}
	// Server derives its Session from the client's KEM ciphertext; it must
	// equal the client's Session. Verify by a cross seal/open round-trip.
	srvSess, err := server.Session(client.KEMCiphertext)
	if err != nil {
		t.Fatalf("server.Session: %v", err)
	}
	probe := []byte("agreement-probe")
	sealed, err := client.Session.Seal(probe)
	if err != nil {
		t.Fatalf("client seal: %v", err)
	}
	got, err := srvSess.Open(sealed)
	if err != nil {
		t.Fatalf("key agreement FAILED: server could not open client-sealed data: %v", err)
	}
	if !bytes.Equal(got, probe) {
		t.Fatalf("key agreement mismatch: got %q want %q", got, probe)
	}
	return srvSess
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

// TestProxy_UpstreamSeesCiphertextClientGetsPlaintext is the LOAD-BEARING GUARD.
//
// before->after delta it proves: the same marker bytes that the client SENDS in
// plaintext are ABSENT from the raw bytes the upstream socket receives (they are
// ciphertext, before->after = plaintext-present -> plaintext-absent on the
// wire), yet the client STILL receives the correctly decrypted echo containing
// the marker. If EncryptingTransport were mutated to forward plaintext, the raw
// upstream body would contain the marker and assertion (a) FAILS.
func TestProxy_UpstreamSeesCiphertextClientGetsPlaintext(t *testing.T) {
	const runUUID = "e1f3a7c2-9b40-4d61-8e2a-1532c0de0001"
	const marker = "SECRET-PLAINTEXT-MARKER"

	sess := establishSharedSession(t)

	// Upstream: raw capture -> DecryptingHandler -> echo.
	cap := &rawCapturingHandler{inner: DecryptingHandler(sess, echoHandler())}
	upstream := httptest.NewServer(cap)
	defer upstream.Close()

	// Client drives a request whose plaintext body carries the marker, through
	// the EncryptingTransport (which seals before the wire).
	client := &http.Client{Transport: &EncryptingTransport{Session: sess}}
	req, err := http.NewRequest(http.MethodPost, upstream.URL+"/echo", bytes.NewReader([]byte(marker)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
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

	// (a) The RAW on-wire upstream body must NOT contain the plaintext marker.
	if bytes.Contains(raw, []byte(marker)) {
		t.Fatalf("WIRE LEAK: raw upstream body contains plaintext marker %q — encryption disabled? raw=%q", marker, raw)
	}
	if len(raw) == 0 {
		t.Fatalf("expected sealed bytes on the wire, got empty raw body")
	}

	// (b) The client must receive the correctly decrypted echo containing the marker.
	if string(clientGot) != marker {
		t.Fatalf("client decrypted echo mismatch: got %q want %q", clientGot, marker)
	}

	t.Logf("run %s: delta proven — plaintext marker %q present at client (sent+received) but ABSENT from %d raw on-wire bytes (ciphertext). raw[:16]=%x",
		runUUID, marker, len(raw), raw[:min(16, len(raw))])
}

// TestProxy_RoundTripIntegrityLargeRandomPayload proves the full E2EE proxy
// round-trip is byte-exact for a large random payload (no truncation, no
// corruption), and that the on-wire sealed form differs from the plaintext.
func TestProxy_RoundTripIntegrityLargeRandomPayload(t *testing.T) {
	const runUUID = "e1f3a7c2-9b40-4d61-8e2a-1532c0de0002"

	sess := establishSharedSession(t)

	payload := make([]byte, 64*1024)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("rand: %v", err)
	}

	cap := &rawCapturingHandler{inner: DecryptingHandler(sess, echoHandler())}
	upstream := httptest.NewServer(cap)
	defer upstream.Close()

	client := &http.Client{Transport: &EncryptingTransport{Session: sess}}
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
	if len(raw) != len(payload)+12+16 {
		t.Fatalf("unexpected sealed size: got %d want %d (plaintext %d + nonce 12 + tag 16)", len(raw), len(payload)+28, len(payload))
	}

	t.Logf("run %s: delta proven — %d-byte random plaintext round-tripped byte-exact through proxy while wire carried %d sealed bytes (+28 nonce/tag overhead), wire != plaintext",
		runUUID, len(payload), len(raw))
}

// TestSession_OpenRejectsTamperedCiphertext proves the AEAD authenticates: a
// single flipped bit in the sealed payload makes Open fail rather than return
// garbage plaintext.
func TestSession_OpenRejectsTamperedCiphertext(t *testing.T) {
	const runUUID = "e1f3a7c2-9b40-4d61-8e2a-1532c0de0003"
	sess := establishSharedSession(t)

	sealed, err := sess.Seal([]byte("authentic"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := sess.Open(sealed); err != nil {
		t.Fatalf("untampered open should succeed: %v", err)
	}
	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 0x01 // flip a tag bit
	if _, err := sess.Open(tampered); err == nil {
		t.Fatalf("tampered open must FAIL but succeeded — AEAD authentication not enforced")
	}
	t.Logf("run %s: delta proven — untampered Open OK; 1-bit tag flip -> Open rejected (authenticated)", runUUID)
}
