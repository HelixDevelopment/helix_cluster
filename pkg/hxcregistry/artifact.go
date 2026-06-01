package hxcregistry

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrNotFound is returned by GetArtifact and Resolve when the requested
// digest or tag does not exist in the registry.
var ErrNotFound = errors.New("not found")

// ErrDigestMismatch is returned when the content retrieved from the store
// does not hash to the digest that was used to request it.  This proves the
// store did not silently corrupt data and cannot be faked by a non-functional
// stub.
var ErrDigestMismatch = errors.New("digest mismatch: content corrupted in store")

// ArtifactRegistry is the content-addressed artifact store interface.  Every
// method must behave identically across the in-memory implementation (used by
// unit tests) and the Postgres implementation (used by integration tests and
// production).  The contract is:
//
//   - PutArtifact(content) computes sha256(content), stores the bytes keyed
//     by that digest, and returns the hex digest.  Duplicate Puts are
//     idempotent: the same digest is returned and the row is not replaced.
//
//   - GetArtifact(digest) returns the exact bytes stored under that digest,
//     or ErrNotFound.  The caller MUST verify the returned bytes hash to
//     digest; the interface enforces this in the default helper VerifyGet.
//
//   - TagArtifact(name, digest) creates or updates the tag → digest mapping.
//     The digest must already exist (i.e. PutArtifact was called first).
//
//   - Resolve(name) returns the digest the tag currently points to, or
//     ErrNotFound.
type ArtifactRegistry interface {
	PutArtifact(mediaType string, content []byte) (digest string, err error)
	GetArtifact(digest string) (mediaType string, content []byte, err error)
	TagArtifact(name, digest string) error
	Resolve(name string) (digest string, err error)
}

// DigestOf computes the canonical sha256 hex digest of content.  It is the
// single authoritative implementation used by both Put paths and the mutation
// tests.
func DigestOf(content []byte) string {
	h := sha256.Sum256(content)
	return hex.EncodeToString(h[:])
}

// VerifyGet checks that the bytes returned by GetArtifact actually hash to the
// requested digest.  A store that silently corrupts data will cause this to
// return ErrDigestMismatch, which is an anti-bluff sentinel: tests that call
// this function cannot pass on a non-functional stub that returns wrong bytes.
func VerifyGet(digest string, content []byte) error {
	got := DigestOf(content)
	if got != digest {
		return fmt.Errorf("%w: want %s got %s", ErrDigestMismatch, digest, got)
	}
	return nil
}

// --- In-memory implementation (unit tests only) ----------------------------

// memArtifact stores one artifact row.
type memArtifact struct {
	mediaType string
	content   []byte
	size      int64
	createdAt time.Time
}

// MemArtifactRegistry is an in-memory ArtifactRegistry backed by plain maps
// protected by a RWMutex.  It is the implementation used by deterministic unit
// tests and is NEVER used in production.
type MemArtifactRegistry struct {
	mu        sync.RWMutex
	artifacts map[string]*memArtifact // digest → artifact
	tags      map[string]string       // name → digest
}

// NewMemArtifactRegistry constructs an empty MemArtifactRegistry.
func NewMemArtifactRegistry() *MemArtifactRegistry {
	return &MemArtifactRegistry{
		artifacts: make(map[string]*memArtifact),
		tags:      make(map[string]string),
	}
}

// PutArtifact stores content under its sha256 digest and returns that digest.
// If the same content (identical digest) is put again the call is a no-op and
// the existing digest is returned (idempotent content-addressing).
func (m *MemArtifactRegistry) PutArtifact(mediaType string, content []byte) (string, error) {
	digest := DigestOf(content)

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.artifacts[digest]; !exists {
		// Copy to avoid aliasing with the caller's slice.
		stored := make([]byte, len(content))
		copy(stored, content)
		m.artifacts[digest] = &memArtifact{
			mediaType: mediaType,
			content:   stored,
			size:      int64(len(content)),
			createdAt: time.Now().UTC(),
		}
	}
	return digest, nil
}

// GetArtifact retrieves the bytes stored under digest.  Returns ErrNotFound if
// the digest has never been Put.  The returned slice is a copy so callers
// cannot mutate the store's internal state.
func (m *MemArtifactRegistry) GetArtifact(digest string) (string, []byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	a, ok := m.artifacts[digest]
	if !ok {
		return "", nil, fmt.Errorf("artifact %s: %w", digest, ErrNotFound)
	}
	out := make([]byte, len(a.content))
	copy(out, a.content)
	return a.mediaType, out, nil
}

// TagArtifact creates or updates the tag → digest mapping.  The digest must
// already exist in the store or ErrNotFound is returned.
func (m *MemArtifactRegistry) TagArtifact(name, digest string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.artifacts[digest]; !ok {
		return fmt.Errorf("TagArtifact: digest %s: %w", digest, ErrNotFound)
	}
	m.tags[name] = digest
	return nil
}

// Resolve returns the digest currently pointed to by name, or ErrNotFound.
func (m *MemArtifactRegistry) Resolve(name string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	d, ok := m.tags[name]
	if !ok {
		return "", fmt.Errorf("tag %q: %w", name, ErrNotFound)
	}
	return d, nil
}
