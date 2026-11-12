package credential

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
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

// vaultRefService is a memory service with an org-wide token login and a
// connection of each place of resolution (WP-894).
func vaultRefService(t *testing.T) (svc *Service, collectorConn, serverConn string) {
	t.Helper()
	svc = NewService(NewMemoryRepository(), testEncryptor(t))
	ctx := context.Background()
	login, err := svc.Create(ctx, CreateRequest{OrganizationID: "org-1", Name: "login", Kind: KindVaultToken,
		Secret: map[string]any{"token": "t"}})
	if err != nil {
		t.Fatal(err)
	}
	conn := func(name, resolveAt string, collectors []string) string {
		c, cerr := svc.Vaults().Create(ctx, "org-1", &VaultConnection{Name: name, Kind: VaultKindOpenBao,
			Address: "https://vault.example.com", Mount: "kv", AuthMethod: VaultAuthToken, AuthCredentialID: login.ID,
			ResolveAt: resolveAt, Enabled: true, CollectorIDs: collectors})
		if cerr != nil {
			t.Fatalf("connection %s: %v", name, cerr)
		}
		return c.ID
	}
	return svc, conn("edge", ResolveAtCollector, []string{"00000000-0000-4000-8000-000000000001"}), conn("server", ResolveAtServer, nil)
}

func fieldOf(err error) string {
	var fe *api.FieldError
	if errors.As(err, &fe) {
		return fe.Field
	}
	return ""
}

// TestService_CreateRequiresExactlyOneSource covers AK2 and AK3 of WP-894:
// secret or vault_ref, both or none is 422; the secret of a reference is
// not replaced; a change of the reference raises secret_version.
func TestService_CreateRequiresExactlyOneSource(t *testing.T) {
	svc, conn, _ := vaultRefService(t)
	ctx := context.Background()
	ref := &VaultRef{ConnectionID: conn, Path: "/net/switch/", FieldMap: map[string]string{"community": "snmp_ro"}}
	base := CreateRequest{OrganizationID: "org-1", Name: "sw", Kind: KindSNMPv2c}
	both := base
	both.Secret, both.VaultRef = map[string]any{"community": "x"}, ref
	if _, err := svc.Create(ctx, both); fieldOf(err) != "vault_ref" {
		t.Errorf("both: %v", err)
	}
	if _, err := svc.Create(ctx, base); fieldOf(err) != "secret" {
		t.Errorf("none: %v", err)
	}
	withRef := base
	withRef.VaultRef = ref
	cred, err := svc.Create(ctx, withRef)
	if err != nil || cred.Source != SourceVaultRef || cred.VaultRef == nil || cred.VaultRef.Path != "net/switch" {
		t.Fatalf("vault_ref: %+v %v", cred, err)
	}
	withSecret := base
	withSecret.Secret = map[string]any{"community": "x"}
	if stored, serr := svc.Create(ctx, withSecret); serr != nil || stored.Source != SourceStored {
		t.Errorf("secret: %+v %v", stored, serr)
	}
	if _, err = svc.ReplaceSecret(ctx, "org-1", cred.ID, map[string]any{"community": "y"}, nil); !errors.Is(err, ErrVaultRefHasNoSecret) {
		t.Errorf("replace the secret of a reference: %v", err)
	}
	path := "net/switch-2"
	updated, err := svc.UpdateMetadata(ctx, "org-1", cred.ID, MetadataUpdate{Ref: VaultRefUpdate{Path: &path}})
	if err != nil || updated.SecretVersion != 2 || updated.VaultRef.Path != path {
		t.Errorf("update the reference: %+v %v", updated, err)
	}
	if _, err = svc.UpdateMetadata(ctx, "org-1", cred.ID, MetadataUpdate{Ref: VaultRefUpdate{FieldMap: map[string]string{}}}); fieldOf(err) != "field_map" {
		t.Errorf("update with an empty field map: %v", err)
	}
}

// TestService_FieldMapPerKind covers AK2: field_map maps every required
// field of the kind and nothing else; the vault logins are only stored; the
// connection must exist.
func TestService_FieldMapPerKind(t *testing.T) {
	svc, conn, _ := vaultRefService(t)
	ctx := context.Background()
	create := func(kind string, fieldMap map[string]string, connection string) error {
		_, err := svc.Create(ctx, CreateRequest{OrganizationID: "org-1", Name: "ref-" + kind, Kind: kind,
			VaultRef: &VaultRef{ConnectionID: connection, Path: "p", FieldMap: fieldMap}})
		return err
	}
	for _, tc := range []struct {
		kind     string
		fieldMap map[string]string
		field    string
	}{
		{KindSNMPv2c, map[string]string{"community": "snmp_ro"}, ""},
		{KindSNMPv2c, map[string]string{}, "field_map"},
		{KindSNMPv2c, map[string]string{"community": "c", "extra": "x"}, "field_map"},
		{KindSSHPassword, map[string]string{"username": "user"}, "field_map"},
		{KindSSHPassword, map[string]string{"username": "user", "password": "pw"}, ""},
		{KindSSHKey, map[string]string{"username": "user", "private_key": "key"}, ""},
		// The probe kinds map one variant of their fields (WP-967).
		{KindHTTP, map[string]string{"token": "t"}, ""},
		{KindHTTP, map[string]string{"username": "u", "password": "p"}, ""},
		{KindHTTP, map[string]string{"username": "u", "password": "p", "token": "t"}, "field_map"},
		{KindACS, map[string]string{"username": "u"}, "field_map"},
		{KindGNMI, map[string]string{"client_cert": "c", "client_key": "k", "ca": "ca"}, ""},
		{KindGNMI, map[string]string{"username": "u", "password": "p", "client_cert": "c", "client_key": "k"}, ""},
		{KindGNMI, map[string]string{"ca": "ca"}, "field_map"},
		{KindVaultToken, map[string]string{"token": "t"}, "vault_ref"},
		{KindVaultAppRole, map[string]string{"role_id": "r", "secret_id": "s"}, "vault_ref"},
	} {
		if err := create(tc.kind, tc.fieldMap, conn); fieldOf(err) != tc.field || (tc.field == "" && err != nil) {
			t.Errorf("%s %v: %v, want field %q", tc.kind, tc.fieldMap, err, tc.field)
		}
	}
	for kind := range validKinds {
		if _, login := vaultLoginFields[kind]; !login && len(KindFields[kind]) == 0 {
			t.Errorf("kind %s has no required fields for field_map", kind)
		}
	}
	if err := create(KindSNMPv2c, map[string]string{"community": "c"}, "00000000-0000-4000-8000-0000000000ff"); fieldOf(err) != "vault_ref.connection_id" {
		t.Errorf("unknown connection: %v", err)
	}
}

// TestService_DecryptVaultRefOnServerFails covers AK4: the server decrypts
// no reference; collector references say so, server references wait for
// the resolver of WP-896.
func TestService_DecryptVaultRefOnServerFails(t *testing.T) {
	svc, edge, server := vaultRefService(t)
	ctx := context.Background()
	for conn, want := range map[string]error{edge: ErrResolvedOnCollector, server: ErrVaultNotConfigured} {
		cred, err := svc.Create(ctx, CreateRequest{OrganizationID: "org-1", Name: "ref-" + conn, Kind: KindAPIToken,
			VaultRef: &VaultRef{ConnectionID: conn, Path: "api/token", FieldMap: map[string]string{"token": "value"}}})
		if err != nil {
			t.Fatal(err)
		}
		if secret, derr := svc.Decrypt(ctx, "org-1", cred.ID); !errors.Is(derr, want) || secret != nil {
			t.Errorf("decrypt a reference of %s: %v %v, want %v", conn, secret, derr, want)
		}
	}
}

// TestService_RotateKeysSkipsVaultRefs covers AK6: the rotation re-encrypts
// the stored secrets only and counts them.
func TestService_RotateKeysSkipsVaultRefs(t *testing.T) {
	svc, conn, _ := vaultRefService(t)
	ctx := context.Background()
	if _, err := svc.Create(ctx, CreateRequest{OrganizationID: "org-1", Name: "ref", Kind: KindSNMPv2c,
		VaultRef: &VaultRef{ConnectionID: conn, Path: "p", FieldMap: map[string]string{"community": "c"}}}); err != nil {
		t.Fatal(err)
	}
	stored, err := svc.Create(ctx, CreateRequest{OrganizationID: "org-1", Name: "stored", Kind: KindSNMPv2c,
		Secret: map[string]any{"community": "public"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := svc.RotateKeys(ctx, "org-1")
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	// The token login and the stored credential.
	if result.Rotated != 2 {
		t.Errorf("rotated %d, want 2", result.Rotated)
	}
	if secret, derr := svc.Decrypt(ctx, "org-1", stored.ID); derr != nil || secret["community"] != "public" {
		t.Errorf("decrypt after the rotation: %v %v", secret, derr)
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

// testPEMPair returns a self-signed certificate and its PKCS#8 key as PEM.
func testPEMPair(t *testing.T) (certPEM, keyPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "gnmi-client"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
}

// TestProbeKindsRequiredFields covers WP-967 AK1 and AK2 (PRT-22, COL-02):
// http and acs carry username and password or token, not both; gnmi at
// least one of the pairs username/password and client_cert/client_key,
// optionally ca, with parseable PEM. A violation is 422 secret.<member> on
// Create and on the replacement of the secret; the message never holds a
// value.
func TestProbeKindsRequiredFields(t *testing.T) {
	cert, key := testPEMPair(t)
	const secretValue = "s3cr3t-value-never-echoed"
	cases := []struct {
		name   string
		kind   string
		secret map[string]any
		field  string // "" = valid
	}{
		{"http user/password", KindHTTP, map[string]any{"username": "u", "password": secretValue}, ""},
		{"http token", KindHTTP, map[string]any{"token": secretValue}, ""},
		{"http nothing", KindHTTP, map[string]any{}, "secret.username"},
		{"http username only", KindHTTP, map[string]any{"username": "u"}, "secret.password"},
		{"http password only", KindHTTP, map[string]any{"password": secretValue}, "secret.username"},
		{"http both variants", KindHTTP, map[string]any{"username": "u", "password": secretValue, "token": secretValue}, "secret.token"},
		{"http token not a string", KindHTTP, map[string]any{"token": 42}, "secret.token"},
		{"acs user/password", KindACS, map[string]any{"username": "u", "password": secretValue}, ""},
		{"acs token", KindACS, map[string]any{"token": secretValue}, ""},
		{"acs both variants", KindACS, map[string]any{"username": "u", "password": secretValue, "token": secretValue}, "secret.token"},
		{"acs empty token", KindACS, map[string]any{"token": " "}, "secret.username"},
		{"gnmi user/password", KindGNMI, map[string]any{"username": "u", "password": secretValue}, ""},
		{"gnmi client cert", KindGNMI, map[string]any{"client_cert": cert, "client_key": key}, ""},
		{"gnmi both pairs and ca", KindGNMI, map[string]any{"username": "u", "password": secretValue, "client_cert": cert, "client_key": key, "ca": cert + cert}, ""},
		{"gnmi user/password and ca", KindGNMI, map[string]any{"username": "u", "password": secretValue, "ca": cert}, ""},
		{"gnmi nothing", KindGNMI, map[string]any{"ca": cert}, "secret.username"},
		{"gnmi cert without key", KindGNMI, map[string]any{"client_cert": cert}, "secret.client_key"},
		{"gnmi key without cert", KindGNMI, map[string]any{"client_key": key}, "secret.client_cert"},
		{"gnmi invalid cert PEM", KindGNMI, map[string]any{"client_cert": secretValue, "client_key": key}, "secret.client_cert"},
		{"gnmi key is a certificate", KindGNMI, map[string]any{"client_cert": cert, "client_key": cert}, "secret.client_key"},
		{"gnmi invalid key PEM", KindGNMI, map[string]any{"client_cert": cert, "client_key": "-----BEGIN PRIVATE KEY-----\n" + secretValue + "\n-----END PRIVATE KEY-----"}, "secret.client_key"},
		{"gnmi invalid ca PEM", KindGNMI, map[string]any{"username": "u", "password": secretValue, "ca": secretValue}, "secret.ca"},
	}
	ctx := context.Background()
	svc := NewService(NewMemoryRepository(), testEncryptor(t))
	for _, tc := range cases {
		_, err := svc.Create(ctx, CreateRequest{OrganizationID: "org-1", Name: tc.name, Kind: tc.kind, Secret: tc.secret})
		checkProbeKindError(t, "Create "+tc.name, err, tc.field, secretValue, cert, key)
	}
	// The replacement of the secret applies the same check.
	for _, kind := range []string{KindHTTP, KindGNMI, KindACS} {
		cred, err := svc.Create(ctx, CreateRequest{OrganizationID: "org-1", Name: "replace " + kind, Kind: kind,
			Secret: map[string]any{"username": "u", "password": "p"}})
		if err != nil {
			t.Fatalf("create %s: %v", kind, err)
		}
		for _, tc := range cases {
			if tc.kind != kind {
				continue
			}
			_, err := svc.ReplaceSecret(ctx, "org-1", cred.ID, tc.secret, nil)
			checkProbeKindError(t, "ReplaceSecret "+tc.name, err, tc.field, secretValue, cert, key)
		}
	}
}

func checkProbeKindError(t *testing.T, name string, err error, field string, secrets ...string) {
	t.Helper()
	if field == "" {
		if err != nil {
			t.Errorf("%s: %v, want valid", name, err)
		}
		return
	}
	var fe *api.FieldError
	if !errors.As(err, &fe) || fe.Field != field || fe.Status != 422 {
		t.Errorf("%s: %v, want 422 field %s", name, err, field)
		return
	}
	for _, s := range secrets {
		if strings.Contains(fe.Message, s) || strings.Contains(err.Error(), s) {
			t.Errorf("%s: the message %q echoes a value of the secret", name, fe.Message)
		}
	}
}
