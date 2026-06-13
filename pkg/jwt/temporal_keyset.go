package jwt

import (
	"crypto/rsa"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Additional sentinel errors for temporal validation and key-set selection.
var (
	// ErrTokenNotYetValid indicates the token's nbf claim is in the future
	// (beyond the configured leeway).
	ErrTokenNotYetValid = errors.New("token not yet valid (nbf)")
	// ErrTokenIssuedInFuture indicates the token's iat claim is in the future
	// (beyond the configured leeway).
	ErrTokenIssuedInFuture = errors.New("token issued in the future (iat)")
	// ErrUnknownKid indicates the token header's kid did not match any key
	// registered in the KeySet (or no kid was present).
	ErrUnknownKid = errors.New("unknown or missing key id (kid)")
)

// Clock is an injectable time source so temporal validation is deterministic
// in tests. Production code uses SystemClock (the default).
type Clock interface {
	Now() time.Time
}

// SystemClock is the default Clock backed by the wall clock (UTC).
type SystemClock struct{}

// Now returns the current UTC time.
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// Validator performs clock-skew-tolerant temporal validation of JWT claims
// (exp, nbf, iat) using an injectable Clock and a configurable leeway.
type Validator struct {
	clock  Clock
	leeway time.Duration
}

// Option configures a Validator.
type Option func(*Validator)

// WithClock injects a deterministic Clock (e.g. for tests). A nil clock is
// ignored and the default SystemClock is retained.
func WithClock(c Clock) Option {
	return func(v *Validator) {
		if c != nil {
			v.clock = c
		}
	}
}

// WithLeeway sets the clock-skew tolerance applied to exp, nbf, and iat. A
// negative leeway is clamped to zero. Default leeway is zero.
func WithLeeway(d time.Duration) Option {
	return func(v *Validator) {
		if d < 0 {
			d = 0
		}
		v.leeway = d
	}
}

// NewValidator builds a Validator with the given options. Defaults: SystemClock
// and zero leeway.
func NewValidator(opts ...Option) *Validator {
	v := &Validator{clock: SystemClock{}, leeway: 0}
	for _, opt := range opts {
		opt(v)
	}
	return v
}

// ValidateHMAC parses the token, verifies its HMAC signature with secret, then
// applies clock-skew-tolerant temporal validation.
func (v *Validator) ValidateHMAC(token string, secret []byte) error {
	tok, err := Parse(token)
	if err != nil {
		return err
	}
	if err := tok.VerifyHMAC(secret); err != nil {
		return err
	}
	return v.ValidateClaims(tok)
}

// ValidateRSA parses the token, verifies its RSA signature with the PEM-encoded
// public key, then applies clock-skew-tolerant temporal validation.
func (v *Validator) ValidateRSA(token string, publicKeyPEM []byte) error {
	tok, err := Parse(token)
	if err != nil {
		return err
	}
	if err := tok.VerifyRSA(publicKeyPEM); err != nil {
		return err
	}
	return v.ValidateClaims(tok)
}

// ValidateClaims applies clock-skew-tolerant temporal validation to an
// already-parsed (and signature-verified) token. It checks exp, nbf and iat
// against the injected clock with the configured leeway.
func (v *Validator) ValidateClaims(tok *Token) error {
	now := v.clock.Now()

	// exp: reject if now is after exp+leeway.
	if exp, ok, err := claimTime(tok.Payload, "exp"); err != nil {
		return err
	} else if ok {
		if now.After(exp.Add(v.leeway)) {
			return ErrTokenExpired
		}
	}

	// nbf: reject if now is before nbf-leeway (not yet valid).
	if nbf, ok, err := claimTime(tok.Payload, "nbf"); err != nil {
		return err
	} else if ok {
		if now.Before(nbf.Add(-v.leeway)) {
			return ErrTokenNotYetValid
		}
	}

	// iat: reject if the token was issued in the future beyond leeway.
	if iat, ok, err := claimTime(tok.Payload, "iat"); err != nil {
		return err
	} else if ok {
		if now.Before(iat.Add(-v.leeway)) {
			return ErrTokenIssuedInFuture
		}
	}

	return nil
}

// claimTime extracts a numeric (NumericDate) claim as a UTC time.Time. The
// second return value reports whether the claim was present.
func claimTime(payload map[string]interface{}, name string) (time.Time, bool, error) {
	raw, ok := payload[name]
	if !ok {
		return time.Time{}, false, nil
	}
	sec, err := toUnixSeconds(raw)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("invalid %s claim: %w", name, err)
	}
	return time.Unix(sec, 0).UTC(), true, nil
}

// toUnixSeconds normalizes a JSON-decoded numeric claim to int64 seconds.
func toUnixSeconds(raw interface{}) (int64, error) {
	switch v := raw.(type) {
	case float64:
		return int64(v), nil
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	default:
		return 0, fmt.Errorf("unexpected claim type %T", raw)
	}
}

// keyKind distinguishes the verification key material stored in a KeySet.
type keyKind int

const (
	keyHMAC keyKind = iota
	keyRSA
)

type keyEntry struct {
	kind   keyKind
	secret []byte
	rsaPub *rsa.PublicKey
}

// KeySet is a registry of verification keys addressed by their `kid`
// (key id). It supports HMAC secrets and RSA public keys, enabling key
// rotation and multi-issuer verification where the token header selects the
// key via its `kid`.
//
// KeySet is safe for concurrent use: tokens are verified (map read via the
// lookup method) concurrently with runtime key rotation (map write via AddHMAC /
// AddRSAPublicKey). Reads take a shared lock, writes an exclusive one, so a
// verify never races a rotation. Without this an unsynchronized map read during
// a write is a fatal "concurrent map read and map write" crash on the auth path.
type KeySet struct {
	mu   sync.RWMutex
	keys map[string]keyEntry
}

// NewKeySet creates an empty KeySet.
func NewKeySet() *KeySet {
	return &KeySet{keys: make(map[string]keyEntry)}
}

// AddHMAC registers an HMAC verification secret under the given kid. An empty
// kid is rejected. Re-registering a kid overwrites the prior entry.
func (ks *KeySet) AddHMAC(kid string, secret []byte) error {
	if kid == "" {
		return fmt.Errorf("%w: empty kid", ErrInvalidKey)
	}
	if len(secret) == 0 {
		return fmt.Errorf("%w: empty HMAC secret", ErrInvalidKey)
	}
	cp := make([]byte, len(secret))
	copy(cp, secret)
	ks.mu.Lock()
	ks.keys[kid] = keyEntry{kind: keyHMAC, secret: cp}
	ks.mu.Unlock()
	return nil
}

// AddRSAPublicKey registers an RSA public key under the given kid. An empty kid
// or nil key is rejected.
func (ks *KeySet) AddRSAPublicKey(kid string, pub *rsa.PublicKey) error {
	if kid == "" {
		return fmt.Errorf("%w: empty kid", ErrInvalidKey)
	}
	if pub == nil {
		return fmt.Errorf("%w: nil RSA public key", ErrInvalidKey)
	}
	ks.mu.Lock()
	ks.keys[kid] = keyEntry{kind: keyRSA, rsaPub: pub}
	ks.mu.Unlock()
	return nil
}

// lookup returns the key entry registered under kid, taking a shared read lock
// so concurrent verifications never race a rotation write.
func (ks *KeySet) lookup(kid string) (keyEntry, bool) {
	ks.mu.RLock()
	entry, ok := ks.keys[kid]
	ks.mu.RUnlock()
	return entry, ok
}

// ValidateKeySet parses the token, selects the verification key whose kid
// matches the token header, verifies the signature with that key, and then
// applies clock-skew-tolerant temporal validation. An unknown or missing kid
// is rejected with ErrUnknownKid before any signature check.
func (v *Validator) ValidateKeySet(token string, ks *KeySet) error {
	tok, err := Parse(token)
	if err != nil {
		return err
	}
	kid := tok.Header.Kid
	if kid == "" {
		return fmt.Errorf("%w: header has no kid", ErrUnknownKid)
	}
	entry, ok := ks.lookup(kid)
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownKid, kid)
	}

	switch entry.kind {
	case keyHMAC:
		if err := tok.VerifyHMAC(entry.secret); err != nil {
			return err
		}
	case keyRSA:
		if err := tok.verifyRSAWithKey(entry.rsaPub); err != nil {
			return err
		}
	default:
		return ErrInvalidKey
	}

	return v.ValidateClaims(tok)
}
