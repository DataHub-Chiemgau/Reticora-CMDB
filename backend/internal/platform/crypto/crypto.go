// Package crypto provides envelope encryption primitives for the Reticora platform.
//
// Envelope encryption uses a two-tier key hierarchy:
//   - A master key (MEK) derived from the RETICORA_MASTER_KEY environment variable.
//   - Per-organization data encryption keys (DEKs) stored encrypted in the database.
//
// Plaintext data is encrypted with the DEK using AES-256-GCM. The DEK itself is
// encrypted (wrapped) with the MEK for storage.
//
// The secrets the platform derives (Derive) come from the derivation root, a
// random key of the installation that is stored wrapped with the MEK
// (derivation_store.go), so they survive a rotation of the MEK.
package crypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
	// ErrDerivationRoot is returned when the sealed derivation root does not
	// unseal: another master key or tampered ciphertext. It names no key
	// material.
	ErrDerivationRoot = errors.New("crypto: the derivation root does not unseal with the configured master keys " +
		"(RETICORA_MASTER_KEY, RETICORA_MASTER_KEY_PREVIOUS)")
)

// EnvelopeEncryptor provides envelope encryption using AES-256-GCM. It
// holds the current master key and, during a master-key rotation (SEC-06),
// the previous ones: DEKs are wrapped with the current key and marked with
// its key id; DEKs of a previous key stay readable until the rewrap job has
// moved them to the current key, after which the previous key is retired.
// The secrets of the installation are derived from the derivation root
// (Derive), which a master-key rotation does not change.
type EnvelopeEncryptor struct {
	masterKey []byte
	keyID     [keyIDSize]byte
	previous  []masterKey
	// root is the open derivation root (OpenDerivationRoot); nil until then.
	root []byte
}

type masterKey struct {
	key []byte
	id  [keyIDSize]byte
}

// keyIDSize is the length of a master key id: the first bytes of the
// SHA-256 of the key.
const keyIDSize = 8

// wrapMagic marks a wrapped DEK with the id of its master key:
// "RK" 0x01 || key id || nonce || ciphertext+tag. Older DEKs carry no
// marker and are tried against every known key.
var wrapMagic = []byte{'R', 'K', 0x01}

func parseMasterKey(masterKeyBase64 string) (masterKey, error) {
	if masterKeyBase64 == "" {
		return masterKey{}, ErrMasterKeyRequired
	}
	key, err := base64.StdEncoding.DecodeString(masterKeyBase64)
	if err != nil {
		return masterKey{}, fmt.Errorf("crypto: invalid base64 master key: %w", err)
	}
	if len(key) != KeySize {
		return masterKey{}, ErrInvalidKeySize
	}
	sum := sha256.Sum256(key)
	var id [keyIDSize]byte
	copy(id[:], sum[:keyIDSize])
	return masterKey{key: key, id: id}, nil
}

// NewEnvelopeEncryptor creates a new encryptor from a base64-encoded master
// key and, during a rotation, the previous master keys.
func NewEnvelopeEncryptor(masterKeyBase64 string, previousBase64 ...string) (*EnvelopeEncryptor, error) {
	current, err := parseMasterKey(masterKeyBase64)
	if err != nil {
		return nil, err
	}
	e := &EnvelopeEncryptor{masterKey: current.key, keyID: current.id}
	for _, p := range previousBase64 {
		if p == "" {
			continue
		}
		prev, err := parseMasterKey(p)
		if err != nil {
			return nil, fmt.Errorf("crypto: previous master key: %w", err)
		}
		if prev.id != current.id {
			e.previous = append(e.previous, prev)
		}
	}
	return e, nil
}

// Derive returns a 32-byte secret for a purpose (HMAC-SHA256 of the label
// under the derivation root): stable for the installation, different per
// label, without an extra secret to configure. The root, not the master
// key, keeps the derived secrets (the keys of the collector NATS endpoint,
// the pagination cursor key) unchanged across a master-key rotation
// (SEC-06). Deriving before the root is open (LoadDerivationRoot,
// OpenDerivationRoot) is a programming error and panics.
func (e *EnvelopeEncryptor) Derive(label string) []byte {
	if e.root == nil {
		panic("crypto: Derive before the derivation root is open (LoadDerivationRoot)")
	}
	mac := hmac.New(sha256.New, e.root)
	mac.Write([]byte(label))
	return mac.Sum(nil)
}

// NewDerivationRoot draws a derivation root (KeySize bytes of crypto/rand)
// and returns it sealed with the current master key like a DEK
// (AES-256-GCM, marked with the key id); the root leaves only sealed.
// OpenDerivationRoot opens it.
func (e *EnvelopeEncryptor) NewDerivationRoot() ([]byte, error) {
	root, err := GenerateDEK()
	if err != nil {
		return nil, err
	}
	defer clear(root)
	return e.WrapDEK(root)
}

// OpenDerivationRoot unseals a derivation root sealed by NewDerivationRoot
// with the current or a previous master key, or rewrapped since (Rewrap),
// and has Derive use it. A root that does not unseal fails with
// ErrDerivationRoot; another root than the open one is refused, so the
// derived secrets never change while the encryptor is in use. It is called
// at the start, before the encryptor is shared between goroutines.
func (e *EnvelopeEncryptor) OpenDerivationRoot(sealed []byte) error {
	root, err := e.UnwrapDEK(sealed)
	if err != nil {
		return ErrDerivationRoot
	}
	if e.root != nil {
		same := hmac.Equal(e.root, root)
		clear(root)
		if !same {
			return errors.New("crypto: another derivation root is already open")
		}
		return nil
	}
	e.root = root
	return nil
}

// UseEphemeralDerivationRoot opens a new derivation root that lives only
// in this process: for the development mode without a database (--no-db)
// and for tests. Secrets derived from it change with every start.
func (e *EnvelopeEncryptor) UseEphemeralDerivationRoot() error {
	sealed, err := e.NewDerivationRoot()
	if err != nil {
		return err
	}
	return e.OpenDerivationRoot(sealed)
}

// KeyID returns the id of the current master key (hex).
func (e *EnvelopeEncryptor) KeyID() string { return hex.EncodeToString(e.keyID[:]) }

// IsCurrent reports whether a wrapped DEK is wrapped with the current
// master key; the rewrap job moves the others.
func (e *EnvelopeEncryptor) IsCurrent(wrappedDEK []byte) bool {
	id, _, ok := splitWrapped(wrappedDEK)
	return ok && id == e.keyID
}

// Rewrap returns the DEK wrapped with the current master key.
func (e *EnvelopeEncryptor) Rewrap(wrappedDEK []byte) ([]byte, error) {
	dek, err := e.UnwrapDEK(wrappedDEK)
	if err != nil {
		return nil, err
	}
	return e.WrapDEK(dek)
}

func splitWrapped(wrapped []byte) (id [keyIDSize]byte, body []byte, ok bool) {
	if len(wrapped) < len(wrapMagic)+keyIDSize || !bytes.Equal(wrapped[:len(wrapMagic)], wrapMagic) {
		return id, wrapped, false
	}
	copy(id[:], wrapped[len(wrapMagic):len(wrapMagic)+keyIDSize])
	return id, wrapped[len(wrapMagic)+keyIDSize:], true
}

// GenerateDEK creates a new random 32-byte data encryption key.
func GenerateDEK() ([]byte, error) {
	dek := make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, dek); err != nil {
		return nil, fmt.Errorf("crypto: failed to generate DEK: %w", err)
	}
	return dek, nil
}

// GenerateMasterKey creates a base64-encoded random master key. It is intended
// for local development only; production deployments must supply a persistent
// key via RETICORA_MASTER_KEY so that stored ciphertext stays decryptable.
func GenerateMasterKey() (string, error) {
	key, err := GenerateDEK()
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

// WrapDEK encrypts a DEK with the current master key (AES-256-GCM) and
// marks it with the key id.
func (e *EnvelopeEncryptor) WrapDEK(dek []byte) ([]byte, error) {
	if len(dek) != KeySize {
		return nil, ErrInvalidKeySize
	}
	body, err := encrypt(e.masterKey, dek)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(wrapMagic)+keyIDSize+len(body))
	out = append(out, wrapMagic...)
	out = append(out, e.keyID[:]...)
	return append(out, body...), nil
}

// UnwrapDEK decrypts a wrapped DEK with the master key that wrapped it: the
// one its key id names, or for an unmarked DEK the current and then each
// previous key.
func (e *EnvelopeEncryptor) UnwrapDEK(wrappedDEK []byte) ([]byte, error) {
	keys := append([]masterKey{{key: e.masterKey, id: e.keyID}}, e.previous...)
	id, body, marked := splitWrapped(wrappedDEK)
	for _, k := range keys {
		if marked && k.id != id {
			continue
		}
		dek, err := decrypt(k.key, body)
		if err != nil {
			continue
		}
		if len(dek) != KeySize {
			return nil, ErrInvalidKeySize
		}
		return dek, nil
	}
	return nil, ErrDecryptionFailed
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
