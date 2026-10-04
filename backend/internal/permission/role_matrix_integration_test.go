package permission_test

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
)

// rba02 is the RBA-02 matrix of the catalogue text over the keys it governs,
// translated to the current permission keys (migration 000073). "C" marks a
// right the client technician holds only in its client scope.
var rba02 = []struct {
	keys                                    []string
	orgAdmin, engineer, viewer, clientTechn string
}{
	{[]string{"ci:read", "site:read", "rack:read", "topology:read"}, "✓", "✓", "✓", "C"},
	{[]string{"ci:write", "rack:write", "relationship:write", "contact:write"}, "✓", "✓", "–", "C"},
	{[]string{"ci:delete", "site:write"}, "✓", "✓", "–", "–"},
	{[]string{"reconciliation:resolve", "export:run"}, "✓", "✓", "–", "C"},
	{[]string{"discovery:write", "credential:read", "credential:write", "credential:manage", "credential:decrypt"}, "✓", "✓", "–", "–"},
	{[]string{"citype:manage", "ci_type:manage", "webhook:manage"}, "✓", "–", "–", "–"},
	{[]string{"user:manage", "role:manage", "permission:write", "entitlement:manage", "audit:read", "apikey:manage"}, "✓", "–", "–", "–"},
}

// TestStandardRolesMatchTheRoleMatrix covers WP-046 (RBA-02): the roles the
// database seeds into an organization equal the target matrix
// permission.StandardRoles, which follows the RBA-02 text; the viewer holds
// no credential right; the client technician grants only within a client
// scope; IdP roles resolve to the database roles of the organization.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStandardRolesMatchTheRoleMatrix(t *testing.T) {
	f := scopetest.Seed(t, "55")
	bg := context.Background()

	var catalogue []string
	if err := f.Admin.QueryRow(bg, `SELECT array_agg(key ORDER BY key) FROM permission`).Scan(&catalogue); err != nil {
		t.Fatalf("read catalogue: %v", err)
	}
	seeded := map[string][]string{}
	rows, err := f.Admin.Query(bg, `
		SELECT ro.name, ro.scope, ARRAY(
			SELECT DISTINCT k FROM (
				SELECT rp.permission_key AS k FROM role_permission rp WHERE rp.role_id = ro.id
				UNION SELECT jsonb_array_elements_text(ro.permissions)
			) keys WHERE k IN (SELECT key FROM permission) ORDER BY k)
		FROM role ro WHERE ro.organization_id = $1 AND ro.is_builtin`, f.OrgA)
	if err != nil {
		t.Fatalf("read seeded roles: %v", err)
	}
	scopes := map[string]string{}
	for rows.Next() {
		var name, scope string
		var keys []string
		if err = rows.Scan(&name, &scope, &keys); err != nil {
			t.Fatal(err)
		}
		seeded[name], scopes[name] = keys, scope
	}
	rows.Close()

	// The seed equals the target matrix.
	for _, role := range permission.StandardRoles {
		want := role.Permissions
		if want == nil {
			want = catalogue
		}
		want = append([]string(nil), want...)
		sort.Strings(want)
		if got := strings.Join(seeded[role.Name], " "); got != strings.Join(want, " ") {
			t.Errorf("seeded %s:\n got  %s\n want %s", role.Name, got, strings.Join(want, " "))
		}
		if scopes[role.Name] != role.Scope {
			t.Errorf("%s scope %q, want %q", role.Name, scopes[role.Name], role.Scope)
		}
	}

	// The target matrix follows the RBA-02 text.
	holds := func(role, key string) bool {
		keys, _ := permission.StandardRolePermissions(role)
		for _, k := range keys {
			if k == key {
				return true
			}
		}
		return false
	}
	for _, row := range rba02 {
		for _, key := range row.keys {
			for role, cell := range map[string]string{"org_admin": row.orgAdmin, "engineer": row.engineer, "viewer": row.viewer, "client_technician": row.clientTechn} {
				if want := cell != "–"; holds(role, key) != want {
					t.Errorf("RBA-02: %s holds %s = %v, want %v (%s)", role, key, !want, want, cell)
				}
			}
		}
	}
	for _, key := range seeded["viewer"] {
		if strings.HasPrefix(key, "credential:") {
			t.Errorf("viewer holds %s", key)
		}
	}

	// C semantics: the client technician grants nothing without a client.
	repo := permission.NewPGRepository(f.App)
	tech := f.AppUser(t, f.OrgA, "matrix-tech")
	var techRole string
	if err = f.Admin.QueryRow(bg, `SELECT id::text FROM role WHERE organization_id = $1 AND name = 'client_technician'`, f.OrgA).Scan(&techRole); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, tech, techRole); err != nil {
		t.Fatal(err)
	}
	if grants, grantErr := repo.AccessGrants(bg, f.OrgA, tech); grantErr != nil || len(grants) != 0 {
		t.Errorf("client technician without client: %+v, %v; want no grant", grants, grantErr)
	}
	if _, err = f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id) VALUES ($1, $2, $3, $4)`, f.OrgA, tech, techRole, f.Client1); err != nil {
		t.Fatal(err)
	}
	grants, err := repo.AccessGrants(bg, f.OrgA, tech)
	if err != nil || len(grants) != 1 || len(grants[0].Clients) != 1 || grants[0].Clients[0] != f.Client1 {
		t.Errorf("client technician in client 1: %+v, %v", grants, err)
	}

	// IdP roles resolve to the organization's database roles; the client
	// technician has no org-wide grant.
	mapped, err := repo.RoleGrants(bg, f.OrgA, []string{"viewer", "client_technician"})
	if err != nil || len(mapped) != 1 {
		t.Fatalf("role grants: %+v, %v; want the viewer only", mapped, err)
	}
	access := identity.ResolveAccess(mapped)
	if !access.Scope.OrgWide() || len(access.Permissions) != len(seeded["viewer"]) {
		t.Errorf("viewer role grant: %d permissions (org-wide %v), want %d", len(access.Permissions), access.Scope.OrgWide(), len(seeded["viewer"]))
	}
}
