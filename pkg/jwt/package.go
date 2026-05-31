// Package jwt provides JWT utilities for Helix Cluster OS.
package jwt

import (
	"crypto/hmac"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"hash"
	"strings"
	"time"
)

var (
	ErrInvalidToken = errors.New("invalid token format")
	ErrInvalidAlg   = errors.New("unsupported algorithm")
	ErrInvalidKey   = errors.New("invalid key")
	ErrVerifyFailed = errors.New("signature verification failed")
	ErrTokenExpired = errors.New("token has expired")
)

// Header represents a JWT header.
type Header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

// Token is a parsed JWT representation.
type Token struct {
	Raw       string
	Header    Header
	Payload   map[string]interface{}
	Signature []byte
	rawHeader string
	rawBody   string
}

// Parse parses a compact JWT string and validates basic structure.
func Parse(token string) (*Token, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrInvalidToken
	}

	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("invalid header encoding: %w", err)
	}

	var header Header
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, fmt.Errorf("invalid header JSON: %w", err)
	}

	if header.Alg == "" {
		return nil, ErrInvalidAlg
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid payload encoding: %w", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return nil, fmt.Errorf("invalid payload JSON: %w", err)
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("invalid signature encoding: %w", err)
	}

	return &Token{
		Raw:       token,
		Header:    header,
		Payload:   payload,
		Signature: sig,
		rawHeader: parts[0],
		rawBody:   parts[1],
	}, nil
}

// VerifyHMAC verifies the token signature using HMAC with the given secret.
func (t *Token) VerifyHMAC(secret []byte) error {
	switch t.Header.Alg {
	case "HS256":
		return t.verifyHMAC(secret, sha256.New)
	case "HS384":
		return t.verifyHMAC(secret, sha512.New384)
	case "HS512":
		return t.verifyHMAC(secret, sha512.New)
	default:
		return fmt.Errorf("%w: %s", ErrInvalidAlg, t.Header.Alg)
	}
}

func (t *Token) verifyHMAC(secret []byte, h func() hash.Hash) error {
	mac := hmac.New(h, secret)
	mac.Write([]byte(t.rawHeader + "." + t.rawBody))
	expected := mac.Sum(nil)
	if !hmac.Equal(expected, t.Signature) {
		return ErrVerifyFailed
	}
	return nil
}

// VerifyRSA verifies the token signature using RSA with the given PEM-encoded public key.
func (t *Token) VerifyRSA(publicKeyPEM []byte) error {
	block, _ := pem.Decode(publicKeyPEM)
	if block == nil {
		return ErrInvalidKey
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidKey, err)
	}
	rsaPub, ok := pub.(*rsa.PublicKey)
	if !ok {
		return ErrInvalidKey
	}

	switch t.Header.Alg {
	case "RS256":
		return t.verifyRSA(rsaPub, sha256.New, cryptoSHA256)
	case "RS384":
		return t.verifyRSA(rsaPub, sha512.New384, cryptoSHA384)
	case "RS512":
		return t.verifyRSA(rsaPub, sha512.New, cryptoSHA512)
	default:
		return fmt.Errorf("%w: %s", ErrInvalidAlg, t.Header.Alg)
	}
}

func (t *Token) verifyRSA(pub *rsa.PublicKey, h func() hash.Hash, hashType cryptoHash) error {
	hasher := h()
	hasher.Write([]byte(t.rawHeader + "." + t.rawBody))
	hashed := hasher.Sum(nil)
	var err error
	switch hashType {
	case cryptoSHA256:
		err = rsa.VerifyPKCS1v15(pub, 0, hashed, t.Signature)
	case cryptoSHA384:
		err = rsa.VerifyPKCS1v15(pub, 1, hashed, t.Signature)
	case cryptoSHA512:
		err = rsa.VerifyPKCS1v15(pub, 2, hashed, t.Signature)
	}
	if err != nil {
		return ErrVerifyFailed
	}
	return nil
}

// DecodePayload returns the raw payload bytes.
func (t *Token) DecodePayload() ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(t.rawBody)
}

// GenerateToken creates a new HS256 JWT with the given claims, secret, and duration.
// It automatically sets the 'iat' (issued at) and 'exp' (expiration) claims.
func GenerateToken(claims map[string]interface{}, secret []byte, duration time.Duration) (string, error) {
	header := Header{Alg: "HS256", Typ: "JWT"}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("failed to marshal header: %w", err)
	}

	now := time.Now().UTC()
	payload := make(map[string]interface{}, len(claims)+2)
	for k, v := range claims {
		payload[k] = v
	}
	payload["iat"] = now.Unix()
	payload["exp"] = now.Add(duration).Unix()

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal payload: %w", err)
	}

	encodedHeader := base64.RawURLEncoding.EncodeToString(headerJSON)
	encodedPayload := base64.RawURLEncoding.EncodeToString(payloadJSON)

	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(encodedHeader + "." + encodedPayload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return encodedHeader + "." + encodedPayload + "." + sig, nil
}

// ValidateToken parses and validates a JWT token (signature and expiry) using HS256.
func ValidateToken(token string, secret []byte) error {
	tok, err := Parse(token)
	if err != nil {
		return err
	}
	if err := tok.VerifyHMAC(secret); err != nil {
		return err
	}
	if exp, ok := tok.Payload["exp"]; ok {
		var expUnix int64
		switch v := exp.(type) {
		case float64:
			expUnix = int64(v)
		case int64:
			expUnix = v
		case int:
			expUnix = int64(v)
		default:
			return fmt.Errorf("invalid exp claim type: %T", exp)
		}
		if time.Now().UTC().After(time.Unix(expUnix, 0).UTC()) {
			return ErrTokenExpired
		}
	}
	return nil
}

// ParseToken parses and validates a JWT token, then returns its claims.
func ParseToken(token string, secret []byte) (map[string]interface{}, error) {
	if err := ValidateToken(token, secret); err != nil {
		return nil, err
	}
	tok, err := Parse(token)
	if err != nil {
		return nil, err
	}
	return tok.Payload, nil
}

type cryptoHash int

const (
	cryptoSHA256 cryptoHash = iota
	cryptoSHA384
	cryptoSHA512
)
