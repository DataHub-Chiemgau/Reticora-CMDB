package identity

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

type permissionContextKey struct{}

// WithPermissions stores permissions in a request context.
func WithPermissions(ctx context.Context, permissions []Permission) context.Context {
	copied := append([]Permission(nil), permissions...)
	return context.WithValue(ctx, permissionContextKey{}, copied)
}

// PermissionsFromContext extracts permissions from a request context.
func PermissionsFromContext(ctx context.Context) ([]Permission, bool) {
	permissions, ok := ctx.Value(permissionContextKey{}).([]Permission)
	if !ok {
		return nil, false
	}
	return append([]Permission(nil), permissions...), true
}

// HasPermission checks whether the required permission is present.
func HasPermission(userPerms []Permission, required Permission) bool {
	for _, permission := range userPerms {
		if permission == required {
			return true
		}
	}
	return false
}

// MergePermissions de-duplicates permissions merged from multiple role sets.
func MergePermissions(roles ...[]Permission) []Permission {
	seen := make(map[Permission]struct{})
	merged := make([]Permission, 0)
	for _, rolePermissions := range roles {
		for _, permission := range rolePermissions {
			if _, ok := seen[permission]; ok {
				continue
			}
			seen[permission] = struct{}{}
			merged = append(merged, permission)
		}
	}
	return merged
}

// RequirePermission checks the request context for the required permission.
func RequirePermission(required Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			permissions, ok := PermissionsFromContext(r.Context())
			if !ok || !HasPermission(permissions, required) {
				api.WriteError(w, http.StatusForbidden, "Forbidden", "missing required permission")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// oidcRoleMapping is the defined mapping of IdP role and group names to the
// standard roles of the database (RBA-02, WP-046). Matching is exact and
// case-insensitive after removing a leading "/" of a group path; any other
// name grants nothing. client_technician has no IdP mapping: it is valid
// only in a client scope, which needs a client-scoped role assignment.
var oidcRoleMapping = map[string]string{
	"org_admin":         "org_admin",
	"reticora-admin":    "org_admin",
	"engineer":          "engineer",
	"reticora-engineer": "engineer",
	"viewer":            "viewer",
	"reticora-viewer":   "viewer",
}

// StandardRolesForGroups maps IdP roles and groups to standard role names,
// sorted and without duplicates.
func StandardRolesForGroups(groups []string) []string {
	seen := map[string]struct{}{}
	roles := make([]string, 0, len(groups))
	for _, group := range groups {
		name := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(group), "/"))
		role, ok := oidcRoleMapping[name]
		if !ok {
			continue
		}
		if _, dup := seen[role]; dup {
			continue
		}
		seen[role] = struct{}{}
		roles = append(roles, role)
	}
	sort.Strings(roles)
	return roles
}
