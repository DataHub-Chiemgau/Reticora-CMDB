package crypto

import (
	"encoding/base64"
	"testing"
)

func testMasterKey(t *testing.T) string {
	t.Helper()
	key := make([]byte, KeySize)
	for i := range key {
		key[i] = byte(i)
	}
	return base64.StdEncoding.EncodeToString(key)
}

func TestNewEnvelopeEncryptor(t *testing.T) {
	t.Run("valid key", func(t *testing.T) {
		enc, err := NewEnvelopeEncryptor(testMasterKey(t))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if enc == nil {
			t.Fatal("expected non-nil encryptor")
		}
	})

	t.Run("empty key", func(t *testing.T) {
		_, err := NewEnvelopeEncryptor("")
		if err != ErrMasterKeyRequired {
			t.Fatalf("expected ErrMasterKeyRequired, got %v", err)
		}
	})

	t.Run("invalid base64", func(t *testing.T) {
		_, err := NewEnvelopeEncryptor("not-valid-base64!!!")
		if err == nil {
			t.Fatal("expected error for invalid base64")
		}
	})

	t.Run("wrong key size", func(t *testing.T) {
		shortKey := base64.StdEncoding.EncodeToString([]byte("tooshort"))
		_, err := NewEnvelopeEncryptor(shortKey)
		if err != ErrInvalidKeySize {
			t.Fatalf("expected ErrInvalidKeySize, got %v", err)
		}
	})
}

func TestGenerateDEK(t *testing.T) {
	dek, err := GenerateDEK()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dek) != KeySize {
		t.Fatalf("expected %d bytes, got %d", KeySize, len(dek))
	}

	// Two generated DEKs should differ
	dek2, _ := GenerateDEK()
	if string(dek) == string(dek2) {
		t.Fatal("two generated DEKs should not be identical")
	}
}

func TestWrapUnwrapDEK(t *testing.T) {
	enc, err := NewEnvelopeEncryptor(testMasterKey(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dek, err := GenerateDEK()
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
	enc, _ := NewEnvelopeEncryptor(testMasterKey(t))
	_, err := enc.WrapDEK([]byte("short"))
	if err != ErrInvalidKeySize {
		t.Fatalf("expected ErrInvalidKeySize, got %v", err)
	}
}

func TestUnwrapDEK_Tampered(t *testing.T) {
	enc, _ := NewEnvelopeEncryptor(testMasterKey(t))
	dek, _ := GenerateDEK()
	wrapped, _ := enc.WrapDEK(dek)

	// Tamper with wrapped data
	wrapped[len(wrapped)-1] ^= 0xFF

	_, err := enc.UnwrapDEK(wrapped)
	if err != ErrDecryptionFailed {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

func TestEncryptDecrypt(t *testing.T) {
	dek, _ := GenerateDEK()
	plaintext := []byte(`{"username":"admin","password":"s3cr3t"}`)

	ciphertext, err := Encrypt(dek, plaintext)
	if err != nil {
		t.Fatalf("Encrypt error: %v", err)
	}

	if string(ciphertext) == string(plaintext) {
		t.Fatal("ciphertext should not equal plaintext")
	}

	decrypted, err := Decrypt(dek, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt error: %v", err)
	}

	if string(decrypted) != string(plaintext) {
		t.Fatalf("decrypted does not match: got %q", decrypted)
	}
}

func TestDecrypt_Tampered(t *testing.T) {
	dek, _ := GenerateDEK()
	plaintext := []byte("hello world")

	ciphertext, _ := Encrypt(dek, plaintext)
	ciphertext[len(ciphertext)-1] ^= 0xFF

	_, err := Decrypt(dek, ciphertext)
	if err != ErrDecryptionFailed {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

func TestDecrypt_WrongKey(t *testing.T) {
	dek1, _ := GenerateDEK()
	dek2, _ := GenerateDEK()
	plaintext := []byte("secret data")

	ciphertext, _ := Encrypt(dek1, plaintext)
	_, err := Decrypt(dek2, ciphertext)
	if err != ErrDecryptionFailed {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

func TestEncrypt_EmptyPlaintext(t *testing.T) {
	dek, _ := GenerateDEK()
	ciphertext, err := Encrypt(dek, []byte{})
	if err != nil {
		t.Fatalf("Encrypt error: %v", err)
	}

	decrypted, err := Decrypt(dek, ciphertext)
	if err != nil {
		t.Fatalf("Decrypt error: %v", err)
	}
	if len(decrypted) != 0 {
		t.Fatalf("expected empty, got %d bytes", len(decrypted))
	}
}
