package contact_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/contact"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestContactsThroughTheVault covers WP-242 (AUD-01, SEC-05) against
// PostgreSQL: name, phone and e-mail of a contact live in the PII vault and
// contact keeps only the surrogate; the contacts resource reads and writes
// them unchanged (with the 24/7 flag of AUD-01); lookup by e-mail and the
// search by name keep working without plaintext in contact or the search
// index; existing contacts are moved by the backfill; deleting a contact
// deletes its vault entry.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestContactsThroughTheVault(t *testing.T) {
	f := scopetest.Seed(t, "a2")
	bg := context.Background()
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	pii.SetDefault(pii.NewVault(f.App, enc))
	t.Cleanup(func() { pii.SetDefault(nil) })
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	repo := contact.NewPGRepository(f.App)
	mux := chi.NewRouter()
	contact.NewHandler(repo).RegisterRoutes(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx))
		return w
	}
	plaintext := func(query string, args ...any) bool {
		t.Helper()
		var found bool
		if qerr := f.Admin.QueryRow(bg, `SELECT EXISTS (`+query+`)`, args...).Scan(&found); qerr != nil {
			t.Fatal(qerr)
		}
		return found
	}

	// The REST resource stays as it was.
	w := call(http.MethodPost, "/api/v1/contacts", `{"client_id": "`+f.Client1+`", "display_name": "Erika Mustermann",
		"email": "Erika@Example.org", "phone": "+49 861 555", "role": "Admin", "is_24_7": true}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created contact.Contact
	if err = json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if plaintext(`SELECT 1 FROM contact WHERE id = $1 AND (display_name IS NOT NULL OR email IS NOT NULL OR phone IS NOT NULL
			OR pii_subject_id IS NULL)`, created.ID) ||
		plaintext(`SELECT 1 FROM pii_subject WHERE encode(encrypted_payload, 'escape') ~ 'Mustermann'`) {
		t.Fatal("contact data stored in plaintext")
	}
	w = call(http.MethodGet, "/api/v1/contacts/"+created.ID, "")
	var got contact.Contact
	if err = json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.DisplayName != "Erika Mustermann" ||
		got.Email != "Erika@Example.org" || got.Phone != "+49 861 555" || got.Role != "Admin" || !got.Is247 {
		t.Fatalf("get = %+v (%v)", got, err)
	}
	w = call(http.MethodPatch, "/api/v1/contacts/"+created.ID, `{"email": "e.mustermann@example.org", "notes": "on call"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "e.mustermann@example.org") ||
		!strings.Contains(w.Body.String(), "Erika Mustermann") {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	if plaintext(`SELECT 1 FROM contact WHERE id = $1 AND (email IS NOT NULL OR display_name IS NOT NULL)`, created.ID) {
		t.Fatal("update wrote plaintext")
	}
	if list := call(http.MethodGet, "/api/v1/contacts", "").Body.String(); !strings.Contains(list, "Erika Mustermann") {
		t.Fatalf("list does not resolve: %s", list)
	}

	// Lookup by e-mail through the blind index.
	byMail, err := repo.ListByEmail(ctx, f.OrgA, "  E.Mustermann@example.org ")
	if err != nil || len(byMail) != 1 || byMail[0].ID != created.ID {
		t.Fatalf("by e-mail = %+v, %v", byMail, err)
	}
	if none, _ := repo.ListByEmail(ctx, f.OrgA, "erika@example.org"); len(none) != 0 {
		t.Fatalf("old address still finds %+v", none)
	}

	// An existing contact with plaintext is moved by the backfill.
	legacy := f.ID()
	if _, err = f.Admin.Exec(bg, `INSERT INTO contact (id, organization_id, client_id, display_name, email, phone)
		VALUES ($1, $2, $3, 'Max Legacy', 'max@example.org', '0861 1')`, legacy, f.OrgA, f.Client1); err != nil {
		t.Fatal(err)
	}
	res, err := jobs.NewPIIBackfill(f.App).RunOrg(bg, f.OrgA)
	if err != nil || res.Contacts != 1 {
		t.Fatalf("backfill = %+v, %v", res, err)
	}
	if plaintext(`SELECT 1 FROM contact WHERE organization_id = $1 AND (display_name IS NOT NULL OR email IS NOT NULL OR phone IS NOT NULL)`, f.OrgA) {
		t.Fatal("plaintext contacts left after the backfill")
	}
	if moved, gerr := repo.GetByID(ctx, f.OrgA, legacy); gerr != nil || moved.DisplayName != "Max Legacy" || moved.Email != "max@example.org" {
		t.Fatalf("moved contact = %+v, %v", moved, gerr)
	}

	// Search by name without plaintext in the index.
	searchRepo := search.NewPGRepository(f.App)
	if _, err = searchRepo.ReindexTenant(ctx, f.OrgA); err != nil {
		t.Fatal(err)
	}
	if plaintext(`SELECT 1 FROM search_document WHERE organization_id = $1 AND (title || summary) ~ '(Mustermann|Legacy|example)'`, f.OrgA) {
		t.Fatal("the search index holds contact data in plaintext")
	}
	for text, want := range map[string]string{"mustermann": "Erika Mustermann", "Max": "Max Legacy", "e.mustermann@example.org": "Erika Mustermann"} {
		hits, qerr := searchRepo.Query(ctx, search.Query{OrganizationID: f.OrgA, UserID: f.User, Text: text,
			EntityTypes: []string{"contact"}, Limit: 10})
		if qerr != nil || len(hits.Data) != 1 || hits.Data[0].Title != want {
			t.Fatalf("search %q = %+v, %v", text, hits.Data, qerr)
		}
	}

	// Another organization does not see the contact.
	if _, err = repo.GetByID(f.OrgCtx(f.OrgB), f.OrgB, created.ID); err == nil {
		t.Fatal("contact visible in another organization")
	}

	// Deleting the contact deletes its vault entry.
	var subject string
	if err = f.Admin.QueryRow(bg, `SELECT pii_subject_id::text FROM contact WHERE id = $1`, created.ID).Scan(&subject); err != nil {
		t.Fatal(err)
	}
	if w = call(http.MethodDelete, "/api/v1/contacts/"+created.ID, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", w.Code)
	}
	if plaintext(`SELECT 1 FROM pii_subject WHERE surrogate_id = $1`, subject) {
		t.Fatal("the vault entry of the deleted contact survived")
	}
}
