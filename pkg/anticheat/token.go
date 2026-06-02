// Package anticheat provides a model/revision anti-cheat verification token
// (V2, HMAC). A result completion is cryptographically bound to a model
// REVISION via an HMAC-SHA256 token keyed by a shared secret.
//
// The package is self-contained, deterministic, and uses only the standard
// library's crypto primitives. There is no network, host, cgo, or LLM access.
//
// Security model: GenerateToken computes an HMAC-SHA256 over a canonical,
// unambiguous encoding of (completion || revision) keyed by the secret.
// ValidateToken recomputes the expected HMAC and constant-time-compares it
// against the supplied token using hmac.Equal. A genuine (completion,
// revision, token) triple validates; a forged completion value, a wrong
// revision, or a tampered token is rejected.
package anticheat

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
)

// ErrEmptySecret is returned when the HMAC key is empty. An empty key would
// reduce the token to a public hash with no anti-cheat value.
var ErrEmptySecret = errors.New("anticheat: secret must be non-empty")

// ErrMalformedToken is returned when the supplied token hex is not valid hex
// or has the wrong length for an HMAC-SHA256 digest.
var ErrMalformedToken = errors.New("anticheat: token is not valid hex of expected length")

// digestLen is the byte length of an HMAC-SHA256 output.
const digestLen = sha256.Size

// canonical produces an unambiguous binary encoding of (completion, revision).
//
// A naive concatenation completion+revision is ambiguous: ("ab","c") and
// ("a","bc") would collide. To bind BOTH fields distinctly we length-prefix
// each field with its byte length as an 8-byte big-endian integer. This makes
// the encoding injective over the (completion, revision) pair, so the HMAC is
// genuinely bound to the revision and not just to some concatenation of bytes.
func canonical(completion, revision string) []byte {
	cb := []byte(completion)
	rb := []byte(revision)
	out := make([]byte, 0, 16+len(cb)+len(rb))
	var n [8]byte

	binary.BigEndian.PutUint64(n[:], uint64(len(cb)))
	out = append(out, n[:]...)
	out = append(out, cb...)

	binary.BigEndian.PutUint64(n[:], uint64(len(rb)))
	out = append(out, n[:]...)
	out = append(out, rb...)

	return out
}

// mac computes the raw HMAC-SHA256 digest over the canonical encoding.
func mac(completion, revision string, secret []byte) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write(canonical(completion, revision))
	return h.Sum(nil)
}

// GenerateToken computes the anti-cheat token binding the completion to the
// model revision, returned as a lowercase hex string. The secret MUST be
// non-empty; an empty secret yields an empty token (no panic) and any
// validation against it will fail via ErrEmptySecret.
func GenerateToken(completion string, revision string, secret []byte) string {
	if len(secret) == 0 {
		return ""
	}
	return hex.EncodeToString(mac(completion, revision, secret))
}

// ValidateToken recomputes the expected token for (completion, revision,
// secret) and constant-time-compares it against tokenHex using hmac.Equal.
//
// Returns (true, nil) only for a genuine triple. Returns (false, nil) when the
// token is well-formed but does not match (forged completion, wrong revision,
// or a tampered-but-still-valid-hex token). Returns (false, err) for an empty
// secret or a malformed token hex.
func ValidateToken(completion string, revision string, tokenHex string, secret []byte) (bool, error) {
	if len(secret) == 0 {
		return false, ErrEmptySecret
	}
	supplied, err := hex.DecodeString(tokenHex)
	if err != nil || len(supplied) != digestLen {
		return false, ErrMalformedToken
	}
	expected := mac(completion, revision, secret)
	// hmac.Equal is constant-time; never use == on the digests.
	if hmac.Equal(supplied, expected) {
		return true, nil
	}
	return false, nil
}
