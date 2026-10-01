// Package scopetest seeds the tenant fixture of the scope integration tests
// (TEN-06): organization A with two clients and an unrelated organization B.
// The WithTenant migration WPs use it to show that a client-scoped principal
// neither sees nor changes objects of another client.
//
// The package is imported by tests only. It needs a migrated PostgreSQL in
// TEST_DATABASE_URL; without one, Seed skips the test.
package scopetest

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Fixture holds the seeded ids and two pools: Admin is a maintenance
// connection that is not subject to RLS (for seeding and checks), App is the
// production pool whose connections run as the restricted application role.
type Fixture struct {
	Admin   *pgxpool.Pool
	App     *pgxpool.Pool
	OrgA    string
	OrgB    string
	Client1 string
	Client2 string
	User    string
	tag     string
	seq     int
}

// Seed creates the fixture. tag (two hex digits, unique per test package)
// keeps ids and slugs apart from the other packages sharing the database.
// Earlier leftovers are removed first and everything is removed again when
// the test ends.
func Seed(t *testing.T, tag string) *Fixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	if len(tag) != 2 {
		t.Fatalf("scopetest: tag %q must be two hex digits", tag)
	}
	ctx := context.Background()

	admin, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		t.Fatalf("maintenance pool: %v", err)
	}
	t.Cleanup(admin.Close)

	f := &Fixture{
		Admin:   admin,
		OrgA:    f0(tag, "a", 1),
		OrgB:    f0(tag, "b", 2),
		Client1: f0(tag, "a", 0xc1),
		Client2: f0(tag, "a", 0xc2),
		User:    f0(tag, "a", 0xf1),
		tag:     tag,
	}
	cleanup := func() {
		// Most rows go with their organization (ON DELETE CASCADE); tables
		// whose organization reference does not cascade are emptied first.
		// They may reference each other, so a failed delete is retried in
		// the next pass.
		pending := nonCascadingTables(t, admin)
		for pass := 0; pass < len(pending)+1 && len(pending) > 0; pass++ {
			var left []string
			for _, table := range pending {
				if _, delErr := admin.Exec(ctx, `DELETE FROM `+table+` WHERE organization_id IN ($1::uuid, $2::uuid)`, f.OrgA, f.OrgB); delErr != nil {
					left = append(left, table)
				}
			}
			pending = left
		}
		if _, delErr := admin.Exec(ctx, `DELETE FROM organization WHERE id IN ($1::uuid, $2::uuid)`, f.OrgA, f.OrgB); delErr != nil {
			t.Errorf("scopetest cleanup: %v", delErr)
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	for _, s := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organization (id, name, slug) VALUES ($1, $2, $2)`, []any{f.OrgA, "scopetest-" + tag + "-a"}},
		{`INSERT INTO organization (id, name, slug) VALUES ($1, $2, $2)`, []any{f.OrgB, "scopetest-" + tag + "-b"}},
		{`INSERT INTO client (id, organization_id, name, slug) VALUES ($1, $2, 'Client 1', $3)`, []any{f.Client1, f.OrgA, "scopetest-" + tag + "-c1"}},
		{`INSERT INTO client (id, organization_id, name, slug) VALUES ($1, $2, 'Client 2', $3)`, []any{f.Client2, f.OrgA, "scopetest-" + tag + "-c2"}},
	} {
		if _, seedErr := admin.Exec(ctx, s.sql, s.args...); seedErr != nil {
			t.Fatalf("seed %q: %v", s.sql, seedErr)
		}
	}

	app, err := database.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("application pool: %v", err)
	}
	t.Cleanup(app.Close)
	f.App = app
	return f
}

// nonCascadingTables lists the tables whose foreign key to organization does
// not cascade on delete.
func nonCascadingTables(t *testing.T, admin *pgxpool.Pool) []string {
	t.Helper()
	rows, err := admin.Query(context.Background(), `
		SELECT DISTINCT quote_ident(c.relname) FROM pg_constraint k
		JOIN pg_class c ON c.oid = k.conrelid
		WHERE k.contype = 'f' AND k.confrelid = 'organization'::regclass AND k.confdeltype <> 'c'
		ORDER BY 1`)
	if err != nil {
		t.Fatalf("list non-cascading tables: %v", err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatalf("scan table: %v", err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list non-cascading tables: %v", err)
	}
	return tables
}

// f0 builds a fixture id: c0de<tag><org>… keeps every package's ids apart.
func f0(tag, org string, n int) string {
	return fmt.Sprintf("c0de%s%s0-0000-4000-8000-%012x", tag, org, n)
}

// ID returns a new fixture id in organization A's id range for rows the test
// inserts itself.
func (f *Fixture) ID() string {
	f.seq++
	return f0(f.tag, "a", 0x1000+f.seq)
}

// ClientCtx returns a request context of a principal of organization A that
// is restricted to clientID (sites and teams unrestricted).
func (f *Fixture) ClientCtx(clientID string) context.Context {
	scope := database.OrgWideScope(f.OrgA, f.User)
	scope.Clients = database.ScopeIDs(clientID)
	return database.ContextWithTenantScope(context.Background(), &scope)
}

// OrgCtx returns a request context of an org-wide principal of orgID.
func (f *Fixture) OrgCtx(orgID string) context.Context {
	scope := database.OrgWideScope(orgID, f.User)
	return database.ContextWithTenantScope(context.Background(), &scope)
}

// CI inserts an active CI of the global "server" type through the maintenance
// pool and returns its id. clientID may be empty for an org-wide CI.
func (f *Fixture) CI(t *testing.T, orgID, clientID, name string) string {
	t.Helper()
	ctx := context.Background()
	var typeID string
	if err := f.Admin.QueryRow(ctx, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1`).Scan(&typeID); err != nil {
		t.Fatalf("lookup server ci_type: %v", err)
	}
	id := f.ID()
	var client any
	if clientID != "" {
		client = clientID
	}
	if _, err := f.Admin.Exec(ctx,
		`INSERT INTO ci (id, organization_id, client_id, ci_type_id, name, status) VALUES ($1, $2, $3, $4, $5, 'active')`,
		id, orgID, client, typeID, name); err != nil {
		t.Fatalf("insert ci %s: %v", name, err)
	}
	return id
}
