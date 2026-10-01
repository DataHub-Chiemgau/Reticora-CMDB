package rls_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// Fixture ids of the global row test (WP-024).
const (
	grOrgA       = "610b0240-0000-4000-8000-00000000000a"
	grOrgB       = "610b0240-0000-4000-8000-00000000000b"
	grTypeA      = "610b0240-0000-4000-8000-0000000000a1"
	grTypeB      = "610b0240-0000-4000-8000-0000000000b1"
	grTeamA      = "610b0240-0000-4000-8000-0000000000a2"
	grTeamB      = "610b0240-0000-4000-8000-0000000000b2"
	grCustomA    = "610b0240-0000-4000-8000-0000000000a3"
	grCustomB    = "610b0240-0000-4000-8000-0000000000b3"
	grUserA      = "610b0240-0000-4000-8000-0000000000a4"
	grUserB      = "610b0240-0000-4000-8000-0000000000b4"
	grGlobalType = "610b0240-0000-4000-8000-0000000000c1"
)

// errRollback makes database.WithTenant roll back after a check, so a policy
// regression can never leave a changed global row behind.
var errRollback = errors.New("rollback")

// TestGlobalRowsAreReadOnly covers TEN-02/TEN-05 and E-10: the application role
// reads global catalog rows but cannot insert, change or delete them; tables
// that got an organization column derive it from their parent row.
func TestGlobalRowsAreReadOnly(t *testing.T) {
	ctx := context.Background()
	dsn := testDatabaseURL(t)
	admin, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		t.Fatalf("maintenance pool: %v", err)
	}
	t.Cleanup(admin.Close)
	cleanup := func() {
		for _, sql := range []string{
			`DELETE FROM ci_type WHERE id = '` + grGlobalType + `'`,
			`DELETE FROM team WHERE organization_id IN ('` + grOrgA + `', '` + grOrgB + `')`,
			`DELETE FROM custom_role WHERE organization_id IN ('` + grOrgA + `', '` + grOrgB + `')`,
			`DELETE FROM organization WHERE id IN ('` + grOrgA + `', '` + grOrgB + `')`,
		} {
			if _, delErr := admin.Exec(ctx, sql); delErr != nil {
				t.Errorf("cleanup %q: %v", sql, delErr)
			}
		}
	}
	cleanup()
	t.Cleanup(cleanup)
	for _, sql := range []string{
		`INSERT INTO organization (id, name, slug) VALUES ('` + grOrgA + `', 'WP-024 A', 'wp024-a'), ('` + grOrgB + `', 'WP-024 B', 'wp024-b')`,
		`INSERT INTO ci_type (id, organization_id, name) VALUES ('` + grGlobalType + `', NULL, 'wp024-global'), ('` + grTypeA + `', '` + grOrgA + `', 'wp024-a'), ('` + grTypeB + `', '` + grOrgB + `', 'wp024-b')`,
		`INSERT INTO ci_type_attribute (ci_type_id, name, data_type) VALUES ('` + grGlobalType + `', 'global-attr', 'string'), ('` + grTypeB + `', 'b-attr', 'string')`,
		`INSERT INTO team (id, organization_id, name) VALUES ('` + grTeamA + `', '` + grOrgA + `', 'Team A'), ('` + grTeamB + `', '` + grOrgB + `', 'Team B')`,
		`INSERT INTO custom_role (id, organization_id, name) VALUES ('` + grCustomA + `', '` + grOrgA + `', 'Custom A'), ('` + grCustomB + `', '` + grOrgB + `', 'Custom B')`,
		`INSERT INTO app_user (id, organization_id, email, display_name) VALUES ('` + grUserA + `', '` + grOrgA + `', 'a@wp024.test', 'A'), ('` + grUserB + `', '` + grOrgB + `', 'b@wp024.test', 'B')`,
		`INSERT INTO team_member (team_id, user_id) VALUES ('` + grTeamB + `', '` + grUserB + `')`,
		`INSERT INTO user_custom_role (user_id, custom_role_id) VALUES ('` + grUserB + `', '` + grCustomB + `')`,
	} {
		if _, seedErr := admin.Exec(ctx, sql); seedErr != nil {
			t.Fatalf("seed %q: %v", sql, seedErr)
		}
	}

	// The trigger derives organization_id from the parent row.
	var attrOrg *string
	if err = admin.QueryRow(ctx, `SELECT organization_id::text FROM ci_type_attribute WHERE ci_type_id = $1`, grGlobalType).Scan(&attrOrg); err != nil || attrOrg != nil {
		t.Fatalf("attribute of a global type must have organization_id NULL, got %v (%v)", attrOrg, err)
	}
	var memberOrg string
	if err = admin.QueryRow(ctx, `SELECT organization_id::text FROM team_member WHERE team_id = $1`, grTeamB).Scan(&memberOrg); err != nil || memberOrg != grOrgB {
		t.Fatalf("team_member.organization_id = %q (%v), want %s", memberOrg, err, grOrgB)
	}

	pool, err := database.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("application pool: %v", err)
	}
	t.Cleanup(pool.Close)
	scopeA := database.OrgWideScope(grOrgA, grUserA)

	affected := func(t *testing.T, sql string, args ...any) int64 {
		t.Helper()
		var n int64
		err := database.WithTenant(ctx, pool, &scopeA, func(ctx context.Context, tx pgx.Tx) error {
			tag, execErr := tx.Exec(ctx, sql, args...)
			if execErr != nil {
				return execErr
			}
			n = tag.RowsAffected()
			return errRollback
		})
		if !errors.Is(err, errRollback) {
			t.Fatalf("%s: %v", sql, err)
		}
		return n
	}
	rejected := func(t *testing.T, sql string, args ...any) {
		t.Helper()
		err := database.WithTenant(ctx, pool, &scopeA, func(ctx context.Context, tx pgx.Tx) error {
			if _, execErr := tx.Exec(ctx, sql, args...); execErr != nil {
				return execErr
			}
			return errRollback
		})
		if err == nil || errors.Is(err, errRollback) {
			t.Fatalf("%s must be rejected", sql)
		}
	}
	count := func(t *testing.T, sql string, args ...any) int {
		t.Helper()
		var n int
		if err := database.WithTenant(ctx, pool, &scopeA, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, sql, args...).Scan(&n)
		}); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return n
	}

	globalTables := []struct{ table, column string }{
		{"ci_type", "name"},
		{"lifecycle_definition", "name"},
		{"lifecycle_state", "label"},
		{"lifecycle_transition", "label"},
		{"relationship_type", "forward_label"},
	}
	for _, g := range globalTables {
		t.Run(g.table, func(t *testing.T) {
			if n := count(t, `SELECT count(*) FROM `+g.table+` WHERE organization_id IS NULL`); n == 0 {
				t.Fatalf("global rows of %s must be readable", g.table)
			}
			if n := affected(t, `UPDATE `+g.table+` SET `+g.column+` = `+g.column+` WHERE organization_id IS NULL`); n != 0 {
				t.Fatalf("update reached %d global rows", n)
			}
			if n := affected(t, `DELETE FROM `+g.table+` WHERE organization_id IS NULL`); n != 0 {
				t.Fatalf("delete reached %d global rows", n)
			}
		})
	}
	t.Run("ci_type insert", func(t *testing.T) {
		rejected(t, `INSERT INTO ci_type (organization_id, name) VALUES (NULL, 'wp024-new-global')`)
		if n := affected(t, `INSERT INTO ci_type (organization_id, name) VALUES ($1, 'wp024-own')`, grOrgA); n != 1 {
			t.Fatalf("own ci_type insert affected %d rows", n)
		}
	})
	t.Run("permission is a read-only global catalogue", func(t *testing.T) {
		if n := count(t, `SELECT count(*) FROM permission`); n == 0 {
			t.Fatal("permission catalogue must be readable")
		}
		rejected(t, `INSERT INTO permission (key, resource, action, description) VALUES ('wp024:test', 'wp024', 'test', 'x')`)
		rejected(t, `UPDATE permission SET description = description`)
		rejected(t, `DELETE FROM permission WHERE key = 'ci:read'`)
	})
	t.Run("ci_type_attribute", func(t *testing.T) {
		if n := count(t, `SELECT count(*) FROM ci_type_attribute WHERE ci_type_id = $1`, grGlobalType); n != 1 {
			t.Fatalf("attributes of global types must be readable, got %d", n)
		}
		if n := count(t, `SELECT count(*) FROM ci_type_attribute WHERE ci_type_id = $1`, grTypeB); n != 0 {
			t.Fatalf("attributes of another organization are visible: %d", n)
		}
		if n := affected(t, `UPDATE ci_type_attribute SET name = name WHERE ci_type_id = $1`, grGlobalType); n != 0 {
			t.Fatalf("update reached %d attributes of a global type", n)
		}
		rejected(t, `INSERT INTO ci_type_attribute (ci_type_id, name, data_type) VALUES ($1, 'new', 'string')`, grGlobalType)
		// organization_id cannot be chosen: the trigger takes it from the type.
		if n := affected(t, `INSERT INTO ci_type_attribute (ci_type_id, name, data_type, organization_id) VALUES ($1, 'own', 'string', $2)`, grTypeA, grOrgB); n != 1 {
			t.Fatalf("own attribute insert affected %d rows", n)
		}
	})
	t.Run("team_member and user_custom_role", func(t *testing.T) {
		if n := count(t, `SELECT count(*) FROM team_member`); n != 0 {
			t.Fatalf("team members of another organization are visible: %d", n)
		}
		if n := count(t, `SELECT count(*) FROM user_custom_role`); n != 0 {
			t.Fatalf("custom role assignments of another organization are visible: %d", n)
		}
		rejected(t, `INSERT INTO team_member (team_id, user_id) VALUES ($1, $2)`, grTeamB, grUserA)
		rejected(t, `INSERT INTO user_custom_role (user_id, custom_role_id) VALUES ($1, $2)`, grUserA, grCustomB)
		if n := affected(t, `INSERT INTO team_member (team_id, user_id) VALUES ($1, $2)`, grTeamA, grUserA); n != 1 {
			t.Fatalf("own team member insert affected %d rows", n)
		}
		if n := affected(t, `INSERT INTO user_custom_role (user_id, custom_role_id) VALUES ($1, $2)`, grUserA, grCustomA); n != 1 {
			t.Fatalf("own custom role insert affected %d rows", n)
		}
	})
}
