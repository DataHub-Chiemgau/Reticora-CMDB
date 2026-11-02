package permission_test

import (
	"context"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
)

// rba02 is the RBA-02 matrix of the catalogue text with its exact keys
// (RBA-01 names since migration 000081), plus the module keys that WP-046
// assigned to the rows they stand for (rack:read with location:read,
// credential:read with credential:manage). The near-duplicate ci_type:manage
// and the keys that gate no route (credential:decrypt, credential:write,
// permission:write and others) are retired since migration 000147 (WP-363)
// and held by no role (retiredKeys).
// "C" marks a right the client technician holds only in its client scope.
var rba02 = []struct {
	keys                                    []string
	orgAdmin, engineer, viewer, clientTechn string
}{
	{[]string{"ci:read", "location:read", "rack:read", "topology:read"}, "✓", "✓", "✓", "C"},
	{[]string{"ci:write", "rack:write", "relationship:write", "contact:write"}, "✓", "✓", "–", "C"},
	{[]string{"ci:delete", "location:write"}, "✓", "✓", "–", "–"},
	{[]string{"review:resolve"}, "✓", "✓", "–", "C"},
	{[]string{"export:run", "job:read"}, "✓", "✓", "–", "C"},
	{[]string{"discovery:manage", "collector:manage", "vrf:manage", "credential:read", "credential:manage"}, "✓", "✓", "–", "–"},
	{[]string{"citype:manage", "webhook:manage", "notification:manage"}, "✓", "–", "–", "–"},
	{[]string{"user:manage", "team:manage", "role:manage", "entitlement:manage", "audit:read", "apikey:manage"}, "✓", "–", "–", "–"},
}

// p2 are the phase-2 rows of the matrix (RBA-05, E-106, WP-245): the module
// keys stock:*, reservation:* and contract:* and the action keys. "C" marks
// a right the client technician holds only in its client scope.
var p2 = []struct {
	keys                                    []string
	orgAdmin, engineer, viewer, clientTechn string
}{
	{[]string{"stock:read", "reservation:read", "contract:read"}, "✓", "✓", "✓", "C"},
	{[]string{"stock:write", "reservation:write", "contract:write"}, "✓", "✓", "–", "–"},
	{[]string{"stocktake:complete"}, "✓", "✓", "–", "C"},
	{[]string{"import:run", "tag:manage"}, "✓", "✓", "–", "–"},
	{[]string{"metamodel:publish", "field_permission:manage", "import:manage", "saved_view:manage"}, "✓", "–", "–", "–"},
	// The current rights stay: engineer approves no order and writes no
	// SLA; the client technician keeps ticket:write and stocktake:write.
	{[]string{"order:approve", "sla:write"}, "✓", "–", "–", "–"},
	{[]string{"ticket:write", "stocktake:write"}, "✓", "✓", "–", "C"},
}

// rba06 are the special rights of RBA-06 per standard role (E-106).
var rba06 = []struct {
	keys                                    []string
	orgAdmin, engineer, viewer, clientTechn string
}{
	{[]string{"lifecycle:transition", "reconciliation:manage", "override:write", "ci_instance_attribute:manage"}, "✓", "✓", "–", "–"},
	{[]string{"lifecycle:manage", "relationship_type:manage"}, "✓", "–", "–", "–"},
}

// retiredKeys are the catalogue keys migration 000147 removed (WP-363) and
// the keys migration 000192 replaced (WP-245: consumable:*,
// inventory:manage and asset:reserve by stock:* and reservation:write).
var retiredKeys = []string{
	"asset:assign", "ci_type:manage", "credential:decrypt", "credential:write",
	"permission:write", "role:write", "user:write", "webhook:write",
	"asset:reserve", "consumable:read", "consumable:write", "inventory:manage",
}

// TestStandardRolesMatchTheRoleMatrix covers WP-046 (RBA-02) and WP-245
// (RBA-05, RBA-06, E-106): the roles the database seeds into an
// organization equal the target matrix permission.StandardRoles, which
// follows the RBA-02 text, the phase-2 rows (p2) and the RBA-06 special
// rights (rba06) of E-106; no role holds a retired or replaced key; the
// viewer holds no credential right; the client technician grants only
// within a client scope, its phase-2 keys included; IdP roles resolve to
// the database roles of the organization.
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
	for _, table := range []struct {
		name string
		rows []struct {
			keys                                    []string
			orgAdmin, engineer, viewer, clientTechn string
		}
	}{{"RBA-02", rba02}, {"P2 (E-106)", p2}, {"RBA-06 (E-106)", rba06}} {
		for _, row := range table.rows {
			for _, key := range row.keys {
				if !slices.Contains(catalogue, key) {
					t.Errorf("%s: %s is not in the catalogue", table.name, key)
				}
				for role, cell := range map[string]string{"org_admin": row.orgAdmin, "engineer": row.engineer, "viewer": row.viewer, "client_technician": row.clientTechn} {
					if want := cell != "–"; holds(role, key) != want {
						t.Errorf("%s: %s holds %s = %v, want %v (%s)", table.name, role, key, !want, want, cell)
					}
					if cell == "C" && scopes[role] != "client" {
						t.Errorf("%s: %s holds %s in scope %q, want client (C)", table.name, role, key, scopes[role])
					}
				}
			}
		}
	}
	// No standard role holds a retired key, neither in the catalogue nor in
	// role_permission or role.permissions (the seed query above only sees
	// catalogue keys).
	var retired []string
	if err = f.Admin.QueryRow(bg, `
		SELECT COALESCE(array_agg(DISTINCT k ORDER BY k), '{}') FROM (
			SELECT key AS k FROM permission
			UNION ALL SELECT rp.permission_key FROM role_permission rp JOIN role ro ON ro.id = rp.role_id WHERE ro.organization_id = $1
			UNION ALL SELECT jsonb_array_elements_text(ro.permissions) FROM role ro WHERE ro.organization_id = $1
		) keys WHERE k = ANY ($2)`, f.OrgA, retiredKeys).Scan(&retired); err != nil {
		t.Fatalf("read retired keys: %v", err)
	}
	if len(retired) != 0 {
		t.Errorf("retired keys still granted or catalogued: %v", retired)
	}
	for _, role := range permission.StandardRoles {
		for _, key := range role.Permissions {
			if slices.Contains(retiredKeys, key) {
				t.Errorf("StandardRoles: %s holds the retired key %s", role.Name, key)
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
		t.Fatalf("client technician in client 1: %+v, %v", grants, err)
	}
	// The phase-2 keys of the client technician hold in its client only.
	for _, key := range []identity.Permission{identity.PermStockRead, identity.PermReservationRead, identity.PermContractRead, identity.PermStocktakeComplete} {
		if !slices.Contains(grants[0].Permissions, key) {
			t.Errorf("client technician in client 1 lacks %s", key)
		}
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
