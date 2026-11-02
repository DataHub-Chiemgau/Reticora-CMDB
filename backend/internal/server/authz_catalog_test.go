package server

import (
	"net/http"
	"net/http/httptest"
	"slices"
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

// rba01Keys are the permissions RBA-01 names (catalogue part 02).
var rba01Keys = []identity.Permission{
	"ci:read", "ci:write", "ci:delete", "citype:manage", "location:read", "location:write", "rack:write",
	"relationship:write", "contact:write", "topology:read", "discovery:ingest", "discovery:manage",
	"collector:manage", "credential:manage", "vrf:manage", "review:resolve", "webhook:manage", "export:run",
	"job:read", "notification:manage", "user:manage", "team:manage", "role:manage", "entitlement:manage",
	"audit:read", "apikey:manage",
}

// unroutedKeys are the catalogue keys no tenant route requires, with the
// reason (F-199). Every other catalogue key gates at least one route.
var unroutedKeys = map[string]string{
	// RBA-02 grants it to org_admin; entitlements are written only on the
	// operator path /admin/orgs/{id}/entitlements (ENT-04, F-211).
	"entitlement:manage": "operator path only (ENT-04)",
}

// TestRBA01KeysGateTheirActions covers WP-068 and WP-363 (RBA-01): the
// actions RBA-01 names are gated by exactly their key, location:* replaces
// site:*, no route requires a key the RBA-01 text replaced, every RBA-01 key
// gates at least one route, and every catalogue key is required by a route
// or listed in unroutedKeys (F-199, F-211).
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
		{"POST", "/api/v1/ci-types", "citype:manage"},
		{"PUT", "/api/v1/ci-types/x/fields", "citype:manage"},
		{"GET", "/api/v1/ci-types", "ci:read"},
		{"GET", "/api/v1/notification-channels", "notification:manage"},
		{"PATCH", "/api/v1/notification-channels/x", "notification:manage"},
		{"GET", "/api/v1/notification-deliveries", "notification:manage"},
		{"GET", "/api/v1/notifications", ""},
		{"PUT", "/api/v1/notifications/preferences", ""},
	} {
		if got, _ := PermissionForRoute(tc.method, tc.path); string(got) != tc.want {
			t.Errorf("%s %s requires %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}
	routed := map[string]bool{}
	for _, op := range routeTable(t, testRouter(t)) {
		required, access := PermissionForRoute(op.Method, op.Path)
		switch key := string(required); {
		case strings.HasPrefix(key, "site:"), key == "discovery:write", key == "reconciliation:resolve",
			key == "ci_type:manage":
			t.Errorf("%s %s requires the replaced key %s", op.Method, op.Path, key)
		case key == "entitlement:manage" && access == routeProtected:
			t.Errorf("%s %s: a tenant route requires entitlement:manage (ENT-04: the operator writes)", op.Method, op.Path)
		}
		if access == routeProtected && required != "" {
			routed[string(required)] = true
		}
	}
	for _, key := range rba01Keys {
		if _, exempt := unroutedKeys[string(key)]; !routed[string(key)] && !exempt {
			t.Errorf("RBA-01 key %s gates no route", key)
		}
	}
	for _, p := range permission.Catalogue {
		_, exempt := unroutedKeys[p.Key]
		switch {
		case routed[p.Key] && exempt:
			t.Errorf("catalogue key %s gates a route but is listed as unrouted", p.Key)
		case !routed[p.Key] && !exempt:
			t.Errorf("catalogue key %s gates no route and is not listed in unroutedKeys (F-199)", p.Key)
		}
	}
	if len(unroutedKeys) != 1 {
		t.Errorf("unroutedKeys %v, want entitlement:manage only", unroutedKeys)
	}
}

// authorizeWith runs the route authorization of method and path for a user
// holding exactly perms and returns the status (200 when admitted).
func authorizeWith(method, path string, perms ...identity.Permission) int {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	req := httptest.NewRequest(method, path, nil)
	p := identity.Principal{Subject: "u", OrganizationID: "o", Type: identity.PrincipalTypeUser, Permissions: perms}
	w := httptest.NewRecorder()
	AuthorizeRoute(method, path)(ok).ServeHTTP(w, req.WithContext(identity.WithPrincipal(req.Context(), p)))
	return w.Code
}

// TestAuditT2RBA01KeysGateCITypesAndNotificationChannels covers WP-363
// (RBA-01, F-067): RBA-01 names citype:manage for CI-type management and
// notification:manage for the notification channels. A custom role built
// from the RBA-01 vocabulary must work, and webhook:manage (the webhook
// subscriptions) must not grant the notification channels with their
// provider credentials. The catalogue has no parallel near-duplicate of an
// RBA-01 key.
//
// Retest:
//
//	cd backend && go test -count=1 -v -run '^TestAuditT2RBA01KeysGateCITypesAndNotificationChannels$' ./internal/server/
func TestAuditT2RBA01KeysGateCITypesAndNotificationChannels(t *testing.T) {
	const ciType = "/api/v1/ci-types/7a2a0000-0000-4000-8000-000000000101"
	const channel = "/api/v1/notification-channels/7a2a0000-0000-4000-8000-000000000102"
	webhookAdmin := []identity.Permission{identity.PermWebhookManage, identity.PermWebhookRead}
	for _, c := range []struct {
		method, path string
		perms        []identity.Permission
		want         int
	}{
		{http.MethodPost, "/api/v1/ci-types", []identity.Permission{identity.PermCITypeManage}, http.StatusOK},
		{http.MethodPatch, ciType, []identity.Permission{identity.PermCITypeManage}, http.StatusOK},
		{http.MethodDelete, ciType, []identity.Permission{identity.PermCITypeManage}, http.StatusOK},
		{http.MethodPut, ciType + "/fields", []identity.Permission{identity.PermCITypeManage}, http.StatusOK},
		{http.MethodPost, ciType + "/clone", []identity.Permission{identity.PermCITypeManage}, http.StatusOK},
		{http.MethodPost, "/api/v1/ci-types", []identity.Permission{identity.PermCIRead, identity.PermCIWrite}, http.StatusForbidden},
		{http.MethodPost, "/api/v1/notification-channels", []identity.Permission{identity.PermNotificationManage}, http.StatusOK},
		{http.MethodGet, "/api/v1/notification-channels", []identity.Permission{identity.PermNotificationManage}, http.StatusOK},
		{http.MethodGet, channel, []identity.Permission{identity.PermNotificationManage}, http.StatusOK},
		{http.MethodPatch, channel, []identity.Permission{identity.PermNotificationManage}, http.StatusOK},
		{http.MethodDelete, channel, []identity.Permission{identity.PermNotificationManage}, http.StatusOK},
		{http.MethodGet, "/api/v1/notification-deliveries", []identity.Permission{identity.PermNotificationManage}, http.StatusOK},
		{http.MethodPost, "/api/v1/notification-channels", webhookAdmin, http.StatusForbidden},
		{http.MethodGet, "/api/v1/notification-channels", webhookAdmin, http.StatusForbidden},
		{http.MethodGet, channel, webhookAdmin, http.StatusForbidden},
		{http.MethodPatch, channel, webhookAdmin, http.StatusForbidden},
		{http.MethodDelete, channel, webhookAdmin, http.StatusForbidden},
		{http.MethodGet, "/api/v1/notification-deliveries", webhookAdmin, http.StatusForbidden},
		// The own inbox stays self-service (NTF-04).
		{http.MethodGet, "/api/v1/notifications", nil, http.StatusOK},
		{http.MethodPut, "/api/v1/notifications/preferences", nil, http.StatusOK},
	} {
		if got := authorizeWith(c.method, c.path, c.perms...); got != c.want {
			t.Errorf("%s %s with only %v -> %d, want %d (RBA-01)", c.method, c.path, c.perms, got, c.want)
		}
	}

	// No catalogue key may be a near-duplicate of an RBA-01 key
	// (ci_type:manage next to citype:manage).
	normalized := map[string]identity.Permission{}
	for _, k := range rba01Keys {
		normalized[strings.ReplaceAll(string(k), "_", "")] = k
	}
	for _, k := range identity.AllPermissions() {
		if slices.Contains(rba01Keys, k) {
			continue
		}
		if twin, ok := normalized[strings.ReplaceAll(string(k), "_", "")]; ok {
			t.Errorf("permission catalogue holds %q next to the RBA-01 key %q", k, twin)
		}
	}
	for _, p := range permission.Catalogue {
		if k := identity.Permission(p.Key); !slices.Contains(rba01Keys, k) {
			if twin, ok := normalized[strings.ReplaceAll(p.Key, "_", "")]; ok {
				t.Errorf("permission catalogue holds %q next to the RBA-01 key %q", k, twin)
			}
		}
	}
}
