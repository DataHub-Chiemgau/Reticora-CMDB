package document_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// TestDocumentRepositoryClientScope runs the document repository with the
// principal's tenant scope (TEN-06, WP-019): a document linked only to a
// client-2 CI is invisible to a principal restricted to client 1, who also
// cannot link documents to client-2 objects.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDocumentRepositoryClientScope(t *testing.T) {
	f := scopetest.Seed(t, "34")
	ownCI := f.CI(t, f.OrgA, f.Client1, "doc-c1")
	foreignCI := f.CI(t, f.OrgA, f.Client2, "doc-c2")
	user := f.AppUser(t, f.OrgA, "author")

	repo := document.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	doc := func(title string) *document.Document {
		return &document.Document{OrganizationID: f.OrgA, Title: title, FileName: title + ".pdf", StorageKey: "k/" + title, UploadedBy: user, Category: "general"}
	}
	ownDoc, foreignDoc := doc("own"), doc("foreign")
	for _, d := range []*document.Document{ownDoc, foreignDoc} {
		if err := repo.Create(orgCtx, d); err != nil {
			t.Fatalf("org-wide document %s: %v", d.Title, err)
		}
	}
	if err := repo.LinkDocument(orgCtx, &document.DocumentLink{OrganizationID: f.OrgA, DocumentID: ownDoc.ID, EntityType: "ci", EntityID: ownCI}); err != nil {
		t.Fatalf("link own document: %v", err)
	}
	if err := repo.LinkDocument(orgCtx, &document.DocumentLink{OrganizationID: f.OrgA, DocumentID: foreignDoc.ID, EntityType: "ci", EntityID: foreignCI}); err != nil {
		t.Fatalf("link foreign document: %v", err)
	}

	ctx := f.ClientCtx(f.Client1)
	list, total, err := repo.List(ctx, f.OrgA, document.FilterParams{}, api.PaginationParams{Limit: 100})
	if err != nil || total != 1 || len(list) != 1 || list[0].ID != ownDoc.ID {
		t.Fatalf("client-1 documents: total=%d %+v err=%v, want only the own one", total, list, err)
	}
	if _, err = repo.GetByID(ctx, f.OrgA, foreignDoc.ID); err == nil {
		t.Fatal("client-1 principal read a document of a client-2 CI")
	}
	if docs, linkErr := repo.GetLinksForEntity(ctx, f.OrgA, "ci", foreignCI); linkErr != nil || len(docs) != 0 {
		t.Fatalf("client-1 documents of client-2 CI: %+v err=%v, want none", docs, linkErr)
	}
	if err = repo.LinkDocument(ctx, &document.DocumentLink{OrganizationID: f.OrgA, DocumentID: ownDoc.ID, EntityType: "ci", EntityID: foreignCI}); err == nil {
		t.Fatal("client-1 principal linked a document to a client-2 CI")
	}
	if err = repo.Delete(ctx, f.OrgA, foreignDoc.ID); err == nil {
		t.Fatal("client-1 principal deleted a document of a client-2 CI")
	}
	var count int
	if err = f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM document WHERE id = $1`, foreignDoc.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("client-2 document: count=%d err=%v, want 1", count, err)
	}
	if _, _, err = repo.List(context.Background(), f.OrgA, document.FilterParams{}, api.PaginationParams{Limit: 10}); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("list without scope: got %v, want ErrNoTenantScope", err)
	}
}

// dbLeak matches database internals in a response body: SQLSTATE codes,
// PostgreSQL error texts, constraint, policy, relation and type names.
var dbLeak = regexp.MustCompile(`(?i)sqlstate|constraint|policy|violat|row-level|relation|_fkey|_pkey|_key\b|_check|duplicate key|foreign key|syntax for type|invalid input|pgx|pgconn|error:|document_link|link document`)

// TestDocumentHandlerErrorsAreSanitized carries the probe
// TestVerifyB03document (WP-019, MGT-03) as a regression test through the
// document handler with PostgreSQL: for a principal restricted to client 1 a
// document of a client-2 CI answers 404, a link to an unknown or disallowed
// object (as an org-wide principal) 404 or 422 and an invalid id 400 or 404,
// never with an SQLSTATE, constraint or policy text; a client-1 link to a
// client-2, unknown or disallowed object is a write outside its scope and
// refused alike with 403. The client-2 document and its link stay
// unchanged; the own document answers 2xx.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDocumentHandlerErrorsAreSanitized(t *testing.T) {
	f := scopetest.Seed(t, "34")
	ownCI := f.CI(t, f.OrgA, f.Client1, "doc-h-c1")
	ownCI2 := f.CI(t, f.OrgA, f.Client1, "doc-h-c1-second")
	foreignCI := f.CI(t, f.OrgA, f.Client2, "doc-h-c2")
	user := f.AppUser(t, f.OrgA, "doc-h-author")
	const unknown = "c0de34a0-0000-4000-8000-00000000dead"

	repo := document.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)
	doc := func(title string) *document.Document {
		return &document.Document{OrganizationID: f.OrgA, Title: title, FileName: title + ".pdf", StorageKey: "k/" + title, UploadedBy: user, Category: "general"}
	}
	ownDoc, foreignDoc := doc("doc-h-own"), doc("doc-h-foreign")
	for _, d := range []*document.Document{ownDoc, foreignDoc} {
		if err := repo.Create(orgCtx, d); err != nil {
			t.Fatalf("org-wide document %s: %v", d.Title, err)
		}
	}
	for d, ci := range map[*document.Document]string{ownDoc: ownCI, foreignDoc: foreignCI} {
		if err := repo.LinkDocument(orgCtx, &document.DocumentLink{OrganizationID: f.OrgA, DocumentID: d.ID, EntityType: "ci", EntityID: ci}); err != nil {
			t.Fatalf("link %s: %v", d.Title, err)
		}
	}

	mux := chi.NewRouter()
	document.NewHandler(repo, nil).RegisterRoutes(mux)
	scope := database.OrgWideScope(f.OrgA, user)
	scope.Clients = database.ScopeIDs(f.Client1)
	orgWide := database.OrgWideScope(f.OrgA, user)
	callAs := func(scope *database.TenantScope, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(database.ContextWithTenantScope(ctx, scope)))
		return w
	}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		return callAs(&scope, method, path, body)
	}
	link := func(entityType, entityID string) string {
		return `{"entity_type":"` + entityType + `","entity_id":"` + entityID + `"}`
	}

	for _, c := range []struct {
		name, method, path, body string
		want                     int
		orgWide                  bool
	}{
		// Foreign row: the document of the client-2 CI does not exist for client 1.
		{"get client-2 document", http.MethodGet, "/api/v1/documents/" + foreignDoc.ID, "", http.StatusNotFound, false},
		{"patch client-2 document", http.MethodPatch, "/api/v1/documents/" + foreignDoc.ID, `{"title":"hijacked"}`, http.StatusNotFound, false},
		{"delete client-2 document", http.MethodDelete, "/api/v1/documents/" + foreignDoc.ID, "", http.StatusNotFound, false},
		{"link client-2 document", http.MethodPost, "/api/v1/documents/" + foreignDoc.ID + "/links", link("ci", ownCI), http.StatusNotFound, false},
		// A link of client 1 to a client-2 CI, to an unknown CI or to a
		// disallowed type would not be a client-1 row: a write outside the
		// client-1 scope, refused alike without telling the cases apart.
		{"link to a client-2 CI", http.MethodPost, "/api/v1/documents/" + ownDoc.ID + "/links", link("ci", foreignCI), http.StatusForbidden, false},
		{"link to an unknown CI (client 1)", http.MethodPost, "/api/v1/documents/" + ownDoc.ID + "/links", link("ci", unknown), http.StatusForbidden, false},
		{"link to a disallowed entity type (client 1)", http.MethodPost, "/api/v1/documents/" + ownDoc.ID + "/links", link("bogus", ownCI), http.StatusForbidden, false},
		// Invalid reference.
		{"link to an unknown CI", http.MethodPost, "/api/v1/documents/" + ownDoc.ID + "/links", link("ci", unknown), http.StatusNotFound, true},
		{"link to a disallowed entity type", http.MethodPost, "/api/v1/documents/" + ownDoc.ID + "/links", link("bogus", ownCI), http.StatusUnprocessableEntity, true},
		{"link to an unknown document", http.MethodPost, "/api/v1/documents/" + unknown + "/links", link("ci", ownCI), http.StatusNotFound, false},
		// Invalid id.
		{"get invalid id", http.MethodGet, "/api/v1/documents/not-a-uuid", "", http.StatusNotFound, false},
		{"patch invalid id", http.MethodPatch, "/api/v1/documents/not-a-uuid", `{"title":"x"}`, http.StatusNotFound, false},
		{"delete invalid id", http.MethodDelete, "/api/v1/documents/not-a-uuid", "", http.StatusNotFound, false},
		{"links of an invalid id", http.MethodGet, "/api/v1/documents/not-a-uuid/links", "", http.StatusBadRequest, false},
		{"link with an invalid entity id", http.MethodPost, "/api/v1/documents/" + ownDoc.ID + "/links", link("ci", "not-a-uuid"), http.StatusBadRequest, false},
	} {
		as := &scope
		if c.orgWide {
			as = &orgWide
		}
		w := callAs(as, c.method, c.path, c.body)
		if w.Code != c.want {
			t.Errorf("%s: status %d, want %d: %s", c.name, w.Code, c.want, w.Body.String())
		}
		if dbLeak.MatchString(w.Body.String()) {
			t.Errorf("%s: body discloses database internals: %s", c.name, w.Body.String())
		}
	}

	// The client-2 document and its link are unchanged; the own document
	// got no link to a foreign or unknown object.
	bg := context.Background()
	var title string
	if err := f.Admin.QueryRow(bg, `SELECT title FROM document WHERE id = $1`, foreignDoc.ID).Scan(&title); err != nil || title != "doc-h-foreign" {
		t.Errorf("client-2 document: title=%q err=%v, want unchanged", title, err)
	}
	var links []string
	rows, err := f.Admin.Query(bg, `SELECT document_id::text || '>' || entity_id::text FROM document_link WHERE document_id IN ($1, $2) ORDER BY 1`, ownDoc.ID, foreignDoc.ID)
	if err != nil {
		t.Fatalf("links: %v", err)
	}
	for rows.Next() {
		var l string
		if err = rows.Scan(&l); err != nil {
			t.Fatalf("scan link: %v", err)
		}
		links = append(links, l)
	}
	rows.Close()
	want := []string{foreignDoc.ID + ">" + foreignCI, ownDoc.ID + ">" + ownCI}
	if want[1] < want[0] {
		want[0], want[1] = want[1], want[0]
	}
	if strings.Join(links, ",") != strings.Join(want, ",") {
		t.Errorf("links: %v, want %v", links, want)
	}
	if w := call(http.MethodGet, "/api/v1/documents/"+foreignDoc.ID+"/links", ""); w.Code != http.StatusOK || strings.Contains(w.Body.String(), foreignCI) {
		t.Errorf("links of the client-2 document: %d %s, want 200 without the client-2 CI", w.Code, w.Body.String())
	}

	// Positive control: the own document.
	if w := call(http.MethodGet, "/api/v1/documents/"+ownDoc.ID, ""); w.Code != http.StatusOK {
		t.Errorf("get own document: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodPatch, "/api/v1/documents/"+ownDoc.ID, `{"title":"doc-h-own-renamed"}`); w.Code != http.StatusOK {
		t.Errorf("patch own document: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodPost, "/api/v1/documents/"+ownDoc.ID+"/links", link("ci", ownCI2)); w.Code != http.StatusCreated {
		t.Errorf("link own document to an own CI: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodGet, "/api/v1/documents/"+ownDoc.ID+"/links", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), ownCI2) {
		t.Errorf("links of the own document: %d %s, want the new link", w.Code, w.Body.String())
	}
}
