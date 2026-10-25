package rls_test

// Error contract API-03 (WP-343, t1-err-core; WP-406, t1-err-ident-loc;
// F-047 in part): repository and database errors of the CMDB core handlers
// (relationship types, CI types, lifecycle, reconciliation source policy,
// privacy retention, discovery scopes) and of teams, roles, role
// permissions, rack mounts, saved views, search and alert rules reach the
// caller only through fixed domain texts or the sanitized RFC 7807 contract
// of api.WriteRepoError: no table, constraint, policy, SQLSTATE or driver
// text, and the status names the client error. The cases WP-010/WP-011 (WP-343)
// and WP-012/WP-014/WP-015/WP-016 (WP-406) come from
// TestAuditT1RepositoryErrorsAreSanitized (auditdefect), WP-013 is the NUL
// case of the discovery scopes (SQLSTATE 22021 -> 400), WP-208 the team
// member written by a team principal outside its team scope
// (TestVerifyB27TeamScope). An object of another organization, client or
// team answers exactly like an unknown one (TEN-04).
//
// Requirements: TEN-06 (API-03).
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
	} {
		t.Run(c.wp+" "+c.name, func(t *testing.T) {
			code, body := w.do(c.p, c.method, c.path, c.body)
			for _, m := range append([]string{"idx_", "_key\"", "_check", "_fkey", "pgx", "lifecycle_definition", "relationship_type", "team_member",
				"rack_mount", "saved_view", "alert_rule", "custom_role", "app_user", "policy for table"}, t1LeakMarkers...) {
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
