package server

import (
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
)

// TestRoutePermissionsExistInCatalog locks the route authorization map
// against the canonical permission catalog, so a typo cannot create a route
// gated by a permission no role can ever grant.
func TestRoutePermissionsExistInCatalog(t *testing.T) {
	catalog := make(map[string]struct{}, len(permission.Catalogue))
	for _, p := range permission.Catalogue {
		catalog[p.Key] = struct{}{}
	}

	check := func(name string, permissions map[string]identity.Permission) {
		for resource, key := range permissions {
			if _, ok := catalog[string(key)]; !ok {
				t.Errorf("%s maps %q to %q, which is missing from the permission catalog", name, resource, key)
			}
		}
	}
	check("readPermissionFor", readPermissionFor)
	check("writePermissionOverrides", writePermissionOverrides)
	rules := make(map[string]identity.Permission, len(routeRules))
	for _, rule := range routeRules {
		rules[rule.method+" "+rule.pattern] = rule.permission
	}
	check("routeRules", rules)

	// Derived write permissions must exist too for resources that use the
	// read→write suffix convention (resources with an override are checked
	// above, and read-only resources derive nothing).
	for resource, read := range readPermissionFor {
		if _, ok := writePermissionOverrides[resource]; ok {
			continue
		}
		derived := writeFromRead(read)
		if derived == read {
			continue
		}
		if _, ok := catalog[string(derived)]; !ok {
			t.Errorf("derived write permission %q for %q is missing from the permission catalog", derived, resource)
		}
	}
}

// TestIdentityCatalogCoversRoutePermissions ensures the session permission
// vocabulary (identity.Permission constants granted via OIDC groups and API
// keys) covers every permission the route map can require — otherwise a route
// would be unreachable for every principal.
func TestIdentityCatalogCoversRoutePermissions(t *testing.T) {
	grants := map[identity.Permission]struct{}{}
	for _, p := range identity.AllPermissions() {
		grants[p] = struct{}{}
	}

	seen := map[identity.Permission]struct{}{}
	for _, key := range readPermissionFor {
		seen[key] = struct{}{}
	}
	for _, key := range writePermissionOverrides {
		seen[key] = struct{}{}
	}
	for _, rule := range routeRules {
		seen[rule.permission] = struct{}{}
	}
	for resource, read := range readPermissionFor {
		if _, ok := writePermissionOverrides[resource]; !ok {
			if derived := writeFromRead(read); derived != read {
				seen[derived] = struct{}{}
			}
		}
	}

	for required := range seen {
		if _, ok := grants[required]; !ok {
			t.Errorf("route permission %q is never granted by the identity layer", required)
		}
	}
}
