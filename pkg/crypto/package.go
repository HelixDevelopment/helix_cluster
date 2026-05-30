// Package crypto provides cryptographic utilities for Helix Cluster OS.
package crypto

import (
	"crypto/sha256"
	"fmt"
)

// Hash returns the SHA-256 hex digest of data.
func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum)
}

// GenerateKey generates a deterministic placeholder key.
func GenerateKey(seed string) string {
	return Hash([]byte(seed))[:32]
}
