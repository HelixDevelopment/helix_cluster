package jwt

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

func TestParseValidToken(t *testing.T) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user1"}`))
	tokenStr := header + "." + payload + "." + base64.RawURLEncoding.EncodeToString([]byte("sig"))

	tok, err := Parse(tokenStr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok.Header.Alg != "HS256" {
		t.Errorf("expected alg HS256, got %s", tok.Header.Alg)
	}
	if tok.Payload["sub"] != "user1" {
		t.Errorf("expected sub=user1, got %v", tok.Payload["sub"])
	}
}

func TestParseInvalidFormat(t *testing.T) {
	_, err := Parse("invalid")
	if err == nil {
		t.Error("expected error for invalid token")
	}
}

func TestParseInvalidBase64(t *testing.T) {
	_, err := Parse("badheader.payload.sig")
	if err == nil {
		t.Error("expected error for bad base64")
	}
}

func TestVerifyHMAC(t *testing.T) {
	secret := []byte("my-secret")
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user1"}`))

	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(header + "." + payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	tokenStr := header + "." + payload + "." + sig
	tok, err := Parse(tokenStr)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if err := tok.VerifyHMAC(secret); err != nil {
		t.Fatalf("HMAC verification failed: %v", err)
	}
}

func TestVerifyHMACBadSecret(t *testing.T) {
	secret := []byte("my-secret")
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user1"}`))

	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(header + "." + payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	tokenStr := header + "." + payload + "." + sig
	tok, err := Parse(tokenStr)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if err := tok.VerifyHMAC([]byte("wrong-secret")); err == nil {
		t.Fatal("expected verification to fail with wrong secret")
	}
}

func TestVerifyRSA(t *testing.T) {
	privKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}

	pubASN1, err := x509.MarshalPKIXPublicKey(&privKey.PublicKey)
	if err != nil {
		t.Fatalf("failed to marshal public key: %v", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubASN1})

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user1"}`))

	hash := sha256.Sum256([]byte(header + "." + payload))
	sigBytes, err := rsa.SignPKCS1v15(rand.Reader, privKey, 0, hash[:])
	if err != nil {
		t.Fatalf("failed to sign: %v", err)
	}
	sig := base64.RawURLEncoding.EncodeToString(sigBytes)

	tokenStr := header + "." + payload + "." + sig
	tok, err := Parse(tokenStr)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if err := tok.VerifyRSA(pubPEM); err != nil {
		t.Fatalf("RSA verification failed: %v", err)
	}
}

func TestVerifyRSABadKey(t *testing.T) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user1"}`))
	sig := base64.RawURLEncoding.EncodeToString([]byte("bad-sig"))

	tokenStr := header + "." + payload + "." + sig
	tok, err := Parse(tokenStr)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if err := tok.VerifyRSA([]byte("not-a-pem")); err == nil {
		t.Fatal("expected error for invalid PEM")
	}
}

func TestDecodePayload(t *testing.T) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user1"}`))
	tokenStr := header + "." + payload + "." + base64.RawURLEncoding.EncodeToString([]byte("sig"))

	tok, err := Parse(tokenStr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := tok.DecodePayload()
	if err != nil {
		t.Fatalf("decode error: %v", err)
	}
	var p map[string]interface{}
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if p["sub"] != "user1" {
		t.Errorf("expected sub=user1, got %v", p["sub"])
	}
}

func TestGenerateToken(t *testing.T) {
	secret := []byte("super-secret")
	claims := map[string]interface{}{"sub": "user1", "role": "admin"}
	token, err := GenerateToken(claims, secret, time.Hour)
	if err != nil {
		t.Fatalf("generate token failed: %v", err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 parts, got %d", len(parts))
	}

	parsed, err := Parse(token)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if parsed.Payload["sub"] != "user1" {
		t.Errorf("expected sub=user1, got %v", parsed.Payload["sub"])
	}
	if parsed.Payload["role"] != "admin" {
		t.Errorf("expected role=admin, got %v", parsed.Payload["role"])
	}
	if _, ok := parsed.Payload["iat"]; !ok {
		t.Error("expected iat claim")
	}
	if _, ok := parsed.Payload["exp"]; !ok {
		t.Error("expected exp claim")
	}
}

func TestValidateTokenSuccess(t *testing.T) {
	secret := []byte("super-secret")
	claims := map[string]interface{}{"sub": "user1"}
	token, err := GenerateToken(claims, secret, time.Hour)
	if err != nil {
		t.Fatalf("generate token failed: %v", err)
	}
	if err := ValidateToken(token, secret); err != nil {
		t.Fatalf("validate failed: %v", err)
	}
}

func TestValidateTokenExpired(t *testing.T) {
	secret := []byte("super-secret")
	claims := map[string]interface{}{"sub": "user1"}
	token, err := GenerateToken(claims, secret, -time.Second)
	if err != nil {
		t.Fatalf("generate token failed: %v", err)
	}
	if err := ValidateToken(token, secret); err != ErrTokenExpired {
		t.Fatalf("expected ErrTokenExpired, got %v", err)
	}
}

func TestValidateTokenBadSecret(t *testing.T) {
	secret := []byte("super-secret")
	claims := map[string]interface{}{"sub": "user1"}
	token, err := GenerateToken(claims, secret, time.Hour)
	if err != nil {
		t.Fatalf("generate token failed: %v", err)
	}
	if err := ValidateToken(token, []byte("wrong-secret")); err == nil {
		t.Fatal("expected validation to fail with wrong secret")
	}
}

func TestParseToken(t *testing.T) {
	secret := []byte("super-secret")
	claims := map[string]interface{}{"sub": "user1", "role": "admin"}
	token, err := GenerateToken(claims, secret, time.Hour)
	if err != nil {
		t.Fatalf("generate token failed: %v", err)
	}
	parsedClaims, err := ParseToken(token, secret)
	if err != nil {
		t.Fatalf("parse token failed: %v", err)
	}
	if parsedClaims["sub"] != "user1" {
		t.Errorf("expected sub=user1, got %v", parsedClaims["sub"])
	}
}

func TestParseTokenExpired(t *testing.T) {
	secret := []byte("super-secret")
	claims := map[string]interface{}{"sub": "user1"}
	token, err := GenerateToken(claims, secret, -time.Second)
	if err != nil {
		t.Fatalf("generate token failed: %v", err)
	}
	_, err = ParseToken(token, secret)
	if err != ErrTokenExpired {
		t.Fatalf("expected ErrTokenExpired, got %v", err)
	}
}

// --- Mutation Tests ---

func TestMutationVerifyHMAC(t *testing.T) {
	secret := []byte("secret")
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user1"}`))

	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(header + "." + payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	tok, _ := Parse(header + "." + payload + "." + sig)

	tok.rawBody = base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"attacker"}`))
	if err := tok.VerifyHMAC(secret); err == nil {
		t.Fatal("mutation: expected verification to fail after payload tamper")
	}
}

func TestMutationVerifyRSA(t *testing.T) {
	privKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	pubASN1, _ := x509.MarshalPKIXPublicKey(&privKey.PublicKey)
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubASN1})

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user1"}`))

	hash := sha256.Sum256([]byte(header + "." + payload))
	sigBytes, _ := rsa.SignPKCS1v15(rand.Reader, privKey, 0, hash[:])
	sig := base64.RawURLEncoding.EncodeToString(sigBytes)

	tok, _ := Parse(header + "." + payload + "." + sig)

	tok.rawBody = base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"attacker"}`))
	if err := tok.VerifyRSA(pubPEM); err == nil {
		t.Fatal("mutation: expected verification to fail after payload tamper")
	}
}

func TestGenerateTokenDoesNotMutateInputClaims(t *testing.T) {
	secret := []byte("secret")
	claims := map[string]interface{}{"sub": "user1"}
	originalLen := len(claims)
	_, err := GenerateToken(claims, secret, time.Hour)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if len(claims) != originalLen {
		t.Error("GenerateToken must not mutate input claims map")
	}
}
