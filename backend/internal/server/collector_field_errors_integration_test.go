package server_test

// WP-694 (API-03, COL-01, WP-144, WP-677; F-423): POST /api/v1/collectors
// and PATCH /api/v1/collectors/{id} keep the field error contract: a value
// the validation or the repository refuses (*api.FieldError) is 422
// application/problem+json naming the field (site_id, name, status) and
// nothing is stored, instead of 500. Through the complete server.NewRouter
// (authorization and OpenAPI validation, newAuditT3Router) against
// PostgreSQL, as an org-wide principal and as a principal holding
// collector:manage in client 1 only:
//   - POST with an unknown site_id, and with client_id client 1 and a site of
//     client 2: 422 on site_id, no collector created;
//   - PATCH with name "", status "bogus", status "paused" (only disabled
//     and active can be set), an unknown site_id and a site of another
//     client: 422 on name, status or site_id, the collector unchanged;
//   - the client-1 principal learns nothing about the site of client 2: its
//     refusal reads as for an unknown site;
//   - the controls (valid POST and PATCH) stay 201/200.
// Before the fix every one of these refusals answered 500.
//
//	cd backend && TEST_DATABASE_URL=... go test -count=1 -run '^TestCollectorWritesKeepFieldErrorContract$' ./internal/server/

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

func TestCollectorWritesKeepFieldErrorContract(t *testing.T) {
	r := newAuditT3Router(t, "6e")
	f := r.f
	all := identity.ScopeSet{All: true}
	orgWide := identity.Principal{Subject: r.user, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser,
		Scope: &identity.Scope{Clients: all, Sites: all, Teams: all}, Permissions: identity.AllPermissions(), Credential: "wp694"}
	client1 := identity.Principal{Subject: r.other, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser,
		Scope:       &identity.Scope{Clients: identity.ScopeSet{IDs: []string{f.Client1}}, Sites: all, Teams: all},
		Permissions: []identity.Permission{identity.PermCollectorManage, identity.PermDiscoveryRead}, Credential: "wp694-c1"}
	client1.ClientScope = client1.Scope.LegacyClientScope()
	call := func(p identity.Principal, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), p.Permissions)
		w := httptest.NewRecorder()
		r.router.ServeHTTP(w, req.WithContext(ctx))
		return w
	}
	// refused checks a 422 problem on field and returns its detail.
	refused := func(what string, w *httptest.ResponseRecorder, field string) string {
		t.Helper()
		var problem struct {
			Field  string `json:"field"`
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &problem)
		if w.Code != http.StatusUnprocessableEntity || problem.Field != field {
			t.Errorf("%s: %d field %q, want 422 on %s; body %.240s", what, w.Code, problem.Field, field, w.Body.String())
			return problem.Detail
		}
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
			t.Errorf("%s: Content-Type %q, want application/problem+json", what, ct)
		}
		return problem.Detail
	}
	site1 := r.str(`INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, 'wp694-site-c1') RETURNING id::text`, f.OrgA, f.Client1)
	site2 := r.str(`INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, 'wp694-site-c2') RETURNING id::text`, f.OrgA, f.Client2)
	const unknown = "00000000-0000-4000-8000-0000000006e2"
	count := func() string {
		return r.str(`SELECT count(*)::text FROM collector WHERE organization_id = $1`, f.OrgA)
	}

	// POST as the org-wide and as the client-1 principal.
	before := count()
	refused("POST, unknown site_id", call(orgWide, http.MethodPost, "/api/v1/collectors",
		`{"name":"wp694-a","site_id":"`+unknown+`"}`), "site_id")
	refused("POST, client 1 with a site of client 2", call(orgWide, http.MethodPost, "/api/v1/collectors",
		`{"name":"wp694-b","client_id":"`+f.Client1+`","site_id":"`+site2+`"}`), "site_id")
	foreign := refused("POST as client 1, site of client 2", call(client1, http.MethodPost, "/api/v1/collectors",
		`{"name":"wp694-c","client_id":"`+f.Client1+`","site_id":"`+site2+`"}`), "site_id")
	missing := refused("POST as client 1, unknown site", call(client1, http.MethodPost, "/api/v1/collectors",
		`{"name":"wp694-d","client_id":"`+f.Client1+`","site_id":"`+unknown+`"}`), "site_id")
	if foreign != missing {
		t.Errorf("client-1 POST: the site of client 2 reads %q, an unknown site %q; want the same answer", foreign, missing)
	}
	if after := count(); after != before {
		t.Errorf("collectors after the refused POSTs: %s, want %s", after, before)
	}
	w := call(orgWide, http.MethodPost, "/api/v1/collectors", `{"name":"wp694-ok","client_id":"`+f.Client1+`","site_id":"`+site1+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("control POST: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("control POST: %v %s", err, w.Body.String())
	}
	if w := call(client1, http.MethodPost, "/api/v1/collectors", `{"name":"wp694-c1ok","client_id":"`+f.Client1+`"}`); w.Code != http.StatusCreated {
		t.Errorf("control POST as client 1: %d %s", w.Code, w.Body.String())
	}

	// PATCH of the collector in client 1.
	path := "/api/v1/collectors/" + created.ID
	state := func() string {
		return r.str(`SELECT format('name=%s site=%s client=%s status=%s', name, coalesce(site_id::text, '<null>'), coalesce(client_id::text, '<null>'), status)
			FROM collector WHERE id = $1`, created.ID)
	}
	unchanged := state()
	for _, c := range []struct {
		what, body, field string
		p                 identity.Principal
	}{
		{"name empty", `{"name":""}`, "name", orgWide},
		{"status bogus", `{"status":"bogus"}`, "status", orgWide},
		{"status paused", `{"status":"paused"}`, "status", orgWide},
		{"unknown site_id", `{"site_id":"` + unknown + `"}`, "site_id", orgWide},
		{"site of client 2", `{"site_id":"` + site2 + `"}`, "site_id", orgWide},
		{"name empty as client 1", `{"name":""}`, "name", client1},
		{"status bogus as client 1", `{"status":"bogus"}`, "status", client1},
	} {
		refused("PATCH, "+c.what, call(c.p, http.MethodPatch, path, c.body), c.field)
	}
	foreign = refused("PATCH as client 1, site of client 2", call(client1, http.MethodPatch, path, `{"site_id":"`+site2+`"}`), "site_id")
	missing = refused("PATCH as client 1, unknown site", call(client1, http.MethodPatch, path, `{"site_id":"`+unknown+`"}`), "site_id")
	if foreign != missing {
		t.Errorf("client-1 PATCH: the site of client 2 reads %q, an unknown site %q; want the same answer", foreign, missing)
	}
	if got := state(); got != unchanged {
		t.Errorf("collector after the refused PATCHes: %s, want unchanged %s", got, unchanged)
	}
	if w := call(orgWide, http.MethodPatch, path, `{"name":"wp694-renamed","status":"active"}`); w.Code != http.StatusOK {
		t.Errorf("control PATCH: %d %s", w.Code, w.Body.String())
	}
	if w := call(client1, http.MethodPatch, path, `{"site_id":"`+site1+`"}`); w.Code != http.StatusOK {
		t.Errorf("control PATCH as client 1: %d %s", w.Code, w.Body.String())
	}
}
