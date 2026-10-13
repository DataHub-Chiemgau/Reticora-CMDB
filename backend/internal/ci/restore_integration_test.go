package ci_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestRestoreAgainstPostgres covers POST /cis/{id}/restore (CI-05, API-05,
// WP-169) against PostgreSQL: a deleted CI comes back with a new version,
// a change row and the ci.restored audit entry; after the 90-day retention
// the answer is 410.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRestoreAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "af")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()))).RegisterRoutes(mux)
	call := func(method, path string) (int, map[string]any) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewReader(nil)).WithContext(ctx))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	id := f.CI(t, f.OrgA, f.Client1, "restore-1")
	if code, _ := call(http.MethodDelete, "/api/v1/cis/"+id); code >= 300 {
		t.Fatalf("delete: %d", code)
	}
	code, out := call(http.MethodPost, "/api/v1/cis/"+id+"/restore")
	if code != http.StatusOK || out["id"] != id {
		t.Fatalf("restore: %d %v", code, out)
	}
	var deleted bool
	var audits, changes int
	if err := f.Admin.QueryRow(bg, `SELECT deleted_at IS NOT NULL,
		(SELECT count(*) FROM audit_log WHERE organization_id = $2 AND action = 'ci.restored' AND resource_id = $1),
		(SELECT count(*) FROM ci_change WHERE ci_id = $1 AND field_name = 'deleted_at')
		FROM ci WHERE id = $1`, id, f.OrgA).Scan(&deleted, &audits, &changes); err != nil {
		t.Fatal(err)
	}
	if deleted || audits != 1 || changes != 1 {
		t.Errorf("deleted %v, audit entries %d, change rows %d", deleted, audits, changes)
	}
	if code, _ = call(http.MethodPost, "/api/v1/cis/"+id+"/restore"); code != http.StatusNotFound {
		t.Errorf("restore of a live CI: %d", code)
	}
	call(http.MethodDelete, "/api/v1/cis/"+id)
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET deleted_at = now() - interval '91 days' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if code, _ = call(http.MethodPost, "/api/v1/cis/"+id+"/restore"); code != http.StatusGone {
		t.Errorf("restore after the retention: %d", code)
	}
}
