package document

// Field permissions of documents in REST (RBA-07, WP-267, finding F-1 of
// WP-326), without a database: the projection of the document and of its
// versions, the write checks of PATCH and of an upload, the list filters
// and the file name of a download. The channel matrix against PostgreSQL
// is TestFieldRightsChannelMatrixObjectKinds (internal/tenant/rls).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

const frRole = "00000000-0000-4000-8000-000000000267"

// documentPolicy is the policy of a role with document:read and
// document:write: core.tags none, core.category read, core.file_name
// sensitive without grant.
func documentPolicy(t *testing.T) *permission.FieldPolicy {
	t.Helper()
	ctx := context.Background()
	repo := permission.NewMemoryFieldGrants()
	role := frRole
	for _, g := range []permission.FieldGrant{
		{RoleID: &role, ObjectKind: permission.KindDocument, FieldKey: strPtr("core.tags"), Level: permission.LevelNone},
		{RoleID: &role, ObjectKind: permission.KindDocument, FieldKey: strPtr("core.category"), Level: permission.LevelRead},
	} {
		if _, err := repo.CreateFieldGrant(ctx, "org-1", &g); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.CreateSensitiveField(ctx, "org-1", &permission.SensitiveField{ObjectKind: permission.KindDocument,
		FieldKey: "core.file_name"}); err != nil {
		t.Fatal(err)
	}
	policy, err := permission.NewFieldResolver(nil, nil, repo, nil).PolicyForGrants(ctx, "org-1",
		[]identity.Grant{{RoleID: frRole, Permissions: []identity.Permission{identity.PermDocumentRead, identity.PermDocumentWrite}}}, nil)
	if err != nil || !policy.Restricted(permission.KindDocument) {
		t.Fatalf("policy: %v", err)
	}
	return policy
}

func strPtr(s string) *string { return &s }

// frRequest sends a request of org-1 with the policy.
func frRequest(t *testing.T, mux http.Handler, policy *permission.FieldPolicy, method, url, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, url, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "user-1"})
	ctx = permission.WithPolicy(ctx, policy)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req.WithContext(ctx))
	return w
}

func frFields(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	var problem struct {
		Fields []string `json:"fields"`
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("got %d %s, want 403", w.Code, w.Body)
	}
	_ = json.Unmarshal(w.Body.Bytes(), &problem)
	return problem.Fields
}

func TestDocumentFieldRightsREST(t *testing.T) {
	repo := NewMemoryRepository()
	doc := &Document{OrganizationID: "org-1", Title: "Handbuch", Description: "offen", FileName: "geheim.pdf",
		MimeType: "application/pdf", Category: "general", Tags: []string{"vertraulich"}}
	if err := repo.Create(context.Background(), doc); err != nil {
		t.Fatal(err)
	}
	blobs := &presignRecorder{fakeBlobStore: newFakeBlobStore()}
	mux := chi.NewRouter()
	NewHandler(repo, blobs).RegisterRoutes(mux)
	policy := documentPolicy(t)
	base := "/api/v1/documents/" + doc.ID

	// Reading: tags and file_name are left out, the rest stays.
	for _, url := range []string{base, "/api/v1/documents"} {
		w := frRequest(t, mux, policy, http.MethodGet, url, "", "")
		body := w.Body.String()
		if w.Code != http.StatusOK || strings.Contains(body, "geheim.pdf") || strings.Contains(body, "vertraulich") ||
			strings.Contains(body, `"tags"`) || strings.Contains(body, `"file_name"`) {
			t.Errorf("GET %s: %d %s", url, w.Code, body)
		}
		if !strings.Contains(body, `"description":"offen"`) || !strings.Contains(body, `"category":"general"`) ||
			!strings.Contains(body, `"title":"Handbuch"`) {
			t.Errorf("GET %s lost readable fields: %s", url, body)
		}
	}
	// A search (matches file_name) or a category filter or sort on a
	// field it cannot read in every scope: 403 with the field.
	if got := frFields(t, frRequest(t, mux, policy, http.MethodGet, "/api/v1/documents?search=geheim", "", "")); !slices.Equal(got, []string{"file_name"}) {
		t.Errorf("search: fields %v", got)
	}

	// Writing: an editable field passes, a read-only, an unreadable and a
	// sensitive one are 403 with the fields.
	if w := frRequest(t, mux, policy, http.MethodPatch, base, "application/json", `{"description":"neu"}`); w.Code != http.StatusOK {
		t.Errorf("PATCH description: %d %s", w.Code, w.Body)
	}
	if got := frFields(t, frRequest(t, mux, policy, http.MethodPatch, base, "application/json", `{"category":"manual","tags":["vertraulich"]}`)); !slices.Equal(got, []string{"category", "tags"}) {
		t.Errorf("PATCH category, tags: fields %v", got)
	}
	// The unchanged readable category passes, the unreadable tags count
	// whatever their value (no equality oracle).
	if w := frRequest(t, mux, policy, http.MethodPatch, base, "application/json", `{"category":"general"}`); w.Code != http.StatusOK {
		t.Errorf("PATCH unchanged category: %d %s", w.Code, w.Body)
	}
	if got := frFields(t, frRequest(t, mux, policy, http.MethodPost, "/api/v1/documents", "application/json",
		`{"title":"x","file_name":"x.pdf","tags":["a"]}`)); !slices.Equal(got, []string{"file_name", "tags"}) {
		t.Errorf("POST: fields %v", got)
	}
	if stored, _ := repo.GetByID(context.Background(), "org-1", doc.ID); stored.Category != "general" || !slices.Equal(stored.Tags, []string{"vertraulich"}) {
		t.Errorf("refused writes changed the document: %+v", stored)
	}

	// An upload writes file_size and mime_type: allowed here, the version
	// and the document answer without the hidden fields.
	w := frRequest(t, mux, policy, http.MethodPost, base+"/versions", "application/pdf", "%PDF-fr")
	if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), `"size"`) {
		t.Fatalf("upload: %d %s", w.Code, w.Body)
	}
	markClean(t, repo, doc.ID, 1)
	if w = frRequest(t, mux, policy, http.MethodGet, base+"/content", "", ""); w.Code != http.StatusOK {
		t.Fatalf("download: %d %s", w.Code, w.Body)
	}
	if dl := blobs.downloads[len(blobs.downloads)-1]; dl.FileName != doc.ID {
		t.Errorf("download file name %q, want the id for an unreadable file_name", dl.FileName)
	}

	// mime_type read-only: the upload changes it and is refused.
	ctx := context.Background()
	grants := permission.NewMemoryFieldGrants()
	role := frRole
	if _, err := grants.CreateFieldGrant(ctx, "org-1", &permission.FieldGrant{RoleID: &role, ObjectKind: permission.KindDocument,
		FieldKey: strPtr("core.mime_type"), Level: permission.LevelRead}); err != nil {
		t.Fatal(err)
	}
	readOnly, err := permission.NewFieldResolver(nil, nil, grants, nil).PolicyForGrants(ctx, "org-1",
		[]identity.Grant{{RoleID: frRole, Permissions: []identity.Permission{identity.PermDocumentRead, identity.PermDocumentWrite}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := frFields(t, frRequest(t, mux, readOnly, http.MethodPost, base+"/versions", "text/plain", "plain text")); !slices.Equal(got, []string{"mime_type"}) {
		t.Errorf("upload with read-only mime_type: fields %v", got)
	}
	// Versions without the members of an unreadable mime_type.
	noMime := permission.NewMemoryFieldGrants()
	if _, err = noMime.CreateFieldGrant(ctx, "org-1", &permission.FieldGrant{RoleID: &role, ObjectKind: permission.KindDocument,
		FieldKey: strPtr("core.mime_type"), Level: permission.LevelNone}); err != nil {
		t.Fatal(err)
	}
	hidden, err := permission.NewFieldResolver(nil, nil, noMime, nil).PolicyForGrants(ctx, "org-1",
		[]identity.Grant{{RoleID: frRole, Permissions: []identity.Permission{identity.PermDocumentRead}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if w = frRequest(t, mux, hidden, http.MethodGet, base+"/versions", "", ""); w.Code != http.StatusOK ||
		strings.Contains(w.Body.String(), "mime_type") || !strings.Contains(w.Body.String(), `"size"`) {
		t.Errorf("versions: %d %s", w.Code, w.Body)
	}
}
