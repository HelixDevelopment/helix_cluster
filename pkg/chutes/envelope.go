package chutes

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// Envelope errors.
var (
	// ErrKeySize is returned when a ReferenceEncryptor key is not 32 bytes.
	ErrKeySize = errors.New("chutes: AES-256 key must be 32 bytes")
	// ErrCiphertextShort is returned when a ciphertext is too short to contain
	// the nonce.
	ErrCiphertextShort = errors.New("chutes: ciphertext too short")
	// ErrOpen is returned when authenticated decryption fails (tamper/wrong key/
	// wrong AAD). It wraps the underlying GCM error.
	ErrOpen = errors.New("chutes: open failed")
)

// Encryptor is the encryption seam. Seal authenticates and encrypts plaintext
// binding the associated data (aad); Open reverses it and FAILS if the
// ciphertext or aad was tampered with. digital.vasic.security/pkg/e2ee (PQ
// ML-KEM hybrid E2EE) implements this same interface and plugs in unchanged.
type Encryptor interface {
	// Seal returns ciphertext that authenticates both plaintext and aad.
	Seal(plaintext, aad []byte) ([]byte, error)
	// Open returns the plaintext, or a non-nil error if authentication fails.
	Open(ciphertext, aad []byte) ([]byte, error)
	// Algorithm names the construction, for audit logging.
	Algorithm() string
}

// ReferenceEncryptor is a REAL, working AES-256-GCM AEAD implementation. It is
// not a stub: Seal/Open round-trip actual ciphertext and Open rejects any
// tampered byte via the GCM authentication tag.
type ReferenceEncryptor struct {
	aead cipher.AEAD
}

// NewReferenceEncryptor builds an AES-256-GCM encryptor from a 32-byte key.
func NewReferenceEncryptor(key []byte) (*ReferenceEncryptor, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("%w: got %d", ErrKeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("chutes: new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("chutes: new gcm: %w", err)
	}
	return &ReferenceEncryptor{aead: aead}, nil
}

// Algorithm implements Encryptor.
func (e *ReferenceEncryptor) Algorithm() string { return "AES-256-GCM" }

// Seal implements Encryptor. The output layout is nonce||sealed, where sealed is
// GCM's ciphertext+tag. A fresh random nonce is generated per call.
func (e *ReferenceEncryptor) Seal(plaintext, aad []byte) ([]byte, error) {
	nonce := make([]byte, e.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("chutes: nonce: %w", err)
	}
	// Seal appends to the nonce slice so the nonce prefixes the ciphertext.
	return e.aead.Seal(nonce, nonce, plaintext, aad), nil
}

// Open implements Encryptor. It splits the nonce prefix and verifies the tag;
// any tamper (ciphertext byte flip or mismatched aad) yields ErrOpen.
func (e *ReferenceEncryptor) Open(ciphertext, aad []byte) ([]byte, error) {
	ns := e.aead.NonceSize()
	if len(ciphertext) < ns {
		return nil, ErrCiphertextShort
	}
	nonce, sealed := ciphertext[:ns], ciphertext[ns:]
	pt, err := e.aead.Open(nil, nonce, sealed, aad)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOpen, err)
	}
	return pt, nil
}

// compile-time assertion.
var _ Encryptor = (*ReferenceEncryptor)(nil)

// AuditEntry is one append-only record describing an encrypted inference round
// trip. These are the fields end-user-visible audit dashboards depend on.
type AuditEntry struct {
	// Time is when the proxy processed the request.
	Time time.Time
	// Principal is who initiated the request (caller-supplied identity).
	Principal string
	// Model is the inference model name.
	Model string
	// TEE records whether the round trip ran inside a Trusted Execution Env.
	TEE bool
	// Algorithm is the Encryptor.Algorithm() used.
	Algorithm string
	// PlaintextBytes / CiphertextBytes record sizes for accounting.
	PlaintextBytes  int
	CiphertextBytes int
}

// AuditSink receives audit entries. An in-memory implementation is provided for
// tests; production wiring can forward to hxcregistry / an append-only log.
type AuditSink interface {
	Append(AuditEntry)
}

// MemoryAuditSink is a concurrency-safe in-memory AuditSink.
type MemoryAuditSink struct {
	mu      sync.Mutex
	entries []AuditEntry
}

// Append implements AuditSink.
func (m *MemoryAuditSink) Append(e AuditEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, e)
}

// Entries returns a copy of the recorded entries in append order.
func (m *MemoryAuditSink) Entries() []AuditEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]AuditEntry, len(m.entries))
	copy(out, m.entries)
	return out
}

// EncryptedInferenceProxy wraps an inference round trip through an Encryptor and
// writes an audit entry for each processed request. It does not itself perform
// inference; it secures and accounts for the request/response payloads that flow
// through a provider, so the same proxy fronts either the stdlib reference
// crypto or the PQ E2EE submodule implementation.
type EncryptedInferenceProxy struct {
	enc  Encryptor
	sink AuditSink
	tee  bool
	now  func() time.Time
}

// ProxyConfig configures an EncryptedInferenceProxy.
type ProxyConfig struct {
	// Encryptor secures payloads (required).
	Encryptor Encryptor
	// Sink receives audit entries (required).
	Sink AuditSink
	// TEE flags that the surrounding execution runs inside a TEE.
	TEE bool
	// Now overrides the clock for deterministic tests; nil uses time.Now.
	Now func() time.Time
}

// NewEncryptedInferenceProxy builds a proxy. It errors if encryptor or sink is
// nil so a misconfigured proxy cannot silently skip encryption or auditing.
func NewEncryptedInferenceProxy(cfg ProxyConfig) (*EncryptedInferenceProxy, error) {
	if cfg.Encryptor == nil {
		return nil, errors.New("chutes: proxy requires an Encryptor")
	}
	if cfg.Sink == nil {
		return nil, errors.New("chutes: proxy requires an AuditSink")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &EncryptedInferenceProxy{enc: cfg.Encryptor, sink: cfg.Sink, tee: cfg.TEE, now: now}, nil
}

// SealRequest encrypts a request payload for principal/model, binding the model
// name as associated data, and records an audit entry. It returns the sealed
// ciphertext.
func (p *EncryptedInferenceProxy) SealRequest(principal, model string, plaintext []byte) ([]byte, error) {
	aad := []byte(model)
	ct, err := p.enc.Seal(plaintext, aad)
	if err != nil {
		return nil, err
	}
	p.sink.Append(AuditEntry{
		Time:            p.now(),
		Principal:       principal,
		Model:           model,
		TEE:             p.tee,
		Algorithm:       p.enc.Algorithm(),
		PlaintextBytes:  len(plaintext),
		CiphertextBytes: len(ct),
	})
	return ct, nil
}

// OpenResponse decrypts a sealed response payload, binding model as associated
// data so a response routed to the wrong model fails authentication.
func (p *EncryptedInferenceProxy) OpenResponse(model string, ciphertext []byte) ([]byte, error) {
	return p.enc.Open(ciphertext, []byte(model))
}
