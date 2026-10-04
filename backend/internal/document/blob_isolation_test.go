package document

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The tests of this file cover WP-030 (TEN-06, MGT-01): document objects are
// stored below org/<org_id>/ only, keys never come from a request, and a key
// of another organization is rejected on every path.

func TestCheckOrgKey(t *testing.T) {
	const org = "11111111-1111-4111-8111-111111111111"
	for _, c := range []struct {
		key string
		ok  bool
	}{
		{blob.OrgKey(org, "documents", "d1", "1"), true},
		{"org/" + org + "/documents/d1/1", true},
		{"org/22222222-2222-4222-8222-222222222222/documents/d1/1", false},
		{"documents/" + org + "/d1/1", false},
		{"org/" + org + "/../22222222-2222-4222-8222-222222222222/x", false},
		{"org/" + org + "//x", false},
		{"org/" + org + "/", false},
		{"org/" + org + "/a\\b", false},
		{"", false},
	} {
		if err := blob.CheckOrgKey(org, c.key); (err == nil) != c.ok {
			t.Errorf("CheckOrgKey(%q) = %v, want ok=%v", c.key, err, c.ok)
		}
	}
	if blob.CheckOrgKey("", "org//x") == nil {
		t.Error("CheckOrgKey accepted an empty organization")
	}
}

func TestOwnsStorageKey(t *testing.T) {
	for _, c := range []struct {
		key string
		ok  bool
	}{
		{"org/org-1/documents/doc-1/1", true},
		{"documents/org-1/doc-1/1", true}, // uploads before migration 000066
		{"org/org-1/documents/doc-2/1", false},
		{"org/org-2/documents/doc-1/1", false},
		{"documents/org-2/doc-1/1", false},
		{"documents/org-1/doc-1/../../org-2/x", false},
		{"k1", false},
	} {
		if got := ownsStorageKey("org-1", "doc-1", c.key); got != c.ok {
			t.Errorf("ownsStorageKey(%q) = %v, want %v", c.key, got, c.ok)
		}
	}
}

// TestCreateIgnoresRequestedStorageKey shows that a client cannot point a new
// document at an object of another organization, and that upload keys carry
// the organization prefix.
func TestCreateIgnoresRequestedStorageKey(t *testing.T) {
	repo, mux := newDocumentFixture(t)
	body := `{"title":"t","file_name":"f.pdf","storage_key":"org/org-2/documents/x/1"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/documents", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "user-1"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: status %d: %s", w.Code, w.Body.String())
	}
	var created Document
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.StorageKey != "" {
		t.Fatalf("created document storage key %q, want empty", created.StorageKey)
	}

	w = contentRequest(t, mux, http.MethodPut, "/api/v1/documents/"+created.ID+"/content", "application/pdf", "%PDF")
	if w.Code != http.StatusOK {
		t.Fatalf("upload: status %d: %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if want := "org/org-1/documents/" + created.ID + "/"; !strings.HasPrefix(created.StorageKey, want) {
		t.Fatalf("upload key %q, want prefix %q", created.StorageKey, want)
	}

	// A key of another organization (as left by older data) is not presigned.
	if _, err := repo.SetStorage(context.Background(), "org-1", created.ID, "org/org-2/documents/x/1", "application/pdf", 4); err != nil {
		t.Fatalf("plant foreign key: %v", err)
	}
	w = contentRequest(t, mux, http.MethodGet, "/api/v1/documents/"+created.ID+"/content", "", "")
	if w.Code != http.StatusConflict || strings.Contains(w.Body.String(), "org-2") {
		t.Fatalf("download of a foreign key: status %d body %s, want 409 without URL", w.Code, w.Body.String())
	}
}

// TestStorageKeyRejectedOutsideOrganization runs against PostgreSQL: the
// repository and the CHECK constraint of migration 000066 reject keys of
// another organization, the repository ignores a key passed on creation.
func TestStorageKeyRejectedOutsideOrganization(t *testing.T) {
	f := scopetest.Seed(t, "4b")
	user := f.AppUser(t, f.OrgA, "blob-user")
	repo := NewPGRepository(f.App)
	ctx := f.OrgCtx(f.OrgA)

	doc := &Document{OrganizationID: f.OrgA, Title: "t", FileName: "f.pdf", MimeType: "application/pdf",
		Category: "general", UploadedBy: user, StorageKey: blob.OrgKey(f.OrgB, "documents", "x", "1")}
	if err := repo.Create(ctx, doc); err != nil {
		t.Fatalf("create: %v", err)
	}
	var stored string
	if err := f.Admin.QueryRow(context.Background(), `SELECT storage_key FROM document WHERE id = $1`, doc.ID).Scan(&stored); err != nil || stored != "" {
		t.Fatalf("stored key %q, %v; want empty", stored, err)
	}

	if _, err := repo.SetStorage(ctx, f.OrgA, doc.ID, blob.OrgKey(f.OrgB, "documents", doc.ID, "1"), "application/pdf", 1); !errors.Is(err, blob.ErrForeignKey) {
		t.Errorf("SetStorage with a key of org B: %v, want ErrForeignKey", err)
	}
	own := blob.OrgKey(f.OrgA, "documents", doc.ID, "1")
	if _, err := repo.SetStorage(ctx, f.OrgA, doc.ID, own, "application/pdf", 1); err != nil {
		t.Errorf("SetStorage with own key: %v", err)
	}

	// The constraint holds for every writer, here the maintenance connection.
	for _, key := range []string{
		blob.OrgKey(f.OrgB, "documents", doc.ID, "1"),
		"documents/" + f.OrgB + "/" + doc.ID + "/1",
		"org/" + f.OrgA + "/../" + f.OrgB + "/x",
		"k1",
	} {
		if _, err := f.Admin.Exec(context.Background(), `UPDATE document SET storage_key = $2 WHERE id = $1`, doc.ID, key); err == nil {
			t.Errorf("CHECK accepted storage key %q", key)
		}
	}
	if _, err := f.Admin.Exec(context.Background(), `UPDATE document SET storage_key = $2 WHERE id = $1`,
		doc.ID, "documents/"+f.OrgA+"/"+doc.ID+"/1"); err != nil {
		t.Errorf("CHECK rejected the legacy upload key: %v", err)
	}
}
