package credential

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
)

func testEncryptor(t *testing.T) *crypto.EnvelopeEncryptor {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	enc, err := crypto.NewEnvelopeEncryptor(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatalf("failed to create encryptor: %v", err)
	}
	return enc
}

func TestService_CreateAndDecrypt(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo, testEncryptor(t))
	ctx := context.Background()

	req := CreateRequest{
		OrganizationID: "org-1",
		Name:           "switch-creds",
		Kind:           KindSNMPv2c,
		Secret:         map[string]any{"community": "public"},
	}

	cred, err := svc.Create(ctx, req)
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if cred.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if cred.Name != "switch-creds" {
		t.Fatalf("expected name 'switch-creds', got %q", cred.Name)
	}
	if cred.Kind != KindSNMPv2c {
		t.Fatalf("expected kind %q, got %q", KindSNMPv2c, cred.Kind)
	}

	// Decrypt should return original secret
	secret, err := svc.Decrypt(ctx, "org-1", cred.ID)
	if err != nil {
		t.Fatalf("Decrypt error: %v", err)
	}
	if secret["community"] != "public" {
		t.Fatalf("expected community 'public', got %v", secret["community"])
	}
}

func TestService_CreateInvalidKind(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo, testEncryptor(t))
	ctx := context.Background()

	_, err := svc.Create(ctx, CreateRequest{
		OrganizationID: "org-1",
		Name:           "bad",
		Kind:           "invalid_kind",
		Secret:         map[string]any{"key": "value"},
	})
	if err != ErrInvalidKind {
		t.Fatalf("expected ErrInvalidKind, got %v", err)
	}
}

func TestService_List(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo, testEncryptor(t))
	ctx := context.Background()

	// Create two creds for org-1 and one for org-2
	svc.Create(ctx, CreateRequest{OrganizationID: "org-1", Name: "cred1", Kind: KindSSHPassword, Secret: map[string]any{"pass": "x"}})
	svc.Create(ctx, CreateRequest{OrganizationID: "org-1", Name: "cred2", Kind: KindSSHKey, Secret: map[string]any{"key": "y"}})
	svc.Create(ctx, CreateRequest{OrganizationID: "org-2", Name: "cred3", Kind: KindRedfish, Secret: map[string]any{"token": "z"}})

	list, err := svc.List(ctx, "org-1")
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 credentials for org-1, got %d", len(list))
	}
}

func TestService_Delete(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo, testEncryptor(t))
	ctx := context.Background()

	cred, _ := svc.Create(ctx, CreateRequest{
		OrganizationID: "org-1",
		Name:           "to-delete",
		Kind:           KindAPIToken,
		Secret:         map[string]any{"token": "abc123"},
	})

	err := svc.Delete(ctx, "org-1", cred.ID)
	if err != nil {
		t.Fatalf("Delete error: %v", err)
	}

	_, err = svc.Get(ctx, "org-1", cred.ID)
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestService_DEKReuse(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo, testEncryptor(t))
	ctx := context.Background()

	// Create two credentials for same org — should reuse DEK
	svc.Create(ctx, CreateRequest{OrganizationID: "org-1", Name: "a", Kind: KindSNMPv2c, Secret: map[string]any{"c": "1"}})
	svc.Create(ctx, CreateRequest{OrganizationID: "org-1", Name: "b", Kind: KindSNMPv2c, Secret: map[string]any{"c": "2"}})

	// Only one DEK should exist for org-1
	dek, err := repo.GetOrgDEK(ctx, "org-1")
	if err != nil {
		t.Fatalf("GetOrgDEK error: %v", err)
	}
	if dek == nil {
		t.Fatal("expected DEK to exist")
	}
}

func TestService_CrossOrgIsolation(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo, testEncryptor(t))
	ctx := context.Background()

	cred, _ := svc.Create(ctx, CreateRequest{
		OrganizationID: "org-1",
		Name:           "isolated",
		Kind:           KindIPMI,
		Secret:         map[string]any{"user": "admin", "pass": "secret"},
	})

	// Attempt to access from different org should fail
	_, err := svc.Get(ctx, "org-2", cred.ID)
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound for cross-org access, got %v", err)
	}

	_, err = svc.Decrypt(ctx, "org-2", cred.ID)
	if err == nil {
		t.Fatal("expected error for cross-org decrypt")
	}
}

func TestService_RotateKeysKeepsSecretsDecryptable(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo, testEncryptor(t))
	ctx := context.Background()

	first, err := svc.Create(ctx, CreateRequest{
		OrganizationID: "org-1",
		Name:           "switch",
		Kind:           KindSNMPv3,
		Secret:         map[string]any{"user": "monitor", "authKey": "k1"},
	})
	if err != nil {
		t.Fatalf("Create first credential: %v", err)
	}
	second, err := svc.Create(ctx, CreateRequest{
		OrganizationID: "org-1",
		Name:           "server",
		Kind:           KindRedfish,
		Secret:         map[string]any{"user": "root", "pass": "p4ss"},
	})
	if err != nil {
		t.Fatalf("Create second credential: %v", err)
	}

	before, err := repo.GetOrgDEK(ctx, "org-1")
	if err != nil {
		t.Fatalf("GetOrgDEK before rotation: %v", err)
	}

	result, err := svc.RotateKeys(ctx, "org-1")
	if err != nil {
		t.Fatalf("RotateKeys: %v", err)
	}
	if result.Rotated != 2 {
		t.Fatalf("expected 2 rotated credentials, got %d", result.Rotated)
	}
	if result.KeyVersion <= before.KeyVersion {
		t.Fatalf("expected key version > %d, got %d", before.KeyVersion, result.KeyVersion)
	}

	// Every credential must still decrypt to its original secret.
	for _, tc := range []struct {
		id   string
		want map[string]any
	}{
		{first.ID, map[string]any{"user": "monitor", "authKey": "k1"}},
		{second.ID, map[string]any{"user": "root", "pass": "p4ss"}},
	} {
		secret, err := svc.Decrypt(ctx, "org-1", tc.id)
		if err != nil {
			t.Fatalf("Decrypt after rotation: %v", err)
		}
		for k, v := range tc.want {
			if secret[k] != v {
				t.Fatalf("secret[%s] = %v, want %v", k, secret[k], v)
			}
		}
	}

	// The DEK itself must have changed.
	after, err := repo.GetOrgDEK(ctx, "org-1")
	if err != nil {
		t.Fatalf("GetOrgDEK after rotation: %v", err)
	}
	if after.KeyVersion != result.KeyVersion {
		t.Fatalf("stored key version = %d, want %d", after.KeyVersion, result.KeyVersion)
	}
	if string(after.EncryptedDEK) == string(before.EncryptedDEK) {
		t.Fatal("wrapped DEK did not change after rotation")
	}
	if after.KeyVersion != before.KeyVersion+1 {
		t.Fatalf("expected stored key version %d, got %d", before.KeyVersion+1, after.KeyVersion)
	}
}

func TestService_RotateKeysWithoutCredentials(t *testing.T) {
	repo := NewMemoryRepository()
	svc := NewService(repo, testEncryptor(t))
	ctx := context.Background()

	// Materialize a DEK by creating and deleting a credential.
	cred, err := svc.Create(ctx, CreateRequest{
		OrganizationID: "org-1",
		Name:           "temp",
		Kind:           KindNUT,
		Secret:         map[string]any{"pass": "x"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.Delete(ctx, "org-1", cred.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	result, err := svc.RotateKeys(ctx, "org-1")
	if err != nil {
		t.Fatalf("RotateKeys with zero credentials: %v", err)
	}
	if result.Rotated != 0 {
		t.Fatalf("expected 0 rotated credentials, got %d", result.Rotated)
	}
}
