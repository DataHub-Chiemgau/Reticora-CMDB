package entitlement

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/prometheus/client_golang/prometheus"
)

// featureRule maps requests to the feature keys required to use them (ENT-03,
// ENT-05, ENT-06, E-44). method is empty for every method. Without suffix
// the rule gates prefix and every path below it; with suffix it gates the
// sub-path suffix of one resource below prefix (prefix/{id}suffix), the
// paths below it and its file forms (prefix/{id}suffix.svg). A request
// needs every key of the rule.
type featureRule struct {
	method   string
	prefix   string
	suffix   string
	features []string
}

// rank orders the rules: suffix rules (with or without method) before
// method rules before plain prefix rules (WP-705).
func (r featureRule) rank() int {
	switch {
	case r.suffix != "":
		return 0
	case r.method != "":
		return 1
	default:
		return 2
	}
}

func (r featureRule) matches(method, path string) bool {
	if r.method != "" && r.method != method {
		return false
	}
	if r.suffix == "" {
		return path == r.prefix || strings.HasPrefix(path, r.prefix+"/") || strings.HasPrefix(path, r.prefix+"?")
	}
	rest, ok := strings.CutPrefix(strings.TrimSuffix(path, "/"), r.prefix+"/")
	if !ok {
		return false
	}
	id, sub, ok := strings.Cut(rest, "/")
	if !ok || id == "" {
		return false
	}
	sub = "/" + sub
	return sub == r.suffix || strings.HasPrefix(sub, r.suffix+"/") || strings.HasPrefix(sub, r.suffix+".")
}

func gate(prefix string, features ...string) featureRule {
	return featureRule{prefix: prefix, features: features}
}

// featureRules is the only route→feature mapping of the org gate (WP-705, E-44,
// E-38, E-39); the module test of package server reads it through
// RequiredFeature. Paths that are not listed belong to the always-available
// core (CMDB, tenants, users, auth, audit and the entitlement API itself),
// among them the location tree and the review queue; writes of rack nodes
// of the tree and the resolution of scope_suggested items check their
// feature in the handler (F-400). Sub-functions without a path of their own
// (a signature in the body, format=datev) check it in the handler through
// Checker; docs/README.md lists the mapping.
var featureRules = []featureRule{
	gate("/api/v1/ingest", FeatureDiscovery),
	gate("/api/v1/discovery", FeatureDiscovery),
	// The discovery scopes are a resource of their own, not below
	// /api/v1/discovery/ (F-400).
	gate("/api/v1/discovery-scopes", FeatureDiscovery),
	gate("/api/v1/collectors", FeatureDiscovery),
	gate("/api/v1/credentials", FeatureDiscovery),

	// Inventory (AST, E-44, E-38): the ledger is read with assets_basic,
	// quantity bookings and transfers need stock; labels need barcode, the
	// GPS history (LOC-09, P4) gps_standard.
	gate("/api/v1/assets", FeatureAssetsBasic),
	{prefix: "/api/v1/assets", suffix: "/label", features: []string{FeatureBarcode, FeatureAssetsBasic}},
	{prefix: "/api/v1/assets", suffix: "/locations", features: []string{FeatureGPSStandard}},
	gate("/api/v1/asset-locations", FeatureGPSStandard),
	gate("/api/v1/assignments", FeatureAssetsBasic),
	gate("/api/v1/reservations", FeatureAssetsBasic),
	gate("/api/v1/compositions", FeatureAssetsBasic),
	gate("/api/v1/disposal-records", FeatureAssetsBasic),
	gate("/api/v1/suppliers", FeatureAssetsBasic),
	gate("/api/v1/contracts", FeatureAssetsBasic),
	{method: http.MethodGet, prefix: "/api/v1/stock-movements", features: []string{FeatureAssetsBasic}},
	{method: http.MethodPost, prefix: "/api/v1/stock-movements", features: []string{FeatureStock}},
	{method: http.MethodGet, prefix: "/api/v1/stock-movements", suffix: "/signature", features: []string{FeatureSignature}},
	// Any other method on the ledger is stock, so no method stays ungated.
	gate("/api/v1/stock-movements", FeatureStock),
	gate("/api/v1/consumables", FeatureStock),
	gate("/api/v1/inventory", FeatureStock),
	gate("/api/v1/labels", FeatureBarcode),
	// Every stocktake path and method is stocktake_standard (INV-02,
	// WP-288); Pro and delegated stocktake (P4) have no route and no key.
	gate("/api/v1/stocktakes", FeatureStocktakeStandard),
	gate("/api/v1/orders", FeatureInternalOrders),
	gate("/api/v1/documents", FeatureDocuments),
	gate("/api/v1/import-profiles", FeatureImport),
	gate("/api/v1/imports", FeatureImport),

	// Ticketing (TKT-01, E-39): SLAs and business calendars are standard.
	gate("/api/v1/tickets", FeatureTicketingEssential),
	{prefix: "/api/v1/tickets", suffix: "/sla", features: []string{FeatureTicketingStandard}},
	// The attachments of a ticket are documents (SR-61, WP-874): both
	// features, like /label (x-feature stays ticketing_essential).
	{prefix: "/api/v1/tickets", suffix: "/attachments", features: []string{FeatureTicketingEssential, FeatureDocuments}},
	gate("/api/v1/slas", FeatureTicketingStandard),
	gate("/api/v1/business-calendars", FeatureTicketingStandard),
	// Reading the review queue stays core; opening a ticket from an item
	// is ticketing (WP-291).
	{prefix: "/api/v1/review-items", suffix: "/ticket", features: []string{FeatureTicketingEssential}},

	gate("/api/v1/maintenance-windows", FeatureMonitoring),
	gate("/api/v1/keys", FeatureComplianceFindings),
	gate("/api/v1/trainings", FeatureWorkflows),
	gate("/api/v1/training-assignments", FeatureWorkflows),
	gate("/api/v1/desks", FeatureWorkplace),
	gate("/api/v1/desk-bookings", FeatureWorkplace),
	gate("/api/v1/agents", FeatureEndpointAgent),
	gate("/api/v1/security", FeatureComplianceFindings),
	gate("/api/v1/webhooks", FeatureWebhooks),
	// Racks, their layout and the mounts are the rack_view feature (ENT-02,
	// ENT-05, F-070).
	gate("/api/v1/racks", FeatureRackView),
	gate("/api/v1/mounts", FeatureRackView),
	gate("/api/v1/rack-mounts", FeatureRackView),
	// Managing API keys is the api_access feature; requests authenticated
	// with a key need it as well (Middleware).
	gate("/api/v1/api-keys", FeatureAPIAccess),
	gate("/api/v1/exports", FeatureExportCSV),
	gate("/api/v1/export", FeatureExportCSV),
	gate("/api/v1/topology", FeatureTopology),
	// Impact analysis is part of the topology feature (API-05 path).
	gate("/api/v1/impact", FeatureTopology),
	// Sub-resources of the core CIs.
	{prefix: "/api/v1/cis", suffix: "/dependencies", features: []string{FeatureTopology}},
	{prefix: "/api/v1/cis", suffix: "/blast-radius", features: []string{FeatureTopology}},
	{prefix: "/api/v1/cis", suffix: "/metrics", features: []string{FeatureMonitoring}},

	// Phase 3 (WP-865, E-151, E-160, decision of the client of 2026-11-09).
	// Every prefix and sub-path rule of the P3 modules stands here only;
	// the domain work packages do not change this table (convention 31).
	// SLA and forecasts are analytics and precede /api/v1/monitoring; the
	// recommendation of an incident (ALR-09) is monitoring_network.
	gate("/api/v1/monitoring/sla", FeatureMonitoringAnalytics),
	gate("/api/v1/monitoring/forecasts", FeatureMonitoringAnalytics),
	{prefix: "/api/v1/monitoring/incidents", suffix: "/recommendation", features: []string{FeatureMonitoringNetwork}},
	gate("/api/v1/collector-groups", FeatureMonitoring),
	{prefix: "/api/v1/cis", suffix: "/availability", features: []string{FeatureMonitoring}},
	gate("/api/v1/credential-bindings", FeatureMonitoring),
	{prefix: "/api/v1/cis", suffix: "/credentials", features: []string{FeatureMonitoring}},
	gate("/api/v1/vault-connections", FeatureCredentialVault),
	{prefix: "/api/v1/cis", suffix: "/configs", features: []string{FeatureMonitoringNetwork}},
	gate("/api/v1/ipam", FeatureIPAM),
	gate("/api/v1/ipam-sections", FeatureIPAM),
	gate("/api/v1/asns", FeatureIPAM),
	gate("/api/v1/asn-ranges", FeatureIPAM),
	gate("/api/v1/bgp-peerings", FeatureIPAM),
	gate("/api/v1/nat-translations", FeatureIPAM),
	gate("/api/v1/nameserver-sets", FeatureIPAM),
	gate("/api/v1/vlan-domains", FeatureIPAM),
	{prefix: "/api/v1/cis", suffix: "/addresses", features: []string{FeatureIPAM}},
	{prefix: "/api/v1/subnets", suffix: "/scan", features: []string{FeatureIPAM, FeatureMonitoring}},
	{prefix: "/api/v1/subnets", suffix: "/rpki", features: []string{FeatureIPAM, FeatureMonitoringNetwork}},
	gate("/api/v1/admp", FeatureADMP),
	{prefix: "/api/v1/cis", suffix: "/admp-connections", features: []string{FeatureADMP}},
	{prefix: "/api/v1/network-interfaces", suffix: "/admp", features: []string{FeatureADMP}},
	gate("/api/v1/escalation-policies", FeatureNotificationsSMSPager),
	gate("/api/v1/notification-escalations", FeatureNotificationsSMSPager),
	// /api/v1/notification-callbacks (public provider callback, WP-1146)
	// has no feature rule: the signature of the provider authenticates it.
	gate("/api/v1/monitoring", FeatureMonitoring),
	gate("/api/v1/metrics", FeatureMonitoring),
	gate("/api/v1/forms", FeatureWorkflows),
	gate("/api/v1/form-submissions", FeatureWorkflows),
	gate("/api/v1/workflows", FeatureWorkflows),
	gate("/api/v1/workflow-runs", FeatureWorkflows),
	gate("/api/v1/compliance", FeatureComplianceFindings),
	gate("/api/v1/iga", FeatureIGA),
	gate("/scim/v2", FeatureIGA),
	gate("/api/v1/ai", FeatureAI),
}

// coreRoutes are core paths below a gated prefix. The review queue is
// cmdb_core (REC-08: CI merges open review items as well): its deprecated
// alias below /api/v1/discovery follows its successor /api/v1/review-items
// and GraphQL reviewInbox (F-400). Resolving a scope_suggested item needs
// the discovery feature in the discovery effect instead.
//
// The full export and the deletion of an organization (TLC-05, TLC-06,
// WP-865) are core as well: they need no paid feature.
var coreRoutes = []string{"/api/v1/discovery/review-items", "/api/v1/org/exports", "/api/v1/org/deletion"}

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

// RequiredFeatures returns the feature keys gating the request: those of
// the first matching suffix rule, else of the first method rule, else of
// the first prefix rule (WP-705). Nil means the core.
func RequiredFeatures(method, path string) []string {
	// The enrollment endpoint is the pre-entitlement onboarding entry point:
	// the collector has no tenant context yet, the single-use code is the
	// credential, and enrollment must work before any plan check can pass.
	if path == "/api/v1/collectors/enroll" {
		return nil
	}
	for _, prefix := range coreRoutes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") || strings.HasPrefix(path, prefix+"?") {
			return nil
		}
	}
	for rank := range 3 {
		for _, rule := range featureRules {
			if rule.rank() == rank && rule.matches(method, path) {
				return rule.features
			}
		}
	}
	return nil
}

// RequiredFeature returns the feature key gating the request: the first
// key of its rule (barcode for assets/{id}/label; Middleware requires the
// others as well).
func RequiredFeature(method, path string) (string, bool) {
	features := RequiredFeatures(method, path)
	if len(features) == 0 {
		return "", false
	}
	return features[0], true
}

// Checker is the port for feature checks in handlers and services, for
// sub-functions without a path of their own (a signature in the body of
// assign, format=datev; E-44). *Service implements it; a failed lookup is
// "not entitled".
type Checker interface {
	IsEnabled(ctx context.Context, orgID, featureKey string) bool
}

var _ Checker = (*Service)(nil)

// RequireFeature is the route middleware for a sub-function below a module
// prefix (ENT-03): without the feature the request is refused with 403
// feature-not-entitled. The org gate of whole prefixes is Middleware.
func (s *Service) RequireFeature(key string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			orgID := tenant.FromContext(r.Context()).OrganizationID
			if orgID == "" {
				api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
				return
			}
			_, ok, err := s.Check(r.Context(), orgID, key)
			if err != nil {
				api.WriteError(w, http.StatusInternalServerError, "Internal Error", "entitlement lookup failed")
				return
			}
			if !ok {
				writeNotEntitled(w, key)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// writeNotEntitled writes 403 feature-not-entitled for the feature; an
// exhausted quota (LimitExceededError, AllowCreate) goes through the same
// api.WriteEntitlementError as 403 entitlement-limit.
func writeNotEntitled(w http.ResponseWriter, feature string) {
	api.WriteEntitlementError(w, &FeatureNotEntitledError{FeatureKey: feature})
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
		features := RequiredFeatures(r.Method, r.URL.Path)
		principal, _ := identity.PrincipalFromContext(r.Context())
		apiKey := principal.Type == identity.PrincipalTypeAPIKey
		if len(features) == 0 && !apiKey {
			next.ServeHTTP(w, r)
			return
		}

		orgID := tenant.FromContext(r.Context()).OrganizationID
		if orgID == "" {
			api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
			return
		}

		if apiKey && !slices.Contains(features, FeatureAPIAccess) {
			_, ok, err := s.Check(r.Context(), orgID, FeatureAPIAccess)
			if err != nil {
				api.WriteError(w, http.StatusInternalServerError, "Internal Error", "entitlement lookup failed")
				return
			}
			if !ok {
				writeNotEntitled(w, FeatureAPIAccess)
				return
			}
		}

		for _, feature := range features {
			if !s.allow(w, r, orgID, feature) {
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// allow checks one feature of the request and writes the refusal. After
// the discovery license expired only ingest and new scans are refused
// (CH21, ENT-07).
func (s *Service) allow(w http.ResponseWriter, r *http.Request, orgID, feature string) bool {
	ent, status, err := s.Status(r.Context(), orgID, feature)
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", "entitlement lookup failed")
		return false
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
			return false
		}
	default:
		writeNotEntitled(w, feature)
		return false
	}
	return true
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
