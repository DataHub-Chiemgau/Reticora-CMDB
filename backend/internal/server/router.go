// Package server owns the HTTP wiring of the Reticora API: it turns a set of
// repositories into handlers and registers every route on one chi router.
//
// Keeping the wiring here (instead of in cmd/server) means the route table can
// be built in tests, which is what the OpenAPI parity check relies on: the
// specification is compared against the very router the binary serves.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/agent"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ai"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/compliance"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/composition"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/contact"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/desk"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/disposal"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/form"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/graphqlbff"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/iga"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/keymgmt"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/location"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/maintenance"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/operator"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/order"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/privacy"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rack"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reservation"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/savedview"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/security"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/signup"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/sla"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/stocktake"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenantapi"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/topology"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/training"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/workflow"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Repositories bundles every persistence port the API depends on.
type Repositories struct {
	CI ci.Repository
	// EventOutbox reports that the CI repository writes its domain events
	// into the outbox, from which the publisher feeds the webhooks
	// (WP-136); the CI handler then dispatches nothing itself.
	EventOutbox       bool
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
	Agent             agent.Repository
	Security          security.Repository
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
	// APIKeys identifies and manages API keys (AUT-04).
	APIKeys identity.APIKeyRepository
	// ServiceAccounts holds the non-human principals (RBA-08).
	ServiceAccounts user.ServiceAccountRepository
	// Invitations are the user invitations (TLC-03); nil answers 503.
	Invitations user.InvitationRepository
	SLA         sla.Repository
	Form        form.Repository
	Workflow    workflow.Repository
	Compliance  compliance.Repository
	IGA         iga.Repository
	Search      search.Backend
	AI          ai.Repository
	ExportJobs  export.JobRepository
	// Jobs is the generic job model (JOB-01).
	Jobs    jobs.Repository
	Privacy privacy.Repository
	// Enterprise CMDB + asset/inventory extension (additive modules).
	CIType            citype.Repository
	RelationshipType  relationshiptype.Repository
	Lifecycle         lifecycle.Repository
	LocationTree      locations.Repository
	Movement          movement.Repository
	Reservation       reservation.Repository
	Composition       composition.Repository
	Override          override.Repository
	History           history.Repository
	SavedView         savedview.Repository
	FilterQuery       savedview.QueryEngine
	LifecycleStates   lifecycle.StateStore
	LifecycleResolver lifecycle.EntityResolver
	Availability      reservation.AvailabilityProvider
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
	// RefreshSessions stores the rotating refresh tokens in the shared cache
	// store; nil keeps a per-process store (tests).
	RefreshSessions *identity.RefreshSessions
	// Operator configures the operator path /admin (SEC-07); OperatorPool
	// holds operator_audit and serves the organization listing (nil: the
	// audit fails and is logged, tests and --no-db).
	Operator     operator.Config
	OperatorPool *pgxpool.Pool
	// UserProvisioner auto-creates the app_user on first OIDC login (nil
	// disables). DefaultProvisionRole names the standard role assigned on
	// first login (empty assigns none).
	UserProvisioner      identity.UserProvisioner
	DefaultProvisionRole string
	// Audit is registered only when a database-backed audit trail exists.
	Audit *audit.Handler
	// AuditPool enables the security report to include audit-chain integrity;
	// it is the same pool the audit handler serves from. With it, every
	// successful write request is audited (AUD-07).
	AuditPool *pgxpool.Pool
	// RouteAuditor replaces the audit of write requests (tests); nil uses
	// AuditPool.
	RouteAuditor middleware.RouteAuditor
	AIProvider   ai.Provider
	// Blobs persists asynchronous export results; nil disables export-job
	// creation (the streaming export endpoint stays available).
	Blobs blob.Store
	// Egress is the destination policy of webhook and connector URLs
	// (SEC-08); the zero value blocks every internal destination.
	Egress egress.Options
	// RouteMiddleware runs on every API route after its authorization, in
	// order (API-04: validation, then idempotency).
	RouteMiddleware []func(http.Handler) http.Handler
	// Readiness lists the dependencies /readyz verifies (OPS-01).
	Readiness []ReadinessCheck
	// IngestQueue carries discovery records over ingest.> (REC-01); nil
	// processes them in the request.
	IngestQueue *discovery.NATSIngestQueue
	// Signup is the self-signup flow (TLC-01); nil answers 503.
	Signup *signup.Service
	// SignupProtection is the captcha, blocklist and per-IP limit of the
	// signup (WP-112); without it signups answer 503.
	SignupProtection *signup.Protection
	// Events reports facts after their commit into the event outbox
	// (review.created, export.completed, job.failed; NTF-05); optional.
	Events events.Emitter
	// Notifications are the notification stores (NTF-04); nil keeps them
	// in memory.
	Notifications *notify.Center
	// InvitationAccounts creates the IdP account of an accepted invitation.
	InvitationAccounts user.InvitationAccounts
	// CollectorCA signs the collector certificates (COL-04); nil answers
	// certificate requests with 503.
	CollectorCA *discovery.CA
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
// agentTypeResolver adapts the discovery repository's CI type lookup for the
// agent handler's endpoint reconciliation.
func agentTypeResolver(repos Repositories) agent.CITypeResolver {
	r, ok := repos.Discovery.(agent.CITypeResolver)
	if !ok {
		return nil
	}
	return r
}

// agentHandler builds the agent handler; with a session issuer, enrollment
// returns the agent's signed credential (AGT-03).
func agentHandler(repos *Repositories, sessions *identity.SessionIssuer) *agent.Handler {
	h := agent.NewHandler(repos.Agent, repos.Metrics, repos.CI, agentTypeResolver(*repos)).WithFindings(repos.Security).
		WithFieldGuard(override.NewGuard(repos.Override))
	if sessions != nil {
		h.WithAgentTokens(sessions)
	}
	return h
}

// operatorHandler builds the operator path; organizations are created with
// the creation chain of the self-signup (TLC-02).
func operatorHandler(opts *Options) *operator.Handler {
	h := operator.NewHandler(opts.Operator, opts.OIDC, opts.Sessions, operator.NewAuditor(opts.OperatorPool), opts.OperatorPool).
		WithEntitlements(opts.Entitlements)
	if opts.Signup != nil {
		h.WithOrgCreator(opts.Signup)
	}
	return h
}

// invitationHandler builds the invitations (TLC-03); with an IdP admin
// client the acceptance creates the account.
func invitationHandler(repos *Repositories, opts *Options) *user.InvitationHandler {
	h := user.NewInvitationHandler(repos.Invitations)
	if opts.InvitationAccounts != nil {
		h.WithAccounts(opts.InvitationAccounts)
	}
	return h
}

// userHandler builds the user handler; with a session blacklist, deactivation
// ends the user's sessions immediately (TLC-04).
func userHandler(repos *Repositories, sessions *identity.SessionIssuer) *user.Handler {
	h := user.NewHandler(repos.User, repos.Contact).WithPrivacySources(repos.Ticket, repos.Assignment)
	if revocations := sessions.Revocations(); revocations != nil {
		h.WithSessionRevoker(revocations)
	}
	return h
}

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

	httpMetrics := registerOperational(mux, opts.Version, opts.MetricsTenantLabel, opts.Readiness)

	// Every domain route is registered through the authorizing router, which
	// attaches the permission middleware resolved from the route table.
	// Routes without a mapping fail closed; the router test locks in that
	// every route is mapped.
	protected := authorizingRouter{Router: mux, after: opts.RouteMiddleware, auditor: routeAuditor(&opts)}

	identityHandler := identity.NewHandler(opts.OIDC, opts.Sessions).
		WithProvisioning(opts.UserProvisioner, opts.DefaultProvisionRole).
		WithAccessResolver(repos.Permission)
	if opts.RefreshSessions != nil {
		identityHandler.WithRefreshSessions(opts.RefreshSessions)
	}
	// New discovered CIs respect max_cis; over the limit they are held as
	// unlicensed_ci and adopted once the limit is raised (ENT-03, CH21).
	// Export and discovery jobs register in the generic job model (JOB-01).
	var jobTracker jobs.Tracker
	if repos.Jobs != nil {
		jobService := jobs.NewService(repos.Jobs)
		jobTracker = jobService
		// A CI merge is a job (CI-12, JOB-01); bulk operations from 100
		// ids on as well (API-08, JOB-04).
		jobService.Register(jobs.Kind{Name: ci.BulkJobKind, MaxAttempts: 1, Timeout: 30 * time.Minute})
		if opts.CIService != nil {
			opts.CIService.WithJobs(jobTracker).WithBulkJobs(jobService)
		}
	}
	exportWorker := export.NewJobWorker(repos.ExportJobs, repos.CI, opts.Blobs)
	exportWorker.Tracker = jobTracker
	discoveryHandler := discovery.NewHandler(repos.Discovery, repos.CI, repos.Relationship).
		WithProvenance(overrideProvenance{repo: repos.Override}).
		WithInstanceFields(repos.CIType).
		WithLimits(opts.Entitlements)
	opts.Entitlements.WithUnlicensed(discoveryHandler)
	if opts.CollectorCA != nil {
		discoveryHandler.WithCA(opts.CollectorCA)
	}
	// Discovered values pass the field rules of manual writes (CI-03).
	if opts.CIService != nil {
		discoveryHandler.WithObservedValidator(opts.CIService)
	}
	// Discovered interfaces and addresses are kept in IPAM (REC-07).
	if syncer, ok := repos.IPAM.(discovery.InterfaceSyncer); ok {
		discoveryHandler.WithInterfaces(syncer)
	}
	if opts.IngestQueue != nil {
		opts.IngestQueue.Attach(discoveryHandler)
	}
	if opts.Events != nil {
		discoveryHandler.WithReviewNotifier(reviewEvents{emitter: opts.Events})
		exportWorker.Events = opts.Events
	}
	registrars := []registrar{
		identityHandler,
		operatorHandler(&opts),
		identity.NewAPIKeyHandler(repos.APIKeys),
		user.NewServiceAccountHandler(repos.ServiceAccounts),
		invitationHandler(&repos, &opts),
		entitlement.NewHandler(opts.Entitlements),
		ciHandler(&repos, &opts),
		notifyHandler(&opts),
		relationship.NewHandler(repos.Relationship, repos.RelationshipType),
		webhook.NewHandler(repos.Webhook, opts.Dispatcher).WithEgress(opts.Egress),
		discoveryHandler,
		topology.NewHandler(repos.CI, repos.Relationship).WithRelationshipTypes(repos.RelationshipType),
		export.NewHandler(repos.CI),
		export.NewJobHandler(repos.ExportJobs, exportWorker, opts.Blobs),
		asset.NewHandler(repos.Asset).
			WithComposition(compositionParentLookup{repo: repos.Composition}).
			WithCIs(ciLookup{repo: repos.CI}),
		assignment.NewHandler(repos.Assignment).WithMovements(repos.Movement),
		document.NewHandler(repos.Document, opts.Blobs),
		stocktake.NewHandler(repos.Stocktake),
		consumable.NewHandler(repos.Consumable),
		order.NewHandler(repos.Order).WithMovements(repos.Movement),
		maintenance.NewHandler(repos.Maintenance),
		disposal.NewHandler(repos.Disposal).WithMovements(repos.Movement),
		keymgmt.NewHandler(repos.Key),
		training.NewHandler(repos.Training),
		desk.NewHandler(repos.Desk),
		location.NewHandler(repos.Location),
		agentHandler(&repos, opts.Sessions),
		security.NewHandler(repos.Security),
		ticket.NewHandler(repos.Ticket, sla.TicketHooks{Repo: repos.SLA}),
		userHandler(&repos, opts.Sessions),
		permission.NewHandler(repos.Permission),
		search.NewHandler(repos.Search, repos.Permission),
		sla.NewHandler(repos.SLA, repos.Ticket),
		form.NewHandler(repos.Form),
		workflow.NewHandler(repos.Workflow, workflow.NewExecutor(repos.Workflow, repos.Ticket, repos.CI, repos.Form, opts.Dispatcher).
			WithFieldGuard(override.NewGuard(repos.Override))),
		compliance.NewHandler(repos.Compliance, compliance.NewEvaluator(repos.Compliance, repos.CI)).
			WithReports(compliance.NewReportService(repos.Compliance, reportAuditVerifier(opts), entitlementLister{svc: opts.Entitlements})),
		iga.NewHandler(repos.IGA, repos.User, opts.Credentials, repos.Discovery, repos.Workflow).WithEgress(opts.Egress),
		tenantapi.NewHandler(repos.TenantHierarchy),
		rack.NewHandler(repos.Rack),
		contact.NewHandler(repos.Contact),
		ipam.NewHandler(repos.IPAM),
		monitoring.NewHandler(repos.Metrics, alertStoreFor(repos.Metrics)),
		graphqlbff.NewHandler(repos.CI, repos.Relationship).WithEntitlements(opts.Entitlements),
		relationshiptype.NewHandler(repos.RelationshipType),
		citype.NewHandler(repos.CIType, opts.Dispatcher),
		lifecycle.NewHandler(repos.Lifecycle,
			lifecycle.NewService(repos.Lifecycle, repos.LifecycleStates, nil), repos.LifecycleResolver),
		locations.NewHandler(repos.LocationTree),
		movement.NewHandler(repos.Movement, opts.Dispatcher).WithAssets(assetCreator{repo: repos.Asset}),
		reservation.NewHandler(repos.Reservation, opts.Dispatcher).WithAvailability(repos.Availability),
		composition.NewHandler(repos.Composition, opts.Dispatcher).
			WithAssets(parentAssetLookup{repo: repos.Asset}),
		override.NewHandler(repos.Override, opts.Dispatcher),
		history.NewHandler(repos.History),
		savedview.NewHandler(repos.SavedView).WithQueryEngine(repos.FilterQuery).WithPermissions(repos.Permission),
		credential.NewHandler(opts.Credentials),
		ai.NewHandler(repos.AI, opts.AIProvider, ai.NewRetriever(repos.AI, repos.Search, repos.Permission, opts.AIProvider)),
		privacy.NewHandler(privacy.NewService(repos.Privacy, repos.Contact, repos.User)),
		signup.NewHandler(opts.Signup).WithProtection(opts.SignupProtection),
	}
	for _, h := range registrars {
		h.RegisterRoutes(protected)
	}

	// The audit trail is hash-chained in PostgreSQL and therefore has no
	// in-memory counterpart. Without a pool (--no-db) its operations still
	// exist and answer 503, so both profiles serve the same contract
	// (API-01).
	if opts.Audit != nil {
		opts.Audit.RegisterRoutes(protected)
	} else {
		unavailable := func(w http.ResponseWriter, _ *http.Request) {
			api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "the audit trail needs PostgreSQL; it is not available with --no-db")
		}
		for _, base := range []string{"/api/v1/audit-log", "/api/v1/audit"} {
			protected.Get(base, unavailable)
			protected.Post(base+"/verify", unavailable)
		}
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
	case repos.CIType == nil:
		return fmt.Errorf("server: CI type repository is required")
	case repos.RelationshipType == nil:
		return fmt.Errorf("server: relationship type repository is required")
	case repos.Lifecycle == nil:
		return fmt.Errorf("server: lifecycle repository is required")
	case repos.LocationTree == nil:
		return fmt.Errorf("server: location repository is required")
	case repos.Movement == nil:
		return fmt.Errorf("server: movement repository is required")
	case repos.Reservation == nil:
		return fmt.Errorf("server: reservation repository is required")
	case repos.Composition == nil:
		return fmt.Errorf("server: composition repository is required")
	case repos.Override == nil:
		return fmt.Errorf("server: override repository is required")
	case repos.History == nil:
		return fmt.Errorf("server: history repository is required")
	case repos.SavedView == nil:
		return fmt.Errorf("server: saved view repository is required")
	}
	return nil
}

// registerOperational adds the unauthenticated health and metrics endpoints:
// /healthz is the liveness probe (the process serves HTTP), /readyz the
// readiness probe (all dependencies usable). It returns the HTTP metrics
// middleware so main can mount it on the outer middleware chain; the
// middleware records into the same registry that serves /metrics.
func registerOperational(mux *chi.Mux, version string, includeTenantLabel bool, readiness []ReadinessCheck) func(http.Handler) http.Handler {
	mux.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"ok"}`)
	})
	mux.Get("/readyz", readinessHandler(readiness))
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
	observability.RegisterWorkerMetrics(registry)
	override.RegisterMetrics(registry)
	operator.RegisterMetrics(registry)
	entitlement.RegisterMetrics(registry)
	discovery.RegisterMetrics(registry)

	httpMetrics := middleware.RegisterHTTPMetrics(registry, includeTenantLabel)

	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{}), httpMetrics
}

// routeAuditor stores the audit entries of write requests in the
// organization's chain; without a database nothing is stored.
func routeAuditor(opts *Options) middleware.RouteAuditor {
	if opts.RouteAuditor != nil {
		return opts.RouteAuditor
	}
	if opts.AuditPool == nil {
		return nil
	}
	return pgRouteAuditor{pool: opts.AuditPool, recorder: audit.NewPGRecorder()}
}

// pgRouteAuditor writes a route's audit entry org-wide on behalf of the
// organization: the entry belongs to the organization's chain (E-08).
type pgRouteAuditor struct {
	pool     *pgxpool.Pool
	recorder *audit.PGRecorder
}

func (a pgRouteAuditor) RecordRoute(ctx context.Context, entry *audit.Entry) error {
	scope := database.OrgWideScope(entry.OrganizationID, "")
	return database.WithTenant(ctx, a.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		_, err := a.recorder.Record(ctx, tx, *entry)
		return err
	})
}

// ciHandler builds the CI handler; without the event outbox it hands CI
// events to the webhook dispatcher directly.
func ciHandler(repos *Repositories, opts *Options) *ci.Handler {
	var h *ci.Handler
	if repos.EventOutbox || opts.Dispatcher == nil {
		h = ci.NewHandler(opts.CIService)
	} else {
		h = ci.NewHandler(opts.CIService, opts.Dispatcher)
	}
	// GET /cis/{id}?include= (API-05) embeds up to includeLimit related
	// rows of each kind.
	const includeLimit = 200
	page := api.PaginationParams{Limit: includeLimit}
	return h.WithIncludes(map[string]ci.IncludeFunc{
		"relationships": func(ctx context.Context, orgID, ciID string) (any, error) {
			rels, _, err := repos.Relationship.List(ctx, orgID, ciID, page)
			return rels, err
		},
		"contacts": func(ctx context.Context, orgID, ciID string) (any, error) {
			contacts, _, err := repos.Contact.ListForCI(ctx, orgID, ciID, page)
			return contacts, err
		},
	})
}

// notifyHandler serves the notification center and the channels.
func notifyHandler(opts *Options) *notify.Handler {
	if opts.Notifications == nil {
		opts.Notifications = notify.NewMemoryCenter()
	}
	return notify.NewHandler(opts.Notifications)
}

// reviewEvents reports opened review items as review.created (NTF-05).
type reviewEvents struct{ emitter events.Emitter }

func (r reviewEvents) ReviewOpened(ctx context.Context, item *reviews.Item) {
	if err := r.emitter.Emit(ctx, &events.Event{OrgID: item.OrganizationID, Type: events.ReviewCreated, EntityID: item.ID,
		Data: map[string]any{"id": item.ID, "kind": item.Kind, "object_key": item.ObjectKey}}); err != nil {
		slog.Error("report opened review item", "item", item.ID, "error", err)
	}
}
