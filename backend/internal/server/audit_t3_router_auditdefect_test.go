//go:build auditdefect

package server_test

// Phase-1 acceptance audit, group T3-data-ingest: DEFECT tests (they fail
// until the product is fixed). Complete NewRouter with authz and OpenAPI
// validation on PostgreSQL (harness audit_t3_harness_test.go).
//
// WP-088 (REC-07, AUD-01; attribution) P2 "Review resolver identity and
// unlicensed_ci audit actor can be spoofed via resolved_by": the review
// handler defaults ResolvedBy only when the body leaves it empty and the
// repository uses it as the audit ActorID. Requirement: resolved_by and the
// audit actor are the authenticated principal; a foreign value is ignored or
// refused with 400 (and then nothing is resolved).
//
// WP-091 (GLO-13, CI-10, E-23) P2 "Manual override endpoint accepts reserved
// namespace names; _instance values are destroyed permanently":
// override.PGRepository.SetOverride refuses only structural fields and
// syncCIField writes jsonb_set(attributes, '{<name>}', value). Requirement:
// PUT and DELETE /api/v1/cis/{id}/fields/{name}/override answer 400 for
// _overrides, _observed, _provenance and _instance; attributes stay
// unchanged and no ci_field_value row is written. A normal attribute
// override still answers 200 (control).
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_03?sslmode=disable \
//     go test -tags auditdefect -count=1 -v -run 'TestAuditT3ResolvedByIsThePrincipal|TestAuditT3OverrideRefusesReservedNames' ./internal/server/

import (
	"net/http"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

func TestAuditT3ResolvedByIsThePrincipal(t *testing.T) {
	r := newAuditT3Router(t, "f7")
	for _, kind := range []string{"duplicate_ip", "unlicensed_ci"} {
		t.Run(kind, func(t *testing.T) {
			item := r.reviewItem(kind)
			w := r.call(identity.AllPermissions(), http.MethodPost, "/api/v1/review-items/"+item+"/resolve",
				`{"action":"dismiss","resolved_by":"`+r.other+`"}`)
			status := r.str(`SELECT status FROM review_item WHERE id = $1`, item)
			by := r.str(`SELECT coalesce(resolved_by::text, '') FROM review_item WHERE id = $1`, item)
			switch w.Code {
			case http.StatusBadRequest:
				if status != "open" {
					t.Errorf("refused resolve (400) still changed the item to %q", status)
				}
			case http.StatusOK:
				if by != r.user {
					t.Errorf("caller %s resolved with body resolved_by=%s: stored resolved_by = %s, want the caller (attribution spoofed)", r.user, r.other, by)
				}
			default:
				t.Fatalf("resolve: %d %s", w.Code, w.Body.String())
			}
			if kind == "unlicensed_ci" && w.Code == http.StatusOK {
				actor := r.str(`SELECT coalesce(actor_id::text, '') FROM audit_log WHERE organization_id = $1 AND resource_id::text = $2
					ORDER BY created_at DESC LIMIT 1`, r.f.OrgA, item)
				if actor != r.user {
					t.Errorf("audit actor of the unlicensed_ci dismissal = %s, want the caller %s", actor, r.user)
				}
			}
		})
	}
}

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
	before := r.str(`SELECT attributes::text FROM ci WHERE id = $1`, device)

	for _, name := range []string{"_overrides", "_observed", "_provenance", "_instance"} {
		t.Run(name, func(t *testing.T) {
			// Each name starts from the same state, whatever an earlier one wrote.
			if _, err := r.f.Admin.Exec(t.Context(), `UPDATE ci SET attributes = $2::jsonb WHERE id = $1`, device, before); err != nil {
				t.Fatal(err)
			}
			if _, err := r.f.Admin.Exec(t.Context(), `DELETE FROM ci_field_value WHERE ci_id = $1 AND field_name LIKE '\_%'`, device); err != nil {
				t.Fatal(err)
			}
			if w := r.call(identity.AllPermissions(), http.MethodPut, base+name+"/override", `{"value":"x","reason":"audit"}`); w.Code != http.StatusBadRequest {
				t.Errorf("PUT override %s = %d, want 400 (reserved namespace, GLO-13)", name, w.Code)
			}
			if w := r.call(identity.AllPermissions(), http.MethodDelete, base+name+"/override", ""); w.Code != http.StatusBadRequest {
				t.Errorf("DELETE override %s = %d, want 400 (reserved namespace, GLO-13)", name, w.Code)
			}
			if got := r.str(`SELECT attributes::text FROM ci WHERE id = $1`, device); got != before {
				t.Errorf("attributes after override calls on %s = %s, want unchanged %s", name, got, before)
			}
			if n := r.str(`SELECT count(*)::text FROM ci_field_value WHERE ci_id = $1 AND field_name = $2`, device, name); n != "0" {
				t.Errorf("ci_field_value rows for reserved name %s = %s, want 0", name, n)
			}
		})
	}
}
