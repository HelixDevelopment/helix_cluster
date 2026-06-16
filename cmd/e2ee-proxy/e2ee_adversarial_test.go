package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"digital.vasic.security/pkg/e2ee"
)

// timeGuard fails the test if the body of fn does not complete within d. It
// guards every test in this file against a hang (a malformed-input DoS would
// otherwise block the package run indefinitely).
func timeGuard(t *testing.T, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("operation did not complete within %s — possible hang/DoS", d)
	}
}

// tamperingRoundTripper relays to an inner DecryptingHandler-backed server but
// flips one bit of the SEALED RESPONSE body on the way back to the client-side
// EncryptingTransport. It models an active on-wire attacker mutating the
// upstream->client direction. The client's response-Open MUST reject it.
type tamperingRoundTripper struct {
	inner http.RoundTripper
	flip  int // byte index from the end of the sealed response to flip
}

func (rt *tamperingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := rt.inner.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	sealed, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if len(sealed) > rt.flip {
		sealed[len(sealed)-1-rt.flip] ^= 0x01
	}
	resp.Body = io.NopCloser(bytes.NewReader(sealed))
	resp.ContentLength = int64(len(sealed))
	return resp, nil
}

// TestEncryptingTransport_RejectsTamperedResponse proves the client side of the
// channel authenticates the RESPONSE, not just the request. An attacker who
// flips a bit of the sealed response body must NOT be able to make the client
// surface forged plaintext: RoundTrip must return an error and deliver NO body.
//
// The existing suite proves the upstream rejects a tampered REQUEST; this closes
// the symmetric gap on the response direction through the real transport adapter.
func TestEncryptingTransport_RejectsTamperedResponse(t *testing.T) {
	clientSess, upstreamSess := establishSharedSessions(t)

	const secret = "RESPONSE-SECRET-do-not-forge"
	worker := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
		_, _ = w.Write([]byte(secret))
	})
	upstream := httptest.NewServer(DecryptingHandler(upstreamSess, worker))
	defer upstream.Close()

	// Client transport whose underlying RoundTripper tampers the sealed response.
	tt := &EncryptingTransport{
		Session: clientSess,
		Next:    &tamperingRoundTripper{inner: http.DefaultTransport},
	}
	client := &http.Client{Transport: tt}

	timeGuard(t, 10*time.Second, func() {
		resp, err := client.Post(upstream.URL+"/x", "application/json", strings.NewReader("hello"))
		if err != nil {
			// Expected: response-Open failed -> client.Do surfaces an error. PASS.
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if bytes.Contains(body, []byte(secret)) {
			t.Fatalf("FAIL-OPEN: tampered sealed response delivered forged plaintext to client: %q", body)
		}
		// A non-secret body with no error is also acceptable only if it is not the
		// forged plaintext; but a successful 200 carrying the secret is the bug.
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("tampered response yielded HTTP 200 with body %q — Open did not reject the forgery", body)
		}
	})
}

// TestDecryptingHandler_RejectsReplayedRequest proves the proxy's decrypt path
// inherits the e2ee single-use (replay) guarantee: a sealed request that the
// upstream already opened ONCE must be rejected on a verbatim replay, and the
// worker must NOT see the plaintext a second time. A passive attacker who
// captures one sealed request and re-sends it must not get it re-processed.
func TestDecryptingHandler_RejectsReplayedRequest(t *testing.T) {
	clientSess, upstreamSess := establishSharedSessions(t)

	worker := &workerRecorder{inner: echoHandler()}
	handler := DecryptingHandler(upstreamSess, worker)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// Seal one request body with the client session (a single sealed record).
	const payload = "REPLAY-PROBE-secret"
	sealed, err := clientSess.Seal([]byte(payload), nilAAD)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	post := func() (int, []byte) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/x", bytes.NewReader(sealed))
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}

	timeGuard(t, 10*time.Second, func() {
		// First delivery: opens fine, worker sees plaintext.
		st1, _ := post()
		if st1 != http.StatusOK {
			t.Fatalf("first delivery should succeed, got status %d", st1)
		}
		if got := worker.seen(); string(got) != payload {
			t.Fatalf("worker did not see plaintext on first delivery: %q", got)
		}

		// Replay the IDENTICAL sealed bytes. The session already consumed this
		// nonce -> Open must reject -> handler returns 400 (not 200).
		st2, _ := post()
		if st2 == http.StatusOK {
			t.Fatalf("REPLAY ACCEPTED: identical sealed request opened twice (status %d) — replay protection bypassed", st2)
		}
	})
}

// TestDecryptingHandler_MalformedSealedBody_NoPanicNoLeak feeds hostile,
// non-sealed bytes straight at the upstream decrypt handler. Every case MUST
// yield a clean 4xx with NO panic, NO hang, and NO echo of the attacker bytes
// (the handler's error body must not reflect the input).
func TestDecryptingHandler_MalformedSealedBody_NoPanicNoLeak(t *testing.T) {
	_, upstreamSess := establishSharedSessions(t)
	worker := &workerRecorder{inner: echoHandler()}
	srv := httptest.NewServer(DecryptingHandler(upstreamSess, worker))
	defer srv.Close()

	marker := []byte("ATTACKER-CONTROLLED-MARKER")
	cases := map[string][]byte{
		"empty":                {},
		"one-byte":             {0x00},
		"shorter-than-nonce":   bytes.Repeat([]byte{0xAB}, e2ee.NonceSize-1),
		"nonce-only":           bytes.Repeat([]byte{0xCD}, e2ee.NonceSize),
		"nonce-plus-short-tag": bytes.Repeat([]byte{0xEF}, e2ee.NonceSize+8),
		"random-with-marker":   append(bytes.Repeat([]byte{0x11}, e2ee.NonceSize+16), marker...),
		"all-zero-record":      make([]byte, e2ee.NonceSize+16+32),
		"plaintext-not-sealed": []byte(`{"messages":"` + string(marker) + `"}`),
	}

	for name, body := range cases {
		body := body
		t.Run(name, func(t *testing.T) {
			timeGuard(t, 10*time.Second, func() {
				req, _ := http.NewRequest(http.MethodPost, srv.URL+"/x", bytes.NewReader(body))
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatalf("do: %v", err)
				}
				defer resp.Body.Close()
				respBody, _ := io.ReadAll(resp.Body)
				if resp.StatusCode == http.StatusOK {
					t.Fatalf("malformed input %q accepted with HTTP 200 (fail-open)", name)
				}
				if resp.StatusCode < 400 || resp.StatusCode >= 500 {
					// A decrypt failure on attacker input is a client error (4xx);
					// 5xx would indicate an internal fault path. 400 is expected.
					if resp.StatusCode != http.StatusBadRequest {
						t.Fatalf("unexpected status %d for malformed input %q", resp.StatusCode, name)
					}
				}
				if bytes.Contains(respBody, marker) {
					t.Fatalf("LEAK: handler error response reflected attacker bytes: %q", respBody)
				}
				if got := worker.seen(); bytes.Contains(got, marker) {
					t.Fatalf("LEAK: worker saw attacker-controlled bytes from malformed input %q: %q", name, got)
				}
			})
		})
	}
}

// TestEncryptingTransport_NilSession_Errors proves the transport fails closed
// when misconfigured with no session — it must never forward a plaintext body
// unsealed. (Guards against a refactor dropping the nil check, which would send
// the request body in cleartext to the upstream.)
func TestEncryptingTransport_NilSession_Errors(t *testing.T) {
	tt := &EncryptingTransport{Session: nil}
	req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:0/x", strings.NewReader("PLAINTEXT-SECRET"))
	timeGuard(t, 5*time.Second, func() {
		resp, err := tt.RoundTrip(req)
		if err == nil {
			if resp != nil {
				_ = resp.Body.Close()
			}
			t.Fatalf("nil-Session RoundTrip must error (fail closed), got nil error")
		}
	})
}

// TestDecryptingHandler_DoesNotLeakBodySecretIntoWireHeaders documents and pins
// the framing contract: the proxy seals the response BODY, and a worker's
// body-derived secret must not cross the relay in cleartext. The body is the
// confidential channel; this asserts the sealed wire body never contains the
// worker's secret and the on-wire Content-Type is the opaque octet-stream.
//
// (Response HEADERS the worker sets ARE forwarded in cleartext by design —
// headers are routing metadata, not the sealed payload. This test pins that the
// BODY secret specifically does not leak, which is the load-bearing guarantee.)
func TestDecryptingHandler_DoesNotLeakBodySecretIntoWireHeaders(t *testing.T) {
	clientSess, upstreamSess := establishSharedSessions(t)

	const bodySecret = "BODY-SECRET-merger-terms"
	worker := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
		_, _ = w.Write([]byte(bodySecret))
	})

	// Capture the RAW sealed response bytes + headers leaving the upstream.
	var rawResp []byte
	var rawHdr http.Header
	capturing := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		DecryptingHandler(upstreamSess, worker).ServeHTTP(rec, r)
		res := rec.Result()
		rawResp, _ = io.ReadAll(res.Body)
		rawHdr = res.Header.Clone()
		for k, vs := range res.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(res.StatusCode)
		_, _ = w.Write(rawResp)
	})
	upstream := httptest.NewServer(capturing)
	defer upstream.Close()

	client := &http.Client{Transport: &EncryptingTransport{Session: clientSess}}
	timeGuard(t, 10*time.Second, func() {
		resp, err := client.Post(upstream.URL+"/x", "application/json", strings.NewReader("q"))
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		defer resp.Body.Close()
		got, _ := io.ReadAll(resp.Body)
		if string(got) != bodySecret {
			t.Fatalf("client did not receive correctly decrypted body: %q", got)
		}
	})

	if bytes.Contains(rawResp, []byte(bodySecret)) {
		t.Fatalf("WIRE LEAK: sealed response body contains the worker's body secret in cleartext: %q", rawResp)
	}
	if ct := rawHdr.Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("on-wire response Content-Type should be opaque octet-stream, got %q", ct)
	}
	for k, vs := range rawHdr {
		for _, v := range vs {
			if strings.Contains(v, bodySecret) {
				t.Fatalf("WIRE LEAK: on-wire response header %q carried the body secret: %q", k, v)
			}
		}
	}
}
