// Package crypto provides envelope encryption primitives for the Reticora platform.
//
// Envelope encryption uses a two-tier key hierarchy:
//   - A master key (MEK) derived from the RETICORA_MASTER_KEY environment variable.
//   - Per-organization data encryption keys (DEKs) stored encrypted in the database.
//
// Plaintext data is encrypted with the DEK using AES-256-GCM. The DEK itself is
// encrypted (wrapped) with the MEK for storage.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

const (
	// KeySize is the required size for AES-256 keys (32 bytes).
	KeySize = 32
	// NonceSize is the standard GCM nonce size (12 bytes).
	NonceSize = 12
)

var (
	// ErrInvalidKeySize is returned when a key does not have the required length.
	ErrInvalidKeySize = errors.New("crypto: key must be 32 bytes (AES-256)")
	// ErrDecryptionFailed is returned when ciphertext cannot be decrypted.
	ErrDecryptionFailed = errors.New("crypto: decryption failed")
	// ErrMasterKeyRequired is returned when master key is empty.
	ErrMasterKeyRequired = errors.New("crypto: master key is required")
)

// EnvelopeEncryptor provides envelope encryption using AES-256-GCM.
type EnvelopeEncryptor struct {
	masterKey []byte
}

// NewEnvelopeEncryptor creates a new encryptor from a base64-encoded master key.
func NewEnvelopeEncryptor(masterKeyBase64 string) (*EnvelopeEncryptor, error) {
	if masterKeyBase64 == "" {
		return nil, ErrMasterKeyRequired
	}

	masterKey, err := base64.StdEncoding.DecodeString(masterKeyBase64)
	if err != nil {
		return nil, fmt.Errorf("crypto: invalid base64 master key: %w", err)
	}

	if len(masterKey) != KeySize {
		return nil, ErrInvalidKeySize
	}

	return &EnvelopeEncryptor{masterKey: masterKey}, nil
}

// GenerateDEK creates a new random 32-byte data encryption key.
func GenerateDEK() ([]byte, error) {
	dek := make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return nil, fmt.Errorf("crypto: failed to generate DEK: %w", err)
	}
	return dek, nil
}

// WrapDEK encrypts a DEK with the master key (AES-256-GCM).
func (e *EnvelopeEncryptor) WrapDEK(dek []byte) ([]byte, error) {
	if len(dek) != KeySize {
		return nil, ErrInvalidKeySize
	}
	return encrypt(e.masterKey, dek)
}

// UnwrapDEK decrypts a wrapped DEK using the master key.
func (e *EnvelopeEncryptor) UnwrapDEK(wrappedDEK []byte) ([]byte, error) {
	dek, err := decrypt(e.masterKey, wrappedDEK)
	if err != nil {
		return nil, ErrDecryptionFailed
	}
	if len(dek) != KeySize {
		return nil, ErrInvalidKeySize
	}
	return dek, nil
}

// Encrypt encrypts plaintext using the provided DEK with AES-256-GCM.
func Encrypt(dek, plaintext []byte) ([]byte, error) {
	if len(dek) != KeySize {
		return nil, ErrInvalidKeySize
	}
	return encrypt(dek, plaintext)
}

// Decrypt decrypts ciphertext using the provided DEK with AES-256-GCM.
func Decrypt(dek, ciphertext []byte) ([]byte, error) {
	if len(dek) != KeySize {
		return nil, ErrInvalidKeySize
	}
	plaintext, err := decrypt(dek, ciphertext)
	if err != nil {
		return nil, ErrDecryptionFailed
	}
	return plaintext, nil
}

// encrypt performs AES-256-GCM encryption.
// Output format: nonce (12 bytes) || ciphertext+tag
func encrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: failed to create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("crypto: failed to generate nonce: %w", err)
	}

	// Seal appends the ciphertext to nonce
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return ciphertext, nil
}

// decrypt performs AES-256-GCM decryption.
// Input format: nonce (12 bytes) || ciphertext+tag
func decrypt(key, data []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: failed to create GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return nil, errors.New("crypto: ciphertext too short")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("crypto: GCM open failed: %w", err)
	}

	return plaintext, nil
}
