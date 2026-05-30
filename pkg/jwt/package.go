// Package jwt provides JWT utilities for Helix Cluster OS.
package jwt

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// Token is a minimal JWT representation.
type Token struct {
	Header    string
	Payload   string
	Signature string
}

// Parse parses a compact JWT string.
func Parse(token string) (*Token, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid token format")
	}
	return &Token{Header: parts[0], Payload: parts[1], Signature: parts[2]}, nil
}

// DecodePayload decodes the payload from base64url.
func (t *Token) DecodePayload() ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(t.Payload)
}
