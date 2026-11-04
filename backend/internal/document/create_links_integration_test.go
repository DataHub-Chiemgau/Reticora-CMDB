package document_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// createBody is the body of POST /api/v1/documents with the links given as
// entity type, id pairs.
func createBody(title string, links ...string) string {
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

// TestCreateDocumentWithLinksClientScopeAndAudit (WP-851, E-61 option 1,
// MGT-01): POST /api/v1/documents behind the production route audit. A
// client-scoped principal creates with a link to a CI of its client (201,
// the link takes the client); without a link it gets 403, with the CI of
// another client or a second link to one 404 (no hint on the target), with
// an invalid second link 400 or 422, and nothing is created. A creation
// with links writes document.created and one document.linked per link in
// its transaction, in this order, and no route entry; one without links
// keeps the route entry. The memory repository refuses the same requests
// before it stores anything. TestCreateDocumentWithLinks (WP-811) covers
// the site-scoped principal.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCreateDocumentWithLinksClientScopeAndAudit(t *testing.T) {
	w := newLinkWorld(t)
	f := w.f
	ci1, ci2 := f.CI(t, f.OrgA, f.Client1, "cd-ci1"), f.CI(t, f.OrgA, f.Client2, "cd-ci2")

	auditedRouter := func(repo document.Repository) func(scope *database.TenantScope, body string) *httptest.ResponseRecorder {
		mux := chi.NewRouter()
		mux.Use(middleware.AuditRoute(http.MethodPost, "/api/v1/documents", audit.NewPGRouteAuditor(f.App), middleware.AuditIntent, ""))
		document.NewHandler(repo, nil).RegisterRoutes(mux)
		return func(scope *database.TenantScope, body string) *httptest.ResponseRecorder {
			t.Helper()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/documents", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: w.user})
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req.WithContext(database.ContextWithTenantScope(ctx, scope)))
			if rec.Code >= http.StatusBadRequest && dbLeak.MatchString(rec.Body.String()) {
				t.Errorf("POST %s: body discloses database internals: %s", body, rec.Body.String())
			}
			return rec
		}
	}
	call := auditedRouter(w.repo)
	created := func(rec *httptest.ResponseRecorder, what string) string {
		t.Helper()
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s, want 201", what, rec.Code, rec.Body.String())
		}
		var d document.Document
		if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
			t.Fatal(err)
		}
		return d.ID
	}
	// entries lists action and link target of the audit entries of a
	// document in chain order.
	entries := func(docID string) []string {
		t.Helper()
		rows, err := f.Admin.Query(context.Background(), `
			SELECT action || COALESCE('|' || (changes->>'entity_type') || '|' || (changes->>'entity_id'), '')
				|| '|' || COALESCE(actor_id::text, '-')
			FROM audit_log WHERE organization_id = $1 AND resource_id = $2 ORDER BY seq`, f.OrgA, docID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	noIntents := func(what string) {
		t.Helper()
		if n := w.scalar(t, `SELECT count(*)::text FROM audit_route_intent WHERE organization_id = $1`, f.OrgA); n != "0" {
			t.Errorf("%s: %s route intents left, want none", what, n)
		}
	}

	// Client 1 creates with a link to its CI: the link takes the client,
	// the transaction ends with document.created and document.linked.
	docID := created(call(&w.client1Scope, createBody("cd-client1", "ci", ci1)), "client 1 with its CI")
	if got := w.scope(t, docID, ci1); got != f.Client1+"|-|-" {
		t.Errorf("link of the created document: client|site|team %s, want client 1", got)
	}
	if got := w.linkCount(t, docID); got != "1/1" {
		t.Errorf("created document: link_count/links %s, want 1/1", got)
	}
	got := entries(docID)
	want := []string{document.ActionCreated + "|" + w.user, document.ActionLinked + "|ci|" + ci1 + "|" + w.user}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("audit entries of the creation with a link: %v, want %v (no route entry)", got, want)
	}
	if title := w.scalar(t, `SELECT changes->'after'->>'title' FROM audit_log WHERE organization_id = $1 AND resource_id = $2 AND action = $3`,
		f.OrgA, docID, document.ActionCreated); title != "cd-client1" {
		t.Errorf("document.created holds title %q, want the new document", title)
	}
	noIntents("creation with a link")

	// Refused creations: nothing stored, no entry.
	before := w.scalar(t, `SELECT count(*)::text FROM audit_log WHERE organization_id = $1`, f.OrgA)
	for _, c := range []struct {
		name, title string
		links       []string
		want        int
	}{
		{"without a link", "cd-unlinked", nil, http.StatusForbidden},
		{"with the CI of client 2", "cd-foreign", []string{"ci", ci2}, http.StatusNotFound},
		{"with the asset of client 2", "cd-foreign-asset", []string{"asset", w.asset2}, http.StatusNotFound},
		{"with its CI and the CI of client 2", "cd-second-foreign", []string{"ci", ci1, "ci", ci2}, http.StatusNotFound},
		{"with its CI and a missing CI", "cd-second-missing", []string{"ci", ci1, "ci", f.ID()}, http.StatusNotFound},
		{"with its CI and a second link without UUID", "cd-second-no-uuid", []string{"ci", ci1, "ci", "not-a-uuid"}, http.StatusBadRequest},
		{"with its CI and an unknown entity type", "cd-second-contact", []string{"ci", ci1, "contact", ci1}, http.StatusUnprocessableEntity},
	} {
		rec := call(&w.client1Scope, createBody(c.title, c.links...))
		if rec.Code != c.want {
			t.Errorf("client 1 %s: %d %s, want %d", c.name, rec.Code, rec.Body.String(), c.want)
		}
		if rec.Code == http.StatusNotFound && strings.Contains(rec.Body.String(), ci2) {
			t.Errorf("client 1 %s: the answer names the target: %s", c.name, rec.Body.String())
		}
		if n := w.scalar(t, `SELECT count(*)::text FROM document WHERE organization_id = $1 AND title = $2`, f.OrgA, c.title); n != "0" {
			t.Errorf("client 1 %s: %s documents created, want none", c.name, n)
		}
	}
	if after := w.scalar(t, `SELECT count(*)::text FROM audit_log WHERE organization_id = $1`, f.OrgA); after != before {
		t.Errorf("audit entries after the refused creations: %s, want %s", after, before)
	}
	if n := w.scalar(t, `SELECT count(*)::text FROM document_link WHERE organization_id = $1 AND entity_id = $2`, f.OrgA, ci2); n != "0" {
		t.Errorf("links to the CI of client 2: %s, want none", n)
	}
	noIntents("refused creations")

	// Org-wide principals create without links (route entry) and with links
	// to targets of two clients (domain entries).
	orgWide := database.OrgWideScope(f.OrgA, w.user)
	plain := created(call(&orgWide, createBody("cd-org")), "org-wide without links")
	if plainEntries := entries(plain); len(plainEntries) != 1 || !strings.HasPrefix(plainEntries[0], "documents.create|") {
		t.Errorf("audit entries of the creation without links: %v, want the route entry documents.create", plainEntries)
	}
	two := created(call(&orgWide, createBody("cd-org-two", "ci", ci1, "ci", ci2)), "org-wide with two links")
	got = entries(two)
	want = []string{document.ActionCreated + "|" + w.user, document.ActionLinked + "|ci|" + ci1 + "|" + w.user,
		document.ActionLinked + "|ci|" + ci2 + "|" + w.user}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("audit entries of the creation with two links: %v, want %v", got, want)
	}
	noIntents("org-wide creations")

	// The memory repository: the same answers before anything is stored.
	mem := document.NewMemoryRepository()
	memCall := entityRouter(t, w, mem)
	for _, c := range []struct {
		name  string
		links []string
		want  int
	}{
		{"client 1 without a link", nil, http.StatusForbidden},
		{"a second link without UUID", []string{"ci", ci1, "ci", "not-a-uuid"}, http.StatusBadRequest},
		{"a second link of an unknown entity type", []string{"ci", ci1, "contact", ci1}, http.StatusUnprocessableEntity},
	} {
		if rec := memCall(&w.client1Scope, http.MethodPost, "/api/v1/documents", createBody("cd-mem", c.links...)); rec.Code != c.want {
			t.Errorf("memory repository, %s: %d %s, want %d", c.name, rec.Code, rec.Body.String(), c.want)
		}
	}
	if _, total, err := mem.List(context.Background(), f.OrgA, document.FilterParams{}, api.PaginationParams{Limit: 10}); err != nil || total != 0 {
		t.Errorf("memory repository after the refused creations: %d documents (%v), want none", total, err)
	}
	if rec := memCall(&w.client1Scope, http.MethodPost, "/api/v1/documents", createBody("cd-mem", "ci", ci1)); rec.Code != http.StatusCreated {
		t.Errorf("memory repository, client 1 with a link: %d %s, want 201", rec.Code, rec.Body.String())
	}
}
