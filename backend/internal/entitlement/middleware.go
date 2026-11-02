package entitlement

import (
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/prometheus/client_golang/prometheus"
)

// gatedRoutes maps API path prefixes to the feature key required to use them.
// Paths that are not listed belong to the always-available core (CMDB, tenants,
// users, auth, audit and the entitlement API itself), among them the
// location tree and the review queue; writes of rack nodes of the tree and
// the resolution of scope_suggested items check their feature in the
// handler (F-400). The phase-2 modules use the keys of ENT-06 (E-44,
// WP-244); docs/README.md lists the mapping.
var gatedRoutes = []struct {
	prefix  string
	feature string
}{
	{"/api/v1/ingest", FeatureDiscovery},
	{"/api/v1/discovery", FeatureDiscovery},
	// The discovery scopes are a resource of their own, not below
	// /api/v1/discovery/ (F-400).
	{"/api/v1/discovery-scopes", FeatureDiscovery},
	{"/api/v1/collectors", FeatureDiscovery},
	{"/api/v1/credentials", FeatureDiscovery},
	{"/api/v1/assets", FeatureAssetsBasic},
	{"/api/v1/assignments", FeatureAssetsBasic},
	{"/api/v1/documents", FeatureDocuments},
	{"/api/v1/stocktakes", FeatureStocktakeStandard},
	{"/api/v1/stocktake", FeatureStocktakeStandard},
	{"/api/v1/consumables", FeatureStock},
	{"/api/v1/orders", FeatureInternalOrders},
	{"/api/v1/maintenance-windows", FeatureMonitoring},
	{"/api/v1/disposal-records", FeatureAssetsBasic},
	{"/api/v1/keys", FeatureCompliance},
	{"/api/v1/trainings", FeatureWorkflowForms},
	{"/api/v1/training-assignments", FeatureWorkflowForms},
	{"/api/v1/desks", FeatureWorkplace},
	{"/api/v1/asset-locations", FeatureGPSStandard},
	{"/api/v1/agents", FeatureEndpointAgent},
	{"/api/v1/security", FeatureCompliance},
	{"/api/v1/desk-bookings", FeatureWorkplace},
	{"/api/v1/tickets", FeatureTicketingEssential},
	{"/api/v1/webhooks", FeatureWebhooks},
	// Racks, their layout and the mounts are the rack_view feature (ENT-02,
	// ENT-05, F-070).
	{"/api/v1/racks", FeatureRackView},
	{"/api/v1/mounts", FeatureRackView},
	{"/api/v1/rack-mounts", FeatureRackView},
	// Managing API keys is the api_access feature; requests authenticated
	// with a key need it as well (Middleware).
	{"/api/v1/api-keys", FeatureAPIAccess},
	{"/api/v1/exports", FeatureExportCSV},
	{"/api/v1/export", FeatureExportCSV},
	{"/api/v1/topology", FeatureTopology},
	// Impact analysis is part of the topology feature (API-05 path).
	{"/api/v1/impact", FeatureTopology},
	{"/api/v1/monitoring", FeatureMonitoring},
	{"/api/v1/metrics", FeatureMonitoring},
	{"/api/v1/forms", FeatureWorkflowForms},
	{"/api/v1/form-submissions", FeatureWorkflowForms},
	{"/api/v1/workflows", FeatureWorkflowForms},
	{"/api/v1/workflow-runs", FeatureWorkflowForms},
	{"/api/v1/compliance", FeatureCompliance},
	{"/api/v1/iga", FeatureIGA},
	{"/scim/v2", FeatureIGA},
	{"/api/v1/ai", FeatureAI},
}

// coreRoutes are core paths below a gated prefix. The review queue is
// cmdb_core (REC-08: CI merges open review items as well): its deprecated
// alias below /api/v1/discovery follows its successor /api/v1/review-items
// and GraphQL reviewInbox (F-400). Resolving a scope_suggested item needs
// the discovery feature in the discovery effect instead.
var coreRoutes = []string{"/api/v1/discovery/review-items"}

// gatedSuffixes gate sub-resources of core resources (/api/v1/cis/{id}/...).
var gatedSuffixes = []struct {
	prefix, suffix, feature string
}{
	{"/api/v1/cis/", "/dependencies", FeatureTopology},
	{"/api/v1/cis/", "/blast-radius", FeatureTopology},
	{"/api/v1/cis/", "/metrics", FeatureMonitoring},
}

// ingestRoutes deliver discovery results or start scans; they are the only
// requests stopped after the discovery license expired (CH21). Reading
// collectors, jobs and review items stays available.
var ingestRoutes = []struct{ method, path string }{
	{http.MethodPost, "/api/v1/ingest/bulk"},
	{http.MethodPost, "/api/v1/discovery/ingest"},
	{http.MethodPost, "/api/v1/discovery/jobs"},
}

// IsIngest reports whether the request delivers discovery results or starts a
// scan (CH21).
func IsIngest(method, path string) bool {
	path = strings.TrimSuffix(path, "/")
	for _, route := range ingestRoutes {
		if method == route.method && path == route.path {
			return true
		}
	}
	return false
}

// LicenseStatusHeader carries the discovery license status ("active" or
// "expired") on discovery responses, the collector heartbeat among them.
const LicenseStatusHeader = "Reticora-License-Status"

var refusedIngest atomic.Int64

// RefusedIngest is the number of ingest requests refused after the license
// expired since start (ENT-07).
func RefusedIngest() int64 { return refusedIngest.Load() }

// RequiredFeature returns the feature key gating the given request path.
func RequiredFeature(path string) (string, bool) {
	// The enrollment endpoint is the pre-entitlement onboarding entry point:
	// the collector has no tenant context yet, the single-use code is the
	// credential, and enrollment must work before any plan check can pass.
	if path == "/api/v1/collectors/enroll" {
		return "", false
	}
	for _, prefix := range coreRoutes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") || strings.HasPrefix(path, prefix+"?") {
			return "", false
		}
	}
	for _, route := range gatedRoutes {
		if path == route.prefix || strings.HasPrefix(path, route.prefix+"/") || strings.HasPrefix(path, route.prefix+"?") {
			return route.feature, true
		}
	}
	for _, route := range gatedSuffixes {
		if strings.HasPrefix(path, route.prefix) && strings.HasSuffix(strings.TrimSuffix(path, "/"), route.suffix) {
			return route.feature, true
		}
	}
	return "", false
}

// Middleware blocks requests to features the organization is not entitled
// to (ENT-03, ENT-05). After the discovery license expired only ingest and
// new scans are refused (CH21, ENT-07); the collector heartbeat keeps
// working so the collector learns that it is paused. A request
// authenticated with an API key needs api_access on every path; a session
// does not (F-070). It must run after the auth and tenant middleware so the
// principal and the organization are known.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		feature, gated := RequiredFeature(r.URL.Path)
		principal, _ := identity.PrincipalFromContext(r.Context())
		apiKey := principal.Type == identity.PrincipalTypeAPIKey
		if !gated && !apiKey {
			next.ServeHTTP(w, r)
			return
		}

		orgID := tenant.FromContext(r.Context()).OrganizationID
		if orgID == "" {
			api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
			return
		}

		if apiKey && feature != FeatureAPIAccess {
			_, ok, err := s.Check(r.Context(), orgID, FeatureAPIAccess)
			if err != nil {
				api.WriteError(w, http.StatusInternalServerError, "Internal Error", "entitlement lookup failed")
				return
			}
			if !ok {
				notEntitled := &FeatureNotEntitledError{FeatureKey: FeatureAPIAccess}
				api.WriteProblem(w, http.StatusForbidden, notEntitled.ProblemType(), "Forbidden", notEntitled.Error())
				return
			}
		}
		if !gated {
			next.ServeHTTP(w, r)
			return
		}

		ent, status, err := s.Status(r.Context(), orgID, feature)
		if err != nil {
			api.WriteError(w, http.StatusInternalServerError, "Internal Error", "entitlement lookup failed")
			return
		}
		if feature == FeatureDiscovery && status != StatusNotEntitled {
			// The collector reads it from the heartbeat response and pauses
			// or resumes (ENT-07).
			w.Header().Set(LicenseStatusHeader, string(status))
		}
		switch status {
		case StatusActive:
		case StatusExpired:
			if IsIngest(r.Method, r.URL.Path) {
				refusedIngest.Add(1)
				expired := &LicenseExpiredError{ValidUntil: *ent.ValidUntil}
				slog.Warn("ingest refused: discovery license expired",
					"organization_id", orgID, "path", r.URL.Path, "valid_until", ent.ValidUntil)
				api.WriteProblem(w, http.StatusForbidden, expired.ProblemType(), "Forbidden", expired.Error())
				return
			}
		default:
			notEntitled := &FeatureNotEntitledError{FeatureKey: feature}
			api.WriteProblem(w, http.StatusForbidden, notEntitled.ProblemType(), "Forbidden", notEntitled.Error())
			return
		}

		next.ServeHTTP(w, r)
	})
}

// RegisterMetrics registers reticora_ingest_refused_license_expired_total
// (ENT-07).
func RegisterMetrics(reg prometheus.Registerer) {
	reg.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{
		Namespace: "reticora",
		Name:      "ingest_refused_license_expired_total",
		Help:      "Ingest and scan requests refused because the discovery license expired (CH21).",
	}, func() float64 { return float64(RefusedIngest()) }))
}
