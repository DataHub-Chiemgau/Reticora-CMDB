package rls_test

// Error contract API-03 (WP-343, t1-err-core; F-047 in part): repository and
// database errors of the CMDB core handlers (relationship types, CI types,
// lifecycle, reconciliation source policy, privacy retention, discovery
// scopes) reach the caller only through fixed domain texts or the sanitized
// RFC 7807 contract of api.WriteRepoError: no table, constraint, policy,
// SQLSTATE or driver text, and the status names the client error. The cases
// WP-010/WP-011 come from TestAuditT1RepositoryErrorsAreSanitized (auditdefect),
// WP-013 is the NUL case of the discovery scopes (SQLSTATE 22021 -> 400).
//
// Requirements: TEN-06 (API-03).
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -count=1 -v -run '^TestRepositoryErrorsAreSanitized$' ./internal/tenant/rls/

import (
	"net/http"
	"strings"
	"testing"
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
	const nul = "x\x00y"

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

	for _, c := range []struct {
		wp, name     string
		method, path string
		body         any
		want         []int
		// detail is a fragment of the expected answer (fixed text or
		// the field of a field error).
		detail string
	}{
		{"WP-010", "relationship type duplicate", http.MethodPost, "/api/v1/relationship-types", relType, []int{409}, "already exists"},
		{"WP-010", "relationship type with a NUL byte", http.MethodPost, "/api/v1/relationship-types", map[string]any{"key": "t1_nul",
			"display_name": nul, "reverse_name": "T1 rev"}, []int{400}, "invalid format or is out of range"},
		{"WP-010", "ci type with a NUL byte", http.MethodPost, "/api/v1/ci-types", map[string]any{"key": "t1_nul", "name": nul}, []int{400, 422}, "invalid format or is out of range"},
		{"WP-010", "ci type duplicate", http.MethodPost, "/api/v1/ci-types", ciType, []int{409}, "already exists"},
		{"WP-011", "lifecycle definition duplicate", http.MethodPost, "/api/v1/lifecycle-definitions", lifecycle, []int{409}, "already exists"},
		{"WP-011", "lifecycle definition invalid applies_to", http.MethodPost, "/api/v1/lifecycle-definitions", map[string]any{"key": "t1_bad",
			"name": "T1", "applies_to": "no-such-kind", "states": []any{map[string]any{"key": "a", "label": "A", "is_initial": true}}}, []int{422}, `"field":"applies_to"`},
		{"WP-011", "lifecycle definition with a NUL byte", http.MethodPost, "/api/v1/lifecycle-definitions", map[string]any{"key": "t1_nul",
			"name": nul, "states": []any{map[string]any{"key": "a", "label": "A", "is_initial": true}}}, []int{400}, "invalid format or is out of range"},
		{"WP-011", "lifecycle transition of an invalid CI id", http.MethodPost, "/api/v1/cis/not-a-uuid/lifecycle-transitions",
			map[string]any{"to_state": "a"}, []int{404}, "entity not found"},
		{"WP-011", "source policy with a NUL byte", http.MethodPut, "/api/v1/reconciliation/source-policy", map[string]any{"name": nul,
			"priorities": []string{"manual"}}, []int{400, 422}, `"field":"name"`},
		{"WP-011", "source policy priority with a NUL byte", http.MethodPut, "/api/v1/reconciliation/source-policy",
			map[string]any{"priorities": []string{"manual", nul}}, []int{400}, `"field":"priorities"`},
		{"WP-011", "source policy without priorities", http.MethodPut, "/api/v1/reconciliation/source-policy",
			map[string]any{"priorities": []string{}}, []int{400}, `"field":"priorities"`},
		{"WP-011", "privacy retention_days beyond int4", http.MethodPut, "/api/v1/privacy/retention", map[string]any{"retention_days": 3000000000}, []int{400, 422},
			`"field":"retention_days"`},
		{"WP-011", "privacy retention_days just beyond int4", http.MethodPut, "/api/v1/privacy/retention", map[string]any{"retention_days": 2147483648}, []int{400},
			`"field":"retention_days"`},
		{"WP-011", "privacy retention_days negative", http.MethodPut, "/api/v1/privacy/retention", map[string]any{"retention_days": -1}, []int{400}, `"field":"retention_days"`},
		{"WP-011", "privacy mode unknown", http.MethodPut, "/api/v1/privacy/retention", map[string]any{"mode": "shred"}, []int{400}, `"field":"mode"`},
		{"WP-013", "discovery scope with a NUL name", http.MethodPost, "/api/v1/discovery-scopes", map[string]any{"collector_id": collector,
			"kind": "cidr", "target": "10.246.0.0/24", "name": nul, "schedule": "24h"}, []int{400}, "invalid format or is out of range"},
	} {
		t.Run(c.wp+" "+c.name, func(t *testing.T) {
			code, body := w.do(w.orgA, c.method, c.path, c.body)
			for _, m := range append([]string{"idx_", "_key", "_check", "_fkey", "pgx", "lifecycle_definition", "relationship_type"}, t1LeakMarkers...) {
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
