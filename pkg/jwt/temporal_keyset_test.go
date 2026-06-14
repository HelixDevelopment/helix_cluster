package jwt

import (
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// fixedClock is a deterministic Clock for tests (no real time).
type fixedClock struct{ t time.Time }

func (c fixedClock) Now() time.Time { return c.t }

// mkHMACToken builds a signed HS256 token with the given header object and claims.
func mkHMACToken(t *testing.T, secret []byte, headerObj map[string]interface{}, claims map[string]interface{}) string {
	t.Helper()
	hj, err := json.Marshal(headerObj)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	cj, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	h := base64.RawURLEncoding.EncodeToString(hj)
	p := base64.RawURLEncoding.EncodeToString(cj)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(h + "." + p))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return h + "." + p + "." + sig
}

// --- Temporal validation with injectable clock + leeway ---

func TestValidateTemporal_ExpiredWithinLeewayAccepted(t *testing.T) {
	secret := []byte("secret")
	base := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	// exp 20s in the past relative to clock, leeway 30s -> accepted.
	tok := mkHMACToken(t, secret,
		map[string]interface{}{"alg": "HS256", "typ": "JWT"},
		map[string]interface{}{"sub": "u", "exp": base.Add(-20 * time.Second).Unix()},
	)
	v := NewValidator(WithClock(fixedClock{base}), WithLeeway(30*time.Second))
	if err := v.ValidateHMAC(tok, secret); err != nil {
		t.Fatalf("expected acceptance within leeway, got %v", err)
	}
}

func TestValidateTemporal_ExpiredBeyondLeewayRejected(t *testing.T) {
	secret := []byte("secret")
	base := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	// exp 40s in the past, leeway 30s -> rejected.
	tok := mkHMACToken(t, secret,
		map[string]interface{}{"alg": "HS256", "typ": "JWT"},
		map[string]interface{}{"sub": "u", "exp": base.Add(-40 * time.Second).Unix()},
	)
	v := NewValidator(WithClock(fixedClock{base}), WithLeeway(30*time.Second))
	if err := v.ValidateHMAC(tok, secret); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("expected ErrTokenExpired beyond leeway, got %v", err)
	}
}

// Mutation: boundary must be strictly enforced. exp exactly leeway+1s in the
// past must be rejected; if leeway were applied as ">=" with off-by-one slack
// this would wrongly pass.
func TestValidateTemporal_ExpiredLeewayBoundaryEnforced(t *testing.T) {
	secret := []byte("secret")
	base := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	leeway := 30 * time.Second
	// Just past the boundary: 31s old with 30s leeway -> must reject.
	tokReject := mkHMACToken(t, secret,
		map[string]interface{}{"alg": "HS256", "typ": "JWT"},
		map[string]interface{}{"sub": "u", "exp": base.Add(-(leeway + time.Second)).Unix()},
	)
	v := NewValidator(WithClock(fixedClock{base}), WithLeeway(leeway))
	if err := v.ValidateHMAC(tokReject, secret); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("boundary: expected reject at leeway+1s, got %v", err)
	}
	// Within boundary: 29s old with 30s leeway -> must accept. Proves the
	// rejection above is due to the boundary, not a blanket reject.
	tokAccept := mkHMACToken(t, secret,
		map[string]interface{}{"alg": "HS256", "typ": "JWT"},
		map[string]interface{}{"sub": "u", "exp": base.Add(-(leeway - time.Second)).Unix()},
	)
	if err := v.ValidateHMAC(tokAccept, secret); err != nil {
		t.Fatalf("boundary: expected accept at leeway-1s, got %v", err)
	}
}

func TestValidateTemporal_NbfInFutureRejected(t *testing.T) {
	secret := []byte("secret")
	base := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	// nbf 60s in the future, leeway 5s -> not yet valid.
	tok := mkHMACToken(t, secret,
		map[string]interface{}{"alg": "HS256", "typ": "JWT"},
		map[string]interface{}{"sub": "u", "nbf": base.Add(60 * time.Second).Unix(), "exp": base.Add(time.Hour).Unix()},
	)
	v := NewValidator(WithClock(fixedClock{base}), WithLeeway(5*time.Second))
	if err := v.ValidateHMAC(tok, secret); !errors.Is(err, ErrTokenNotYetValid) {
		t.Fatalf("expected ErrTokenNotYetValid, got %v", err)
	}
}

func TestValidateTemporal_NbfWithinLeewayAccepted(t *testing.T) {
	secret := []byte("secret")
	base := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	// nbf 3s in the future, leeway 10s -> accepted.
	tok := mkHMACToken(t, secret,
		map[string]interface{}{"alg": "HS256", "typ": "JWT"},
		map[string]interface{}{"sub": "u", "nbf": base.Add(3 * time.Second).Unix(), "exp": base.Add(time.Hour).Unix()},
	)
	v := NewValidator(WithClock(fixedClock{base}), WithLeeway(10*time.Second))
	if err := v.ValidateHMAC(tok, secret); err != nil {
		t.Fatalf("expected nbf within leeway accepted, got %v", err)
	}
}

// Mutation: nbf boundary. nbf at leeway+1s in future must reject; nbf at
// leeway-1s must accept. Guards against the future-check being dropped.
func TestValidateTemporal_NbfBoundaryEnforced(t *testing.T) {
	secret := []byte("secret")
	base := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	leeway := 10 * time.Second
	v := NewValidator(WithClock(fixedClock{base}), WithLeeway(leeway))

	reject := mkHMACToken(t, secret,
		map[string]interface{}{"alg": "HS256", "typ": "JWT"},
		map[string]interface{}{"sub": "u", "nbf": base.Add(leeway + time.Second).Unix(), "exp": base.Add(time.Hour).Unix()},
	)
	if err := v.ValidateHMAC(reject, secret); !errors.Is(err, ErrTokenNotYetValid) {
		t.Fatalf("nbf boundary: expected reject at leeway+1s, got %v", err)
	}
	accept := mkHMACToken(t, secret,
		map[string]interface{}{"alg": "HS256", "typ": "JWT"},
		map[string]interface{}{"sub": "u", "nbf": base.Add(leeway - time.Second).Unix(), "exp": base.Add(time.Hour).Unix()},
	)
	if err := v.ValidateHMAC(accept, secret); err != nil {
		t.Fatalf("nbf boundary: expected accept at leeway-1s, got %v", err)
	}
}

func TestValidateTemporal_IatInFutureRejected(t *testing.T) {
	secret := []byte("secret")
	base := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	// iat 120s in the future, leeway 5s -> issued in the future, reject.
	tok := mkHMACToken(t, secret,
		map[string]interface{}{"alg": "HS256", "typ": "JWT"},
		map[string]interface{}{"sub": "u", "iat": base.Add(120 * time.Second).Unix(), "exp": base.Add(time.Hour).Unix()},
	)
	v := NewValidator(WithClock(fixedClock{base}), WithLeeway(5*time.Second))
	if err := v.ValidateHMAC(tok, secret); !errors.Is(err, ErrTokenIssuedInFuture) {
		t.Fatalf("expected ErrTokenIssuedInFuture, got %v", err)
	}
}

func TestValidateTemporal_DeterministicNoRealTime(t *testing.T) {
	// A token that is long expired in wall-clock terms must still be accepted
	// when the injected clock is set before its exp -- proving the validator
	// uses the injected clock, not time.Now().
	secret := []byte("secret")
	clockTime := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	tok := mkHMACToken(t, secret,
		map[string]interface{}{"alg": "HS256", "typ": "JWT"},
		map[string]interface{}{"sub": "u", "exp": clockTime.Add(time.Hour).Unix()},
	)
	v := NewValidator(WithClock(fixedClock{clockTime}))
	if err := v.ValidateHMAC(tok, secret); err != nil {
		t.Fatalf("expected acceptance with injected past clock, got %v", err)
	}
}

// --- Key set / kid selection ---

func TestKeySet_CorrectKidSelected(t *testing.T) {
	base := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	secretA := []byte("key-A-secret")
	secretB := []byte("key-B-secret")

	ks := NewKeySet()
	ks.AddHMAC("kid-A", secretA)
	ks.AddHMAC("kid-B", secretB)

	// Token signed with B's secret and kid-B must verify against the set.
	tok := mkHMACToken(t, secretB,
		map[string]interface{}{"alg": "HS256", "typ": "JWT", "kid": "kid-B"},
		map[string]interface{}{"sub": "u", "exp": base.Add(time.Hour).Unix()},
	)
	v := NewValidator(WithClock(fixedClock{base}))
	if err := v.ValidateKeySet(tok, ks); err != nil {
		t.Fatalf("expected kid-B token to verify, got %v", err)
	}
}

func TestKeySet_UnknownKidRejected(t *testing.T) {
	base := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	ks := NewKeySet()
	ks.AddHMAC("kid-A", []byte("key-A-secret"))

	tok := mkHMACToken(t, []byte("key-A-secret"),
		map[string]interface{}{"alg": "HS256", "typ": "JWT", "kid": "kid-ZZZ"},
		map[string]interface{}{"sub": "u", "exp": base.Add(time.Hour).Unix()},
	)
	v := NewValidator(WithClock(fixedClock{base}))
	if err := v.ValidateKeySet(tok, ks); !errors.Is(err, ErrUnknownKid) {
		t.Fatalf("expected ErrUnknownKid, got %v", err)
	}
}

func TestKeySet_MissingKidRejected(t *testing.T) {
	base := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	ks := NewKeySet()
	ks.AddHMAC("kid-A", []byte("key-A-secret"))

	// No kid in header.
	tok := mkHMACToken(t, []byte("key-A-secret"),
		map[string]interface{}{"alg": "HS256", "typ": "JWT"},
		map[string]interface{}{"sub": "u", "exp": base.Add(time.Hour).Unix()},
	)
	v := NewValidator(WithClock(fixedClock{base}))
	if !errors.Is(v.ValidateKeySet(tok, ks), ErrUnknownKid) {
		t.Fatal("expected ErrUnknownKid for missing kid")
	}
}

// Mutation: a token carrying kid-A but signed with the WRONG secret must be
// rejected -- proving kid selection does not silently accept, and that the
// selected key is actually used to verify the signature.
func TestKeySet_KidMatchButWrongSignatureRejected(t *testing.T) {
	base := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	ks := NewKeySet()
	ks.AddHMAC("kid-A", []byte("real-key-A-secret"))

	// Signed with an attacker secret but claims kid-A.
	tok := mkHMACToken(t, []byte("attacker-secret"),
		map[string]interface{}{"alg": "HS256", "typ": "JWT", "kid": "kid-A"},
		map[string]interface{}{"sub": "u", "exp": base.Add(time.Hour).Unix()},
	)
	v := NewValidator(WithClock(fixedClock{base}))
	if !errors.Is(v.ValidateKeySet(tok, ks), ErrVerifyFailed) {
		t.Fatal("expected ErrVerifyFailed when kid matches but signature is forged")
	}
}

// Mutation: prove kid selection picks the RIGHT key, not just any registered
// key. Token signed with A's secret but tagged kid-B must fail signature.
func TestKeySet_WrongKidSelectionRejected(t *testing.T) {
	base := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	ks := NewKeySet()
	ks.AddHMAC("kid-A", []byte("key-A-secret"))
	ks.AddHMAC("kid-B", []byte("key-B-secret"))

	// Signed with A's secret, but header says kid-B -> B's key won't verify.
	tok := mkHMACToken(t, []byte("key-A-secret"),
		map[string]interface{}{"alg": "HS256", "typ": "JWT", "kid": "kid-B"},
		map[string]interface{}{"sub": "u", "exp": base.Add(time.Hour).Unix()},
	)
	v := NewValidator(WithClock(fixedClock{base}))
	if !errors.Is(v.ValidateKeySet(tok, ks), ErrVerifyFailed) {
		t.Fatal("expected ErrVerifyFailed: kid-B key must not verify an A-signed token")
	}
}

func TestKeySet_RSAKidSelected(t *testing.T) {
	base := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	pubASN1, err := x509.MarshalPKIXPublicKey(&privKey.PublicKey)
	if err != nil {
		t.Fatalf("marshal pub: %v", err)
	}

	ks := NewKeySet()
	ks.AddHMAC("kid-hmac", []byte("hmac-secret"))
	if err := ks.AddRSAPublicKey("kid-rsa", &privKey.PublicKey); err != nil {
		t.Fatalf("add rsa: %v", err)
	}
	_ = pubASN1

	// Build an RS256 token with kid-rsa.
	hj, _ := json.Marshal(map[string]interface{}{"alg": "RS256", "typ": "JWT", "kid": "kid-rsa"})
	cj, _ := json.Marshal(map[string]interface{}{"sub": "u", "exp": base.Add(time.Hour).Unix()})
	h := base64.RawURLEncoding.EncodeToString(hj)
	p := base64.RawURLEncoding.EncodeToString(cj)
	digest := sha256.Sum256([]byte(h + "." + p))
	sigBytes, err := rsa.SignPKCS1v15(rand.Reader, privKey, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	tok := h + "." + p + "." + base64.RawURLEncoding.EncodeToString(sigBytes)

	v := NewValidator(WithClock(fixedClock{base}))
	if err := v.ValidateKeySet(tok, ks); err != nil {
		t.Fatalf("expected RSA kid token to verify, got %v", err)
	}
}

func TestKeySet_TemporalAppliedAfterKidSelection(t *testing.T) {
	// kid selected correctly, signature valid, but token expired beyond
	// leeway -> must still be rejected as expired.
	base := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	secret := []byte("key-A-secret")
	ks := NewKeySet()
	ks.AddHMAC("kid-A", secret)

	tok := mkHMACToken(t, secret,
		map[string]interface{}{"alg": "HS256", "typ": "JWT", "kid": "kid-A"},
		map[string]interface{}{"sub": "u", "exp": base.Add(-time.Hour).Unix()},
	)
	v := NewValidator(WithClock(fixedClock{base}), WithLeeway(30*time.Second))
	if !errors.Is(v.ValidateKeySet(tok, ks), ErrTokenExpired) {
		t.Fatal("expected ErrTokenExpired after successful kid selection")
	}
}
