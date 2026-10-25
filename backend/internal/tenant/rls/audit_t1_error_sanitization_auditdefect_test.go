//go:build auditdefect

package rls_test

// Phase-1 acceptance audit, group T1-tenancy: DEFECT test (it fails until
// the product is fixed).
//
// The WithTenant migration WPs require that repository and database errors
// reach the caller only through the sanitized RFC 7807 contract of
// api.WriteRepoError (no table, constraint, policy, SQLSTATE or driver text;
// the status names the client error). Confirmed P2 findings, each one case of
// the table below (the cases of WP-010 and WP-011 moved to
// TestRepositoryErrorsAreSanitized in error_sanitization_integration_test.go
// with WP-343, those of WP-012, WP-014, WP-015 and WP-016 with WP-406; the
// rest follows with WP-407):
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

func TestAuditT1RepositoryErrorsAreSanitized(t *testing.T) {
	w := newT1World(t, "d5")
	f := w.f
	ticket := w.scalar(`INSERT INTO ticket (organization_id, title, reporter_id) VALUES ($1, 't1-err-ticket', $2) RETURNING id::text`, f.OrgA, w.userA)
	client1 := w.as("client1", only(f.Client1), all())
	const unknown = "00000000-0000-4000-8000-0000000000ff"

	// Fixture created through the API.
	reservation2 := w.create(w.orgA, "/api/v1/reservations", map[string]any{"asset_id": f.Asset(t, f.OrgA, f.Client2, "t1-err-a2")})

	for _, c := range []struct {
		wp, name     string
		p            *channelPrincipal
		method, path string
		body         any
		want         []int
	}{
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
