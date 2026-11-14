package credential_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Retest (WP-893, CRD-03, COL-03):
//
//	cd backend && go test -count=1 -p 1 -run 'TestVaultConnection' ./internal/credential/

// Canary values of the vault logins: no answer and no audit entry may
// carry them.
const (
	canaryRoleID   = "wp893-role-id-canary"
	canarySecretID = "wp893-secret-id-canary"
	canaryToken    = "wp893-token-canary"
)

// vaultEnv is the credential API of organization A on PostgreSQL behind the
// route authorization and the route audit of the server, with an AppRole
// login of client 1 (approle1), an org-wide one (approleOrg), an org-wide
// token login (tokenOrg) and a collector of each client and an org-wide one.
type vaultEnv struct {
	t                    *testing.T
	f                    *scopetest.Fixture
	mux                  *chi.Mux
	approle1, approleOrg string
	tokenOrg             string
	col1, col2, colOrg   string
	perms                []identity.Permission
	clientScope          string
	// svc is the service behind the routes (bundles, rotation; WP-894).
	svc *credential.Service
}

// vaultRoutes are the routes the environment serves with their audit.
var vaultRoutes = []struct{ method, pattern string }{
	{http.MethodPost, "/api/v1/credentials/"},
	{http.MethodPut, "/api/v1/credentials/{id}/secret"},
	{http.MethodPatch, "/api/v1/credentials/{id}"},
	{http.MethodDelete, "/api/v1/credentials/{id}"},
	{http.MethodPost, "/api/v1/credential-bindings/"},
	{http.MethodPost, "/api/v1/vault-connections/"},
	{http.MethodGet, "/api/v1/vault-connections/"},
	{http.MethodGet, "/api/v1/vault-connections/{id}"},
	{http.MethodPatch, "/api/v1/vault-connections/{id}"},
	{http.MethodDelete, "/api/v1/vault-connections/{id}"},
}

func newVaultEnv(t *testing.T) *vaultEnv {
	t.Helper()
	f := scopetest.Seed(t, "15")
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	inner := chi.NewRouter()
	svc := credential.NewService(credential.NewPGRepository(f.App), enc)
	credential.NewHandler(svc).RegisterRoutes(inner)
	// Every route behind its permission and the production route audit,
	// as the server wires it.
	mux := chi.NewRouter()
	auditor := audit.NewPGRouteAuditor(f.App)
	for _, rt := range vaultRoutes {
		mode := middleware.AuditIntent
		if rt.method == http.MethodGet {
			mode = middleware.AuditNone
		}
		mux.With(server.AuthorizeRoute(rt.method, rt.pattern), middleware.AuditRoute(rt.method, rt.pattern, auditor, mode, "")).
			Method(rt.method, rt.pattern, inner)
	}
	e := &vaultEnv{t: t, f: f, mux: mux, perms: identity.AllPermissions(), svc: svc}
	e.approle1 = e.createCredential(`{"name":"wp893-approle-1","kind":"vault_approle","client_id":"` + f.Client1 +
		`","secret":{"role_id":"` + canaryRoleID + `","secret_id":"` + canarySecretID + `"}}`)
	e.approleOrg = e.createCredential(`{"name":"wp893-approle-org","kind":"vault_approle","secret":{"role_id":"` +
		canaryRoleID + `","secret_id":"` + canarySecretID + `"}}`)
	e.tokenOrg = e.createCredential(`{"name":"wp893-token","kind":"vault_token","secret":{"token":"` + canaryToken + `"}}`)
	e.col1 = e.collector(f.Client1, "wp893-col-1")
	e.col2 = e.collector(f.Client2, "wp893-col-2")
	e.colOrg = e.collector("", "wp893-col-org")
	return e
}

func (e *vaultEnv) collector(clientID, name string) string {
	e.t.Helper()
	var client any
	if clientID != "" {
		client = clientID
	}
	var id string
	if err := e.f.Admin.QueryRow(context.Background(), `INSERT INTO collector (organization_id, client_id, name)
		VALUES ($1, $2, $3) RETURNING id::text`, e.f.OrgA, client, name).Scan(&id); err != nil {
		e.t.Fatalf("insert collector: %v", err)
	}
	return id
}

// do sends a request with the environment's permissions, org-wide or
// restricted to clientScope.
func (e *vaultEnv) do(method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	scope := database.OrgWideScope(e.f.OrgA, e.f.User)
	idScope := &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}}
	if e.clientScope != "" {
		scope.Clients = database.ScopeIDs(e.clientScope)
		idScope.Clients = identity.ScopeSet{IDs: []string{e.clientScope}}
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	p := identity.Principal{Subject: e.f.User, OrganizationID: e.f.OrgA, Type: identity.PrincipalTypeUser,
		Scope: idScope, Permissions: e.perms, Credential: "wp893"}
	ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), e.perms)
	ctx = database.ContextWithTenantScope(ctx, &scope)
	ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: e.f.User})
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

func (e *vaultEnv) createCredential(body string) string {
	e.t.Helper()
	rec := e.do(http.MethodPost, "/api/v1/credentials/", body)
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("create credential: %d %s", rec.Code, rec.Body.String())
	}
	return decodeVault(e.t, rec).ID
}

// connection builds the body of a connection; extra members replace the
// defaults (an org-wide AppRole connection resolved by the org-wide
// collector).
func (e *vaultEnv) connection(name string, extra map[string]any) string {
	e.t.Helper()
	body := map[string]any{"name": name, "kind": "openbao_kv2", "address": "https://vault.example.com:8200",
		"mount": "secret", "auth_method": "approle", "auth_credential_id": e.approleOrg, "collector_ids": []string{e.colOrg}}
	for k, v := range extra {
		if v == nil {
			delete(body, k)
			continue
		}
		body[k] = v
	}
	raw, err := json.Marshal(body)
	if err != nil {
		e.t.Fatal(err)
	}
	return string(raw)
}

func (e *vaultEnv) create(name string, extra map[string]any) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(http.MethodPost, "/api/v1/vault-connections/", e.connection(name, extra))
}

type vaultReply struct {
	credential.VaultConnection
	Field string `json:"field"`
}

func decodeVault(t *testing.T, rec *httptest.ResponseRecorder) vaultReply {
	t.Helper()
	var v vaultReply
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %d %s: %v", rec.Code, rec.Body.String(), err)
	}
	return v
}

func wantField(t *testing.T, what string, rec *httptest.ResponseRecorder, status int, field string) {
	t.Helper()
	if rec.Code != status || !strings.Contains(rec.Body.String(), `"field":"`+field+`"`) {
		t.Errorf("%s: %d %s, want %d with field %s", what, rec.Code, rec.Body.String(), status, field)
	}
}

// selfSignedPEM returns a PEM certificate.
func selfSignedPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "wp893 vault ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// TestVaultConnectionCRUD covers AK1, AK3 and AK4 (WP-893): create with the
// default resolve_at collector, list, get, PATCH, a duplicate name (409),
// syntax checks of address and ca_pem, delete.
func TestVaultConnectionCRUD(t *testing.T) {
	e := newVaultEnv(t)
	ca := selfSignedPEM(t)
	rec := e.create("wp893-bao", map[string]any{"namespace": "team-a", "ca_pem": ca})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	c := decodeVault(t, rec)
	if c.ResolveAt != "collector" || !c.Enabled || c.Namespace != "team-a" || c.CAPEM != ca ||
		len(c.CollectorIDs) != 1 || c.CollectorIDs[0] != e.colOrg || c.AuthCredentialID != e.approleOrg {
		t.Errorf("created connection %+v", c.VaultConnection)
	}
	wantField(t, "duplicate name", e.create("wp893-bao", nil), http.StatusConflict, "name")

	rec = e.do(http.MethodGet, "/api/v1/vault-connections/?limit=10", "")
	var page struct {
		Data []credential.VaultConnection `json:"data"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &page) != nil || len(page.Data) != 1 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if rec = e.do(http.MethodGet, "/api/v1/vault-connections/"+c.ID, ""); rec.Code != http.StatusOK {
		t.Errorf("get: %d %s", rec.Code, rec.Body.String())
	}

	// PATCH: the token login and the server; the collectors go.
	rec = e.do(http.MethodPatch, "/api/v1/vault-connections/"+c.ID,
		`{"auth_method":"token","auth_credential_id":"`+e.tokenOrg+`","resolve_at":"server","kind":"hashicorp_kv2","ca_pem":null}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
	}
	if p := decodeVault(t, rec); p.ResolveAt != "server" || len(p.CollectorIDs) != 0 || p.CAPEM != "" || p.Kind != "hashicorp_kv2" {
		t.Errorf("patched connection %+v", p.VaultConnection)
	}
	wantField(t, "patch client_id", e.do(http.MethodPatch, "/api/v1/vault-connections/"+c.ID, `{"client_id":"`+e.f.Client1+`"}`),
		http.StatusUnprocessableEntity, "client_id")
	wantField(t, "patch unknown member", e.do(http.MethodPatch, "/api/v1/vault-connections/"+c.ID, `{"token":"x"}`),
		http.StatusUnprocessableEntity, "token")

	for _, tc := range []struct {
		what  string
		extra map[string]any
		field string
	}{
		{"http address", map[string]any{"address": "http://vault.example.com"}, "address"},
		{"address without host", map[string]any{"address": "https://"}, "address"},
		{"address with user", map[string]any{"address": "https://user:pw@vault.example.com"}, "address"},
		{"ca_pem no PEM", map[string]any{"ca_pem": "not a certificate"}, "ca_pem"},
		{"ca_pem no certificate", map[string]any{"ca_pem": "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"}, "ca_pem"},
		{"kind", map[string]any{"kind": "azure_kv"}, "kind"},
		{"auth_method", map[string]any{"auth_method": "kubernetes"}, "auth_method"},
		{"mount", map[string]any{"mount": ""}, "mount"},
		{"resolve_at", map[string]any{"resolve_at": "edge"}, "resolve_at"},
	} {
		wantField(t, tc.what, e.create("wp893-bad", tc.extra), http.StatusUnprocessableEntity, tc.field)
	}

	if rec = e.do(http.MethodDelete, "/api/v1/vault-connections/"+c.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if rec = e.do(http.MethodGet, "/api/v1/vault-connections/"+c.ID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("get after delete: %d", rec.Code)
	}
	// The login of a connection is not deleted underneath it (ON DELETE
	// RESTRICT).
	if rec = e.create("wp893-keep", nil); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}
	if _, err := e.f.Admin.Exec(context.Background(), `DELETE FROM credential WHERE id = $1`, e.approleOrg); err == nil ||
		!strings.Contains(err.Error(), "vault_connection_auth_credential_fkey") {
		t.Errorf("database delete of a login in use: %v", err)
	}
	// Over the API: 409 with field id, not the generic 422 of the foreign
	// key; the login stays.
	wantField(t, "delete the login of a connection", e.do(http.MethodDelete, "/api/v1/credentials/"+e.approleOrg, ""),
		http.StatusConflict, "id")
	var kept int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM credential WHERE id = $1`, e.approleOrg).Scan(&kept); err != nil || kept != 1 {
		t.Errorf("the login is gone after a refused delete: %d %v", kept, err)
	}
	// An unused login is deleted.
	if rec = e.do(http.MethodDelete, "/api/v1/credentials/"+e.tokenOrg, ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete an unused login: %d %s", rec.Code, rec.Body.String())
	}
}

// TestVaultConnectionResponsesCarryNoSecrets covers AK3 and AK5: no answer
// of the routes and no audit entry of the organization carries the values
// of the logins; the answers name the login by auth_credential_id.
func TestVaultConnectionResponsesCarryNoSecrets(t *testing.T) {
	e := newVaultEnv(t)
	var bodies []string
	rec := e.create("wp893-secretless", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	id := decodeVault(t, rec).ID
	bodies = append(bodies, rec.Body.String())
	for _, r := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/vault-connections/", ""},
		{http.MethodGet, "/api/v1/vault-connections/" + id, ""},
		{http.MethodPatch, "/api/v1/vault-connections/" + id, `{"auth_method":"token","auth_credential_id":"` + e.tokenOrg + `"}`},
		{http.MethodPatch, "/api/v1/vault-connections/" + id, `{"auth_method":"approle","auth_credential_id":"` + e.approleOrg + `"}`},
		{http.MethodDelete, "/api/v1/vault-connections/" + id, ""},
	} {
		rec = e.do(r.method, r.path, r.body)
		if rec.Code >= http.StatusMultipleChoices {
			t.Errorf("%s %s: %d %s", r.method, r.path, rec.Code, rec.Body.String())
		}
		bodies = append(bodies, rec.Body.String())
	}
	if !strings.Contains(bodies[0], `"auth_credential_id":"`+e.approleOrg+`"`) {
		t.Errorf("the answer does not name the login: %s", bodies[0])
	}
	var entries []string
	rows, err := e.f.Admin.Query(context.Background(), `SELECT action || ' ' || COALESCE(changes::text, '') FROM audit_log
		WHERE organization_id = $1`, e.f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	routeEntries := 0
	for rows.Next() {
		var entry string
		if err = rows.Scan(&entry); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(entry, "vault-connections") {
			routeEntries++
		}
		entries = append(entries, entry)
	}
	if routeEntries < 4 {
		t.Errorf("%d audit entries of the vault connection routes, want at least 4: %v", routeEntries, entries)
	}
	for _, text := range append(bodies, entries...) {
		for _, canary := range []string{canaryRoleID, canarySecretID, canaryToken} {
			if strings.Contains(text, canary) {
				t.Errorf("%q carries a secret of a login: %s", canary, text)
			}
		}
	}
}

// TestVaultConnectionAuthCredentialKind covers AK2: the login must be of
// the kind of auth_method and of the connection's client or org-wide; a
// vault login needs its members.
func TestVaultConnectionAuthCredentialKind(t *testing.T) {
	e := newVaultEnv(t)
	snmp := e.createCredential(`{"name":"wp893-snmp","kind":"snmp_v2c","secret":{"community":"x"}}`)
	unknown := "00000000-0000-4000-8000-0000000000bb"
	for _, tc := range []struct {
		what  string
		extra map[string]any
	}{
		{"token for approle", map[string]any{"auth_credential_id": e.tokenOrg}},
		{"approle for token", map[string]any{"auth_method": "token", "auth_credential_id": e.approleOrg}},
		{"snmp credential", map[string]any{"auth_credential_id": snmp}},
		{"unknown credential", map[string]any{"auth_credential_id": unknown}},
		{"client login for an org-wide connection", map[string]any{"auth_credential_id": e.approle1}},
		{"client login for another client", map[string]any{"client_id": e.f.Client2, "auth_credential_id": e.approle1,
			"collector_ids": []string{e.col2}}},
	} {
		wantField(t, tc.what, e.create("wp893-kind", tc.extra), http.StatusUnprocessableEntity, "auth_credential_id")
	}
	if rec := e.create("wp893-client-1", map[string]any{"client_id": e.f.Client1, "auth_credential_id": e.approle1,
		"collector_ids": []string{e.col1}}); rec.Code != http.StatusCreated {
		t.Errorf("client login for its client: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.create("wp893-token", map[string]any{"auth_method": "token", "auth_credential_id": e.tokenOrg}); rec.Code != http.StatusCreated {
		t.Errorf("token login: %d %s", rec.Code, rec.Body.String())
	}
	// The members of the logins (AK2).
	for _, body := range []string{
		`{"name":"wp893-x","kind":"vault_approle","secret":{"role_id":"r"}}`,
		`{"name":"wp893-y","kind":"vault_token","secret":{"value":"t"}}`,
	} {
		wantField(t, "login without its members", e.do(http.MethodPost, "/api/v1/credentials/", body), http.StatusUnprocessableEntity, "secret")
	}
	wantField(t, "replace a token without token", e.do(http.MethodPut, "/api/v1/credentials/"+e.tokenOrg+"/secret", `{"secret":{"x":"y"}}`),
		http.StatusUnprocessableEntity, "secret")
	// A login is not bound to targets (namespace vault has no binding).
	ci := e.f.CI(t, e.f.OrgA, "", "wp893-ci")
	wantField(t, "bind a vault login", e.do(http.MethodPost, "/api/v1/credential-bindings/",
		`{"credential_id":"`+e.tokenOrg+`","target_kind":"ci","target_id":"`+ci+`"}`), http.StatusUnprocessableEntity, "credential_id")
}

// TestVaultConnectionClientPrincipal covers the RLS of AK1: a principal of
// client 1 sees the connections of client 1 and the org-wide ones; a
// connection of client 2 is 404 for it and it cannot create one.
func TestVaultConnectionClientPrincipal(t *testing.T) {
	e := newVaultEnv(t)
	approle2 := e.createCredential(`{"name":"wp893-approle-2","kind":"vault_approle","client_id":"` + e.f.Client2 +
		`","secret":{"role_id":"r","secret_id":"s"}}`)
	own := e.create("wp893-own", map[string]any{"client_id": e.f.Client1, "auth_credential_id": e.approle1, "collector_ids": []string{e.col1}})
	foreign := e.create("wp893-foreign", map[string]any{"client_id": e.f.Client2, "auth_credential_id": approle2, "collector_ids": []string{e.col2}})
	orgWide := e.create("wp893-org", nil)
	for _, rec := range []*httptest.ResponseRecorder{own, foreign, orgWide} {
		if rec.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
		}
	}
	e.clientScope = e.f.Client1
	rec := e.do(http.MethodGet, "/api/v1/vault-connections/?limit=50", "")
	var page struct {
		Data []credential.VaultConnection `json:"data"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &page) != nil {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if len(page.Data) != 2 {
		t.Errorf("client-1 principal sees %d connections, want 2: %+v", len(page.Data), page.Data)
	}
	foreignID := decodeVault(t, foreign).ID
	for _, r := range []struct{ method, body string }{
		{http.MethodGet, ""}, {http.MethodPatch, `{"enabled":false}`}, {http.MethodDelete, ""},
	} {
		if rec = e.do(r.method, "/api/v1/vault-connections/"+foreignID, r.body); rec.Code != http.StatusNotFound {
			t.Errorf("%s of a connection of client 2: %d, want 404", r.method, rec.Code)
		}
	}
	// A connection of client 2 is not created by the client-1 principal:
	// its login is invisible (422) and the row outside its scope.
	if rec = e.create("wp893-sneak", map[string]any{"client_id": e.f.Client2, "auth_credential_id": e.approleOrg,
		"collector_ids": []string{e.colOrg}}); rec.Code != http.StatusForbidden && rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("create for client 2: %d %s", rec.Code, rec.Body.String())
	}
	if rec = e.do(http.MethodPatch, "/api/v1/vault-connections/"+decodeVault(t, own).ID, `{"enabled":false}`); rec.Code != http.StatusOK {
		t.Errorf("patch the own connection: %d %s", rec.Code, rec.Body.String())
	}
	// The collector rows carry the client of their connection.
	var client string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT client_id::text FROM vault_connection_collector
		WHERE vault_connection_id = $1`, foreignID).Scan(&client); err != nil || client != e.f.Client2 {
		t.Errorf("client of the collector row: %q %v", client, err)
	}
}

// TestVaultConnectionCollectorsRequired covers AK3: resolve_at collector
// needs collectors of the connection's client or org-wide ones; server
// refuses them.
func TestVaultConnectionCollectorsRequired(t *testing.T) {
	e := newVaultEnv(t)
	wantField(t, "collector without collectors", e.create("wp893-none", map[string]any{"collector_ids": []string{}}),
		http.StatusUnprocessableEntity, "collector_ids")
	wantField(t, "collector_ids missing", e.create("wp893-missing", map[string]any{"collector_ids": nil}),
		http.StatusUnprocessableEntity, "collector_ids")
	wantField(t, "server with collectors", e.create("wp893-server", map[string]any{"resolve_at": "server"}),
		http.StatusUnprocessableEntity, "collector_ids")
	wantField(t, "unknown collector", e.create("wp893-unknown", map[string]any{"collector_ids": []string{"00000000-0000-4000-8000-0000000000cc"}}),
		http.StatusUnprocessableEntity, "collector_ids")
	wantField(t, "collector of another client", e.create("wp893-other", map[string]any{"client_id": e.f.Client1,
		"auth_credential_id": e.approle1, "collector_ids": []string{e.col2}}), http.StatusUnprocessableEntity, "collector_ids")
	if rec := e.create("wp893-server-ok", map[string]any{"resolve_at": "server", "collector_ids": nil}); rec.Code != http.StatusCreated {
		t.Errorf("server without collectors: %d %s", rec.Code, rec.Body.String())
	}
	rec := e.create("wp893-mixed", map[string]any{"client_id": e.f.Client1, "auth_credential_id": e.approle1,
		"collector_ids": []string{e.col1, e.colOrg, e.col1}})
	if rec.Code != http.StatusCreated || len(decodeVault(t, rec).CollectorIDs) != 2 {
		t.Errorf("collectors of the client and org-wide: %d %s", rec.Code, rec.Body.String())
	}
	// An org-wide connection takes collectors of any client.
	if rec = e.create("wp893-org-any", map[string]any{"collector_ids": []string{e.col1, e.col2}}); rec.Code != http.StatusCreated {
		t.Errorf("org-wide connection with client collectors: %d %s", rec.Code, rec.Body.String())
	}
	// The database refuses a collector of another client as well.
	id := decodeVault(t, e.create("wp893-db", map[string]any{"client_id": e.f.Client1, "auth_credential_id": e.approle1,
		"collector_ids": []string{e.col1}})).ID
	if _, err := e.f.Admin.Exec(context.Background(), `INSERT INTO vault_connection_collector (organization_id, vault_connection_id, collector_id)
		VALUES ($1, $2, $3)`, e.f.OrgA, id, e.col2); err == nil || !strings.Contains(err.Error(), "SQLSTATE 23514") {
		t.Errorf("database insert of a foreign collector: %v", err)
	}
}

// TestVaultConnectionReadNeedsCredentialRead covers AK3: credential:read
// reads the connections (200) but does not write them (403); writing needs
// vault:manage.
func TestVaultConnectionReadNeedsCredentialRead(t *testing.T) {
	e := newVaultEnv(t)
	rec := e.create("wp893-read", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	id := decodeVault(t, rec).ID
	e.perms = []identity.Permission{identity.PermCredentialRead}
	for _, path := range []string{"/api/v1/vault-connections/", "/api/v1/vault-connections/" + id} {
		if rec = e.do(http.MethodGet, path, ""); rec.Code != http.StatusOK {
			t.Errorf("GET %s with credential:read: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	for _, r := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/vault-connections/", e.connection("wp893-denied", nil)},
		{http.MethodPatch, "/api/v1/vault-connections/" + id, `{"enabled":false}`},
		{http.MethodDelete, "/api/v1/vault-connections/" + id, ""},
	} {
		if rec = e.do(r.method, r.path, r.body); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with credential:read: %d, want 403", r.method, r.path, rec.Code)
		}
	}
	e.perms = []identity.Permission{identity.PermVaultManage}
	if rec = e.do(http.MethodGet, "/api/v1/vault-connections/"+id, ""); rec.Code != http.StatusForbidden {
		t.Errorf("GET with vault:manage only: %d, want 403", rec.Code)
	}
	if rec = e.do(http.MethodPatch, "/api/v1/vault-connections/"+id, `{"enabled":false}`); rec.Code != http.StatusOK {
		t.Errorf("PATCH with vault:manage: %d %s", rec.Code, rec.Body.String())
	}
}
