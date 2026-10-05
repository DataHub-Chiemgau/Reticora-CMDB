package ci_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// TestOptimisticConcurrency covers WP-061 (API-07, API-03): GET returns the
// version as ETag; only writes of rank >= 92 raise it; a PATCH with an older
// If-Match is refused with 409 only when one of its fields changed since;
// an unknown version is 412; an organization can require If-Match (412
// without it); of two concurrent PATCHes of the same field one wins.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestOptimisticConcurrency(t *testing.T) {
	f := scopetest.Seed(t, "5e")
	bg := context.Background()
	ciID := f.CI(t, f.OrgA, f.Client1, "occ-ci")
	user := f.AppUser(t, f.OrgA, "occ-editor")
	scope := database.OrgWideScope(f.OrgA, user)
	ctx := tenant.WithTenant(database.ContextWithTenantScope(bg, &scope), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
	repo := ci.NewPGRepository(f.App)
	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(repo)).RegisterRoutes(mux)

	call := func(method, ifMatch, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/api/v1/cis/"+ciID, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if ifMatch != "" {
			req.Header.Set("If-Match", ifMatch)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(ctx))
		return w
	}
	expect := func(w *httptest.ResponseRecorder, status int, etag string) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("status %d, want %d: %s", w.Code, status, w.Body.String())
		}
		if etag != "" && w.Header().Get("ETag") != etag {
			t.Errorf("ETag %q, want %q", w.Header().Get("ETag"), etag)
		}
	}

	expect(call(http.MethodGet, "", ""), http.StatusOK, `"1"`)
	expect(call(http.MethodPatch, `"1"`, `{"name":"occ-a"}`), http.StatusOK, `"2"`)

	// An observed update (discovery) leaves the version.
	src := ci.SourceSNMP
	if _, err := repo.Update(ctx, f.OrgA, ciID, ci.UpdateRequest{Model: strPtr("observed"), DiscoverySource: &src}); err != nil {
		t.Fatal(err)
	}
	expect(call(http.MethodGet, "", ""), http.StatusOK, `"2"`)
	// So does a write of a lower rank; a workflow write (92) raises it.
	if _, err := repo.Update(ctx, f.OrgA, ciID, ci.UpdateRequest{OSName: strPtr("linux"), Authoritative: true}); err != nil {
		t.Fatal(err)
	}
	expect(call(http.MethodGet, "", ""), http.StatusOK, `"3"`)

	// Field-granular: an old If-Match passes when its fields did not change
	// since, and is refused with the changed fields otherwise.
	expect(call(http.MethodPatch, `"1"`, `{"manufacturer":"Acme"}`), http.StatusOK, `"4"`)
	w := call(http.MethodPatch, `W/"1"`, `{"name":"occ-b","os_name":"bsd"}`)
	expect(w, http.StatusConflict, `"4"`)
	if !strings.Contains(w.Body.String(), `"fields":["name","os_name"]`) || !strings.Contains(w.Body.String(), `"current_version":4`) {
		t.Errorf("409 body: %s", w.Body.String())
	}
	// Unknown or malformed versions are 412.
	for _, tag := range []string{`"99"`, `"abc"`, `"0"`} {
		if w = call(http.MethodPatch, tag, `{"name":"occ-c"}`); w.Code != http.StatusPreconditionFailed ||
			!strings.Contains(w.Body.String(), "precondition-failed") {
			t.Errorf("If-Match %s: %d %s, want 412", tag, w.Code, w.Body.String())
		}
	}

	// Without the organization setting If-Match is optional; with it, it is
	// required ("*" satisfies it).
	expect(call(http.MethodPatch, "", `{"model":"m1"}`), http.StatusOK, `"5"`)
	if _, err := f.Admin.Exec(bg, `UPDATE organization SET settings = COALESCE(settings, '{}'::jsonb) || '{"require_if_match": true}' WHERE id = $1`, f.OrgA); err != nil {
		t.Fatal(err)
	}
	expect(call(http.MethodPatch, "", `{"model":"m2"}`), http.StatusPreconditionFailed, "")
	expect(call(http.MethodPatch, "*", `{"model":"m2"}`), http.StatusOK, `"6"`)

	// Two editors with the same version change the same field: one wins.
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = call(http.MethodPatch, `"6"`, `{"name":"race"}`).Code
		}()
	}
	wg.Wait()
	slices.Sort(codes)
	if codes[0] != http.StatusOK || codes[1] != http.StatusConflict {
		t.Errorf("concurrent PATCHes: %v, want one 200 and one 409", codes)
	}
}

func strPtr(s string) *string { return &s }
