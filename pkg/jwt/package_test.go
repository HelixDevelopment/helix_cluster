package jwt

import (
	"encoding/base64"
	"testing"
)

func TestParse(t *testing.T) {
	tokenStr := "eyJhbGciOiJIUzI1NiJ9." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"user1"}`)) + ".signature"
	tok, err := Parse(tokenStr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok.Header == "" {
		t.Error("expected non-empty header")
	}
}

func TestParseInvalid(t *testing.T) {
	_, err := Parse("invalid")
	if err == nil {
		t.Error("expected error for invalid token")
	}
}
