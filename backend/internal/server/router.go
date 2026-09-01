// Package server owns the HTTP wiring of the Reticora API: it turns a set of
// repositories into handlers and registers every route on one chi router.
//
// Keeping the wiring here (instead of in cmd/server) means the route table can
// be built in tests, which is what the OpenAPI parity check relies on: the
// specification is compared against the very router the binary serves.
package server

import (
	"fmt"
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ai"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/compliance"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/contact"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/disposal"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/desk"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/training"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/keymgmt"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/form"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/graphqlbff"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/iga"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/location"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/maintenance"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/order"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/privacy"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rack"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/sla"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/stocktake"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenantapi"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/topology"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/workflow"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Repositories bundles every persistence port the API depends on.
type Repositories struct {
	CI                ci.Repository
	Relationship      relationship.Repository
	Webhook           webhook.Repository
	WebhookDeliveries webhook.DeliveryStore
	Discovery         discovery.Repository
	Asset             asset.Repository
	Assignment        assignment.Repository
	Document          document.Repository
	Stocktake         stocktake.Repository
	Consumable        consumable.Repository
	Order             order.Repository
	Maintenance       maintenance.Repository
	Disposal          disposal.Repository
	Key               keymgmt.Repository
	Training          training.Repository
	Desk              desk.Repository
	Location          location.Repository
	Ticket            ticket.Repository
	User              user.Repository
	Credential        credential.Repository
	Entitlement       entitlement.Repository
	TenantHierarchy   tenantapi.Repository
	Rack              rack.Repository
	Contact           contact.Repository
	IPAM              ipam.Repository
	Metrics           monitoring.MetricStore
	Permission        permission.Repository
	SLA               sla.Repository
	Form              form.Repository
	Workflow          workflow.Repository
	Compliance        compliance.Repository
	IGA               iga.Repository
	Search            search.Backend
	AI                ai.Repository
	ExportJobs        export.JobRepository
	Privacy           privacy.Repository
}

// Options carries everything the router needs beyond the repositories.
type Options struct {
	Version string
	// MetricsTenantLabel controls whether the organization_id label is
	// populated on the HTTP request metrics. It multiplies the series count by
	// the number of tenants, so it is opt-in for bounded-tenant deployments.
	MetricsTenantLabel bool
	// Entitlements resolves plans and gates add-on routes.
	Entitlements *entitlement.Service
	// Dispatcher publishes CI lifecycle events to webhook subscribers.
	Dispatcher *webhook.Dispatcher
	// CIService is the CI domain service (limit-aware).
	CIService *ci.Service
	// Credentials is the credential service (envelope encryption).
	Credentials *credential.Service
	// OIDC and Sessions power the authentication endpoints.
	OIDC     *identity.OIDCProvider
	Sessions *identity.SessionIssuer
	// UserProvisioner auto-creates the app_user on first OIDC login (nil
	// disables). DefaultProvisionRole names the standard role assigned on
	// first login (empty assigns none).
	UserProvisioner      identity.UserProvisioner
	DefaultProvisionRole string
	// Audit is registered only when a database-backed audit trail exists.
	Audit *audit.Handler
	// AuditPool enables the security report to include audit-chain integrity;
	// it is the same pool the audit handler serves from.
	AuditPool  *pgxpool.Pool
	AIProvider ai.Provider
	// Blobs persists asynchronous export results; nil disables export-job
	// creation (the streaming export endpoint stays available).
	Blobs blob.Store
}

// registrar is implemented by every domain handler.
type registrar interface {
	RegisterRoutes(r chi.Router)
}

// NewRouter builds the complete API router. Every route the server exposes is
// registered here; there is no other registration site. It also returns the
// tenant-aware HTTP metrics middleware, which the caller mounts on the outer
// middleware chain so requests are recorded into the same registry that serves
// /metrics.
func NewRouter(repos Repositories, opts Options) (*chi.Mux, func(http.Handler) http.Handler, error) {
	if err := validate(repos, opts); err != nil {
		return nil, nil, err
	}

	mux := chi.NewRouter()

	// Unknown and method-mismatched routes must answer with RFC 7807
	// problem+json like every other API error, not chi's plain-text default.
	mux.NotFound(func(w http.ResponseWriter, r *http.Request) {
		api.WriteError(w, http.StatusNotFound, "Not Found", "the requested resource does not exist")
	})
	mux.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		api.WriteError(w, http.StatusMethodNotAllowed, "Method Not Allowed", "the method is not allowed for this resource")
	})

	httpMetrics := registerOperational(mux, opts.Version, opts.MetricsTenantLabel)

	// Every domain route is registered through the authorizing router, which
	// attaches the permission middleware resolved from the route table.
	// Routes without a mapping fail closed; the router test locks in that
	// every route is mapped.
	protected := authorizingRouter{Router: mux}

	registrars := []registrar{
		identity.NewHandler(opts.OIDC, opts.Sessions).WithProvisioning(opts.UserProvisioner, opts.DefaultProvisionRole),
		entitlement.NewHandler(opts.Entitlements),
		ci.NewHandler(opts.CIService, opts.Dispatcher),
		relationship.NewHandler(repos.Relationship),
		webhook.NewHandler(repos.Webhook, opts.Dispatcher),
		discovery.NewHandler(repos.Discovery, repos.CI, repos.Relationship),
		topology.NewHandler(repos.CI, repos.Relationship),
		export.NewHandler(repos.CI),
		export.NewJobHandler(repos.ExportJobs, export.NewJobWorker(repos.ExportJobs, repos.CI, opts.Blobs), opts.Blobs),
		asset.NewHandler(repos.Asset),
		assignment.NewHandler(repos.Assignment),
		document.NewHandler(repos.Document, opts.Blobs),
		stocktake.NewHandler(repos.Stocktake),
		consumable.NewHandler(repos.Consumable),
		order.NewHandler(repos.Order),
		maintenance.NewHandler(repos.Maintenance),
		disposal.NewHandler(repos.Disposal),
		keymgmt.NewHandler(repos.Key),
		training.NewHandler(repos.Training),
		desk.NewHandler(repos.Desk),
		location.NewHandler(repos.Location),
		ticket.NewHandler(repos.Ticket, sla.TicketHooks{Repo: repos.SLA}),
		user.NewHandler(repos.User, repos.Contact).WithPrivacySources(repos.Ticket, repos.Assignment),
		permission.NewHandler(repos.Permission),
		search.NewHandler(repos.Search, repos.Permission),
		sla.NewHandler(repos.SLA, repos.Ticket),
		form.NewHandler(repos.Form),
		workflow.NewHandler(repos.Workflow, workflow.NewExecutor(repos.Workflow, repos.Ticket, repos.CI, repos.Form, opts.Dispatcher)),
		compliance.NewHandler(repos.Compliance, compliance.NewEvaluator(repos.Compliance, repos.CI)).
			WithReports(compliance.NewReportService(repos.Compliance, reportAuditVerifier(opts), entitlementLister{svc: opts.Entitlements})),
		iga.NewHandler(repos.IGA, repos.User, opts.Credentials, repos.Discovery, repos.Workflow),
		tenantapi.NewHandler(repos.TenantHierarchy),
		rack.NewHandler(repos.Rack),
		contact.NewHandler(repos.Contact),
		ipam.NewHandler(repos.IPAM),
		monitoring.NewHandler(repos.Metrics, alertStoreFor(repos.Metrics)),
		graphqlbff.NewHandler(repos.CI, repos.Relationship),
		credential.NewHandler(opts.Credentials),
		ai.NewHandler(repos.AI, opts.AIProvider, ai.NewRetriever(repos.AI, repos.Search, repos.Permission, opts.AIProvider)),
		privacy.NewHandler(privacy.NewService(repos.Privacy, repos.Contact, repos.User)),
	}
	for _, h := range registrars {
		h.RegisterRoutes(protected)
	}

	// The audit trail is hash-chained in PostgreSQL and therefore has no
	// in-memory counterpart; it is only served when a pool was configured.
	if opts.Audit != nil {
		opts.Audit.RegisterRoutes(protected)
	}

	return mux, httpMetrics, nil
}

// alertStoreFor pairs the alert-rule persistence with the metric store when
// the store provides one (PostgreSQL); otherwise the in-memory store is used
// and alert rules live in the default in-memory manager.
func alertStoreFor(store monitoring.MetricStore) monitoring.AlertStore {
	if evaluating, ok := store.(monitoring.EvaluatingStore); ok {
		return evaluating.AlertStore()
	}
	return nil
}

// reportAuditVerifier returns the audit-chain verifier for the security
// report when a database pool exists; without a database the report omits the
// integrity section instead of failing.
func reportAuditVerifier(opts Options) compliance.AuditVerifier {
	if opts.AuditPool == nil {
		return nil
	}
	return auditVerifier{pool: opts.AuditPool}
}

func validate(repos Repositories, opts Options) error {
	switch {
	case repos.Permission == nil:
		return fmt.Errorf("server: permission repository is required")
	case repos.SLA == nil:
		return fmt.Errorf("server: SLA repository is required")
	case repos.Form == nil:
		return fmt.Errorf("server: form repository is required")
	case repos.Workflow == nil:
		return fmt.Errorf("server: workflow repository is required")
	case repos.Compliance == nil:
		return fmt.Errorf("server: compliance repository is required")
	case repos.IGA == nil:
		return fmt.Errorf("server: IGA repository is required")
	case repos.Search == nil:
		return fmt.Errorf("server: search backend is required")
	case repos.AI == nil:
		return fmt.Errorf("server: AI repository is required")
	case repos.CI == nil:
		return fmt.Errorf("server: CI repository is required")
	case repos.Relationship == nil:
		return fmt.Errorf("server: relationship repository is required")
	case opts.CIService == nil:
		return fmt.Errorf("server: CI service is required")
	case opts.Entitlements == nil:
		return fmt.Errorf("server: entitlement service is required")
	case opts.Credentials == nil:
		return fmt.Errorf("server: credential service is required")
	}
	return nil
}

// registerOperational adds the unauthenticated health and metrics endpoints.
// It returns the HTTP metrics middleware so main can mount it on the outer
// middleware chain; the middleware records into the same registry that serves
// /metrics.
func registerOperational(mux *chi.Mux, version string, includeTenantLabel bool) func(http.Handler) http.Handler {
	mux.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"ok"}`)
	})
	handler, httpMetrics := metricsHandler(version, includeTenantLabel)
	mux.Handle("/metrics", handler)
	return httpMetrics
}

// metricsHandler serves application and Go runtime metrics in the Prometheus
// exposition format. The reticora_up/reticora_info series are registered on a
// dedicated registry so the endpoint stays stable regardless of what else is
// instrumented. It also registers the tenant-aware HTTP request metrics and
// returns the middleware that records them.
func metricsHandler(version string, includeTenantLabel bool) (http.Handler, func(http.Handler) http.Handler) {
	registry := prometheus.NewRegistry()

	up := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "reticora",
		Name:      "up",
		Help:      "Whether the Reticora server is up.",
	})
	up.Set(1)
	registry.MustRegister(up)

	info := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "reticora",
		Name:      "info",
		Help:      "Build and version information.",
	}, []string{"version"})
	info.WithLabelValues(version).Set(1)
	registry.MustRegister(info)

	registry.MustRegister(collectors.NewGoCollector())

	httpMetrics := middleware.RegisterHTTPMetrics(registry, includeTenantLabel)

	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{}), httpMetrics
}
