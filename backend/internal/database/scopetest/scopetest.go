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
		// Append-only tables refuse deletes through a trigger; the rows of
		// the fixture organizations are removed with triggers disabled.
		deleteAppendOnly(t, admin, f.OrgA, f.OrgB)
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

// appendOnlyTables refuse UPDATE and DELETE through a trigger (migrations
// 000049, 000055, 000056).
var appendOnlyTables = []string{"audit_log", "asset_movement", "disposal_record"}

// deleteAppendOnly removes the fixture rows of the append-only tables under
// session_replication_role = replica, which skips their guard triggers.
func deleteAppendOnly(t *testing.T, admin *pgxpool.Pool, orgIDs ...string) {
	t.Helper()
	ctx := context.Background()
	conn, err := admin.Acquire(ctx)
	if err != nil {
		t.Errorf("scopetest cleanup: acquire: %v", err)
		return
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SET session_replication_role = replica`); err != nil {
		t.Errorf("scopetest cleanup: lift append-only guards: %v", err)
		return
	}
	defer func() {
		if _, err := conn.Exec(ctx, `SET session_replication_role = origin`); err != nil {
			t.Errorf("scopetest cleanup: restore replication role: %v", err)
		}
	}()
	for _, table := range appendOnlyTables {
		if _, err := conn.Exec(ctx, `DELETE FROM `+table+` WHERE organization_id = ANY($1::uuid[])`, orgIDs); err != nil {
			t.Errorf("scopetest cleanup: %s: %v", table, err)
		}
	}
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

// Asset inserts an asset through the maintenance pool and returns its id.
// clientID may be empty for an org-wide asset; tag must be unique.
func (f *Fixture) Asset(t *testing.T, orgID, clientID, tag string) string {
	t.Helper()
	id := f.ID()
	var client any
	if clientID != "" {
		client = clientID
	}
	if _, err := f.Admin.Exec(context.Background(),
		`INSERT INTO asset (id, organization_id, client_id, asset_tag, name) VALUES ($1, $2, $3, $4, $4)`,
		id, orgID, client, tag); err != nil {
		t.Fatalf("insert asset %s: %v", tag, err)
	}
	return id
}

// AppUser inserts an active user of orgID through the maintenance pool and
// returns its id.
func (f *Fixture) AppUser(t *testing.T, orgID, name string) string {
	t.Helper()
	id := f.ID()
	if _, err := f.Admin.Exec(context.Background(),
		`INSERT INTO app_user (id, organization_id, oidc_subject, email, display_name) VALUES ($1, $2, $3, $3 || '@scopetest.invalid', $3)`,
		id, orgID, "scopetest-"+f.tag+"-"+name); err != nil {
		t.Fatalf("insert user %s: %v", name, err)
	}
	return id
}
