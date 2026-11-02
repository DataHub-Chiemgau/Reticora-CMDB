package ci_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// provenance adapts the override repository to discovery as the server does.
type provenance struct{ repo *override.PGRepository }

func (p provenance) RecordDiscovered(ctx context.Context, orgID, ciID, fieldName string, value any, source string, observedAt time.Time) (*discovery.FieldProvenance, error) {
	fv, err := p.repo.RecordDiscovered(ctx, orgID, ciID, fieldName, value, source, observedAt)
	if err != nil {
		return nil, err
	}
	return &discovery.FieldProvenance{Diverged: fv.Diverged}, nil
}

func (p provenance) Decide(ctx context.Context, w *override.Write) override.Decision {
	return override.DecideAutomatedWrite(ctx, p.repo, w)
}

// TestManualPatchCreatesOverridesDiscoveryKeeps covers WP-057 (CI-04,
// OVR-01): PATCH /cis/{id} applies RFC 7396 to the attributes, every
// manually changed field gets a protected override with author, time and
// reason in the same transaction, and a later ingest overwrites none of
// them while it still updates fields nobody changed by hand.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestManualPatchCreatesOverridesDiscoveryKeeps(t *testing.T) {
	f := scopetest.Seed(t, "5c")
	bg := context.Background()
	ciID := f.CI(t, f.OrgA, f.Client1, "mo-ci")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET serial_number = 'MO-SN-1', model = 'old-model',
		attributes = '{"net":{"vlan":10,"mtu":1500},"rack":"R1"}' WHERE id = $1`, ciID); err != nil {
		t.Fatal(err)
	}
	user := f.AppUser(t, f.OrgA, "mo-editor")
	scope := database.OrgWideScope(f.OrgA, user)
	ctx := tenant.WithTenant(database.ContextWithTenantScope(bg, &scope), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
	repo := ci.NewPGRepository(f.App)

	serve := func(register func(chi.Router), method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		mux := chi.NewRouter()
		register(mux)
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(ctx))
		return w
	}
	w := serve(ci.NewHandler(ci.NewService(repo)).RegisterRoutes, http.MethodPatch, "/api/v1/cis/"+ciID,
		`{"name":"mo-manual","attributes":{"net":{"mtu":null},"rack":null,"owner":"ops"},"change_reason":"ticket 42"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH: %d %s", w.Code, w.Body.String())
	}
	item, err := repo.GetByID(ctx, f.OrgA, ciID)
	if err != nil {
		t.Fatal(err)
	}
	wantAttrs := map[string]any{"net": map[string]any{"vlan": 10.0}, "owner": "ops"}
	if !reflect.DeepEqual(item.Attributes, wantAttrs) {
		t.Errorf("attributes after the merge patch: %v, want %v", item.Attributes, wantAttrs)
	}

	type ovr struct {
		value, author, reason string
		protected, at         bool
	}
	read := func(field string) ovr {
		t.Helper()
		var o ovr
		if scanErr := f.Admin.QueryRow(bg, `SELECT COALESCE(override_value::text, ''), COALESCE(override_author::text, ''), COALESCE(override_reason, ''),
			protected, override_at IS NOT NULL FROM ci_field_value WHERE ci_id = $1 AND field_name = $2`, ciID, field).
			Scan(&o.value, &o.author, &o.reason, &o.protected, &o.at); scanErr != nil {
			t.Fatalf("override %s: %v", field, scanErr)
		}
		return o
	}
	for field, value := range map[string]string{"name": `"mo-manual"`, "net": `{"vlan": 10}`, "rack": "", "owner": `"ops"`} {
		if got := read(field); got != (ovr{value, user, "ticket 42", true, true}) {
			t.Errorf("override of %s: %+v, want value %s by the editor", field, got, value)
		}
	}
	var untouched int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_field_value WHERE ci_id = $1 AND field_name IN ('model', 'serial_number')`, ciID).Scan(&untouched); err != nil || untouched != 0 {
		t.Errorf("overrides of unchanged fields: %d, %v", untouched, err)
	}

	// A later ingest keeps every manual value and updates the rest.
	ingest := discovery.NewHandler(discovery.NewPGRepository(f.App), repo).WithProvenance(provenance{repo: override.NewPGRepository(f.App)})
	w = serve(ingest.RegisterRoutes, http.MethodPost, "/api/v1/ingest/bulk", `{"collector_id":"col-1","items":[{"ci_type_name":"server",
		"name":"mo-discovered","serial_number":"MO-SN-1","model":"new-model",
		"attributes":{"rack":"R9","owner":"discovery","net":{"vlan":99},"firmware":"1.2"}}]}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
	}
	var resp discovery.BulkIngestResponse
	if err = json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Updated != 1 || resp.ProtectedOverrides != 1 {
		t.Fatalf("ingest response %+v, %v", resp, err)
	}
	if item, err = repo.GetByID(ctx, f.OrgA, ciID); err != nil {
		t.Fatal(err)
	}
	if item.Name != "mo-manual" || item.Model != "new-model" {
		t.Errorf("after ingest: name %q model %q, want the manual name and the discovered model", item.Name, item.Model)
	}
	wantAttrs = map[string]any{"net": map[string]any{"vlan": 10.0}, "owner": "ops", "firmware": "1.2"}
	for k, v := range wantAttrs {
		if !reflect.DeepEqual(item.Attributes[k], v) {
			t.Errorf("attribute %s after ingest: %v, want %v", k, item.Attributes[k], v)
		}
	}
	if _, ok := item.Attributes["rack"]; ok {
		t.Errorf("manually removed attribute rack was restored by discovery: %v", item.Attributes["rack"])
	}

	// Each differing value under an override is one override_conflict
	// review (REC-12, WP-058); a repeated report opens no second one.
	for range 2 {
		if w = serve(ingest.RegisterRoutes, http.MethodPost, "/api/v1/ingest/bulk", `{"collector_id":"col-1","items":[{"ci_type_name":"server",
			"name":"mo-discovered","serial_number":"MO-SN-1","attributes":{"rack":"R9"}}]}`); w.Code != http.StatusAccepted {
			t.Fatalf("repeated ingest: %d", w.Code)
		}
	}
	rows, err := f.Admin.Query(bg, `SELECT payload->>'field' FROM review_item WHERE organization_id = $1 AND kind = 'override_conflict'
		AND status = 'open' AND payload->>'ci_id' = $2 ORDER BY 1`, f.OrgA, ciID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var fields []string
	for rows.Next() {
		var field string
		if err = rows.Scan(&field); err != nil {
			t.Fatal(err)
		}
		fields = append(fields, field)
	}
	if want := []string{"name", "net", "owner", "rack"}; !reflect.DeepEqual(fields, want) {
		t.Errorf("open override_conflict reviews: %v, want %v", fields, want)
	}
}

// TestAuditT3MergePatchNullClearsTypedField covers WP-476 (CI-04, F-080;
// retest of WP-057): PATCH /cis/{id} is an RFC 7396 merge patch on the
// typed fields too. A null member clears the column and gives the field a
// protected override with the value null (CH9), so a later ingest does not
// bring the value back; an absent member stays; null on a required field
// (name, status) is 422 with the field.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
// Retest:
//
//	cd backend && TEST_DATABASE_URL=... go test -count=1 -run 'TestAuditT3MergePatchNullClearsTypedField' ./internal/ci/
func TestAuditT3MergePatchNullClearsTypedField(t *testing.T) {
	r := newAuditT3CIRouter(t, "6c")
	id := r.f.CI(t, r.f.OrgA, r.f.Client1, "audit-t3-null")
	if _, err := r.f.Admin.Exec(context.Background(), `UPDATE ci SET hostname = 'host-a', model = 'model-a',
		firmware_version = '1.2.3', serial_number = 'NULL-SN-1', manufacturer = 'maker-a' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"hostname", "model", "firmware_version"} {
		t.Run(field, func(t *testing.T) {
			w := r.call(http.MethodPatch, "/api/v1/cis/"+id, "", `{"`+field+`":null}`)
			got := r.str(`SELECT coalesce(`+field+`, '<NULL>') FROM ci WHERE id = $1`, id)
			if w.Code != http.StatusOK || got != "<NULL>" {
				t.Errorf("PATCH {%q:null} = %d, stored %s = %q; want 200 and NULL (RFC 7396: null removes the member)", field, w.Code, field, got)
			}
			if o := r.str(`SELECT coalesce((SELECT concat_ws('|', coalesce(override_value::text, '<NULL>'), override_author, protected,
				override_at IS NOT NULL) FROM ci_field_value WHERE ci_id = $1 AND field_name = $2), '<none>')`, id, field); o != "<NULL>|"+r.user+"|t|t" {
				t.Errorf("override of %s after null: %s, want a protected override with value null by the editor (CH9)", field, o)
			}
		})
	}
	// Absent members stay.
	if got := r.str(`SELECT concat_ws('|', name, serial_number, manufacturer) FROM ci WHERE id = $1`, id); got != "audit-t3-null|NULL-SN-1|maker-a" {
		t.Errorf("absent members after the null patches: %s, want them unchanged", got)
	}
	// null on a required field is refused with the field, so is a malformed
	// last_seen_at (not 404, F-229).
	for field, body := range map[string]string{"name": `{"name":null}`, "status": `{"status":null}`,
		"last_seen_at": `{"last_seen_at":"yesterday"}`} {
		w := r.call(http.MethodPatch, "/api/v1/cis/"+id, "", body)
		var problem struct {
			Violations []ci.Violation `json:"violations"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil || w.Code != http.StatusUnprocessableEntity ||
			len(problem.Violations) != 1 || problem.Violations[0].Field != field {
			t.Errorf("PATCH %s = %d %s, want 422 naming %s", body, w.Code, w.Body.String(), field)
		}
	}
	if got := r.str(`SELECT concat_ws('|', name, status) FROM ci WHERE id = $1`, id); got != "audit-t3-null|active" {
		t.Errorf("required fields after the refused null: %s", got)
	}

	// The null override keeps the field empty against a later ingest.
	mux := chi.NewRouter()
	discovery.NewHandler(discovery.NewPGRepository(r.f.App), ci.NewPGRepository(r.f.App)).
		WithProvenance(provenance{repo: override.NewPGRepository(r.f.App)}).RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ingest/bulk", strings.NewReader(`{"collector_id":"col-1","items":[{"ci_type_name":"server",
		"name":"audit-t3-null","serial_number":"NULL-SN-1","model":"model-discovered","hostname":"host-discovered","manufacturer":"maker-b"}]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req.WithContext(r.ctx))
	if w.Code != http.StatusAccepted {
		t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
	}
	if got := r.str(`SELECT concat_ws('|', coalesce(model, '<NULL>'), coalesce(hostname, '<NULL>'), manufacturer) FROM ci WHERE id = $1`, id); got != "<NULL>|<NULL>|maker-b" {
		t.Errorf("after ingest: %s, want model and hostname still NULL and the discovered manufacturer", got)
	}
}
