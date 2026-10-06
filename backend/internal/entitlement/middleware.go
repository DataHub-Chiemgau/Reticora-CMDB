package entitlement

import (
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/prometheus/client_golang/prometheus"
)

// gatedRoutes maps API path prefixes to the feature key required to use them.
// Paths that are not listed belong to the always-available core (CMDB, tenants,
// users, auth, audit and the entitlement API itself).
var gatedRoutes = []struct {
	prefix  string
	feature string
}{
	{"/api/v1/ingest", FeatureDiscovery},
	{"/api/v1/discovery", FeatureDiscovery},
	{"/api/v1/collectors", FeatureDiscovery},
	{"/api/v1/credentials", FeatureDiscovery},
	{"/api/v1/assets", FeatureInventory},
	{"/api/v1/assignments", FeatureInventory},
	{"/api/v1/documents", FeatureDocuments},
	{"/api/v1/stocktakes", FeatureStocktake},
	{"/api/v1/stocktake", FeatureStocktake},
	{"/api/v1/consumables", FeatureStocktake},
	{"/api/v1/orders", FeatureWorkflowForms},
	{"/api/v1/maintenance-windows", FeatureMonitoring},
	{"/api/v1/disposal-records", FeatureCompliance},
	{"/api/v1/keys", FeatureCompliance},
	{"/api/v1/trainings", FeatureWorkflowForms},
	{"/api/v1/training-assignments", FeatureWorkflowForms},
	{"/api/v1/desks", FeatureInventory},
	{"/api/v1/asset-locations", FeatureInventory},
	{"/api/v1/agents", FeatureEndpointAgent},
	{"/api/v1/security", FeatureCompliance},
	{"/api/v1/desk-bookings", FeatureInventory},
	{"/api/v1/tickets", FeatureTicketing},
	{"/api/v1/webhooks", FeatureWebhooks},
	{"/api/v1/exports", FeatureExportCSV},
	{"/api/v1/export", FeatureExportCSV},
	{"/api/v1/topology", FeatureTopology},
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

// gatedSuffixes gate sub-resources of core resources (/api/v1/cis/{id}/...).
var gatedSuffixes = []struct {
	prefix, suffix, feature string
}{
	{"/api/v1/cis/", "/dependencies", FeatureTopology},
	{"/api/v1/cis/", "/blast-radius", FeatureTopology},
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
// working so the collector learns that it is paused. It must run after the
// tenant middleware so the organization is known.
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
