package rls_test

// Phase-1 acceptance audit, group T1-tenancy: shared fixture of the audit
// tests in this package (regression tests and the defect tests behind the
// auditdefect build tag).
//
// WPs: WP-008, WP-009, WP-010 .. WP-021, WP-025, WP-027, WP-028, WP-034,
// WP-040, WP-208. Requirements: TEN-04, TEN-05, TEN-06, TEN-09, TST-02.
//
// A t1World is the scopetest fixture (organization A with clients 1 and 2,
// organization B) behind the complete PostgreSQL router of newChannelEnv,
// with one org_admin user per organization. Requests carry a fixed
// principal whose scope the test chooses (TEN-04), so every assertion runs
// through router, authorization, handler, repository and RLS.
//
// Retest (regression tests of this group; add -tags auditdefect for the
// defect tests):
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -count=1 -v -run 'TestAuditT1' ./internal/tenant/rls/

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

type t1World struct {
	t            *testing.T
	f            *scopetest.Fixture
	env          *channelEnv
	userA, userB string
	orgA, orgB   *channelPrincipal
}

// newT1World seeds the fixture with tag (two hex digits, unique in the
// repository) and wires the router.
func newT1World(t *testing.T, tag string) *t1World {
	t.Helper()
	f := scopetest.Seed(t, tag)
	w := &t1World{t: t, f: f, env: newChannelEnv(t, f)}
	w.userA = f.AppUser(t, f.OrgA, "t1-a")
	w.userB = f.AppUser(t, f.OrgB, "t1-b")
	// Channels that resolve grants from the database (search, saved views)
	// see the users as administrators; the scope comes from the principal.
	for org, user := range map[string]string{f.OrgA: w.userA, f.OrgB: w.userB} {
		if _, err := f.Admin.Exec(context.Background(), `INSERT INTO role_assignment (organization_id, user_id, role_id)
			SELECT $1, $2, id FROM role WHERE organization_id = $1 AND name = 'org_admin'`, org, user); err != nil {
			t.Fatalf("role assignment: %v", err)
		}
	}
	w.orgA = &channelPrincipal{kind: "org", org: f.OrgA, user: w.userA, scope: identity.Scope{Clients: all(), Sites: all(), Teams: all()}}
	w.orgB = &channelPrincipal{kind: "other-org", org: f.OrgB, user: w.userB, scope: identity.Scope{Clients: all(), Sites: all(), Teams: all()}}
	return w
}

// as returns a principal of organization A with the given client and site
// scope (teams unrestricted).
func (w *t1World) as(kind string, clients, sites identity.ScopeSet) *channelPrincipal {
	return &channelPrincipal{kind: kind, org: w.f.OrgA, user: w.userA, scope: identity.Scope{Clients: clients, Sites: sites, Teams: all()}}
}

// do sends one request through the complete router.
func (w *t1World) do(p *channelPrincipal, method, path string, body any) (int, string) {
	w.t.Helper()
	return w.env.call(p, method, path, body)
}

// create sends a POST that must answer 201 and returns the new id.
func (w *t1World) create(p *channelPrincipal, path string, body any) string {
	w.t.Helper()
	code, resp := w.env.call(p, http.MethodPost, path, body)
	var out struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(resp), &out)
	if code != http.StatusCreated || out.ID == "" {
		w.t.Fatalf("setup POST %s as %s: %d %s", path, p.kind, code, trim(resp))
	}
	return out.ID
}

// scalar runs a query on the maintenance pool (no RLS) and returns the
// single value as text.
func (w *t1World) scalar(query string, args ...any) string {
	w.t.Helper()
	var v string
	if err := w.f.Admin.QueryRow(context.Background(), query, args...).Scan(&v); err != nil {
		w.t.Fatalf("%s: %v", query, err)
	}
	return v
}

// refused reports whether a write was refused: 403 or 404 (TEN-05: a
// write outside the scope is rejected).
func refused(code int) bool { return code == http.StatusForbidden || code == http.StatusNotFound }

// siteOfRoom returns the site of a room seeded by scopetest.Room.
func (w *t1World) siteOfRoom(room string) string {
	w.t.Helper()
	return w.scalar(`SELECT b.site_id::text FROM room r JOIN building b ON b.id = r.building_id WHERE r.id = $1`, room)
}
