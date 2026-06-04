// Command e2ee-proxy is a transparent post-quantum encrypting proxy (HXC-1532).
//
// It models a transparent client<->upstream end-to-end-encrypted (E2EE)
// channel: a local listener accepts plaintext HTTP from clients, seals every
// request body under the canonical post-quantum E2EE session, and forwards the
// sealed bytes to a configured upstream. A passive capture on the wire BETWEEN
// the proxy and the upstream observes only ciphertext. The upstream side runs a
// DecryptingHandler that opens the sealed request, hands plaintext to the real
// handler, and seals the response on the way back. The client transparently
// receives plaintext.
//
// Cryptography is provided entirely by the canonical, separately-tested
// digital.vasic.security/pkg/e2ee package (HXC-934 — this command previously
// reimplemented ML-KEM-768 + HKDF + AES-256-GCM inline; that duplication has
// been deleted in favour of the single canonical implementation):
//   - Key establishment: ML-KEM-768 (FIPS 203) post-quantum KEM via the
//     e2ee.Initiator handshake (EncapsulationKeyBytes / Respond / Complete).
//   - Key derivation:     HKDF-SHA-256 over the KEM shared secret (internal to
//     e2ee, salted by the KEM ciphertext for encapsulation binding).
//   - Payload sealing:    AES-256-GCM AEAD with a fresh per-record nonce that
//     the e2ee.Session prepends to every record, with single-use (replay)
//     enforcement on Open.
//
// All cryptographic logic lives in e2ee; the EncryptingTransport /
// DecryptingHandler types here are thin HTTP adapters so the data path is
// exercisable with net/http/httptest.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"digital.vasic.security/pkg/e2ee"
)

// proxyInfo binds the derived AEAD key to this protocol/version so the same KEM
// secret used elsewhere would not collide with this channel's key schedule. It
// is supplied as e2ee.SessionConfig.Info on BOTH endpoints so they derive the
// identical session key.
const proxyInfo = "helix-e2ee-proxy/HXC-1532/aes-256-gcm/v1"

// proxyConfig is the shared session-derivation configuration both endpoints use.
// Both sides MUST pass the identical config (Info + Suite) for the derived keys
// to agree. The zero-value Suite keeps the AES-256-GCM default.
func proxyConfig() e2ee.SessionConfig {
	return e2ee.SessionConfig{Info: proxyInfo}
}

// nilAAD is the additional-authenticated-data used for proxy records. The proxy
// frames plaintext bodies opaquely and does not bind extra associated data, so
// both Seal and Open pass nil; using a named constant documents the choice.
var nilAAD []byte

// establishProxySessions performs the canonical ML-KEM-768 handshake and returns
// the two ends of the channel: clientSess is held by the client-side
// EncryptingTransport, upstreamSess by the upstream-side DecryptingHandler. Both
// derive the identical session key from the same KEM exchange, so either can
// Seal and the other can Open. In a real deployment the encapsulation key would
// be fetched from the upstream over an authenticated channel and the handshake
// driven across the network; here both ends are established locally so the proxy
// can run standalone. The byte-for-byte data path is identical to the one the
// httptest-covered tests exercise.
func establishProxySessions() (clientSess, upstreamSess *e2ee.Session, err error) {
	cfg := proxyConfig()

	// Upstream is the handshake initiator: it publishes an ephemeral ML-KEM-768
	// encapsulation key.
	initiator, err := e2ee.NewInitiator(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("e2ee-proxy: initiator: %w", err)
	}

	// Client is the responder: it encapsulates against the published key,
	// yielding the KEM ciphertext and its own Session.
	kemCiphertext, clientSession, err := e2ee.Respond(initiator.EncapsulationKeyBytes(), cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("e2ee-proxy: respond: %w", err)
	}

	// Upstream completes the handshake by decapsulating the ciphertext, deriving
	// the identical session key.
	upstreamSession, err := initiator.Complete(kemCiphertext, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("e2ee-proxy: complete: %w", err)
	}

	return clientSession, upstreamSession, nil
}

// EncryptingTransport is an http.RoundTripper that seals the outgoing request
// body with the e2ee Session before it leaves toward the upstream, and opens the
// sealed response body before returning it to the caller. To a passive capture
// between this transport and the upstream, both bodies are ciphertext.
type EncryptingTransport struct {
	Session *e2ee.Session
	// Next is the underlying RoundTripper used to reach the upstream. If nil,
	// http.DefaultTransport is used.
	Next http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (t *EncryptingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.Session == nil {
		return nil, errors.New("e2ee: EncryptingTransport has no Session")
	}

	// Read and seal the plaintext request body.
	var plain []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("e2ee: read request body: %w", err)
		}
		plain = b
	}
	sealed, err := t.Session.Seal(plain, nilAAD)
	if err != nil {
		return nil, fmt.Errorf("e2ee: seal request: %w", err)
	}

	// Clone the request with the sealed body so the original is untouched.
	outReq := req.Clone(req.Context())
	outReq.Body = io.NopCloser(bytes.NewReader(sealed))
	outReq.ContentLength = int64(len(sealed))
	outReq.Header = req.Header.Clone()
	if outReq.Header == nil {
		outReq.Header = http.Header{}
	}
	outReq.Header.Set("Content-Type", "application/octet-stream")
	// GetBody enables transparent redirect/retry with the sealed payload.
	outReq.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(sealed)), nil
	}

	next := t.Next
	if next == nil {
		next = http.DefaultTransport
	}
	resp, err := next.RoundTrip(outReq)
	if err != nil {
		return nil, err
	}

	// Open the sealed response body before handing it back to the caller.
	sealedResp, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("e2ee: read response body: %w", err)
	}
	openedResp, err := t.Session.Open(sealedResp, nilAAD)
	if err != nil {
		return nil, fmt.Errorf("e2ee: open response: %w", err)
	}
	resp.Body = io.NopCloser(bytes.NewReader(openedResp))
	resp.ContentLength = int64(len(openedResp))
	resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(openedResp)))
	return resp, nil
}

// DecryptingHandler is the upstream-side counterpart of EncryptingTransport. It
// opens the sealed request body, replaces it with plaintext for next, and seals
// next's response body before it goes back onto the wire.
func DecryptingHandler(sess *e2ee.Session, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Open the sealed inbound request body.
		var plain []byte
		if r.Body != nil {
			sealed, err := io.ReadAll(r.Body)
			_ = r.Body.Close()
			if err != nil {
				http.Error(w, "e2ee: read sealed body", http.StatusBadRequest)
				return
			}
			opened, err := sess.Open(sealed, nilAAD)
			if err != nil {
				http.Error(w, "e2ee: open sealed body", http.StatusBadRequest)
				return
			}
			plain = opened
		}
		r.Body = io.NopCloser(bytes.NewReader(plain))
		r.ContentLength = int64(len(plain))

		// Capture next's plaintext response, then seal it.
		rec := &responseRecorder{header: http.Header{}, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		sealed, err := sess.Seal(rec.body.Bytes(), nilAAD)
		if err != nil {
			http.Error(w, "e2ee: seal response", http.StatusInternalServerError)
			return
		}
		// Copy through captured headers (minus length, which now differs).
		for k, vs := range rec.header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(sealed)))
		w.WriteHeader(rec.status)
		_, _ = w.Write(sealed)
	})
}

// responseRecorder buffers a downstream handler's response so it can be sealed.
type responseRecorder struct {
	header      http.Header
	body        bytes.Buffer
	status      int
	wroteHeader bool
}

func (r *responseRecorder) Header() http.Header { return r.header }

func (r *responseRecorder) WriteHeader(status int) {
	if !r.wroteHeader {
		r.status = status
		r.wroteHeader = true
	}
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	return r.body.Write(b)
}

// newReverseProxy builds an httputil.ReverseProxy that targets upstream and
// uses the EncryptingTransport, so every proxied request is sealed on the wire.
func newReverseProxy(upstream *url.URL, transport *EncryptingTransport) *httputil.ReverseProxy {
	rp := httputil.NewSingleHostReverseProxy(upstream)
	rp.Transport = transport
	return rp
}

func main() {
	listenAddr := flag.String("listen", "127.0.0.1:8443", "local plaintext listen address")
	upstreamAddr := flag.String("upstream", "http://127.0.0.1:9000", "upstream base URL (proxy target)")
	flag.Parse()

	upstream, err := url.Parse(*upstreamAddr)
	if err != nil {
		log.Fatalf("e2ee-proxy: invalid -upstream %q: %v", *upstreamAddr, err)
	}

	// Establish the shared post-quantum E2EE session via the canonical handshake.
	clientSess, _, err := establishProxySessions()
	if err != nil {
		log.Fatalf("e2ee-proxy: establish session: %v", err)
	}

	transport := &EncryptingTransport{Session: clientSess}
	proxy := newReverseProxy(upstream, transport)

	srv := &http.Server{
		Addr:              *listenAddr,
		Handler:           proxy,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("e2ee-proxy: ML-KEM-768 E2EE proxy listening on %s -> upstream %s (wire payloads sealed with AES-256-GCM via digital.vasic.security/pkg/e2ee)", *listenAddr, upstream)
	log.Fatal(srv.ListenAndServe())
}
