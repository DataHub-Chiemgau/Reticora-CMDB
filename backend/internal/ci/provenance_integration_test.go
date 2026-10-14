package ci_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestProvenanceAgainstPostgres covers REC-10 (WP-198) against PostgreSQL:
// GET /cis/{id}/provenance answers every typed field of the CI with its
// source, time, rank and the reason the value is shown: an observed value
// by its source rank, a manual override with author and reason over the
// observed value it covers, and the other fields from the CI record.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestProvenanceAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "be")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	repo := ci.NewPGRepository(f.App)
	fields := override.NewPGRepository(f.App)

	id := f.CI(t, f.OrgA, f.Client1, "prov-1")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET manufacturer = 'Cisco', model = 'C9300', firmware_version = '17.9',
		discovery_source = 'snmp', is_manual = false WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	observed := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	if _, err := fields.RecordDiscovered(ctx, f.OrgA, id, "firmware_version", "17.9", "snmp", observed); err != nil {
		t.Fatal(err)
	}
	if _, err := fields.RecordDiscovered(ctx, f.OrgA, id, "model", "C9300-48P", "redfish", observed); err != nil {
		t.Fatal(err)
	}
	if _, err := fields.SetOverride(ctx, f.OrgA, id, "model", "C9300-48P-E", f.User, "Typenschild abgelesen", true); err != nil {
		t.Fatal(err)
	}

	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(repo)).WithProvenance(fields).RegisterRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/cis/"+id+"/provenance", http.NoBody).WithContext(ctx))
	if w.Code != http.StatusOK {
		t.Fatalf("provenance: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Data []ci.FieldProvenance `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	byField := map[string]ci.FieldProvenance{}
	for _, p := range body.Data {
		byField[p.Field] = p
	}

	// Observed: source, rank and the observation time.
	fw := byField["firmware_version"]
	if fw.Reason != ci.ReasonSourceRank || fw.Source != "snmp" || fw.Rank != override.SourceRank("snmp") ||
		fw.Value != "17.9" || fw.ObservedAt == nil || !fw.ObservedAt.Equal(observed) || fw.RecordedAt == nil {
		t.Errorf("firmware_version = %+v", fw)
	}
	// Override: wins with author and reason, the covered observation stays visible.
	model := byField["model"]
	if model.Reason != ci.ReasonOverride || model.Value != "C9300-48P-E" || model.OverrideReason != "Typenschild abgelesen" ||
		model.OverrideAuthor != f.User || !model.Protected || model.DiscoveredValue != "C9300-48P" ||
		model.DiscoveredSource != "redfish" || model.RecordedAt == nil || model.Rank != 100 {
		t.Errorf("model = %+v", model)
	}
	// Typed fields without a provenance row come from the CI record.
	for _, field := range []string{"name", "status", "manufacturer"} {
		p, ok := byField[field]
		if !ok || p.Reason != ci.ReasonRecord || p.Source != "snmp" || p.RecordedAt == nil {
			t.Errorf("%s = %+v (present %v)", field, p, ok)
		}
	}
	if byField["manufacturer"].Value != "Cisco" {
		t.Errorf("manufacturer value = %v", byField["manufacturer"].Value)
	}
	// Empty typed fields are not listed.
	if _, ok := byField["fqdn"]; ok {
		t.Error("fqdn without a value is listed")
	}

	// A CI of another organization is not found.
	other := f.CI(t, f.OrgB, "", "prov-other")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/cis/"+other+"/provenance", http.NoBody).WithContext(ctx))
	if w.Code != http.StatusNotFound {
		t.Errorf("foreign CI: %d", w.Code)
	}
}
