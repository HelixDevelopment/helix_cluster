package jwt

// SECOND-PASS adversarial SINK-SIDE probes for pkg/jwt.
//
// The first pass (jwt_adversarial_test.go) covered alg=none / HS-RS confusion on
// the ValidateRSA path / tampered payload / truncated sig / expiry / nbf / the
// RFC-7518 RS256 interop regression (HXC-1825). This pass goes DEEPER and targets
// gaps the first pass and the unit suite leave open. Each probe asserts the real
// accept/reject verdict of a public entry point against a hostile or degenerate
// token. A token that MUST be rejected but is ACCEPTED is the bug — "no panic" is
// never sufficient on its own (but DoS-robustness is asserted where relevant).
//
// Classification of what this file pins (no fail-open bug was found; the package
// is fail-closed on every probe below):
//   - SECURITY PIN: a reject that must never silently flip to accept.
//   - CONTRACT PIN: a documented scope boundary (e.g. the simple ValidateToken
//     path enforces "signature and expiry" ONLY — not nbf/iat) plus proof the
//     secure Validator path DOES enforce the broader checks.
//   - DOS PIN: a degenerate/hostile token must not panic.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// adv2B64 is RawURLEncoding shorthand (named to avoid colliding with b64/sb64
// helpers in sibling test files within the same package).
func adv2B64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// adv2SignHS256 returns a correct HS256 signature (base64) over header.payload.
func adv2SignHS256(secret []byte, hp, pp string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(hp + "." + pp))
	return adv2B64(mac.Sum(nil))
}

// adv2Token builds a signed HS256 token from header + claims objects.
func adv2Token(t *testing.T, secret []byte, hdr, claims map[string]interface{}) string {
	t.Helper()
	hj, err := json.Marshal(hdr)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	cj, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	hp := adv2B64(hj)
	pp := adv2B64(cj)
	return hp + "." + pp + "." + adv2SignHS256(secret, hp, pp)
}

// =====================================================================
// (A) exp TYPE CONFUSION — must FAIL CLOSED on BOTH validation paths.
// A non-numeric exp must NOT be silently treated as "no exp" (which would make
// an attacker-supplied {"exp":"never"} a never-expiring token). The contract is:
// a present-but-malformed exp is an error, never a free pass.
// =====================================================================

func TestAdversarial2_ExpTypeConfusion_FailsClosed(t *testing.T) {
	secret := []byte("secret")
	base := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	v := NewValidator(WithClock(fixedClock{base}))

	// Each of these exp values is non-numeric; a token bearing it must be
	// REJECTED (error), never accepted as if exp were absent.
	cases := []struct {
		name string
		exp  interface{}
	}{
		{"string", "9999999999"},
		{"bool", true},
		{"null", nil},
		{"object", map[string]interface{}{"v": 1}},
		{"array", []interface{}{1, 2}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tok := adv2Token(t, secret,
				map[string]interface{}{"alg": "HS256", "typ": "JWT"},
				map[string]interface{}{"sub": "admin", "exp": c.exp},
			)
			// Simple path.
			if err := ValidateToken(tok, secret); err == nil {
				t.Fatalf("SINK-SIDE BUG: ValidateToken ACCEPTED a token with non-numeric exp=%v (type confusion bypasses expiry)", c.name)
			}
			// Validator path.
			if err := v.ValidateHMAC(tok, secret); err == nil {
				t.Fatalf("SINK-SIDE BUG: Validator.ValidateHMAC ACCEPTED a token with non-numeric exp=%v", c.name)
			}
		})
	}
}

// nbf / iat type confusion on the Validator path must likewise fail closed: a
// present-but-malformed nbf/iat is an error, not "claim absent".
func TestAdversarial2_NbfIatTypeConfusion_FailsClosed(t *testing.T) {
	secret := []byte("secret")
	base := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	v := NewValidator(WithClock(fixedClock{base}))

	for _, claim := range []string{"nbf", "iat"} {
		t.Run(claim, func(t *testing.T) {
			tok := adv2Token(t, secret,
				map[string]interface{}{"alg": "HS256", "typ": "JWT"},
				map[string]interface{}{"sub": "u", "exp": base.Add(time.Hour).Unix(), claim: "not-a-number"},
			)
			if err := v.ValidateHMAC(tok, secret); err == nil {
				t.Fatalf("SINK-SIDE BUG: malformed %s claim accepted (must fail closed)", claim)
			}
		})
	}
}

// =====================================================================
// (B) CONTRACT BOUNDARY — the simple ValidateToken/ParseToken path enforces
// "signature and expiry" ONLY (its doc-comment). It does NOT enforce nbf/iat.
// The Validator path is the one that enforces nbf/iat. We PIN both halves so a
// silent change in either direction is surfaced:
//   - if ValidateToken silently STARTS enforcing nbf, callers relying on the
//     documented narrow contract break;
//   - if the Validator silently STOPS enforcing nbf, a not-yet-valid (e.g.
//     pre-provisioned / future-dated) token would be accepted — a real hole.
// =====================================================================

func TestAdversarial2_SimplePath_DoesNotEnforceNbf_ContractPin(t *testing.T) {
	secret := []byte("secret")
	// nbf far in the future, exp even further: a not-yet-valid token. The simple
	// path (documented: signature + expiry only) does not look at nbf, so as long
	// as exp is in the future it is accepted. Use real-wall-clock-future values so
	// ValidateToken (which uses time.Now) sees a non-expired token.
	now := time.Now()
	tok := adv2Token(t, secret,
		map[string]interface{}{"alg": "HS256", "typ": "JWT"},
		map[string]interface{}{
			"sub": "u",
			"nbf": now.Add(100 * time.Hour).Unix(),
			"exp": now.Add(200 * time.Hour).Unix(),
		},
	)
	if err := ValidateToken(tok, secret); err != nil {
		t.Fatalf("CONTRACT PIN: simple ValidateToken documents signature+expiry only; "+
			"a future-nbf but unexpired token must be accepted, got %v", err)
	}
	// Same for ParseToken (delegates to ValidateToken).
	if _, err := ParseToken(tok, secret); err != nil {
		t.Fatalf("CONTRACT PIN: ParseToken must accept future-nbf unexpired token, got %v", err)
	}
}

func TestAdversarial2_ValidatorPath_DOESEnforceNbf_SecurityPin(t *testing.T) {
	secret := []byte("secret")
	base := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	v := NewValidator(WithClock(fixedClock{base})) // zero leeway
	// Identical token shape, but the Validator path MUST reject the not-yet-valid
	// token. This is the secure entry point; if this flips to accept, a
	// future-dated token becomes usable early — a real authn hole.
	tok := adv2Token(t, secret,
		map[string]interface{}{"alg": "HS256", "typ": "JWT"},
		map[string]interface{}{
			"sub": "u",
			"nbf": base.Add(100 * time.Hour).Unix(),
			"exp": base.Add(200 * time.Hour).Unix(),
		},
	)
	if !errors.Is(v.ValidateHMAC(tok, secret), ErrTokenNotYetValid) {
		t.Fatalf("SECURITY PIN: Validator path must reject not-yet-valid (future nbf) token")
	}
	// iat in the future must likewise be rejected by the Validator path.
	tokIat := adv2Token(t, secret,
		map[string]interface{}{"alg": "HS256", "typ": "JWT"},
		map[string]interface{}{
			"sub": "u",
			"iat": base.Add(100 * time.Hour).Unix(),
			"exp": base.Add(200 * time.Hour).Unix(),
		},
	)
	if !errors.Is(v.ValidateHMAC(tokIat, secret), ErrTokenIssuedInFuture) {
		t.Fatalf("SECURITY PIN: Validator path must reject issued-in-future (future iat) token")
	}
}

// =====================================================================
// (C) KEYSET CROSS-FAMILY ALG CONFUSION — the kid selects a key *kind* (HMAC or
// RSA), but each VerifyXxx dispatches on the token header's alg. An attacker who
// knows a kid is RSA cannot downgrade it to HS256 (and vice-versa). Existing
// keyset tests cover wrong-secret / wrong-kid but NOT cross-family alg confusion
// at the selector. These are the highest-value JWT key-confusion vectors.
// =====================================================================

// RSA-registered kid + token alg=HS256, HMAC-signed using the RSA public modulus
// bytes as the secret (the textbook HS/RS key-confusion attack), aimed straight
// at the KeySet entry point. Must be REJECTED.
func TestAdversarial2_KeySet_RSAKid_HS256Confusion_Rejected(t *testing.T) {
	base := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	v := NewValidator(WithClock(fixedClock{base}))

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	ks := NewKeySet()
	if err := ks.AddRSAPublicKey("rsa-kid", &priv.PublicKey); err != nil {
		t.Fatalf("add rsa: %v", err)
	}

	// Forge an HS256 token using the public modulus bytes as the HMAC secret.
	pubSecret := priv.PublicKey.N.Bytes()
	hj, _ := json.Marshal(map[string]interface{}{"alg": "HS256", "typ": "JWT", "kid": "rsa-kid"})
	cj, _ := json.Marshal(map[string]interface{}{"sub": "admin", "exp": base.Add(time.Hour).Unix()})
	hp := adv2B64(hj)
	pp := adv2B64(cj)
	tok := hp + "." + pp + "." + adv2SignHS256(pubSecret, hp, pp)

	if err := v.ValidateKeySet(tok, ks); err == nil {
		t.Fatalf("SINK-SIDE BUG: KeySet accepted an HS256 token (public-key-as-HMAC-secret) for an RSA-registered kid")
	}
}

// HMAC-registered kid + token alg=RS256. The selector dispatches to the HMAC
// verify (kind=HMAC), whose alg switch rejects RS256. An attacker cannot present
// an RS-headed token against an HMAC kid to dodge the MAC check. Must be REJECTED.
func TestAdversarial2_KeySet_HMACKid_RS256Header_Rejected(t *testing.T) {
	base := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	v := NewValidator(WithClock(fixedClock{base}))

	secret := []byte("hmac-kid-secret")
	ks := NewKeySet()
	if err := ks.AddHMAC("hmac-kid", secret); err != nil {
		t.Fatalf("add hmac: %v", err)
	}
	// Token claims RS256 but is HMAC-signed with the real secret (so a naive path
	// that ignored alg and ran HMAC-with-secret would WRONGLY accept it).
	hj, _ := json.Marshal(map[string]interface{}{"alg": "RS256", "typ": "JWT", "kid": "hmac-kid"})
	cj, _ := json.Marshal(map[string]interface{}{"sub": "admin", "exp": base.Add(time.Hour).Unix()})
	hp := adv2B64(hj)
	pp := adv2B64(cj)
	tok := hp + "." + pp + "." + adv2SignHS256(secret, hp, pp)

	if err := v.ValidateKeySet(tok, ks); !errors.Is(err, ErrInvalidAlg) {
		t.Fatalf("SINK-SIDE BUG: KeySet must reject RS256-headed token against HMAC kid with ErrInvalidAlg, got %v", err)
	}
}

// =====================================================================
// (D) HEADER alg SUBSTITUTION — the header is part of the HMAC-signed input, so
// swapping the alg field after signing must break verification. Proves there is
// no alg-substitution downgrade (e.g. HS256 -> none) that preserves the sig.
// =====================================================================

func TestAdversarial2_AlgSubstitution_AfterSigning_Rejected(t *testing.T) {
	secret := []byte("secret")
	base := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)

	// Legitimately sign an HS256 token.
	hOrig, _ := json.Marshal(map[string]interface{}{"alg": "HS256", "typ": "JWT"})
	cOrig, _ := json.Marshal(map[string]interface{}{"sub": "u", "exp": base.Add(time.Hour).Unix()})
	hpO := adv2B64(hOrig)
	ppO := adv2B64(cOrig)
	sig := adv2SignHS256(secret, hpO, ppO)

	// Swap the header to alg=none, keep the original signature and payload.
	hEvil, _ := json.Marshal(map[string]interface{}{"alg": "none", "typ": "JWT"})
	tokNone := adv2B64(hEvil) + "." + ppO + "." + sig
	if err := ValidateToken(tokNone, secret); err == nil {
		t.Fatalf("SINK-SIDE BUG: alg substituted HS256->none with stale sig was ACCEPTED")
	}

	// Swap the header to a different real alg (HS512), keep the HS256 signature.
	// The signed input changed (different header bytes) AND the alg now selects a
	// different MAC, so it must not verify.
	hEvil2, _ := json.Marshal(map[string]interface{}{"alg": "HS512", "typ": "JWT"})
	tokHS512 := adv2B64(hEvil2) + "." + ppO + "." + sig
	if err := ValidateToken(tokHS512, secret); err == nil {
		t.Fatalf("SINK-SIDE BUG: alg substituted HS256->HS512 with stale sig was ACCEPTED")
	}
}

// =====================================================================
// (E) DEGENERATE / DOS — a JSON 'null' payload parses to a nil claims map. It
// must not panic on any validation path, and (signature still required) it is
// accepted only with a valid MAC. We PIN that current behavior is accept-with-
// nil-claims and explicitly assert NO PANIC. This documents that downstream
// nil-map claim reads are the caller's responsibility.
// =====================================================================

func TestAdversarial2_NullPayload_NoPanic_ContractPin(t *testing.T) {
	secret := []byte("secret")
	base := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	v := NewValidator(WithClock(fixedClock{base}))

	hj, _ := json.Marshal(map[string]interface{}{"alg": "HS256", "typ": "JWT"})
	hp := adv2B64(hj)
	pp := adv2B64([]byte("null")) // JSON null payload -> nil map
	tok := hp + "." + pp + "." + adv2SignHS256(secret, hp, pp)

	// Must not panic on any path; accepted with valid sig (no exp present).
	if err := ValidateToken(tok, secret); err != nil {
		t.Fatalf("CONTRACT PIN: null-payload token with valid sig must be accepted by simple path, got %v", err)
	}
	if err := v.ValidateHMAC(tok, secret); err != nil {
		t.Fatalf("CONTRACT PIN: null-payload token with valid sig must be accepted by Validator, got %v", err)
	}
	claims, err := ParseToken(tok, secret)
	if err != nil {
		t.Fatalf("CONTRACT PIN: ParseToken must accept null-payload token, got %v", err)
	}
	// nil-map read must be safe (no panic) and yield a zero value.
	if _, ok := claims["anything"]; ok {
		t.Fatalf("null-payload claims should be empty/nil, found a value")
	}

	// A wrong secret over the same null-payload token must STILL be rejected:
	// the degenerate payload does not weaken signature enforcement.
	if err := ValidateToken(tok, []byte("wrong-secret")); err == nil {
		t.Fatalf("SINK-SIDE BUG: null-payload token accepted under WRONG secret")
	}
}

// =====================================================================
// (F) exp INT64 OVERFLOW — an absurd far-future exp (1e20) saturates int64 to
// MaxInt64, and time.Unix(MaxInt64,0) wraps internally to a time in the PAST, so
// the token is reported EXPIRED. This is FAIL-CLOSED (an attacker gains nothing
// by submitting an absurd exp). We PIN it so a future change cannot silently turn
// the overflow into a fail-OPEN "never expires". A realistic far-future exp
// (year 9999) must still be accepted — proving the rejection is the overflow
// edge, not a blanket reject of large exp.
// =====================================================================

func TestAdversarial2_ExpInt64Overflow_FailsClosed(t *testing.T) {
	secret := []byte("secret")

	// 1e20 overflows int64 -> MaxInt64 -> time.Unix wraps to the past -> expired.
	tokOverflow := adv2Token(t, secret,
		map[string]interface{}{"alg": "HS256", "typ": "JWT"},
		map[string]interface{}{"sub": "admin", "exp": 1e20},
	)
	if err := ValidateToken(tokOverflow, secret); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("FAIL-CLOSED PIN: exp=1e20 (int64 overflow) must NOT become 'never expires'; "+
			"expected ErrTokenExpired, got %v", err)
	}

	// year 9999 (253402300799) is a realistic far-future exp and must be accepted,
	// proving the rejection above is the overflow edge, not large-exp paranoia.
	tokYear9999 := adv2Token(t, secret,
		map[string]interface{}{"alg": "HS256", "typ": "JWT"},
		map[string]interface{}{"sub": "u", "exp": float64(253402300799)},
	)
	if err := ValidateToken(tokYear9999, secret); err != nil {
		t.Fatalf("FAIL-CLOSED PIN: a realistic year-9999 exp must be accepted, got %v", err)
	}
}

// =====================================================================
// (G) NEGATIVE exp — exp = -1 (and 0) are in the Unix past and must be rejected
// as expired, never accepted. Pins that signed sub-epoch exp values are expired.
// =====================================================================

func TestAdversarial2_NegativeAndZeroExp_Rejected(t *testing.T) {
	secret := []byte("secret")
	for _, exp := range []interface{}{-1, 0, float64(-1), float64(0)} {
		tok := adv2Token(t, secret,
			map[string]interface{}{"alg": "HS256", "typ": "JWT"},
			map[string]interface{}{"sub": "admin", "exp": exp},
		)
		if err := ValidateToken(tok, secret); !errors.Is(err, ErrTokenExpired) {
			t.Fatalf("SINK-SIDE BUG: exp=%v (epoch/sub-epoch, long past) must be ErrTokenExpired, got %v", exp, err)
		}
	}
}
