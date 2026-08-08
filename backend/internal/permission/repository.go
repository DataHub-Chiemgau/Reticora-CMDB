package permission

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Repository defines persistence operations for permission grants.
type Repository interface {
	ListPermissions(ctx context.Context) ([]Permission, error)
	ListRolePermissions(ctx context.Context, orgID, roleID string) ([]RolePermissionGrant, error)
	ReplaceRolePermissions(ctx context.Context, orgID, roleID, grantedBy string, keys []string) ([]RolePermissionGrant, error)
	EffectivePermissions(ctx context.Context, orgID, userID string) ([]string, error)
	HasPermission(ctx context.Context, orgID, userID, key string) (bool, error)
}

// MemoryRepository is an in-memory implementation of Repository.
type MemoryRepository struct {
	mu          sync.RWMutex
	catalogue   map[string]Permission
	roleGrants  map[string]map[string]RolePermissionGrant
	assignments map[string][]string
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
		assignments: make(map[string][]string),
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
	for _, roleID := range r.assignments[userGrantKey(orgID, userID)] {
		for key := range r.roleGrants[grantKey(orgID, roleID)] {
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

// AssignRoleToUser exists for handler tests and --no-db fixtures.
func (r *MemoryRepository) AssignRoleToUser(orgID, userID, roleID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := userGrantKey(orgID, userID)
	r.assignments[key] = append(r.assignments[key], roleID)
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
