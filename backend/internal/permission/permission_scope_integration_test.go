package permission_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
)

// TestPermissionRepositoryScope runs the permission repository with the
// principal's tenant scope (TEN-06, WP-012). Request paths need the
// principal's scope and its organization; resolving the access of a login or
// refresh (AccessGrants) runs before a principal exists and reads the user's
// own assignments org-wide.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestPermissionRepositoryScope(t *testing.T) {
	f := scopetest.Seed(t, "1b")
	ctx := context.Background()
	var userID string
	if err := f.Admin.QueryRow(ctx,
		`INSERT INTO app_user (organization_id, oidc_subject, email, display_name) VALUES ($1, 'scopetest-1b', 'p@scopetest.invalid', 'P') RETURNING id::text`,
		f.OrgA).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := f.Admin.Exec(ctx,
		`INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id)
		 SELECT $1, $2, id, $3 FROM role WHERE organization_id = $1 AND name = 'viewer'`,
		f.OrgA, userID, f.Client1); err != nil {
		t.Fatalf("assign client-scoped role: %v", err)
	}

	repo := permission.NewPGRepository(f.App)
	grants, err := repo.AccessGrants(ctx, f.OrgA, userID)
	if err != nil {
		t.Fatalf("access grants without request scope: %v", err)
	}
	if len(grants) != 1 || len(grants[0].Clients) != 1 || grants[0].Clients[0] != f.Client1 {
		t.Fatalf("grants: %+v, want one grant scoped to client 1", grants)
	}

	reqCtx := f.ClientCtx(f.Client1)
	keys, err := repo.EffectivePermissions(reqCtx, f.OrgA, userID)
	if err != nil || len(keys) == 0 {
		t.Fatalf("effective permissions: %v err=%v", keys, err)
	}
	if keys, err = repo.EffectivePermissions(reqCtx, f.OrgB, userID); !errors.Is(err, database.ErrTenantMismatch) {
		t.Fatalf("permissions in organization B: %v err=%v, want ErrTenantMismatch", keys, err)
	}
	catalogue, err := repo.ListPermissions(reqCtx)
	if err != nil || len(catalogue) == 0 {
		t.Fatalf("permission catalogue: %d entries err=%v", len(catalogue), err)
	}
	if _, err = repo.ListPermissions(ctx); !errors.Is(err, database.ErrNoTenantScope) {
		t.Fatalf("catalogue without scope: got %v, want ErrNoTenantScope", err)
	}
}
