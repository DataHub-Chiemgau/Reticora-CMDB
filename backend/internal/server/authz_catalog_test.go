package server

import (
	"strings"
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

// TestRBA01KeysGateTheirActions covers WP-068 (RBA-01): the actions RBA-01
// names are gated by exactly their key, location:* replaces site:*, and no
// route requires a key the RBA-01 text replaced.
func TestRBA01KeysGateTheirActions(t *testing.T) {
	for _, tc := range []struct{ method, path, want string }{
		{"GET", "/api/v1/sites", "location:read"},
		{"GET", "/api/v1/locations/tree", "location:read"},
		{"POST", "/api/v1/locations", "location:write"},
		{"DELETE", "/api/v1/rooms/x", "location:write"},
		{"POST", "/api/v1/collectors", "collector:manage"},
		{"POST", "/api/v1/collectors/enrollment-codes", "collector:manage"},
		{"POST", "/api/v1/collectors/x/heartbeat", "discovery:ingest"},
		{"POST", "/api/v1/discovery/jobs", "discovery:manage"},
		{"POST", "/api/v1/discovery/review-items/x/resolve", "review:resolve"},
		{"GET", "/api/v1/export/jobs", "job:read"},
		{"GET", "/api/v1/export/jobs/x", "job:read"},
		{"POST", "/api/v1/export/jobs", "export:run"},
		{"POST", "/api/v1/teams", "team:manage"},
		{"POST", "/api/v1/teams/x/members", "team:manage"},
		{"POST", "/api/v1/api-keys", "apikey:manage"},
		{"DELETE", "/api/v1/cis/x", "ci:delete"},
		{"POST", "/api/v1/webhooks", "webhook:manage"},
	} {
		if got, _ := PermissionForRoute(tc.method, tc.path); string(got) != tc.want {
			t.Errorf("%s %s requires %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}
	for _, op := range routeTable(t, testRouter(t)) {
		required, _ := PermissionForRoute(op.Method, op.Path)
		switch key := string(required); {
		case strings.HasPrefix(key, "site:"), key == "discovery:write", key == "reconciliation:resolve":
			t.Errorf("%s %s requires the replaced key %s", op.Method, op.Path, key)
		}
	}
}
