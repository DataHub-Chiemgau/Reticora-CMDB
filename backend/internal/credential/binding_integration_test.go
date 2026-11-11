package credential_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// bindingEnv is the credential API of organization A on PostgreSQL with a
// credential of client 1 (cred1), an org-wide one (credOrg), a CI and a
// subnet of each client.
type bindingEnv struct {
	t                *testing.T
	f                *scopetest.Fixture
	mux              *chi.Mux
	svc              *credential.Service
	cred1, credOrg   string
	ci1, ci2         string
	subnet1, subnet2 string
}

func newBindingEnv(t *testing.T) *bindingEnv {
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
	mux := chi.NewRouter()
	svc := credential.NewService(credential.NewPGRepository(f.App), enc)
	credential.NewHandler(svc).RegisterRoutes(mux)
	e := &bindingEnv{t: t, f: f, mux: mux, svc: svc}
	e.cred1 = e.createCredential(`{"name":"wp890-c1","kind":"snmp_v2c","client_id":"` + f.Client1 + `","secret":{"community":"x"}}`)
	e.credOrg = e.createCredential(`{"name":"wp890-org","kind":"ssh_password","secret":{"password":"x"}}`)
	e.ci1 = f.CI(t, f.OrgA, f.Client1, "wp890-ci-1")
	e.ci2 = f.CI(t, f.OrgA, f.Client2, "wp890-ci-2")
	e.subnet1 = e.subnet(f.Client1, "10.89.1.0/24")
	e.subnet2 = e.subnet(f.Client2, "10.89.2.0/24")
	return e
}

func (e *bindingEnv) subnet(clientID, cidr string) string {
	e.t.Helper()
	var id string
	if err := e.f.Admin.QueryRow(context.Background(), `INSERT INTO subnet (organization_id, client_id, cidr)
		VALUES ($1, $2, $3) RETURNING id::text`, e.f.OrgA, clientID, cidr).Scan(&id); err != nil {
		e.t.Fatalf("insert subnet: %v", err)
	}
	return id
}

// do sends a request as an org-wide principal, or restricted to clientID.
func (e *bindingEnv) do(method, path, body, clientID string) *httptest.ResponseRecorder {
	e.t.Helper()
	scope := database.OrgWideScope(e.f.OrgA, e.f.User)
	if clientID != "" {
		scope.Clients = database.ScopeIDs(clientID)
	}
	ctx := database.ContextWithTenantScope(context.Background(), &scope)
	ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: e.f.User})
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

func (e *bindingEnv) createCredential(body string) string {
	e.t.Helper()
	rec := e.do(http.MethodPost, "/api/v1/credentials", body, "")
	if rec.Code != http.StatusCreated {
		e.t.Fatalf("create credential: %d %s", rec.Code, rec.Body.String())
	}
	var c struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		e.t.Fatal(err)
	}
	return c.ID
}

func (e *bindingEnv) bind(credentialID, kind, target string, priority int) *httptest.ResponseRecorder {
	e.t.Helper()
	body, _ := json.Marshal(map[string]any{"credential_id": credentialID, "target_kind": kind, "target_id": target, "priority": priority})
	return e.do(http.MethodPost, "/api/v1/credential-bindings", string(body), "")
}

func idOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var b credential.Binding
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	return b.ID
}

// TestBindingClientRule covers CRD-01 (WP-890): a credential of client 1
// binds to a CI and a subnet of client 1 (201, scope of the target), not to
// those of client 2 (422 target_id, like an unknown target); the namespace
// follows the kind, a wrong one is 422.
func TestBindingClientRule(t *testing.T) {
	e := newBindingEnv(t)
	rec := e.bind(e.cred1, "ci", e.ci1, 100)
	if rec.Code != http.StatusCreated {
		t.Fatalf("bind to the own client's CI: %d %s", rec.Code, rec.Body.String())
	}
	var b credential.Binding
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if b.Namespace != "snmp" || b.ClientID != e.f.Client1 || b.TargetID != e.ci1 {
		t.Errorf("binding %+v, want namespace snmp and client 1", b)
	}
	if rec = e.bind(e.cred1, "subnet", e.subnet1, 100); rec.Code != http.StatusCreated {
		t.Errorf("bind to the own client's subnet: %d %s", rec.Code, rec.Body.String())
	}
	unknown := "00000000-0000-4000-8000-0000000000aa"
	var bodies []string
	for _, target := range []struct{ kind, id string }{{"ci", e.ci2}, {"subnet", e.subnet2}, {"ci", unknown}} {
		rec = e.bind(e.cred1, target.kind, target.id, 100)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"field":"target_id"`) {
			t.Errorf("bind to %s %s: %d %s, want 422 target_id", target.kind, target.id, rec.Code, rec.Body.String())
		}
		var problem struct {
			Detail string `json:"detail"`
			Field  string `json:"field"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &problem)
		bodies = append(bodies, problem.Field+"|"+strings.ReplaceAll(problem.Detail, target.id, ""))
	}
	if bodies[0] != bodies[2] {
		t.Errorf("a foreign and an unknown target answer differently: %q vs %q", bodies[0], bodies[2])
	}
	// The org-wide credential binds to any client's CI.
	if rec = e.bind(e.credOrg, "ci", e.ci2, 100); rec.Code != http.StatusCreated {
		t.Errorf("org-wide credential to client 2's CI: %d %s", rec.Code, rec.Body.String())
	}
	body := `{"credential_id":"` + e.credOrg + `","target_kind":"ci","target_id":"` + e.ci1 + `","namespace":"snmp"}`
	if rec = e.do(http.MethodPost, "/api/v1/credential-bindings", body, ""); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), `"field":"namespace"`) {
		t.Errorf("ssh credential with namespace snmp: %d %s", rec.Code, rec.Body.String())
	}
}

// TestBindingDeleteProtection covers AK5: a bound credential is not
// deleted (409 with its bindings); after deleting the binding it is.
func TestBindingDeleteProtection(t *testing.T) {
	e := newBindingEnv(t)
	rec := e.bind(e.cred1, "ci", e.ci1, 100)
	if rec.Code != http.StatusCreated {
		t.Fatalf("bind: %d %s", rec.Code, rec.Body.String())
	}
	binding := idOf(t, rec)
	rec = e.do(http.MethodDelete, "/api/v1/credentials/"+e.cred1, "", "")
	var problem struct {
		Bindings []string `json:"bindings"`
	}
	if rec.Code != http.StatusConflict || json.Unmarshal(rec.Body.Bytes(), &problem) != nil ||
		len(problem.Bindings) != 1 || problem.Bindings[0] != binding {
		t.Fatalf("delete a bound credential: %d %s", rec.Code, rec.Body.String())
	}
	if rec = e.do(http.MethodDelete, "/api/v1/credential-bindings/"+binding, "", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete binding: %d", rec.Code)
	}
	if rec = e.do(http.MethodDelete, "/api/v1/credentials/"+e.cred1, "", ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete the unbound credential: %d %s", rec.Code, rec.Body.String())
	}
	// The foreign key refuses too, without the handler's check.
	rec = e.bind(e.credOrg, "ci", e.ci1, 100)
	if rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}
	if _, err := e.f.Admin.Exec(context.Background(), `DELETE FROM credential WHERE id = $1`, e.credOrg); err == nil ||
		!strings.Contains(err.Error(), "credential_binding_credential_fkey") {
		t.Errorf("database delete of a bound credential: %v", err)
	}
}

// TestBindingClientPrincipalSeesOwnBindings covers AK6: a principal of
// client 1 lists only the bindings of client 1; a binding of client 2 is
// 404 for it.
func TestBindingClientPrincipalSeesOwnBindings(t *testing.T) {
	e := newBindingEnv(t)
	own := e.bind(e.credOrg, "ci", e.ci1, 100)
	foreign := e.bind(e.credOrg, "ci", e.ci2, 100)
	if own.Code != http.StatusCreated || foreign.Code != http.StatusCreated {
		t.Fatalf("bind: %d %d", own.Code, foreign.Code)
	}
	rec := e.do(http.MethodGet, "/api/v1/credential-bindings?limit=50", "", e.f.Client1)
	var page struct {
		Data []credential.Binding `json:"data"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &page) != nil {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	for _, b := range page.Data {
		if b.ClientID != e.f.Client1 {
			t.Errorf("client-1 principal sees binding %+v", b)
		}
	}
	if len(page.Data) != 1 {
		t.Errorf("client-1 principal sees %d bindings, want 1", len(page.Data))
	}
	if rec = e.do(http.MethodGet, "/api/v1/credential-bindings/"+idOf(t, foreign), "", e.f.Client1); rec.Code != http.StatusNotFound {
		t.Errorf("foreign binding: %d, want 404", rec.Code)
	}
	// Binding to a CI outside the principal's scope is 422 like unknown.
	body := `{"credential_id":"` + e.credOrg + `","target_kind":"ci","target_id":"` + e.ci2 + `","priority":5}`
	if rec = e.do(http.MethodPost, "/api/v1/credential-bindings", body, e.f.Client1); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("bind outside the scope: %d %s", rec.Code, rec.Body.String())
	}
}

// TestBindingUniquePerPriority covers AK1: a second binding of the target
// for the namespace with the same priority is 409; another priority is fine.
func TestBindingUniquePerPriority(t *testing.T) {
	e := newBindingEnv(t)
	if rec := e.bind(e.credOrg, "ci", e.ci1, 10); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}
	if rec := e.bind(e.credOrg, "ci", e.ci1, 10); rec.Code != http.StatusConflict {
		t.Errorf("same priority: %d %s, want 409", rec.Code, rec.Body.String())
	}
	if rec := e.bind(e.credOrg, "ci", e.ci1, 20); rec.Code != http.StatusCreated {
		t.Errorf("other priority: %d %s", rec.Code, rec.Body.String())
	}
}

// TestBindingDeviceGroupAndProbeTargetsUnsupported covers AK4.
func TestBindingDeviceGroupAndProbeTargetsUnsupported(t *testing.T) {
	e := newBindingEnv(t)
	for _, kind := range []string{"device_group", "probe"} {
		rec := e.bind(e.credOrg, kind, e.ci1, 100)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "target kind not supported yet") ||
			!strings.Contains(rec.Body.String(), `"field":"target_kind"`) {
			t.Errorf("%s: %d %s", kind, rec.Code, rec.Body.String())
		}
	}
}

// TestCIBindingsReplace covers AK2: PUT /cis/{id}/credentials replaces the
// CI's bindings atomically; a refused entry leaves the former bindings.
func TestCIBindingsReplace(t *testing.T) {
	e := newBindingEnv(t)
	path := "/api/v1/cis/" + e.ci1 + "/credentials"
	put := func(body string) *httptest.ResponseRecorder { return e.do(http.MethodPut, path, body, "") }
	rec := put(`{"bindings":[{"credential_id":"` + e.cred1 + `","priority":1},{"credential_id":"` + e.credOrg + `","priority":2}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("replace: %d %s", rec.Code, rec.Body.String())
	}
	list := func() []credential.Binding {
		r := e.do(http.MethodGet, path, "", "")
		var set struct {
			Data []credential.Binding `json:"data"`
		}
		if r.Code != http.StatusOK || json.Unmarshal(r.Body.Bytes(), &set) != nil {
			t.Fatalf("list: %d %s", r.Code, r.Body.String())
		}
		return set.Data
	}
	if got := list(); len(got) != 2 {
		t.Fatalf("bindings after the replacement: %+v", got)
	}
	// A refused entry (wrong namespace) rolls the whole replacement back.
	rec = put(`{"bindings":[{"credential_id":"` + e.credOrg + `","priority":7},{"credential_id":"` + e.cred1 + `","namespace":"ssh"}]}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `bindings[1].namespace`) {
		t.Errorf("refused replacement: %d %s", rec.Code, rec.Body.String())
	}
	if got := list(); len(got) != 2 {
		t.Errorf("a refused replacement changed the bindings: %+v", got)
	}
	if rec = put(`{"bindings":[]}`); rec.Code != http.StatusOK || len(list()) != 0 {
		t.Errorf("empty replacement: %d, %d left", rec.Code, len(list()))
	}
	if rec = e.do(http.MethodGet, "/api/v1/cis/"+e.ci2+"/credentials", "", e.f.Client1); rec.Code != http.StatusNotFound {
		t.Errorf("CI outside the scope: %d, want 404", rec.Code)
	}
}
