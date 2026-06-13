package chutes

// HXC-1431: E2EE inference envelope wiring pkg/chutes to the real
// post-quantum end-to-end encryption package digital.vasic.security/pkg/e2ee.
//
// The existing envelope.go defines the Encryptor seam plus a stdlib
// AES-256-GCM ReferenceEncryptor. This file supplies the REAL crypto: it
// adapts an established e2ee.Session (ML-KEM-768 hybrid handshake -> HKDF-SHA-256
// session key -> AES-256-GCM / ChaCha20-Poly1305 AEAD with per-message nonces
// and single-use replay rejection) to that same Encryptor seam, and exposes an
// EncryptedEnvelope that seals an inference REQUEST before transit and opens it
// at the other end — and likewise for the RESPONSE.
//
// Where it plugs in: EncryptedInferenceProxy (envelope.go) already consumes any
// Encryptor. NewE2EESessionEncryptor produces an Encryptor backed by e2ee, so
// the serving path obtains real end-to-end encryption by constructing the proxy
// with a SessionEncryptor instead of a ReferenceEncryptor — no proxy change.
// EncryptedEnvelope is the standalone request/response sealing layer the chutes
// client/server transit path adopts directly: the sender peer holds the request
// sealer + response opener; the receiver peer holds the request opener +
// response sealer. The two peers are keyed from one e2ee handshake (Initiator +
// Respond) or, for out-of-band-keyed sessions, NewSessionFromKey on each side.

import (
	"errors"
	"fmt"

	"digital.vasic.security/pkg/e2ee"
)

// E2EE envelope errors.
var (
	// ErrNilSession is returned when an envelope is built without a Session.
	ErrNilSession = errors.New("chutes: e2ee envelope requires a non-nil *e2ee.Session")
)

// SessionEncryptor adapts an established digital.vasic.security/pkg/e2ee.Session
// to the chutes Encryptor seam. Seal/Open delegate to the REAL e2ee AEAD: each
// Seal emits a record carrying a unique per-message nonce, and Open verifies the
// AEAD tag and rejects nonce reuse (replay). This is not a stub — it is the
// post-quantum E2EE path the Encryptor interface was designed to accept.
//
// Because e2ee.Session.Open enforces single-use of every nonce, a SessionEncryptor
// is directional: the sealing peer and the opening peer hold DISTINCT sessions
// derived from the same key (the normal two-peer arrangement). Sealing and opening
// on one Session is only valid for traffic that genuinely flows one-way through it.
type SessionEncryptor struct {
	sess *e2ee.Session
}

// NewE2EESessionEncryptor wraps an established e2ee.Session as an Encryptor.
// It errors on a nil session so a misconfigured envelope cannot silently skip
// real encryption.
func NewE2EESessionEncryptor(sess *e2ee.Session) (*SessionEncryptor, error) {
	if sess == nil {
		return nil, ErrNilSession
	}
	return &SessionEncryptor{sess: sess}, nil
}

// Algorithm implements Encryptor. It names the post-quantum hybrid construction
// so audit entries record that the real E2EE path (not the stdlib reference)
// secured the payload.
func (s *SessionEncryptor) Algorithm() string {
	return "ML-KEM-768/HKDF-SHA-256/AEAD (digital.vasic.security/pkg/e2ee)"
}

// Seal implements Encryptor by delegating to the real e2ee.Session.Seal. The
// returned record contains the AEAD nonce and tag; aad is authenticated but not
// encrypted.
func (s *SessionEncryptor) Seal(plaintext, aad []byte) ([]byte, error) {
	record, err := s.sess.Seal(plaintext, aad)
	if err != nil {
		return nil, fmt.Errorf("chutes: e2ee seal: %w", err)
	}
	return record, nil
}

// Open implements Encryptor by delegating to the real e2ee.Session.Open. It
// fails (non-nil error) on a flipped ciphertext byte, a wrong key, an aad
// mismatch, or a replayed nonce — the AEAD integrity guarantee, surfaced not
// swallowed.
func (s *SessionEncryptor) Open(ciphertext, aad []byte) ([]byte, error) {
	plain, err := s.sess.Open(ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOpen, err)
	}
	return plain, nil
}

// compile-time assertion: the real e2ee adapter satisfies the same seam as the
// stdlib ReferenceEncryptor.
var _ Encryptor = (*SessionEncryptor)(nil)

// EncryptedEnvelope is the end-to-end encrypted inference envelope between two
// peers (e.g. a chutes client and the serving side). It holds the two directional
// e2ee sessions of one logical channel:
//
//   - requestSealer / requestOpener: the request travels sealer -> opener.
//   - responseSealer / responseOpener: the response travels sealer -> opener.
//
// A single peer constructs an EncryptedEnvelope from its own viewpoint via
// NewClientEnvelope (seals requests, opens responses) or NewServerEnvelope
// (opens requests, seals responses). The two viewpoints share key material from
// one handshake, so a request sealed by the client opens on the server and a
// response sealed by the server opens on the client — byte-exact.
type EncryptedEnvelope struct {
	requestSealer  *SessionEncryptor
	requestOpener  *SessionEncryptor
	responseSealer *SessionEncryptor
	responseOpener *SessionEncryptor
	// aad binds an associated-data label (e.g. the model name) into every
	// record, so a payload replayed onto a different model/route fails Open.
	aad []byte
}

// EnvelopeConfig configures an EncryptedEnvelope.
//
// RequestToServer and ResponseToClient are the two directional sessions of the
// channel. From the CLIENT viewpoint, RequestToServer is the session the client
// SEALS requests with and ResponseToClient is the session the client OPENS
// responses with. From the SERVER viewpoint they are mirrored (server opens
// RequestToServer, seals ResponseToClient). Use the NewClientEnvelope /
// NewServerEnvelope constructors rather than building this struct by hand.
type EnvelopeConfig struct {
	// RequestToServer carries client->server request records.
	RequestToServer *e2ee.Session
	// ResponseToClient carries server->client response records.
	ResponseToClient *e2ee.Session
	// AAD is bound into every record (commonly the model name). May be nil.
	AAD []byte
}

// NewClientEnvelope builds the CLIENT-side envelope: it seals outbound requests
// on RequestToServer and opens inbound responses on ResponseToClient.
func NewClientEnvelope(cfg EnvelopeConfig) (*EncryptedEnvelope, error) {
	sealer, err := NewE2EESessionEncryptor(cfg.RequestToServer)
	if err != nil {
		return nil, fmt.Errorf("chutes: client envelope request session: %w", err)
	}
	opener, err := NewE2EESessionEncryptor(cfg.ResponseToClient)
	if err != nil {
		return nil, fmt.Errorf("chutes: client envelope response session: %w", err)
	}
	return &EncryptedEnvelope{
		requestSealer:  sealer,
		responseOpener: opener,
		aad:            cfg.AAD,
	}, nil
}

// NewServerEnvelope builds the SERVER-side envelope: it opens inbound requests on
// RequestToServer and seals outbound responses on ResponseToClient.
func NewServerEnvelope(cfg EnvelopeConfig) (*EncryptedEnvelope, error) {
	opener, err := NewE2EESessionEncryptor(cfg.RequestToServer)
	if err != nil {
		return nil, fmt.Errorf("chutes: server envelope request session: %w", err)
	}
	sealer, err := NewE2EESessionEncryptor(cfg.ResponseToClient)
	if err != nil {
		return nil, fmt.Errorf("chutes: server envelope response session: %w", err)
	}
	return &EncryptedEnvelope{
		requestOpener:  opener,
		responseSealer: sealer,
		aad:            cfg.AAD,
	}, nil
}

// SealRequest seals an inference request payload for transit (client side). The
// returned bytes are the on-the-wire record; they MUST differ from plaintext and
// are openable only by the peer holding the matching request session.
func (e *EncryptedEnvelope) SealRequest(plaintext []byte) ([]byte, error) {
	if e.requestSealer == nil {
		return nil, errors.New("chutes: envelope cannot seal requests (server-side viewpoint)")
	}
	return e.requestSealer.Seal(plaintext, e.aad)
}

// OpenRequest opens a sealed inference request (server side), failing on tamper,
// wrong key, aad mismatch, or replay.
func (e *EncryptedEnvelope) OpenRequest(record []byte) ([]byte, error) {
	if e.requestOpener == nil {
		return nil, errors.New("chutes: envelope cannot open requests (client-side viewpoint)")
	}
	return e.requestOpener.Open(record, e.aad)
}

// SealResponse seals an inference response payload for transit (server side).
func (e *EncryptedEnvelope) SealResponse(plaintext []byte) ([]byte, error) {
	if e.responseSealer == nil {
		return nil, errors.New("chutes: envelope cannot seal responses (client-side viewpoint)")
	}
	return e.responseSealer.Seal(plaintext, e.aad)
}

// OpenResponse opens a sealed inference response (client side), failing on
// tamper, wrong key, aad mismatch, or replay.
func (e *EncryptedEnvelope) OpenResponse(record []byte) ([]byte, error) {
	if e.responseOpener == nil {
		return nil, errors.New("chutes: envelope cannot open responses (server-side viewpoint)")
	}
	return e.responseOpener.Open(record, e.aad)
}

// EstablishEnvelopePair runs a REAL e2ee ML-KEM-768 hybrid handshake and returns
// a matched client/server envelope pair sharing the derived key material. It
// builds two directional channels (request and response) so that requests sealed
// by the client open on the server and responses sealed by the server open on the
// client. aad is bound into every record on both ends.
//
// This is the convenience path the serving code adopts to obtain a ready E2EE
// envelope without hand-driving the handshake. For an out-of-band key, callers
// can instead pair NewSessionFromKey-derived sessions through NewClientEnvelope /
// NewServerEnvelope directly.
func EstablishEnvelopePair(aad []byte) (client, server *EncryptedEnvelope, err error) {
	cfg := e2ee.SessionConfig{}

	reqClient, reqServer, err := establishChannel(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("chutes: establish request channel: %w", err)
	}
	respClient, respServer, err := establishChannel(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("chutes: establish response channel: %w", err)
	}

	client, err = NewClientEnvelope(EnvelopeConfig{
		RequestToServer:  reqClient,
		ResponseToClient: respClient,
		AAD:              aad,
	})
	if err != nil {
		return nil, nil, err
	}
	server, err = NewServerEnvelope(EnvelopeConfig{
		RequestToServer:  reqServer,
		ResponseToClient: respServer,
		AAD:              aad,
	})
	if err != nil {
		return nil, nil, err
	}
	return client, server, nil
}

// establishChannel runs one ML-KEM-768 handshake and returns the two peer
// sessions (initiator-side and responder-side) keyed by the same shared secret.
func establishChannel(cfg e2ee.SessionConfig) (initiatorSess, responderSess *e2ee.Session, err error) {
	initiator, err := e2ee.NewInitiator(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("new initiator: %w", err)
	}
	kemCiphertext, responderSess, err := e2ee.Respond(initiator.EncapsulationKeyBytes(), cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("respond: %w", err)
	}
	initiatorSess, err = initiator.Complete(kemCiphertext, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("complete: %w", err)
	}
	return initiatorSess, responderSess, nil
}
