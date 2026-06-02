// Command e2ee-proxy is a transparent ML-KEM-768 encrypting proxy (HXC-1532).
//
// It models a transparent client<->upstream end-to-end-encrypted (E2EE)
// channel: a local listener accepts plaintext HTTP from clients, seals every
// request body under an AES-256-GCM AEAD whose key is derived from an
// ML-KEM-768 (FIPS 203) shared secret, and forwards the sealed bytes to a
// configured upstream. A passive capture on the wire BETWEEN the proxy and the
// upstream observes only ciphertext. The upstream side runs a DecryptingHandler
// that opens the sealed request, hands plaintext to the real handler, and seals
// the response on the way back. The client transparently receives plaintext.
//
// Cryptography (standard library only):
//   - Key establishment: crypto/mlkem ML-KEM-768 (post-quantum KEM).
//   - Key derivation:     crypto/hkdf (HKDF-SHA-256) over the KEM shared secret.
//   - Payload sealing:    crypto/aes + crypto/cipher AES-256-GCM with a fresh
//     crypto/rand nonce prepended to every ciphertext.
//
// All cryptographic logic lives in the Session / EncryptingTransport /
// DecryptingHandler types so the data path is exercisable with net/http/httptest.
package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/mlkem"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

// hkdfInfo binds the derived AEAD key to this protocol/version so the same KEM
// secret used elsewhere would not collide with this channel's key schedule.
const hkdfInfo = "helix-e2ee-proxy/HXC-1532/aes-256-gcm/v1"

// Session holds the AES-256-GCM AEAD derived from an established ML-KEM-768
// shared secret. Both endpoints of the channel hold the SAME Session (same
// derived key), so either can seal and the other can open.
type Session struct {
	aead cipher.AEAD
}

// newSessionFromSharedSecret derives the AES-256-GCM AEAD from a 32-byte
// ML-KEM-768 shared secret via HKDF-SHA-256. Both endpoints run this over the
// identical shared secret and therefore obtain the identical AEAD key.
func newSessionFromSharedSecret(sharedSecret []byte) (*Session, error) {
	if len(sharedSecret) != mlkem.SharedKeySize {
		return nil, fmt.Errorf("e2ee: bad shared secret length %d, want %d", len(sharedSecret), mlkem.SharedKeySize)
	}
	// AES-256 wants a 32-byte key. HKDF expands the KEM secret into it.
	key, err := hkdf.Key(sha256.New, sharedSecret, nil /*salt*/, hkdfInfo, 32)
	if err != nil {
		return nil, fmt.Errorf("e2ee: hkdf: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("e2ee: aes: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("e2ee: gcm: %w", err)
	}
	return &Session{aead: aead}, nil
}

// Seal encrypts plaintext, returning nonce||ciphertext. A fresh random nonce is
// generated per call and prepended so each Seal is independently openable.
func (s *Session) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("e2ee: nonce: %w", err)
	}
	// dst == nonce so the returned slice is nonce||ciphertext.
	return s.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open reverses Seal: it splits off the prepended nonce and authenticates +
// decrypts the remainder. A tampered or truncated payload yields an error.
func (s *Session) Open(sealed []byte) ([]byte, error) {
	ns := s.aead.NonceSize()
	if len(sealed) < ns {
		return nil, errors.New("e2ee: sealed payload shorter than nonce")
	}
	nonce, ct := sealed[:ns], sealed[ns:]
	pt, err := s.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("e2ee: open: %w", err)
	}
	return pt, nil
}

// ServerSession is the upstream-side endpoint. It owns the ML-KEM-768
// decapsulation key and publishes its encapsulation (public) key bytes so a
// client can establish a shared secret with it.
type ServerSession struct {
	dk *mlkem.DecapsulationKey768
}

// NewServerSession generates a fresh ML-KEM-768 key pair for the upstream side.
func NewServerSession() (*ServerSession, error) {
	dk, err := mlkem.GenerateKey768()
	if err != nil {
		return nil, fmt.Errorf("e2ee: generate ml-kem-768: %w", err)
	}
	return &ServerSession{dk: dk}, nil
}

// EncapsulationKeyBytes returns the server's ML-KEM-768 encapsulation (public)
// key, to be handed to a client so it can encapsulate to this server.
func (ss *ServerSession) EncapsulationKeyBytes() []byte {
	return ss.dk.EncapsulationKey().Bytes()
}

// Session derives the shared Session from a client-produced KEM ciphertext by
// decapsulating it to recover the shared secret.
func (ss *ServerSession) Session(kemCiphertext []byte) (*Session, error) {
	sharedSecret, err := ss.dk.Decapsulate(kemCiphertext)
	if err != nil {
		return nil, fmt.Errorf("e2ee: decapsulate: %w", err)
	}
	return newSessionFromSharedSecret(sharedSecret)
}

// ClientSession is the client-side endpoint. It encapsulates to a server's
// published encapsulation key to derive the shared Session, and yields the KEM
// ciphertext the server needs to derive the identical Session.
type ClientSession struct {
	Session       *Session
	KEMCiphertext []byte
}

// NewClientSession encapsulates to the given server encapsulation-key bytes,
// producing the shared Session plus the KEM ciphertext to relay to the server.
func NewClientSession(serverEncapKeyBytes []byte) (*ClientSession, error) {
	ek, err := mlkem.NewEncapsulationKey768(serverEncapKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("e2ee: parse encapsulation key: %w", err)
	}
	sharedSecret, kemCiphertext := ek.Encapsulate()
	sess, err := newSessionFromSharedSecret(sharedSecret)
	if err != nil {
		return nil, err
	}
	return &ClientSession{Session: sess, KEMCiphertext: kemCiphertext}, nil
}

// EncryptingTransport is an http.RoundTripper that seals the outgoing request
// body with the Session AEAD before it leaves toward the upstream, and opens
// the sealed response body before returning it to the caller. To a passive
// capture between this transport and the upstream, both bodies are ciphertext.
type EncryptingTransport struct {
	Session *Session
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
	sealed, err := t.Session.Seal(plain)
	if err != nil {
		return nil, err
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
	openedResp, err := t.Session.Open(sealedResp)
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(openedResp))
	resp.ContentLength = int64(len(openedResp))
	resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(openedResp)))
	return resp, nil
}

// DecryptingHandler is the upstream-side counterpart of EncryptingTransport. It
// opens the sealed request body, replaces it with plaintext for next, and seals
// next's response body before it goes back onto the wire.
func DecryptingHandler(sess *Session, next http.Handler) http.Handler {
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
			opened, err := sess.Open(sealed)
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

		sealed, err := sess.Seal(rec.body.Bytes())
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

	// In a real deployment the encapsulation key would be fetched from the
	// upstream over an authenticated channel. Here we generate the upstream
	// side locally and establish a shared Session so the proxy can run
	// standalone. The full data path is identical to the httptest-covered one.
	server, err := NewServerSession()
	if err != nil {
		log.Fatalf("e2ee-proxy: server session: %v", err)
	}
	client, err := NewClientSession(server.EncapsulationKeyBytes())
	if err != nil {
		log.Fatalf("e2ee-proxy: client session: %v", err)
	}

	transport := &EncryptingTransport{Session: client.Session}
	proxy := newReverseProxy(upstream, transport)

	srv := &http.Server{
		Addr:              *listenAddr,
		Handler:           proxy,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("e2ee-proxy: ML-KEM-768 E2EE proxy listening on %s -> upstream %s (wire payloads sealed with AES-256-GCM)", *listenAddr, upstream)
	log.Fatal(srv.ListenAndServe())
}
