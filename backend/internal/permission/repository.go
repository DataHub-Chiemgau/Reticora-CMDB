package permission

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// ErrRoleNotFound: the standard role is not one of the organization (404).
var ErrRoleNotFound = errors.New("role not found")

// unknownPermission refuses a permission key outside the catalogue: a 400
// field error naming permission_keys.
func unknownPermission(key string) error {
	return &api.FieldError{Field: "permission_keys", Message: fmt.Sprintf("unknown permission %q", key)}
}

// Repository defines persistence operations for permission grants.
type Repository interface {
	ListPermissions(ctx context.Context) ([]Permission, error)
	ListRolePermissions(ctx context.Context, orgID, roleID string) ([]RolePermissionGrant, error)
	// ReplaceRolePermissions replaces the permissions of a standard role.
	// Permissions it adds must be held by caller organization-wide
	// (RoleWideningError); a nil caller holds nothing.
	ReplaceRolePermissions(ctx context.Context, orgID, roleID, grantedBy string, caller *identity.Principal, keys []string) ([]RolePermissionGrant, error)
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

// RoleWideningError refuses a change of a role's permissions that adds
// permissions the caller does not hold organization-wide (403, RBA-03,
// F-015): the role applies to all its assignments, org-wide ones too.
type RoleWideningError struct {
	Missing []identity.Permission
}

func (e *RoleWideningError) Error() string {
	keys := make([]string, len(e.Missing))
	for i, p := range e.Missing {
		keys[i] = string(p)
	}
	return "adding " + strings.Join(keys, ", ") + " to the role requires holding them organization-wide"
}

// checkWidening returns a RoleWideningError unless caller holds every key of
// next that current lacks organization-wide (identity.GrantCeiling with an
// org-wide target). Removing permissions is not limited.
func checkWidening(caller *identity.Principal, current, next []string) error {
	var added []identity.Permission
	for _, key := range next {
		if p := identity.Permission(key); !slices.Contains(current, key) && !slices.Contains(added, p) {
			added = append(added, p)
		}
	}
	if len(added) == 0 {
		return nil
	}
	if missing := identity.GrantCeiling(caller, []identity.Grant{identity.OrgWideGrant(added)}); len(missing) > 0 {
		return &RoleWideningError{Missing: missing}
	}
	return nil
}

// MemoryRepository is an in-memory implementation of Repository.
type MemoryRepository struct {
	mu          sync.RWMutex
	catalogue   map[string]Permission
	roleGrants  map[string]map[string]RolePermissionGrant
	assignments map[string][]assignment
	// siteClients maps the sites of an organization to their client
	// (RegisterSite).
	siteClients map[string]string
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
		siteClients: make(map[string]string),
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

func (r *MemoryRepository) ReplaceRolePermissions(_ context.Context, orgID, roleID, grantedBy string, caller *identity.Principal, keys []string) ([]RolePermissionGrant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	unique := make(map[string]RolePermissionGrant)
	now := time.Now().UTC()
	for _, key := range keys {
		if _, ok := r.catalogue[key]; !ok {
			return nil, unknownPermission(key)
		}
		unique[key] = RolePermissionGrant{OrganizationID: orgID, RoleID: roleID, PermissionKey: key, GrantedAt: now, GrantedBy: grantedBy}
	}
	current := make([]string, 0, len(r.roleGrants[grantKey(orgID, roleID)]))
	for key := range r.roleGrants[grantKey(orgID, roleID)] {
		current = append(current, key)
	}
	if err := checkWidening(caller, current, keys); err != nil {
		return nil, err
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

// AccessGrants derives the scope like PGRepository.AccessGrants: a site
// assignment is confined to the clients of its sites (RegisterSite); a site of
// unknown client, or of another client than the assignment's clients, grants
// nothing (fail-closed, E-08).
func (r *MemoryRepository) AccessGrants(_ context.Context, orgID, userID string) ([]identity.Grant, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	assignments := r.assignments[userGrantKey(orgID, userID)]
	grants := make([]identity.Grant, 0, len(assignments))
	for _, a := range assignments {
		clients, ok := r.assignmentClients(orgID, a)
		if !ok {
			continue
		}
		keys := make(map[string]struct{})
		for key := range r.roleGrants[grantKey(orgID, a.roleID)] {
			keys[key] = struct{}{}
		}
		grants = append(grants, identity.Grant{
			Permissions: toPermissions(sortedKeys(keys)),
			Clients:     clients,
			Sites:       append([]string(nil), a.sites...),
		})
	}
	return grants, nil
}

// assignmentClients returns the client scope of an assignment: its clients,
// or for a site assignment the clients of its sites. It reports false when a
// site has no known client in the organization or lies outside the
// assignment's clients.
func (r *MemoryRepository) assignmentClients(orgID string, a assignment) ([]string, bool) {
	if len(a.sites) == 0 {
		return append([]string(nil), a.clients...), true
	}
	clients := make([]string, 0, len(a.sites))
	for _, site := range a.sites {
		client, ok := r.siteClients[siteKey(orgID, site)]
		if !ok || (len(a.clients) > 0 && !slices.Contains(a.clients, client)) {
			return nil, false
		}
		if !slices.Contains(clients, client) {
			clients = append(clients, client)
		}
	}
	return clients, true
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

// RegisterSite records the client of a site of the organization; a site
// assignment derives its client from it (AccessGrants).
func (r *MemoryRepository) RegisterSite(orgID, siteID, clientID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.siteClients[siteKey(orgID, siteID)] = clientID
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
func siteKey(orgID, siteID string) string      { return orgID + ":" + siteID }
