package jwt

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/HelixDevelopment/helix_cluster/pkg/testing/evidence"
)

// TestJWT_SignVerifyTamper_Evidence is the §7.1-evidence proof of the JWT
// sign -> verify -> tamper-detection lifecycle. A unique per-run token is placed
// in the "jti" claim and signed (HS256). Verification of the untouched token
// MUST succeed and recover the exact token (positive sink evidence). A token
// whose payload is mutated to forge a different "jti" MUST be REJECTED — and the
// rejection is the load-bearing security guarantee (state delta: valid->invalid).
// A non-functional verifier (one that ignores the signature) would accept the
// forged token and FAIL this test.
func TestJWT_SignVerifyTamper_Evidence(t *testing.T) {
	e := evidence.NewT(t, "jwt-sign-verify-tamper", t.TempDir())
	token := e.Token()
	secret := []byte("helix-jwt-signing-secret")

	signed, err := GenerateToken(map[string]interface{}{
		"sub": "helix-user",
		"jti": token,
	}, secret, time.Hour)
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}

	// 1) The genuine token verifies and its claims recover the per-run token.
	claims, err := ParseToken(signed, secret)
	if err != nil {
		t.Fatalf("genuine token rejected: %v", err)
	}
	gotJTI, _ := claims["jti"].(string)
	e.MustPositive(t, "verified-claims-contain-token", gotJTI, token)

	// 2) Forge the payload: replace jti with an attacker value WITHOUT re-signing.
	parts := strings.Split(signed, ".")
	if len(parts) != 3 {
		t.Fatalf("unexpected token shape: %d parts", len(parts))
	}
	forgedJTI := "attacker-" + token
	forgedPayload := base64.RawURLEncoding.EncodeToString(mustJSON(t, map[string]interface{}{
		"sub": "helix-user",
		"jti": forgedJTI,
		"iat": claims["iat"],
		"exp": claims["exp"],
	}))
	forged := parts[0] + "." + forgedPayload + "." + parts[2]

	// The forged token MUST be rejected — this is the tamper-detection proof.
	validErr := ValidateToken(signed, secret) == nil // genuine: should be valid (true)
	forgedRejected := ValidateToken(forged, secret) != nil

	if !validErr {
		t.Fatal("genuine token unexpectedly failed validation")
	}
	if !forgedRejected {
		t.Fatal("SECURITY: forged (tampered) token was accepted — signature not enforced")
	}

	// §7.1 #2 DELTA: validity flips from genuine(valid) to forged(rejected).
	e.MustDelta(t, "tamper-detection-validity-flip", validErr, !forgedRejected)

	// Sanity: a forged token must not surface its attacker jti as verified.
	if _, perr := ParseToken(forged, secret); perr == nil {
		t.Fatal("ParseToken returned claims for a tampered token")
	}

	if _, err := e.Artifact("signed_token.jwt", []byte(signed)); err != nil {
		t.Fatalf("artifact signed: %v", err)
	}
	if _, err := e.Artifact("forged_token.jwt", []byte(forged)); err != nil {
		t.Fatalf("artifact forged: %v", err)
	}
}

func mustJSON(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
