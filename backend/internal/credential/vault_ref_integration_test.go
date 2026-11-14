package credential_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
)

// Retest (WP-894, CRD-02):
//
//	cd backend && go test -count=1 -p 1 -run 'TestBundleCarries|TestServerResolved|TestRotateOrgDEKSkips|TestVaultConnectionDeleteWith|TestVaultRef' ./internal/credential/

// vaultRef creates a credential of kind snmp_v2c referencing the
// connection; clientID may be empty.
func (e *vaultEnv) vaultRef(name, connection, clientID string) string {
	e.t.Helper()
	body := map[string]any{"name": name, "kind": "snmp_v2c",
		"vault_ref": map[string]any{"connection_id": connection, "path": "network/" + name, "field_map": map[string]string{"community": "snmp_ro"}}}
	if clientID != "" {
		body["client_id"] = clientID
	}
	raw, _ := json.Marshal(body)
	return e.createCredential(string(raw))
}

func (e *vaultEnv) connectionID(name string, extra map[string]any) string {
	e.t.Helper()
	rec := e.create(name, extra)
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("create connection %s: %d %s", name, rec.Code, rec.Body.String())
	}
	return decodeVault(e.t, rec).ID
}

// bundle seals and opens the bundle of collector for the credential ids.
func (e *vaultEnv) bundle(collector string, ids ...string) (*credential.BundleContent, []string) {
	e.t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		e.t.Fatal(err)
	}
	sealed, delivered, err := e.svc.Bundle(e.f.OrgCtx(e.f.OrgA), &credential.BundleTarget{OrganizationID: e.f.OrgA,
		CollectorID: collector, PublicKey: &key.PublicKey, CredentialIDs: ids})
	if err != nil {
		e.t.Fatalf("bundle: %v", err)
	}
	content, err := credential.OpenBundle(sealed, key)
	if err != nil {
		e.t.Fatal(err)
	}
	return content, delivered
}

// TestBundleCarriesReferenceNotPlaintext covers AK1, AK2 and AK5 of WP-894:
// a vault_ref credential is stored without secret; the opened bundle of a
// collector of its connection carries the reference (no secret) and the
// stored login of the connection as its own counted entry; a collector the
// connection does not list gets neither.
func TestBundleCarriesReferenceNotPlaintext(t *testing.T) {
	e := newVaultEnv(t)
	conn := e.connectionID("wp894-edge", map[string]any{"namespace": "ns1"})
	ref := e.vaultRef("wp894-switch", conn, "")
	var ciphertextNull bool
	var source string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT ciphertext IS NULL, source FROM credential WHERE id = $1`, ref).
		Scan(&ciphertextNull, &source); err != nil || !ciphertextNull || source != "vault_ref" {
		t.Fatalf("stored reference: ciphertext null %v, source %q, %v", ciphertextNull, source, err)
	}
	rec := e.do(http.MethodGet, "/api/v1/vault-connections/"+conn, "")
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}

	content, delivered := e.bundle(e.colOrg, ref)
	slices.Sort(delivered)
	want := []string{ref, e.approleOrg}
	slices.Sort(want)
	if !slices.Equal(delivered, want) {
		t.Errorf("delivered %v, want the reference and the login %v", delivered, want)
	}
	var refEntry, loginEntry *credential.BundleCredential
	for i := range content.Credentials {
		switch content.Credentials[i].ID {
		case ref:
			refEntry = &content.Credentials[i]
		case e.approleOrg:
			loginEntry = &content.Credentials[i]
		}
	}
	if refEntry == nil || loginEntry == nil {
		t.Fatalf("bundle %+v lacks the reference or the login", content)
	}
	if refEntry.Source != "vault_ref" || refEntry.Secret != nil || refEntry.VaultRef == nil ||
		refEntry.VaultRef.ConnectionID != conn || refEntry.VaultRef.Path != "network/wp894-switch" ||
		refEntry.VaultRef.Mount != "secret" || refEntry.VaultRef.Namespace != "ns1" || refEntry.VaultRef.LoginID != e.approleOrg ||
		refEntry.VaultRef.FieldMap["community"] != "snmp_ro" || refEntry.VaultRef.Address != "https://vault.example.com:8200" {
		t.Errorf("reference entry %+v %+v", refEntry, refEntry.VaultRef)
	}
	if loginEntry.Secret["role_id"] != canaryRoleID || loginEntry.Secret["secret_id"] != canarySecretID || loginEntry.VaultRef != nil {
		t.Errorf("login entry %+v", loginEntry)
	}

	// A collector the connection does not list resolves nothing.
	if other, ids := e.bundle(e.col1, ref); len(other.Credentials) != 0 || len(ids) != 0 {
		t.Errorf("bundle of an unlisted collector: %+v %v", other, ids)
	}
	// The API shows the reference, the server decrypts nothing.
	rec = e.do(http.MethodGet, "/api/v1/vault-connections/", "")
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if _, err := e.svc.Decrypt(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, ref); err == nil {
		t.Error("the server decrypted a vault reference")
	}
}

// TestServerResolvedReferenceNotInBundle covers AK5: a reference of a
// connection with resolve_at server never reaches a bundle.
func TestServerResolvedReferenceNotInBundle(t *testing.T) {
	e := newVaultEnv(t)
	conn := e.connectionID("wp894-server", map[string]any{"resolve_at": "server", "collector_ids": nil})
	ref := e.vaultRef("wp894-server-ref", conn, "")
	content, delivered := e.bundle(e.colOrg, ref)
	if len(content.Credentials) != 0 || len(delivered) != 0 {
		t.Errorf("bundle with a server reference: %+v %v", content, delivered)
	}
}

// TestRotateOrgDEKSkipsVaultRefs covers AK6: the rotation re-encrypts the
// stored credentials only and counts them; the reference keeps no
// ciphertext.
func TestRotateOrgDEKSkipsVaultRefs(t *testing.T) {
	e := newVaultEnv(t)
	conn := e.connectionID("wp894-rotate", nil)
	ref := e.vaultRef("wp894-rotate-ref", conn, "")
	var stored int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM credential WHERE organization_id = $1 AND source = 'stored'`,
		e.f.OrgA).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	result, err := e.svc.RotateKeys(e.f.OrgCtx(e.f.OrgA), e.f.OrgA)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if result.Rotated != stored || stored == 0 {
		t.Errorf("rotated %d, want the %d stored credentials", result.Rotated, stored)
	}
	var keyVersion int
	var ciphertextNull bool
	if err = e.f.Admin.QueryRow(context.Background(), `SELECT key_version, ciphertext IS NULL FROM credential WHERE id = $1`, ref).
		Scan(&keyVersion, &ciphertextNull); err != nil || keyVersion != 1 || !ciphertextNull {
		t.Errorf("reference after the rotation: key version %d, ciphertext null %v, %v", keyVersion, ciphertextNull, err)
	}
	if secret, derr := e.svc.Decrypt(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, e.tokenOrg); derr != nil || secret["token"] != canaryToken {
		t.Errorf("login after the rotation: %v", derr)
	}
}

// TestVaultConnectionDeleteWithReferences covers AK3 of WP-893 with the
// references of WP-894: a referenced connection is not deleted (409), also
// not in the database; without references it is.
func TestVaultConnectionDeleteWithReferences(t *testing.T) {
	e := newVaultEnv(t)
	conn := e.connectionID("wp894-delete", nil)
	ref := e.vaultRef("wp894-delete-ref", conn, "")
	rec := e.do(http.MethodDelete, "/api/v1/vault-connections/"+conn, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete a referenced connection: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := e.f.Admin.Exec(context.Background(), `DELETE FROM vault_connection WHERE id = $1`, conn); err == nil ||
		!strings.Contains(err.Error(), "credential_vault_connection_fkey") {
		t.Errorf("database delete of a referenced connection: %v", err)
	}
	if rec = e.do(http.MethodDelete, "/api/v1/credentials/"+ref, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete the reference: %d %s", rec.Code, rec.Body.String())
	}
	if rec = e.do(http.MethodDelete, "/api/v1/vault-connections/"+conn, ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete the unreferenced connection: %d %s", rec.Code, rec.Body.String())
	}
}

// TestVaultRefChangesAndClientRule covers AK2 and AK3: PUT …/secret of a
// reference is 409; PATCH changes path, version and field map, raises
// secret_version and audits the fields without values; a client
// credential takes only a connection of its client or an org-wide one;
// both sources at once are 422.
func TestVaultRefChangesAndClientRule(t *testing.T) {
	e := newVaultEnv(t)
	conn := e.connectionID("wp894-change", nil)
	ref := e.vaultRef("wp894-change-ref", conn, "")
	wantField(t, "replace the secret of a reference", e.do(http.MethodPut, "/api/v1/credentials/"+ref+"/secret", `{"secret":{"community":"x"}}`),
		http.StatusConflict, "secret")
	rec := e.do(http.MethodPatch, "/api/v1/credentials/"+ref,
		`{"vault_path":"network/wp894-moved","vault_version":3,"field_map":{"community":"ro_community"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch the reference: %d %s", rec.Code, rec.Body.String())
	}
	var c credential.Credential
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	if c.SecretVersion != 2 || c.VaultRef == nil || c.VaultRef.Path != "network/wp894-moved" || c.VaultRef.Version == nil ||
		*c.VaultRef.Version != 3 || c.VaultRef.FieldMap["community"] != "ro_community" {
		t.Errorf("patched reference %+v %+v", c, c.VaultRef)
	}
	var changes string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT changes::text FROM audit_log WHERE organization_id = $1
		AND changes->>'route' = 'PATCH /api/v1/credentials/{id}' ORDER BY created_at DESC LIMIT 1`, e.f.OrgA).Scan(&changes); err != nil {
		t.Fatalf("audit entry of the patch: %v", err)
	}
	if !strings.Contains(changes, "vault_path") || strings.Contains(changes, "wp894-moved") || strings.Contains(changes, "ro_community") {
		t.Errorf("audit entry names the fields without values: %s", changes)
	}
	wantField(t, "field_map without community", e.do(http.MethodPatch, "/api/v1/credentials/"+ref, `{"field_map":{"x":"y"}}`),
		http.StatusUnprocessableEntity, "field_map")
	wantField(t, "vault_path of a stored credential", e.do(http.MethodPatch, "/api/v1/credentials/"+e.tokenOrg, `{"vault_path":"a"}`),
		http.StatusUnprocessableEntity, "vault_path")

	// The client rule: a connection of client 1 serves client 1 only.
	conn1 := e.connectionID("wp894-client-1", map[string]any{"client_id": e.f.Client1, "auth_credential_id": e.approle1,
		"collector_ids": []string{e.col1}})
	if id := e.vaultRef("wp894-c1", conn1, e.f.Client1); id == "" {
		t.Error("reference of client 1 to its connection")
	}
	body := `{"name":"wp894-c2","kind":"snmp_v2c","client_id":"` + e.f.Client2 + `","vault_ref":{"connection_id":"` + conn1 +
		`","path":"p","field_map":{"community":"c"}}}`
	wantField(t, "reference of client 2 to a connection of client 1", e.do(http.MethodPost, "/api/v1/credentials/", body),
		http.StatusUnprocessableEntity, "vault_ref.connection_id")
	if id := e.vaultRef("wp894-c2-org", conn, e.f.Client2); id == "" {
		t.Error("reference of client 2 to an org-wide connection")
	}
	both := `{"name":"wp894-both","kind":"snmp_v2c","secret":{"community":"c"},"vault_ref":{"connection_id":"` + conn +
		`","path":"p","field_map":{"community":"c"}}}`
	wantField(t, "both sources", e.do(http.MethodPost, "/api/v1/credentials/", both), http.StatusUnprocessableEntity, "vault_ref")
	wantField(t, "no source", e.do(http.MethodPost, "/api/v1/credentials/", `{"name":"wp894-none","kind":"snmp_v2c"}`),
		http.StatusUnprocessableEntity, "secret")
	// The database refuses a reference to a connection of another client.
	if _, err := e.f.Admin.Exec(context.Background(), `INSERT INTO credential (organization_id, client_id, name, kind, source,
		vault_connection_id, vault_path, field_map) VALUES ($1, $2, 'wp894-db', 'snmp_v2c', 'vault_ref', $3, 'p', '{"community":"c"}')`,
		e.f.OrgA, e.f.Client2, conn1); err == nil || !strings.Contains(err.Error(), "SQLSTATE 23514") {
		t.Errorf("database insert of a foreign reference: %v", err)
	}
}
