// Package anonymize provides deterministic, keyed-hash anonymization of text.
//
// Model: input text is deterministically split into blocks. Each block is
// replaced by an HMAC-SHA256 digest keyed by a per-TENANT secret. Because HMAC
// is deterministic, the SAME block under the SAME tenant key always yields the
// IDENTICAL hash — enabling dedup and correlation WITHIN a tenant. Because the
// tenant key is mixed into the MAC, the SAME block under a DIFFERENT tenant key
// yields a DIFFERENT hash — giving cross-tenant unlinkability. The raw text is
// never present in the anonymized output (only fixed-width hex digests are).
//
// The package is pure Go and uses only the standard library
// (crypto/hmac, crypto/sha256). It performs no I/O and is deterministic.
package anonymize

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Anonymizer turns text into per-block keyed-hash tokens.
//
// A non-empty domain is mixed into every MAC as a scheme/namespace separator so
// that digests produced by one Anonymizer configuration cannot collide with
// those of another configuration using the same tenant key and block text.
type Anonymizer struct {
	domain string
}

// New returns an Anonymizer for the given scheme domain (namespace). The domain
// is a configuration label (e.g. "prompt-v1"); it is NOT a secret and is the
// same across tenants. Cross-tenant separation comes from the per-tenant key
// supplied to Anonymize, not from the domain.
func New(domain string) *Anonymizer {
	return &Anonymizer{domain: domain}
}

// SplitBlocks deterministically splits text into blocks.
//
// Splitting is on runs of whitespace (so it does not depend on the exact
// spacing in the input), and empty blocks are dropped. This is exported so
// tests and callers can reason about block boundaries independently of hashing.
func SplitBlocks(text string) []string {
	fields := strings.Fields(text)
	// strings.Fields already drops empty tokens and collapses whitespace.
	// Return a fresh slice so callers cannot mutate internal state.
	out := make([]string, len(fields))
	copy(out, fields)
	return out
}

// Anonymize splits text into blocks and returns one lowercase-hex HMAC-SHA256
// digest per block, keyed by tenantKey. The returned slice is positionally
// aligned with SplitBlocks(text).
//
// Determinism: identical (tenantKey, domain, block) inputs always produce the
// identical digest. Unlinkability: changing tenantKey changes every digest.
func (a *Anonymizer) Anonymize(tenantKey []byte, text string) []string {
	blocks := SplitBlocks(text)
	out := make([]string, len(blocks))
	for i, block := range blocks {
		out[i] = a.hashBlock(tenantKey, block)
	}
	return out
}

// hashBlock computes HMAC-SHA256 over a length-framed (domain, block) message,
// keyed by the per-tenant secret. Length framing prevents boundary-ambiguity
// collisions between distinct (domain, block) pairs that share a concatenation.
func (a *Anonymizer) hashBlock(tenantKey []byte, block string) string {
	mac := hmac.New(sha256.New, tenantKey)
	writeFramed(mac, a.domain)
	writeFramed(mac, block)
	return hex.EncodeToString(mac.Sum(nil))
}

// writeFramed writes an 8-byte big-endian length prefix followed by the bytes
// of s, so that the concatenation of framed fields is unambiguous.
func writeFramed(w interface{ Write([]byte) (int, error) }, s string) {
	var l [8]byte
	n := uint64(len(s))
	// Manual big-endian PutUint64: each byte() takes the intended low 8 bits.
	l[0] = byte((n >> 56) & 0xff)
	l[1] = byte((n >> 48) & 0xff)
	l[2] = byte((n >> 40) & 0xff)
	l[3] = byte((n >> 32) & 0xff)
	l[4] = byte((n >> 24) & 0xff)
	l[5] = byte((n >> 16) & 0xff)
	l[6] = byte((n >> 8) & 0xff)
	l[7] = byte(n & 0xff)
	// hash.Hash.Write never returns an error.
	_, _ = w.Write(l[:])
	_, _ = w.Write([]byte(s))
}
