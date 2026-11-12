// Package server owns the HTTP wiring of the Reticora API: it turns a set of
// repositories into handlers and registers every route on one chi router.
//
// Keeping the wiring here (instead of in cmd/server) means the route table can
// be built in tests, which is what the OpenAPI parity check relies on: the
// specification is compared against the very router the binary serves.
package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/admp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/agent"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ai"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/compliance"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/contact"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/contract"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/desk"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/disposal"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/form"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/graphqlbff"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/iga"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/importer"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/keymgmt"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/labels"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/location"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/maintenance"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/metamodel"
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
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenantapi"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/topology"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/training"
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
	// AssignmentService writes assignments over the ledger (WP-275); nil
	// (profile without database): the writing assignment routes answer 501.
	AssignmentService *assignment.Service
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
	// ADMPAccounts are the ADMP accounts (ADM-01, WP-925).
	ADMPAccounts admp.AccountRepository
	// ADMP is the read-only cache of ADMP data (ADM-03, WP-928); the
	// refresh (WP-929) and the read API (WP-930) use it.
	ADMP            admp.Repository
	Entitlement     entitlement.Repository
	TenantHierarchy tenantapi.Repository
	Rack            rack.Repository
	Contact         contact.Repository
	IPAM            ipam.Repository
	Metrics         monitoring.MetricStore
	Permission      permission.Repository
	// FieldGrants stores the field permissions (RBA-07, WP-264).
	FieldGrants permission.FieldGrantRepository
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
	MetamodelDraft    metamodel.Repository        // drafts (WP-260); nil without a database: the routes answer 503
	Import            importer.Repository         // import mapping profiles (WP-774, IMP-IO-01)
	Supplier          contract.SupplierRepository // suppliers (WP-736, MGT-12); nil: the routes answer 503
	Contract          contract.ContractRepository // contracts (WP-269, MGT-12); nil: the routes answer 503
	RelationshipType  relationshiptype.Repository
	Lifecycle         lifecycle.Repository
	LocationTree      locations.Repository
	Labels            labels.Repository // code lookup (WP-762, INV-03); nil: the route answers 503
	Movement          movement.Repository
	Reservation       reservation.Repository
	Override          override.Repository
	History           history.Repository
	SavedView         savedview.Repository
	FilterQuery       savedview.QueryEngine
	LifecycleStates   lifecycle.StateStore
	LifecycleResolver lifecycle.EntityResolver
	// LifecycleGuards checks the system guards of LCY-03 (WP-279); nil
	// (--no-db) refuses guarded transitions of assets (fail-closed).
	LifecycleGuards lifecycle.GuardChecker
	Availability    reservation.AvailabilityProvider
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
	// FieldPolicyCache holds the field permission snapshots of the
	// organizations (fieldgrants:<org>, RBA-07); the server passes the
	// shared cache store, nil keeps a per-process store (tests).
	FieldPolicyCache cache.Store
	// Dispatcher publishes CI lifecycle events to webhook subscribers.
	Dispatcher *webhook.Dispatcher
	// CIService is the CI domain service (limit-aware).
	CIService *ci.Service
	// Credentials is the credential service (envelope encryption).
	Credentials *credential.Service
	// ADMPSync refreshes the ADMP cache (WP-929): cmd/server shares it with
	// the system jobs admp.refresh_linked and admp.expire; nil builds one
	// on the repositories with Credentials as secret source and Egress
	// (NewADMPSync). The router queues its job admp.refresh.
	ADMPSync *admp.Sync
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
	// OpsAlerts configures the Alertmanager bridge of the operator path
	// (OPS-05, E-75): without an operator organization it answers 404.
	// Notifier queues its notifications on the channels of that
	// organization; without it, or without OperatorPool, a configured
	// bridge answers 503.
	OpsAlerts operator.AlertConfig
	Notifier  *notify.Notifier
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
	// Documents are the upload limits of the document module (SEC-09,
	// WP-268); the zero value uses the defaults of config.DocumentSettings.
	Documents config.DocumentSettings
	// Egress is the destination policy of webhook and connector URLs and
	// of own SMTP servers (SEC-08), when they are stored (webhooks, IGA
	// connectors, notification channels) and called; the zero value
	// blocks every internal destination.
	Egress egress.Options
	// RouteMiddleware runs on every API route after its authorization, in
	// order (API-04: validation, then idempotency).
	RouteMiddleware []func(http.Handler) http.Handler
	// Readiness lists the dependencies /readyz verifies (OPS-01).
	Readiness []ReadinessCheck
	// IngestQueue carries discovery records over ingest.> (REC-01); nil
	// processes them in the request.
	IngestQueue *discovery.NATSIngestQueue
	// JobQueue runs the submitted job kinds on the JOBS work queue on NATS
	// (TEC-14, jobkinds.go); nil runs them in the accepting process.
	JobQueue *jobs.Queue
	// Signup is the self-signup flow (TLC-01); nil answers 503.
	Signup *signup.Service
	// SignupProtection is the captcha, blocklist and per-IP limit of the
	// signup (WP-112); without it signups answer 503.
	SignupProtection *signup.Protection
	// Events reports facts after their commit into the event outbox
	// (NTF-05); optional. review.created (WP-549), export.completed, job.failed
	// and webhook.dead (WP-550) are written in the transaction of their change.
	Events events.Emitter
	// Notifications are the notification stores (NTF-04); nil keeps them
	// in memory.
	Notifications *notify.Center
	// InvitationAccounts creates the IdP account of an accepted invitation.
	// It is wired only together with InvitationMailer
	// (user.InvitationHandler.WithAccounts).
	InvitationAccounts user.InvitationAccounts
	// InvitationMailer sends the invitation mail straight to the invited
	// address over the central SMTP server where the acceptance creates the
	// IdP account; nil without central SMTP.
	InvitationMailer user.InvitationMailer
	// InvitationAcceptURL is the page the link of the invitation mail opens
	// (RETICORA_INVITATION_ACCEPT_URL); empty: issuing and renewing
	// invitations answers 503.
	InvitationAcceptURL string
	// MFA binds the IdP account of every new org_admin to the second factor
	// (WP-358): role assignment and first login of the PostgreSQL user
	// repository, and the login callback for org_admin from the IdP group
	// org_admin or the default role (WP-367). nil binds nothing (tests,
	// development without Keycloak admin client); user.UnavailableMFA fails
	// every org_admin grant.
	MFA user.MFAEnforcer
	// CollectorCA signs the collector certificates (COL-04); nil answers
	// certificate requests with 503.
	CollectorCA *discovery.CA
	// Modules are the enabled late modules (WP-214, E-03); nil enables all
	// (tests). The server passes the set from RETICORA_MODULES.
	Modules *ModuleSet
	// RetentionSoftDeleteDays is the configured soft-delete retention
	// (RETICORA_RETENTION_SOFT_DELETE_DAYS): the window of POST
	// /cis/{id}/restore (CI-05); 0 keeps the default of 90 days.
	RetentionSoftDeleteDays int
}

// registrar is implemented by every domain handler.
type registrar interface {
	RegisterRoutes(r chi.Router)
}

// errMergeGone refuses the revert of a merge job whose merge no longer
// exists (409).
var errMergeGone = errors.New("the merge of the job no longer exists")

// NewRouter builds the complete API router. Every route the server exposes is
// registered here; there is no other registration site. It also returns the
// tenant-aware HTTP metrics middleware, which the caller mounts on the outer
// middleware chain so requests are recorded into the same registry that serves
// /metrics.
// agentTypeResolver adapts the discovery repository's CI type lookup for the
// agent handler's endpoint reconciliation.
func agentTypeResolver(repos *Repositories) agent.CITypeResolver {
	r, ok := repos.Discovery.(agent.CITypeResolver)
	if !ok {
		return nil
	}
	return r
}

// agentHandler builds the agent handler; with a session issuer, enrollment
// returns the agent's signed credential (AGT-03). Values that differ from a
// manual override open an override_conflict review in the review queue of
// discovery (REC-12).
func agentHandler(repos *Repositories, sessions *identity.SessionIssuer, conflicts agent.ConflictReviews) *agent.Handler {
	h := agent.NewHandler(repos.Agent, repos.Metrics, repos.CI, agentTypeResolver(repos)).WithFindings(repos.Security).
		WithFieldGuard(override.NewGuard(repos.Override)).WithConflictReviews(conflicts)
	if sessions != nil {
		h.WithAgentTokens(sessions)
	}
	return h
}

// newFieldResolver builds the field permission resolver (RBA-07): users
// and IdP groups resolve through the permission repository, service
// accounts through their role assignments, the snapshots are cached in
// opts.FieldPolicyCache.
func newFieldResolver(repos *Repositories, opts *Options) *permission.FieldResolver {
	return WithAssetsFeature(NewFieldResolver(repos, opts.FieldPolicyCache), opts.Entitlements)
}

// WithAssetsFeature makes the resolver close computed attributes with
// asset sources for an organization without assets_basic (SR-58, WP-871);
// without entitlements the feature counts as enabled.
func WithAssetsFeature(r *permission.FieldResolver, svc *entitlement.Service) *permission.FieldResolver {
	if svc != nil {
		r.WithFeatureCheck(svc.IsEnabled)
	}
	return r
}

// NewFieldResolver builds the field permission resolver of the
// repositories with the snapshot cache store (nil: per process); the
// export worker of cmd/server resolves the policy of a job's creator with
// it (WP-729).
func NewFieldResolver(repos *Repositories, store cache.Store) *permission.FieldResolver {
	var serviceAccounts identity.ServiceAccountAccessResolver
	if repos.ServiceAccounts != nil {
		serviceAccounts = user.ServiceAccountAccess{Repo: repos.ServiceAccounts}
	}
	return permission.NewFieldResolver(repos.Permission, serviceAccounts, repos.FieldGrants, store)
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

// alertBridge builds the Alertmanager bridge (OPS-05, E-75): the alerts go
// to the channels of the operator organization, written in its tenant
// transaction; refused requests are recorded in operator_audit.
func alertBridge(repos *Repositories, opts *Options) *operator.AlertBridge {
	b := operator.NewAlertBridge(opts.OpsAlerts, operator.NewAuditor(opts.OperatorPool))
	if opts.OperatorPool != nil && opts.Notifier != nil && opts.Notifications != nil && repos.Permission != nil {
		b.WithDelivery(opts.OperatorPool, opts.Notifier, opts.Notifications.Channels,
			notify.NewPGResolver(opts.OperatorPool, repos.Permission))
	}
	return b
}

// apiKeyHandler builds the api-keys resource with the same owner and
// service-account resolvers as the API key service in cmd/server, so that
// rotation, revocation and service-account binding are checked against the
// key holder's rights (AUT-04, RBA-08); a new key counts against
// max_api_keys (ENT-03).
func apiKeyHandler(repos *Repositories, opts *Options) *identity.APIKeyHandler {
	h := identity.NewAPIKeyHandler(repos.APIKeys).WithOwnerAccess(repos.Permission).WithQuota(opts.Entitlements)
	if repos.ServiceAccounts != nil {
		h.WithServiceAccountAccess(user.ServiceAccountAccess{Repo: repos.ServiceAccounts})
	}
	return h
}

// invitationHandler builds the invitations (TLC-03); the accept URL makes
// the link of the invitation mail, with an IdP admin client and the
// invitation mailer the acceptance creates the account and the mail goes
// straight to the invitee; the acceptance counts against max_users
// (ENT-03).
func invitationHandler(repos *Repositories, opts *Options) *user.InvitationHandler {
	h := user.NewInvitationHandler(repos.Invitations).WithAcceptURL(opts.InvitationAcceptURL).WithQuota(opts.Entitlements)
	if opts.InvitationAccounts != nil {
		h.WithAccounts(opts.InvitationAccounts, opts.InvitationMailer)
	}
	return h
}

// userHandler builds the user handler; with a session blacklist, deactivation
// ends the user's sessions immediately (TLC-04); POST /users counts against
// max_users (ENT-03).
func userHandler(repos *Repositories, opts *Options) *user.Handler {
	h := user.NewHandler(repos.User, repos.Contact).WithPrivacySources(repos.Ticket, repos.Assignment).WithQuota(opts.Entitlements)
	if revocations := opts.Sessions.Revocations(); revocations != nil {
		h.WithSessionRevoker(revocations)
	}
	return h
}

func NewRouter(repos Repositories, opts Options) (*chi.Mux, func(http.Handler) http.Handler, error) { //nolint:gocritic // public constructor with about 480 callers; the configuration is passed by value
	if err := validate(&repos, &opts); err != nil {
		return nil, nil, err
	}
	if pg, ok := repos.User.(*user.PGRepository); ok && opts.MFA != nil {
		pg.WithMFA(opts.MFA)
	}
	if pg, ok := repos.User.(*user.PGRepository); ok {
		// A first login with an invitation counts against max_users
		// (ENT-03).
		pg.WithQuota(opts.Entitlements)
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
	// The draft service captures metamodel writes with ?draft (WP-720)
	// and lays a draft over the form preview (E-113).
	// The field permissions (RBA-07, WP-264) resolve per principal; the
	// handlers of WP-726 take this resolver. Behind the route permission
	// FieldPolicyMiddleware puts the policy source of each request into its
	// context: permission.PolicyFrom resolves it once, on first need
	// (WP-265), also for POST /bff/graphql.
	fieldResolver := newFieldResolver(&repos, &opts)
	after := append(slices.Clone(opts.RouteMiddleware), permission.FieldPolicyMiddleware(fieldResolver))
	protected := &authorizingRouter{Router: mux, after: after, auditor: routeAuditor(&opts), modules: opts.Modules,
		drafts: repos.MetamodelDraft}

	identityHandler := identity.NewHandler(opts.OIDC, opts.Sessions).
		WithProvisioning(opts.UserProvisioner, opts.DefaultProvisionRole).
		WithAccessResolver(repos.Permission)
	if opts.RefreshSessions != nil {
		identityHandler.WithRefreshSessions(opts.RefreshSessions)
	}
	if opts.MFA != nil {
		// org_admin from the IdP group org_admin or the default role
		// (WP-367).
		identityHandler.WithMFA(opts.MFA)
	}
	// New discovered CIs respect max_cis; over the limit they are held as
	// unlicensed_ci and adopted once the limit is raised (ENT-03, CH21).
	// Export and discovery jobs register in the generic job model (JOB-01).
	var jobTracker jobs.Tracker
	var bffJobs graphqlbff.JobSource
	// The CI history projects the field values with the policy of the
	// caller, a queued snapshot with that of its creator (WP-727).
	historyHandler := history.NewHandler(repos.History).WithCIs(HistoryCIs(&repos)).
		WithCreatorPolicy(SnapshotCreatorPolicy(&repos, fieldResolver))
	// The rebuild of the search index is the job search.reindex (WP-786),
	// wired in registerJobKinds; without the job model it runs in the
	// request.
	searchHandler := search.NewHandler(repos.Search, repos.Permission)
	// Hits of asset, reservation, document and ticket need their feature
	// and module, the holder fields assets_basic (WP-788).
	if opts.Entitlements != nil {
		searchHandler.WithGates(opts.Entitlements, opts.Modules.Enabled)
	}
	// The label batches of inventory areas (WP-763) are the job
	// labels.batch, wired in registerJobKinds; they read through the
	// PostgreSQL resolver (the memory one reads no batch: 503) and store
	// the PDF in the bucket exports. A queued batch renders asset labels
	// with the field policy of its creator.
	labelSource, _ := repos.Labels.(labels.BatchSource)
	labelBatch := labels.NewBatchHandler(labelSource, opts.Blobs).WithFeatures(opts.Entitlements).
		WithCreatorPolicy(labels.CreatorPolicy(SnapshotCreatorPolicy(&repos, fieldResolver)))
	// credential.rotated goes to the outbox of the application pool
	// (WP-889); the test of a vault connection is the job vault.test
	// (WP-896, jobkinds.go).
	credentialHandler := credential.NewHandler(opts.Credentials).WithEvents(opts.AuditPool)
	// The refresh of the ADMP cache (WP-929): readonly clients per account
	// on the egress policy with the credential service as secret source;
	// its job admp.refresh is registered with the job kinds.
	admpSync := opts.ADMPSync
	if admpSync == nil {
		admpSync = NewADMPSync(&repos, opts.Credentials, opts.Egress, opts.AuditPool, 0)
	}
	// The draft handler (WP-260) runs the test run of a draft as a job
	// (WP-721, metamodel.test) with the job service, like historyHandler.
	draftHandler := metamodel.NewHandler(repos.MetamodelDraft)
	// The publication of a draft (WP-261) applies its items through the
	// resource routes on repos.CIType, the instance cmd/server/workers.go
	// gives the index scheduler, and sends their webhook events after the
	// commit through the dispatcher, as the direct path does.
	var draftDispatcher citype.EventDispatcher
	if opts.Dispatcher != nil {
		draftDispatcher = opts.Dispatcher
	}
	draftHandler.WithPublish(repos.CIType, draftDispatcher).WithFieldPolicyInvalidator(fieldResolver)
	// The generic job API (API-09) serves in both profiles; without a
	// database it has an empty in-memory job table.
	jobAPI := jobs.NewAPIHandler(jobs.NewService(jobs.NewMemoryRepository()))
	// The template migration (WP-712) is a job of the module metamodel
	// (E-105): its executor works on the decorated CI repository (WP-418),
	// so the migrated CIs are indexed. Without the module neither route nor
	// job kind exists.
	var templateJobs *ci.TemplateMigrationJobs
	if opts.Modules.Enabled(ModuleMetamodel) {
		templateJobs = ci.NewTemplateMigrationJobs(repos.CI, repos.CIType)
	}
	// The import runs (WP-301) get the job service in registerJobKinds; the
	// routes serve in both profiles and answer 503 without it. A run
	// (WP-775) hands its rows to the writer of the profile's entity:
	// locations here, CIs below the discovery handler (WP-302), assets, contracts, consumables and
	// relationships with WP-776. Before the first row it checks module and
	// features of the entity with the release and the org gate, and after
	// its success it reports import.completed.
	importHandler := importer.NewImportHandler(repos.Import, repos.Document, opts.Blobs).
		WithWriter(importer.NewLocationWriter(repos.LocationTree)).
		WithGates(opts.Modules, importFeatureGate(&opts)).
		WithEvents(opts.Events)
	// The run reloads its starter from the job row: user status and
	// current role assignments (no rights in the job parameters). Without
	// them (in-memory user store) runs fail closed.
	if users, ok := repos.User.(importer.UserStatus); ok && repos.Permission != nil {
		importHandler.WithStarters(users, repos.Permission)
		// A run started with an API key reloads the key like the auth
		// middleware (cmd/server): revoked or expired fails it, its rights
		// are the intersection with the owner's current ones (AUT-04,
		// WP-779, E-145).
		if repos.APIKeys != nil {
			keys := identity.NewAPIKeyServiceWithStore(repos.APIKeys).WithOwnerAccess(repos.Permission)
			if repos.ServiceAccounts != nil {
				keys.WithServiceAccountAccess(user.ServiceAccountAccess{Repo: repos.ServiceAccounts})
			}
			importHandler.WithAPIKeys(keys)
		}
	}
	// The malware scan of document versions (WP-734, SEC-09, E-42) is the
	// job kind document.scan of the module documents on the job queue:
	// clamd from opts.Documents, status, audit and event in a transaction
	// of the application pool. The upload submits it after its commit;
	// without the queue no submitter is wired and versions stay
	// pending_scan.
	documentHandler := document.NewHandler(repos.Document, opts.Blobs).WithLimits(opts.Entitlements).WithUploads(opts.Documents)
	var documentScanner *document.Scanner
	if opts.Modules.Enabled("documents") {
		documentScanner = document.NewScanner(repos.Document, opts.Blobs, opts.Documents).WithPool(opts.AuditPool)
		if opts.JobQueue != nil && repos.Jobs != nil {
			documentHandler.WithScans(opts.JobQueue)
		}
	}
	// The asset handler serves POST /assets/bulk (WP-781): its jobs
	// asset.bulk are wired in registerJobKinds, its delegating actions
	// below (AssetBulkDelegates).
	assetHandler := asset.NewHandler(repos.Asset).
		WithCIs(ciLookup{repo: repos.CI}).
		// The active reservation in GET /assets/{id} (API-06, WP-750).
		WithReservations(reservationLookup{repo: repos.Reservation}).
		// Links write ci.asset_id through the CI service (WP-272).
		WithCILinks(opts.CIService)
	if repos.Jobs != nil {
		jobService := jobs.NewService(repos.Jobs)
		jobTracker = jobService
		bffJobs = jobService
		jobs.SetBacklogSource(repos.Jobs)
		// A bulk job on CIs is reverted by the revert job bulk.revert
		// within 24 hours, with ci:write also from its creator (BLK-02,
		// WP-305); only the PostgreSQL job repository keeps the change set,
		// without it the bulk has no revert (409). The revert job itself
		// has none.
		var bulkRevert *ci.BulkRevert
		var bulkReverter jobs.Reverter
		if store, ok := repos.Jobs.(jobs.ChangeSetStore); ok && opts.CIService != nil {
			bulkRevert = ci.NewBulkRevert(opts.CIService, jobService, store)
			bulkReverter = bulkRevert.Revert
		}
		// A bulk job on assets is reverted the same way with asset:write
		// (WP-784), only while the module inventory is on: /jobs belongs to
		// no module, so without inventory the kind has no revert (409).
		var assetRevert *asset.BulkRevert
		var assetReverter jobs.Reverter
		if store, ok := repos.Jobs.(jobs.ChangeSetStore); ok && opts.Modules.Enabled(ModuleInventory) {
			assetRevert = asset.NewBulkRevert(assetHandler, jobService, store)
			assetReverter = assetRevert.Revert
		}
		jobAPI = jobs.NewAPIHandler(jobService).
			WithKind(ci.BulkJobKind, identity.PermCIWrite, bulkReverter).
			WithBulkKind(ci.BulkJobKind).
			WithKind(asset.BulkJobKind, identity.PermAssetWrite, assetReverter).
			WithBulkKind(asset.BulkJobKind).
			WithKind(jobs.RevertKind, identity.PermCIWrite, nil).
			WithKind("export", identity.PermExportRun, nil).
			// A running template migration is cancelled with citype:manage
			// like its start; it has no revert (E-25).
			WithKind(ci.TemplateMigrateJobKind, identity.PermCITypeManage, nil).
			// A running test of a metamodel draft is cancelled with
			// metamodel:publish like its start (WP-721, E-106).
			WithKind(metamodel.TestJobKind, identity.PermMetamodelPublish, nil).
			// A running import is cancelled with import:run like its
			// start (WP-301, WP-514).
			WithKind(importer.JobKind, identity.PermImportRun, nil).
			// The rebuild of the search index is read with search:write
			// like its start (WP-786).
			WithKind(search.ReindexJobKind, identity.PermSearchWrite, nil).
			// The test of a vault connection is read with vault:manage like
			// its start (WP-896); it has no revert.
			WithKind(credential.VaultTestJobKind, identity.PermVaultManage, nil).
			// The merge is reverted by its unmerge within 30 days (CI-12, E-25).
			// It answers 200 (no revert job).
			WithKind(ci.MergeJobKind, identity.PermCIDelete, func(ctx context.Context, job *jobs.Job) (*jobs.Job, error) {
				if opts.CIService == nil {
					return nil, &jobs.RevertRefusedError{Reason: ci.ErrMergeUnsupported}
				}
				// The refusals of the unmerge answer with the sentinel's text
				// (409); like an unmerge without force, a survivor changed
				// after the merge refuses the revert (E-80). A repository
				// error reaches the client sanitized (API-03).
				_, err := opts.CIService.RevertMerge(ctx, job.OrganizationID, job.RefID)
				// A later open merge of the survivor blocks the revert like
				// the unmerge; the refusal names that merge (F-416).
				var later *ci.UnmergeLaterMergeError
				if errors.As(err, &later) {
					return nil, &jobs.RevertRefusedError{Reason: later}
				}
				for _, refusal := range []error{ci.ErrSnapshotExpired, ci.ErrMergeConflict, ci.ErrUnmergeConflict, ci.ErrMergeUnsupported} {
					if errors.Is(err, refusal) {
						return nil, &jobs.RevertRefusedError{Reason: refusal}
					}
				}
				if err != nil && err.Error() == "not found" {
					return nil, &jobs.RevertRefusedError{Reason: errMergeGone}
				}
				return nil, err
			})
		// A CI merge is a job (CI-12, JOB-01); bulk operations from 100
		// ids on (API-08, JOB-04) and state snapshots as well, on the job
		// queue when there is one (jobkinds.go, TEC-14).
		if opts.CIService != nil {
			opts.CIService.WithJobs(jobTracker)
			// A queue bulk job checks its ids with the policy of its creator,
			// reloaded from the job row (WP-728); without the user status it
			// fails (fail-closed).
			if users, ok := repos.User.(permission.CreatorStatus); ok {
				opts.CIService.WithCreatorPolicy(ci.CreatorPolicyOf(fieldResolver, users))
			}
		}
		// A queue bulk job on assets resolves the policy of its creator the
		// same way (WP-781); without the user status it fails.
		if users, ok := repos.User.(permission.CreatorStatus); ok {
			assetHandler.WithCreatorPolicy(ci.CreatorPolicyOf(fieldResolver, users))
		}
		registerJobKinds(jobService, opts.JobQueue, opts.CIService, bulkRevert, historyHandler, templateJobs, draftHandler,
			importHandler, documentScanner, searchHandler, labelBatch, assetHandler, assetRevert, credentialHandler, admpSync)
	}
	exportWorker := export.NewJobWorker(repos.ExportJobs, repos.CI, opts.Blobs)
	exportWorker.Tracker = jobTracker
	datevGate := exportDATEVGate(&opts)
	exportWorker.DATEV = datevGate
	// The worker renders with the field policy of the job's creator,
	// resolved at render time (RBA-07, WP-729).
	exportWorker.Policies = fieldResolver
	// DATEV jobs read assets and the mapping override (WP-310); the memory
	// profile has no asset source and fails them.
	exportWorker.Assets, _ = repos.Asset.(export.AssetSource)
	exportWorker.Orgs = repos.TenantHierarchy
	// Asset jobs of POST /assets/bulk read the selection by id (WP-782);
	// without the source they fail.
	exportWorker.AssetRows, _ = repos.Asset.(export.AssetRows)
	discoveryHandler := discovery.NewHandler(repos.Discovery, repos.CI, repos.Relationship).
		WithProvenance(overrideProvenance{repo: repos.Override}).
		WithInstanceFields(repos.CIType).
		WithLimits(opts.Entitlements)
	// The ingest links CIs to the asset with their serial number (WP-741),
	// not while the module inventory is off (WP-214).
	if links, ok := repos.Asset.(discovery.AssetLinks); ok && opts.Modules.Enabled(ModuleInventory) {
		discoveryHandler.WithAssetLinks(links)
	}
	opts.Entitlements.WithUnlicensed(discoveryHandler)
	// The collectors' edge data path checks the discovery license like the
	// entitlement middleware on the HTTP ingest (COL-08, ENT-07).
	if opts.Entitlements != nil {
		discoveryHandler.WithEdgeLicense(DiscoveryLicense(opts.Entitlements))
	}
	if opts.CollectorCA != nil {
		discoveryHandler.WithCA(opts.CollectorCA)
	}
	// The collectors' metric samples arrive on the edge (.metrics) and run
	// through the ingest of POST /monitoring/metrics with the monitoring
	// entitlement of that route (MON-02, F-046).
	monitoringHandler := monitoring.NewHandler(repos.Metrics, alertStoreFor(repos.Metrics)).
		WithCIResolver(monitoring.CIAddressResolver{CIs: repos.CI})
	var monitoringFeatures discovery.FeatureStatus
	if opts.Entitlements != nil {
		monitoringFeatures = opts.Entitlements
	}
	discoveryHandler.WithEdgeMetrics(monitoringHandler, monitoringFeatures)
	// The location tree is core; its rack nodes are written only with
	// rack_view, like /api/v1/racks (ENT-05, F-400).
	locationsHandler := locations.NewHandler(repos.LocationTree)
	if opts.Entitlements != nil {
		locationsHandler.WithFeatures(opts.Entitlements)
	}
	// Discovered values pass the field rules of manual writes (CI-03).
	if opts.CIService != nil {
		discoveryHandler.WithObservedValidator(opts.CIService)
		// Reference attributes may name contracts (MET-12, WP-737).
		if resolver := contract.NewReferenceResolver(repos.Contract); resolver != nil {
			opts.CIService.WithReferenceTarget(fieldmeta.TargetContract, resolver)
		}
	}
	// Discovered interfaces and addresses are kept in IPAM (REC-07).
	if syncer, ok := repos.IPAM.(discovery.InterfaceSyncer); ok {
		discoveryHandler.WithInterfaces(syncer)
	}
	if opts.IngestQueue != nil {
		opts.IngestQueue.Attach(discoveryHandler)
	}
	// Review merges run as jobs (JOB-04); the merge of a probable
	// duplicate is the CI-12 merge, a ci.merge job of its own (CH29).
	if jobTracker != nil {
		discoveryHandler.WithReviewJobs(jobTracker)
		// Discovery jobs are generic jobs discovery.<job_type> whose
		// status follows discovery_job (JOB-01, F-146).
		discoveryHandler.WithJobTracker(jobTracker)
	}
	if opts.CIService != nil {
		discoveryHandler.WithMerger(opts.CIService)
	}
	// CI rows of an import (WP-302) resolve their identity with the ingest's
	// implementation under its identity locks, decide every field with
	// source import (rank 95) and open the ingest's reviews; the policy of
	// the starter is resolved like a request's.
	// Assets, contracts, consumables and relationships (WP-776) are
	// written through their repositories in the row's transaction; the
	// identity lookups and the audit entry of a row go through the
	// profile store (PostgreSQL only). Relation columns of CI rows use the
	// relationship writer.
	var relWriter *importer.RelationshipWriter
	if store, ok := repos.Import.(importer.EntityStore); ok {
		if repos.Asset != nil {
			assetWriter := importer.NewAssetWriter(repos.Asset, store, fieldResolver)
			if opts.CIService != nil {
				assetWriter.WithCILinks(opts.CIService)
			}
			importHandler.WithWriter(assetWriter)
		}
		if repos.Contract != nil {
			importHandler.WithWriter(importer.NewContractWriter(repos.Contract, store, fieldResolver))
		}
		if repos.Consumable != nil {
			importHandler.WithWriter(importer.NewConsumableWriter(repos.Consumable, store))
		}
		if repos.Relationship != nil {
			relWriter = importer.NewRelationshipWriter(repos.Relationship, store, fieldResolver).WithIdentities(discoveryHandler)
			importHandler.WithWriter(relWriter)
		}
	}
	if opts.CIService != nil && repos.Override != nil {
		ciWriter := importer.NewCIWriter(opts.CIService, discoveryHandler, repos.Override, fieldResolver).
			WithInstanceFields(repos.CIType).WithFields(repos.CIType).WithLocations(repos.LocationTree)
		if analyzer, ok := repos.Discovery.(discovery.BulkAnalyzer); ok {
			ciWriter.WithAnalyzer(analyzer)
		}
		if relWriter != nil {
			ciWriter.WithRelations(relWriter)
		}
		importHandler.WithWriter(ciWriter)
	}
	topologyHandler := topology.NewHandler(repos.CI, repos.Relationship).WithRelationshipTypes(repos.RelationshipType).
		WithCITypes(repos.CIType)
	// The type filter of a walk from a root includes the subtypes (WP-708).
	if repos.CIType != nil {
		topologyHandler.WithTypeSubtrees(repos.CIType)
	}
	// include=network projects subnets and VLANs from IPAM (IMP-11, WP-298).
	if projector, ok := repos.IPAM.(topology.NetworkProjector); ok {
		topologyHandler.WithNetworkProjection(projector)
	}
	// The Ask path sends the question and the CI context to the provider only
	// with the organization's opt-in and the ai add-on (AI-02); the same
	// consent as the CI chunk indexer of cmd/server.
	var aiEntitlements ai.EntitlementChecker
	if opts.Entitlements != nil {
		aiEntitlements = opts.Entitlements
	}
	aiConsent := ai.NewOrgConsent(repos.AI, aiEntitlements)
	// Workflow and agent open override_conflict reviews through the review
	// queue of discovery, deduplicated with its items (REC-12). Both modules
	// stay switched off in G1 (E-03); the protection holds once they are on.
	// The queue is built here, after the last option of the handler.
	// Tickets and comments are created through one service (WP-766).
	// SLAs are ticketing_standard (E-39, WP-769): the hooks apply, choose
	// again and reset them only with the feature.
	slaHooks := sla.TicketHooks{Repo: repos.SLA}
	if opts.Entitlements != nil {
		slaHooks.Features = opts.Entitlements
	}
	tickets := ticket.NewService(repos.Ticket, slaHooks)
	// Review items open tickets only while the module ticketing is on
	// (WP-291); before the queue is built.
	if opts.Modules.Enabled("ticketing") {
		discoveryHandler.WithReviewTickets(reviewTickets{svc: tickets, cis: repos.CI, features: opts.Entitlements})
	}
	conflictReviews := discoveryHandler.ReviewService()
	// One executor runs the runs of the workflow API and the run an IGA
	// access request starts (WFL-04).
	workflowRuns := workflow.NewExecutor(repos.Workflow, repos.Ticket, repos.CI, repos.Form, opts.Dispatcher).
		WithFieldGuard(override.NewGuard(repos.Override)).WithConflictReviews(conflictReviews)
	// The one writer of the ledger stock_movement (WP-274): the route and
	// the disposal in its transaction (WP-278) lock, check and write
	// stock.movement to the outbox; assignments book through
	// repos.AssignmentService (WP-275).
	movements := movement.NewService(repos.Movement)
	// One lifecycle service for the transition routes and the disposal
	// (WP-278), with the system guards of LCY-03 (WP-279).
	lifecycleService := lifecycle.NewService(repos.Lifecycle, repos.LifecycleStates, lifecycle.NewHistoryRecorder(repos.History)).
		WithGuards(repos.LifecycleGuards)
	relationshipHandler := relationship.NewHandler(repos.Relationship, repos.RelationshipType)
	exportJobHandler := export.NewJobHandler(repos.ExportJobs, exportWorker, opts.Blobs).WithDATEVGate(datevGate)
	// The delegating actions of POST /cis/bulk reach the edges, the
	// lifecycle transition and the export jobs through adapters (WP-780).
	if opts.CIService != nil {
		opts.CIService.WithBulkDelegates(CIBulkDelegates(relationshipHandler, repos.Relationship, lifecycleService,
			repos.LifecycleResolver, exportJobHandler, opts.Entitlements))
	}
	// The movements, assignments and lifecycle transitions of POST
	// /assets/bulk (WP-781) and its export jobs with entity asset (WP-782).
	assetDelegates := AssetBulkDelegates(movements, repos.AssignmentService, lifecycleService, repos.LifecycleResolver, opts.Entitlements)
	assetDelegates.Exporter = AssetBulkExporter(exportJobHandler)
	assetHandler.WithBulkDelegates(&assetDelegates)
	registrars := []registrar{
		identityHandler,
		operatorHandler(&opts),
		alertBridge(&repos, &opts),
		apiKeyHandler(&repos, &opts),
		user.NewServiceAccountHandler(repos.ServiceAccounts),
		invitationHandler(&repos, &opts),
		entitlement.NewHandler(withUsageCounters(opts.Entitlements, &repos)),
		ciHandler(&repos, &opts),
		jobAPI,
		notifyHandler(&opts),
		relationshipHandler,
		webhook.NewHandler(repos.Webhook, opts.Dispatcher).WithEgress(opts.Egress),
		discoveryHandler,
		topologyHandler,
		export.NewHandler(repos.CI).WithDATEVGate(datevGate),
		exportJobHandler,
		assetHandler,
		assignment.NewHandler(repos.Assignment).WithService(signingAssignments(&repos, &opts)),
		documentHandler,
		// The completion books its corrections through the movement
		// service (WP-761); without PostgreSQL it answers 501.
		stocktake.NewHandler(repos.Stocktake).WithService(stocktake.NewService(repos.Stocktake, movements)),
		consumable.NewHandler(repos.Consumable),
		// The goods receipt books through the movement service (WP-760).
		order.NewHandler(repos.Order).WithBookings(movements),
		maintenance.NewHandler(repos.Maintenance),
		disposal.NewHandler(repos.Disposal).WithLifecycle(movements, lifecycleService, repos.LifecycleResolver, repos.Lifecycle),
		keymgmt.NewHandler(repos.Key),
		training.NewHandler(repos.Training),
		desk.NewHandler(repos.Desk),
		location.NewHandler(repos.Location),
		agentHandler(&repos, opts.Sessions, conflictReviews),
		security.NewHandler(repos.Security),
		// The attachments of a ticket are its linked documents (WP-765).
		ticketHandler(tickets, &repos, &opts),
		userHandler(&repos, &opts),
		permission.NewHandler(repos.Permission),
		searchHandler,
		sla.NewHandler(repos.SLA, repos.Ticket),
		form.NewHandler(repos.Form),
		workflow.NewHandler(repos.Workflow, workflowRuns),
		compliance.NewHandler(repos.Compliance, compliance.NewEvaluator(repos.Compliance, repos.CI)).
			WithReports(compliance.NewReportService(repos.Compliance, reportAuditVerifier(&opts), entitlementLister{svc: opts.Entitlements})),
		iga.NewHandler(repos.IGA, repos.User, opts.Credentials, repos.Discovery, repos.Workflow).
			WithWorkflowRuns(accessRequestRuns(opts.Modules, workflowRuns)).WithEgress(opts.Egress),
		tenantapi.NewHandler(repos.TenantHierarchy),
		rack.NewHandler(repos.Rack),
		contact.NewHandler(repos.Contact),
		contract.NewHandler(repos.Supplier),
		contract.NewContractHandler(repos.Contract),
		// Duplicate addresses open duplicate_ip items in the review queue of
		// discovery (NET-06, WP-300).
		ipam.NewHandler(repos.IPAM).WithReviews(discoveryHandler.ReviewService()),
		monitoringHandler,
		// The BFF gates the fields of late modules itself (WP-311): its
		// route belongs to no module prefix.
		graphqlbff.NewHandler(repos.CI, repos.Relationship).WithEntitlements(opts.Entitlements).
			WithSources(bffSources(&repos, topologyHandler, bffJobs, FormPolicyFactory(&repos, fieldResolver))).WithModules(opts.Modules),
		relationshiptype.NewHandler(repos.RelationshipType),
		citype.NewHandler(repos.CIType, opts.Dispatcher).
			WithLifecycleModels(lifecycleModelExport{repo: repos.Lifecycle}).WithRoles(roleExport{repo: repos.User}).
			WithGrantExporter(fieldGrantExport{repo: repos.FieldGrants}).
			// The resolved forms follow the field permissions (WP-727).
			WithFormPolicy(FormPolicyFactory(&repos, fieldResolver)).
			// Computed asset sources are part of the policy (SR-58).
			WithFieldPolicyInvalidator(fieldResolver),
		// Administration of the field permissions (RBA-07, WP-726); a write
		// drops the resolver's snapshot after the commit.
		permission.NewFieldGrantHandler(repos.FieldGrants, fieldResolver),
		draftHandler,
		metamodelImportHandler(&repos),
		importer.NewHandler(repos.Import),
		importHandler,
		lifecycle.NewHandler(repos.Lifecycle, lifecycleService, repos.LifecycleResolver).WithCIScope(lifecycleCIScope(repos.CI)),
		locationsHandler,
		// The code lookup checks the read permission and the feature of
		// the object's kind; the path itself is gated with barcode
		// (WP-762, E-44).
		labels.NewHandler(repos.Labels).WithFeatures(opts.Entitlements),
		// The label batches check the read permission and the feature of
		// the kind like the lookup (WP-763).
		labelBatch,
		movement.NewHandler(movements).WithAssets(assetCreator{repo: repos.Asset}),
		reservationHandler(&repos, &opts),
		// The overrides follow the field permissions of the CI (WP-265).
		override.NewHandler(repos.Override, opts.Dispatcher).WithCIs(overrideCILookup{repo: repos.CI, fields: repos.CIType}),
		historyHandler,
		// Asset references of the structured query need the module
		// inventory and assets_basic (WP-309); the route stays ungated.
		savedview.NewHandler(repos.SavedView).WithQueryEngine(repos.FilterQuery).WithPermissions(repos.Permission).
			WithEntitlements(aiEntitlements).WithInventory(opts.Modules.Enabled(ModuleInventory)),
		credentialHandler,
		// ADMP accounts (ADM-01, WP-925): local configuration, the URLs
		// checked against the egress policy of the router.
		admp.NewHandler(admp.NewAccountService(repos.ADMPAccounts, admp.CredentialsOf(opts.Credentials), opts.Egress)),
		ai.NewHandler(repos.AI, opts.AIProvider,
			ai.NewRetriever(repos.AI, repos.Search, repos.Permission, opts.AIProvider).WithConsent(aiConsent)).
			WithConsent(aiConsent),
		privacy.NewHandler(privacy.NewService(repos.Privacy, repos.Contact, repos.User)),
		signup.NewHandler(opts.Signup).WithProtection(opts.SignupProtection),
		modulesHandler{set: opts.Modules},
	}
	if templateJobs != nil {
		registrars = append(registrars, templateJobs)
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

// reviewTickets is the ticket side of the review queue (TKT-01, WP-291)
// over the ticket service: package reviews does not import package ticket.
type reviewTickets struct {
	svc *ticket.Service
	cis ci.Repository
	// features gates the tickets of an item on ticketing_essential; nil
	// checks none.
	features *entitlement.Service
}

// CreateTicket creates the ticket with its links in one transaction,
// exclusive for the item (at most one ticket that is not closed) and with
// the item as origin of the audit entry ticket.create. The fields the body
// sets must be editable (field permissions, WP-765); the answer is the
// ticket as the caller may read it.
func (a reviewTickets) CreateTicket(ctx context.Context, nt *reviews.NewTicket) (any, error) {
	policy, err := permission.PolicyFrom(ctx)
	if err != nil {
		return nil, err
	}
	if writeErr := permission.CheckWrite(policy, permission.KindTicket, "", permission.ObjectRef{TeamID: nt.TeamID},
		nt.RequestFields); writeErr != nil {
		return nil, writeErr
	}
	t := &ticket.Ticket{
		OrganizationID: nt.OrganizationID, Title: nt.Title, Description: nt.Description,
		Status: ticket.StatusNew, Priority: nt.Priority, Category: nt.Category,
		ReporterID: tenant.FromContext(ctx).UserID, AssigneeID: nt.AssigneeID, TeamID: nt.TeamID,
		RelatedCIID: nt.RelatedCIID, RelatedAssetID: nt.RelatedAssetID,
	}
	links := make([]ticket.Link, 0, len(nt.CIIDs)+1)
	for _, id := range nt.CIIDs {
		links = append(links, ticket.Link{EntityType: ticket.LinkCI, EntityID: id})
	}
	if nt.AssetID != "" {
		links = append(links, ticket.Link{EntityType: ticket.LinkAsset, EntityID: nt.AssetID})
	}
	item := &ticket.Target{EntityType: ticket.LinkReviewItem, EntityID: nt.ReviewItemID}
	err = a.svc.Create(ctx, t, links, ticket.CreateOptions{Exclusive: item, Origin: item})
	var open *ticket.ErrOpenTicketExists
	switch {
	case errors.As(err, &open):
		return nil, reviews.TicketConflict(open.ID, open.TicketNumber)
	case errors.Is(err, ticket.ErrNotFound):
		return nil, reviews.ErrNotFound
	case err != nil:
		return nil, err
	}
	return permission.ProjectValue(policy, permission.KindTicket, "",
		permission.ObjectRef{ID: t.ID, ClientID: t.ClientID, SiteID: t.SiteID, TeamID: t.TeamID}, t)
}

// LinkedTickets names the tickets of the items the caller sees, with one
// query; none without ticket:read or without ticketing_essential.
func (a reviewTickets) LinkedTickets(ctx context.Context, orgID string, itemIDs []string) (map[string][]reviews.TicketRef, error) {
	if p, ok := identity.PrincipalFromContext(ctx); !ok || !p.Has(identity.PermTicketRead) {
		return nil, nil
	}
	if a.features != nil && !a.features.IsEnabled(ctx, orgID, entitlement.FeatureTicketingEssential) {
		return nil, nil
	}
	linked, err := a.svc.LinkedTickets(ctx, orgID, ticket.LinkReviewItem, itemIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]reviews.TicketRef, len(linked))
	for id, list := range linked {
		refs := make([]reviews.TicketRef, len(list))
		for i := range list {
			refs[i] = reviews.TicketRef{ID: list[i].ID, TicketNumber: list[i].TicketNumber, Status: list[i].Status}
		}
		out[id] = refs
	}
	return out, nil
}

// The resolution of an item is commented on its open tickets (WP-767).
var _ reviews.ResolutionCommenter = reviewTickets{}

// OpenTickets names the tickets of the item that are not closed and that
// the caller sees under ticket RLS (WP-767).
func (a reviewTickets) OpenTickets(ctx context.Context, orgID, itemID string) ([]reviews.TicketRef, error) {
	linked, err := a.svc.LinkedTickets(ctx, orgID, ticket.LinkReviewItem, []string{itemID})
	if err != nil {
		return nil, err
	}
	var out []reviews.TicketRef
	for _, list := range linked {
		for i := range list {
			if list[i].Status != ticket.StatusClosed {
				out = append(out, reviews.TicketRef{ID: list[i].ID, TicketNumber: list[i].TicketNumber, Status: list[i].Status})
			}
		}
	}
	return out, nil
}

// CommentTicket adds the internal comment on the resolution of an item: by
// the resolver, or a system comment when the resolution has no actor or the
// actor is a service account (its API key acts as the account, which is no
// app_user and cannot author a user comment).
func (a reviewTickets) CommentTicket(ctx context.Context, orgID, ticketID, authorID, content string) error {
	if p, ok := identity.PrincipalFromContext(ctx); ok && p.ServiceAccountID != "" && p.Subject == authorID {
		authorID = ""
	}
	if authorID == "" {
		_, err := a.svc.AddSystemComment(ctx, orgID, ticketID, content)
		return err
	}
	return a.svc.AddComment(ctx, &ticket.Comment{OrganizationID: orgID, TicketID: ticketID, AuthorID: authorID,
		AuthorType: ticket.AuthorUser, Content: content, IsInternal: true})
}

// CIName returns the name of a CI the caller sees, "" for none.
func (a reviewTickets) CIName(ctx context.Context, orgID, ciID string) (string, error) {
	item, err := a.cis.GetByID(ctx, orgID, ciID)
	switch {
	case errors.Is(err, ci.ErrNotFound) || errors.Is(err, api.ErrNotFound):
		// A CI that is gone leaves the title without its name.
		return "", nil
	case err != nil:
		return "", err
	case item == nil:
		return "", nil
	}
	return item.Name, nil
}

// accessRequestRuns is the executor that starts the workflow run of an IGA
// access request (WFL-04): none while the workflows module is off (E-03),
// so a switched-off module executes no workflow action.
func accessRequestRuns(modules *ModuleSet, runs *workflow.Executor) iga.RunStarter {
	if !modules.Enabled("workflows") {
		return nil
	}
	return runs
}

// reportAuditVerifier returns the audit-chain verifier for the security
// report when a database pool exists; without a database the report omits the
// integrity section instead of failing.
func reportAuditVerifier(opts *Options) compliance.AuditVerifier {
	if opts.AuditPool == nil {
		return nil
	}
	return auditVerifier{pool: opts.AuditPool}
}

func validate(repos *Repositories, opts *Options) error {
	switch {
	case repos.Permission == nil:
		return fmt.Errorf("server: permission repository is required")
	case repos.FieldGrants == nil:
		return fmt.Errorf("server: field grant repository is required")
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
	case repos.ADMPAccounts == nil:
		return fmt.Errorf("server: ADMP account repository is required")
	case repos.ADMP == nil:
		return fmt.Errorf("server: ADMP cache repository is required")
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
	case repos.Override == nil:
		return fmt.Errorf("server: override repository is required")
	case repos.History == nil:
		return fmt.Errorf("server: history repository is required")
	case repos.SavedView == nil:
		return fmt.Errorf("server: saved view repository is required")
	case repos.Import == nil:
		return fmt.Errorf("server: import profile repository is required")
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
func metricsHandler(version string, includeTenantLabel bool) (handler http.Handler, record func(http.Handler) http.Handler) {
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
	jobs.RegisterMetrics(registry)
	audit.RegisterMetrics(registry)
	override.RegisterMetrics(registry)
	operator.RegisterMetrics(registry)
	entitlement.RegisterMetrics(registry)
	discovery.RegisterMetrics(registry)
	rack.RegisterMetrics(registry)
	iga.RegisterMetrics(registry)
	ipam.RegisterMetrics(registry)
	credential.RegisterMetrics(registry)
	admp.RegisterMetrics(registry)

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
	// Intents, entries and the scheduler's entries belong to the
	// organization's chain (E-08, WP-504); the auditor reads the rows of
	// the snapshot register in the principal's scope, so the entries of
	// routes without a domain recorder hold old and new values (AUD-07,
	// WP-505).
	return audit.NewPGRouteAuditor(opts.AuditPool)
}

// The production route auditor reads route snapshots (WP-505).
var _ middleware.RouteSnapshotter = (*audit.PGRouteAuditor)(nil)

// lifecycleCIScope reads the type and scope of a CI for the action grant
// of its lifecycle transition (WP-728).
func lifecycleCIScope(repo ci.Repository) lifecycle.CIScopeFunc {
	return func(ctx context.Context, orgID, ciID string) (string, permission.ObjectRef, error) {
		item, err := repo.GetByID(ctx, orgID, ciID)
		if err != nil {
			return "", permission.ObjectRef{}, err
		}
		return item.CITypeID, permission.ObjectRef{ID: item.ID, ClientID: item.ClientID, SiteID: item.SiteID}, nil
	}
}

// overrideCILookup reads the type, scope and instance attributes of a CI
// for the field permissions of its overrides (RBA-07, WP-265): the
// instance attributes the CI holds and those its definitions name.
type overrideCILookup struct {
	repo   ci.Repository
	fields any
}

func (l overrideCILookup) FieldScope(ctx context.Context, orgID, ciID string) (*override.CIScope, error) {
	item, err := l.repo.GetByID(ctx, orgID, ciID)
	if errors.Is(err, ci.ErrNotFound) {
		return nil, override.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	keys := slices.Clone(item.InstanceKeys)
	if lister, ok := l.fields.(ci.InstanceFieldLister); ok {
		defs, lerr := lister.ListInstanceFields(ctx, orgID, ciID)
		if lerr != nil {
			return nil, lerr
		}
		for i := range defs {
			keys = append(keys, defs[i].Name)
		}
	}
	return &override.CIScope{TypeID: item.CITypeID, ClientID: item.ClientID, SiteID: item.SiteID, InstanceKeys: keys}, nil
}

// reservationHandler builds the reservation handler; the PostgreSQL
// repository writes reservation.* to the outbox (WP-277), only without it
// (--no-db) the handler hands them to the webhook dispatcher directly.
func reservationHandler(repos *Repositories, opts *Options) *reservation.Handler {
	var h *reservation.Handler
	if repos.EventOutbox || opts.Dispatcher == nil {
		h = reservation.NewHandler(repos.Reservation)
	} else {
		h = reservation.NewHandler(repos.Reservation, opts.Dispatcher)
	}
	h.WithAvailability(repos.Availability)
	// Quantity reservations and their conversion need the feature stock
	// beyond the route gate assets_basic (E-44, WP-759).
	if opts.Entitlements != nil {
		h.WithEntitlements(opts.Entitlements)
	}
	return h
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
	// The restore window is the configured soft-delete retention (CI-05).
	if opts.RetentionSoftDeleteDays > 0 {
		opts.CIService.WithRestoreRetention(time.Duration(opts.RetentionSoftDeleteDays) * 24 * time.Hour)
	}
	// GET /cis/{id}?include= (API-05) embeds up to includeLimit related
	// rows of each kind; a kind with more rows the caller may see is marked
	// as truncated (F-223).
	const includeLimit = 200
	page := api.PaginationParams{Limit: includeLimit}
	if repos.Override != nil {
		h.WithProvenance(repos.Override)
	}
	// GET /cis/{id}/parent projects the parent asset (WP-742).
	h.WithParentAssets(ciParentAssetLookup{repo: repos.Asset})
	return h.WithIncludes(map[string]ci.IncludeFunc{
		"relationships": func(ctx context.Context, orgID, ciID string) (any, bool, error) {
			rels, total, err := repos.Relationship.List(ctx, orgID, ciID, page)
			if err != nil {
				return nil, false, err
			}
			// The edge fields as the principal may read them (RBA-07, WP-770).
			capped := total > len(rels)
			rels, err = relationship.ProjectEdges(ctx, orgID, rels, repos.RelationshipType)
			return rels, capped, err
		},
		"contacts": func(ctx context.Context, orgID, ciID string) (any, bool, error) {
			contacts, total, err := repos.Contact.ListForCI(ctx, orgID, ciID, page)
			if contacts == nil {
				contacts = []contact.CIContact{}
			}
			return contacts, total > len(contacts), err
		},
		// The targets of the reference values with their names (MET-12,
		// WP-713); never capped.
		ci.IncludeReferences: func(ctx context.Context, orgID, ciID string) (any, bool, error) {
			refs, err := opts.CIService.References(ctx, orgID, ciID)
			return refs, false, err
		},
	})
}

// notifyHandler serves the notification center and the channels.
func notifyHandler(opts *Options) *notify.Handler {
	if opts.Notifications == nil {
		opts.Notifications = notify.NewMemoryCenter()
	}
	return notify.NewHandler(opts.Notifications).WithEgress(opts.Egress)
}

// lifecycleModelExport reads the lifecycle models for the metamodel export
// v2 (WP-262): every model the organization sees with its states and
// transitions. In the export's snapshot the reads join its transaction.
type lifecycleModelExport struct{ repo lifecycle.Repository }

func (a lifecycleModelExport) ExportLifecycleModels(ctx context.Context, orgID string) ([]citype.ExportLifecycleModelV2, error) {
	out := []citype.ExportLifecycleModelV2{}
	for offset := 0; ; offset += api.MaxPageLimit {
		defs, total, err := a.repo.List(ctx, orgID, api.PaginationParams{Limit: api.MaxPageLimit, Offset: offset})
		if err != nil {
			return nil, err
		}
		for i := range defs {
			d, getErr := a.repo.GetByID(ctx, orgID, defs[i].ID)
			if getErr != nil {
				return nil, getErr
			}
			out = append(out, exportLifecycleModel(d))
		}
		if len(defs) == 0 || offset+len(defs) >= total {
			return out, nil
		}
	}
}

// exportLifecycleModel maps a lifecycle model onto the export v2: states and
// transitions by key, without ids and the version.
func exportLifecycleModel(d *lifecycle.Definition) citype.ExportLifecycleModelV2 {
	body := &citype.ExportLifecycleModelBody{Name: d.Name, AppliesTo: d.AppliesTo, Description: d.Description,
		States:      make([]citype.ExportLifecycleStateV2, 0, len(d.States)),
		Transitions: make([]citype.ExportLifecycleTransitionV2, 0, len(d.Transitions))}
	for i := range d.States {
		s := &d.States[i]
		body.States = append(body.States, citype.ExportLifecycleStateV2{Key: s.Key, Label: s.Label, IsInitial: s.IsInitial,
			IsTerminal: s.IsTerminal, SortOrder: s.SortOrder, RequiredFields: s.RequiredFields})
	}
	for i := range d.Transitions {
		t := &d.Transitions[i]
		body.Transitions = append(body.Transitions, citype.ExportLifecycleTransitionV2{Key: t.Key, Label: t.Label,
			FromStateKey: t.FromStateKey, ToStateKey: t.ToStateKey, RequiredFields: t.RequiredFields, Validation: t.Validation})
	}
	return citype.ExportLifecycleModelV2{ID: d.ID, Key: d.Key, IsSystem: d.IsSystem, ExportLifecycleModelBody: body}
}

// signingAssignments returns the assignment service with signatures
// (AST-02, WP-276): the entitlements decide the feature signature and the
// storage limit (fail-closed without them), the document repository counts
// the storage, the blob store keeps the image and the job queue scans it
// after the commit (only with the job repository, like the upload). Nil
// without a service (profile without database).
func signingAssignments(repos *Repositories, opts *Options) *assignment.Service {
	if repos.AssignmentService == nil {
		return nil
	}
	var scans document.ScanSubmitter
	if opts.JobQueue != nil && repos.Jobs != nil {
		scans = opts.JobQueue
	}
	return repos.AssignmentService.WithSignatures(opts.Entitlements, repos.Document, opts.Blobs, scans)
}

// importFeatureGate is the org gate of an import run (WP-775): the
// entitlements; nil without them, so a run checks no feature.
func importFeatureGate(opts *Options) importer.FeatureGate {
	if opts.Entitlements == nil {
		return nil
	}
	return opts.Entitlements
}

// metamodelImportHandler serves POST /api/v1/metamodel/import (WP-722) in
// both profiles: it reads the target with the export v2 builder (the
// readers of the export, the relationship types) and captures its items
// into a draft of repos.MetamodelDraft; without the database it answers
// 503.
func metamodelImportHandler(repos *Repositories) *metamodel.ImportHandler {
	var target metamodel.ImportTargetReader
	if src, ok := repos.CIType.(citype.ExportV2Source); ok && repos.RelationshipType != nil {
		target = metamodel.NewImportTarget(src, lifecycleModelExport{repo: repos.Lifecycle}, roleExport{repo: repos.User},
			repos.RelationshipType)
	}
	var store metamodel.ImportDraftStore
	if s, ok := repos.MetamodelDraft.(metamodel.ImportDraftStore); ok {
		store = s
	}
	return metamodel.NewImportHandler(target, store)
}

// roleExport reads the role names for the layouts of the metamodel export
// v2: the union of role (builtin, kind "role") and custom_role.
// fieldGrantExport adapts the field permission repository to the
// metamodel export v2 (WP-726): grants and sensitive fields with keys
// instead of ids, read in the export's snapshot transaction.
type fieldGrantExport struct {
	repo permission.FieldGrantRepository
}

func (a fieldGrantExport) ExportFieldPermissions(ctx context.Context, orgID string) (*citype.FieldPermissionsExportV2, error) {
	e, err := a.repo.Export(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := &citype.FieldPermissionsExportV2{
		FieldGrants: make([]citype.ExportFieldGrantV2, 0, len(e.FieldGrants)),
		Sensitivity: make([]citype.ExportSensitiveFieldV2, 0, len(e.Sensitivity)),
	}
	for i := range e.FieldGrants {
		g := &e.FieldGrants[i]
		out.FieldGrants = append(out.FieldGrants, citype.ExportFieldGrantV2{Role: g.Role, RoleKind: g.RoleKind,
			ObjectKind: string(g.ObjectKind), CIType: g.CIType, FieldKey: g.FieldKey, RelationshipType: g.RelationshipType,
			Action: g.Action, Level: string(g.Level)})
	}
	for i := range e.Sensitivity {
		s := &e.Sensitivity[i]
		out.Sensitivity = append(out.Sensitivity, citype.ExportSensitiveFieldV2{ObjectKind: string(s.ObjectKind), CIType: s.CIType,
			FieldKey: s.FieldKey})
	}
	return out, nil
}

type roleExport struct{ repo user.Repository }

func (a roleExport) ExportRoles(ctx context.Context, orgID string) ([]citype.ExportRole, error) {
	out := []citype.ExportRole{}
	for offset := 0; ; offset += api.MaxPageLimit {
		roles, total, err := a.repo.ListRoles(ctx, orgID, api.PaginationParams{Limit: api.MaxPageLimit, Offset: offset})
		if err != nil {
			return nil, err
		}
		for i := range roles {
			kind := citype.RoleKindCustomRole
			if roles[i].IsBuiltin {
				kind = citype.RoleKindRole
			}
			out = append(out, citype.ExportRole{ID: roles[i].ID, Name: roles[i].Name, Kind: kind})
		}
		if len(roles) == 0 || offset+len(roles) >= total {
			return out, nil
		}
	}
}

// exportDATEVGate is the gate of the export format datev (WP-790): the
// entitlements of the organization and the module inventory, passed as a
// value because export does not import server. Without entitlements DATEV
// is refused.
func exportDATEVGate(opts *Options) *export.DATEVGate {
	var entitlements export.EntitlementChecker
	if opts.Entitlements != nil {
		entitlements = opts.Entitlements
	}
	return export.NewDATEVGate(entitlements, opts.Modules.Enabled(ModuleInventory))
}

// ticketHandler builds the ticket routes; the attachments (documents
// linked to the ticket) exist only with the release module documents
// (SR-61, WP-874): without it GET /tickets/{id}/attachments is not
// registered and answers 404 like any unknown path.
func ticketHandler(tickets *ticket.Service, repos *Repositories, opts *Options) *ticket.Handler {
	h := ticket.NewHandler(tickets)
	if opts.Modules.Enabled(ModuleDocuments) && repos.Document != nil {
		h.WithDocuments(repos.Document)
	}
	return h
}

// withUsageCounters sets the counters of the usage report (UsageCounters)
// on svc.
func withUsageCounters(svc *entitlement.Service, repos *Repositories) *entitlement.Service {
	if svc == nil {
		return nil
	}
	return svc.WithUsageCounters(UsageCounters(repos))
}

// UsageCounters are the counters of the usage report (PKG-05, WP-885) from
// the repositories, with the counting rules of the AllowCreate callers:
// active CIs (ci.CountActive), collectors, active users, active API keys
// and the stored document bytes in MiB (rounded up).
func UsageCounters(repos *Repositories) entitlement.UsageCounters {
	counters := entitlement.UsageCounters{}
	if repos.CI != nil {
		counters[entitlement.LimitMaxCIs] = func(ctx context.Context, orgID string) (int64, error) {
			return ci.CountActive(ctx, repos.CI, orgID)
		}
	}
	if repos.Discovery != nil {
		counters[entitlement.LimitMaxCollectors] = repos.Discovery.CountCollectors
	}
	if repos.User != nil {
		counters[entitlement.LimitMaxUsers] = repos.User.CountActiveUsers
	}
	if repos.APIKeys != nil {
		counters[entitlement.LimitMaxAPIKeys] = func(ctx context.Context, orgID string) (int64, error) {
			return identity.CountActiveAPIKeys(ctx, repos.APIKeys, orgID, time.Now())
		}
	}
	if storage, ok := repos.Document.(interface {
		StorageUsage(ctx context.Context, orgID string) (int64, error)
	}); ok {
		counters[entitlement.LimitMaxStorageMB] = func(ctx context.Context, orgID string) (int64, error) {
			bytes, err := storage.StorageUsage(ctx, orgID)
			const mib = 1 << 20
			return (bytes + mib - 1) / mib, err
		}
	}
	return counters
}
