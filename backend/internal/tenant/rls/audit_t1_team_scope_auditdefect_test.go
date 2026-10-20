//go:build auditdefect

package rls_test

// Phase-1 acceptance audit, group T1-tenancy: DEFECT tests (they fail until
// the product is fixed).
//
// WP-009 (RBA-03, TEN-04, TEN-05; CH25, E-11) P2 "Team scope can never be
// granted: role assignments have no team dimension, so app.team_scope is
// always 'all' for real users": RBA-03 defines
// role_assignment(role, scope_kind org|client|site|team, scope_id), and
// WP-009 AC1 requires EffectivePermissions to deliver the scope set per
// dimension including teams. role_assignment has only scope_client_id and
// scope_site_id, user.assignStandardRole rejects scope_type 'team' and
// permission.AccessGrants never fills Grant.Teams, so the team predicate of
// the ticket policies restricts only synthetic test principals.
//
// The review_item defect of WP-025 (F-003) is fixed (WP-342); its test runs
// in the regular suite as TestReviewItemRowsCarryClientScope
// (review_item_scope_integration_test.go). metric_sample: see
// internal/monitoring TestAuditT1MetricViewsHonourScope.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -tags auditdefect -count=1 -v -run '^TestAuditT1TeamScopedAssignment$' ./internal/tenant/rls/

import (
	"context"
	"net/http"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/jackc/pgx/v5"
)

// TestAuditT1TeamScopedAssignment assigns the engineer role for team 1
// through POST /api/v1/role-assignments and resolves the user's access the
// way login and refresh do (permission.AccessGrants + identity.ResolveAccess
// + middleware.TenantScopeFor): the user must see the tickets of team 1 and
// not those of team 2.
func TestAuditT1TeamScopedAssignment(t *testing.T) {
	w := newT1World(t, "d7")
	f := w.f
	bg := context.Background()
	team1 := w.create(w.orgA, "/api/v1/teams", map[string]any{"name": "t1-team-1"})
	team2 := w.create(w.orgA, "/api/v1/teams", map[string]any{"name": "t1-team-2"})
	member := f.AppUser(t, f.OrgA, "t1-team-member")
	if _, err := f.Admin.Exec(bg, `DELETE FROM role_assignment WHERE user_id = $1`, member); err != nil {
		t.Fatal(err)
	}
	engineer := w.scalar(`SELECT id::text FROM role WHERE organization_id = $1 AND name = 'engineer'`, f.OrgA)
	ticket1 := w.scalar(`INSERT INTO ticket (organization_id, title, reporter_id, team_id) VALUES ($1, 't1-team-ticket-1', $2, $3) RETURNING id::text`, f.OrgA, w.userA, team1)
	ticket2 := w.scalar(`INSERT INTO ticket (organization_id, title, reporter_id, team_id) VALUES ($1, 't1-team-ticket-2', $2, $3) RETURNING id::text`, f.OrgA, w.userA, team2)

	code, body := w.do(w.orgA, http.MethodPost, "/api/v1/role-assignments", map[string]any{
		"user_id": member, "role_id": engineer, "scope_type": "team", "scope_id": team1,
	})
	if code != http.StatusCreated {
		t.Errorf("RBA-03: POST /role-assignments with scope_type team answered %d %s, want 201", code, trim(body))
	}

	grants, err := permission.NewPGRepository(f.App).AccessGrants(bg, f.OrgA, member)
	if err != nil {
		t.Fatalf("AccessGrants: %v", err)
	}
	access := identity.ResolveAccess(grants)
	if access.Scope.Teams.All || len(access.Scope.Teams.IDs) != 1 || access.Scope.Teams.IDs[0] != team1 {
		t.Fatalf("RBA-03/WP-009 AC1: the resolved team scope of a user assigned for team 1 is %+v (grants %+v), want exactly team 1",
			access.Scope.Teams, grants)
	}
	principal := identity.Principal{Subject: member, OrganizationID: f.OrgA, Scope: &access.Scope}
	scope := middleware.TenantScopeFor(&principal)
	seen := map[string]bool{}
	if err := database.WithTenant(bg, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM ticket WHERE id = ANY($1::uuid[])`, []string{ticket1, ticket2})
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			seen[id] = true
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if !seen[ticket1] || seen[ticket2] {
		t.Errorf("CH25/TEN-05: team-1 user sees ticket of team 1 %v, of team 2 %v; want true/false", seen[ticket1], seen[ticket2])
	}
}
