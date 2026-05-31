// Package crypto provides cryptographic utilities for Helix Cluster OS.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/pbkdf2"
)

var (
	ErrInvalidKey       = errors.New("invalid key")
	ErrVerifyFailed     = errors.New("signature verification failed")
	ErrInvalidCiphertext = errors.New("invalid ciphertext")
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

// KeyPair holds an Ed25519 signing key pair.
type KeyPair struct {
	Public  ed25519.PublicKey
	Private ed25519.PrivateKey
}

// GenerateKeyPair generates a new Ed25519 key pair using crypto/rand.
func GenerateKeyPair() (*KeyPair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate ed25519 key pair: %w", err)
	}
	return &KeyPair{Public: pub, Private: priv}, nil
}

// Sign signs message with the given Ed25519 private key and returns the hex-encoded signature.
func Sign(privateKey ed25519.PrivateKey, message []byte) (string, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("invalid private key size: expected %d, got %d: %w", ed25519.PrivateKeySize, len(privateKey), ErrInvalidKey)
	}
	sig := ed25519.Sign(privateKey, message)
	return hex.EncodeToString(sig), nil
}

// Verify verifies an Ed25519 signature (hex-encoded) against a message.
func Verify(publicKey ed25519.PublicKey, message []byte, signatureHex string) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid public key size: expected %d, got %d: %w", ed25519.PublicKeySize, len(publicKey), ErrInvalidKey)
	}
	sig, err := hex.DecodeString(signatureHex)
	if err != nil {
		return fmt.Errorf("invalid signature hex: %w", err)
	}
	if !ed25519.Verify(publicKey, message, sig) {
		return fmt.Errorf("signature verification failed: %w", ErrVerifyFailed)
	}
	return nil
}

// Encrypt encrypts plaintext with AES-GCM using the provided key.
// The key must be 16, 24, or 32 bytes (AES-128, AES-192, AES-256).
// Returns hex-encoded ciphertext with the nonce prepended.
func Encrypt(key, plaintext []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("failed to create AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return hex.EncodeToString(ciphertext), nil
}

// Decrypt decrypts AES-GCM ciphertext (hex-encoded with nonce prepended).
func Decrypt(key []byte, ciphertextHex string) ([]byte, error) {
	ciphertext, err := hex.DecodeString(ciphertextHex)
	if err != nil {
		return nil, fmt.Errorf("invalid ciphertext hex: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, fmt.Errorf("ciphertext too short: %w", ErrInvalidCiphertext)
	}
	nonce, ciphertext := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decryption failed: %w", err)
	}
	return plaintext, nil
}

// DeriveKey derives a key of the specified length using PBKDF2 with SHA-256.
func DeriveKey(password, salt []byte, iterations, keyLen int) []byte {
	return pbkdf2.Key(password, salt, iterations, keyLen, sha256.New)
}
