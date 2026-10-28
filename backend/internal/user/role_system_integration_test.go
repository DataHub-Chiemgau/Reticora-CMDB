// The roles tab shows the four RBA-02 standard roles as system roles
// (F-201): GET /api/v1/roles (ListRoles) marks the seeded roles of the table
// `role` (is_builtin) with is_system=true; tenant-defined custom roles stay
// is_system=false. Before the fix ListRoles answered `false AS is_system`
// for the table `role`, and the UI showed "Custom" (roles-authz 09b).
//
// Retest (needs a migrated database):
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -run TestListRolesMarksStandardRolesAsSystem ./internal/user/
package user_test

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
)

func TestListRolesMarksStandardRolesAsSystem(t *testing.T) {
	f := scopetest.Seed(t, "c5")
	if _, err := f.Admin.Exec(context.Background(),
		`INSERT INTO custom_role (organization_id, name, permissions) VALUES ($1, 'rolesys-auditor', '["ci:read"]')`, f.OrgA); err != nil {
		t.Fatalf("insert custom role: %v", err)
	}
	roles, _, err := user.NewPGRepository(f.App).ListRoles(f.OrgCtx(f.OrgA), f.OrgA, api.PaginationParams{Limit: 100})
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	standard := map[string]bool{"org_admin": false, "engineer": false, "viewer": false, "client_technician": false}
	custom := false
	for _, r := range roles {
		if _, ok := standard[r.Name]; ok {
			standard[r.Name] = true
			if !r.IsBuiltin || !r.IsSystem {
				t.Errorf("%s: is_builtin=%v is_system=%v, want both true", r.Name, r.IsBuiltin, r.IsSystem)
			}
		}
		if r.Name == "rolesys-auditor" {
			custom = true
			if r.IsBuiltin || r.IsSystem {
				t.Errorf("custom role: is_builtin=%v is_system=%v, want both false", r.IsBuiltin, r.IsSystem)
			}
		}
	}
	for name, seen := range standard {
		if !seen {
			t.Errorf("standard role %s missing from ListRoles", name)
		}
	}
	if !custom {
		t.Error("custom role missing from ListRoles")
	}
}
