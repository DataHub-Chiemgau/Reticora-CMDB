package document_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// entityRouter serves the document routes of repo; call sends a request
// as a principal of organization A with the given scope.
func entityRouter(t *testing.T, w *linkWorld, repo document.Repository) func(scope *database.TenantScope, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := chi.NewRouter()
	document.NewHandler(repo, nil).RegisterRoutes(mux)
	return func(scope *database.TenantScope, method, target, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: w.f.OrgA, UserID: w.user})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req.WithContext(database.ContextWithTenantScope(ctx, scope)))
		if rec.Code >= http.StatusBadRequest && dbLeak.MatchString(rec.Body.String()) {
			t.Errorf("%s %s: body discloses database internals: %s", method, target, rec.Body.String())
		}
		return rec
	}
}

// listTitles decodes a document list response into its titles, total and
// next cursor.
func listTitles(t *testing.T, rec *httptest.ResponseRecorder) (titles []string, total int, next string) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	var body api.ListResponse[document.Document]
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, d := range body.Data {
		titles = append(titles, d.Title)
	}
	slices.Sort(titles)
	return titles, body.Total, body.NextCursor
}

func entityQuery(entityType, entityID string, extra ...string) string {
	q := url.Values{"entity_type": {entityType}, "entity_id": {entityID}}
	for i := 0; i+1 < len(extra); i += 2 {
		q.Set(extra[i], extra[i+1])
	}
	return "/api/v1/documents?" + q.Encode()
}

// TestDocumentEntityFilter (WP-811, MGT-01): GET /api/v1/documents with
// entity_type and entity_id lists the documents of an asset and of a CI
// in the caller's scope, combinable with category and search and paged by
// cursor; a target of another client answers 404, only one of the two
// parameters 400 and an unknown entity type 422. The memory repository
// returns the same hits.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDocumentEntityFilter(t *testing.T) {
	w := newLinkWorld(t)
	f := w.f
	ci1 := f.CI(t, f.OrgA, f.Client1, "ef-ci1")

	// The same documents in both repositories: title, category, targets.
	type seed struct {
		title, category string
		targets         []string
	}
	seeds := []seed{
		{"ef-asset1-a", "manual", []string{"asset", w.asset1}},
		{"ef-asset1-b", "general", []string{"asset", w.asset1}},
		{"ef-shared", "manual", []string{"asset", w.asset1, "asset", w.asset2}},
		{"ef-asset2", "general", []string{"asset", w.asset2}},
		{"ef-ci-1", "general", []string{"ci", ci1}},
		{"ef-ci-2", "general", []string{"ci", ci1}},
		{"ef-ci-3", "manual", []string{"ci", ci1, "asset", w.asset2}},
		{"ef-unlinked", "general", nil},
	}
	mem := document.NewMemoryRepository()
	for _, s := range seeds {
		id := w.doc(t, s.title, s.targets...)
		if _, err := f.Admin.Exec(context.Background(), `UPDATE document SET category = $2 WHERE id = $1`, id, s.category); err != nil {
			t.Fatal(err)
		}
		d := &document.Document{OrganizationID: f.OrgA, Title: s.title, FileName: s.title + ".pdf", Category: s.category}
		var links []document.DocumentLink
		for i := 0; i+1 < len(s.targets); i += 2 {
			links = append(links, document.DocumentLink{EntityType: s.targets[i], EntityID: s.targets[i+1]})
		}
		if err := mem.CreateWithLinks(context.Background(), d, links); err != nil {
			t.Fatal(err)
		}
	}

	orgWide := database.OrgWideScope(f.OrgA, w.user)
	pg, memCall := entityRouter(t, w, w.repo), entityRouter(t, w, mem)
	for _, c := range []struct {
		name        string
		scope       *database.TenantScope
		target      string
		want        []string
		memoryCheck bool
	}{
		{"asset 1 as client 1", &w.client1Scope, entityQuery("asset", w.asset1),
			[]string{"ef-asset1-a", "ef-asset1-b", "ef-shared"}, true},
		{"asset 2 org-wide", &orgWide, entityQuery("asset", w.asset2),
			[]string{"ef-asset2", "ef-ci-3", "ef-shared"}, true},
		{"CI as client 1", &w.client1Scope, entityQuery("ci", ci1),
			[]string{"ef-ci-1", "ef-ci-2", "ef-ci-3"}, true},
		{"CI with category", &w.client1Scope, entityQuery("ci", ci1, "category", "manual"),
			[]string{"ef-ci-3"}, true},
		{"asset 1 with search", &orgWide, entityQuery("asset", w.asset1, "search", "shared"),
			[]string{"ef-shared"}, true},
		{"CI sorted by title", &w.client1Scope, entityQuery("ci", ci1, "sort_by", "title", "sort_dir", "asc"),
			[]string{"ef-ci-1", "ef-ci-2", "ef-ci-3"}, true},
	} {
		got, total, _ := listTitles(t, pg(c.scope, http.MethodGet, c.target, ""))
		if !slices.Equal(got, c.want) || total != len(c.want) {
			t.Errorf("%s: %v (total %d), want %v", c.name, got, total, c.want)
		}
		if c.memoryCheck {
			memGot, memTotal, _ := listTitles(t, memCall(&orgWide, http.MethodGet, c.target, ""))
			if !slices.Equal(memGot, c.want) || memTotal != len(c.want) {
				t.Errorf("%s in the memory repository: %v (total %d), want %v", c.name, memGot, memTotal, c.want)
			}
		}
	}

	// Cursor over two pages: total counts only the hits of the target.
	first := pg(&w.client1Scope, http.MethodGet, entityQuery("ci", ci1, "limit", "2"), "")
	page1, total, next := listTitles(t, first)
	if len(page1) != 2 || total != 3 || next == "" {
		t.Fatalf("first page: %v total %d cursor %q, want 2 of 3 with a cursor", page1, total, next)
	}
	page2, total2, next2 := listTitles(t, pg(&w.client1Scope, http.MethodGet, entityQuery("ci", ci1, "limit", "2", "cursor", next), ""))
	if len(page2) != 1 || total2 != 3 || next2 != "" || slices.Contains(page1, page2[0]) {
		t.Errorf("second page: %v total %d cursor %q after %v, want the third document", page2, total2, next2, page1)
	}
	memFirst := memCall(&orgWide, http.MethodGet, entityQuery("ci", ci1, "limit", "2"), "")
	memPage1, _, memNext := listTitles(t, memFirst)
	memPage2, _, _ := listTitles(t, memCall(&orgWide, http.MethodGet, entityQuery("ci", ci1, "limit", "2", "cursor", memNext), ""))
	if all := slices.Sorted(slices.Values(append(memPage1, memPage2...))); !slices.Equal(all, []string{"ef-ci-1", "ef-ci-2", "ef-ci-3"}) {
		t.Errorf("memory repository over two pages: %v + %v", memPage1, memPage2)
	}

	// Errors: a target of another client or a missing one 404, one
	// parameter or no UUID 400, an unknown entity type 422.
	for _, c := range []struct {
		name   string
		scope  *database.TenantScope
		target string
		want   int
		field  string
	}{
		{"asset of client 2 as client 1", &w.client1Scope, entityQuery("asset", w.asset2), http.StatusNotFound, ""},
		{"location of client 2 as client 1", &w.client1Scope, entityQuery("location", w.room2), http.StatusNotFound, ""},
		{"missing asset", &orgWide, entityQuery("asset", f.ID()), http.StatusNotFound, ""},
		{"entity_type only", &orgWide, "/api/v1/documents?entity_type=asset", http.StatusBadRequest, "entity_id"},
		{"entity_id only", &orgWide, "/api/v1/documents?entity_id=" + w.asset1, http.StatusBadRequest, "entity_type"},
		{"entity_id no UUID", &orgWide, entityQuery("asset", "not-a-uuid"), http.StatusBadRequest, "entity_id"},
		{"unknown entity type", &orgWide, entityQuery("contact", w.asset1), http.StatusUnprocessableEntity, "entity_type"},
	} {
		rec := pg(c.scope, http.MethodGet, c.target, "")
		if rec.Code != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, rec.Code, rec.Body.String(), c.want)
			continue
		}
		if c.field != "" && !strings.Contains(rec.Body.String(), `"field":"`+c.field+`"`) {
			t.Errorf("%s: body %s names no field %s", c.name, rec.Body.String(), c.field)
		}
	}
	if rec := memCall(&orgWide, http.MethodGet, entityQuery("contact", w.asset1), ""); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("memory repository, unknown entity type: %d, want 422", rec.Code)
	}

	// The site-1 principal sees the documents of asset 1 (site 1), but no
	// link of client 2: the shared document's link to asset 2 stays hidden.
	site1 := database.OrgWideScope(f.OrgA, w.user)
	site1.Sites = database.ScopeIDs(w.site1)
	if got, _, _ := listTitles(t, pg(&site1, http.MethodGet, entityQuery("asset", w.asset1), "")); !slices.Equal(got, []string{"ef-asset1-a", "ef-asset1-b", "ef-shared"}) {
		t.Errorf("asset 1 as site 1: %v", got)
	}
	if titles, _, _ := listTitles(t, pg(&w.client1Scope, http.MethodGet, entityQuery("asset", w.asset1, "search", "shared"), "")); len(titles) != 1 {
		t.Fatalf("shared document as client 1: %v", titles)
	}
	sharedID := w.scalar(t, `SELECT id::text FROM document WHERE organization_id = $1 AND title = 'ef-shared'`, f.OrgA)
	rec := pg(&w.client1Scope, http.MethodGet, "/api/v1/documents/"+sharedID+"/links", "")
	var links struct {
		Data []document.DocumentLink `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &links); err != nil || len(links.Data) != 1 || links.Data[0].EntityID != w.asset1 {
		t.Errorf("links of the shared document as client 1: %s (%v), want only the link to asset 1", rec.Body.String(), err)
	}
}

// TestCreateDocumentWithLinks (WP-811, E-61 option 1): POST
// /api/v1/documents with links creates document and links in one
// transaction; a site-scoped principal needs a link to a target of its
// site (201), without one it gets 403, with a CI of another site 404 and
// no document; an unknown entity type answers 422.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCreateDocumentWithLinks(t *testing.T) {
	w := newLinkWorld(t)
	f := w.f
	ciAt := func(name, room string) string {
		id := f.CI(t, f.OrgA, "", name)
		if _, err := f.Admin.Exec(context.Background(), `UPDATE ci SET location_id = $2 WHERE id = $1`, id, room); err != nil {
			t.Fatal(err)
		}
		return id
	}
	ciSite1, ciSite2 := ciAt("cl-ci-site1", w.room1), ciAt("cl-ci-site2", w.room2)
	if got := w.scalar(t, `SELECT COALESCE(site_id::text, '-') FROM ci WHERE id = $1`, ciSite1); got != w.site1 {
		t.Fatalf("CI at room 1: site %s, want %s", got, w.site1)
	}

	site1 := database.OrgWideScope(f.OrgA, w.user)
	site1.Sites = database.ScopeIDs(w.site1)
	call := entityRouter(t, w, w.repo)
	body := func(title string, links ...string) string {
		var parts []string
		for i := 0; i+1 < len(links); i += 2 {
			parts = append(parts, `{"entity_type":"`+links[i]+`","entity_id":"`+links[i+1]+`"}`)
		}
		out := `{"title":"` + title + `","file_name":"` + title + `.pdf"`
		if parts != nil {
			out += `,"links":[` + strings.Join(parts, ",") + `]`
		}
		return out + "}"
	}
	count := func(title string) string {
		return w.scalar(t, `SELECT count(*)::text FROM document WHERE organization_id = $1 AND title = $2`, f.OrgA, title)
	}

	rec := call(&site1, http.MethodPost, "/api/v1/documents", body("cl-site1", "ci", ciSite1))
	if rec.Code != http.StatusCreated {
		t.Fatalf("site 1 creates with a link to its CI: %d %s, want 201", rec.Code, rec.Body.String())
	}
	var created document.Document
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if got := w.scope(t, created.ID, ciSite1); got != "-|"+w.site1+"|-" {
		t.Errorf("link of the created document: client|site|team %s, want site 1 (the CI has no client)", got)
	}
	if got := w.linkCount(t, created.ID); got != "1/1" {
		t.Errorf("created document: link_count/links %s, want 1/1", got)
	}
	if _, err := w.repo.GetByID(w.site1Ctx, f.OrgA, created.ID); err != nil {
		t.Errorf("site 1 reads its new document: %v", err)
	}
	listed, _, _ := listTitles(t, call(&site1, http.MethodGet, entityQuery("ci", ciSite1), ""))
	if !slices.Equal(listed, []string{"cl-site1"}) {
		t.Errorf("documents of the CI as site 1: %v", listed)
	}

	for _, c := range []struct {
		name, title string
		scope       *database.TenantScope
		links       []string
		want        int
	}{
		{"site 1 without a link", "cl-unlinked", &site1, nil, http.StatusForbidden},
		{"site 1 with the CI of site 2", "cl-foreign", &site1, []string{"ci", ciSite2}, http.StatusNotFound},
		{"site 1 with its CI and the CI of site 2", "cl-second-foreign", &site1, []string{"ci", ciSite1, "ci", ciSite2}, http.StatusNotFound},
		{"site 1 with a missing CI", "cl-missing", &site1, []string{"ci", f.ID()}, http.StatusNotFound},
		{"unknown entity type", "cl-contact", &site1, []string{"contact", ciSite1}, http.StatusUnprocessableEntity},
		{"entity_id no UUID", "cl-no-uuid", &site1, []string{"ci", "not-a-uuid"}, http.StatusBadRequest},
	} {
		got := call(c.scope, http.MethodPost, "/api/v1/documents", body(c.title, c.links...))
		if got.Code != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, got.Code, got.Body.String(), c.want)
		}
		if n := count(c.title); n != "0" {
			t.Errorf("%s: %s documents created, want none", c.name, n)
		}
	}

	// An org-wide principal still creates without links, and with links
	// to targets of two clients.
	orgWide := database.OrgWideScope(f.OrgA, w.user)
	if rec = call(&orgWide, http.MethodPost, "/api/v1/documents", body("cl-org")); rec.Code != http.StatusCreated {
		t.Errorf("org-wide without links: %d %s, want 201", rec.Code, rec.Body.String())
	}
	rec = call(&orgWide, http.MethodPost, "/api/v1/documents", body("cl-org-two", "ci", ciSite1, "asset", w.asset2))
	if rec.Code != http.StatusCreated {
		t.Fatalf("org-wide with two links: %d %s, want 201", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if got := w.linkCount(t, created.ID); got != "2/2" {
		t.Errorf("org-wide document with two links: link_count/links %s, want 2/2", got)
	}
}
