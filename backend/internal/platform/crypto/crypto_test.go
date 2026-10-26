package crypto_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
)

func testMasterKey(t *testing.T) string {
	t.Helper()
	key := make([]byte, crypto.KeySize)
	for i := range key {
		key[i] = byte(i)
	}
	return base64.StdEncoding.EncodeToString(key)
}

func TestNewEnvelopeEncryptor(t *testing.T) {
	t.Run("valid key", func(t *testing.T) {
		enc, err := crypto.NewEnvelopeEncryptor(testMasterKey(t))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if enc == nil {
			t.Fatal("expected non-nil encryptor")
		}
	})

	t.Run("empty key", func(t *testing.T) {
		_, err := crypto.NewEnvelopeEncryptor("")
		if err != crypto.ErrMasterKeyRequired {
			t.Fatalf("expected ErrMasterKeyRequired, got %v", err)
		}
	})

	t.Run("invalid base64", func(t *testing.T) {
		_, err := crypto.NewEnvelopeEncryptor("not-valid-base64!!!")
		if err == nil {
			t.Fatal("expected error for invalid base64")
		}
	})

	t.Run("wrong key size", func(t *testing.T) {
		shortKey := base64.StdEncoding.EncodeToString([]byte("tooshort"))
		_, err := crypto.NewEnvelopeEncryptor(shortKey)
		if err != crypto.ErrInvalidKeySize {
			t.Fatalf("expected ErrInvalidKeySize, got %v", err)
		}
	})
}

func TestGenerateDEK(t *testing.T) {
	dek, err := crypto.GenerateDEK()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dek) != crypto.KeySize {
		t.Fatalf("expected %d bytes, got %d", crypto.KeySize, len(dek))
	}

	// Two generated DEKs should differ
	dek2, _ := crypto.GenerateDEK()
	if string(dek) == string(dek2) {
		t.Fatal("two generated DEKs should not be identical")
	}
}

func TestWrapUnwrapDEK(t *testing.T) {
	enc, err := crypto.NewEnvelopeEncryptor(testMasterKey(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dek, err := crypto.GenerateDEK()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wrapped, err := enc.WrapDEK(dek)
	if err != nil {
		t.Fatalf("WrapDEK error: %v", err)
	}

	// Wrapped should differ from original
	if string(wrapped) == string(dek) {
		t.Fatal("wrapped DEK should not equal plaintext DEK")
	}

	unwrapped, err := enc.UnwrapDEK(wrapped)
	if err != nil {
		t.Fatalf("UnwrapDEK error: %v", err)
	}

	if string(unwrapped) != string(dek) {
		t.Fatal("unwrapped DEK does not match original")
	}
}

func TestWrapDEK_InvalidSize(t *testing.T) {
	enc, _ := crypto.NewEnvelopeEncryptor(testMasterKey(t))
	_, err := enc.WrapDEK([]byte("short"))
	if err != crypto.ErrInvalidKeySize {
		t.Fatalf("expected ErrInvalidKeySize, got %v", err)
	}
}

func TestUnwrapDEK_Tampered(t *testing.T) {
	enc, _ := crypto.NewEnvelopeEncryptor(testMasterKey(t))
	dek, _ := crypto.GenerateDEK()
	wrapped, _ := enc.WrapDEK(dek)

	// Tamper with wrapped data
	wrapped[len(wrapped)-1] ^= 0xFF

	_, err := enc.UnwrapDEK(wrapped)
	if err != crypto.ErrDecryptionFailed {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

func TestEncryptDecrypt(t *testing.T) {
	dek, _ := crypto.GenerateDEK()
	plaintext := []byte(`{"username":"admin","password":"s3cr3t"}`)

	ciphertext, err := crypto.Encrypt(dek, plaintext)
	if err != nil {
		t.Fatalf("Encrypt error: %v", err)
	}

	if string(ciphertext) == string(plaintext) {
		t.Fatal("ciphertext should not equal plaintext")
	}

	decrypted, err := crypto.Decrypt(dek, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt error: %v", err)
	}

	if string(decrypted) != string(plaintext) {
		t.Fatalf("decrypted does not match: got %q", decrypted)
	}
}

func TestDecrypt_Tampered(t *testing.T) {
	dek, _ := crypto.GenerateDEK()
	plaintext := []byte("hello world")

	ciphertext, _ := crypto.Encrypt(dek, plaintext)
	ciphertext[len(ciphertext)-1] ^= 0xFF

	_, err := crypto.Decrypt(dek, ciphertext)
	if err != crypto.ErrDecryptionFailed {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

func TestDecrypt_WrongKey(t *testing.T) {
	dek1, _ := crypto.GenerateDEK()
	dek2, _ := crypto.GenerateDEK()
	plaintext := []byte("secret data")

	ciphertext, _ := crypto.Encrypt(dek1, plaintext)
	_, err := crypto.Decrypt(dek2, ciphertext)
	if err != crypto.ErrDecryptionFailed {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

func TestEncrypt_EmptyPlaintext(t *testing.T) {
	dek, _ := crypto.GenerateDEK()
	ciphertext, err := crypto.Encrypt(dek, []byte{})
	if err != nil {
		t.Fatalf("Encrypt error: %v", err)
	}

	decrypted, err := crypto.Decrypt(dek, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt error: %v", err)
	}
	if len(decrypted) != 0 {
		t.Fatalf("expected empty, got %d bytes", len(decrypted))
	}
}

// TestMasterKeyRotation covers SEC-06 (WP-141): a wrapped DEK names its
// master key; during a rotation the previous key still unwraps, the rewrap
// moves the DEK to the current key, after which the previous key can be
// retired; unmarked DEKs from before the key ids stay readable.
func TestMasterKeyRotation(t *testing.T) {
	oldKey, _ := crypto.GenerateMasterKey()
	newKey, _ := crypto.GenerateMasterKey()
	oldEnc, err := crypto.NewEnvelopeEncryptor(oldKey)
	if err != nil {
		t.Fatal(err)
	}
	dek, _ := crypto.GenerateDEK()
	wrapped, err := oldEnc.WrapDEK(dek)
	if err != nil || !oldEnc.IsCurrent(wrapped) {
		t.Fatalf("wrap: %v", err)
	}

	rotating, err := crypto.NewEnvelopeEncryptor(newKey, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	if rotating.KeyID() == oldEnc.KeyID() || len(rotating.KeyID()) != 16 {
		t.Errorf("key ids: %s %s", rotating.KeyID(), oldEnc.KeyID())
	}
	if rotating.IsCurrent(wrapped) {
		t.Error("a DEK of the previous key counts as current")
	}
	if got, unwrapErr := rotating.UnwrapDEK(wrapped); unwrapErr != nil || string(got) != string(dek) {
		t.Fatalf("unwrap with the previous key: %v", unwrapErr)
	}
	rewrapped, err := rotating.Rewrap(wrapped)
	if err != nil || !rotating.IsCurrent(rewrapped) {
		t.Fatalf("rewrap: %v", err)
	}

	retired, _ := crypto.NewEnvelopeEncryptor(newKey)
	if got, unwrapErr := retired.UnwrapDEK(rewrapped); unwrapErr != nil || string(got) != string(dek) {
		t.Errorf("rewrapped DEK after retiring the previous key: %v", unwrapErr)
	}
	if _, unwrapErr := retired.UnwrapDEK(wrapped); unwrapErr == nil {
		t.Error("a DEK of the retired key was unwrapped")
	}

	// An unmarked DEK from before the key ids.
	legacy, err := crypto.WrapUnmarked(oldEnc, dek)
	if err != nil {
		t.Fatal(err)
	}
	if rotating.IsCurrent(legacy) {
		t.Error("an unmarked DEK counts as current")
	}
	if got, err := rotating.UnwrapDEK(legacy); err != nil || string(got) != string(dek) {
		t.Errorf("unmarked DEK: %v", err)
	}
	if _, err := crypto.NewEnvelopeEncryptor(newKey, "not base64!"); err == nil {
		t.Error("an invalid previous key was accepted")
	}
}

// derivedSecrets are the secrets the server derives at its start: the
// pagination cursor key and the keys of the collector NATS endpoint.
type derivedSecrets struct {
	cursor []byte
	edge   *discovery.EdgeSecrets
}

func deriveSecrets(t *testing.T, enc *crypto.EnvelopeEncryptor) derivedSecrets {
	t.Helper()
	edge, err := discovery.DeriveEdgeSecrets(enc)
	if err != nil {
		t.Fatal(err)
	}
	return derivedSecrets{cursor: enc.Derive("reticora/pagination-cursor/v1"), edge: edge}
}

// TestDerivedSecretsStableAcrossRotation covers SEC-06 (WP-459, F-158): the
// derived secrets come from the derivation root, not from the master key.
// The cursor key (Derive("reticora/pagination-cursor/v1")) and
// discovery.DeriveEdgeSecrets are the same with the old master key, during
// the rotation (new key, old one previous) and, once the root is rewrapped,
// with the new key alone; a cursor issued before the rotation verifies
// afterwards (no 400). After the rewrap the old key alone no longer unseals
// the root; a tampered root fails with ErrDerivationRoot, another root does
// not replace the open one, the same master key with another root derives
// other secrets, and deriving without a root panics.
func TestDerivedSecretsStableAcrossRotation(t *testing.T) {
	oldKey, _ := crypto.GenerateMasterKey()
	newKey, _ := crypto.GenerateMasterKey()
	oldEnc, err := crypto.NewEnvelopeEncryptor(oldKey)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := oldEnc.NewDerivationRoot()
	if err != nil || !oldEnc.IsCurrent(sealed) {
		t.Fatalf("new derivation root: %v", err)
	}
	if err = oldEnc.OpenDerivationRoot(sealed); err != nil {
		t.Fatal(err)
	}
	before := deriveSecrets(t, oldEnc)
	if bytes.Equal(before.cursor, oldEnc.Derive("reticora/nats-edge/issuer")) || before.edge.Password == before.edge.SysPassword {
		t.Fatal("two purposes share a derived secret")
	}
	if err = api.SetCursorKey(before.cursor); err != nil {
		t.Fatal(err)
	}
	cursor := api.EncodeCursor(api.Cursor{Sort: "name", Dir: "asc", Value: "core-sw-01", ID: "0b5e7c1a-2f3d-4e5f-8a9b-0c1d2e3f4a5b"})

	// The rotation: the new key, the old one previous; the rewrap run
	// reseals the root with the new key, then the old key is retired.
	rotating, err := crypto.NewEnvelopeEncryptor(newKey, oldKey)
	if err != nil {
		t.Fatal(err)
	}
	if err = rotating.OpenDerivationRoot(sealed); err != nil {
		t.Fatalf("root sealed with the previous key: %v", err)
	}
	rewrapped, err := rotating.Rewrap(sealed)
	if err != nil || !rotating.IsCurrent(rewrapped) {
		t.Fatalf("rewrap the root: %v", err)
	}
	retired, err := crypto.NewEnvelopeEncryptor(newKey)
	if err != nil {
		t.Fatal(err)
	}
	if err = retired.OpenDerivationRoot(rewrapped); err != nil {
		t.Fatalf("rewrapped root with the new key alone: %v", err)
	}
	for _, step := range []struct {
		name string
		enc  *crypto.EnvelopeEncryptor
	}{{"rotating (new, old)", rotating}, {"retired (new)", retired}} {
		got := deriveSecrets(t, step.enc)
		cursorStable := bytes.Equal(got.cursor, before.cursor)
		issuerStable := got.edge.IssuerSeed == before.edge.IssuerSeed && got.edge.IssuerKey == before.edge.IssuerKey
		passwordsStable := got.edge.Password == before.edge.Password && got.edge.SysPassword == before.edge.SysPassword
		if !cursorStable || !issuerStable || !passwordsStable {
			t.Errorf("%s: cursor key stable: %t, issuer stable: %t, passwords stable: %t",
				step.name, cursorStable, issuerStable, passwordsStable)
		}
		// The restarted server signs and verifies cursors with its key.
		if err = api.SetCursorKey(got.cursor); err != nil {
			t.Fatal(err)
		}
		if c, derr := api.DecodeCursor(cursor); derr != nil || c.Value != "core-sw-01" {
			t.Errorf("%s: a cursor issued before the rotation: %v", step.name, derr)
		}
	}

	// After the rewrap the old key is no longer needed and no longer enough.
	oldOnly, _ := crypto.NewEnvelopeEncryptor(oldKey)
	if err = oldOnly.OpenDerivationRoot(rewrapped); !errors.Is(err, crypto.ErrDerivationRoot) {
		t.Errorf("the rewrapped root with the retired key alone: %v, want ErrDerivationRoot", err)
	}
	tampered := bytes.Clone(rewrapped)
	tampered[len(tampered)-1] ^= 0x01
	fresh, _ := crypto.NewEnvelopeEncryptor(newKey)
	if err = fresh.OpenDerivationRoot(tampered); !errors.Is(err, crypto.ErrDerivationRoot) {
		t.Errorf("tampered root: %v, want ErrDerivationRoot", err)
	}
	if err = retired.OpenDerivationRoot(rewrapped); err != nil {
		t.Errorf("opening the open root again: %v", err)
	}
	other, err := retired.NewDerivationRoot()
	if err != nil {
		t.Fatal(err)
	}
	if err = retired.OpenDerivationRoot(other); err == nil || !bytes.Equal(retired.Derive("reticora/pagination-cursor/v1"), before.cursor) {
		t.Errorf("another root replaced the open one: %v", err)
	}
	// The secrets come from the root: the same master key with another
	// root derives others.
	sameKey, _ := crypto.NewEnvelopeEncryptor(oldKey)
	if err = sameKey.UseEphemeralDerivationRoot(); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(sameKey.Derive("reticora/pagination-cursor/v1"), before.cursor) {
		t.Error("the derived secret follows the master key, not the root")
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("Derive without a derivation root did not panic")
			}
		}()
		fresh.Derive("reticora/pagination-cursor/v1")
	}()
}
