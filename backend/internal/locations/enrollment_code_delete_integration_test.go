package locations_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// TestSiteDeleteKeepsCollectorEnrollmentCodes covers WP-701 (F-430; LOC-11,
// E-71, COL-04): a collector enrollment code bound to a site (000187) keeps
// the site like an agent enrollment token. While a code that is neither used
// nor expired is bound to it, DELETE /sites and /locations answer 409
// [collector_enrollment_code] and the code stays (RESTRICT since 000189);
// expired and used codes go with the site (204, counted in the audit entry);
// a refused delete keeps them. A code the deleting principal cannot see
// refuses the delete through the foreign key. Deleting the client still
// removes its sites with their codes (DELETE /clients has no dependency
// rule).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSiteDeleteKeepsCollectorEnrollmentCodes(t *testing.T) {
	l := newAuditT3Locations(t, "fa")
	site := func(clientID, name string) string {
		t.Helper()
		return mustCreate(t, l.repo, l.ctx, l.f.OrgA, locations.CreateRequest{Kind: locations.KindSite, ClientID: clientID, Name: name}).ID
	}
	// code binds an enrollment code to siteID as POST
	// /collectors/enrollment-codes does: valid for ttl, used when used is
	// set.
	code := func(siteID, ttl string, used bool) {
		t.Helper()
		l.exec(`INSERT INTO collector_enrollment_code (organization_id, code_hash, label, expires_at, used_at, binding_recorded, site_id)
			VALUES ($1, $2, 'wp701', now() + $3::interval, CASE WHEN $4 THEN now() END, true, $5)`,
			l.f.OrgA, "wp701-"+l.f.ID(), ttl, used, siteID)
	}
	codes := func(siteID string) int {
		return l.count(`SELECT count(*) FROM collector_enrollment_code WHERE site_id = $1`, siteID)
	}
	// The kind of DependencyProblem (locations.DependencyEnrollmentCode).
	const kind = "collector_enrollment_code"

	// The finding: a code that can still enroll a collector keeps the site,
	// through both delete paths.
	live := site(l.f.Client1, "wp701-live")
	code(live, "1 day", false)
	for _, route := range []string{"/api/v1/sites/", "/api/v1/locations/"} {
		if status, deps := l.del(route + live); status != http.StatusConflict || !slices.Equal(deps, []string{kind}) {
			t.Errorf("DELETE %s{site with an unused, unexpired enrollment code} = %d %v, want 409 [%s]", route, status, deps, kind)
		}
	}
	if n := codes(live); n != 1 || l.nodes(live) != 1 {
		t.Errorf("enrollment codes at the site after the refused deletes: %d, site rows %d; want 1 and 1 (silent loss of the code)", n, l.nodes(live))
	}

	// Expired and used codes do not count and go with the site; the audit
	// entry counts them. The codes of another site stay.
	other := site(l.f.Client1, "wp701-other")
	code(other, "-1 minute", false)
	code(other, "1 day", true)
	code(other, "1 day", false)
	for _, c := range []struct {
		name, ttl string
		used      bool
	}{
		{"expired", "-1 minute", false},
		{"used", "1 day", true},
	} {
		for _, route := range []string{"/api/v1/locations/", "/api/v1/sites/"} {
			id := site(l.f.Client1, "wp701-"+c.name+strings.ReplaceAll(route, "/", "-"))
			code(id, c.ttl, c.used)
			if status, deps := l.del(route + id); status != http.StatusNoContent {
				t.Errorf("DELETE %s{site with only a %s enrollment code} = %d %v, want 204", route, c.name, status, deps)
			}
			if n := codes(id); n != 0 || l.nodes(id) != 0 {
				t.Errorf("%s: %s enrollment codes left %d, site rows %d; want both removed", route, c.name, n, l.nodes(id))
			}
			if n := l.count(`SELECT count(*) FROM audit_log WHERE organization_id = $1 AND action = 'location.deleted' AND resource_id = $2
				AND changes->'removed' = '{"collector_enrollment_code": 1}'::jsonb`, l.f.OrgA, id); n != 1 {
				t.Errorf("%s: location.deleted entries naming the removed %s code: %d, want 1", route, c.name, n)
			}
		}
	}
	if n := codes(other); n != 3 {
		t.Errorf("enrollment codes of another site after the deletes: %d, want 3", n)
	}

	// A refused delete keeps the lapsed codes: removal and delete are one
	// transaction.
	mixed := site(l.f.Client1, "wp701-mixed")
	code(mixed, "1 day", false)
	code(mixed, "-1 hour", false)
	code(mixed, "1 day", true)
	if status, deps := l.del("/api/v1/locations/" + mixed); status != http.StatusConflict || !slices.Equal(deps, []string{kind}) || codes(mixed) != 3 {
		t.Errorf("DELETE /api/v1/locations/{site with a live, an expired and a used code} = %d %v, codes left %d; want 409 [%s] and 3",
			status, deps, codes(mixed), kind)
	}

	// A code the principal cannot see: bound to the site, but carrying
	// client 2 (fixture past the binding trigger), so the SELECT policy of a
	// client 1 principal hides it. The foreign key refuses the delete.
	hidden := site(l.f.Client1, "wp701-hidden")
	conn, err := l.f.Admin.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []struct {
		sql  string
		args []any
	}{
		{`SET session_replication_role = replica`, nil},
		{`INSERT INTO collector_enrollment_code (organization_id, code_hash, expires_at, binding_recorded, client_id, site_id)
			VALUES ($1, 'wp701-hidden', now() + interval '1 day', true, $2, $3)`, []any{l.f.OrgA, l.f.Client2, hidden}},
		{`SET session_replication_role = origin`, nil},
	} {
		if _, err = conn.Exec(context.Background(), s.sql, s.args...); err != nil {
			conn.Release()
			t.Fatalf("%s: %v", s.sql, err)
		}
	}
	conn.Release()
	restricted := l.f.AppUser(t, l.f.OrgA, "wp701-client1")
	scope := database.OrgWideScope(l.f.OrgA, restricted)
	scope.Clients = database.ScopeIDs(l.f.Client1)
	orgWide := l.ctx
	l.ctx = tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope), tenant.TenantInfo{OrganizationID: l.f.OrgA, UserID: restricted})
	var visible int
	if err = database.WithRequestTenant(l.ctx, l.f.App, l.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM collector_enrollment_code WHERE site_id = $1`, hidden).Scan(&visible)
	}); err != nil || visible != 0 {
		t.Fatalf("precondition: the client 1 principal does not see the code: %d, %v", visible, err)
	}
	if status, deps := l.del("/api/v1/sites/" + hidden); status != http.StatusConflict || !slices.Equal(deps, []string{kind}) {
		t.Errorf("DELETE /api/v1/sites/{site with an enrollment code the caller cannot see} = %d %v, want 409 [%s]", status, deps, kind)
	}
	if n := codes(hidden); n != 1 || l.nodes(hidden) != 1 {
		t.Errorf("hidden enrollment code after the refused delete: %d, site rows %d; want 1 and 1", n, l.nodes(hidden))
	}
	l.ctx = orgWide

	// Deleting the client removes its sites and their codes as before
	// (collector_enrollment_code_client_fkey stays CASCADE).
	client := l.f.ID()
	l.exec(`INSERT INTO client (id, organization_id, name, slug) VALUES ($1, $2, 'wp701 client', $3)`, client, l.f.OrgA, "wp701-"+client)
	clientSite := site(client, "wp701-client-site")
	code(clientSite, "1 day", false)
	if status, deps := l.del("/api/v1/clients/" + client); status != http.StatusNoContent {
		t.Errorf("DELETE /api/v1/clients/{client with a site and a live enrollment code} = %d %v, want 204", status, deps)
	}
	if n := codes(clientSite) + l.nodes(clientSite); n != 0 {
		t.Errorf("site and enrollment code after deleting the client: %d rows left, want 0", n)
	}
}
