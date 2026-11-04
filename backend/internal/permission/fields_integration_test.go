package permission_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
)

// Fixture ids of the field permission tests (WP-264).
const (
	fgOrg       = "26400000-0000-4000-8000-000000000001"
	fgOrgB      = "26400000-0000-4000-8000-000000000002"
	fgClient1   = "26400000-0000-4000-8000-0000000000c1"
	fgClient2   = "26400000-0000-4000-8000-0000000000c2"
	fgRole      = "26400000-0000-4000-8000-0000000000a1"
	fgRoleB     = "26400000-0000-4000-8000-0000000000a2"
	fgCustom    = "26400000-0000-4000-8000-0000000000a3"
	fgTeam      = "26400000-0000-4000-8000-0000000000e1"
	fgTeamB     = "26400000-0000-4000-8000-0000000000e2"
	fgUser      = "26400000-0000-4000-8000-0000000000f1"
	fgCI1       = "26400000-0000-4000-8000-000000000101"
	fgCI2       = "26400000-0000-4000-8000-000000000102"
	fgCI3       = "26400000-0000-4000-8000-000000000103"
	fgCIB       = "26400000-0000-4000-8000-000000000104"
	fgChildType = "26400000-0000-4000-8000-0000000000d1"
	fgTypeB     = "26400000-0000-4000-8000-0000000000d2"
)

type fieldFixture struct {
	admin, app *pgxpool.Pool
	repo       *permission.PGFieldGrants
	server     string // system ci_type "server"
	relType    string // a system relationship type
	relKey     string
	engineer   string // builtin role engineer of fgOrg
}

func seedFieldGrants(t *testing.T) (context.Context, *fieldFixture) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()
	admin, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		t.Fatalf("maintenance pool: %v", err)
	}
	t.Cleanup(admin.Close)
	orgs := `('` + fgOrg + `', '` + fgOrgB + `')`
	cleanup := func() {
		for _, sql := range []string{
			`DELETE FROM user_custom_role WHERE custom_role_id = '` + fgCustom + `'`,
			`DELETE FROM field_grant WHERE organization_id IN ` + orgs,
			`DELETE FROM custom_role WHERE organization_id IN ` + orgs,
			`DELETE FROM ci_instance_field_definition WHERE organization_id IN ` + orgs,
			`DELETE FROM ci WHERE organization_id IN ` + orgs,
			`DELETE FROM attribute_definition WHERE organization_id IN ` + orgs,
			`DELETE FROM ci_type WHERE organization_id IN ` + orgs,
			`DELETE FROM team_member WHERE organization_id IN ` + orgs,
			`DELETE FROM team WHERE organization_id IN ` + orgs,
			`DELETE FROM organization WHERE id IN ` + orgs,
		} {
			if _, delErr := admin.Exec(ctx, sql); delErr != nil {
				t.Errorf("cleanup %q: %v", sql, delErr)
			}
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	f := &fieldFixture{admin: admin}
	if err = admin.QueryRow(ctx, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&f.server); err != nil {
		t.Fatalf("system type server: %v", err)
	}
	if err = admin.QueryRow(ctx, `SELECT id::text, key FROM relationship_type WHERE organization_id IS NULL ORDER BY key LIMIT 1`).Scan(&f.relType, &f.relKey); err != nil {
		t.Fatalf("system relationship type: %v", err)
	}
	stmts := []string{
		`INSERT INTO organization (id, name, slug) VALUES ('` + fgOrg + `', 'WP-264 Org', 'wp264-org')`,
		`INSERT INTO organization (id, name, slug) VALUES ('` + fgOrgB + `', 'WP-264 Org B', 'wp264-org-b')`,
		`INSERT INTO client (id, organization_id, name, slug) VALUES ('` + fgClient1 + `', '` + fgOrg + `', 'Client 1', 'wp264-client-1')`,
		`INSERT INTO client (id, organization_id, name, slug) VALUES ('` + fgClient2 + `', '` + fgOrg + `', 'Client 2', 'wp264-client-2')`,
		`INSERT INTO role (id, organization_id, name, permissions) VALUES ('` + fgRole + `', '` + fgOrg + `', 'wp264-role', '["ci:read","ci:write"]')`,
		`INSERT INTO role (id, organization_id, name, permissions) VALUES ('` + fgRoleB + `', '` + fgOrgB + `', 'wp264-role-b', '["ci:read"]')`,
		`INSERT INTO custom_role (id, organization_id, name, permissions) VALUES ('` + fgCustom + `', '` + fgOrg + `', 'wp264-custom', '["ci:read","ci:write"]')`,
		`INSERT INTO team (id, organization_id, name) VALUES ('` + fgTeam + `', '` + fgOrg + `', 'WP-264 Team')`,
		`INSERT INTO team (id, organization_id, name) VALUES ('` + fgTeamB + `', '` + fgOrgB + `', 'WP-264 Team B')`,
		`INSERT INTO app_user (id, organization_id, email, display_name) VALUES ('` + fgUser + `', '` + fgOrg + `', 'wp264@example.test', 'WP-264')`,
		`INSERT INTO team_member (team_id, user_id) VALUES ('` + fgTeam + `', '` + fgUser + `')`,
		`INSERT INTO user_custom_role (user_id, custom_role_id, scope_type) VALUES ('` + fgUser + `', '` + fgCustom + `', 'organization')`,
		`INSERT INTO ci_type (id, organization_id, key, name, parent_type_id) VALUES ('` + fgChildType + `', '` + fgOrg + `', 'wp264-child', 'Child', '` + f.server + `')`,
		`INSERT INTO ci_type (id, organization_id, key, name, parent_type_id) VALUES ('` + fgTypeB + `', '` + fgOrgB + `', 'wp264-b', 'B', '` + f.server + `')`,
		`INSERT INTO attribute_definition (organization_id, key, data_type, display_name, scope)
		 VALUES ('` + fgOrg + `', 'wp264_attr', 'string', '{"de": "A", "en": "A"}', 'global')`,
	}
	for _, c := range []struct{ id, org, client string }{
		{fgCI1, fgOrg, fgClient1}, {fgCI2, fgOrg, fgClient1}, {fgCI3, fgOrg, fgClient2}, {fgCIB, fgOrgB, ""},
	} {
		stmts = append(stmts, `INSERT INTO ci (id, organization_id, client_id, ci_type_id, name, status)
			VALUES ('`+c.id+`', '`+c.org+`', NULLIF('`+c.client+`', '')::uuid, '`+f.server+`', 'wp264-`+c.id[len(c.id)-3:]+`', 'active')`)
	}
	stmts = append(stmts, `INSERT INTO ci_instance_field_definition (organization_id, ci_id, name, data_type)
		VALUES ('`+fgOrg+`', '`+fgCI1+`', 'wp264_inst', 'string')`)
	for _, sql := range stmts {
		if _, seedErr := admin.Exec(ctx, sql); seedErr != nil {
			t.Fatalf("seed %q: %v", sql, seedErr)
		}
	}
	if err = admin.QueryRow(ctx, `SELECT id::text FROM role WHERE organization_id = $1 AND name = 'engineer' AND is_builtin`, fgOrg).Scan(&f.engineer); err != nil {
		t.Fatalf("builtin role engineer: %v", err)
	}
	if f.app, err = database.NewPool(ctx, scopetest.AppDSN(t)); err != nil {
		t.Fatalf("application pool: %v", err)
	}
	t.Cleanup(f.app.Close)
	f.repo = permission.NewPGFieldGrants(f.app)
	return ctx, f
}

// orgCtx is the request context of an org-wide principal of org.
func orgCtx(ctx context.Context, org string) context.Context {
	scope := database.OrgWideScope(org, fgUser)
	return database.ContextWithTenantScope(ctx, &scope)
}

// clientCtx is the request context of a principal confined to client 1.
func clientCtx(ctx context.Context) context.Context {
	scope := database.TenantScope{OrgID: fgOrg, UserID: fgUser, Clients: database.ScopeIDs(fgClient1),
		Sites: database.AllScopes(), Teams: database.AllScopes()}
	return database.ContextWithTenantScope(ctx, &scope)
}

func sp(s string) *string { return &s }

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// TestFieldGrantRepository covers WP-264 (RBA-07, MET-10) on PostgreSQL:
// CRUD and lists of the three tables, the isolation of other
// organizations (RLS and the composite foreign keys), system types as
// target, unknown field keys, duplicates, the delete trigger, the type
// chain, team memberships and the export with keys.
func TestFieldGrantRepository(t *testing.T) {
	ctx, f := seedFieldGrants(t)
	a, b := orgCtx(ctx, fgOrg), orgCtx(ctx, fgOrgB)
	repo := f.repo

	t.Run("field grant CRUD and list", func(t *testing.T) {
		g, err := repo.CreateFieldGrant(a, fgOrg, &permission.FieldGrant{RoleID: sp(fgRole), ObjectKind: permission.KindCI,
			FieldKey: sp("core.hostname"), Level: permission.LevelNone, CreatedBy: sp(fgUser)})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if got, getErr := repo.GetFieldGrant(a, fgOrg, g.ID); getErr != nil || *got.FieldKey != "core.hostname" {
			t.Fatalf("get: %+v, %v", got, getErr)
		}
		up, err := repo.UpdateFieldGrantLevel(a, fgOrg, g.ID, permission.LevelRead)
		if err != nil || up.Level != permission.LevelRead {
			t.Fatalf("update: %+v, %v", up, err)
		}
		if _, err = repo.CreateFieldGrant(a, fgOrg, &permission.FieldGrant{RoleID: sp(fgRole), ObjectKind: permission.KindCI,
			FieldKey: sp("core.hostname"), Level: permission.LevelWrite}); pgCode(err) != "23505" {
			t.Errorf("duplicate: %v, want 23505", err)
		}
		// A system type is a valid target.
		typed, err := repo.CreateFieldGrant(a, fgOrg, &permission.FieldGrant{CustomRoleID: sp(fgCustom), ObjectKind: permission.KindCI,
			CITypeID: sp(f.server), Level: permission.LevelRead})
		if err != nil {
			t.Fatalf("grant on the system type: %v", err)
		}
		items, next, err := repo.ListFieldGrants(a, fgOrg, &permission.FieldGrantFilter{}, api.PaginationParams{Limit: 1})
		if err != nil || len(items) != 1 || next == "" {
			t.Fatalf("first page: %d items, next %q, %v", len(items), next, err)
		}
		cursor, err := api.DecodeCursor(next)
		if err != nil {
			t.Fatal(err)
		}
		rest, last, err := repo.ListFieldGrants(a, fgOrg, &permission.FieldGrantFilter{}, api.PaginationParams{Limit: 1, Cursor: &cursor})
		if err != nil || len(rest) != 1 || last != "" || rest[0].ID == items[0].ID {
			t.Fatalf("second page: %+v, next %q, %v", rest, last, err)
		}
		filtered, _, err := repo.ListFieldGrants(a, fgOrg, &permission.FieldGrantFilter{CustomRoleID: fgCustom}, api.PaginationParams{})
		if err != nil || len(filtered) != 1 || filtered[0].ID != typed.ID {
			t.Errorf("filter custom_role_id: %+v, %v", filtered, err)
		}
		if err = repo.DeleteFieldGrant(a, fgOrg, typed.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = repo.GetFieldGrant(a, fgOrg, typed.ID); !errors.Is(err, api.ErrNotFound) {
			t.Errorf("get deleted: %v", err)
		}
		// Organization B sees nothing of organization A.
		if foreign, _, listErr := repo.ListFieldGrants(b, fgOrgB, &permission.FieldGrantFilter{}, api.PaginationParams{}); listErr != nil || len(foreign) != 0 {
			t.Errorf("organization B lists %d grants (%v)", len(foreign), listErr)
		}
		if _, err = repo.GetFieldGrant(b, fgOrgB, g.ID); !errors.Is(err, api.ErrNotFound) {
			t.Errorf("organization B reads the grant of A: %v", err)
		}
		if err = repo.DeleteFieldGrant(b, fgOrgB, g.ID); !errors.Is(err, api.ErrNotFound) {
			t.Errorf("organization B deletes the grant of A: %v", err)
		}
		if _, err = repo.GetFieldGrant(b, fgOrg, g.ID); !errors.Is(err, database.ErrTenantMismatch) {
			t.Errorf("organization of another scope: %v", err)
		}
	})

	t.Run("references", func(t *testing.T) {
		cases := []struct {
			name string
			g    permission.FieldGrant
			want func(error) bool
		}{
			{"role of another organization", permission.FieldGrant{RoleID: sp(fgRoleB), ObjectKind: permission.KindCI, Level: permission.LevelNone},
				func(err error) bool { return pgCode(err) == "23503" }},
			{"type of another organization", permission.FieldGrant{RoleID: sp(fgRole), ObjectKind: permission.KindCI, CITypeID: sp(fgTypeB), Level: permission.LevelNone},
				func(err error) bool { return errors.Is(err, permission.ErrUnknownReference) }},
			{"unknown action", permission.FieldGrant{RoleID: sp(fgRole), ObjectKind: permission.KindCI, Action: sp("ci:fly"), Level: permission.LevelNone},
				func(err error) bool { return errors.Is(err, permission.ErrUnknownReference) }},
			{"unknown attribute", permission.FieldGrant{RoleID: sp(fgRole), ObjectKind: permission.KindCI, FieldKey: sp("attributes.wp264_nope"), Level: permission.LevelNone},
				func(err error) bool { return errors.Is(err, permission.ErrUnknownFieldKey) }},
			{"unknown instance key", permission.FieldGrant{RoleID: sp(fgRole), ObjectKind: permission.KindCI, FieldKey: sp("_instance.wp264_nope"), Level: permission.LevelNone},
				func(err error) bool { return errors.Is(err, permission.ErrUnknownFieldKey) }},
			{"structural field", permission.FieldGrant{RoleID: sp(fgRole), ObjectKind: permission.KindCI, FieldKey: sp("core.name"), Level: permission.LevelNone},
				func(err error) bool { return errors.Is(err, permission.ErrUnknownFieldKey) }},
			{"known attribute", permission.FieldGrant{RoleID: sp(fgRole), ObjectKind: permission.KindCI, FieldKey: sp("attributes.wp264_attr"), Level: permission.LevelNone},
				func(err error) bool { return err == nil }},
			{"known instance key", permission.FieldGrant{RoleID: sp(fgRole), ObjectKind: permission.KindCI, FieldKey: sp("_instance.wp264_inst"), Level: permission.LevelRead},
				func(err error) bool { return err == nil }},
			{"action", permission.FieldGrant{RoleID: sp(fgRole), ObjectKind: permission.KindCI, CITypeID: sp(fgChildType), Action: sp("lifecycle:transition"), Level: permission.LevelNone},
				func(err error) bool { return err == nil }},
			{"relationship type", permission.FieldGrant{RoleID: sp(fgRole), ObjectKind: permission.KindRelationship, RelationshipTypeID: sp(f.relType), Level: permission.LevelRead},
				func(err error) bool { return err == nil }},
		}
		for _, c := range cases {
			if _, err := repo.CreateFieldGrant(a, fgOrg, &c.g); !c.want(err) {
				t.Errorf("%s: %v", c.name, err)
			}
		}
		// The foreign key refuses a role of another organization even
		// without the repository (K19).
		err := database.WithTenant(ctx, f.app, ptrScope(database.OrgWideScope(fgOrg, "")), func(ctx context.Context, tx pgx.Tx) error {
			_, execErr := tx.Exec(ctx, `INSERT INTO object_grant (organization_id, grantee_team_id, object_kind, object_id, level)
				VALUES ($1, $2, 'ci', $3, 'none')`, fgOrg, fgTeamB, fgCI1)
			return execErr
		})
		if pgCode(err) != "23503" {
			t.Errorf("team of another organization: %v, want 23503", err)
		}
	})

	t.Run("object grants and delete trigger", func(t *testing.T) {
		if _, err := repo.CreateObjectGrant(a, fgOrg, &permission.ObjectGrant{GranteeTeamID: sp(fgTeamB), ObjectKind: permission.KindCI,
			ObjectID: fgCI1, Level: permission.LevelRead}); pgCode(err) != "23503" {
			t.Errorf("team of another organization: %v, want 23503", err)
		}
		if _, err := repo.CreateObjectGrant(a, fgOrg, &permission.ObjectGrant{GranteeRoleID: sp(fgRole), ObjectKind: permission.KindCI,
			ObjectID: fgCIB, Level: permission.LevelRead}); !errors.Is(err, permission.ErrUnknownReference) {
			t.Errorf("CI of another organization: %v, want ErrUnknownReference", err)
		}
		// A principal confined to client 1 cannot name a CI of client 2.
		if _, err := repo.CreateObjectGrant(clientCtx(ctx), fgOrg, &permission.ObjectGrant{GranteeRoleID: sp(fgRole),
			ObjectKind: permission.KindCI, ObjectID: fgCI3, Level: permission.LevelRead}); !errors.Is(err, permission.ErrUnknownReference) {
			t.Errorf("CI outside the client scope: %v, want ErrUnknownReference", err)
		}
		soft, err := repo.CreateObjectGrant(a, fgOrg, &permission.ObjectGrant{GranteeTeamID: sp(fgTeam), ObjectKind: permission.KindCI,
			ObjectID: fgCI1, Level: permission.LevelRead})
		if err != nil {
			t.Fatalf("team grant: %v", err)
		}
		hard, err := repo.CreateObjectGrant(clientCtx(ctx), fgOrg, &permission.ObjectGrant{GranteeRoleID: sp(fgRole), ObjectKind: permission.KindCI,
			ObjectID: fgCI2, Level: permission.LevelNone})
		if err != nil {
			t.Fatalf("role grant: %v", err)
		}
		if _, err = repo.CreateObjectGrant(a, fgOrg, &permission.ObjectGrant{GranteeTeamID: sp(fgTeam), ObjectKind: permission.KindCI,
			ObjectID: fgCI1, Level: permission.LevelNone}); pgCode(err) != "23505" {
			t.Errorf("duplicate: %v, want 23505", err)
		}
		list, _, err := repo.ListObjectGrants(a, fgOrg, &permission.ObjectGrantFilter{ObjectID: fgCI1}, api.PaginationParams{})
		if err != nil || len(list) != 1 || list[0].ID != soft.ID {
			t.Errorf("list by object: %+v, %v", list, err)
		}
		if foreign, _, listErr := repo.ListObjectGrants(b, fgOrgB, &permission.ObjectGrantFilter{}, api.PaginationParams{}); listErr != nil || len(foreign) != 0 {
			t.Errorf("organization B lists %d object grants (%v)", len(foreign), listErr)
		}
		scope := database.OrgWideScope(fgOrg, fgUser)
		if err = database.WithTenant(ctx, f.app, &scope, func(ctx context.Context, tx pgx.Tx) error {
			if _, execErr := tx.Exec(ctx, `UPDATE ci SET deleted_at = now() WHERE id = $1`, fgCI1); execErr != nil {
				return execErr
			}
			_, execErr := tx.Exec(ctx, `DELETE FROM ci WHERE id = $1`, fgCI2)
			return execErr
		}); err != nil {
			t.Fatalf("delete CIs: %v", err)
		}
		if _, err = repo.GetObjectGrant(a, fgOrg, soft.ID); err != nil {
			t.Errorf("grant of a soft-deleted CI: %v, want kept", err)
		}
		if _, err = repo.GetObjectGrant(a, fgOrg, hard.ID); !errors.Is(err, api.ErrNotFound) {
			t.Errorf("grant of a hard-deleted CI: %v, want removed", err)
		}
	})

	t.Run("sensitivity", func(t *testing.T) {
		s, err := repo.CreateSensitiveField(a, fgOrg, &permission.SensitiveField{ObjectKind: permission.KindCI, CITypeID: sp(f.server), FieldKey: "core.serial_number"})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if _, err = repo.CreateSensitiveField(a, fgOrg, &permission.SensitiveField{ObjectKind: permission.KindCI, CITypeID: sp(f.server), FieldKey: "core.serial_number"}); pgCode(err) != "23505" {
			t.Errorf("duplicate: %v, want 23505", err)
		}
		if _, err = repo.CreateSensitiveField(a, fgOrg, &permission.SensitiveField{ObjectKind: permission.KindCI, FieldKey: "attributes.wp264_nope"}); !errors.Is(err, permission.ErrUnknownFieldKey) {
			t.Errorf("unknown attribute: %v", err)
		}
		if _, err = repo.CreateSensitiveField(a, fgOrg, &permission.SensitiveField{ObjectKind: permission.KindAsset, FieldKey: "core.purchase_cost"}); err != nil {
			t.Fatalf("asset column: %v", err)
		}
		list, _, err := repo.ListSensitiveFields(a, fgOrg, &permission.SensitiveFieldFilter{ObjectKind: permission.KindCI}, api.PaginationParams{})
		if err != nil || len(list) != 1 || list[0].ID != s.ID {
			t.Errorf("list: %+v, %v", list, err)
		}
		if foreign, _, listErr := repo.ListSensitiveFields(b, fgOrgB, &permission.SensitiveFieldFilter{}, api.PaginationParams{}); listErr != nil || len(foreign) != 0 {
			t.Errorf("organization B lists %d marks (%v)", len(foreign), listErr)
		}
	})

	t.Run("type chain, teams and export", func(t *testing.T) {
		chain, err := repo.TypeChain(ctx, fgOrg)
		if err != nil {
			t.Fatal(err)
		}
		if chain[fgChildType] != f.server {
			t.Errorf("chain of the child type: %q, want the server type", chain[fgChildType])
		}
		if _, ok := chain[fgTypeB]; ok {
			t.Error("type chain contains a type of another organization")
		}
		teams, err := repo.TeamIDs(ctx, fgOrg, fgUser)
		if err != nil || !slices.Equal(teams, []string{fgTeam}) {
			t.Errorf("teams: %v, %v", teams, err)
		}
		export, err := repo.Export(a, fgOrg)
		if err != nil {
			t.Fatal(err)
		}
		want := permission.ExportedFieldGrant{Role: "wp264-role", RoleKind: permission.RoleKindRole, ObjectKind: permission.KindRelationship,
			RelationshipType: f.relKey, Level: permission.LevelRead}
		if !slices.Contains(export.FieldGrants, want) {
			t.Errorf("export lacks %+v: %+v", want, export.FieldGrants)
		}
		action := permission.ExportedFieldGrant{Role: "wp264-role", RoleKind: permission.RoleKindRole, ObjectKind: permission.KindCI,
			CIType: "wp264-child", Action: "lifecycle:transition", Level: permission.LevelNone}
		if !slices.Contains(export.FieldGrants, action) {
			t.Errorf("export lacks %+v", action)
		}
		mark := permission.ExportedSensitiveField{ObjectKind: permission.KindCI, CIType: "server", FieldKey: "core.serial_number"}
		if !slices.Contains(export.Sensitivity, mark) {
			t.Errorf("export lacks %+v: %+v", mark, export.Sensitivity)
		}
		if !slices.IsSortedFunc(export.Sensitivity, func(x, y permission.ExportedSensitiveField) int {
			return compareStrings(string(x.ObjectKind)+x.CIType+x.FieldKey, string(y.ObjectKind)+y.CIType+y.FieldKey)
		}) {
			t.Errorf("sensitivity not sorted: %+v", export.Sensitivity)
		}
	})
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func ptrScope(s database.TenantScope) *database.TenantScope { return &s }

// TestFieldResolverOnPostgres: Resolve for a session with a custom role, an
// IdP group and a team, within a client-scoped request transaction, the
// cache with Invalidate and TTL, and the isolation of another organization.
func TestFieldResolverOnPostgres(t *testing.T) {
	ctx, f := seedFieldGrants(t)
	a := orgCtx(ctx, fgOrg)
	repo := f.repo
	perms := permission.NewPGRepository(f.app)
	resolver := permission.NewFieldResolver(perms, user.ServiceAccountAccess{Repo: user.NewPGServiceAccounts(f.app)}, repo, cache.NewMemoryStore()).
		WithTTL(time.Second)
	mustField := func(g permission.FieldGrant) {
		t.Helper()
		if _, err := repo.CreateFieldGrant(a, fgOrg, &g); err != nil {
			t.Fatalf("create %+v: %v", g, err)
		}
	}
	// The custom role closes hostname; the engineer role (IdP group) has no
	// grant; the team of the user may read CI 1 only.
	mustField(permission.FieldGrant{CustomRoleID: sp(fgCustom), ObjectKind: permission.KindCI, FieldKey: sp("core.hostname"), Level: permission.LevelNone})
	if _, err := repo.CreateObjectGrant(a, fgOrg, &permission.ObjectGrant{GranteeTeamID: sp(fgTeam), ObjectKind: permission.KindCI,
		ObjectID: fgCI1, Level: permission.LevelRead}); err != nil {
		t.Fatal(err)
	}
	all := []identity.Permission{identity.PermCIRead, identity.PermCIWrite}
	session := identity.Principal{Subject: fgUser, OrganizationID: fgOrg, Type: identity.PrincipalTypeUser, Permissions: all}
	ci2 := permission.ObjectRef{ID: fgCI2, ClientID: fgClient1}
	ci1 := permission.ObjectRef{ID: fgCI1, ClientID: fgClient1}
	level := func(p identity.Principal, field string, o permission.ObjectRef) permission.Level {
		t.Helper()
		policy, err := resolver.Resolve(ctx, p)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		return policy.Level(permission.KindCI, fgChildType, field, o)
	}
	if got := level(session, "core.hostname", ci2); got != permission.LevelNone {
		t.Errorf("custom role: %q, want none", got)
	}
	if got := level(session, "core.fqdn", ci2); got != permission.LevelWrite {
		t.Errorf("custom role, other field: %q, want write", got)
	}
	if got := level(session, "core.fqdn", ci1); got != permission.LevelRead {
		t.Errorf("team object grant: %q, want read", got)
	}
	grouped := session
	grouped.Groups = []string{"engineer"}
	if got := level(grouped, "core.hostname", ci2); got != permission.LevelWrite {
		t.Errorf("with IdP group engineer: %q, want write (union)", got)
	}

	// Within a client-scoped request transaction the org-wide reads run
	// outside of it.
	clientScope := database.TenantScope{OrgID: fgOrg, UserID: fgUser, Clients: database.ScopeIDs(fgClient1),
		Sites: database.AllScopes(), Teams: database.AllScopes()}
	scoped := session
	scoped.Scope = &identity.Scope{Clients: identity.ScopeSet{IDs: []string{fgClient1}}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}}
	err := database.WithRequestTenantTx(database.ContextWithTenantScope(ctx, &clientScope), f.app, fgOrg, func(txCtx context.Context) error {
		resolver.Invalidate(txCtx, fgOrg)
		policy, resolveErr := resolver.Resolve(txCtx, scoped)
		if resolveErr != nil {
			return resolveErr
		}
		if got := policy.Level(permission.KindCI, "", "core.hostname", ci2); got != permission.LevelNone {
			t.Errorf("in the transaction: %q, want none", got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Resolve in a client-scoped transaction: %v", err)
	}

	// Cache: a new grant is invisible until Invalidate, and at the latest
	// after the TTL.
	mustField(permission.FieldGrant{CustomRoleID: sp(fgCustom), ObjectKind: permission.KindCI, FieldKey: sp("core.fqdn"), Level: permission.LevelRead})
	if got := level(session, "core.fqdn", ci2); got != permission.LevelWrite {
		t.Errorf("cached: %q, want write", got)
	}
	resolver.Invalidate(ctx, fgOrg)
	if got := level(session, "core.fqdn", ci2); got != permission.LevelRead {
		t.Errorf("after Invalidate: %q, want read", got)
	}
	mustField(permission.FieldGrant{CustomRoleID: sp(fgCustom), ObjectKind: permission.KindCI, FieldKey: sp("core.model"), Level: permission.LevelNone})
	if got := level(session, "core.model", ci2); got != permission.LevelWrite {
		t.Errorf("cached: %q, want write", got)
	}
	time.Sleep(1100 * time.Millisecond)
	if got := level(session, "core.model", ci2); got != permission.LevelNone {
		t.Errorf("after the TTL: %q, want none", got)
	}

	// Organization B has no field permissions: its principals stay
	// unrestricted, the rows of A do not reach them.
	policyB, err := resolver.Resolve(ctx, identity.Principal{Subject: fgUser, OrganizationID: fgOrgB, Permissions: all})
	if err != nil {
		t.Fatal(err)
	}
	if policyB.Restricted(permission.KindCI) || policyB.Level(permission.KindCI, "", "core.hostname", ci2) != permission.LevelWrite {
		t.Error("organization B sees the field permissions of A")
	}
}
