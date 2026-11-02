package rls_test

// Error contract API-03 (WP-343, t1-err-core; WP-406, t1-err-ident-loc;
// WP-407, t1-err-modules; F-047): repository and database errors of the CMDB
// core handlers (relationship types, CI types, lifecycle, reconciliation
// source policy, privacy retention, discovery scopes), of teams, roles, role
// permissions, rack mounts, saved views, search and alert rules, and of the
// module handlers (reservations, assets, stock movements and quantity items,
// compliance rules, tickets, SLAs, forms, workflows, IGA) reach the caller
// only through fixed domain texts or the sanitized RFC 7807 contract of
// api.WriteRepoError: no table, constraint, policy, SQLSTATE or driver text,
// and the status names the client error. The cases WP-010/WP-011 (WP-343),
// WP-012/WP-014/WP-015/WP-016 (WP-406) and WP-017/WP-018/WP-020/WP-021
// (WP-407) come from the former defect test
// TestAuditT1RepositoryErrorsAreSanitized, WP-013 is the NUL case of the
// discovery scopes (SQLSTATE 22021 -> 400), WP-208 the team member written by
// a team principal outside its team scope (TestVerifyB27TeamScope); the
// credential cases come from WP-661 (k7n-credential-invalid-id, F-390). An object
// of another organization, client or team answers exactly like an unknown
// one (TEN-04).
//
// Requirements: TEN-06 (API-03), TKT-01, WFL-01.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -count=1 -v -run '^TestRepositoryErrorsAreSanitized$' ./internal/tenant/rls/

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// t1LeakMarkers are fragments of PostgreSQL and pgx error texts that must
// never reach an API caller.
var t1LeakMarkers = []string{"SQLSTATE", "ERROR:", "violates", "constraint \"", "row-level security", "invalid input syntax",
	"invalid byte sequence", "can't scan", "failed to encode", "OID "}

func TestRepositoryErrorsAreSanitized(t *testing.T) {
	w := newT1World(t, "46")
	f := w.f
	collector := w.scalar(`INSERT INTO collector (organization_id, client_id, name, status) VALUES ($1, $2, 't1-err-collector', 'active')
		RETURNING id::text`, f.OrgA, f.Client1)
	ci1 := f.CI(t, f.OrgA, f.Client1, "t1-err-c1")
	ci2 := f.CI(t, f.OrgA, f.Client2, "t1-err-c2")
	ciB := f.CI(t, f.OrgB, "", "t1-err-b")
	room1 := f.Room(t, f.Client1, "t1-err-s1")
	rack := w.scalar(`INSERT INTO rack (organization_id, room_id, name) VALUES ($1, $2, 't1-err-rack') RETURNING id::text`, f.OrgA, room1)
	member := f.AppUser(t, f.OrgA, "t1-err-member")
	client1 := w.as("client1", only(f.Client1), all())
	const nul = "x\x00y"
	const unknown = "00000000-0000-4000-8000-0000000000ff"
	// Objects of the module handlers (WP-407): assets of client 1, client 2
	// and organization B, a private CI type and a collector of organization
	// B, a ticket without related objects (visible to every scope).
	ticket := w.scalar(`INSERT INTO ticket (organization_id, title, reporter_id) VALUES ($1, 't1-err-ticket', $2) RETURNING id::text`, f.OrgA, w.userA)
	asset1 := f.Asset(t, f.OrgA, f.Client1, "t1-err-a1")
	asset2 := f.Asset(t, f.OrgA, f.Client2, "t1-err-a2")
	assetB := f.Asset(t, f.OrgB, "", "t1-err-ab")
	typeB := w.scalar(`INSERT INTO ci_type (organization_id, key, name) VALUES ($1, 't1_err_b', 'T1 B') RETURNING id::text`, f.OrgB)
	collectorB := w.scalar(`INSERT INTO collector (organization_id, name, status) VALUES ($1, 't1-err-collector-b', 'active') RETURNING id::text`, f.OrgB)

	// Fixtures created through the API: the first request of each duplicate
	// pair must succeed.
	relType := map[string]any{"key": "t1_dup", "display_name": "T1", "reverse_name": "T1 rev",
		"directed": true, "category": "logical", "impact_direction": "none", "cardinality": "n:m"}
	w.create(w.orgA, "/api/v1/relationship-types", relType)
	ciType := map[string]any{"key": "t1_dup", "name": "T1 Dup"}
	w.create(w.orgA, "/api/v1/ci-types", ciType)
	lifecycle := map[string]any{"key": "t1_dup", "name": "T1", "applies_to": "asset",
		"states": []any{map[string]any{"key": "a", "label": "A", "is_initial": true}}}
	w.create(w.orgA, "/api/v1/lifecycle-definitions", lifecycle)
	team := w.create(w.orgA, "/api/v1/teams", map[string]any{"name": "t1-err-team"})
	team2 := w.create(w.orgA, "/api/v1/teams", map[string]any{"name": "t1-err-team-2"})
	w.create(w.orgA, "/api/v1/teams/"+team+"/members", map[string]any{"user_id": w.userA})
	// The team principal is restricted to the first team (app.team_scope).
	teamPrincipal := &channelPrincipal{kind: "team", org: f.OrgA, user: w.userA, scope: identity.Scope{Clients: all(), Sites: all(), Teams: only(team)}}
	w.create(w.orgA, "/api/v1/saved-views", map[string]any{"name": "t1-dup", "entity_kind": "ci"})
	mount := func(ci string) map[string]any {
		return map[string]any{"ci_id": ci, "position_u": 3, "height_u": 1, "face": "front"}
	}
	alert := func(name string, duration any) map[string]any {
		return map[string]any{"name": name, "metric_name": "cpu", "condition": "gt", "threshold": 1, "duration": duration}
	}
	reservation2 := w.create(w.orgA, "/api/v1/reservations", map[string]any{"asset_id": asset2})
	sla2 := w.create(w.orgA, "/api/v1/slas", map[string]any{"name": "t1-err-sla-2", "priority": "high", "response_target_minutes": 30,
		"resolution_target_minutes": 60, "client_id": f.Client2})
	form := w.create(w.orgA, "/api/v1/forms", map[string]any{"name": "t1-err-form", "schema": map[string]any{"type": "object"}})
	movement := func(asset string) map[string]any {
		return map[string]any{"movement_type": "correction", "asset_id": asset, "reason": "t1"}
	}
	rule := func(ciType string) map[string]any {
		return map[string]any{"ci_type_id": ciType, "name": "t1-err-rule", "severity": "high", "category": "security",
			"expression": map[string]any{"field": "x"}}
	}

	for _, c := range []struct {
		wp, name     string
		p            *channelPrincipal
		method, path string
		body         any
		want         []int
		// detail is a fragment of the expected answer (fixed text or
		// the field of a field error).
		detail string
	}{
		{"WP-010", "relationship type duplicate", w.orgA, http.MethodPost, "/api/v1/relationship-types", relType, []int{409}, "already exists"},
		{"WP-010", "relationship type with a NUL byte", w.orgA, http.MethodPost, "/api/v1/relationship-types", map[string]any{"key": "t1_nul",
			"display_name": nul, "reverse_name": "T1 rev"}, []int{400}, "invalid format or is out of range"},
		{"WP-010", "ci type with a NUL byte", w.orgA, http.MethodPost, "/api/v1/ci-types", map[string]any{"key": "t1_nul", "name": nul}, []int{400, 422},
			"invalid format or is out of range"},
		{"WP-010", "ci type duplicate", w.orgA, http.MethodPost, "/api/v1/ci-types", ciType, []int{409}, "already exists"},
		{"WP-011", "lifecycle definition duplicate", w.orgA, http.MethodPost, "/api/v1/lifecycle-definitions", lifecycle, []int{409}, "already exists"},
		{"WP-011", "lifecycle definition invalid applies_to", w.orgA, http.MethodPost, "/api/v1/lifecycle-definitions", map[string]any{"key": "t1_bad",
			"name": "T1", "applies_to": "no-such-kind", "states": []any{map[string]any{"key": "a", "label": "A", "is_initial": true}}}, []int{422}, `"field":"applies_to"`},
		{"WP-011", "lifecycle definition with a NUL byte", w.orgA, http.MethodPost, "/api/v1/lifecycle-definitions", map[string]any{"key": "t1_nul",
			"name": nul, "states": []any{map[string]any{"key": "a", "label": "A", "is_initial": true}}}, []int{400}, "invalid format or is out of range"},
		// WP-661 (F-390): the credential handler answers an id that is no
		// UUID like an unknown one, not 500.
		{"WP-011", "credential of an invalid id", w.orgA, http.MethodGet, "/api/v1/credentials/not-a-uuid", nil, []int{404}, "credential not found"},
		{"WP-011", "credential deletion of an invalid id", w.orgA, http.MethodDelete, "/api/v1/credentials/not-a-uuid", nil, []int{404},
			"credential not found"},
		{"WP-011", "credential of an unknown id", w.orgA, http.MethodGet, "/api/v1/credentials/" + unknown, nil, []int{404}, "credential not found"},
		{"WP-011", "credential with a NUL byte", w.orgA, http.MethodPost, "/api/v1/credentials/", map[string]any{"name": nul, "kind": "snmp_v2c",
			"secret": map[string]any{"community": "x"}}, []int{400}, "invalid format or is out of range"},
		{"WP-011", "credential of an unknown client", w.orgA, http.MethodPost, "/api/v1/credentials/", map[string]any{"name": "t1-err-cred",
			"kind": "snmp_v2c", "client_id": unknown, "secret": map[string]any{"community": "x"}}, []int{422}, "does not exist or is still in use"},
		{"WP-011", "credential with an invalid client_id", w.orgA, http.MethodPost, "/api/v1/credentials/", map[string]any{"name": "t1-err-cred",
			"kind": "snmp_v2c", "client_id": "not-a-uuid", "secret": map[string]any{"community": "x"}}, []int{400}, "invalid format or is out of range"},
		{"WP-011", "credential for client 2 by client 1", client1, http.MethodPost, "/api/v1/credentials/", map[string]any{"name": "t1-err-cred",
			"kind": "snmp_v2c", "client_id": f.Client2, "secret": map[string]any{"community": "x"}}, []int{403}, "the operation is not permitted"},
		{"WP-011", "lifecycle transition of an invalid CI id", w.orgA, http.MethodPost, "/api/v1/cis/not-a-uuid/lifecycle-transitions",
			map[string]any{"to_state": "a"}, []int{404}, "entity not found"},
		{"WP-011", "source policy with a NUL byte", w.orgA, http.MethodPut, "/api/v1/reconciliation/source-policy", map[string]any{"name": nul,
			"priorities": []string{"manual"}}, []int{400, 422}, `"field":"name"`},
		{"WP-011", "source policy priority with a NUL byte", w.orgA, http.MethodPut, "/api/v1/reconciliation/source-policy",
			map[string]any{"priorities": []string{"manual", nul}}, []int{400}, `"field":"priorities"`},
		{"WP-011", "source policy without priorities", w.orgA, http.MethodPut, "/api/v1/reconciliation/source-policy",
			map[string]any{"priorities": []string{}}, []int{400}, `"field":"priorities"`},
		{"WP-011", "privacy retention_days beyond int4", w.orgA, http.MethodPut, "/api/v1/privacy/retention", map[string]any{"retention_days": 3000000000},
			[]int{400, 422}, `"field":"retention_days"`},
		{"WP-011", "privacy retention_days just beyond int4", w.orgA, http.MethodPut, "/api/v1/privacy/retention", map[string]any{"retention_days": 2147483648},
			[]int{400}, `"field":"retention_days"`},
		{"WP-011", "privacy retention_days negative", w.orgA, http.MethodPut, "/api/v1/privacy/retention", map[string]any{"retention_days": -1}, []int{400},
			`"field":"retention_days"`},
		{"WP-011", "privacy mode unknown", w.orgA, http.MethodPut, "/api/v1/privacy/retention", map[string]any{"mode": "shred"}, []int{400}, `"field":"mode"`},
		{"WP-013", "discovery scope with a NUL name", w.orgA, http.MethodPost, "/api/v1/discovery-scopes", map[string]any{"collector_id": collector,
			"kind": "cidr", "target": "10.246.0.0/24", "name": nul, "schedule": "24h"}, []int{400}, "invalid format or is out of range"},

		// WP-406: teams, roles and role permissions (WP-012, WP-208).
		{"WP-012", "team members of an invalid team id", w.orgA, http.MethodGet, "/api/v1/teams/not-a-uuid/members", nil, []int{404}, "team not found"},
		{"WP-012", "duplicate team member", w.orgA, http.MethodPost, "/api/v1/teams/" + team + "/members", map[string]any{"user_id": w.userA}, []int{409},
			"member of the team already"},
		{"WP-012", "team member of another organization", w.orgA, http.MethodPost, "/api/v1/teams/" + team + "/members", map[string]any{"user_id": w.userB},
			[]int{422}, `"field":"user_id"`},
		{"WP-012", "team member unknown", w.orgA, http.MethodPost, "/api/v1/teams/" + team + "/members", map[string]any{"user_id": unknown}, []int{422},
			`"field":"user_id"`},
		{"WP-012", "team lead of another organization", w.orgA, http.MethodPost, "/api/v1/teams", map[string]any{"name": "t1-err-lead", "lead_id": w.userB},
			[]int{422}, `"field":"lead_id"`},
		{"WP-012", "team removal of an invalid member id", w.orgA, http.MethodDelete, "/api/v1/teams/" + team + "/members/not-a-uuid", nil, []int{404},
			"team member not found"},
		{"WP-012", "role assignment with an invalid role id", w.orgA, http.MethodPost, "/api/v1/role-assignments", map[string]any{"user_id": w.userA,
			"role_id": "not-a-uuid"}, []int{404}, "role not found"},
		{"WP-012", "role permissions of an invalid role id", w.orgA, http.MethodPut, "/api/v1/roles/not-a-uuid/permissions",
			map[string]any{"permission_keys": []string{"ci:read"}}, []int{404}, "role not found"},
		{"WP-012", "role permissions of an unknown role", w.orgA, http.MethodPut, "/api/v1/roles/" + unknown + "/permissions",
			map[string]any{"permission_keys": []string{"ci:read"}}, []int{404}, "role not found"},
		{"WP-012", "PATCH role with an invalid id", w.orgA, http.MethodPatch, "/api/v1/roles/not-a-uuid", map[string]any{"name": "x"}, []int{404}, "role not found"},
		{"WP-012", "DELETE role with an invalid id", w.orgA, http.MethodDelete, "/api/v1/roles/not-a-uuid", nil, []int{404}, "role not found"},
		{"WP-208", "team member of a foreign team by a team principal", teamPrincipal, http.MethodPost, "/api/v1/teams/" + team2 + "/members",
			map[string]any{"user_id": member}, []int{404}, "team not found"},
		{"WP-208", "team member of an unknown team by a team principal", teamPrincipal, http.MethodPost, "/api/v1/teams/" + unknown + "/members",
			map[string]any{"user_id": member}, []int{404}, "team not found"},

		// WP-406: rack mounts (WP-014).
		{"WP-014", "rack mount with an invalid ci_id", w.orgA, http.MethodPost, "/api/v1/racks/" + rack + "/mounts", mount("not-a-uuid"), []int{422},
			`"field":"ci_id"`},
		{"WP-014", "rack mount with an unknown ci_id", w.orgA, http.MethodPost, "/api/v1/racks/" + rack + "/mounts", mount(unknown), []int{422}, "CI not found"},
		{"WP-014", "rack mount of a client-2 CI by client 1", client1, http.MethodPost, "/api/v1/racks/" + rack + "/mounts", mount(ci2), []int{422},
			"CI not found"},
		{"WP-014", "rack mount of an organization-B CI", w.orgA, http.MethodPost, "/api/v1/racks/" + rack + "/mounts", mount(ciB), []int{422}, "CI not found"},
		{"WP-014", "rack mount at an invalid ci_id path", w.orgA, http.MethodPut, "/api/v1/racks/" + rack + "/mounts/not-a-uuid",
			map[string]any{"position_u": 3}, []int{422}, `"field":"ci_id"`},
		{"WP-014", "rack mount of an unknown rack", w.orgA, http.MethodPost, "/api/v1/racks/not-a-uuid/mounts", mount(ci1), []int{404}, "rack not found"},

		// WP-406: saved views and search (WP-015).
		{"WP-015", "saved view duplicate", w.orgA, http.MethodPost, "/api/v1/saved-views", map[string]any{"name": "t1-dup", "entity_kind": "ci"}, []int{409},
			"already exists"},
		{"WP-015", "saved view of an invalid id", w.orgA, http.MethodGet, "/api/v1/saved-views/not-a-uuid", nil, []int{404}, "saved view not found"},
		{"WP-015", "structured search with an invalid upstream_of", w.orgA, http.MethodPost, "/api/v1/search/query", map[string]any{"entity_kind": "ci",
			"upstream_of": "not-a-uuid"}, []int{422}, `"field":"upstream_of"`},
		{"WP-015", "full-text search with a NUL byte", w.orgA, http.MethodGet, "/api/v1/search?q=a%00b", nil, []int{400}, `"field":"q"`},
		{"WP-015", "full-text search with a NUL type", w.orgA, http.MethodGet, "/api/v1/search?q=a&type=ci%00", nil, []int{400}, `"field":"type"`},

		// WP-406: alert rules (WP-016).
		{"WP-016", "alert rule with a NUL byte", w.orgA, http.MethodPost, "/api/v1/monitoring/alerts", map[string]any{"name": nul, "metric_name": "cpu",
			"condition": "gt", "threshold": 1}, []int{400}, `"field":"name"`},
		{"WP-016", "alert rule duration overflow", w.orgA, http.MethodPost, "/api/v1/monitoring/alerts", alert("t1", int64(9223372036854775807)),
			[]int{422}, `"field":"duration"`},
		{"WP-016", "alert rule negative duration", w.orgA, http.MethodPost, "/api/v1/monitoring/alerts", alert("t1", "-1s"), []int{422}, `"field":"duration"`},
		{"WP-016", "alert rule duration beyond the retention", w.orgA, http.MethodPost, "/api/v1/monitoring/alerts", alert("t1", "9601h"), []int{422},
			`"field":"duration"`},

		// WP-407: reservations and assets (WP-017).
		{"WP-017", "reservation of an unknown asset", w.orgA, http.MethodPost, "/api/v1/reservations", map[string]any{"asset_id": unknown}, []int{404},
			"asset not found"},
		{"WP-017", "release of an invalid reservation id", w.orgA, http.MethodPost, "/api/v1/reservations/not-a-uuid/release", nil, []int{404},
			"reservation not found"},
		{"WP-017", "release of a client-2 reservation by client 1", client1, http.MethodPost, "/api/v1/reservations/" + reservation2 + "/release", nil,
			[]int{404}, "reservation not found"},
		{"WP-017", "PATCH asset with an invalid id", w.orgA, http.MethodPatch, "/api/v1/assets/not-a-uuid", map[string]any{"name": "x"}, []int{404},
			"asset not found"},
		{"WP-017", "reservation of an organization-B asset", w.orgA, http.MethodPost, "/api/v1/reservations", map[string]any{"asset_id": assetB},
			[]int{404}, "asset not found"},
		{"WP-017", "reservation of a client-2 asset by client 1", client1, http.MethodPost, "/api/v1/reservations", map[string]any{"asset_id": asset2},
			[]int{404}, "asset not found"},
		{"WP-017", "reservation with an invalid asset_id", w.orgA, http.MethodPost, "/api/v1/reservations", map[string]any{"asset_id": "not-a-uuid"},
			[]int{422}, `"field":"asset_id"`},
		{"WP-017", "reservation of an invalid id", w.orgA, http.MethodGet, "/api/v1/reservations/not-a-uuid", nil, []int{404}, "reservation not found"},
		{"WP-017", "asset of an invalid id", w.orgA, http.MethodGet, "/api/v1/assets/not-a-uuid", nil, []int{404}, "asset not found"},
		{"WP-017", "asset linked to a client-2 CI by client 1", client1, http.MethodPost, "/api/v1/assets", map[string]any{"asset_tag": "t1-err-link",
			"name": "t1", "client_id": f.Client1, "ci_id": ci2}, []int{422}, `"field":"ci_id"`},

		// WP-407: stock movements and quantity items (WP-018).
		{"WP-018", "stock movement of an unknown asset", w.orgA, http.MethodPost, "/api/v1/stock-movements", movement(unknown), []int{404},
			"asset not found"},
		{"WP-018", "inventory item of an unknown client", w.orgA, http.MethodPost, "/api/v1/inventory/items", map[string]any{"name": "t1",
			"client_id": unknown}, []int{422}, "a referenced record does not exist"},
		{"WP-018", "stock movement of an organization-B asset", w.orgA, http.MethodPost, "/api/v1/stock-movements", movement(assetB), []int{404},
			"asset not found"},
		{"WP-018", "stock movement to an unknown location", w.orgA, http.MethodPost, "/api/v1/stock-movements", map[string]any{"movement_type": "correction",
			"asset_id": asset1, "to_location_id": unknown}, []int{422}, `"field":"to_location_id"`},
		{"WP-018", "inventory item of an invalid id", w.orgA, http.MethodGet, "/api/v1/inventory/items/not-a-uuid", nil, []int{404},
			"inventory item not found"},
		{"WP-018", "movements of an invalid asset id", w.orgA, http.MethodGet, "/api/v1/assets/not-a-uuid/movements", nil, []int{404}, "asset not found"},

		// WP-407: compliance rules, tickets and SLAs (WP-020).
		{"WP-020", "compliance rule of an unknown ci type", w.orgA, http.MethodPost, "/api/v1/compliance/rules", rule(unknown), []int{422},
			`"field":"ci_type_id"`},
		{"WP-020", "ticket comment of an invalid ticket id", w.orgA, http.MethodPost, "/api/v1/tickets/not-a-uuid/comments", map[string]any{"content": "x"},
			[]int{404}, "ticket not found"},
		{"WP-020", "SLA attach with an invalid sla_id", w.orgA, http.MethodPost, "/api/v1/tickets/" + ticket + "/sla", map[string]any{"sla_id": "not-a-uuid"},
			[]int{422}, `"field":"sla_id"`},
		{"WP-020", "compliance rule of an organization-B ci type", w.orgA, http.MethodPost, "/api/v1/compliance/rules", rule(typeB), []int{422},
			`"field":"ci_type_id"`},
		{"WP-020", "compliance rule of an invalid id", w.orgA, http.MethodGet, "/api/v1/compliance/rules/not-a-uuid", nil, []int{404},
			"compliance rule not found"},
		{"WP-020", "ticket related to a client-2 CI by client 1", client1, http.MethodPost, "/api/v1/tickets", map[string]any{"title": "t1-err-foreign",
			"related_ci_id": ci2}, []int{422}, `"field":"related_ci_id"`},
		{"WP-020", "ticket assigned to a user of organization B", w.orgA, http.MethodPost, "/api/v1/tickets", map[string]any{"title": "t1-err-foreign",
			"assignee_id": w.userB}, []int{422}, `"field":"assignee_id"`},
		{"WP-020", "SLA attach of a client-2 SLA by client 1", client1, http.MethodPost, "/api/v1/tickets/" + ticket + "/sla", map[string]any{"sla_id": sla2},
			[]int{422}, `"field":"sla_id"`},
		{"WP-020", "SLA of an invalid ticket id", w.orgA, http.MethodPost, "/api/v1/tickets/not-a-uuid/sla", map[string]any{}, []int{404},
			"ticket not found"},

		// WP-407: forms, workflows and IGA (WP-021).
		{"WP-021", "form for client 2 by client 1", client1, http.MethodPost, "/api/v1/forms", map[string]any{"name": "t1", "schema": map[string]any{"type": "object"},
			"client_id": f.Client2}, []int{403}, "the operation is not permitted"},
		{"WP-021", "workflow runs filtered by an invalid workflow id", w.orgA, http.MethodGet, "/api/v1/workflow-runs?workflow_id=not-a-uuid", nil, []int{400},
			`"field":"workflow_id"`},
		{"WP-021", "IGA access request for an unknown subject", w.orgA, http.MethodPost, "/api/v1/iga/access-requests", map[string]any{"subject_user_id": unknown,
			"entitlement": "x"}, []int{422}, `"field":"subject_user_id"`},
		{"WP-021", "form submission with a CI of organization B", w.orgA, http.MethodPost, "/api/v1/forms/" + form + "/submissions",
			map[string]any{"values": map[string]any{}, "ci_id": ciB}, []int{422}, `"field":"ci_id"`},
		{"WP-021", "workflow run of an invalid id", w.orgA, http.MethodGet, "/api/v1/workflow-runs/not-a-uuid", nil, []int{404}, "workflow run not found"},
		{"WP-021", "IGA access request for a user of organization B", w.orgA, http.MethodPost, "/api/v1/iga/access-requests",
			map[string]any{"subject_user_id": w.userB, "entitlement": "x"}, []int{422}, `"field":"subject_user_id"`},
		{"WP-021", "IGA connector with a collector of organization B", w.orgA, http.MethodPost, "/api/v1/iga/connectors", map[string]any{"name": "t1-err-relay",
			"type": "relay", "collector_id": collectorB}, []int{422}, `"field":"collector_id"`},
		{"WP-021", "IGA access request decision of an invalid id", w.orgA, http.MethodPost, "/api/v1/iga/access-requests/not-a-uuid/approve", nil,
			[]int{404}, "access request not found"},
	} {
		t.Run(c.wp+" "+c.name, func(t *testing.T) {
			code, body := w.do(c.p, c.method, c.path, c.body)
			for _, m := range append([]string{"idx_", "_key\"", "_check", "_fkey", "pgx", "lifecycle_definition", "relationship_type", "team_member",
				"rack_mount", "saved_view", "alert_rule", "custom_role", "app_user", "policy for table", "asset_movement", "quantity_item",
				"compliance_rule", "ticket_comment", "ticket_sla", "form_def", "form_submission", "workflow_run", "workflow_def", "iga_access_request",
				"iga_connector", "relation \""}, t1LeakMarkers...) {
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
				t.Errorf("%s %s answered %d, want one of %v: %s", c.method, c.path, code, c.want, trim(body))
			}
			if !strings.Contains(body, c.detail) {
				t.Errorf("%s %s answered %s, want %s", c.method, c.path, trim(body), c.detail)
			}
		})
	}

	// An object of another organization, client or team answers exactly
	// like an unknown one: same status, same body but the trace id (TEN-04).
	t.Run("foreign and unknown objects answer alike", func(t *testing.T) {
		for _, pair := range []struct {
			name                string
			p                   *channelPrincipal
			method, path        string
			foreign, unknownReq any
			foreignPath         string
		}{
			{"rack mount, client-2 CI by client 1", client1, http.MethodPost, "/api/v1/racks/" + rack + "/mounts", mount(ci2), mount(unknown), ""},
			{"rack mount, organization-B CI", w.orgA, http.MethodPost, "/api/v1/racks/" + rack + "/mounts", mount(ciB), mount(unknown), ""},
			{"team member, user of organization B", w.orgA, http.MethodPost, "/api/v1/teams/" + team + "/members", map[string]any{"user_id": w.userB},
				map[string]any{"user_id": unknown}, ""},
			{"team lead, user of organization B", w.orgA, http.MethodPost, "/api/v1/teams", map[string]any{"name": "t1-err-lead", "lead_id": w.userB},
				map[string]any{"name": "t1-err-lead", "lead_id": unknown}, ""},
			{"team member, team outside the team scope", teamPrincipal, http.MethodPost, "/api/v1/teams/" + unknown + "/members",
				map[string]any{"user_id": member}, map[string]any{"user_id": member}, "/api/v1/teams/" + team2 + "/members"},
			// WP-407: the module handlers.
			{"reservation, client-2 asset by client 1", client1, http.MethodPost, "/api/v1/reservations", map[string]any{"asset_id": asset2},
				map[string]any{"asset_id": unknown}, ""},
			{"reservation, organization-B asset", w.orgA, http.MethodPost, "/api/v1/reservations", map[string]any{"asset_id": assetB},
				map[string]any{"asset_id": unknown}, ""},
			{"release, client-2 reservation by client 1", client1, http.MethodPost, "/api/v1/reservations/" + unknown + "/release", nil, nil,
				"/api/v1/reservations/" + reservation2 + "/release"},
			{"asset, client-2 CI by client 1", client1, http.MethodPost, "/api/v1/assets", map[string]any{"asset_tag": "t1-err-link", "name": "t1",
				"client_id": f.Client1, "ci_id": ci2}, map[string]any{"asset_tag": "t1-err-link", "name": "t1", "client_id": f.Client1, "ci_id": unknown}, ""},
			{"stock movement, client-2 asset by client 1", client1, http.MethodPost, "/api/v1/stock-movements", movement(asset2), movement(unknown), ""},
			{"stock movement, organization-B asset", w.orgA, http.MethodPost, "/api/v1/stock-movements", movement(assetB), movement(unknown), ""},
			{"compliance rule, CI type of organization B", w.orgA, http.MethodPost, "/api/v1/compliance/rules", rule(typeB), rule(unknown), ""},
			{"ticket, client-2 CI by client 1", client1, http.MethodPost, "/api/v1/tickets", map[string]any{"title": "t1-err-foreign", "related_ci_id": ci2},
				map[string]any{"title": "t1-err-foreign", "related_ci_id": unknown}, ""},
			{"ticket, assignee of organization B", w.orgA, http.MethodPost, "/api/v1/tickets", map[string]any{"title": "t1-err-foreign",
				"assignee_id": w.userB}, map[string]any{"title": "t1-err-foreign", "assignee_id": unknown}, ""},
			{"SLA attach, client-2 SLA by client 1", client1, http.MethodPost, "/api/v1/tickets/" + ticket + "/sla", map[string]any{"sla_id": sla2},
				map[string]any{"sla_id": unknown}, ""},
			{"form, client 2 by client 1", client1, http.MethodPost, "/api/v1/forms", map[string]any{"name": "t1", "schema": map[string]any{"type": "object"},
				"client_id": f.Client2}, map[string]any{"name": "t1", "schema": map[string]any{"type": "object"}, "client_id": unknown}, ""},
			{"form submission, CI of organization B", w.orgA, http.MethodPost, "/api/v1/forms/" + form + "/submissions",
				map[string]any{"values": map[string]any{}, "ci_id": ciB}, map[string]any{"values": map[string]any{}, "ci_id": unknown}, ""},
			{"IGA access request, subject of organization B", w.orgA, http.MethodPost, "/api/v1/iga/access-requests",
				map[string]any{"subject_user_id": w.userB, "entitlement": "x"}, map[string]any{"subject_user_id": unknown, "entitlement": "x"}, ""},
			{"IGA connector, collector of organization B", w.orgA, http.MethodPost, "/api/v1/iga/connectors", map[string]any{"name": "t1-err-relay",
				"type": "relay", "collector_id": collectorB}, map[string]any{"name": "t1-err-relay", "type": "relay", "collector_id": unknown}, ""},
		} {
			foreignPath := pair.path
			if pair.foreignPath != "" {
				foreignPath = pair.foreignPath
			}
			fc, fb := w.do(pair.p, pair.method, foreignPath, pair.foreign)
			uc, ub := w.do(pair.p, pair.method, pair.path, pair.unknownReq)
			if fc != uc || withoutTraceID(t, fb) != withoutTraceID(t, ub) {
				t.Errorf("%s: foreign %d %s, unknown %d %s; want the same answer", pair.name, fc, trim(fb), uc, trim(ub))
			}
		}
	})

	// The refused writes left nothing behind; the same writes within the
	// scope succeed.
	t.Run("refused writes store nothing, writes in scope succeed", func(t *testing.T) {
		if n := w.scalar(`SELECT count(*)::text FROM rack_mount WHERE rack_id = $1`, rack); n != "0" {
			t.Errorf("%s mounts in the rack after the refusals, want 0", n)
		}
		if n := w.scalar(`SELECT count(*)::text FROM team_member WHERE team_id = $1 OR user_id = $2`, team2, w.userB); n != "0" {
			t.Errorf("%s memberships of team 2 or the organization-B user after the refusals, want 0", n)
		}
		if n := w.scalar(`SELECT count(*)::text FROM team WHERE organization_id = $1 AND name = 't1-err-lead'`, f.OrgA); n != "0" {
			t.Errorf("%s teams led by a user of another organization, want 0", n)
		}
		if code, body := w.do(client1, http.MethodPost, "/api/v1/racks/"+rack+"/mounts", mount(ci1)); code != http.StatusCreated {
			t.Errorf("mount of a client-1 CI by client 1: %d %s, want 201", code, trim(body))
		}
		// The rack rule keeps its domain message (RCK-01).
		if code, body := w.do(w.orgA, http.MethodPut, "/api/v1/racks/"+rack+"/mounts/"+ci2, map[string]any{"position_u": 3}); code != http.StatusConflict ||
			!strings.Contains(body, "overlaps mount at position 3U") {
			t.Errorf("mount over the occupied unit 3: %d %s, want 409 naming the occupied unit", code, trim(body))
		}
		if code, body := w.do(teamPrincipal, http.MethodPost, "/api/v1/teams/"+team+"/members", map[string]any{"user_id": member}); code != http.StatusCreated {
			t.Errorf("member of the own team by the team principal: %d %s, want 201", code, trim(body))
		}
		if code, body := w.do(w.orgA, http.MethodPost, "/api/v1/teams", map[string]any{"name": "t1-err-own-lead", "lead_id": member}); code != http.StatusCreated {
			t.Errorf("team led by a user of the organization: %d %s, want 201", code, trim(body))
		}
		if code, body := w.do(w.orgA, http.MethodPost, "/api/v1/monitoring/alerts", alert("t1-max", "9600h")); code != http.StatusCreated ||
			!strings.Contains(body, `"duration":"9600h0m0s"`) {
			t.Errorf("alert rule with the longest duration: %d %s, want 201", code, trim(body))
		}
	})

	// WP-018: a stock movement of a client-2 asset by a client-1 principal is
	// 404 with the fixed text like an unknown asset (not 400 "not found"),
	// names no table or constraint and records nothing; the movement of a
	// client-1 asset is 201.
	t.Run("WP-018 stock movement of a client-2 asset by client 1", func(t *testing.T) {
		before := w.scalar(`SELECT count(*)::text FROM asset_movement WHERE asset_id = $1`, asset2)
		code, body := w.do(client1, http.MethodPost, "/api/v1/stock-movements", movement(asset2))
		if code != http.StatusNotFound || !strings.Contains(body, "asset not found") {
			t.Errorf("movement of a client-2 asset by client 1: %d %s, want 404 asset not found", code, trim(body))
		}
		for _, m := range append([]string{"asset_movement", "_fkey", "policy for table"}, t1LeakMarkers...) {
			if strings.Contains(body, m) {
				t.Errorf("movement of a client-2 asset discloses database text %q: %s", m, trim(body))
			}
		}
		if after := w.scalar(`SELECT count(*)::text FROM asset_movement WHERE asset_id = $1`, asset2); after != before {
			t.Errorf("asset_movement rows of the client-2 asset: %s before, %s after the refusal", before, after)
		}
		if code, body := w.do(client1, http.MethodPost, "/api/v1/stock-movements", movement(asset1)); code != http.StatusCreated {
			t.Errorf("movement of a client-1 asset by client 1: %d %s, want 201", code, trim(body))
		}
	})

	// WP-407: the refused module writes left nothing behind; the same writes
	// within the scope succeed, and the domain messages stay.
	t.Run("refused module writes store nothing, writes in scope succeed", func(t *testing.T) {
		for _, c := range []struct{ what, query, arg string }{
			{"reservations of an unknown or organization-B asset", `SELECT count(*)::text FROM reservation WHERE asset_id = ANY(ARRAY[$1, '` + unknown + `']::uuid[])`, assetB},
			{"assets linked to the client-2 CI", `SELECT count(*)::text FROM asset WHERE ci_id = $1`, ci2},
			{"compliance rules of the organization-B type", `SELECT count(*)::text FROM compliance_rule WHERE ci_type_id = $1`, typeB},
			{"tickets with a foreign CI or assignee", `SELECT count(*)::text FROM ticket WHERE title = 't1-err-foreign' AND organization_id = $1`, f.OrgA},
			{"forms for client 2", `SELECT count(*)::text FROM form_def WHERE name = 't1' AND client_id = $1`, f.Client2},
			{"submissions with the organization-B CI", `SELECT count(*)::text FROM form_submission WHERE ci_id = $1`, ciB},
			{"access requests for the organization-B user", `SELECT count(*)::text FROM iga_access_request WHERE subject_user_id = $1`, w.userB},
			{"connectors with the organization-B collector", `SELECT count(*)::text FROM iga_connector WHERE collector_id = $1`, collectorB},
		} {
			if n := w.scalar(c.query, c.arg); n != "0" {
				t.Errorf("%s %s after the refusals, want 0", n, c.what)
			}
		}
		if state := w.scalar(`SELECT state FROM reservation WHERE id = $1`, reservation2); state != "active" {
			t.Errorf("client-2 reservation after the release by client 1: %s, want active", state)
		}
		for _, c := range []struct {
			name         string
			p            *channelPrincipal
			method, path string
			body         any
			want         int
			detail       string
		}{
			{"reservation of a client-1 asset by client 1", client1, http.MethodPost, "/api/v1/reservations", map[string]any{"asset_id": asset1}, 201, ""},
			{"second reservation of the asset (domain rule)", client1, http.MethodPost, "/api/v1/reservations", map[string]any{"asset_id": asset1}, 409,
				"asset already has an active reservation"},
			{"release of the client-2 reservation by the organization", w.orgA, http.MethodPost, "/api/v1/reservations/" + reservation2 + "/release", nil,
				200, ""},
			{"second release (domain rule)", w.orgA, http.MethodPost, "/api/v1/reservations/" + reservation2 + "/release", nil, 409,
				"reservation is not active"},
			{"asset linked to a client-1 CI by client 1", client1, http.MethodPost, "/api/v1/assets", map[string]any{"asset_tag": "t1-err-own",
				"name": "t1", "client_id": f.Client1, "ci_id": ci1}, 201, ""},
			{"compliance rule of the system server type", w.orgA, http.MethodPost, "/api/v1/compliance/rules",
				rule(w.scalar(`SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1`)), 201, ""},
			{"ticket related to a client-1 CI by client 1", client1, http.MethodPost, "/api/v1/tickets", map[string]any{"title": "t1-err-own",
				"related_ci_id": ci1, "assignee_id": w.userA}, 201, ""},
			{"comment on the ticket", w.orgA, http.MethodPost, "/api/v1/tickets/" + ticket + "/comments", map[string]any{"content": "x"}, 201, ""},
			{"form for client 1 by client 1", client1, http.MethodPost, "/api/v1/forms", map[string]any{"name": "t1-own",
				"schema": map[string]any{"type": "object"}, "client_id": f.Client1}, 201, ""},
			{"form submission with a CI of the organization", w.orgA, http.MethodPost, "/api/v1/forms/" + form + "/submissions",
				map[string]any{"values": map[string]any{}, "ci_id": ci1}, 201, ""},
			{"access request for a user of the organization", w.orgA, http.MethodPost, "/api/v1/iga/access-requests",
				map[string]any{"subject_user_id": member, "entitlement": "x"}, 201, ""},
			{"connector with a collector of the organization", w.orgA, http.MethodPost, "/api/v1/iga/connectors",
				map[string]any{"name": "t1-err-own-relay", "type": "relay", "collector_id": collector}, 201, ""},
		} {
			code, body := w.do(c.p, c.method, c.path, c.body)
			if code != c.want || !strings.Contains(body, c.detail) {
				t.Errorf("%s: %d %s, want %d %s", c.name, code, trim(body), c.want, c.detail)
			}
		}
	})

	// WP-015: renaming a saved view to the name of another view of the owner
	// is 409, not 404, and changes nothing; an unknown view is 404, a free
	// name 200.
	t.Run("WP-015 saved view renamed to an existing name", func(t *testing.T) {
		view := w.create(w.orgA, "/api/v1/saved-views", map[string]any{"name": "t1-rename", "entity_kind": "ci"})
		code, body := w.do(w.orgA, http.MethodPatch, "/api/v1/saved-views/"+view, map[string]any{"name": "t1-dup"})
		if code != http.StatusConflict || !strings.Contains(body, "already exists") {
			t.Errorf("PATCH to a taken name: %d %s, want 409", code, trim(body))
		}
		for _, m := range append([]string{"saved_view", "_key\""}, t1LeakMarkers...) {
			if strings.Contains(body, m) {
				t.Errorf("PATCH to a taken name discloses database text %q: %s", m, trim(body))
			}
		}
		if name := w.scalar(`SELECT name FROM saved_view WHERE id = $1`, view); name != "t1-rename" {
			t.Errorf("name after the refused rename %q, want t1-rename", name)
		}
		if code, body := w.do(w.orgA, http.MethodPatch, "/api/v1/saved-views/"+unknown, map[string]any{"name": "t1-free"}); code != http.StatusNotFound {
			t.Errorf("PATCH of an unknown view: %d %s, want 404", code, trim(body))
		}
		if code, body := w.do(w.orgA, http.MethodPatch, "/api/v1/saved-views/"+view, map[string]any{"name": "t1-free"}); code != http.StatusOK {
			t.Errorf("PATCH to a free name: %d %s, want 200", code, trim(body))
		}
		if name := w.scalar(`SELECT name FROM saved_view WHERE id = $1`, view); name != "t1-free" {
			t.Errorf("name after the rename %q, want t1-free", name)
		}
	})

	// Positive controls: the boundaries of the validation stay accepted.
	t.Run("retention_days 2147483647 accepted", func(t *testing.T) {
		if code, body := w.do(w.orgA, http.MethodPut, "/api/v1/privacy/retention", map[string]any{"retention_days": 2147483647}); code != http.StatusOK {
			t.Fatalf("PUT retention_days 2147483647: %d %s", code, trim(body))
		}
		if days := w.scalar(`SELECT retention_days::text FROM privacy_retention_policy WHERE organization_id = $1`, f.OrgA); days != "2147483647" {
			t.Fatalf("stored retention_days %s, want 2147483647", days)
		}
	})
	t.Run("lifecycle definition applies_to ci accepted", func(t *testing.T) {
		w.create(w.orgA, "/api/v1/lifecycle-definitions", map[string]any{"key": "t1_ci", "name": "T1 CI", "applies_to": "ci",
			"states": []any{map[string]any{"key": "a", "label": "A", "is_initial": true}}})
	})
}

// withoutTraceID returns the problem body without its trace id, which
// differs per request.
func withoutTraceID(t *testing.T, body string) string {
	t.Helper()
	var problem map[string]any
	if err := json.Unmarshal([]byte(body), &problem); err != nil {
		t.Fatalf("problem body %q: %v", trim(body), err)
	}
	delete(problem, "trace_id")
	out, err := json.Marshal(problem)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
