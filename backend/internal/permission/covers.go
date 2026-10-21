package permission

import (
	"slices"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// Covers reports whether grants hold the permission for an object in the
// given client and site (empty: an object of the whole organization). The
// grants are resolved as for the session (identity.ResolveAccess): the scope
// of the permission is the union per dimension, and client and site must both
// lie in it (E-70 option 1, like the RLS), so a site grant beside a client
// grant does not cover the client's objects at other sites and a team grant
// beside a client grant does not cover other clients. An org-wide scope
// covers every object; a scope restricted in a dimension covers no object
// without a value there (RBA-03, fail-closed). The objects carry no team, so
// the team dimension does not restrict them (E-11: rows without team are
// visible). Notification recipients are resolved with it (NTF-03): only who
// may read the object hears of it.
func Covers(grants []identity.Grant, key identity.Permission, clientID, siteID string) bool {
	access := identity.ResolveAccess(grants)
	p := identity.Principal{Scope: &access.Scope, PermissionScopes: access.PermissionScopes, Permissions: access.Permissions}
	scope, ok := p.ScopeFor(key)
	if !ok {
		return false
	}
	in := func(set identity.ScopeSet, id string) bool {
		return set.All || (id != "" && slices.Contains(set.IDs, strings.ToLower(id)))
	}
	return in(scope.Clients, clientID) && in(scope.Sites, siteID)
}
