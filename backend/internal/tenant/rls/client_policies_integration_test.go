package rls_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// Fixture ids of the client policy test (WP-023); distinct from the other
// integration tests that share the database.
const (
	cpOrg     = "c11e0230-0000-4000-8000-000000000001"
	cpClient1 = "c11e0230-0000-4000-8000-0000000000c1"
	cpClient2 = "c11e0230-0000-4000-8000-0000000000c2"
	cpWindow  = "c11e0230-0000-4000-8000-0000000000e1"
	cpUser    = "c11e0230-0000-4000-8000-0000000000f1"
)

// clientPolicyTable describes how to insert a minimal row into one of the nine
// tables that received a client predicate in migration 000058.
type clientPolicyTable struct {
	name    string
	columns string // besides organization_id and client_id
	values  string // SQL literals for columns
}

var clientPolicyTables = []clientPolicyTable{
	{"asset", "asset_tag, name", "'wp023-' || gen_random_uuid(), 'Asset'"},
	{"consumable", "name", "'Consumable ' || gen_random_uuid()"},
	{"form_def", "name, schema", "'Form ' || gen_random_uuid(), '{}'::jsonb"},
	{"internal_order", "order_number, title", "'wp023-' || gen_random_uuid(), 'Order'"},
	{"key_item", "name", "'Key ' || gen_random_uuid()"},
	{"location_node", "node_type, name", "'room', 'Room ' || gen_random_uuid()"},
	{"maintenance_notification", "maintenance_window_id", "'" + cpWindow + "'"},
	{"quantity_item", "name", "'Item ' || gen_random_uuid()"},
	{"sla", "name, priority, response_target_minutes, resolution_target_minutes", "'SLA ' || gen_random_uuid(), 'low', 10, 20"},
}

func (c clientPolicyTable) insertSQL(client string) string {
	clientLiteral := "NULL"
	if client != "" {
		clientLiteral = "'" + client + "'"
	}
	return fmt.Sprintf(`INSERT INTO %s (organization_id, client_id, %s) VALUES ('%s', %s, %s) RETURNING id::text`,
		c.name, c.columns, cpOrg, clientLiteral, c.values)
}

// seedClientPolicies creates the organization with two clients and, per table,
// one row of client 1, one of client 2 and one without client. It returns the
// row ids per table and client ("" = NULL client).
func seedClientPolicies(t *testing.T, ctx context.Context, dsn string) (*pgxpool.Pool, map[string]map[string]string) {
	t.Helper()
	admin, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		t.Fatalf("maintenance pool: %v", err)
	}
	t.Cleanup(admin.Close)
	cleanup := func() {
		// asset has NO ACTION foreign keys to organization and client.
		for _, tbl := range clientPolicyTables {
			if _, delErr := admin.Exec(ctx, `DELETE FROM `+tbl.name+` WHERE organization_id = $1`, cpOrg); delErr != nil {
				t.Errorf("cleanup %s: %v", tbl.name, delErr)
			}
		}
		if _, delErr := admin.Exec(ctx, `DELETE FROM organization WHERE id = $1`, cpOrg); delErr != nil {
			t.Errorf("cleanup organization: %v", delErr)
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	for _, sql := range []string{
		`INSERT INTO organization (id, name, slug) VALUES ('` + cpOrg + `', 'WP-023 Org', 'wp023-org')`,
		`INSERT INTO client (id, organization_id, name, slug) VALUES ('` + cpClient1 + `', '` + cpOrg + `', 'Client 1', 'wp023-client-1')`,
		`INSERT INTO client (id, organization_id, name, slug) VALUES ('` + cpClient2 + `', '` + cpOrg + `', 'Client 2', 'wp023-client-2')`,
		`INSERT INTO maintenance_window (id, organization_id, title, starts_at, ends_at) VALUES ('` + cpWindow + `', '` + cpOrg + `', 'Window', now(), now() + interval '1 hour')`,
	} {
		if _, seedErr := admin.Exec(ctx, sql); seedErr != nil {
			t.Fatalf("seed %q: %v", sql, seedErr)
		}
	}
	rows := make(map[string]map[string]string, len(clientPolicyTables))
	for _, tbl := range clientPolicyTables {
		rows[tbl.name] = make(map[string]string, 3)
		for _, client := range []string{cpClient1, cpClient2, ""} {
			var id string
			if seedErr := admin.QueryRow(ctx, tbl.insertSQL(client)).Scan(&id); seedErr != nil {
				t.Fatalf("seed %s (client %q): %v", tbl.name, client, seedErr)
			}
			rows[tbl.name][client] = id
		}
	}

	pool, err := database.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("application pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, rows
}

// TestClientPoliciesEnforceClientScope covers TEN-05/CH11 for the nine tables
// of migration 000058 and E-09 (no NULL client writes without org-wide scope).
func TestClientPoliciesEnforceClientScope(t *testing.T) {
	ctx := context.Background()
	pool, rows := seedClientPolicies(t, ctx, testDatabaseURL(t))

	client1 := database.OrgWideScope(cpOrg, cpUser)
	client1.Clients = database.ScopeIDs(cpClient1)
	orgWide := database.OrgWideScope(cpOrg, cpUser)

	// run executes fn in its own tenant transaction so a rejected statement
	// does not abort the following checks.
	run := func(scope database.TenantScope, fn func(context.Context, pgx.Tx) error) error {
		return database.WithTenant(ctx, pool, &scope, fn)
	}
	affected := func(t *testing.T, scope database.TenantScope, sql string, args ...any) int64 {
		t.Helper()
		var n int64
		if err := run(scope, func(ctx context.Context, tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, sql, args...)
			n = tag.RowsAffected()
			return err
		}); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return n
	}
	rejected := func(t *testing.T, scope database.TenantScope, what, sql string, args ...any) {
		t.Helper()
		err := run(scope, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, sql, args...)
			return err
		})
		if err == nil {
			t.Fatalf("%s must be rejected by the policy", what)
		}
	}

	for _, tbl := range clientPolicyTables {
		ids := rows[tbl.name]
		t.Run(tbl.name, func(t *testing.T) {
			t.Run("select", func(t *testing.T) {
				var visible []string
				if err := run(client1, func(ctx context.Context, tx pgx.Tx) error {
					r, err := tx.Query(ctx, `SELECT id::text FROM `+tbl.name+` WHERE organization_id = $1`, cpOrg)
					if err != nil {
						return err
					}
					visible, err = pgx.CollectRows(r, pgx.RowTo[string])
					return err
				}); err != nil {
					t.Fatal(err)
				}
				seen := map[string]bool{}
				for _, id := range visible {
					seen[id] = true
				}
				if !seen[ids[cpClient1]] || !seen[ids[""]] || seen[ids[cpClient2]] || len(visible) != 2 {
					t.Fatalf("client-1 scope sees %v; want own row and shared row only", visible)
				}
			})
			t.Run("insert", func(t *testing.T) {
				rejected(t, client1, "insert into a foreign client", tbl.insertSQL(cpClient2))
				rejected(t, client1, "insert without client (E-09)", tbl.insertSQL(""))
				if err := run(client1, func(ctx context.Context, tx pgx.Tx) error {
					_, err := tx.Exec(ctx, tbl.insertSQL(cpClient1))
					return err
				}); err != nil {
					t.Fatalf("insert into own client: %v", err)
				}
				if err := run(orgWide, func(ctx context.Context, tx pgx.Tx) error {
					_, err := tx.Exec(ctx, tbl.insertSQL(""))
					return err
				}); err != nil {
					t.Fatalf("org-wide insert without client: %v", err)
				}
			})
			t.Run("update", func(t *testing.T) {
				if n := affected(t, client1, `UPDATE `+tbl.name+` SET client_id = client_id WHERE id = $1`, ids[cpClient2]); n != 0 {
					t.Fatalf("update of a foreign client row affected %d rows", n)
				}
				rejected(t, client1, "moving an own row to a foreign client", `UPDATE `+tbl.name+` SET client_id = $2 WHERE id = $1`, ids[cpClient1], cpClient2)
				rejected(t, client1, "clearing the client of an own row (E-09)", `UPDATE `+tbl.name+` SET client_id = NULL WHERE id = $1`, ids[cpClient1])
				rejected(t, client1, "updating a shared row without client (E-09)", `UPDATE `+tbl.name+` SET client_id = NULL WHERE id = $1`, ids[""])
				if n := affected(t, client1, `UPDATE `+tbl.name+` SET client_id = client_id WHERE id = $1`, ids[cpClient1]); n != 1 {
					t.Fatalf("update of an own row affected %d rows, want 1", n)
				}
			})
			t.Run("delete", func(t *testing.T) {
				if n := affected(t, client1, `DELETE FROM `+tbl.name+` WHERE id = $1`, ids[cpClient2]); n != 0 {
					t.Fatalf("delete of a foreign client row affected %d rows", n)
				}
			})
		})
	}
}
