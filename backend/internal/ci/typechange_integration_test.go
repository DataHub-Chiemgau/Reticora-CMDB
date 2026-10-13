package ci_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestTypeChangeAgainstPostgres covers CI-11 (WP-172) against PostgreSQL:
// discovery raises generic_device to a specific type and is refused a
// specific-to-specific change; the manual change through POST /cis/{id}/type
// keeps the CI row and its asset link, records ci_change type_change and
// the ci.type_changed audit entry and reports missing required attributes
// of the target type as findings.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestTypeChangeAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "b2")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	repo := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
	typeID := func(key string) string {
		var id string
		if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = $1 AND organization_id IS NULL`, key).Scan(&id); err != nil {
			t.Fatalf("type %s: %v", key, err)
		}
		return id
	}
	generic, switchType, router := typeID("generic_device"), typeID("switch"), typeID("router")

	id := f.CI(t, f.OrgA, f.Client1, "typechange-1")
	asset := f.Asset(t, f.OrgA, f.Client1, "TC-1")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET ci_type_id = $2, asset_id = $3 WHERE id = $1`, id, generic, asset); err != nil {
		t.Fatal(err)
	}

	res, err := repo.ChangeType(ctx, f.OrgA, id, switchType, ci.TypeChangeDiscovery)
	if err != nil || !res.Changed || res.CI.ID != id || res.CI.CITypeID != switchType || res.CI.AssetID != asset {
		t.Fatalf("discovery upgrade: %+v %v", res, err)
	}
	if _, err = repo.ChangeType(ctx, f.OrgA, id, router, ci.TypeChangeDiscovery); !errors.Is(err, ci.ErrReclassificationRefused) {
		t.Errorf("discovery switch -> router: %v", err)
	}

	// The organization's own type requires an attribute the CI lacks.
	own := f.ID()
	if _, err = f.Admin.Exec(bg, `INSERT INTO ci_type (id, organization_id, key, name) VALUES ($1, $2, 'tc-appliance', 'TC Appliance')`, own, f.OrgA); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Admin.Exec(bg, `INSERT INTO attribute_definition (ci_type_id, organization_id, key, data_type, required)
		VALUES ($1, $2, 'tc_rack_units', 'string', true)`, own, f.OrgA); err != nil {
		t.Fatal(err)
	}
	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(repo)).RegisterRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/cis/"+id+"/type",
		bytes.NewBufferString(`{"ci_type_id":"`+own+`"}`)).WithContext(ctx))
	var out ci.TypeChangeResult
	if err = json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusOK {
		t.Fatalf("manual change: %d %s", w.Code, w.Body.String())
	}
	if !out.Changed || out.CI.CITypeID != own || len(out.Findings) != 1 || out.Findings[0].Field != "tc_rack_units" {
		t.Errorf("manual change result: %+v", out)
	}

	var changes, audits int
	if err = f.Admin.QueryRow(bg, `SELECT
		(SELECT count(*) FROM ci_change WHERE ci_id = $1 AND change_type = 'type_change' AND field_name = 'ci_type_id'),
		(SELECT count(*) FROM audit_log WHERE organization_id = $2 AND action = 'ci.type_changed' AND resource_id = $1)`,
		id, f.OrgA).Scan(&changes, &audits); err != nil {
		t.Fatal(err)
	}
	if changes != 2 || audits != 2 {
		t.Errorf("change rows %d, audit entries %d, want 2 each", changes, audits)
	}
}
