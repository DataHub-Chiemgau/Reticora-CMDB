package credential

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
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
		secret, decryptErr := svc.Decrypt(ctx, "org-1", tc.id)
		if decryptErr != nil {
			t.Fatalf("Decrypt after rotation: %v", decryptErr)
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
	if deleteErr := svc.Delete(ctx, "org-1", cred.ID); deleteErr != nil {
		t.Fatalf("Delete: %v", deleteErr)
	}

	result, err := svc.RotateKeys(ctx, "org-1")
	if err != nil {
		t.Fatalf("RotateKeys with zero credentials: %v", err)
	}
	if result.Rotated != 0 {
		t.Fatalf("expected 0 rotated credentials, got %d", result.Rotated)
	}
}

// TestCredentialKindsMatchTheCollector covers WP-378 (COL-02, F-028): the
// contract file of the collector lists exactly the kinds the server accepts,
// each once. The collector test TestCredentialKindsContract checks that it
// files each kind under its namespace and hands it to the listed plugins, so
// a kind added on one side only fails here or there.
func TestCredentialKindsMatchTheCollector(t *testing.T) {
	raw, err := os.ReadFile("../../../collector/collectorcmd/testdata/credential-kinds.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Kinds []struct {
			Kind string `json:"kind"`
		} `json:"kinds"`
	}
	if err = json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, k := range contract.Kinds {
		if listed[k.Kind] {
			t.Errorf("credential-kinds.json lists %q more than once", k.Kind)
		}
		listed[k.Kind] = true
		if !validKinds[k.Kind] {
			t.Errorf("credential-kinds.json lists %q, which the server does not accept", k.Kind)
		}
	}
	for kind := range validKinds {
		if !listed[kind] {
			t.Errorf("credential kind %q is missing in credential-kinds.json: the collector does not route it", kind)
		}
	}
}

// flakyDEKRepository fails GetOrgDEK from the given call on, as a store
// that becomes unavailable during a rotation would.
type flakyDEKRepository struct {
	*MemoryRepository
	calls, failFrom int
}

func (f *flakyDEKRepository) GetOrgDEK(ctx context.Context, orgID string) (*OrgDEK, error) {
	f.calls++
	if f.calls >= f.failFrom {
		return nil, errors.New("store unavailable")
	}
	return f.MemoryRepository.GetOrgDEK(ctx, orgID)
}

// TestService_RotateKeysFailsWhenKeyVersionUnreadable covers F-123 (govet
// shadow in RotateKeys): an error reading the current key version fails the
// rotation instead of restarting the version at 1, and no credential is
// rewritten.
func TestService_RotateKeysFailsWhenKeyVersionUnreadable(t *testing.T) {
	mem := NewMemoryRepository()
	ctx := context.Background()
	cred, err := NewService(mem, testEncryptor(t)).Create(ctx, CreateRequest{
		OrganizationID: "org-1",
		Name:           "switch",
		Kind:           KindNUT,
		Secret:         map[string]any{"pass": "x"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	before, err := mem.Get(ctx, "org-1", cred.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	beforeVersion, beforeCiphertext := before.KeyVersion, string(before.Ciphertext)

	// The first read (the DEK to decrypt with) succeeds, the second (the
	// key version) fails.
	repo := &flakyDEKRepository{MemoryRepository: mem, failFrom: 2}
	if _, rotateErr := NewService(repo, testEncryptor(t)).RotateKeys(ctx, "org-1"); rotateErr == nil {
		t.Fatal("RotateKeys succeeded although the key version could not be read")
	}
	after, err := mem.Get(ctx, "org-1", cred.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if after.KeyVersion != beforeVersion || string(after.Ciphertext) != beforeCiphertext {
		t.Fatalf("credential rewritten by a failed rotation: key version %d -> %d", beforeVersion, after.KeyVersion)
	}
}

// TestService_ReplaceSecretDecryptsNewValue covers WP-889: after the
// replacement Decrypt returns the new value, also after RotateKeys.
func TestService_ReplaceSecretDecryptsNewValue(t *testing.T) {
	svc := NewService(NewMemoryRepository(), testEncryptor(t))
	ctx := context.Background()
	cred, err := svc.Create(ctx, CreateRequest{OrganizationID: "org-1", Name: "n", Kind: KindSSHPassword,
		Secret: map[string]any{"password": "old"}})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	replaced, err := svc.ReplaceSecret(ctx, "org-1", cred.ID, map[string]any{"password": "new"},
		func(_ context.Context, c *Credential) error {
			calls++
			if c.SecretVersion != 2 {
				t.Errorf("in-transaction step sees secret_version %d, want 2", c.SecretVersion)
			}
			return nil
		})
	if err != nil || replaced.SecretVersion != 2 || calls != 1 {
		t.Fatalf("replace: %+v, %v, %d calls", replaced, err, calls)
	}
	for _, step := range []string{"replace", "rotate keys"} {
		secret, decErr := svc.Decrypt(ctx, "org-1", cred.ID)
		if decErr != nil || secret["password"] != "new" {
			t.Errorf("after %s: %v, %v", step, secret, decErr)
		}
		if _, err = svc.RotateKeys(ctx, "org-1"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = svc.ReplaceSecret(ctx, "org-2", cred.ID, map[string]any{"password": "x"}, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("foreign organization: %v", err)
	}
}

// TestVaultLoginOnlyThroughConnection covers the vault logins of WP-893 in
// the memory store: a bundle never carries a login listed as a credential of
// the collector's scopes, and the login of a connection is not deleted
// (ErrCredentialIsVaultLogin, 409) while an unused one is.
func TestVaultLoginOnlyThroughConnection(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryRepository(), testEncryptor(t))
	mk := func(name, kind string, secret map[string]any) string {
		t.Helper()
		c, err := svc.Create(ctx, CreateRequest{OrganizationID: "org-1", Name: name, Kind: kind, Secret: secret})
		if err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	approle := mk("approle", KindVaultAppRole, map[string]any{"role_id": "role", "secret_id": "secret"})
	token := mk("token", KindVaultToken, map[string]any{"token": "tok"})
	ssh := mk("ssh", KindSSHPassword, map[string]any{"username": "u", "password": "pw"})

	key := newCollectorKey(t)
	_, ids, err := svc.Bundle(ctx, &BundleTarget{OrganizationID: "org-1", CollectorID: "col-1", PublicKey: &key.PublicKey,
		CredentialIDs: []string{approle, token, ssh}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != ssh {
		t.Errorf("bundle with listed vault logins delivered %v, want only %s", ids, ssh)
	}

	if _, err = svc.Vaults().Create(ctx, "org-1", &VaultConnection{Name: "bao", Kind: "openbao_kv2",
		Address: "https://vault.example.com", Mount: "secret", AuthMethod: "approle", AuthCredentialID: approle,
		CollectorIDs: []string{"00000000-0000-4000-8000-0000000000c1"}}); err != nil {
		t.Fatal(err)
	}
	if err = svc.Delete(ctx, "org-1", approle); !errors.Is(err, ErrCredentialIsVaultLogin) {
		t.Errorf("delete the login of a connection: %v, want ErrCredentialIsVaultLogin", err)
	}
	if _, err = svc.Get(ctx, "org-1", approle); err != nil {
		t.Errorf("the login is gone after a refused delete: %v", err)
	}
	if err = svc.Delete(ctx, "org-1", token); err != nil {
		t.Errorf("delete an unused login: %v", err)
	}
}
