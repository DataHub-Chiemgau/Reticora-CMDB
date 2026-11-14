package security_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/security"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/vulnfeed"
)

// The product mapping API on PostgreSQL (SEC-02, WP-1122, E-236): CRUD of
// cpe_mapping with validation, writes only org-wide, isolation between
// organizations, the groups without mapping under row level security, the
// product suggestion of the catalog and the match job a write queues.
//
// They run only with TEST_DATABASE_URL (scopetest.Seed).
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestCPEMapping|TestUnmapped|TestVulnProductSuggestion' ./internal/security/

// mappingQueue records the match jobs submitted.
type mappingQueue struct {
	mu   sync.Mutex
	jobs []security.MatchParams
}

func (q *mappingQueue) Submit(_ context.Context, orgID, kind string, params any, _ jobs.EnqueueOptions) (*jobs.Job, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	p, _ := params.(security.MatchParams)
	q.jobs = append(q.jobs, p)
	return &jobs.Job{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", len(q.jobs)), OrganizationID: orgID, Kind: kind}, true, nil
}

func (q *mappingQueue) last() (security.MatchParams, int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.jobs) == 0 {
		return security.MatchParams{}, 0
	}
	return q.jobs[len(q.jobs)-1], len(q.jobs)
}

type mappingEnv struct {
	*matchEnv
	api   chi.Router
	queue *mappingQueue
}

func newMappingEnv(t *testing.T) *mappingEnv {
	t.Helper()
	m := newMatchEnv(t)
	e := &mappingEnv{matchEnv: m, api: chi.NewRouter(), queue: &mappingQueue{}}
	matcher := security.NewMatcher(m.f.App, m.catalog, m.findings).WithQueue(e.queue)
	security.NewHandler(m.findings).WithMappings(security.NewMappingStore(m.f.App), m.catalog, matcher).RegisterRoutes(e.api)
	return e
}

var mappingPerms = []identity.Permission{identity.PermFindingRead, identity.PermFindingWrite}

// asSite is the request context of a user of org bound to the site.
func (e *mappingEnv) asSite(org, site string) context.Context {
	user := e.f.User
	scope := database.OrgWideScope(org, user)
	scope.Sites = database.ScopeIDs(site)
	p := identity.Principal{Subject: user, OrganizationID: org, Permissions: mappingPerms, Type: identity.PrincipalTypeUser,
		Scope: &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{IDs: []string{site}}, Teams: identity.ScopeSet{All: true}}}
	ctx := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope), tenant.TenantInfo{OrganizationID: org, UserID: user})
	return identity.WithPrincipal(ctx, p)
}

// siteOf returns the site of a room of the fixture.
func (e *mappingEnv) siteOf(room string) string {
	e.t.Helper()
	var site string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT b.site_id::text FROM room r JOIN building b ON b.id = r.building_id WHERE r.id = $1`,
		room).Scan(&site); err != nil {
		e.t.Fatal(err)
	}
	return site
}

// as is the request context of a user of org, org-wide or bound to clients.
func (e *mappingEnv) as(org string, clients ...string) context.Context {
	user := e.f.User
	scope := database.OrgWideScope(org, user)
	p := identity.Principal{Subject: user, OrganizationID: org, Permissions: mappingPerms, Type: identity.PrincipalTypeUser}
	if len(clients) > 0 {
		scope.Clients = database.ScopeIDs(clients...)
		p.Scope = &identity.Scope{Clients: identity.ScopeSet{IDs: clients}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}}
	}
	ctx := tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope), tenant.TenantInfo{OrganizationID: org, UserID: user})
	return identity.WithPrincipal(ctx, p)
}

func (e *mappingEnv) call(ctx context.Context, method, path string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			e.t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.api.ServeHTTP(rec, req)
	return rec
}

func decodeMapping(t *testing.T, rec *httptest.ResponseRecorder) security.MappingRecord {
	t.Helper()
	var m security.MappingRecord
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("mapping %s: %v", rec.Body.String(), err)
	}
	return m
}

func problemField(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var p struct {
		Field  string `json:"field"`
		Errors []struct {
			Field string `json:"field"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if p.Field == "" && len(p.Errors) > 0 {
		return p.Errors[0].Field
	}
	return p.Field
}

func (e *mappingEnv) unmapped(ctx context.Context) []security.UnmappedGroup {
	e.t.Helper()
	rec := e.call(ctx, http.MethodGet, "/api/v1/security/unmapped?limit=200", nil)
	if rec.Code != http.StatusOK {
		e.t.Fatalf("unmapped: %d %s", rec.Code, rec.Body.String())
	}
	var list api.ListResponse[security.UnmappedGroup]
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		e.t.Fatal(err)
	}
	return list.Data
}

func hasGroup(groups []security.UnmappedGroup, manufacturer string) *security.UnmappedGroup {
	for i := range groups {
		if groups[i].Manufacturer == manufacturer {
			return &groups[i]
		}
	}
	return nil
}

// TestCPEMappingCRUD: create (201), read, change (200), delete (204) and a
// refused value as 422 with the field.
func TestCPEMappingCRUD(t *testing.T) {
	e := newMappingEnv(t)
	ctx := e.as(e.f.OrgA)
	rec := e.call(ctx, http.MethodPost, "/api/v1/security/cpe-mappings", map[string]any{"match_kind": "manufacturer_model",
		"manufacturer_pattern": "Acme*", "model_pattern": "R-*", "vendor": "Acme Networks", "products": []string{"AcmeOS", "acmeos"},
		"version_scheme": "generic"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	m := decodeMapping(t, rec)
	if m.ID == "" || m.Vendor != "acme_networks" || !slices.Equal(m.Products, []string{"acmeos"}) || m.VersionField != "firmware_version" || m.Priority != 100 {
		t.Errorf("created %+v, want the normalized mapping with the defaults", m)
	}
	if rec = e.call(ctx, http.MethodGet, "/api/v1/security/cpe-mappings/"+m.ID, nil); rec.Code != http.StatusOK || decodeMapping(t, rec).ManufacturerPattern != "Acme*" {
		t.Errorf("get: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.call(ctx, http.MethodPatch, "/api/v1/security/cpe-mappings/"+m.ID, map[string]any{"priority": 5, "version_field": "os_version"})
	if rec.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
	}
	if got := decodeMapping(t, rec); got.Priority != 5 || got.VersionField != "os_version" || got.Vendor != "acme_networks" {
		t.Errorf("patched %+v", got)
	}
	rec = e.call(ctx, http.MethodGet, "/api/v1/security/cpe-mappings", nil)
	var list api.ListResponse[security.MappingRecord]
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != http.StatusOK || len(list.Data) != 1 {
		t.Errorf("list: %d %s", rec.Code, rec.Body.String())
	}
	for field, body := range map[string]map[string]any{
		"match_kind":           {"match_kind": "cpe", "vendor": "x", "products": []string{"y"}},
		"manufacturer_pattern": {"match_kind": "manufacturer_model", "vendor": "x", "products": []string{"y"}},
		"sys_object_id_prefix": {"match_kind": "sys_object_id", "sys_object_id_prefix": "1.3.x", "vendor": "x", "products": []string{"y"}},
		"products":             {"match_kind": "sys_object_id", "sys_object_id_prefix": "1.3.6.1.4.1.9", "vendor": "x", "products": []string{}},
		"vendor":               {"match_kind": "sys_object_id", "sys_object_id_prefix": "1.3.6.1.4.1.9", "vendor": " ", "products": []string{"y"}},
		"version_scheme": {"match_kind": "sys_object_id", "sys_object_id_prefix": "1.3.6.1.4.1.9", "vendor": "x", "products": []string{"y"},
			"version_scheme": "semver"},
		"version_field": {"match_kind": "sys_object_id", "sys_object_id_prefix": "1.3.6.1.4.1.9", "vendor": "x", "products": []string{"y"},
			"version_field": "kernel"},
	} {
		rec = e.call(ctx, http.MethodPost, "/api/v1/security/cpe-mappings", body)
		if rec.Code != http.StatusUnprocessableEntity || problemField(t, rec) != field {
			t.Errorf("%s: %d %s, want 422 with field %s", field, rec.Code, rec.Body.String(), field)
		}
	}
	if rec = e.call(ctx, http.MethodPatch, "/api/v1/security/cpe-mappings/"+m.ID, map[string]any{"products": []string{}}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("patch with no product: %d, want 422", rec.Code)
	}
	if rec = e.call(ctx, http.MethodDelete, "/api/v1/security/cpe-mappings/"+m.ID, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if rec = e.call(ctx, http.MethodGet, "/api/v1/security/cpe-mappings/"+m.ID, nil); rec.Code != http.StatusNotFound {
		t.Errorf("get after delete: %d, want 404", rec.Code)
	}
}

// TestCPEMappingWriteNeedsOrgWidePrincipal: a principal bound to a client
// reads but does not write (403).
func TestCPEMappingWriteNeedsOrgWidePrincipal(t *testing.T) {
	e := newMappingEnv(t)
	bound := e.as(e.f.OrgA, e.f.Client1)
	body := map[string]any{"match_kind": "sys_object_id", "sys_object_id_prefix": "1.3.6.1.4.1.99999", "vendor": "x", "products": []string{"y"}}
	if rec := e.call(bound, http.MethodPost, "/api/v1/security/cpe-mappings", body); rec.Code != http.StatusForbidden {
		t.Fatalf("create by a client-bound principal: %d, want 403", rec.Code)
	}
	rec := e.call(e.as(e.f.OrgA), http.MethodPost, "/api/v1/security/cpe-mappings", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create org-wide: %d %s", rec.Code, rec.Body.String())
	}
	id := decodeMapping(t, rec).ID
	if rec = e.call(bound, http.MethodPatch, "/api/v1/security/cpe-mappings/"+id, map[string]any{"priority": 1}); rec.Code != http.StatusForbidden {
		t.Errorf("patch by a client-bound principal: %d, want 403", rec.Code)
	}
	if rec = e.call(bound, http.MethodDelete, "/api/v1/security/cpe-mappings/"+id, nil); rec.Code != http.StatusForbidden {
		t.Errorf("delete by a client-bound principal: %d, want 403", rec.Code)
	}
	if rec = e.call(bound, http.MethodGet, "/api/v1/security/cpe-mappings/"+id, nil); rec.Code != http.StatusOK {
		t.Errorf("read by a client-bound principal: %d, want 200", rec.Code)
	}
}

// TestCPEMappingOrgIsolation: organization B neither sees nor changes the
// mapping of A (404), and its list is empty.
func TestCPEMappingOrgIsolation(t *testing.T) {
	e := newMappingEnv(t)
	rec := e.call(e.as(e.f.OrgA), http.MethodPost, "/api/v1/security/cpe-mappings", map[string]any{"match_kind": "sys_object_id",
		"sys_object_id_prefix": "1.3.6.1.4.1.99998", "vendor": "x", "products": []string{"y"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	id := decodeMapping(t, rec).ID
	other := e.as(e.f.OrgB)
	for _, method := range []string{http.MethodGet, http.MethodPatch, http.MethodDelete} {
		if rec = e.call(other, method, "/api/v1/security/cpe-mappings/"+id, map[string]any{"priority": 1}); rec.Code != http.StatusNotFound {
			t.Errorf("%s by organization B: %d, want 404", method, rec.Code)
		}
	}
	rec = e.call(other, http.MethodGet, "/api/v1/security/cpe-mappings", nil)
	var list api.ListResponse[security.MappingRecord]
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Data) != 0 {
		t.Errorf("list of organization B: %s, want empty", rec.Body.String())
	}
}

// TestUnmappedGroupsRespectScope: the groups count the CIs visible in the
// caller's client and site scope; mapped vendors (system mapping) are left
// out.
func TestUnmappedGroupsRespectScope(t *testing.T) {
	e := newMappingEnv(t)
	e.device("wp1122-a1", "Obscura", "Box 1", "", "1.0")
	e.device("wp1122-a2", "Obscura", "Box 1", "", "1.1")
	other := e.f.CI(t, e.f.OrgA, e.f.Client2, "wp1122-a3")
	if _, err := e.f.Admin.Exec(context.Background(), `UPDATE ci SET manufacturer = 'Obscura', model = 'Box 1' WHERE id = $1`, other); err != nil {
		t.Fatal(err)
	}
	e.device("wp1122-cisco", "Cisco Systems", "C2960", "", "15.2(4)M7")
	e.device("wp1122-none", "", "", "", "")
	all := e.unmapped(e.as(e.f.OrgA))
	g := hasGroup(all, "Obscura")
	if g == nil || g.CICount != 3 || g.Model != "Box 1" || g.ExampleCIID == "" {
		t.Fatalf("org-wide groups %+v, want Obscura/Box 1 with 3 CIs", all)
	}
	if hasGroup(all, "Cisco Systems") != nil || hasGroup(all, "") != nil {
		t.Errorf("groups %+v contain a mapped vendor or a CI without data", all)
	}
	if g := hasGroup(e.unmapped(e.as(e.f.OrgA, e.f.Client1)), "Obscura"); g == nil || g.CICount != 2 {
		t.Errorf("client-1 group %+v, want 2 CIs", g)
	}
	if g := hasGroup(e.unmapped(e.as(e.f.OrgB)), "Obscura"); g != nil {
		t.Errorf("organization B sees the group of A: %+v", g)
	}
	// Site scope: two CIs of the group Umbra/Box 9 at two sites; a principal
	// of site 1 counts only the CI of its site.
	room1 := e.f.Room(t, e.f.Client1, "wp1122-s1")
	site1 := e.siteOf(room1)
	for i, room := range []string{room1, e.f.Room(t, e.f.Client1, "wp1122-s2")} {
		id := e.device(fmt.Sprintf("wp1122-site-%d", i), "Umbra", "Box 9", "", "1.0")
		// The site of a CI follows from its location (ci_derive_location).
		if _, err := e.f.Admin.Exec(context.Background(), `UPDATE ci SET location_id = $2 WHERE id = $1`, id, room); err != nil {
			t.Fatal(err)
		}
	}
	if g := hasGroup(e.unmapped(e.as(e.f.OrgA)), "Umbra"); g == nil || g.CICount != 2 {
		t.Errorf("org-wide group Umbra %+v, want 2 CIs", g)
	}
	if g := hasGroup(e.unmapped(e.asSite(e.f.OrgA, site1)), "Umbra"); g == nil || g.CICount != 1 {
		t.Errorf("site-1 group Umbra %+v, want 1 CI", g)
	}
}

// TestUnmappedReadsEveryMapping: the list without mapping applies every
// mapping of the organization, as the matcher does, also beyond the cap of
// the mapping list (1000).
func TestUnmappedReadsEveryMapping(t *testing.T) {
	e := newMappingEnv(t)
	e.device("wp1122-many", "Penumbra", "Box 7", "", "1.0")
	if _, err := e.f.Admin.Exec(context.Background(), `INSERT INTO cpe_mapping (organization_id, match_kind, manufacturer_pattern, vendor, products, priority)
		SELECT $1, 'manufacturer_model', 'wp1122-filler-' || g, 'filler', ARRAY['filler'], 1 FROM generate_series(1, 1000) g`, e.f.OrgA); err != nil {
		t.Fatal(err)
	}
	e.mapping(e.f.OrgA, "penumbra", "penumbra", "boxos", "generic", 50000)
	if g := hasGroup(e.unmapped(e.as(e.f.OrgA)), "Penumbra"); g != nil {
		t.Errorf("group %+v listed although the 1001st mapping applies to it", g)
	}
}

// TestUnmappedDisappearsAfterMapping: a mapping of the organization takes
// the group out of the list.
func TestUnmappedDisappearsAfterMapping(t *testing.T) {
	e := newMappingEnv(t)
	e.device("wp1122-b1", "Obscura", "Box 2", "", "1.0")
	ctx := e.as(e.f.OrgA)
	if hasGroup(e.unmapped(ctx), "Obscura") == nil {
		t.Fatal("the group is not listed before the mapping")
	}
	if rec := e.call(ctx, http.MethodPost, "/api/v1/security/cpe-mappings", map[string]any{"match_kind": "manufacturer_model",
		"manufacturer_pattern": "obscura", "vendor": "obscura", "products": []string{"boxos"}}); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if g := hasGroup(e.unmapped(ctx), "Obscura"); g != nil {
		t.Errorf("group %+v still listed after the mapping", g)
	}
}

// TestVulnProductSuggestion: q finds vendor and product of the catalog, at
// most 50.
func TestVulnProductSuggestion(t *testing.T) {
	e := newMappingEnv(t)
	e.advisory("wp1121-sugg-1", matchCSAF, []string{"CVE-2026-1122"}, vulnfeed.Affected{Vendor: "wp1122vendor", Product: "wp1122router",
		VersionStart: "1.0", VersionEnd: "2.0"}, false)
	rec := e.call(e.as(e.f.OrgA), http.MethodGet, "/api/v1/security/vuln-products?q=wp1122rout", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("suggestion: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data []security.VulnProduct `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Data) != 1 || body.Data[0].Vendor != "wp1122vendor" || body.Data[0].Product != "wp1122router" {
		t.Errorf("suggestion %+v, want wp1122vendor/wp1122router", body.Data)
	}
	if rec = e.call(e.as(e.f.OrgA), http.MethodGet, "/api/v1/security/vuln-products", nil); rec.Code != http.StatusOK {
		t.Fatalf("suggestion without q: %d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Data) > 50 {
		t.Errorf("suggestion without q: %d entries, want at most 50", len(body.Data))
	}
}

// TestCPEMappingSaveEnqueuesMatch: a write queues vulnfeed.match with the
// ids of the CIs the pattern applies to (before and after a change) and
// names the job; a CI of another organization is not among them.
func TestCPEMappingSaveEnqueuesMatch(t *testing.T) {
	e := newMappingEnv(t)
	a := e.device("wp1122-m1", "Obscura", "Box 3", "", "1.0")
	b := e.device("wp1122-m2", "obscura gmbh", "Box 4", "", "1.0")
	e.device("wp1122-m3", "Other", "Box 3", "", "1.0")
	oid := e.device("wp1122-m4", "", "", ".1.3.6.1.4.1.99997.1.2", "1.0")
	foreign := e.f.CI(t, e.f.OrgB, "", "wp1122-foreign")
	if _, err := e.f.Admin.Exec(context.Background(), `UPDATE ci SET manufacturer = 'Obscura' WHERE id = $1`, foreign); err != nil {
		t.Fatal(err)
	}
	ctx := e.as(e.f.OrgA)
	rec := e.call(ctx, http.MethodPost, "/api/v1/security/cpe-mappings", map[string]any{"match_kind": "manufacturer_model",
		"manufacturer_pattern": "obscura*", "vendor": "obscura", "products": []string{"boxos"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	m := decodeMapping(t, rec)
	params, n := e.queue.last()
	want := []string{a, b}
	slices.Sort(want)
	if n != 1 || m.MatchJobID == "" || !slices.Equal(params.CIIDs, want) {
		t.Fatalf("job %q with %v (%d jobs), want one with %v", m.MatchJobID, params.CIIDs, n, want)
	}
	// A change to the sys_object_id kind queues the CIs of the old and the
	// new pattern.
	rec = e.call(ctx, http.MethodPatch, "/api/v1/security/cpe-mappings/"+m.ID, map[string]any{"match_kind": "sys_object_id",
		"sys_object_id_prefix": "1.3.6.1.4.1.99997"})
	if rec.Code != http.StatusOK || decodeMapping(t, rec).MatchJobID == "" {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body.String())
	}
	params, _ = e.queue.last()
	want = []string{a, b, oid}
	slices.Sort(want)
	if !slices.Equal(params.CIIDs, want) {
		t.Errorf("job after the change with %v, want %v", params.CIIDs, want)
	}
	rec = e.call(ctx, http.MethodDelete, "/api/v1/security/cpe-mappings/"+m.ID, nil)
	params, n = e.queue.last()
	var deleted security.MappingDeleted
	if err := json.Unmarshal(rec.Body.Bytes(), &deleted); err != nil {
		t.Fatalf("delete answer %s: %v", rec.Body.String(), err)
	}
	if rec.Code != http.StatusOK || deleted.MatchJobID == "" || rec.Header().Get("Location") != "/api/v1/jobs/"+deleted.MatchJobID || n != 3 ||
		!slices.Equal(params.CIIDs, []string{oid}) {
		t.Errorf("delete: %d %s location %q, job %v (%d jobs); want 200 with match_job_id of the job of %s", rec.Code, rec.Body.String(),
			rec.Header().Get("Location"), params.CIIDs, n, oid)
	}
}
