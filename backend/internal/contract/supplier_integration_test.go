package contract_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/contract"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Retest (WP-736, MGT-12, API-06):
//
//	cd backend && go test -count=1 -p 1 -run 'TestSupplier' ./internal/contract/

const suppliers = "/api/v1/suppliers"

type supplierWorld struct {
	f   *scopetest.Fixture
	mux *chi.Mux
}

func newSupplierWorld(t *testing.T) *supplierWorld {
	t.Helper()
	f := scopetest.Seed(t, "73")
	mux := chi.NewRouter()
	contract.NewHandler(contract.NewPGRepository(f.App)).RegisterRoutes(mux)
	return &supplierWorld{f: f, mux: mux}
}

// request is one call of a principal: ctx carries its tenant scope.
type request struct {
	ctx            context.Context
	org            string
	method, path   string
	body, ifMatch  string
	wantStatus     int
	wantProblemFor string
}

func (w *supplierWorld) do(t *testing.T, r *request) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(r.method, r.path, bytes.NewBufferString(r.body))
	if r.ifMatch != "" {
		req.Header.Set("If-Match", r.ifMatch)
	}
	ctx, org := r.ctx, r.org
	if ctx == nil {
		ctx, org = w.f.OrgCtx(w.f.OrgA), w.f.OrgA
	}
	req = req.WithContext(tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: org, UserID: w.f.User}))
	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, req)
	if r.wantStatus != 0 && rec.Code != r.wantStatus {
		t.Fatalf("%s %s: got %d %s, want %d", r.method, r.path, rec.Code, rec.Body.String(), r.wantStatus)
	}
	if r.wantProblemFor != "" {
		var p api.FieldProblem
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Field != r.wantProblemFor {
			t.Fatalf("%s %s: problem %s, want field %q", r.method, r.path, rec.Body.String(), r.wantProblemFor)
		}
	}
	return rec
}

func decodeSupplier(t *testing.T, rec *httptest.ResponseRecorder) contract.Supplier {
	t.Helper()
	var s contract.Supplier
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatalf("decode supplier %s: %v", rec.Body.String(), err)
	}
	if got, want := rec.Header().Get("ETag"), api.ETag(s.Version); got != want {
		t.Fatalf("ETag %q, want %q", got, want)
	}
	return s
}

func (w *supplierWorld) create(t *testing.T, body string) contract.Supplier {
	t.Helper()
	return decodeSupplier(t, w.do(t, &request{method: http.MethodPost, path: suppliers, body: body, wantStatus: http.StatusCreated}))
}

// contactOf inserts a contact of orgID through the maintenance pool.
func (w *supplierWorld) contactOf(t *testing.T, orgID, clientID string) string {
	t.Helper()
	id := w.f.ID()
	var client any
	if clientID != "" {
		client = clientID
	}
	if _, err := w.f.Admin.Exec(context.Background(), `INSERT INTO contact (id, organization_id, client_id, display_name) VALUES ($1, $2, $3, 'Ansprechpartner')`,
		id, orgID, client); err != nil {
		t.Fatalf("insert contact: %v", err)
	}
	return id
}

type supplierPage struct {
	Data       []contract.Supplier `json:"data"`
	Total      int                 `json:"total"`
	NextCursor string              `json:"next_cursor"`
}

func (w *supplierWorld) list(t *testing.T, ctx context.Context, org, query string) supplierPage {
	t.Helper()
	rec := w.do(t, &request{ctx: ctx, org: org, method: http.MethodGet, path: suppliers + query, wantStatus: http.StatusOK})
	var page supplierPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode list %s: %v", rec.Body.String(), err)
	}
	return page
}

func names(page supplierPage) map[string]bool {
	out := map[string]bool{}
	for i := range page.Data {
		out[page.Data[i].Name] = true
	}
	return out
}

// TestSupplierCRUD covers MGT-12 and API-06 (WP-736): create, read, list,
// update and delete over PostgreSQL with the version as ETag; a duplicate
// name in the organization is 409, in another organization allowed;
// client_id cannot change; deleting the contact sets contact_id to NULL.
func TestSupplierCRUD(t *testing.T) {
	w := newSupplierWorld(t)
	contactID := w.contactOf(t, w.f.OrgA, "")

	s := w.create(t, `{"name":"  Muster GmbH ","external_ref":"K-1001","contact_id":"`+contactID+`","notes":"Rahmenvertrag"}`)
	if s.Name != "Muster GmbH" || s.Version != 1 || s.ClientID != nil || s.ContactID == nil || *s.ContactID != contactID ||
		s.ExternalRef == nil || *s.ExternalRef != "K-1001" || s.OrganizationID != w.f.OrgA {
		t.Fatalf("created: %+v", s)
	}
	path := suppliers + "/" + s.ID
	if got := decodeSupplier(t, w.do(t, &request{method: http.MethodGet, path: path, wantStatus: http.StatusOK})); got.Name != s.Name {
		t.Fatalf("get: %+v", got)
	}

	w.do(t, &request{method: http.MethodPost, path: suppliers, body: `{"name":"Muster GmbH"}`, wantStatus: http.StatusConflict, wantProblemFor: "name"})
	w.do(t, &request{method: http.MethodPost, path: suppliers, body: `{"name":" "}`, wantStatus: http.StatusBadRequest, wantProblemFor: "name"})
	w.do(t, &request{method: http.MethodPost, path: suppliers, body: `{"name":"x","client_id":"c1"}`, wantStatus: http.StatusBadRequest, wantProblemFor: "client_id"})
	other := decodeSupplier(t, w.do(t, &request{ctx: w.f.OrgCtx(w.f.OrgB), org: w.f.OrgB, method: http.MethodPost, path: suppliers,
		body: `{"name":"Muster GmbH"}`, wantStatus: http.StatusCreated}))
	if other.OrganizationID != w.f.OrgB {
		t.Fatalf("org B supplier: %+v", other)
	}
	// Another organization sees nothing of the first one.
	for _, m := range []string{http.MethodGet, http.MethodPatch, http.MethodDelete} {
		w.do(t, &request{ctx: w.f.OrgCtx(w.f.OrgB), org: w.f.OrgB, method: m, path: path, body: `{"name":"stolen"}`, wantStatus: http.StatusNotFound})
	}

	patched := decodeSupplier(t, w.do(t, &request{method: http.MethodPatch, path: path, ifMatch: api.ETag(1),
		body: `{"name":"Muster AG","external_ref":null,"notes":"neu"}`, wantStatus: http.StatusOK}))
	if patched.Name != "Muster AG" || patched.Version != 2 || patched.ExternalRef != nil || patched.Notes == nil || *patched.Notes != "neu" ||
		patched.ContactID == nil {
		t.Fatalf("patched: %+v", patched)
	}
	w.do(t, &request{method: http.MethodPatch, path: path, body: `{"client_id":"` + w.f.Client1 + `"}`, wantStatus: http.StatusBadRequest, wantProblemFor: "client_id"})
	taken := w.create(t, `{"name":"Zweitlieferant"}`)
	w.do(t, &request{method: http.MethodPatch, path: suppliers + "/" + taken.ID, body: `{"name":"Muster AG"}`, wantStatus: http.StatusConflict, wantProblemFor: "name"})

	// Deleting the contact clears only the reference.
	if _, err := w.f.Admin.Exec(context.Background(), `DELETE FROM contact WHERE id = $1`, contactID); err != nil {
		t.Fatalf("delete contact: %v", err)
	}
	if got := decodeSupplier(t, w.do(t, &request{method: http.MethodGet, path: path, wantStatus: http.StatusOK})); got.ContactID != nil || got.Name != "Muster AG" {
		t.Fatalf("after contact delete: %+v", got)
	}

	w.do(t, &request{method: http.MethodDelete, path: path, wantStatus: http.StatusNoContent})
	w.do(t, &request{method: http.MethodGet, path: path, wantStatus: http.StatusNotFound})
	w.do(t, &request{method: http.MethodDelete, path: path, wantStatus: http.StatusNotFound})
}

// TestSupplierIfMatch covers API-07 on suppliers (WP-736): an outdated
// If-Match is 412, a missing one too when the organization requires it.
func TestSupplierIfMatch(t *testing.T) {
	w := newSupplierWorld(t)
	s := w.create(t, `{"name":"Versioniert"}`)
	path := suppliers + "/" + s.ID
	decodeSupplier(t, w.do(t, &request{method: http.MethodPatch, path: path, body: `{"notes":"a"}`, wantStatus: http.StatusOK}))
	w.do(t, &request{method: http.MethodPatch, path: path, ifMatch: api.ETag(1), body: `{"notes":"b"}`, wantStatus: http.StatusPreconditionFailed})
	w.do(t, &request{method: http.MethodPatch, path: path, ifMatch: "kein-tag", body: `{"notes":"b"}`, wantStatus: http.StatusPreconditionFailed})

	if _, err := w.f.Admin.Exec(context.Background(),
		`UPDATE organization SET settings = COALESCE(settings, '{}'::jsonb) || '{"require_if_match": true}' WHERE id = $1`, w.f.OrgA); err != nil {
		t.Fatalf("require if-match: %v", err)
	}
	w.do(t, &request{method: http.MethodPatch, path: path, body: `{"notes":"c"}`, wantStatus: http.StatusPreconditionFailed})
	got := decodeSupplier(t, w.do(t, &request{method: http.MethodPatch, path: path, ifMatch: api.ETag(2), body: `{"notes":"c"}`, wantStatus: http.StatusOK}))
	if got.Version != 3 || got.Notes == nil || *got.Notes != "c" {
		t.Fatalf("after if-match: %+v", got)
	}
	decodeSupplier(t, w.do(t, &request{method: http.MethodPatch, path: path, ifMatch: "*", body: `{"notes":"d"}`, wantStatus: http.StatusOK}))
}

// TestSupplierClientScope covers E-09 on suppliers (WP-736): a principal
// of client 1 sees org-wide suppliers and those of client 1, not those of
// client 2; it creates suppliers of client 1 only and cannot change or
// delete an org-wide one.
func TestSupplierClientScope(t *testing.T) {
	w := newSupplierWorld(t)
	orgWide := w.create(t, `{"name":"Org-weit"}`)
	w.create(t, `{"name":"Mandant 1","client_id":"`+w.f.Client1+`"}`)
	c2 := w.create(t, `{"name":"Mandant 2","client_id":"`+w.f.Client2+`"}`)

	c1Ctx := w.f.ClientCtx(w.f.Client1)
	seen := names(w.list(t, c1Ctx, w.f.OrgA, ""))
	if !seen["Org-weit"] || !seen["Mandant 1"] || seen["Mandant 2"] || len(seen) != 2 {
		t.Fatalf("client 1 sees %v", seen)
	}
	asC1 := func(method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		return w.do(t, &request{ctx: c1Ctx, org: w.f.OrgA, method: method, path: path, body: body, wantStatus: want})
	}
	asC1(http.MethodGet, suppliers+"/"+c2.ID, "", http.StatusNotFound)
	asC1(http.MethodGet, suppliers+"/"+orgWide.ID, "", http.StatusOK)
	asC1(http.MethodPost, suppliers, `{"name":"Neu org-weit"}`, http.StatusForbidden)
	asC1(http.MethodPost, suppliers, `{"name":"Neu Mandant 2","client_id":"`+w.f.Client2+`"}`, http.StatusForbidden)
	own := decodeSupplier(t, asC1(http.MethodPost, suppliers, `{"name":"Neu Mandant 1","client_id":"`+w.f.Client1+`"}`, http.StatusCreated))
	if own.ClientID == nil || *own.ClientID != w.f.Client1 {
		t.Fatalf("client 1 supplier: %+v", own)
	}
	asC1(http.MethodPatch, suppliers+"/"+orgWide.ID, `{"notes":"x"}`, http.StatusNotFound)
	asC1(http.MethodDelete, suppliers+"/"+orgWide.ID, "", http.StatusNotFound)
	asC1(http.MethodPatch, suppliers+"/"+own.ID, `{"notes":"x"}`, http.StatusOK)
	if got := decodeSupplier(t, w.do(t, &request{method: http.MethodGet, path: suppliers + "/" + orgWide.ID, wantStatus: http.StatusOK})); got.Notes != nil {
		t.Fatalf("org-wide supplier changed: %+v", got)
	}

	// A contact of client 2 is invisible to client 1: 422.
	c2Contact := w.contactOf(t, w.f.OrgA, w.f.Client2)
	w.do(t, &request{ctx: c1Ctx, org: w.f.OrgA, method: http.MethodPost, path: suppliers,
		body:       `{"name":"Mit fremdem Kontakt","client_id":"` + w.f.Client1 + `","contact_id":"` + c2Contact + `"}`,
		wantStatus: http.StatusUnprocessableEntity, wantProblemFor: "contact_id"})
}

// TestSupplierForeignReferences covers K19 on suppliers (WP-736): a client
// or a contact of another organization is 422 on the field.
func TestSupplierForeignReferences(t *testing.T) {
	w := newSupplierWorld(t)
	foreignClient := w.f.ID()
	if _, err := w.f.Admin.Exec(context.Background(), `INSERT INTO client (id, organization_id, name, slug) VALUES ($1, $2, 'Fremd', $3)`,
		foreignClient, w.f.OrgB, "scopetest-73-foreign"); err != nil {
		t.Fatalf("insert foreign client: %v", err)
	}
	foreignContact := w.contactOf(t, w.f.OrgB, "")
	w.do(t, &request{method: http.MethodPost, path: suppliers, body: `{"name":"F1","client_id":"` + foreignClient + `"}`,
		wantStatus: http.StatusUnprocessableEntity, wantProblemFor: "client_id"})
	w.do(t, &request{method: http.MethodPost, path: suppliers, body: `{"name":"F2","contact_id":"` + foreignContact + `"}`,
		wantStatus: http.StatusUnprocessableEntity, wantProblemFor: "contact_id"})
	s := w.create(t, `{"name":"F3"}`)
	w.do(t, &request{method: http.MethodPatch, path: suppliers + "/" + s.ID, body: `{"contact_id":"` + foreignContact + `"}`,
		wantStatus: http.StatusUnprocessableEntity, wantProblemFor: "contact_id"})
}

// TestSupplierListFilterAndCursor covers API-02 on suppliers (WP-736):
// filter[name], filter[client_id], sort and the keyset cursor return every
// matching row once; an unknown filter or sort field is 422, a cursor of
// another sort 400.
func TestSupplierListFilterAndCursor(t *testing.T) {
	w := newSupplierWorld(t)
	for _, name := range []string{"Alpha Elektro", "Beta Elektro", "Gamma Elektro", "Delta Bau", "Epsilon Elektro"} {
		w.create(t, `{"name":"`+name+`"}`)
	}
	w.create(t, `{"name":"Zeta Elektro","client_id":"`+w.f.Client1+`","external_ref":"Z-1"}`)

	ctx := w.f.OrgCtx(w.f.OrgA)
	for _, c := range []struct {
		sort string
		want []string
	}{
		{"", []string{"Alpha Elektro", "Beta Elektro", "Epsilon Elektro", "Gamma Elektro", "Zeta Elektro"}},
		{"-name", []string{"Zeta Elektro", "Gamma Elektro", "Epsilon Elektro", "Beta Elektro", "Alpha Elektro"}},
		{"created_at", []string{"Alpha Elektro", "Beta Elektro", "Gamma Elektro", "Epsilon Elektro", "Zeta Elektro"}},
		{"-created_at", []string{"Zeta Elektro", "Epsilon Elektro", "Gamma Elektro", "Beta Elektro", "Alpha Elektro"}},
	} {
		q := url.Values{"filter[name][like]": {"Elektro"}, "limit": {"2"}}
		if c.sort != "" {
			q.Set("sort", c.sort)
		}
		var got []string
		next := "?" + q.Encode()
		for pages := 0; next != ""; pages++ {
			if pages > 5 {
				t.Fatalf("sort %q: cursor does not end", c.sort)
			}
			page := w.list(t, ctx, w.f.OrgA, next)
			if page.Total != 5 || len(page.Data) > 2 {
				t.Fatalf("sort %q: total %d, %d rows", c.sort, page.Total, len(page.Data))
			}
			for i := range page.Data {
				got = append(got, page.Data[i].Name)
			}
			next = ""
			if page.NextCursor != "" {
				q.Set("cursor", page.NextCursor)
				next = "?" + q.Encode()
			}
		}
		if len(got) != len(c.want) {
			t.Fatalf("sort %q: %v, want %v", c.sort, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("sort %q: %v, want %v", c.sort, got, c.want)
			}
		}
	}

	if page := w.list(t, ctx, w.f.OrgA, "?filter[client_id]="+w.f.Client1); len(page.Data) != 1 || page.Data[0].Name != "Zeta Elektro" {
		t.Fatalf("filter client_id: %+v", page.Data)
	}
	if page := w.list(t, ctx, w.f.OrgA, "?filter[external_ref]=Z-1"); len(page.Data) != 1 {
		t.Fatalf("filter external_ref: %+v", page.Data)
	}
	w.do(t, &request{method: http.MethodGet, path: suppliers + "?filter[notes]=x", wantStatus: http.StatusUnprocessableEntity})
	w.do(t, &request{method: http.MethodGet, path: suppliers + "?sort=version", wantStatus: http.StatusUnprocessableEntity})
	first := w.list(t, ctx, w.f.OrgA, "?limit=1")
	w.do(t, &request{method: http.MethodGet, path: suppliers + "?sort=-name&cursor=" + url.QueryEscape(first.NextCursor), wantStatus: http.StatusBadRequest})
	w.do(t, &request{method: http.MethodGet, path: suppliers + "?cursor=forged", wantStatus: http.StatusBadRequest})
}
