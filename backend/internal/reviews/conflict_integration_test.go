package reviews_test

import (
	"context"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// pgOverrides adapts the override repository to discovery as the server
// does.
type pgOverrides struct{ repo *override.PGRepository }

func (p pgOverrides) RecordDiscovered(ctx context.Context, orgID, ciID, field string, value any, source string, observedAt time.Time) (*discovery.FieldProvenance, error) {
	fv, err := p.repo.RecordDiscovered(ctx, orgID, ciID, field, value, source, observedAt)
	if err != nil {
		return nil, err
	}
	return &discovery.FieldProvenance{Diverged: fv.Diverged}, nil
}

func (p pgOverrides) Decide(ctx context.Context, w *override.Write) override.Decision {
	return override.DecideAutomatedWrite(ctx, p.repo, w)
}

func (p pgOverrides) ClearOverride(ctx context.Context, orgID, ciID, field string) error {
	_, err := p.repo.ClearOverride(ctx, orgID, ciID, field)
	return err
}

// TestOverrideConflictOnPostgreSQL covers WP-089 (REC-12, REC-06, OVR-01)
// against PostgreSQL: the observed value is kept next to the override, one
// override_conflict per field, dismiss suppresses the value until it
// changes, accept removes the override and the CI takes the observed value;
// a lower-ranked serial number opens conflicting_values with the reported
// value.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestOverrideConflictOnPostgreSQL(t *testing.T) {
	f := scopetest.Seed(t, "81")
	bg := context.Background()
	cis := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
	overrideRepo := override.NewPGRepository(f.App)
	mux := chi.NewRouter()
	discovery.NewHandler(discovery.NewPGRepository(f.App), cis).WithProvenance(pgOverrides{overrideRepo}).RegisterRoutes(mux)
	env := &conflictEnv{t: t, mux: mux,
		ctx: tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})}

	device := f.CI(t, f.OrgA, "", "oc-pg")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET serial_number = 'OC-PG-1', model = 'manual-model',
		hardware_uuid = '4c4c4544-0042-3510-8051-b4c04f4a4e81', discovery_source = 'redfish' WHERE id = $1`, device); err != nil {
		t.Fatal(err)
	}
	if _, err := overrideRepo.SetOverride(f.OrgCtx(f.OrgA), f.OrgA, device, "model", "manual-model", f.User, "ticket 89", true); err != nil {
		t.Fatal(err)
	}
	report := func(model string) discovery.BulkIngestResponse {
		return env.ingest(`{"ci_type_name":"server","name":"oc-pg","serial_number":"OC-PG-1","model":"` + model + `","source":"redfish"}`)
	}
	modelOf := func() (model string, overridden bool, observed string) {
		t.Helper()
		if err := f.Admin.QueryRow(bg, `SELECT c.model, v.override_at IS NOT NULL, COALESCE(v.discovered_value #>> '{}', '')
			FROM ci c JOIN ci_field_value v ON v.ci_id = c.id AND v.field_name = 'model' WHERE c.id = $1`, device).
			Scan(&model, &overridden, &observed); err != nil {
			t.Fatal(err)
		}
		return model, overridden, observed
	}

	report("seen-1")
	report("seen-1")
	items := env.open(reviews.KindOverrideConflict)
	if len(items) != 1 {
		t.Fatalf("override_conflict items: %+v", items)
	}
	if model, overridden, observed := modelOf(); model != "manual-model" || !overridden || observed != "seen-1" {
		t.Errorf("after the report: model %q overridden %v observed %q", model, overridden, observed)
	}

	env.resolve(items[0].ID, `{"action":"dismiss"}`)
	if resp := report("seen-1"); resp.ReviewItems != 0 {
		t.Errorf("dismissed value reported again: %+v", resp)
	}
	report("seen-2")
	if items = env.open(reviews.KindOverrideConflict); len(items) != 1 || items[0].Payload["observed_value"] != "seen-2" {
		t.Fatalf("new value: %+v", items)
	}
	env.resolve(items[0].ID, `{"action":"accept"}`)
	if model, overridden, _ := modelOf(); model != "seen-2" || overridden {
		t.Errorf("after accept: model %q overridden %v", model, overridden)
	}

	// REC-06: SNMP (80) reports another serial for the Redfish CI.
	if resp := env.ingest(`{"ci_type_name":"server","name":"oc-pg","hardware_uuid":"4c4c4544-0042-3510-8051-b4c04f4a4e81",
		"serial_number":"OC-PG-SNMP","source":"snmp"}`); resp.ReviewItems != 1 {
		t.Fatalf("lower-ranked serial: %+v", resp)
	}
	conflicts := env.open(reviews.KindConflictingValues)
	if len(conflicts) != 1 {
		t.Fatalf("conflicting_values: %+v", conflicts)
	}
	incoming, _ := conflicts[0].Payload["incoming"].(map[string]any)
	if incoming["serial_number"] != "OC-PG-SNMP" {
		t.Errorf("reported serial: %+v", conflicts[0].Payload)
	}
	var serial string
	if err := f.Admin.QueryRow(bg, `SELECT serial_number FROM ci WHERE id = $1`, device).Scan(&serial); err != nil || serial != "OC-PG-1" {
		t.Errorf("stored serial: %q, %v", serial, err)
	}
}

// TestOverrideConflictSuppressionEndsWithValueChange covers WP-576 (REC-12,
// F-248) against PostgreSQL: a dismissal suppresses its value only until the
// source reports another one.
//
//   - Direct repetition: dismiss seen-1, the source reports seen-1 again: no
//     item.
//   - S1: dismiss seen-1, the source reports the override value, then
//     seen-1: a new item.
//   - S2: dismiss seen-1, seen-2 opens an item that is accepted, a new
//     override manual-2, then seen-1: a new item.
//   - While an item is open, another value opens no second one.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestOverrideConflictSuppressionEndsWithValueChange(t *testing.T) {
	f := scopetest.Seed(t, "81")
	bg := context.Background()
	cis := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
	overrideRepo := override.NewPGRepository(f.App)
	mux := chi.NewRouter()
	discovery.NewHandler(discovery.NewPGRepository(f.App), cis).WithProvenance(pgOverrides{overrideRepo}).RegisterRoutes(mux)
	env := &conflictEnv{t: t, mux: mux,
		ctx: tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})}

	device := f.CI(t, f.OrgA, "", "oc-s12")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET serial_number = 'OC-S12-1', model = 'manual-model',
		discovery_source = 'redfish' WHERE id = $1`, device); err != nil {
		t.Fatal(err)
	}
	if _, err := overrideRepo.SetOverride(f.OrgCtx(f.OrgA), f.OrgA, device, "model", "manual-model", f.User, "ticket 576", true); err != nil {
		t.Fatal(err)
	}
	report := func(model string) int {
		t.Helper()
		return env.ingest(`{"ci_type_name":"server","name":"oc-s12","serial_number":"OC-S12-1","model":"` + model + `","source":"redfish"}`).ReviewItems
	}
	openItem := func(step string) reviews.Item {
		t.Helper()
		items := env.open(reviews.KindOverrideConflict)
		if len(items) != 1 {
			t.Fatalf("%s: open override_conflict items %+v, want one", step, items)
		}
		return items[0]
	}
	observed := func() string {
		t.Helper()
		var v string
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(discovered_value #>> '{}', '') FROM ci_field_value
			WHERE ci_id = $1 AND field_name = 'model'`, device).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	if n := report("seen-1"); n != 1 {
		t.Fatalf("first report: %d items", n)
	}
	item := openItem("first report")
	if item.Payload["observed_value"] != "seen-1" || item.Payload["source"] != "redfish" || item.Payload["rank"] != float64(90) {
		t.Errorf("payload: %+v", item.Payload)
	}
	env.resolve(item.ID, `{"action":"dismiss"}`)

	// Direct repetition: the dismissed value stays suppressed.
	for i := range 2 {
		if n := report("seen-1"); n != 0 || len(env.open(reviews.KindOverrideConflict)) != 0 {
			t.Fatalf("repetition %d of the dismissed value opened %d items", i+1, n)
		}
	}

	// S1: the source reports the override value in between.
	if n := report("manual-model"); n != 0 || len(env.open(reviews.KindOverrideConflict)) != 0 {
		t.Fatalf("override value reported: %d items", n)
	}
	if got := observed(); got != "manual-model" {
		t.Errorf("observed value after the override value was reported: %q", got)
	}
	if n := report("seen-1"); n != 1 {
		t.Fatalf("S1: seen-1 after the override value: %d items, want a new one", n)
	}
	item = openItem("S1")
	if item.Payload["observed_value"] != "seen-1" {
		t.Errorf("S1 payload: %+v", item.Payload)
	}
	env.resolve(item.ID, `{"action":"dismiss"}`)
	if n := report("seen-1"); n != 0 {
		t.Fatalf("S1: repetition after the second dismissal: %d items", n)
	}

	// S2: seen-2 is accepted, a new override follows, then seen-1.
	if n := report("seen-2"); n != 1 {
		t.Fatalf("S2: seen-2: %d items", n)
	}
	env.resolve(openItem("S2 seen-2").ID, `{"action":"accept"}`)
	if _, err := overrideRepo.SetOverride(f.OrgCtx(f.OrgA), f.OrgA, device, "model", "manual-2", f.User, "ticket 576-2", true); err != nil {
		t.Fatal(err)
	}
	if n := report("seen-1"); n != 1 {
		t.Fatalf("S2: seen-1 under the new override: %d items, want a new one", n)
	}
	item = openItem("S2")
	if item.Payload["observed_value"] != "seen-1" {
		t.Errorf("S2 payload: %+v", item.Payload)
	}

	// An open item takes no second one.
	if n := report("seen-3"); n != 0 {
		t.Errorf("another value while an item is open: %d items", n)
	}
	openItem("open item")
	var total int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM review_item WHERE organization_id = $1 AND kind = 'override_conflict'
		AND object_key = $2`, f.OrgA, "ci:"+device+"/model").Scan(&total); err != nil || total != 4 {
		t.Errorf("override_conflict items of the field: %d, %v; want 4", total, err)
	}
}
