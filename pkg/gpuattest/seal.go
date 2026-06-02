package gpuattest

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
)

// HXC-1571: device-sealed encrypt/decrypt (AES-256-GCM keyed via HKDF).

// ErrSealOpen is returned by OpenFromDevice when the ciphertext fails GCM
// authentication under the supplied device secret — i.e. the wrong device (or
// tampered ciphertext).
var ErrSealOpen = errors.New("gpuattest: device seal open failed (auth)")

// sealInfo is the HKDF "info" context binding derived keys to this scheme, so a
// key derived here can never collide with a key derived for another purpose
// from the same device secret.
var sealInfo = []byte("helix/gpuattest/seal/v1")

// sealSalt is a fixed non-secret salt. HKDF-Extract still produces independent
// keys per distinct device secret; per-message uniqueness comes from the random
// GCM nonce, not the salt.
var sealSalt = []byte("helix-gpuattest-seal-salt")

// deriveKey turns a device secret of any length into a 32-byte AES-256 key via
// HKDF-SHA256. Two different secrets yield two different keys, so device-B can
// never derive device-A's key.
func deriveKey(deviceSecret []byte) ([]byte, error) {
	return hkdf.Key(sha256.New, deviceSecret, sealSalt, string(sealInfo), 32)
}

// SealForDevice encrypts payload under a key derived from deviceSecret using
// AES-256-GCM. A fresh 12-byte random nonce is generated per call and PREPENDED
// to the ciphertext, so the same payload+secret produces different output each
// time yet always decrypts. Output layout: nonce(12) || gcmCiphertext.
func SealForDevice(payload, deviceSecret []byte) ([]byte, error) {
	key, err := deriveKey(deviceSecret)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	// Seal appends the ciphertext+tag to the nonce prefix; nonce is also passed
	// as the dst so the result is nonce || ciphertext.
	return gcm.Seal(nonce, nonce, payload, nil), nil
}

// OpenFromDevice reverses SealForDevice. It derives the key from deviceSecret,
// splits off the nonce prefix, and authenticates+decrypts. A wrong device
// secret derives a different key, so GCM authentication fails and ErrSealOpen
// is returned (no plaintext leaks).
func OpenFromDevice(ciphertext, deviceSecret []byte) ([]byte, error) {
	key, err := deriveKey(deviceSecret)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(ciphertext) < ns {
		return nil, ErrSealOpen
	}
	nonce, ct := ciphertext[:ns], ciphertext[ns:]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, ErrSealOpen
	}
	return pt, nil
}
