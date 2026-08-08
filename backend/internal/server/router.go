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
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/compliance"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/contact"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/form"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/graphqlbff"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/iga"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
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
}

// Options carries everything the router needs beyond the repositories.
type Options struct {
	Version string
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
	// Audit is registered only when a database-backed audit trail exists.
	Audit      *audit.Handler
	AIProvider ai.Provider
}

// registrar is implemented by every domain handler.
type registrar interface {
	RegisterRoutes(r chi.Router)
}

// NewRouter builds the complete API router. Every route the server exposes is
// registered here; there is no other registration site.
func NewRouter(repos Repositories, opts Options) (*chi.Mux, error) {
	if err := validate(repos, opts); err != nil {
		return nil, err
	}

	mux := chi.NewRouter()

	registerOperational(mux, opts.Version)

	// Every domain route is registered through the authorizing router, which
	// attaches the permission middleware resolved from the route table.
	// Routes without a mapping fail closed; the router test locks in that
	// every route is mapped.
	protected := authorizingRouter{Router: mux}

	registrars := []registrar{
		identity.NewHandler(opts.OIDC, opts.Sessions),
		entitlement.NewHandler(opts.Entitlements),
		ci.NewHandler(opts.CIService, opts.Dispatcher),
		relationship.NewHandler(repos.Relationship),
		webhook.NewHandler(repos.Webhook, opts.Dispatcher),
		discovery.NewHandler(repos.Discovery, repos.CI, repos.Relationship),
		topology.NewHandler(repos.CI, repos.Relationship),
		export.NewHandler(repos.CI),
		asset.NewHandler(repos.Asset),
		assignment.NewHandler(repos.Assignment),
		document.NewHandler(repos.Document),
		stocktake.NewHandler(repos.Stocktake),
		ticket.NewHandler(repos.Ticket, sla.TicketHooks{Repo: repos.SLA}),
		user.NewHandler(repos.User),
		permission.NewHandler(repos.Permission),
		search.NewHandler(repos.Search, repos.Permission),
		sla.NewHandler(repos.SLA, repos.Ticket),
		form.NewHandler(repos.Form),
		workflow.NewHandler(repos.Workflow, workflow.NewExecutor(repos.Workflow, repos.Ticket, repos.CI, repos.Form, opts.Dispatcher)),
		compliance.NewHandler(repos.Compliance, compliance.NewEvaluator(repos.Compliance, repos.CI)),
		iga.NewHandler(repos.IGA, repos.User, opts.Credentials, repos.Discovery, repos.Workflow),
		tenantapi.NewHandler(repos.TenantHierarchy),
		rack.NewHandler(repos.Rack),
		contact.NewHandler(repos.Contact),
		ipam.NewHandler(repos.IPAM),
		monitoring.NewHandler(repos.Metrics),
		graphqlbff.NewHandler(repos.CI, repos.Relationship),
		credential.NewHandler(opts.Credentials),
		ai.NewHandler(repos.AI, opts.AIProvider, ai.NewRetriever(repos.AI, repos.Search, repos.Permission, opts.AIProvider)),
	}
	for _, h := range registrars {
		h.RegisterRoutes(protected)
	}

	// The audit trail is hash-chained in PostgreSQL and therefore has no
	// in-memory counterpart; it is only served when a pool was configured.
	if opts.Audit != nil {
		opts.Audit.RegisterRoutes(protected)
	}

	return mux, nil
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
func registerOperational(mux *chi.Mux, version string) {
	mux.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"ok"}`)
	})
	mux.Get("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		// Application metrics in Prometheus exposition format. Once the
		// OpenTelemetry SDK is wired to a Prometheus exporter this is replaced
		// by promhttp.Handler().
		fmt.Fprint(w, "# HELP reticora_up Whether the Reticora server is up.\n")
		fmt.Fprint(w, "# TYPE reticora_up gauge\n")
		fmt.Fprint(w, "reticora_up 1\n")
		fmt.Fprint(w, "# HELP reticora_info Build and version information.\n")
		fmt.Fprint(w, "# TYPE reticora_info gauge\n")
		fmt.Fprintf(w, "reticora_info{version=%q} 1\n", version)
	})
}
