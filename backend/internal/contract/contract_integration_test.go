package contract_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/contract"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
)

// Retest (WP-269, MGT-12, API-06):
//
//	cd backend && go test -count=1 -p 1 -run 'TestContract' ./internal/contract/

const contracts = "/api/v1/contracts"

// newContractWorld mounts the supplier and the contract routes on one
// router over PostgreSQL.
func newContractWorld(t *testing.T) *supplierWorld {
	t.Helper()
	f := scopetest.Seed(t, "73")
	mux := chi.NewRouter()
	contract.NewHandler(contract.NewPGRepository(f.App)).RegisterRoutes(mux)
	contract.NewContractHandler(contract.NewPGContracts(f.App)).RegisterRoutes(mux)
	return &supplierWorld{f: f, mux: mux}
}

func decodeContract(t *testing.T, rec *httptest.ResponseRecorder) contract.Contract {
	t.Helper()
	var c contract.Contract
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatalf("decode contract %s: %v", rec.Body.String(), err)
	}
	if got, want := rec.Header().Get("ETag"), api.ETag(c.Version); got != want {
		t.Fatalf("ETag %q, want %q", got, want)
	}
	return c
}

func (w *supplierWorld) createContract(t *testing.T, body string) contract.Contract {
	t.Helper()
	return decodeContract(t, w.do(t, &request{method: http.MethodPost, path: contracts, body: body, wantStatus: http.StatusCreated}))
}

type contractPage struct {
	Data       []contract.Contract `json:"data"`
	Total      int                 `json:"total"`
	NextCursor string              `json:"next_cursor"`
}

func (w *supplierWorld) listContracts(t *testing.T, ctx context.Context, query string) contractPage {
	t.Helper()
	rec := w.do(t, &request{ctx: ctx, org: w.f.OrgA, method: http.MethodGet, path: contracts + query, wantStatus: http.StatusOK})
	var page contractPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode list %s: %v", rec.Body.String(), err)
	}
	return page
}

func numbers(page contractPage) map[string]bool {
	out := map[string]bool{}
	for i := range page.Data {
		out[page.Data[i].Number] = true
	}
	return out
}

// TestContractCRUD covers MGT-12 and API-06 (WP-269), part 1: create, read,
// list, update and delete over PostgreSQL with the version as ETag; a
// duplicate number in the organization is 409, in another organization
// allowed; the CHECK rules are 422 on their field; a missing currency is
// the currency of the organization.
func TestContractCRUD(t *testing.T) {
	w := newContractWorld(t)
	bg := context.Background()
	if _, err := w.f.Admin.Exec(bg, `UPDATE organization SET currency = 'CHF' WHERE id = $1`, w.f.OrgA); err != nil {
		t.Fatalf("org currency: %v", err)
	}

	c := w.createContract(t, `{"number":" V-2026-001 ","kind":"maintenance","start_date":"2026-01-01","end_date":"2026-12-31",
		"notice_period_days":90,"value":1200.50,"notes":"Wartung Kern"}`)
	if c.Number != "V-2026-001" || c.Version != 1 || c.ClientID != nil || c.Currency != "CHF" || c.Status != "active" ||
		c.Kind == nil || *c.Kind != "maintenance" || c.EndDate == nil || *c.EndDate != "2026-12-31" ||
		c.NoticePeriodDays == nil || *c.NoticePeriodDays != 90 || c.Value == nil || c.Value.String() != "1200.50" ||
		c.OrganizationID != w.f.OrgA {
		t.Fatalf("created: %+v", c)
	}
	explicit := w.createContract(t, `{"number":"V-USD","currency":"USD","status":"draft"}`)
	if explicit.Currency != "USD" || explicit.Status != "draft" || explicit.Kind != nil {
		t.Fatalf("explicit currency: %+v", explicit)
	}
	path := contracts + "/" + c.ID
	if got := decodeContract(t, w.do(t, &request{method: http.MethodGet, path: path, wantStatus: http.StatusOK})); got.Number != c.Number {
		t.Fatalf("get: %+v", got)
	}

	w.do(t, &request{method: http.MethodPost, path: contracts, body: `{"number":"V-2026-001"}`, wantStatus: http.StatusConflict, wantProblemFor: "number"})
	w.do(t, &request{method: http.MethodPost, path: contracts, body: `{"number":" "}`, wantStatus: http.StatusBadRequest, wantProblemFor: "number"})
	for _, bad := range []struct{ body, field string }{
		{`{"number":"X1","start_date":"2026-02-01","end_date":"2026-01-31"}`, "end_date"},
		{`{"number":"X2","notice_period_days":-1}`, "notice_period_days"},
		{`{"number":"X3","status":"gekuendigt"}`, "status"},
		{`{"number":"X4","kind":"rental"}`, "kind"},
		{`{"number":"X5","currency":"eur"}`, "currency"},
		{`{"number":"X6","currency":"EURO"}`, "currency"},
	} {
		w.do(t, &request{method: http.MethodPost, path: contracts, body: bad.body, wantStatus: http.StatusUnprocessableEntity, wantProblemFor: bad.field})
	}
	w.do(t, &request{method: http.MethodPost, path: contracts, body: `{"number":"X7","start_date":"01.02.2026"}`,
		wantStatus: http.StatusBadRequest, wantProblemFor: "start_date"})
	other := decodeContract(t, w.do(t, &request{ctx: w.f.OrgCtx(w.f.OrgB), org: w.f.OrgB, method: http.MethodPost, path: contracts,
		body: `{"number":"V-2026-001"}`, wantStatus: http.StatusCreated}))
	if other.OrganizationID != w.f.OrgB || other.Currency != "EUR" {
		t.Fatalf("org B contract: %+v", other)
	}
	for _, m := range []string{http.MethodGet, http.MethodPatch, http.MethodDelete} {
		w.do(t, &request{ctx: w.f.OrgCtx(w.f.OrgB), org: w.f.OrgB, method: m, path: path, body: `{"notes":"stolen"}`, wantStatus: http.StatusNotFound})
	}

	patched := decodeContract(t, w.do(t, &request{method: http.MethodPatch, path: path, ifMatch: api.ETag(1),
		body: `{"status":"terminated","notes":null,"value":null,"end_date":"2027-03-31"}`, wantStatus: http.StatusOK}))
	if patched.Status != "terminated" || patched.Version != 2 || patched.Notes != nil || patched.Value != nil ||
		patched.EndDate == nil || *patched.EndDate != "2027-03-31" || patched.Currency != "CHF" {
		t.Fatalf("patched: %+v", patched)
	}
	w.do(t, &request{method: http.MethodPatch, path: path, body: `{"end_date":"2025-12-31"}`, wantStatus: http.StatusUnprocessableEntity, wantProblemFor: "end_date"})
	w.do(t, &request{method: http.MethodPatch, path: path, body: `{"status":null}`, wantStatus: http.StatusBadRequest, wantProblemFor: "status"})
	w.do(t, &request{method: http.MethodPatch, path: path, body: `{"client_id":"` + w.f.Client1 + `"}`, wantStatus: http.StatusBadRequest, wantProblemFor: "client_id"})
	w.do(t, &request{method: http.MethodPatch, path: contracts + "/" + explicit.ID, body: `{"number":"V-2026-001"}`,
		wantStatus: http.StatusConflict, wantProblemFor: "number"})

	w.do(t, &request{method: http.MethodDelete, path: path, wantStatus: http.StatusNoContent})
	w.do(t, &request{method: http.MethodGet, path: path, wantStatus: http.StatusNotFound})
	w.do(t, &request{method: http.MethodDelete, path: path, wantStatus: http.StatusNotFound})
}

// TestContractIfMatch covers API-07 on contracts (WP-269): an outdated
// If-Match is 412, a missing one too when the organization requires it.
func TestContractIfMatch(t *testing.T) {
	w := newContractWorld(t)
	c := w.createContract(t, `{"number":"IM-1"}`)
	path := contracts + "/" + c.ID
	decodeContract(t, w.do(t, &request{method: http.MethodPatch, path: path, body: `{"notes":"a"}`, wantStatus: http.StatusOK}))
	w.do(t, &request{method: http.MethodPatch, path: path, ifMatch: api.ETag(1), body: `{"notes":"b"}`, wantStatus: http.StatusPreconditionFailed})
	w.do(t, &request{method: http.MethodPatch, path: path, ifMatch: "kein-tag", body: `{"notes":"b"}`, wantStatus: http.StatusPreconditionFailed})

	if _, err := w.f.Admin.Exec(context.Background(),
		`UPDATE organization SET settings = COALESCE(settings, '{}'::jsonb) || '{"require_if_match": true}' WHERE id = $1`, w.f.OrgA); err != nil {
		t.Fatalf("require if-match: %v", err)
	}
	w.do(t, &request{method: http.MethodPatch, path: path, body: `{"notes":"c"}`, wantStatus: http.StatusPreconditionFailed})
	got := decodeContract(t, w.do(t, &request{method: http.MethodPatch, path: path, ifMatch: api.ETag(2), body: `{"notes":"c"}`, wantStatus: http.StatusOK}))
	if got.Version != 3 || got.Notes == nil || *got.Notes != "c" {
		t.Fatalf("after if-match: %+v", got)
	}
}

// TestContractSupplierAndClientScope covers MGT-12, E-41 and E-09 on
// contracts (WP-269), part 2: the supplier must be org-wide or of the
// contract's client (422 on supplier_id otherwise, also for a supplier
// outside the writer's scope); a client-1 principal sees no contracts of
// client 2 and creates no org-wide ones; a client of another organization
// is 422; a supplier with a contract cannot be deleted (422).
func TestContractSupplierAndClientScope(t *testing.T) {
	w := newContractWorld(t)
	orgWide := w.create(t, `{"name":"Org-Lieferant"}`)
	sup1 := w.create(t, `{"name":"Lieferant 1","client_id":"`+w.f.Client1+`"}`)
	sup2 := w.create(t, `{"name":"Lieferant 2","client_id":"`+w.f.Client2+`"}`)

	// Compatibility of supplier and contract.
	w.do(t, &request{method: http.MethodPost, path: contracts, body: `{"number":"S-1","client_id":"` + w.f.Client1 + `","supplier_id":"` + sup2.ID + `"}`,
		wantStatus: http.StatusUnprocessableEntity, wantProblemFor: "supplier_id"})
	w.do(t, &request{method: http.MethodPost, path: contracts, body: `{"number":"S-2","supplier_id":"` + sup1.ID + `"}`,
		wantStatus: http.StatusUnprocessableEntity, wantProblemFor: "supplier_id"})
	w.do(t, &request{method: http.MethodPost, path: contracts, body: `{"number":"S-3","supplier_id":"` + w.f.ID() + `"}`,
		wantStatus: http.StatusUnprocessableEntity, wantProblemFor: "supplier_id"})
	withOrgWide := w.createContract(t, `{"number":"S-4","client_id":"`+w.f.Client1+`","supplier_id":"`+orgWide.ID+`"}`)
	own := w.createContract(t, `{"number":"S-5","client_id":"`+w.f.Client1+`","supplier_id":"`+sup1.ID+`"}`)
	w.createContract(t, `{"number":"S-6","client_id":"`+w.f.Client2+`","supplier_id":"`+sup2.ID+`"}`)
	orgContract := w.createContract(t, `{"number":"S-7","supplier_id":"`+orgWide.ID+`"}`)
	w.do(t, &request{method: http.MethodPatch, path: contracts + "/" + withOrgWide.ID, body: `{"supplier_id":"` + sup2.ID + `"}`,
		wantStatus: http.StatusUnprocessableEntity, wantProblemFor: "supplier_id"})

	// A supplier with a contract cannot be deleted.
	w.do(t, &request{method: http.MethodDelete, path: suppliers + "/" + sup2.ID, wantStatus: http.StatusUnprocessableEntity})

	// Client scope (E-09).
	c1Ctx := w.f.ClientCtx(w.f.Client1)
	seen := numbers(w.listContracts(t, c1Ctx, ""))
	if !seen["S-4"] || !seen["S-5"] || !seen["S-7"] || seen["S-6"] || len(seen) != 3 {
		t.Fatalf("client 1 sees %v", seen)
	}
	asC1 := func(method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		return w.do(t, &request{ctx: c1Ctx, org: w.f.OrgA, method: method, path: path, body: body, wantStatus: want})
	}
	asC1(http.MethodPost, contracts, `{"number":"C1-org"}`, http.StatusForbidden)
	asC1(http.MethodPost, contracts, `{"number":"C1-c2","client_id":"`+w.f.Client2+`"}`, http.StatusForbidden)
	created := decodeContract(t, asC1(http.MethodPost, contracts, `{"number":"C1-own","client_id":"`+w.f.Client1+`"}`, http.StatusCreated))
	if created.ClientID == nil || *created.ClientID != w.f.Client1 {
		t.Fatalf("client 1 contract: %+v", created)
	}
	// A supplier of client 2 is outside the scope of client 1: not found.
	w.do(t, &request{ctx: c1Ctx, org: w.f.OrgA, method: http.MethodPatch, path: contracts + "/" + own.ID,
		body: `{"supplier_id":"` + sup2.ID + `"}`, wantStatus: http.StatusUnprocessableEntity, wantProblemFor: "supplier_id"})
	asC1(http.MethodPatch, contracts+"/"+orgContract.ID, `{"notes":"x"}`, http.StatusNotFound)
	asC1(http.MethodDelete, contracts+"/"+orgContract.ID, "", http.StatusNotFound)
	asC1(http.MethodGet, contracts+"/"+orgContract.ID, "", http.StatusOK)

	// A client of another organization: 422.
	foreignClient := w.f.ID()
	if _, err := w.f.Admin.Exec(context.Background(), `INSERT INTO client (id, organization_id, name, slug) VALUES ($1, $2, 'Fremd', $3)`,
		foreignClient, w.f.OrgB, "scopetest-73-foreign-contract"); err != nil {
		t.Fatalf("insert foreign client: %v", err)
	}
	w.do(t, &request{method: http.MethodPost, path: contracts, body: `{"number":"F-1","client_id":"` + foreignClient + `"}`,
		wantStatus: http.StatusUnprocessableEntity, wantProblemFor: "client_id"})
}

// TestContractListFilterAndCursor covers API-02 on contracts (WP-269):
// filter[end_date][lte] and sort=end_date with the cursor return every
// matching row once; contracts without end date come last ascending.
func TestContractListFilterAndCursor(t *testing.T) {
	w := newContractWorld(t)
	for _, c := range []struct{ number, end, status string }{
		{"L-1", "2026-06-30", "active"}, {"L-2", "2026-03-31", "active"}, {"L-3", "2027-01-31", "active"},
		{"L-4", "2026-04-30", "terminated"}, {"L-5", "", "active"}, {"L-6", "2026-09-30", "ended"},
	} {
		body := `{"number":"` + c.number + `","status":"` + c.status + `"`
		if c.end != "" {
			body += `,"end_date":"` + c.end + `"`
		}
		w.createContract(t, body+"}")
	}
	ctx := w.f.OrgCtx(w.f.OrgA)
	collect := func(q url.Values) []string {
		t.Helper()
		var got []string
		next := "?" + q.Encode()
		for pages := 0; next != ""; pages++ {
			if pages > 6 {
				t.Fatalf("%v: cursor does not end", q)
			}
			page := w.listContracts(t, ctx, next)
			for i := range page.Data {
				got = append(got, page.Data[i].Number)
			}
			next = ""
			if page.NextCursor != "" {
				q.Set("cursor", page.NextCursor)
				next = "?" + q.Encode()
			}
		}
		return got
	}
	for _, c := range []struct {
		query url.Values
		want  string
	}{
		{url.Values{"filter[end_date][lte]": {"2026-09-30"}, "sort": {"end_date"}, "limit": {"2"}}, "L-2,L-4,L-1,L-6"},
		{url.Values{"sort": {"end_date"}, "limit": {"2"}}, "L-2,L-4,L-1,L-6,L-3,L-5"},
		{url.Values{"sort": {"-end_date"}, "limit": {"2"}}, "L-5,L-3,L-6,L-1,L-4,L-2"},
		{url.Values{"filter[end_date][gte]": {"2026-06-30"}, "filter[status]": {"active"}, "limit": {"1"}}, "L-1,L-3"},
		{url.Values{"filter[status][in]": {"terminated,ended"}, "sort": {"-number"}}, "L-6,L-4"},
	} {
		if got := strings.Join(collect(c.query), ","); got != c.want {
			t.Fatalf("%v: %s, want %s", c.query, got, c.want)
		}
	}
	w.do(t, &request{method: http.MethodGet, path: contracts + "?filter[notes]=x", wantStatus: http.StatusUnprocessableEntity})
	w.do(t, &request{method: http.MethodGet, path: contracts + "?sort=value", wantStatus: http.StatusUnprocessableEntity})
	first := w.listContracts(t, ctx, "?limit=1")
	w.do(t, &request{method: http.MethodGet, path: contracts + "?sort=end_date&cursor=" + url.QueryEscape(first.NextCursor), wantStatus: http.StatusBadRequest})
	w.do(t, &request{method: http.MethodGet, path: contracts + "?cursor=forged", wantStatus: http.StatusBadRequest})
}

// TestContractFieldRights covers RBA-07 on contracts (WP-269, E-43): a
// field without read grant (core.value) is left out of the detail and the
// list while the ETag stays the same; a filter on it and a write of a
// read-only field (core.notes) are 403 with the fields; an object grant on
// a contract is accepted and removed with the contract.
func TestContractFieldRights(t *testing.T) {
	w := newContractWorld(t)
	bg := context.Background()
	c := w.createContract(t, `{"number":"FR-1","value":4711.00,"notes":"lesbar"}`)
	path := contracts + "/" + c.ID
	full := w.do(t, &request{method: http.MethodGet, path: path, wantStatus: http.StatusOK})

	role := w.f.ID()
	perms := []string{"contract:read", "contract:write"}
	raw, _ := json.Marshal(perms)
	for _, s := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 'wp269-reader', $3::jsonb)`, []any{role, w.f.OrgA, string(raw)}},
		{`INSERT INTO field_grant (organization_id, role_id, object_kind, field_key, level) VALUES ($1, $2, 'contract', 'core.value', 'none')`, []any{w.f.OrgA, role}},
		{`INSERT INTO field_grant (organization_id, role_id, object_kind, field_key, level) VALUES ($1, $2, 'contract', 'core.notes', 'read')`, []any{w.f.OrgA, role}},
		{`INSERT INTO field_grant (organization_id, role_id, object_kind, field_key, level) VALUES ($1, $2, 'contract', 'core.end_date', 'none')`, []any{w.f.OrgA, role}},
	} {
		if _, err := w.f.Admin.Exec(bg, s.sql, s.args...); err != nil {
			t.Fatalf("%s: %v", s.sql, err)
		}
	}
	policy, err := permission.NewFieldResolver(nil, nil, permission.NewPGFieldGrants(w.f.App), nil).
		PolicyForGrants(bg, w.f.OrgA, []identity.Grant{{Permissions: []identity.Permission{"contract:read", "contract:write"}, RoleID: role}}, nil)
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	reader := permission.WithPolicy(w.f.OrgCtx(w.f.OrgA), policy)
	asReader := func(method, path, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		return w.do(t, &request{ctx: reader, org: w.f.OrgA, method: method, path: path, body: body, wantStatus: want})
	}
	wantFields := func(rec *httptest.ResponseRecorder, field string) {
		t.Helper()
		var p api.FieldForbiddenProblem
		if decodeErr := json.Unmarshal(rec.Body.Bytes(), &p); decodeErr != nil || len(p.Fields) != 1 || p.Fields[0] != field {
			t.Fatalf("403 %s, want fields [%s]", rec.Body.String(), field)
		}
	}

	got := asReader(http.MethodGet, path, "", http.StatusOK)
	if strings.Contains(got.Body.String(), "4711") || !strings.Contains(got.Body.String(), `"notes":"lesbar"`) {
		t.Fatalf("projected detail: %s", got.Body.String())
	}
	if got.Header().Get("ETag") != full.Header().Get("ETag") {
		t.Fatalf("ETag %q, want %q", got.Header().Get("ETag"), full.Header().Get("ETag"))
	}
	var body map[string]any
	if err = json.Unmarshal(got.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["value"]; ok || body["number"] != "FR-1" || body["version"] == nil {
		t.Fatalf("projected detail: %v", body)
	}
	list := asReader(http.MethodGet, contracts, "", http.StatusOK)
	if strings.Contains(list.Body.String(), "4711") || !strings.Contains(list.Body.String(), "FR-1") {
		t.Fatalf("projected list: %s", list.Body.String())
	}

	wantFields(asReader(http.MethodPatch, path, `{"notes":"neu"}`, http.StatusForbidden), "notes")
	wantFields(asReader(http.MethodPatch, path, `{"value":4711.00}`, http.StatusForbidden), "value")
	wantFields(asReader(http.MethodPost, contracts, `{"number":"FR-2","value":1}`, http.StatusForbidden), "value")
	wantFields(asReader(http.MethodGet, contracts+"?filter[end_date][lte]=2030-01-01", "", http.StatusForbidden), "end_date")
	wantFields(asReader(http.MethodGet, contracts+"?sort=-end_date", "", http.StatusForbidden), "end_date")
	asReader(http.MethodGet, contracts+"?filter[status]=active", "", http.StatusOK)
	// The unchanged value of a readable, read-only field passes.
	if patched := decodeContract(t, asReader(http.MethodPatch, path, `{"notes":"lesbar","status":"terminated"}`, http.StatusOK)); patched.Status != "terminated" {
		t.Fatalf("patched as reader: %+v", patched)
	}

	// Object grants on contracts are accepted and leave with the contract.
	grants := permission.NewPGFieldGrants(w.f.App)
	if _, err = grants.CreateObjectGrant(w.f.OrgCtx(w.f.OrgA), w.f.OrgA, &permission.ObjectGrant{GranteeRoleID: &role,
		ObjectKind: permission.KindContract, ObjectID: c.ID, Level: permission.LevelRead}); err != nil {
		t.Fatalf("object grant on a contract: %v", err)
	}
	if _, err = grants.CreateObjectGrant(w.f.OrgCtx(w.f.OrgA), w.f.OrgA, &permission.ObjectGrant{GranteeRoleID: &role,
		ObjectKind: permission.KindContract, ObjectID: w.f.ID(), Level: permission.LevelRead}); err == nil {
		t.Fatal("object grant on an unknown contract accepted")
	}
	countGrants := func() int {
		t.Helper()
		var n int
		if err := w.f.Admin.QueryRow(bg, `SELECT COUNT(*) FROM object_grant WHERE object_kind = 'contract' AND object_id = $1`, c.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := countGrants(); n != 1 {
		t.Fatalf("object grants before delete: %d", n)
	}
	w.do(t, &request{method: http.MethodDelete, path: path, wantStatus: http.StatusNoContent})
	if n := countGrants(); n != 0 {
		t.Fatalf("object grants after delete: %d", n)
	}
}
