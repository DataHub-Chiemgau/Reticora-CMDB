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
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestInstanceAttributeValuesOnPostgreSQL covers MET-14 (WP-256) against
// PostgreSQL:
//   - GET /cis/{id}/instance-attributes lists {key, definition, value} for
//     every instance definition of the CI, complete without a cursor;
//   - PUT and DELETE /cis/{id}/instance-attributes/{key} write through
//     Service.Update: If-Match, version + 1, a ci_change row of the field
//     attributes, audit ci.updated and the event ci.changed in the outbox;
//     the value lies under _instance and never on the top level; DELETE
//     keeps the definition;
//   - a key without a definition and a value the definition refuses are
//     422 with violations[].field;
//   - PATCH /cis/{id} of a changed instance key without
//     ci_instance_attribute:manage is 403, the value sent unchanged is 200;
//   - the override PUT of an instance key is 422;
//   - the type change and bulk set_status leave _instance untouched;
//   - GET /cis/{id} keeps the flat read view (WP-091).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestInstanceAttributeValuesOnPostgreSQL(t *testing.T) {
	f := scopetest.Seed(t, "83")
	bg := context.Background()
	base := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	principal := func(perms ...identity.Permission) context.Context {
		return identity.WithPrincipal(base, identity.Principal{Subject: f.User, OrganizationID: f.OrgA,
			Type: identity.PrincipalTypeUser, Permissions: perms})
	}
	manager := principal(identity.PermCIRead, identity.PermCIWrite, identity.PermCIInstanceAttributeManage, identity.PermOverrideWrite)
	technician := principal(identity.PermCIRead, identity.PermCIWrite)

	repo := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox())
	svc := ci.NewService(repo).WithFieldResolver(citype.NewPGRepository(f.App))
	mux := chi.NewRouter()
	// The field policy source of the server router (WP-265).
	mux.Use(fieldPolicySource())
	ci.NewHandler(svc).RegisterRoutes(mux)
	override.NewHandler(override.NewPGRepository(f.App)).RegisterRoutes(mux)
	call := func(ctx context.Context, method, path, body string, header ...string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
		r.Header.Set("Content-Type", "application/json")
		for i := 0; i+1 < len(header); i += 2 {
			r.Header.Set(header[i], header[i+1])
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	want := func(step string, w *httptest.ResponseRecorder, code int) {
		t.Helper()
		if w.Code != code {
			t.Fatalf("%s: %d %s, want %d", step, w.Code, w.Body.String(), code)
		}
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	device := f.CI(t, f.OrgA, f.Client1, "inst-256")
	stored := func() map[string]any {
		t.Helper()
		var raw []byte
		if err := f.Admin.QueryRow(bg, `SELECT attributes FROM ci WHERE id = $1`, device).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	instance := func() map[string]any {
		t.Helper()
		m, _ := stored()["_instance"].(map[string]any)
		return m
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci_instance_field_definition (organization_id, ci_id, name, data_type, sort_order)
		VALUES ($1, $2, 'rack_label', 'text', 1), ($1, $2, 'slots', 'integer', 2)`, f.OrgA, device); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/cis/" + device + "/instance-attributes"
	etag := func() string {
		t.Helper()
		w := call(manager, http.MethodGet, "/api/v1/cis/"+device, "")
		want("GET /cis/{id}", w, http.StatusOK)
		return w.Header().Get("ETag")
	}
	list := func() map[string]any {
		t.Helper()
		w := call(technician, http.MethodGet, path, "")
		want("GET instance-attributes", w, http.StatusOK)
		var body struct {
			Data       []ci.InstanceAttribute `json:"data"`
			NextCursor *string                `json:"next_cursor"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.NextCursor != nil || len(body.Data) != 2 || body.Data[0].Key != "rack_label" || body.Data[1].Key != "slots" ||
			body.Data[0].Definition.Name != "rack_label" || body.Data[0].Definition.DataType != "text" {
			t.Fatalf("instance attributes: %s", w.Body.String())
		}
		return map[string]any{"rack_label": body.Data[0].Value, "slots": body.Data[1].Value}
	}
	type marks struct{ version, changes, audits, events int }
	mark := func() marks {
		t.Helper()
		return marks{
			version: count(`SELECT version FROM ci WHERE id = $1`, device),
			changes: count(`SELECT count(*) FROM ci_change WHERE ci_id = $1 AND field_name = 'attributes'`, device),
			audits:  count(`SELECT count(*) FROM audit_log WHERE resource_id = $1 AND action = 'ci.updated'`, device),
			events:  count(`SELECT count(*) FROM event_outbox WHERE entity_id = $1::text AND type = 'ci.changed'`, device),
		}
	}
	oneWrite := func(step string, before marks) {
		t.Helper()
		after := mark()
		if after.version != before.version+1 || after.changes != before.changes+1 || after.audits != before.audits+1 ||
			after.events != before.events+1 {
			t.Errorf("%s: before %+v, after %+v: want version + 1, one ci_change (attributes), one ci.updated, one ci.changed",
				step, before, after)
		}
	}

	// GET lists every definition, without a value yet.
	if values := list(); values["rack_label"] != nil || values["slots"] != nil {
		t.Errorf("values before a write: %v", values)
	}

	// PUT with If-Match writes the value under _instance.
	before := mark()
	w := call(manager, http.MethodPut, path+"/rack_label", `{"value":"A-07"}`, "If-Match", etag())
	want("PUT rack_label", w, http.StatusOK)
	oneWrite("PUT rack_label", before)
	var attr ci.InstanceAttribute
	if err := json.Unmarshal(w.Body.Bytes(), &attr); err != nil || attr.Key != "rack_label" || attr.Value != "A-07" {
		t.Errorf("PUT response: %s (%v)", w.Body.String(), err)
	}
	if got := w.Header().Get("ETag"); got != etag() {
		t.Errorf("PUT ETag %q, want the new version %q", got, etag())
	}
	if attrs := stored(); attrs["rack_label"] != nil || instance()["rack_label"] != "A-07" {
		t.Errorf("stored after PUT: %v", attrs)
	}
	if values := list(); values["rack_label"] != "A-07" {
		t.Errorf("values after PUT: %v", values)
	}
	// GET /cis/{id} keeps the flat read view (WP-091).
	var item ci.Item
	if err := json.Unmarshal(call(manager, http.MethodGet, "/api/v1/cis/"+device, "").Body.Bytes(), &item); err != nil ||
		item.Attributes["rack_label"] != "A-07" || item.Attributes["_instance"] != nil {
		t.Errorf("GET /cis/{id}: %v (%v)", item.Attributes, err)
	}

	// A stale If-Match is refused.
	if w = call(manager, http.MethodPut, path+"/rack_label", `{"value":"A-08"}`, "If-Match", `"1"`); w.Code != http.StatusConflict &&
		w.Code != http.StatusPreconditionFailed {
		t.Errorf("PUT with a stale If-Match: %d %s, want 409 or 412", w.Code, w.Body.String())
	}

	// 422 with violations[].field: a key without a definition, an invalid
	// value, a null value.
	for _, c := range []struct{ key, body string }{
		{"unknown_key", `{"value":"x"}`},
		{"slots", `{"value":"many"}`},
		{"slots", `{"value":null}`},
	} {
		before = mark()
		w = call(manager, http.MethodPut, path+"/"+c.key, c.body)
		want("PUT "+c.key+" "+c.body, w, http.StatusUnprocessableEntity)
		var problem struct {
			Violations []struct {
				Field string `json:"field"`
			} `json:"violations"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil || len(problem.Violations) != 1 || problem.Violations[0].Field != c.key {
			t.Errorf("PUT %s %s: %s, want one violation of the field %s", c.key, c.body, w.Body.String(), c.key)
		}
		if after := mark(); after != before {
			t.Errorf("PUT %s %s wrote: %+v -> %+v", c.key, c.body, before, after)
		}
	}
	w = call(manager, http.MethodDelete, path+"/unknown_key", "")
	want("DELETE unknown_key", w, http.StatusUnprocessableEntity)

	// A valid integer.
	w = call(manager, http.MethodPut, path+"/slots", `{"value":4}`)
	want("PUT slots", w, http.StatusOK)
	if instance()["slots"] != 4.0 {
		t.Errorf("stored after PUT slots: %v", stored())
	}

	// PATCH: a changed instance key needs ci_instance_attribute:manage, the
	// unchanged value sent along with the whole map does not.
	before = mark()
	w = call(technician, http.MethodPatch, "/api/v1/cis/"+device, `{"attributes":{"rack_label":"B-01"}}`)
	want("PATCH of a changed instance key without the right", w, http.StatusForbidden)
	if after := mark(); after != before || instance()["rack_label"] != "A-07" {
		t.Errorf("a refused PATCH wrote: %+v -> %+v, %v", before, after, stored())
	}
	w = call(technician, http.MethodPatch, "/api/v1/cis/"+device, `{"attributes":{"slots":null}}`)
	want("PATCH removing an instance value without the right", w, http.StatusForbidden)
	w = call(technician, http.MethodPatch, "/api/v1/cis/"+device, `{"attributes":{"rack_label":"A-07","slots":4,"owner":"ops"}}`)
	want("PATCH with unchanged instance values without the right", w, http.StatusOK)
	if attrs := stored(); attrs["owner"] != "ops" || instance()["rack_label"] != "A-07" || instance()["slots"] != 4.0 {
		t.Errorf("stored after the PATCH: %v", attrs)
	}
	w = call(manager, http.MethodPatch, "/api/v1/cis/"+device, `{"attributes":{"rack_label":"A-09"}}`)
	want("PATCH of a changed instance key with the right", w, http.StatusOK)
	if instance()["rack_label"] != "A-09" {
		t.Errorf("stored after the PATCH with the right: %v", stored())
	}

	// The override PUT of an instance key is 422 and writes nothing.
	before = mark()
	w = call(manager, http.MethodPut, "/api/v1/cis/"+device+"/fields/rack_label/override", `{"value":"X-1","reason":"wp-256"}`)
	want("override PUT of an instance key", w, http.StatusUnprocessableEntity)
	if after := mark(); after != before || instance()["rack_label"] != "A-09" ||
		count(`SELECT count(*) FROM ci_field_value WHERE ci_id = $1 AND field_name = 'rack_label' AND override_reason = 'wp-256'`, device) != 0 {
		t.Errorf("the refused override wrote: %+v -> %+v, %v", before, after, stored())
	}

	// The type change and bulk set_status leave _instance untouched.
	held := instance()
	var router string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'router' AND organization_id IS NULL`).Scan(&router); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ChangeType(manager, f.OrgA, device, router); err != nil {
		t.Fatalf("type change: %v", err)
	}
	if got := instance(); got["rack_label"] != held["rack_label"] || got["slots"] != held["slots"] || len(got) != len(held) {
		t.Errorf("_instance after the type change: %v, want %v", got, held)
	}
	result, _, err := svc.Bulk(manager, f.OrgA, &ci.BulkRequest{Action: ci.BulkSetStatus, IDs: []string{device}, Status: "maintenance"})
	if err != nil || len(result.Items) != 1 || result.Items[0].Code != http.StatusOK {
		t.Fatalf("bulk set_status: %+v, %v", result, err)
	}
	if got := instance(); got["rack_label"] != held["rack_label"] || got["slots"] != held["slots"] || len(got) != len(held) {
		t.Errorf("_instance after bulk set_status: %v, want %v", got, held)
	}

	// DELETE removes the value from _instance; the definition stays.
	before = mark()
	w = call(manager, http.MethodDelete, path+"/rack_label", "", "If-Match", etag())
	want("DELETE rack_label", w, http.StatusNoContent)
	oneWrite("DELETE rack_label", before)
	if _, ok := instance()["rack_label"]; ok || stored()["rack_label"] != nil {
		t.Errorf("stored after DELETE: %v", stored())
	}
	if n := count(`SELECT count(*) FROM ci_instance_field_definition WHERE ci_id = $1 AND name = 'rack_label'`, device); n != 1 {
		t.Errorf("definitions of rack_label after DELETE: %d, want 1", n)
	}
	if values := list(); values["rack_label"] != nil || values["slots"] != 4.0 {
		t.Errorf("values after DELETE: %v", values)
	}

	// An unknown CI is 404.
	w = call(manager, http.MethodGet, "/api/v1/cis/00000000-0000-0000-0000-0000000000f6/instance-attributes", "")
	want("GET of an unknown CI", w, http.StatusNotFound)
}
