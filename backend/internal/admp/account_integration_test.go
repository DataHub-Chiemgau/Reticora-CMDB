package admp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/admp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// ADMP accounts (ADM-01, WP-925) on PostgreSQL and on the memory store of
// --no-db: the same rules through the same handler.

// canaryToken is the secret of the accounts' credential; no answer and no
// account column may carry it.
const canaryToken = "wp925-canary-api-token-6f0b"

// accountEnv is the account and credential API of one organization with
// two clients, on PostgreSQL (f set) or in memory.
type accountEnv struct {
	t                *testing.T
	f                *scopetest.Fixture
	mux              *chi.Mux
	svc              *admp.AccountService
	org, user        string
	client1, client2 string
	// credOrg and cred1 are api_token credentials, org-wide and of
	// client 1.
	credOrg, cred1 string
}

// forEachStore runs fn on the memory store and, with TEST_DATABASE_URL, on
// PostgreSQL.
func forEachStore(t *testing.T, fn func(t *testing.T, e *accountEnv)) {
	t.Run("memory", func(t *testing.T) { fn(t, newAccountEnv(t, false)) })
	t.Run("postgres", func(t *testing.T) {
		if os.Getenv("TEST_DATABASE_URL") == "" {
			t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
		}
		fn(t, newAccountEnv(t, true))
	})
}

func newAccountEnv(t *testing.T, pg bool) *accountEnv {
	t.Helper()
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	e := &accountEnv{t: t}
	var creds *credential.Service
	var repo admp.AccountRepository
	if pg {
		e.f = scopetest.Seed(t, "e6")
		e.org, e.user, e.client1, e.client2 = e.f.OrgA, e.f.User, e.f.Client1, e.f.Client2
		creds = credential.NewService(credential.NewPGRepository(e.f.App), enc)
		repo = admp.NewPGAccountRepository(e.f.App)
	} else {
		e.org, e.user = "6f0b6f0e-0000-4000-8000-000000925000", "6f0b6f0e-0000-4000-8000-000000925001"
		e.client1, e.client2 = "6f0b6f0e-0000-4000-8000-000000925011", "6f0b6f0e-0000-4000-8000-000000925012"
		creds = credential.NewService(credential.NewMemoryRepository(), enc)
		repo = admp.NewMemoryAccountRepository()
	}
	e.svc = admp.NewAccountService(repo, admp.CredentialsOf(creds), egress.Options{})
	e.mux = chi.NewRouter()
	credential.NewHandler(creds).RegisterRoutes(e.mux)
	admp.NewHandler(e.svc).RegisterRoutes(e.mux)
	e.credOrg = e.createCredential(`{"name":"wp925-org","kind":"api_token","secret":{"token":"` + canaryToken + `"}}`)
	e.cred1 = e.createCredential(`{"name":"wp925-c1","kind":"api_token","client_id":"` + e.client1 + `","secret":{"token":"` + canaryToken + `"}}`)
	return e
}

// ctx is the request context of an org-wide principal, or of one
// restricted to clientID.
func (e *accountEnv) ctx(clientID string) context.Context {
	scope := database.OrgWideScope(e.org, e.user)
	if clientID != "" {
		scope.Clients = database.ScopeIDs(clientID)
	}
	ctx := database.ContextWithTenantScope(context.Background(), &scope)
	return tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: e.org, UserID: e.user})
}

func (e *accountEnv) do(method, path, body, clientID string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(e.ctx(clientID))
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), canaryToken) {
		e.t.Errorf("%s %s answers with the secret of a credential", method, path)
	}
	return rec
}

func (e *accountEnv) createCredential(body string) string {
	e.t.Helper()
	rec := e.do(http.MethodPost, "/api/v1/credentials", body, "")
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("create credential: %d %s", rec.Code, rec.Body.String())
	}
	return idOf(e.t, rec)
}

func idOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var v struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v.ID
}

// account builds the body of an account; extra replaces members (nil drops).
func account(name string, extra map[string]any) string {
	body := map[string]any{"name": name, "base_url": "https://admp.example.com/api/v1"}
	for k, v := range extra {
		if v == nil {
			delete(body, k)
			continue
		}
		body[k] = v
	}
	raw, _ := json.Marshal(body)
	return string(raw)
}

func (e *accountEnv) create(name string, extra map[string]any, clientID string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(http.MethodPost, "/api/v1/admp/accounts", account(name, extra), clientID)
}

func (e *accountEnv) mustCreate(name string, extra map[string]any) string {
	e.t.Helper()
	rec := e.create(name, extra, "")
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("create account %s: %d %s", name, rec.Code, rec.Body.String())
	}
	return idOf(e.t, rec)
}

// field returns the field of a problem answer.
func field(rec *httptest.ResponseRecorder) string {
	var p struct {
		Field string `json:"field"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	return p.Field
}

func decodeAccount(t *testing.T, rec *httptest.ResponseRecorder) admp.Account {
	t.Helper()
	var a admp.Account
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	return a
}

func listIDs(t *testing.T, rec *httptest.ResponseRecorder) map[string]bool {
	t.Helper()
	var page struct {
		Data []admp.Account `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for i := range page.Data {
		out[page.Data[i].ID] = true
	}
	return out
}

// TestAdmpAccountCRUD: create with defaults and the credential reference,
// list, get, patch, a taken name 409, the immutable and read-only members
// 422, delete.
func TestAdmpAccountCRUD(t *testing.T) {
	forEachStore(t, func(t *testing.T, e *accountEnv) {
		rec := e.create("wp925-main", map[string]any{"credential_id": e.credOrg, "auth_url": "https://login.example.com/token"}, "")
		if rec.Code != http.StatusCreated {
			t.Fatalf("POST = %d %s", rec.Code, rec.Body.String())
		}
		a := decodeAccount(t, rec)
		if a.RateLimitRPS != admp.DefaultRateLimitRPS || a.TimeoutMS != admp.DefaultTimeoutMS || !a.Enabled || a.ClientID != "" ||
			a.Credential == nil || a.Credential.Name != "wp925-org" || a.Credential.Source != credential.SourceStored ||
			a.LastOKAt != nil || a.LastErrorClass != nil || a.AuthURL != "https://login.example.com/token" {
			t.Errorf("created %+v, want the defaults, the credential reference and no status", a)
		}
		if rec = e.create("wp925-main", map[string]any{"enabled": false}, ""); rec.Code != http.StatusConflict || field(rec) != "name" {
			t.Errorf("taken name = %d %s, want 409 name", rec.Code, rec.Body.String())
		}
		if rec = e.do(http.MethodGet, "/api/v1/admp/accounts", "", ""); rec.Code != http.StatusOK || !listIDs(t, rec)[a.ID] {
			t.Errorf("GET list = %d %s, want the account", rec.Code, rec.Body.String())
		}
		rec = e.do(http.MethodPatch, "/api/v1/admp/accounts/"+a.ID, `{"name":"wp925-renamed","rate_limit_rps":5.5,"timeout_ms":20000,"auth_url":null}`, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("PATCH = %d %s", rec.Code, rec.Body.String())
		}
		if got := decodeAccount(t, rec); got.Name != "wp925-renamed" || got.RateLimitRPS != 5.5 || got.TimeoutMS != 20000 || got.AuthURL != "" {
			t.Errorf("patched %+v", got)
		}
		for body, want := range map[string]string{
			`{"client_id":"` + e.client1 + `"}`:     "client_id",
			`{"last_ok_at":"2026-11-11T00:00:00Z"}`: "last_ok_at",
			`{"last_error_class":"auth"}`:           "last_error_class",
			`{"rate_limit_rps":0}`:                  "rate_limit_rps",
			`{"rate_limit_rps":51}`:                 "rate_limit_rps",
			`{"timeout_ms":999}`:                    "timeout_ms",
			`{"name":""}`:                           "name",
		} {
			if rec = e.do(http.MethodPatch, "/api/v1/admp/accounts/"+a.ID, body, ""); rec.Code != http.StatusUnprocessableEntity || field(rec) != want {
				t.Errorf("PATCH %s = %d %s, want 422 %s", body, rec.Code, rec.Body.String(), want)
			}
		}
		if rec = e.do(http.MethodDelete, "/api/v1/admp/accounts/"+a.ID, "", ""); rec.Code != http.StatusNoContent {
			t.Errorf("DELETE = %d %s", rec.Code, rec.Body.String())
		}
		if rec = e.do(http.MethodGet, "/api/v1/admp/accounts/"+a.ID, "", ""); rec.Code != http.StatusNotFound {
			t.Errorf("GET after DELETE = %d, want 404", rec.Code)
		}
	})
}

// TestAdmpAccountClientScope: a principal of client 1 sees only the
// accounts of client 1, neither the organization-wide one nor that of
// client 2 (404, AK6); it writes no organization-wide account and none of
// client 2 (403 on create, 404 on the invisible ones). AccountFor still
// falls back to the organization-wide account for it.
func TestAdmpAccountClientScope(t *testing.T) {
	forEachStore(t, func(t *testing.T, e *accountEnv) {
		a1 := e.mustCreate("wp925-c1", map[string]any{"client_id": e.client1, "credential_id": e.cred1})
		a2 := e.mustCreate("wp925-c2", map[string]any{"client_id": e.client2})
		ao := e.mustCreate("wp925-org", nil)
		ids := listIDs(t, e.do(http.MethodGet, "/api/v1/admp/accounts", "", e.client1))
		if !ids[a1] || ids[ao] || ids[a2] {
			t.Errorf("client 1 lists %v, want only its account", ids)
		}
		for _, id := range []string{a2, ao} {
			if rec := e.do(http.MethodGet, "/api/v1/admp/accounts/"+id, "", e.client1); rec.Code != http.StatusNotFound {
				t.Errorf("client 1 GET of %s = %d, want 404", id, rec.Code)
			}
		}
		if got, err := e.svc.AccountFor(e.ctx(e.client2), e.org, e.client2); err != nil || got.ID != a2 {
			t.Errorf("AccountFor(client 2) as client 2 = %v, %v, want its account", got, err)
		}
		if rec := e.do(http.MethodPatch, "/api/v1/admp/accounts/"+a2, `{"enabled":false}`, ""); rec.Code != http.StatusOK {
			t.Fatalf("disable client 2's account = %d %s", rec.Code, rec.Body.String())
		}
		if got, err := e.svc.AccountFor(e.ctx(e.client2), e.org, e.client2); err != nil || got.ID != ao {
			t.Errorf("AccountFor(client 2) as client 2 = %v, %v, want the organization-wide account", got, err)
		}
		if rec := e.create("wp925-org-by-c1", map[string]any{"enabled": false}, e.client1); rec.Code != http.StatusForbidden {
			t.Errorf("client 1 creates an organization-wide account = %d %s, want 403", rec.Code, rec.Body.String())
		}
		if rec := e.create("wp925-c2-by-c1", map[string]any{"client_id": e.client2, "enabled": false}, e.client1); rec.Code != http.StatusForbidden {
			t.Errorf("client 1 creates an account of client 2 = %d %s, want 403", rec.Code, rec.Body.String())
		}
		if rec := e.do(http.MethodPatch, "/api/v1/admp/accounts/"+ao, `{"timeout_ms":5000}`, e.client1); rec.Code != http.StatusNotFound {
			t.Errorf("client 1 PATCH of the organization-wide account = %d %s, want 404", rec.Code, rec.Body.String())
		}
		if rec := e.do(http.MethodDelete, "/api/v1/admp/accounts/"+ao, "", e.client1); rec.Code != http.StatusNotFound {
			t.Errorf("client 1 DELETE of the organization-wide account = %d %s, want 404", rec.Code, rec.Body.String())
		}
		if rec := e.do(http.MethodPatch, "/api/v1/admp/accounts/"+a1, `{"timeout_ms":5000}`, e.client1); rec.Code != http.StatusOK {
			t.Errorf("client 1 PATCH of its account = %d %s, want 200", rec.Code, rec.Body.String())
		}
		if rec := e.create("wp925-c1-second", map[string]any{"client_id": e.client1, "enabled": false}, e.client1); rec.Code != http.StatusCreated {
			t.Errorf("client 1 creates a disabled account of its own = %d %s, want 201", rec.Code, rec.Body.String())
		}
	})
}

// TestAdmpAccountOneActivePerClient: one enabled account per client and one
// enabled organization-wide account (409 enabled); AccountFor takes the
// client's, else the organization's, else ErrNoAccount.
func TestAdmpAccountOneActivePerClient(t *testing.T) {
	forEachStore(t, func(t *testing.T, e *accountEnv) {
		a1 := e.mustCreate("wp925-c1", map[string]any{"client_id": e.client1})
		if rec := e.create("wp925-c1-b", map[string]any{"client_id": e.client1}, ""); rec.Code != http.StatusConflict || field(rec) != "enabled" {
			t.Errorf("second enabled account of client 1 = %d %s, want 409 enabled", rec.Code, rec.Body.String())
		}
		spare := e.mustCreate("wp925-c1-spare", map[string]any{"client_id": e.client1, "enabled": false})
		if rec := e.do(http.MethodPatch, "/api/v1/admp/accounts/"+spare, `{"enabled":true}`, ""); rec.Code != http.StatusConflict {
			t.Errorf("enabling the second account of client 1 = %d %s, want 409", rec.Code, rec.Body.String())
		}
		ao := e.mustCreate("wp925-org", nil)
		if rec := e.create("wp925-org-b", nil, ""); rec.Code != http.StatusConflict {
			t.Errorf("second enabled organization-wide account = %d %s, want 409", rec.Code, rec.Body.String())
		}
		ctx := e.ctx("")
		if got, err := e.svc.AccountFor(ctx, e.org, e.client1); err != nil || got.ID != a1 {
			t.Errorf("AccountFor(client 1) = %v, %v, want its account", got, err)
		}
		if got, err := e.svc.AccountFor(ctx, e.org, e.client2); err != nil || got.ID != ao {
			t.Errorf("AccountFor(client 2) = %v, %v, want the organization-wide account", got, err)
		}
		if got, err := e.svc.AccountFor(ctx, e.org, ""); err != nil || got.ID != ao {
			t.Errorf("AccountFor(no client) = %v, %v, want the organization-wide account", got, err)
		}
		if rec := e.do(http.MethodPatch, "/api/v1/admp/accounts/"+ao, `{"enabled":false}`, ""); rec.Code != http.StatusOK {
			t.Fatalf("disable the organization-wide account = %d %s", rec.Code, rec.Body.String())
		}
		if _, err := e.svc.AccountFor(ctx, e.org, e.client2); !errors.Is(err, admp.ErrNoAccount) {
			t.Errorf("AccountFor(client 2) without organization-wide account = %v, want ErrNoAccount", err)
		}
	})
}

// TestAdmpAccountRejectsPrivateDestination: base_url and auth_url are
// absolute https URLs without query and fragment to an allowed destination
// (SEC-08): private, loopback, link-local metadata and internal names are
// 422 with the field.
func TestAdmpAccountRejectsPrivateDestination(t *testing.T) {
	forEachStore(t, func(t *testing.T, e *accountEnv) {
		cases := []struct{ member, url string }{
			{"base_url", "https://10.0.0.5/api"},
			{"base_url", "https://127.0.0.1/api"},
			{"base_url", "https://169.254.169.254/latest/meta-data"},
			{"base_url", "https://localhost/api"},
			{"base_url", "https://[::1]/api"},
			{"base_url", "http://admp.example.com/api"},
			{"base_url", "https://admp.example.com/api?x=1"},
			{"base_url", "https://admp.example.com/api#frag"},
			{"base_url", "https://user:pw@admp.example.com/api"},
			{"base_url", "/api"},
			{"auth_url", "https://192.168.1.1/token"},
			{"auth_url", "https://metadata.google.internal/token"},
		}
		for i, c := range cases {
			rec := e.create("wp925-dest", map[string]any{c.member: c.url}, "")
			if rec.Code != http.StatusUnprocessableEntity || field(rec) != c.member {
				t.Errorf("case %d %s=%s: %d %s, want 422 %s", i, c.member, c.url, rec.Code, rec.Body.String(), c.member)
			}
		}
	})
}

// TestAdmpAccountCredentialRules: a credential of another client, of
// another kind and a vault reference of a connection resolved by
// collectors are 422 credential_id; a vault reference resolved on the
// server is accepted. Deleting the credential sets credential_id NULL and
// last_error_class credential_missing (the foreign key on PostgreSQL, the
// service on the memory store); attaching a credential again clears it.
func TestAdmpAccountCredentialRules(t *testing.T) {
	forEachStore(t, func(t *testing.T, e *accountEnv) {
		ssh := e.createCredential(`{"name":"wp925-ssh","kind":"ssh_password","secret":{"username":"u","password":"p"}}`)
		token := e.createCredential(`{"name":"wp925-vault-login","kind":"vault_token","secret":{"token":"wp925-vault-token"}}`)
		collector := "6f0b6f0e-0000-4000-8000-000000925099"
		if e.f != nil {
			if err := e.f.Admin.QueryRow(context.Background(), `INSERT INTO collector (organization_id, name, status)
				VALUES ($1, 'wp925-collector', 'active') RETURNING id::text`, e.org).Scan(&collector); err != nil {
				t.Fatal(err)
			}
		}
		connection := func(name, resolveAt string) string {
			body := map[string]any{"name": name, "kind": "openbao_kv2", "address": "https://vault.example.com:8200", "mount": "secret",
				"auth_method": "token", "auth_credential_id": token, "resolve_at": resolveAt}
			if resolveAt == credential.ResolveAtCollector {
				body["collector_ids"] = []string{collector}
			}
			raw, _ := json.Marshal(body)
			rec := e.do(http.MethodPost, "/api/v1/vault-connections", string(raw), "")
			if rec.Code != http.StatusCreated {
				t.Fatalf("create vault connection: %d %s", rec.Code, rec.Body.String())
			}
			return idOf(t, rec)
		}
		ref := func(name, conn string) string {
			raw, _ := json.Marshal(map[string]any{"name": name, "kind": "api_token", "source": "vault_ref",
				"vault_ref": map[string]any{"connection_id": conn, "path": "admp/" + name, "field_map": map[string]string{"token": "token"}}})
			return e.createCredential(string(raw))
		}
		refCollector := ref("wp925-ref-collector", connection("wp925-at-collector", credential.ResolveAtCollector))
		refServer := ref("wp925-ref-server", connection("wp925-at-server", credential.ResolveAtServer))

		for name, extra := range map[string]map[string]any{
			"client-2 account with client-1 credential":   {"client_id": e.client2, "credential_id": e.cred1},
			"organization account with client credential": {"credential_id": e.cred1},
			"kind ssh_password":                           {"credential_id": ssh},
			"vault_ref resolved by collectors":            {"credential_id": refCollector},
			"unknown credential":                          {"credential_id": "6f0b6f0e-0000-4000-8000-00000000dead"},
			"malformed credential":                        {"credential_id": "x"},
		} {
			if rec := e.create("wp925-cred", extra, ""); rec.Code != http.StatusUnprocessableEntity || field(rec) != "credential_id" {
				t.Errorf("%s: %d %s, want 422 credential_id", name, rec.Code, rec.Body.String())
			}
		}
		withRef := e.create("wp925-ref", map[string]any{"credential_id": refServer}, "")
		if withRef.Code != http.StatusCreated {
			t.Fatalf("vault_ref resolved on the server: %d %s, want 201", withRef.Code, withRef.Body.String())
		}
		if a := decodeAccount(t, withRef); a.Credential == nil || a.Credential.Source != credential.SourceVaultRef {
			t.Errorf("account %+v, want the vault_ref credential reference", a)
		}
		mine := e.mustCreate("wp925-c1", map[string]any{"client_id": e.client1, "credential_id": e.cred1})
		if rec := e.do(http.MethodDelete, "/api/v1/credentials/"+e.cred1, "", ""); rec.Code != http.StatusNoContent {
			t.Fatalf("delete credential = %d %s", rec.Code, rec.Body.String())
		}
		missing := func(got admp.Account) bool {
			return got.CredentialID == "" && got.Credential == nil && got.LastErrorClass != nil &&
				*got.LastErrorClass == admp.ErrorCredentialMissing
		}
		if got := decodeAccount(t, e.do(http.MethodGet, "/api/v1/admp/accounts/"+mine, "", "")); !missing(got) {
			t.Errorf("account after the credential was deleted: %+v, want no credential and credential_missing", got)
		}
		var page struct {
			Data []admp.Account `json:"data"`
		}
		if err := json.Unmarshal(e.do(http.MethodGet, "/api/v1/admp/accounts", "", "").Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		for _, a := range page.Data {
			if a.ID == mine && !missing(a) {
				t.Errorf("listed account after the credential was deleted: %+v, want credential_missing", a)
			}
		}
		// Another member can be changed without a credential; a new
		// credential clears the mark.
		rec := e.do(http.MethodPatch, "/api/v1/admp/accounts/"+mine, `{"timeout_ms":4000}`, "")
		if rec.Code != http.StatusOK || !missing(decodeAccount(t, rec)) {
			t.Errorf("PATCH timeout_ms after the credential was deleted = %d %s, want 200 and credential_missing", rec.Code, rec.Body.String())
		}
		fresh := e.createCredential(`{"name":"wp925-c1-new","kind":"api_token","client_id":"` + e.client1 + `","secret":{"token":"` + canaryToken + `"}}`)
		rec = e.do(http.MethodPatch, "/api/v1/admp/accounts/"+mine, `{"credential_id":"`+fresh+`"}`, "")
		if got := decodeAccount(t, rec); rec.Code != http.StatusOK || got.CredentialID != fresh || got.LastErrorClass != nil {
			t.Errorf("PATCH with a new credential = %d %s, want the credential and no error class", rec.Code, rec.Body.String())
		}
	})
}

// TestAdmpAccountResponsesCarryNoSecret: no answer of the account and
// credential routes carries the credential's secret (checked in do); on
// PostgreSQL no column of admp_account does.
func TestAdmpAccountResponsesCarryNoSecret(t *testing.T) {
	forEachStore(t, func(t *testing.T, e *accountEnv) {
		id := e.mustCreate("wp925-secret", map[string]any{"credential_id": e.credOrg})
		e.do(http.MethodGet, "/api/v1/admp/accounts", "", "")
		e.do(http.MethodGet, "/api/v1/admp/accounts/"+id, "", "")
		rec := e.do(http.MethodPatch, "/api/v1/admp/accounts/"+id, `{"credential_id":"`+e.credOrg+`","timeout_ms":3000}`, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("PATCH = %d %s", rec.Code, rec.Body.String())
		}
		for _, member := range []string{"secret", "token", "password"} {
			if strings.Contains(rec.Body.String(), `"`+member+`"`) {
				t.Errorf("the answer has a member %q: %s", member, rec.Body.String())
			}
		}
		if e.f == nil {
			return
		}
		var row string
		if err := e.f.Admin.QueryRow(context.Background(), `SELECT row_to_json(a)::text FROM admp_account a WHERE id = $1`, id).Scan(&row); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(row, canaryToken) {
			t.Error("admp_account stores the secret of its credential")
		}
	})
}
