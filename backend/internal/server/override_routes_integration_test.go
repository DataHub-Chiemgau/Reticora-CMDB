package server_test

// WP-478 (GLO-13, CI-10, E-23; F-092) "Override endpoint refuses reserved
// namespaces": TestAuditT3OverrideRefusesReservedNames, moved here from
// audit_t3_router_auditdefect_test.go (WP-091 defect). Complete NewRouter
// with authz and OpenAPI validation on PostgreSQL (harness
// audit_t3_harness_test.go). PUT and DELETE
// /api/v1/cis/{id}/fields/{name}/override answer 400 with the field "name"
// for _overrides, _observed, _provenance and _instance; attributes, with
// the instance values in attributes._instance, stay unchanged and no
// ci_field_value row is written (the CHECK constraint of migration 000166
// refuses one as well). An override of a normal attribute and of an
// instance attribute still answers 200 (control).
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_03?sslmode=disable \
//     go test -count=1 -v -run 'TestAuditT3OverrideRefusesReservedNames' ./internal/server/

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

func TestAuditT3OverrideRefusesReservedNames(t *testing.T) {
	r := newAuditT3Router(t, "f8")
	device := r.f.CI(t, r.f.OrgA, "", "audit-t3-ns")
	const attrs = `{"owner": "ops", "_instance": {"slot": "7"}}`
	if _, err := r.f.Admin.Exec(t.Context(), `INSERT INTO ci_instance_field_definition (organization_id, ci_id, name, data_type) VALUES ($1, $2, 'slot', 'text')`, r.f.OrgA, device); err != nil {
		t.Fatal(err)
	}
	if _, err := r.f.Admin.Exec(t.Context(), `UPDATE ci SET attributes = $2::jsonb WHERE id = $1`, device, attrs); err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/cis/" + device + "/fields/"
	// Control: a normal attribute override is accepted.
	if w := r.call(identity.AllPermissions(), http.MethodPut, base+"owner/override", `{"value":"netops","reason":"audit"}`); w.Code != http.StatusOK {
		t.Fatalf("control PUT override owner: %d %s", w.Code, w.Body.String())
	}
	if w := r.call(identity.AllPermissions(), http.MethodDelete, base+"owner/override", ""); w.Code != http.StatusOK {
		t.Fatalf("control DELETE override owner: %d %s", w.Code, w.Body.String())
	}
	// Control: an instance attribute override writes attributes._instance.slot.
	if w := r.call(identity.AllPermissions(), http.MethodPut, base+"slot/override", `{"value":"8","reason":"audit"}`); w.Code != http.StatusOK {
		t.Fatalf("control PUT override slot: %d %s", w.Code, w.Body.String())
	}
	if got := r.str(`SELECT COALESCE(attributes -> '_instance' ->> 'slot', '') FROM ci WHERE id = $1`, device); got != "8" {
		t.Fatalf("attributes._instance.slot after the override = %q, want 8", got)
	}
	if w := r.call(identity.AllPermissions(), http.MethodDelete, base+"slot/override", ""); w.Code != http.StatusOK {
		t.Fatalf("control DELETE override slot: %d %s", w.Code, w.Body.String())
	}
	before := r.str(`SELECT attributes::text FROM ci WHERE id = $1`, device)
	beforeVersion := r.str(`SELECT version::text FROM ci WHERE id = $1`, device)

	for _, name := range []string{"_overrides", "_observed", "_provenance", "_instance"} {
		t.Run(name, func(t *testing.T) {
			// Each name starts from the same state, whatever an earlier one wrote.
			if _, err := r.f.Admin.Exec(t.Context(), `UPDATE ci SET attributes = $2::jsonb WHERE id = $1`, device, before); err != nil {
				t.Fatal(err)
			}
			for method, body := range map[string]string{http.MethodPut: `{"value":"x","reason":"audit"}`, http.MethodDelete: ""} {
				w := r.call(identity.AllPermissions(), method, base+name+"/override", body)
				if w.Code != http.StatusBadRequest {
					t.Errorf("%s override %s = %d, want 400 (reserved namespace, GLO-13)", method, name, w.Code)
					continue
				}
				var problem struct {
					Field string `json:"field"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil || problem.Field != "name" {
					t.Errorf("%s override %s: problem %s, want the field name", method, name, w.Body.String())
				}
			}
			if got := r.str(`SELECT attributes::text FROM ci WHERE id = $1`, device); got != before {
				t.Errorf("attributes after override calls on %s = %s, want unchanged %s", name, got, before)
			}
			if got := r.str(`SELECT COALESCE(attributes -> '_instance' ->> 'slot', '') FROM ci WHERE id = $1`, device); got != "8" {
				t.Errorf("instance value slot after override calls on %s = %q, want 8", name, got)
			}
			if got := r.str(`SELECT version::text FROM ci WHERE id = $1`, device); got != beforeVersion {
				t.Errorf("CI version after override calls on %s = %s, want unchanged %s", name, got, beforeVersion)
			}
			if n := r.str(`SELECT count(*)::text FROM ci_field_value WHERE ci_id = $1 AND field_name = $2`, device, name); n != "0" {
				t.Errorf("ci_field_value rows for reserved name %s = %s, want 0", name, n)
			}
		})
	}
}
