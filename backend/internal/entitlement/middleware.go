package entitlement

import (
	"net/http"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// gatedRoutes maps API path prefixes to the feature key required to use them.
// Paths that are not listed belong to the always-available core (CMDB, tenants,
// users, auth, audit and the entitlement API itself).
var gatedRoutes = []struct {
	prefix  string
	feature string
}{
	{"/api/v1/discovery", FeatureDiscovery},
	{"/api/v1/collectors", FeatureDiscovery},
	{"/api/v1/credentials", FeatureDiscovery},
	{"/api/v1/assets", FeatureInventory},
	{"/api/v1/assignments", FeatureInventory},
	{"/api/v1/documents", FeatureDocuments},
	{"/api/v1/stocktakes", FeatureStocktake},
	{"/api/v1/stocktake", FeatureStocktake},
	{"/api/v1/tickets", FeatureTicketing},
	{"/api/v1/webhooks", FeatureWebhooks},
	{"/api/v1/exports", FeatureExport},
	{"/api/v1/monitoring", FeatureMonitoring},
	{"/api/v1/metrics", FeatureMonitoring},
	{"/api/v1/forms", FeatureWorkflowForms},
	{"/api/v1/form-submissions", FeatureWorkflowForms},
	{"/api/v1/workflows", FeatureWorkflowForms},
	{"/api/v1/workflow-runs", FeatureWorkflowForms},
	{"/api/v1/compliance", FeatureCompliance},
	{"/api/v1/iga", FeatureIGA},
	{"/scim/v2", FeatureIGA},
}

// RequiredFeature returns the feature key gating the given request path.
func RequiredFeature(path string) (string, bool) {
	for _, route := range gatedRoutes {
		if path == route.prefix || strings.HasPrefix(path, route.prefix+"/") || strings.HasPrefix(path, route.prefix+"?") {
			return route.feature, true
		}
	}
	return "", false
}

// Middleware blocks requests to modules the tenant is not entitled to. It must
// run after the tenant middleware so the organization is known.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		feature, gated := RequiredFeature(r.URL.Path)
		if !gated {
			next.ServeHTTP(w, r)
			return
		}

		orgID := tenant.FromContext(r.Context()).OrganizationID
		if orgID == "" {
			api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
			return
		}

		_, allowed, err := s.Check(r.Context(), orgID, feature)
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, "Internal Error", "entitlement lookup failed")
			return
		}
		if !allowed {
			api.WriteError(w, http.StatusForbidden, "Forbidden",
				(&FeatureNotEntitledError{FeatureKey: feature}).Error())
			return
		}

		next.ServeHTTP(w, r)
	})
}
