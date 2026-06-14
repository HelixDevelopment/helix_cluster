package jwt

// Adversarial SINK-SIDE probes for pkg/jwt.
//
// Every test here constructs a REAL hostile token and asserts the actual
// accept/reject verdict of the public verification entry points
// (ValidateToken / ParseToken / Validator.ValidateHMAC / Validator.ValidateRSA /
// Validator.ValidateKeySet). A JWT that MUST be rejected but is ACCEPTED is the
// bug — we never settle for "no panic".
//
// Classic JWT vulnerability classes covered:
//   (a) alg confusion / "alg":"none" / HS-RS confusion / empty signature
//   (b) expiry / nbf / iat
//   (c) signature tampering / truncation / constant-time compare
//   (d) claims / empty key / zero-value claims

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
	"strings"
	"testing"
	"time"
)

// b64 is RawURLEncoding shorthand.
func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// craft builds a compact JWT from a header object, claims object, and a raw
// (already-bytes) signature. No signing is performed — caller supplies the sig.
func craft(t *testing.T, header, claims map[string]interface{}, sig []byte) string {
	t.Helper()
	hj, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	cj, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	return b64(hj) + "." + b64(cj) + "." + b64(sig)
}

// signHS256 returns a correct HS256 signature over header.payload for secret.
func signHS256(secret []byte, headerPart, payloadPart string) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(headerPart + "." + payloadPart))
	return mac.Sum(nil)
}

// =====================================================================
// (a) ALG CONFUSION / none / empty signature
// =====================================================================

// alg=none with an empty signature must NEVER be accepted by the HMAC path.
func TestAdversarial_AlgNone_EmptySig_Rejected(t *testing.T) {
	secret := []byte("super-secret")
	tok := craft(t,
		map[string]interface{}{"alg": "none", "typ": "JWT"},
		map[string]interface{}{"sub": "admin", "exp": time.Now().Add(time.Hour).Unix()},
		[]byte{}, // empty signature
	)
	if err := ValidateToken(tok, secret); err == nil {
		t.Fatalf("SINK-SIDE BUG: alg=none with empty signature was ACCEPTED")
	}
}

// alg=none but carrying a "signature" that is just the secret/garbage — still reject.
func TestAdversarial_AlgNone_WithGarbageSig_Rejected(t *testing.T) {
	secret := []byte("super-secret")
	tok := craft(t,
		map[string]interface{}{"alg": "none", "typ": "JWT"},
		map[string]interface{}{"sub": "admin"},
		[]byte("anything"),
	)
	if err := ValidateToken(tok, secret); err == nil {
		t.Fatalf("SINK-SIDE BUG: alg=none with garbage sig was ACCEPTED")
	}
	if _, err := ParseToken(tok, secret); err == nil {
		t.Fatalf("SINK-SIDE BUG: ParseToken accepted alg=none token")
	}
}

// Case variants of "none" (None, NONE) must not slip through an upper/lower bypass.
func TestAdversarial_AlgNone_CaseVariants_Rejected(t *testing.T) {
	secret := []byte("super-secret")
	for _, alg := range []string{"None", "NONE", "nOnE", "  none"} {
		tok := craft(t,
			map[string]interface{}{"alg": alg, "typ": "JWT"},
			map[string]interface{}{"sub": "admin"},
			[]byte{},
		)
		if err := ValidateToken(tok, secret); err == nil {
			t.Fatalf("SINK-SIDE BUG: alg=%q was ACCEPTED", alg)
		}
	}
}

// HS/RS confusion: an attacker takes the RSA *public* key (public knowledge),
// signs an HS256 token using the PEM bytes as the HMAC secret, and presents it.
// The Validator.ValidateRSA path must NOT verify an HS256 token, and the HMAC
// path must not be reachable with the public key as secret in normal operation.
// Here we assert the RSA entry point rejects an HS256-headed token.
func TestAdversarial_HSRSConfusion_RSAPathRejectsHSToken(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	pubPEM := marshalRSAPub(t, &priv.PublicKey)

	// Build an HS256 token whose HMAC secret is the public key PEM bytes.
	hj, _ := json.Marshal(map[string]interface{}{"alg": "HS256", "typ": "JWT"})
	cj, _ := json.Marshal(map[string]interface{}{"sub": "admin"})
	hp := b64(hj)
	pp := b64(cj)
	sig := signHS256(pubPEM, hp, pp)
	tok := hp + "." + pp + "." + b64(sig)

	v := NewValidator()
	if err := v.ValidateRSA(tok, pubPEM); err == nil {
		t.Fatalf("SINK-SIDE BUG: HS/RS confusion — RSA verify ACCEPTED an HS256 token signed with the public key")
	}
}

// =====================================================================
// (b) EXPIRY / NBF / IAT
// =====================================================================

// An expired token (exp in the past) must be rejected by ValidateToken.
func TestAdversarial_ExpiredToken_Rejected(t *testing.T) {
	secret := []byte("secret")
	hj, _ := json.Marshal(map[string]interface{}{"alg": "HS256", "typ": "JWT"})
	cj, _ := json.Marshal(map[string]interface{}{"sub": "u", "exp": time.Now().Add(-time.Hour).Unix()})
	hp := b64(hj)
	pp := b64(cj)
	tok := hp + "." + pp + "." + b64(signHS256(secret, hp, pp))
	if err := ValidateToken(tok, secret); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("SINK-SIDE BUG: expired token not rejected as expired, got %v", err)
	}
}

// A correctly-signed token with NO exp claim: ValidateToken documents
// "signature and expiry". With no exp present it cannot expire — pin current
// behavior (accepted) so a future change that silently flips it is caught.
func TestAdversarial_MissingExp_ContractPin(t *testing.T) {
	secret := []byte("secret")
	hj, _ := json.Marshal(map[string]interface{}{"alg": "HS256", "typ": "JWT"})
	cj, _ := json.Marshal(map[string]interface{}{"sub": "u"}) // no exp
	hp := b64(hj)
	pp := b64(cj)
	tok := hp + "." + pp + "." + b64(signHS256(secret, hp, pp))
	// Document & pin: a valid-signature token with no exp is accepted today.
	if err := ValidateToken(tok, secret); err != nil {
		t.Fatalf("contract pin: signed token with no exp unexpectedly rejected: %v", err)
	}
}

// nbf in the future must be rejected by the Validator path (zero leeway).
func TestAdversarial_NbfFuture_Rejected(t *testing.T) {
	secret := []byte("secret")
	base := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	hj, _ := json.Marshal(map[string]interface{}{"alg": "HS256", "typ": "JWT"})
	cj, _ := json.Marshal(map[string]interface{}{"sub": "u", "nbf": base.Add(time.Hour).Unix()})
	hp := b64(hj)
	pp := b64(cj)
	tok := hp + "." + pp + "." + b64(signHS256(secret, hp, pp))
	v := NewValidator(WithClock(fixedClock{base}))
	if !errors.Is(v.ValidateHMAC(tok, secret), ErrTokenNotYetValid) {
		t.Fatalf("SINK-SIDE BUG: nbf-in-future token accepted by Validator")
	}
}

// exp exactly-now boundary (zero leeway): now == exp must be accepted (not
// strictly after); now == exp+1s must be rejected. Pins the boundary semantics.
func TestAdversarial_ExpExactBoundary(t *testing.T) {
	secret := []byte("secret")
	base := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	v := NewValidator(WithClock(fixedClock{base}))

	hj, _ := json.Marshal(map[string]interface{}{"alg": "HS256", "typ": "JWT"})
	// exp == now: now.After(exp) is false -> accept.
	cjNow, _ := json.Marshal(map[string]interface{}{"sub": "u", "exp": base.Unix()})
	hp := b64(hj)
	ppNow := b64(cjNow)
	tokNow := hp + "." + ppNow + "." + b64(signHS256(secret, hp, ppNow))
	if err := v.ValidateHMAC(tokNow, secret); err != nil {
		t.Fatalf("boundary: exp==now should be accepted, got %v", err)
	}
	// exp == now-1s: expired.
	cjPast, _ := json.Marshal(map[string]interface{}{"sub": "u", "exp": base.Add(-time.Second).Unix()})
	ppPast := b64(cjPast)
	tokPast := hp + "." + ppPast + "." + b64(signHS256(secret, hp, ppPast))
	if !errors.Is(v.ValidateHMAC(tokPast, secret), ErrTokenExpired) {
		t.Fatalf("boundary: exp==now-1s should be expired")
	}
}

// =====================================================================
// (c) SIGNATURE TAMPERING / truncation
// =====================================================================

// Payload tampered after signing (privilege escalation) — signature no longer
// matches and MUST be rejected.
func TestAdversarial_TamperedPayload_Rejected(t *testing.T) {
	secret := []byte("secret")
	hj, _ := json.Marshal(map[string]interface{}{"alg": "HS256", "typ": "JWT"})
	cjOrig, _ := json.Marshal(map[string]interface{}{"sub": "user", "role": "user", "exp": time.Now().Add(time.Hour).Unix()})
	hp := b64(hj)
	ppOrig := b64(cjOrig)
	sig := signHS256(secret, hp, ppOrig) // sign the ORIGINAL payload

	// Now swap in an escalated payload but keep the old signature.
	cjEvil, _ := json.Marshal(map[string]interface{}{"sub": "user", "role": "admin", "exp": time.Now().Add(time.Hour).Unix()})
	ppEvil := b64(cjEvil)
	tok := hp + "." + ppEvil + "." + b64(sig)

	if err := ValidateToken(tok, secret); err == nil {
		t.Fatalf("SINK-SIDE BUG: tampered payload (role user->admin) ACCEPTED with stale signature")
	}
}

// Truncated signature must be rejected (no prefix/early-exit accept).
func TestAdversarial_TruncatedSignature_Rejected(t *testing.T) {
	secret := []byte("secret")
	hj, _ := json.Marshal(map[string]interface{}{"alg": "HS256", "typ": "JWT"})
	cj, _ := json.Marshal(map[string]interface{}{"sub": "u", "exp": time.Now().Add(time.Hour).Unix()})
	hp := b64(hj)
	pp := b64(cj)
	full := signHS256(secret, hp, pp)

	// Single-byte signature (a prefix of the real one) must not pass.
	tok := hp + "." + pp + "." + b64(full[:1])
	if err := ValidateToken(tok, secret); err == nil {
		t.Fatalf("SINK-SIDE BUG: 1-byte truncated signature ACCEPTED (prefix/early-exit compare)")
	}
	// Empty signature must not pass.
	tokEmpty := hp + "." + pp + "." + b64([]byte{})
	if err := ValidateToken(tokEmpty, secret); err == nil {
		t.Fatalf("SINK-SIDE BUG: empty signature ACCEPTED")
	}
}

// Wrong secret (forged by attacker who guessed/used a different key) — reject.
func TestAdversarial_WrongSecret_Rejected(t *testing.T) {
	hj, _ := json.Marshal(map[string]interface{}{"alg": "HS256", "typ": "JWT"})
	cj, _ := json.Marshal(map[string]interface{}{"sub": "u", "exp": time.Now().Add(time.Hour).Unix()})
	hp := b64(hj)
	pp := b64(cj)
	tok := hp + "." + pp + "." + b64(signHS256([]byte("attacker-secret"), hp, pp))
	if err := ValidateToken(tok, []byte("real-secret")); !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("SINK-SIDE BUG: token signed with wrong secret accepted, got %v", err)
	}
}

// =====================================================================
// (d) CLAIMS / RSA interop correctness
// =====================================================================

// RFC-7518 interop: a standards-compliant RS256 signature (which includes the
// SHA-256 DigestInfo ASN.1 prefix, i.e. signed with crypto.SHA256) over a valid
// token MUST verify. This is the SINK-SIDE acceptance test for a *legitimate*
// token produced by any spec-conformant issuer (Go stdlib, OpenSSL, every IdP).
//
// If this FAILS while the package's own self-signed (crypto.Hash(0)) tokens
// pass, the RSA verify path is using the wrong crypto.Hash constant and is
// NOT interoperable — it rejects every real-world RS256 token (fail-closed
// interop defect). Marked clearly; reported to the coordinator.
func TestAdversarial_RS256_RFC7518Compliant_MustVerify(t *testing.T) {
	// REGRESSION GUARD — HXC-1825 (FIXED): verifyRSA now passes crypto.SHA256/384/
	// 512 to rsa.VerifyPKCS1v15, so a standards-compliant RFC-7518 RS256 token
	// from any conformant issuer verifies. Runs by default; if verifyRSA reverts
	// to crypto.Hash(0)/1/2 this FAILS.
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	pubPEM := marshalRSAPub(t, &priv.PublicKey)

	hj, _ := json.Marshal(map[string]interface{}{"alg": "RS256", "typ": "JWT"})
	cj, _ := json.Marshal(map[string]interface{}{"sub": "u", "exp": time.Now().Add(time.Hour).Unix()})
	hp := b64(hj)
	pp := b64(cj)

	// Spec-compliant signature: SignPKCS1v15 with crypto.SHA256 (prepends the
	// SHA-256 DigestInfo prefix, exactly what RFC 7518 RS256 mandates).
	digest := sha256.Sum256([]byte(hp + "." + pp))
	sigBytes, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	tok := hp + "." + pp + "." + b64(sigBytes)

	v := NewValidator()
	err = v.ValidateRSA(tok, pubPEM)
	if err != nil {
		t.Fatalf("INTEROP DEFECT (fail-closed, LATENT — reported): a standards-compliant "+
			"RFC-7518 RS256 token was REJECTED by ValidateRSA: %v — the verify path passes "+
			"crypto.Hash(0) to rsa.VerifyPKCS1v15 instead of crypto.SHA256, so it only accepts "+
			"its own non-conformant self-signed tokens", err)
	}
}

// Empty/nil HMAC secret: ValidateToken must not "succeed" for an attacker who
// forges with an empty secret when the verifier also passes an empty secret.
// This pins that an empty-secret verify still does a real MAC comparison (it
// is a caller-misuse footgun, not a free-pass). Asserted SINK-SIDE: a token
// forged with secret X must NOT verify under empty secret.
func TestAdversarial_EmptySecret_DoesNotFreePass(t *testing.T) {
	hj, _ := json.Marshal(map[string]interface{}{"alg": "HS256", "typ": "JWT"})
	cj, _ := json.Marshal(map[string]interface{}{"sub": "u", "exp": time.Now().Add(time.Hour).Unix()})
	hp := b64(hj)
	pp := b64(cj)
	// Forged with a real secret, verified with empty secret -> must fail.
	tok := hp + "." + pp + "." + b64(signHS256([]byte("real"), hp, pp))
	if err := ValidateToken(tok, []byte{}); err == nil {
		t.Fatalf("SINK-SIDE BUG: token forged with non-empty secret verified under EMPTY secret")
	}
}

// Zero-value / empty claims with a VALID signature: an empty claims object is
// a structurally valid token. Pin that it is accepted (no exp to fail) so a
// regression that rejects/accepts inconsistently is surfaced.
func TestAdversarial_EmptyClaims_ValidSig_Accepted(t *testing.T) {
	secret := []byte("secret")
	hj, _ := json.Marshal(map[string]interface{}{"alg": "HS256", "typ": "JWT"})
	cj, _ := json.Marshal(map[string]interface{}{}) // zero-value claims
	hp := b64(hj)
	pp := b64(cj)
	tok := hp + "." + pp + "." + b64(signHS256(secret, hp, pp))
	if _, err := ParseToken(tok, secret); err != nil {
		t.Fatalf("contract pin: empty-claims token with valid sig rejected: %v", err)
	}
}

// --- helpers ---

func marshalRSAPub(t *testing.T, pub *rsa.PublicKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("marshal pub: %v", err)
	}
	pemStr := "-----BEGIN PUBLIC KEY-----\n" +
		chunk64(base64.StdEncoding.EncodeToString(der)) +
		"-----END PUBLIC KEY-----\n"
	return []byte(pemStr)
}

func chunk64(s string) string {
	var b strings.Builder
	for len(s) > 64 {
		b.WriteString(s[:64])
		b.WriteByte('\n')
		s = s[64:]
	}
	b.WriteString(s)
	b.WriteByte('\n')
	return b.String()
}
