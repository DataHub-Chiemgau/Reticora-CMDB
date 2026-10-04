package permission

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// Repository defines persistence operations for permission grants.
type Repository interface {
	ListPermissions(ctx context.Context) ([]Permission, error)
	ListRolePermissions(ctx context.Context, orgID, roleID string) ([]RolePermissionGrant, error)
	ReplaceRolePermissions(ctx context.Context, orgID, roleID, grantedBy string, keys []string) ([]RolePermissionGrant, error)
	EffectivePermissions(ctx context.Context, orgID, userID string) ([]string, error)
	HasPermission(ctx context.Context, orgID, userID, key string) (bool, error)
	// AccessGrants returns every role and custom role assignment of the user
	// with its permissions and scope (RBA-03). It is the source of the
	// session's permissions and tenant scope (identity.ResolveAccess).
	AccessGrants(ctx context.Context, orgID, userID string) ([]identity.Grant, error)
	// RoleGrants returns an org-wide grant for each named standard role the
	// IdP roles of a session map to (RBA-02); client-scope roles grant
	// nothing without a client.
	RoleGrants(ctx context.Context, orgID string, roleNames []string) ([]identity.Grant, error)
}

var _ identity.AccessResolver = Repository(nil)

// MemoryRepository is an in-memory implementation of Repository.
type MemoryRepository struct {
	mu          sync.RWMutex
	catalogue   map[string]Permission
	roleGrants  map[string]map[string]RolePermissionGrant
	assignments map[string][]assignment
}

// assignment is one role assignment of the memory repository; empty client and
// site lists make it org-wide.
type assignment struct {
	roleID  string
	clients []string
	sites   []string
}

// NewMemoryRepository creates a memory permission repository.
func NewMemoryRepository() *MemoryRepository {
	catalogue := make(map[string]Permission, len(Catalogue))
	for _, p := range Catalogue {
		catalogue[p.Key] = p
	}
	return &MemoryRepository{
		catalogue:   catalogue,
		roleGrants:  make(map[string]map[string]RolePermissionGrant),
		assignments: make(map[string][]assignment),
	}
}

func (r *MemoryRepository) ListPermissions(_ context.Context) ([]Permission, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	items := make([]Permission, 0, len(r.catalogue))
	for _, p := range r.catalogue {
		items = append(items, p)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return items, nil
}

func (r *MemoryRepository) ListRolePermissions(_ context.Context, orgID, roleID string) ([]RolePermissionGrant, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	items := grantsToSlice(r.roleGrants[grantKey(orgID, roleID)])
	return items, nil
}

func (r *MemoryRepository) ReplaceRolePermissions(_ context.Context, orgID, roleID, grantedBy string, keys []string) ([]RolePermissionGrant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	unique := make(map[string]RolePermissionGrant)
	now := time.Now().UTC()
	for _, key := range keys {
		if _, ok := r.catalogue[key]; !ok {
			return nil, fmt.Errorf("unknown permission %q", key)
		}
		unique[key] = RolePermissionGrant{OrganizationID: orgID, RoleID: roleID, PermissionKey: key, GrantedAt: now, GrantedBy: grantedBy}
	}
	r.roleGrants[grantKey(orgID, roleID)] = unique
	return grantsToSlice(unique), nil
}

func (r *MemoryRepository) EffectivePermissions(_ context.Context, orgID, userID string) ([]string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	set := make(map[string]struct{})
	for _, a := range r.assignments[userGrantKey(orgID, userID)] {
		for key := range r.roleGrants[grantKey(orgID, a.roleID)] {
			set[key] = struct{}{}
		}
	}
	return sortedKeys(set), nil
}

func (r *MemoryRepository) HasPermission(ctx context.Context, orgID, userID, key string) (bool, error) {
	keys, err := r.EffectivePermissions(ctx, orgID, userID)
	if err != nil {
		return false, err
	}
	for _, candidate := range keys {
		if candidate == key {
			return true, nil
		}
	}
	return false, nil
}

func (r *MemoryRepository) AccessGrants(_ context.Context, orgID, userID string) ([]identity.Grant, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	assignments := r.assignments[userGrantKey(orgID, userID)]
	grants := make([]identity.Grant, 0, len(assignments))
	for _, a := range assignments {
		keys := make(map[string]struct{})
		for key := range r.roleGrants[grantKey(orgID, a.roleID)] {
			keys[key] = struct{}{}
		}
		grants = append(grants, identity.Grant{
			Permissions: toPermissions(sortedKeys(keys)),
			Clients:     append([]string(nil), a.clients...),
			Sites:       append([]string(nil), a.sites...),
		})
	}
	return grants, nil
}

// RoleGrants grants the target matrix of the named org-scope standard roles
// (StandardRoles); the memory repository holds no per-organization roles.
func (r *MemoryRepository) RoleGrants(_ context.Context, _ string, roleNames []string) ([]identity.Grant, error) {
	grants := make([]identity.Grant, 0, len(roleNames))
	for _, name := range roleNames {
		for _, role := range StandardRoles {
			if role.Name != name || role.Scope != "org" {
				continue
			}
			keys, _ := StandardRolePermissions(name)
			grants = append(grants, identity.OrgWideGrant(toPermissions(keys)))
		}
	}
	return grants, nil
}

// AssignRoleToUser exists for handler tests and --no-db fixtures.
func (r *MemoryRepository) AssignRoleToUser(orgID, userID, roleID string) {
	r.AssignScopedRoleToUser(orgID, userID, roleID, nil, nil)
}

// AssignScopedRoleToUser assigns a role restricted to clients and sites.
func (r *MemoryRepository) AssignScopedRoleToUser(orgID, userID, roleID string, clients, sites []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := userGrantKey(orgID, userID)
	r.assignments[key] = append(r.assignments[key], assignment{
		roleID:  roleID,
		clients: append([]string(nil), clients...),
		sites:   append([]string(nil), sites...),
	})
}

func toPermissions(keys []string) []identity.Permission {
	out := make([]identity.Permission, 0, len(keys))
	for _, key := range keys {
		out = append(out, identity.Permission(key))
	}
	return out
}

func grantsToSlice(values map[string]RolePermissionGrant) []RolePermissionGrant {
	items := make([]RolePermissionGrant, 0, len(values))
	for _, grant := range values {
		items = append(items, grant)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].PermissionKey < items[j].PermissionKey })
	return items
}

func sortedKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func grantKey(orgID, roleID string) string     { return orgID + ":" + roleID }
func userGrantKey(orgID, userID string) string { return orgID + ":" + userID }
