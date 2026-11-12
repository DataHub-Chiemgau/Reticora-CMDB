package credential_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

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

// group inserts a monitoring group of orgID (clientID empty: org-wide).
func (e *bindingEnv) group(orgID, clientID, name string, priority int) string {
	e.t.Helper()
	var client any
	if clientID != "" {
		client = clientID
	}
	return e.scalar(`INSERT INTO monitoring_group (organization_id, client_id, name, priority) VALUES ($1, $2, $3, $4) RETURNING id::text`,
		orgID, client, name, priority)
}

// member adds the CI to the group.
func (e *bindingEnv) member(orgID, groupID, ciID string) {
	e.t.Helper()
	e.exec(`INSERT INTO monitoring_group_member (organization_id, group_id, ci_id) VALUES ($1, $2, $3)`, orgID, groupID, ciID)
}

// probe inserts an icmp probe on the CI.
func (e *bindingEnv) probe(orgID, ciID, target string) string {
	e.t.Helper()
	return e.scalar(`INSERT INTO probe (organization_id, ci_id, type, target) VALUES ($1, $2, 'icmp', $3) RETURNING id::text`,
		orgID, ciID, target)
}

// TestBindingDeviceGroupAndProbeTargets covers CRD-01 (WP-935 AK7): a
// credential binds to a monitoring group (device group) and to a probe of
// the organization (201, scope of the target); a group or probe of another
// organization is 422 target_id through the API and 23503 in the database;
// a credential of client 1 does not bind to the group or probe of client 2
// (422 like an unknown target).
func TestBindingDeviceGroupAndProbeTargets(t *testing.T) {
	e := newBindingEnv(t)
	ctx := context.Background()
	group1 := e.group(e.f.OrgA, e.f.Client1, "wp935-group-c1", 0)
	group2 := e.group(e.f.OrgA, e.f.Client2, "wp935-group-c2", 0)
	orgGroup := e.group(e.f.OrgA, "", "wp935-group-org", 0)
	probe1 := e.probe(e.f.OrgA, e.ci1, "10.89.1.10")
	probe2 := e.probe(e.f.OrgA, e.ci2, "10.89.2.10")
	ciB := e.f.CI(t, e.f.OrgB, "", "wp935-ci-b")
	groupB := e.group(e.f.OrgB, "", "wp935-group-b", 0)
	probeB := e.probe(e.f.OrgB, ciB, "10.89.9.10")

	check := func(rec *httptest.ResponseRecorder, kind, target, client string) {
		t.Helper()
		if rec.Code != http.StatusCreated {
			t.Fatalf("bind to %s %s: %d %s", kind, target, rec.Code, rec.Body.String())
		}
		var b credential.Binding
		if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
			t.Fatal(err)
		}
		if b.TargetKind != kind || b.TargetID != target || b.ClientID != client {
			t.Errorf("binding %+v, want %s %s of client %q", b, kind, target, client)
		}
	}
	check(e.bind(e.cred1, "device_group", group1, 100), "device_group", group1, e.f.Client1)
	check(e.bind(e.credOrg, "device_group", orgGroup, 100), "device_group", orgGroup, "")
	check(e.bind(e.cred1, "probe", probe1, 100), "probe", probe1, e.f.Client1)
	check(e.bind(e.credOrg, "probe", probe2, 100), "probe", probe2, e.f.Client2)
	if rec := e.bind(e.cred1, "device_group", group1, 100); rec.Code != http.StatusConflict {
		t.Errorf("second binding of the group with the same priority: %d %s, want 409", rec.Code, rec.Body.String())
	}

	// The filter by target finds the probe binding only.
	rec := e.do(http.MethodGet, "/api/v1/credential-bindings?target_id="+probe1, "", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), probe1) || strings.Contains(rec.Body.String(), group1) {
		t.Errorf("list by probe: %d %s", rec.Code, rec.Body.String())
	}

	// Another organization's group or probe: 422 target_id through the API.
	for kind, target := range map[string]string{"device_group": groupB, "probe": probeB} {
		rec := e.bind(e.credOrg, kind, target, 100)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"field":"target_id"`) {
			t.Errorf("%s of organization B: %d %s, want 422 target_id", kind, rec.Code, rec.Body.String())
		}
	}
	// ... and 23503 in the database (composite foreign keys).
	for _, c := range []struct{ column, kind, target string }{
		{"device_group_id", "device_group", groupB}, {"probe_id", "probe", probeB},
	} {
		_, err := e.f.Admin.Exec(ctx, `INSERT INTO credential_binding (organization_id, credential_id, target_kind, `+c.column+`, namespace)
			VALUES ($1, $2, $3, $4, 'ssh')`, e.f.OrgA, e.credOrg, c.kind, c.target)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
			t.Errorf("%s of organization B in the database: %v, want 23503", c.column, err)
		}
	}
	// A credential of client 1 on the group or probe of client 2: 422.
	for kind, target := range map[string]string{"device_group": group2, "probe": probe2} {
		rec := e.bind(e.cred1, kind, target, 50)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"field":"target_id"`) {
			t.Errorf("credential of client 1 on %s of client 2: %d %s, want 422 target_id", kind, rec.Code, rec.Body.String())
		}
	}
	// The target column must match the kind (CHECK).
	if _, err := e.f.Admin.Exec(ctx, `INSERT INTO credential_binding (organization_id, credential_id, target_kind, ci_id, namespace)
		VALUES ($1, $2, 'probe', $3, 'ssh')`, e.f.OrgA, e.credOrg, e.ci1); err == nil || !strings.Contains(err.Error(), "credential_binding_target_check") {
		t.Errorf("probe binding with ci_id: %v, want credential_binding_target_check", err)
	}
	// Deleting the probe removes its bindings.
	e.exec(`DELETE FROM probe WHERE id = $1`, probe1)
	if n := e.scalar(`SELECT count(*)::text FROM credential_binding WHERE probe_id = $1`, probe1); n != "0" {
		t.Errorf("bindings of a deleted probe: %s", n)
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
