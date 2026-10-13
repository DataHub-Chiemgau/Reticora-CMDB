package ci_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestCreateStoresTheFieldsOfCI01 covers CI-01 (WP-120) against
// PostgreSQL: POST /cis stores every declared field — assignment,
// hardware, network, system, attributes — and reads it back; a CI a
// person creates is manual, import is a valid source, schema_version and
// merged_into_id are part of the CI.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCreateStoresTheFieldsOfCI01(t *testing.T) {
	f := scopetest.Seed(t, "8b")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	site, err := locations.NewPGRepository(f.App).Create(ctx, f.OrgA,
		locations.CreateRequest{Kind: locations.KindSite, ClientID: f.Client1, Name: "ci01-site"})
	if err != nil {
		t.Fatalf("create site: %v", err)
	}
	chassis := f.CI(t, f.OrgA, f.Client1, "ci01-chassis")
	var typeID string
	if err = f.Admin.QueryRow(bg, `SELECT ci_type_id::text FROM ci WHERE id = $1`, chassis).Scan(&typeID); err != nil {
		t.Fatal(err)
	}

	repo := ci.NewPGRepository(f.App)
	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(repo)).RegisterRoutes(mux)
	create := func(body map[string]any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/cis", bytes.NewReader(raw)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("create: %d %s", w.Code, w.Body.String())
		}
		var out map[string]any
		if uerr := json.Unmarshal(w.Body.Bytes(), &out); uerr != nil {
			t.Fatal(uerr)
		}
		return out
	}

	declared := map[string]any{
		"ci_type_id": typeID, "name": "ci01-server", "client_id": f.Client1, "location_id": site.ID,
		"parent_ci_id": chassis, "status": "maintenance",
		"manufacturer": "Dell", "model": "R750", "serial_number": "CI01-SN", "hardware_uuid": "4c4c4544-0000-1000-8000-0000000c1001",
		"management_ip": "10.1.0.5", "primary_mac": "00:11:22:33:44:c1", "hostname": "ci01-server", "fqdn": "ci01-server.example.org",
		"os_name": "Debian", "os_version": "12", "firmware_version": "2.1", "sys_object_id": "1.3.6.1.4.1.674",
		"attributes": map[string]any{"ci01_note": "rack 4"},
	}
	created := create(declared)

	item, err := repo.GetByID(ctx, f.OrgA, created["id"].(string))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	stored, _ := json.Marshal(item)
	var got map[string]any
	_ = json.Unmarshal(stored, &got)
	for key, want := range declared {
		if key == "attributes" {
			if attrs, _ := got["attributes"].(map[string]any); attrs["ci01_note"] != "rack 4" {
				t.Errorf("attributes = %v", got["attributes"])
			}
			continue
		}
		if got[key] != want {
			t.Errorf("%s = %v, want %v", key, got[key], want)
		}
	}
	if item.SiteID != site.ID || !item.IsManual || item.DiscoverySource != ci.SourceManual || item.SchemaVersion < 1 || item.Version != 1 {
		t.Errorf("derived fields: site %q manual %v source %q schema v%d version %d",
			item.SiteID, item.IsManual, item.DiscoverySource, item.SchemaVersion, item.Version)
	}

	imported := create(map[string]any{"ci_type_id": typeID, "name": "ci01-imported", "discovery_source": "import"})
	if imported["discovery_source"] != ci.SourceImport || imported["is_manual"] != true || imported["status"] != ci.StatusActive {
		t.Errorf("imported CI = %v", imported)
	}

	// merged_into_id is part of the CI; the merge (CI-12) sets it.
	if _, err = f.Admin.Exec(bg, `UPDATE ci SET merged_into_id = $1 WHERE id = $2`, item.ID, imported["id"]); err != nil {
		t.Fatalf("set merged_into_id: %v", err)
	}
	merged, err := repo.GetByID(ctx, f.OrgA, imported["id"].(string))
	if err != nil || merged.MergedIntoID != item.ID {
		t.Errorf("merged_into_id = %q (%v), want %s", merged.MergedIntoID, err, item.ID)
	}
	if _, err = f.Admin.Exec(bg, `UPDATE ci SET merged_into_id = id WHERE id = $1`, item.ID); err == nil {
		t.Error("a CI merged into itself was accepted")
	}
}
