//go:build auditdefect

package rls_test

// Phase-1 acceptance audit, group T1-tenancy: DEFECT test (it fails until
// the product is fixed).
//
// The WithTenant migration WPs require that repository and database errors
// reach the caller only through the sanitized RFC 7807 contract of
// api.WriteRepoError (no table, constraint, policy, SQLSTATE or driver text;
// the status names the client error). Confirmed P2 findings, each one case of
// the table below:
//   - WP-010 (CI-01, REL-01): relationship-types duplicate, ci-types NUL byte.
//   - WP-011 (LCM-01, IMP-04, PRV-01): lifecycle duplicate and invalid
//     applies_to, reconciliation source-policy NUL, privacy retention_days.
//   - WP-012 (RBA-01..03): team members / role ids that are no UUID,
//     duplicate team member, role-assignment role id.
//   - WP-014 (LOC-01, RCK-01): rack mount with an invalid, unknown or
//     out-of-scope CI (RLS policy text disclosed to a client-scoped user).
//   - WP-015 (SRC-02, VIEW-01): saved-view duplicate, structured search
//     upstream_of, full-text search NUL.
//   - WP-016 (MON-01): alert rule NUL name and duration overflow.
//   - WP-017 (AST-01, AST-04): reservation unknown asset / invalid id /
//     release of an invisible (client-2) reservation answering 409 instead
//     of 404, asset PATCH with an invalid id (composition lookup).
//   - WP-018 (AST-02, AST-05): stock movement unknown asset, inventory item
//     unknown client.
//   - WP-020 (TKT-01, SLA-01, CMP-01): compliance rule unknown ci_type,
//     ticket comment invalid id, SLA attach invalid id.
//   - WP-021 (FRM-01, WFL-01, IGA-01): form for a foreign client by a
//     client-scoped user (RLS text), workflow-runs filter, IGA access request.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -tags auditdefect -count=1 -v -run 'TestAuditT1RepositoryErrorsAreSanitized' ./internal/tenant/rls/

import (
	"net/http"
	"strings"
	"testing"
)

// t1LeakMarkers are fragments of PostgreSQL and pgx error texts that must
// never reach an API caller.
var t1LeakMarkers = []string{"SQLSTATE", "ERROR:", "violates", "constraint \"", "row-level security", "invalid input syntax",
	"invalid byte sequence", "can't scan", "failed to encode", "OID "}

func TestAuditT1RepositoryErrorsAreSanitized(t *testing.T) {
	w := newT1World(t, "d5")
	f := w.f
	ci2 := f.CI(t, f.OrgA, f.Client2, "t1-err-c2")
	room1 := f.Room(t, f.Client1, "t1-err-s1")
	rack := w.scalar(`INSERT INTO rack (organization_id, room_id, name) VALUES ($1, $2, 't1-err-rack') RETURNING id::text`, f.OrgA, room1)
	ticket := w.scalar(`INSERT INTO ticket (organization_id, title, reporter_id) VALUES ($1, 't1-err-ticket', $2) RETURNING id::text`, f.OrgA, w.userA)
	client1 := w.as("client1", only(f.Client1), all())
	const nul = "x\x00y"
	const unknown = "00000000-0000-4000-8000-0000000000ff"

	// Fixtures created through the API: the first request of each duplicate
	// pair must succeed.
	w.create(w.orgA, "/api/v1/relationship-types", map[string]any{"key": "t1_dup", "display_name": "T1", "reverse_name": "T1 rev",
		"directed": true, "category": "logical", "impact_direction": "none", "cardinality": "n:m"})
	lifecycle := map[string]any{"key": "t1_dup", "name": "T1", "applies_to": "asset",
		"states": []any{map[string]any{"key": "a", "label": "A", "is_initial": true}}}
	w.create(w.orgA, "/api/v1/lifecycle-definitions", lifecycle)
	team := w.create(w.orgA, "/api/v1/teams", map[string]any{"name": "t1-err-team"})
	if code, body := w.do(w.orgA, http.MethodPost, "/api/v1/teams/"+team+"/members", map[string]any{"user_id": w.userA}); code < 200 || code >= 300 {
		t.Fatalf("setup: add team member: %d %s", code, trim(body))
	}
	w.create(w.orgA, "/api/v1/saved-views", map[string]any{"name": "t1-dup", "entity_kind": "ci"})
	reservation2 := w.create(w.orgA, "/api/v1/reservations", map[string]any{"asset_id": f.Asset(t, f.OrgA, f.Client2, "t1-err-a2")})

	for _, c := range []struct {
		wp, name     string
		p            *channelPrincipal
		method, path string
		body         any
		want         []int
	}{
		{"WP-010", "relationship type duplicate", w.orgA, http.MethodPost, "/api/v1/relationship-types", map[string]any{"key": "t1_dup", "display_name": "T1",
			"reverse_name": "T1 rev", "directed": true, "category": "logical", "impact_direction": "none", "cardinality": "n:m"}, []int{409}},
		{"WP-010", "ci type with a NUL byte", w.orgA, http.MethodPost, "/api/v1/ci-types", map[string]any{"key": "t1_nul", "name": nul}, []int{400, 422}},
		{"WP-011", "lifecycle definition duplicate", w.orgA, http.MethodPost, "/api/v1/lifecycle-definitions", lifecycle, []int{409}},
		{"WP-011", "lifecycle definition invalid applies_to", w.orgA, http.MethodPost, "/api/v1/lifecycle-definitions", map[string]any{"key": "t1_bad",
			"name": "T1", "applies_to": "no-such-kind", "states": []any{map[string]any{"key": "a", "label": "A", "is_initial": true}}}, []int{400, 422}},
		{"WP-011", "source policy with a NUL byte", w.orgA, http.MethodPut, "/api/v1/reconciliation/source-policy", map[string]any{"name": nul,
			"priorities": []string{"manual"}}, []int{400, 422}},
		{"WP-011", "privacy retention_days beyond int4", w.orgA, http.MethodPut, "/api/v1/privacy/retention", map[string]any{"retention_days": 3000000000}, []int{400, 422}},
		{"WP-012", "team members of an invalid team id", w.orgA, http.MethodGet, "/api/v1/teams/not-a-uuid/members", nil, []int{400, 404}},
		{"WP-012", "duplicate team member", w.orgA, http.MethodPost, "/api/v1/teams/" + team + "/members", map[string]any{"user_id": w.userA}, []int{409}},
		{"WP-012", "role assignment with an invalid role id", w.orgA, http.MethodPost, "/api/v1/role-assignments", map[string]any{"user_id": w.userA,
			"role_id": "not-a-uuid"}, []int{400, 404, 422}},
		{"WP-012", "role permissions of an invalid role id", w.orgA, http.MethodPut, "/api/v1/roles/not-a-uuid/permissions",
			map[string]any{"permission_keys": []string{"ci:read"}}, []int{400, 404}},
		{"WP-012", "PATCH role with an invalid id", w.orgA, http.MethodPatch, "/api/v1/roles/not-a-uuid", map[string]any{"name": "x"}, []int{400, 404}},
		{"WP-012", "DELETE role with an invalid id", w.orgA, http.MethodDelete, "/api/v1/roles/not-a-uuid", nil, []int{400, 404}},
		{"WP-014", "rack mount with an invalid ci_id", w.orgA, http.MethodPost, "/api/v1/racks/" + rack + "/mounts", map[string]any{"ci_id": "not-a-uuid",
			"position_u": 1, "height_u": 1, "face": "front"}, []int{400, 422}},
		{"WP-014", "rack mount with an unknown ci_id", w.orgA, http.MethodPost, "/api/v1/racks/" + rack + "/mounts", map[string]any{"ci_id": unknown,
			"position_u": 1, "height_u": 1, "face": "front"}, []int{404, 422}},
		{"WP-014", "rack mount of a client-2 CI by client 1", client1, http.MethodPost, "/api/v1/racks/" + rack + "/mounts", map[string]any{"ci_id": ci2,
			"position_u": 3, "height_u": 1, "face": "front"}, []int{403, 404, 422}},
		{"WP-015", "saved view duplicate", w.orgA, http.MethodPost, "/api/v1/saved-views", map[string]any{"name": "t1-dup", "entity_kind": "ci"}, []int{409}},
		{"WP-015", "structured search with an invalid upstream_of", w.orgA, http.MethodPost, "/api/v1/search/query", map[string]any{"entity_kind": "ci",
			"upstream_of": "not-a-uuid"}, []int{400, 422}},
		{"WP-015", "full-text search with a NUL byte", w.orgA, http.MethodGet, "/api/v1/search?q=a%00b", nil, []int{400, 422}},
		{"WP-016", "alert rule with a NUL byte", w.orgA, http.MethodPost, "/api/v1/monitoring/alerts", map[string]any{"name": nul, "metric_name": "cpu",
			"condition": "gt", "threshold": 1}, []int{400, 422}},
		{"WP-016", "alert rule duration overflow", w.orgA, http.MethodPost, "/api/v1/monitoring/alerts", map[string]any{"name": "t1", "metric_name": "cpu",
			"condition": "gt", "threshold": 1, "duration": int64(9223372036854775807)}, []int{400, 422}},
		{"WP-017", "reservation of an unknown asset", w.orgA, http.MethodPost, "/api/v1/reservations", map[string]any{"asset_id": unknown}, []int{404, 422}},
		{"WP-017", "release of an invalid reservation id", w.orgA, http.MethodPost, "/api/v1/reservations/not-a-uuid/release", nil, []int{400, 404}},
		{"WP-017", "release of a client-2 reservation by client 1", client1, http.MethodPost, "/api/v1/reservations/" + reservation2 + "/release", nil, []int{404}},
		{"WP-017", "PATCH asset with an invalid id", w.orgA, http.MethodPatch, "/api/v1/assets/not-a-uuid", map[string]any{"name": "x"}, []int{400, 404}},
		{"WP-018", "stock movement of an unknown asset", w.orgA, http.MethodPost, "/api/v1/stock-movements", map[string]any{"movement_type": "correction",
			"asset_id": unknown, "reason": "t1"}, []int{404, 422}},
		{"WP-018", "inventory item of an unknown client", w.orgA, http.MethodPost, "/api/v1/inventory/items", map[string]any{"name": "t1",
			"client_id": unknown}, []int{403, 404, 422}},
		{"WP-020", "compliance rule of an unknown ci type", w.orgA, http.MethodPost, "/api/v1/compliance/rules", map[string]any{"ci_type_id": unknown,
			"name": "t1", "severity": "high", "category": "security", "expression": map[string]any{"field": "x"}}, []int{400, 404, 422}},
		{"WP-020", "ticket comment of an invalid ticket id", w.orgA, http.MethodPost, "/api/v1/tickets/not-a-uuid/comments", map[string]any{"content": "x"}, []int{400, 404}},
		{"WP-020", "SLA attach with an invalid sla_id", w.orgA, http.MethodPost, "/api/v1/tickets/" + ticket + "/sla", map[string]any{"sla_id": "not-a-uuid"}, []int{400, 404, 422}},
		{"WP-021", "form for client 2 by client 1", client1, http.MethodPost, "/api/v1/forms", map[string]any{"name": "t1", "schema": map[string]any{"type": "object"},
			"client_id": f.Client2}, []int{403, 404}},
		{"WP-021", "workflow runs filtered by an invalid workflow id", w.orgA, http.MethodGet, "/api/v1/workflow-runs?workflow_id=not-a-uuid", nil, []int{400, 422}},
		{"WP-021", "IGA access request for an unknown subject", w.orgA, http.MethodPost, "/api/v1/iga/access-requests", map[string]any{"subject_user_id": unknown,
			"entitlement": "x"}, []int{400, 404, 422}},
	} {
		t.Run(c.wp+" "+c.name, func(t *testing.T) {
			code, body := w.do(c.p, c.method, c.path, c.body)
			for _, m := range t1LeakMarkers {
				if strings.Contains(body, m) {
					t.Errorf("%s %s answered %d and discloses database text %q: %s", c.method, c.path, code, m, trim(body))
					break
				}
			}
			ok := false
			for _, s := range c.want {
				ok = ok || code == s
			}
			if !ok {
				t.Errorf("%s %s answered %d, want one of %v", c.method, c.path, code, c.want)
			}
		})
	}
}
