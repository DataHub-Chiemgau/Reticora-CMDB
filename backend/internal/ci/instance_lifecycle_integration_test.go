package ci_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// lifecycleEnv is the PostgreSQL setup of the WP-714 tests: the CI and
// override routes, an ingest, and readers of the stored rows.
type lifecycleEnv struct {
	t       *testing.T
	f       *scopetest.Fixture
	manager context.Context
	repo    *ci.PGRepository
	types   *citype.PGRepository
	mux     *chi.Mux
}

func newLifecycleEnv(t *testing.T, tag string) *lifecycleEnv {
	t.Helper()
	f := scopetest.Seed(t, tag)
	base := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	manager := identity.WithPrincipal(base, identity.Principal{Subject: f.User, OrganizationID: f.OrgA,
		Type: identity.PrincipalTypeUser, Permissions: []identity.Permission{identity.PermCIRead, identity.PermCIWrite,
			identity.PermCIInstanceAttributeManage, identity.PermOverrideWrite}})
	repo := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox())
	types := citype.NewPGRepository(f.App)
	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(repo).WithFieldResolver(types)).RegisterRoutes(mux)
	override.NewHandler(override.NewPGRepository(f.App)).RegisterRoutes(mux)
	return &lifecycleEnv{t: t, f: f, manager: manager, repo: repo, types: types, mux: mux}
}

func (e *lifecycleEnv) call(method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(e.manager)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	return w
}

func (e *lifecycleEnv) must(step string, w *httptest.ResponseRecorder, code int) {
	e.t.Helper()
	if w.Code != code {
		e.t.Fatalf("%s: %d %s, want %d", step, w.Code, w.Body.String(), code)
	}
}

func (e *lifecycleEnv) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.f.Admin.Exec(context.Background(), q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

func (e *lifecycleEnv) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
	return n
}

// stored returns ci.attributes as stored.
func (e *lifecycleEnv) stored(id string) map[string]any {
	e.t.Helper()
	var raw []byte
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT attributes FROM ci WHERE id = $1`, id).Scan(&raw); err != nil {
		e.t.Fatal(err)
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		e.t.Fatal(err)
	}
	return out
}

func (e *lifecycleEnv) namespace(id, ns string) map[string]any {
	e.t.Helper()
	m, _ := e.stored(id)[ns].(map[string]any)
	return m
}

func (e *lifecycleEnv) define(id string, names ...string) {
	e.t.Helper()
	for i, name := range names {
		e.exec(`INSERT INTO ci_instance_field_definition (organization_id, ci_id, name, data_type, sort_order)
			VALUES ($1, $2, $3, 'text', $4)`, e.f.OrgA, id, name, i)
	}
}

// overrideRows counts the rows of ci_field_value with an override.
func (e *lifecycleEnv) overrideRows(id, field string) int {
	e.t.Helper()
	return e.count(`SELECT count(*) FROM ci_field_value WHERE ci_id = $1 AND field_name = $2
		AND (override_at IS NOT NULL OR override_value IS NOT NULL OR protected)`, id, field)
}

// TestInstanceValuesCreateNoOverrides covers WP-714 (MET-14, CI-04): a
// manual change of an instance attribute, by PATCH or by PUT
// /cis/{id}/instance-attributes, records no override; a business attribute
// changed in the same PATCH still does. Migration 000204 removes the
// overrides of instance attributes written before: the override columns are
// empty, a row without observed value is gone, the provenance of an
// observed value stays, _overrides no longer names them and _instance is
// unchanged.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestInstanceValuesCreateNoOverrides(t *testing.T) {
	e := newLifecycleEnv(t, "e4")
	device := e.f.CI(t, e.f.OrgA, e.f.Client1, "il-patch")
	e.define(device, "contract", "slot")

	e.must("PATCH", e.call(http.MethodPatch, "/api/v1/cis/"+device,
		`{"attributes":{"contract":"C-1","owner":"ops"},"change_reason":"wp-714"}`), http.StatusOK)
	if got := e.namespace(device, "_instance"); got["contract"] != "C-1" {
		t.Errorf("_instance after PATCH: %v", got)
	}
	if n := e.overrideRows(device, "contract"); n != 0 {
		t.Errorf("override rows of the instance key after PATCH: %d, want 0", n)
	}
	if n := e.count(`SELECT count(*) FROM ci_field_value WHERE ci_id = $1 AND field_name = 'contract'`, device); n != 0 {
		t.Errorf("ci_field_value rows of the instance key after PATCH: %d, want 0", n)
	}
	overrides := e.namespace(device, "_overrides")
	if _, ok := overrides["contract"]; ok || overrides["owner"] == nil {
		t.Errorf("_overrides after PATCH: %v, want owner and not contract", overrides)
	}

	e.must("PUT instance-attributes", e.call(http.MethodPut, "/api/v1/cis/"+device+"/instance-attributes/slot",
		`{"value":"S-4"}`), http.StatusOK)
	if n := e.overrideRows(device, "slot"); n != 0 {
		t.Errorf("override rows of the instance key after PUT: %d, want 0", n)
	}
	if _, ok := e.namespace(device, "_overrides")["slot"]; ok {
		t.Errorf("_overrides after PUT names slot: %v", e.namespace(device, "_overrides"))
	}

	// Overrides of instance attributes from before WP-714: one without and
	// one with an observed value.
	e.exec(`INSERT INTO ci_field_value (organization_id, ci_id, field_name, override_value, override_author, override_reason, override_at, protected)
		VALUES ($1, $2, 'contract', '"C-1"', $3, 'legacy', now(), true)`, e.f.OrgA, device, e.f.User)
	e.exec(`INSERT INTO ci_field_value (organization_id, ci_id, field_name, discovered_value, discovered_source, discovered_at, discovered_rank,
			override_value, override_author, override_reason, override_at, protected)
		VALUES ($1, $2, 'slot', '"S-9"', 'snmp', now(), 80, '"S-4"', $3, 'legacy', now(), true)`, e.f.OrgA, device, e.f.User)
	overrides = e.namespace(device, "_overrides")
	if overrides["contract"] == nil || overrides["slot"] == nil {
		t.Fatalf("legacy overrides not projected: %v", overrides)
	}
	instanceBefore := e.namespace(device, "_instance")

	up, err := os.ReadFile("../../migrations/000204_instance_attribute_overrides.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	e.exec(string(up))

	if n := e.count(`SELECT count(*) FROM ci_field_value WHERE ci_id = $1 AND field_name = 'contract'`, device); n != 0 {
		t.Errorf("legacy override row without observed value after the migration: %d rows, want 0", n)
	}
	var discovered, source string
	var overridden, protected bool
	if err = e.f.Admin.QueryRow(context.Background(), `SELECT discovered_value::text, discovered_source,
		override_at IS NOT NULL OR override_value IS NOT NULL OR override_author IS NOT NULL OR override_reason IS NOT NULL, protected
		FROM ci_field_value WHERE ci_id = $1 AND field_name = 'slot'`, device).Scan(&discovered, &source, &overridden, &protected); err != nil {
		t.Fatalf("provenance of slot after the migration: %v", err)
	}
	if discovered != `"S-9"` || source != "snmp" || overridden || protected {
		t.Errorf("slot after the migration: discovered %s from %s, override %v, protected %v; want the provenance without override",
			discovered, source, overridden, protected)
	}
	overrides = e.namespace(device, "_overrides")
	if _, ok := overrides["contract"]; ok {
		t.Errorf("_overrides names contract after the migration: %v", overrides)
	}
	if _, ok := overrides["slot"]; ok {
		t.Errorf("_overrides names slot after the migration: %v", overrides)
	}
	if overrides["owner"] == nil {
		t.Errorf("the override of the business attribute owner is gone: %v", overrides)
	}
	if got := e.namespace(device, "_instance"); !reflect.DeepEqual(got, instanceBefore) {
		t.Errorf("_instance after the migration: %v, want unchanged %v", got, instanceBefore)
	}
	if e.namespace(device, "_provenance")["slot"] == nil {
		t.Errorf("_provenance lost slot: %v", e.stored(device))
	}
}

// TestInstanceValuesIngestAndOverrideClear covers WP-714 (MET-14, OVR-02,
// REC-12): an ingest that reports a differing value for an instance
// attribute opens no override_conflict review, records the provenance of the
// value and leaves _instance unchanged, through the central decision alone
// (the ingest here has no instance definitions, so stripInstanceFields does
// not act). DELETE /cis/{id}/fields/{name}/override of an instance attribute
// with a legacy override does not bring the observed value into _instance.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestInstanceValuesIngestAndOverrideClear(t *testing.T) {
	e := newLifecycleEnv(t, "e5")
	device := e.f.CI(t, e.f.OrgA, e.f.Client1, "il-ingest")
	e.exec(`UPDATE ci SET serial_number = 'IL-SN-714', attributes = '{"_instance":{"contract":"C-1"}}' WHERE id = $1`, device)
	e.define(device, "contract")

	ingest := discovery.NewHandler(discovery.NewPGRepository(e.f.App), e.repo).WithProvenance(provenance{repo: override.NewPGRepository(e.f.App)})
	imux := chi.NewRouter()
	ingest.RegisterRoutes(imux)
	for range 2 {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/ingest/bulk", bytes.NewBufferString(`{"collector_id":"col-1","items":[{"ci_type_name":"server",
			"name":"il-ingest","serial_number":"IL-SN-714","attributes":{"contract":"DISCOVERED","firmware":"2.0"}}]}`)).WithContext(e.manager)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		imux.ServeHTTP(w, r)
		e.must("ingest", w, http.StatusAccepted)
		var resp discovery.BulkIngestResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Updated != 1 || resp.ReviewItems != 0 {
			t.Fatalf("ingest response %s (%v), want one update and no review", w.Body.String(), err)
		}
	}
	if got := e.namespace(device, "_instance"); !reflect.DeepEqual(got, map[string]any{"contract": "C-1"}) {
		t.Errorf("_instance after the ingest: %v, want unchanged", got)
	}
	attrs := e.stored(device)
	if _, ok := attrs["contract"]; ok || attrs["firmware"] != "2.0" {
		t.Errorf("attributes after the ingest: %v, want firmware and no top-level contract", attrs)
	}
	if n := e.count(`SELECT count(*) FROM review_item WHERE organization_id = $1 AND kind = 'override_conflict'
		AND payload->>'ci_id' = $2`, e.f.OrgA, device); n != 0 {
		t.Errorf("override_conflict reviews for the instance attribute: %d, want 0", n)
	}
	if n := e.count(`SELECT count(*) FROM ci_field_value WHERE ci_id = $1 AND field_name = 'contract'
		AND discovered_value = '"DISCOVERED"' AND discovered_source IS NOT NULL AND override_at IS NULL`, device); n != 1 {
		t.Errorf("provenance of the reported instance value: %d rows, want 1 without override", n)
	}

	// A legacy override of the instance attribute (before migration 000204):
	// clearing it keeps the manual value in _instance.
	e.exec(`UPDATE ci_field_value SET override_value = '"C-1"', override_reason = 'legacy', override_at = now(), protected = true
		WHERE ci_id = $1 AND field_name = 'contract'`, device)
	e.must("DELETE override", e.call(http.MethodDelete, "/api/v1/cis/"+device+"/fields/contract/override", ""), http.StatusOK)
	if got := e.namespace(device, "_instance"); !reflect.DeepEqual(got, map[string]any{"contract": "C-1"}) {
		t.Errorf("_instance after clearing the override: %v, want the manual value C-1", got)
	}
	if n := e.overrideRows(device, "contract"); n != 0 {
		t.Errorf("override rows after the clear: %d", n)
	}
	item, err := e.repo.GetByID(e.manager, e.f.OrgA, device)
	if err != nil || item.Attributes["contract"] != "C-1" {
		t.Errorf("read view after the clear: %v (%v), want contract C-1", item, err)
	}
	// The same through the repository the review accept calls.
	e.exec(`UPDATE ci_field_value SET override_value = '"C-1"', override_reason = 'legacy', override_at = now(), protected = true
		WHERE ci_id = $1 AND field_name = 'contract'`, device)
	if _, err = override.NewPGRepository(e.f.App).ClearOverride(e.manager, e.f.OrgA, device, "contract"); err != nil {
		t.Fatalf("ClearOverride: %v", err)
	}
	if got := e.namespace(device, "_instance"); !reflect.DeepEqual(got, map[string]any{"contract": "C-1"}) {
		t.Errorf("_instance after ClearOverride: %v, want the manual value C-1", got)
	}
}

// TestInstanceValuesMergeAndUnmerge covers WP-714 (MET-14, CI-12): the
// merge keeps the survivor's _instance, puts no instance value of either
// CI on the top level of the survivor, takes no override of the victim onto
// an instance attribute of the survivor and opens no conflict for it; the
// unmerge restores the victim's instance values from the snapshot.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestInstanceValuesMergeAndUnmerge(t *testing.T) {
	e := newLifecycleEnv(t, "e6")
	survivor := e.f.CI(t, e.f.OrgA, e.f.Client1, "il-survivor")
	victim := e.f.CI(t, e.f.OrgA, e.f.Client1, "il-victim")
	e.exec(`UPDATE ci SET attributes = '{"owner":"ops","_instance":{"contract":"S-1","label":"SL"}}' WHERE id = $1`, survivor)
	e.exec(`UPDATE ci SET attributes = '{"rack":"R1","label":"VL","_instance":{"contract":"V-1","slot":"V-3"}}' WHERE id = $1`, victim)
	e.define(survivor, "contract", "label")
	e.define(victim, "contract", "slot")
	// The victim's business attribute label carries an override; label is
	// an instance attribute of the survivor.
	e.exec(`INSERT INTO ci_field_value (organization_id, ci_id, field_name, override_value, override_reason, override_at, protected)
		VALUES ($1, $2, 'label', '"VL"', 'victim', now(), true)`, e.f.OrgA, victim)
	survivorBefore, victimBefore := e.stored(survivor), e.stored(victim)

	res, err := e.repo.Merge(e.manager, e.f.OrgA, survivor, victim, "")
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if len(res.OverrideConflicts) != 0 {
		t.Errorf("merge conflicts: %v, want none", res.OverrideConflicts)
	}
	attrs := e.stored(survivor)
	if !reflect.DeepEqual(attrs["_instance"], survivorBefore["_instance"]) {
		t.Errorf("survivor _instance after the merge: %v, want %v", attrs["_instance"], survivorBefore["_instance"])
	}
	for _, key := range []string{"contract", "slot", "label"} {
		if v, ok := attrs[key]; ok {
			t.Errorf("instance key %s on the top level of the survivor: %v", key, v)
		}
	}
	if attrs["owner"] != "ops" || attrs["rack"] != "R1" {
		t.Errorf("business attributes after the merge: %v, want owner of the survivor and rack of the victim", attrs)
	}
	if n := e.overrideRows(survivor, "label"); n != 0 {
		t.Errorf("override of the survivor's instance attribute label after the merge: %d rows", n)
	}
	if _, ok := e.namespace(survivor, "_overrides")["label"]; ok {
		t.Errorf("_overrides of the survivor names label: %v", e.namespace(survivor, "_overrides"))
	}
	item, err := e.repo.GetByID(e.manager, e.f.OrgA, survivor)
	if err != nil || item.Attributes["contract"] != "S-1" || item.Attributes["label"] != "SL" || item.Attributes["slot"] != nil {
		t.Errorf("read view of the survivor after the merge: %v (%v)", item, err)
	}

	if _, err = e.repo.Unmerge(e.manager, e.f.OrgA, victim, false); err != nil {
		t.Fatalf("unmerge: %v", err)
	}
	for id, before := range map[string]map[string]any{survivor: survivorBefore, victim: victimBefore} {
		after := e.stored(id)
		if !reflect.DeepEqual(after["_instance"], before["_instance"]) {
			t.Errorf("_instance of %s after the unmerge: %v, want %v", id, after["_instance"], before["_instance"])
		}
	}
	if attrs = e.stored(victim); attrs["label"] != "VL" || attrs["rack"] != "R1" {
		t.Errorf("victim after the unmerge: %v", attrs)
	}
	if n := e.overrideRows(victim, "label"); n != 1 {
		t.Errorf("the victim's override of label after the unmerge: %d rows, want 1", n)
	}
}

// TestInstanceDefinitionMovesValue covers WP-714 (MET-14, CI-10): creating
// an instance definition moves a top-level value of the same name into
// _instance and removes its override in the same transaction; deleting the
// definition moves it back, so no stale _instance value hides a later one.
// Both keep the audit ci_instance_field.*.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestInstanceDefinitionMovesValue(t *testing.T) {
	e := newLifecycleEnv(t, "e8")
	device := e.f.CI(t, e.f.OrgA, e.f.Client1, "il-define")
	e.exec(`UPDATE ci SET attributes = '{"asset_note":"N-1","owner":"ops"}' WHERE id = $1`, device)
	e.exec(`INSERT INTO ci_field_value (organization_id, ci_id, field_name, override_value, override_reason, override_at, protected)
		VALUES ($1, $2, 'asset_note', '"N-1"', 'manual', now(), true)`, e.f.OrgA, device)

	if _, err := e.types.UpsertInstanceField(e.manager, e.f.OrgA, device,
		citype.UpsertInstanceFieldRequest{Name: "asset_note", DataType: "text"}); err != nil {
		t.Fatalf("upsert definition: %v", err)
	}
	attrs := e.stored(device)
	if _, ok := attrs["asset_note"]; ok || e.namespace(device, "_instance")["asset_note"] != "N-1" || attrs["owner"] != "ops" {
		t.Errorf("attributes after the definition: %v, want asset_note under _instance only", attrs)
	}
	if n := e.count(`SELECT count(*) FROM ci_field_value WHERE ci_id = $1 AND field_name = 'asset_note'`, device); n != 0 {
		t.Errorf("override row of the new instance attribute: %d, want 0", n)
	}
	item, err := e.repo.GetByID(e.manager, e.f.OrgA, device)
	if err != nil || item.Attributes["asset_note"] != "N-1" {
		t.Errorf("read view after the definition: %v (%v)", item, err)
	}
	// An update of the definition moves nothing.
	if _, err = e.types.UpsertInstanceField(e.manager, e.f.OrgA, device,
		citype.UpsertInstanceFieldRequest{Name: "asset_note", DataType: "text", Label: "Note"}); err != nil {
		t.Fatalf("update definition: %v", err)
	}
	if e.namespace(device, "_instance")["asset_note"] != "N-1" {
		t.Errorf("_instance after the update: %v", e.stored(device))
	}

	if err = e.types.DeleteInstanceField(e.manager, e.f.OrgA, device, "asset_note"); err != nil {
		t.Fatalf("delete definition: %v", err)
	}
	attrs = e.stored(device)
	if attrs["asset_note"] != "N-1" {
		t.Errorf("attributes after deleting the definition: %v, want asset_note on the top level", attrs)
	}
	if _, ok := attrs["_instance"]; ok {
		t.Errorf("an emptied _instance stays: %v", attrs)
	}
	// A later PATCH of the ordinary attribute is what the read view shows.
	e.must("PATCH", e.call(http.MethodPatch, "/api/v1/cis/"+device, `{"attributes":{"asset_note":"N-2"}}`), http.StatusOK)
	if item, err = e.repo.GetByID(e.manager, e.f.OrgA, device); err != nil || item.Attributes["asset_note"] != "N-2" {
		t.Errorf("read view after the PATCH: %v (%v), want N-2", item, err)
	}
	for action, want := range map[string]int{"ci_instance_field.created": 1, "ci_instance_field.updated": 1, "ci_instance_field.deleted": 1} {
		if n := e.count(`SELECT count(*) FROM audit_log WHERE resource_id = $1 AND action = $2`, device, action); n != want {
			t.Errorf("audit %s: %d, want %d", action, n, want)
		}
	}
}
