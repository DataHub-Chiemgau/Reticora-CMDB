package keystore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sampleCreds() Credentials {
	return Credentials{
		DeviceID:             "edge-01",
		ClientCertificatePEM: "-----BEGIN CERTIFICATE-----\nfake\n-----END CERTIFICATE-----",
		ClientPrivateKeyPEM:  "-----BEGIN PRIVATE KEY-----\nfake\n-----END PRIVATE KEY-----",
		CertificateAuthority: "-----BEGIN CERTIFICATE-----\nca\n-----END CERTIFICATE-----",
		EnrollmentToken:      "token-123",
		Metadata:             map[string]string{"site": "munich"},
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "creds", "credentials.json")
	s := &FileStore{Path: path}
	ctx := context.Background()

	want := sampleCreds()
	if err := s.Save(ctx, want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := s.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.DeviceID != want.DeviceID {
		t.Errorf("DeviceID = %q, want %q", got.DeviceID, want.DeviceID)
	}
	if got.ClientCertificatePEM != want.ClientCertificatePEM {
		t.Errorf("ClientCertificatePEM mismatch")
	}
	if got.ClientPrivateKeyPEM != want.ClientPrivateKeyPEM {
		t.Errorf("ClientPrivateKeyPEM mismatch")
	}
	if got.CertificateAuthority != want.CertificateAuthority {
		t.Errorf("CertificateAuthority mismatch")
	}
	if got.EnrollmentToken != want.EnrollmentToken {
		t.Errorf("EnrollmentToken = %q, want %q", got.EnrollmentToken, want.EnrollmentToken)
	}
	if got.Metadata["site"] != "munich" {
		t.Errorf("Metadata[site] = %q, want %q", got.Metadata["site"], "munich")
	}
	if got.UpdatedAt.IsZero() {
		t.Error("expected UpdatedAt to be set by Save when zero")
	}
}

func TestSavePreservesExplicitUpdatedAt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	s := &FileStore{Path: path}

	ts := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	creds := sampleCreds()
	creds.UpdatedAt = ts

	if err := s.Save(context.Background(), creds); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := s.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.UpdatedAt.Equal(ts) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, ts)
	}
}

func TestSaveRestrictivePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	s := &FileStore{Path: path}

	if err := s.Save(context.Background(), sampleCreds()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file permissions = %o, want 600 (credentials must not be world-readable)", perm)
	}
}

func TestLoadNotFound(t *testing.T) {
	s := &FileStore{Path: filepath.Join(t.TempDir(), "missing.json")}

	_, err := s.Load(context.Background())
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Load on missing file = %v, want ErrNotFound", err)
	}
}

func TestLoadCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(path, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &FileStore{Path: path}

	_, err := s.Load(context.Background())
	if err == nil {
		t.Fatal("expected decode error for corrupt file, got nil")
	}
	if errors.Is(err, ErrNotFound) {
		t.Errorf("corrupt file must not report ErrNotFound, got: %v", err)
	}
}

func TestClearRemovesCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	s := &FileStore{Path: path}
	ctx := context.Background()

	if err := s.Save(ctx, sampleCreds()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.Clear(ctx); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file should be gone after Clear, stat err = %v", err)
	}
	if _, err := s.Load(ctx); !errors.Is(err, ErrNotFound) {
		t.Errorf("Load after Clear = %v, want ErrNotFound", err)
	}
}

func TestClearIsIdempotent(t *testing.T) {
	s := &FileStore{Path: filepath.Join(t.TempDir(), "never-existed.json")}

	if err := s.Clear(context.Background()); err != nil {
		t.Fatalf("Clear on missing file should be a no-op, got: %v", err)
	}
}

func TestSaveOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	s := &FileStore{Path: path}
	ctx := context.Background()

	if err := s.Save(ctx, sampleCreds()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rotated := sampleCreds()
	rotated.ClientCertificatePEM = "rotated-cert"
	if err := s.Save(ctx, rotated); err != nil {
		t.Fatalf("Save (overwrite): %v", err)
	}

	got, err := s.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.ClientCertificatePEM != "rotated-cert" {
		t.Errorf("ClientCertificatePEM = %q, want rotated value after overwrite", got.ClientCertificatePEM)
	}
}

func TestContextCancelled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	s := &FileStore{Path: path}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := s.Load(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Load with cancelled ctx = %v, want context.Canceled", err)
	}
	if err := s.Save(ctx, sampleCreds()); !errors.Is(err, context.Canceled) {
		t.Errorf("Save with cancelled ctx = %v, want context.Canceled", err)
	}
	if err := s.Clear(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Clear with cancelled ctx = %v, want context.Canceled", err)
	}
}

func TestFileStoreImplementsStore(t *testing.T) {
	// Compile-time check that FileStore satisfies the Store contract.
	var _ Store = (*FileStore)(nil)
}
