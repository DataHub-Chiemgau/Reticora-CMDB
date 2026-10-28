// Package main provides the entry point for the Reticora Cloud backend server.
package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ai"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/bootstrap"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/operator"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/httpx"
	redisx "github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/redis"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rack"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/signup"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// version is set at build-time via -ldflags.
var version = "dev"

func main() {
	// Operator command of the collector CA (COL-04): server collector-ca
	// status|rollover|retire [--force] (collectorca.go).
	if len(os.Args) > 1 && os.Args[1] == collectorCACommand {
		os.Exit(runCollectorCA(context.Background(), os.Args[2:], config.Load(), os.Stdout, os.Stderr))
	}
	noDB := flag.Bool("no-db", false,
		"run with in-memory repositories for local development; all data is lost on restart")
	checkIssuer := flag.Bool("check-oidc-issuer", false,
		"check that the configured OIDC issuer is reachable from this container and announces itself, then exit (used by the installer)")
	flag.Parse()

	cfg := config.Load()
	if *checkIssuer {
		os.Exit(runIssuerCheck(cfg))
	}

	// Every record passes the redaction handler: secret attributes and URL
	// passwords never reach the log (SEC-01).
	logger := slog.New(httpx.NewRedactingHandler(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})))
	slog.SetDefault(logger)
	// A deprecated switch or a worker switch without a worker (OPS-02,
	// F-289) is reported, not refused.
	for _, w := range cfg.Warnings() {
		slog.Warn("configuration warning", "warning", w)
	}

	// Fail-closed start (OPS-01): missing or invalid settings stop the server
	// instead of silently falling back to development defaults.
	if err := cfg.Validate(*noDB); err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	// readiness collects the dependencies /readyz verifies.
	var readiness []server.ReadinessCheck

	shutdown, err := observability.Init(cfg.OTelEndpoint, "reticora-server", cfg.Environment)
	if err != nil {
		slog.Error("failed to init observability", "error", err)
		os.Exit(1)
	}
	defer shutdown(context.Background())

	// Initialize envelope encryptor. Credentials are stored encrypted, so a
	// missing master key must fail startup instead of silently disabling the
	// credential API.
	var encryptor *crypto.EnvelopeEncryptor
	if cfg.MasterKey == "" {
		if !*noDB {
			slog.Error("RETICORA_MASTER_KEY is required for credential encryption")
			os.Exit(1)
		}
		slog.Warn("RETICORA_MASTER_KEY not set; generating an ephemeral development key")
		devKey, keyErr := crypto.GenerateMasterKey()
		if keyErr != nil {
			slog.Error("failed to generate development master key", "error", keyErr)
			os.Exit(1)
		}
		cfg.MasterKey = devKey
	}
	encryptor, err = crypto.NewEnvelopeEncryptor(cfg.MasterKey, strings.Split(cfg.MasterKeyPrevious, ",")...)
	if err != nil {
		slog.Error("failed to init envelope encryptor", "error", err)
		os.Exit(1)
	}
	slog.Info("envelope encryption initialized")

	// Initialize OIDC provider. When RETICORA_OIDC_CA_CERT_FILE is set the
	// provider calls (discovery, JWKS, token exchange) additionally trust that
	// PEM bundle, which is required when the issuer is served with a private
	// or not-yet-issued (self-signed bootstrap) certificate.
	oidcHTTPClient, err := identity.NewHTTPClientWithCA(cfg.OIDCCACertFile)
	if err != nil {
		slog.Error("OIDC CA bundle setup failed", "path", cfg.OIDCCACertFile, "error", err)
		os.Exit(1)
	}
	oidcProvider := identity.NewOIDCProvider(identity.OIDCConfig{
		IssuerURL:    cfg.OIDCIssuerURL,
		ClientID:     cfg.OIDCClientID,
		ClientSecret: cfg.OIDCClientSecret,
		RedirectURL:  cfg.OIDCRedirectURL,
		HTTPClient:   oidcHTTPClient,
	})

	// Initialize the session issuer. Signature verification of session tokens
	// is mandatory: without an explicit insecure-development opt-in a missing
	// or unreadable key is a fatal startup error instead of a silent downgrade
	// to accepting forged tokens.
	sessionIssuer, err := loadSessionIssuer(cfg)
	if err != nil {
		slog.Error("session token setup failed", "error", err)
		os.Exit(1)
	}

	// Initialize the Redis-backed cache store. An unreachable Redis is fatal:
	// the in-memory store is per replica and would silently weaken rate
	// limits, idempotency and the logout blacklist. Only --no-db and the
	// implicit development default may fall back, with a warning.
	var cacheStore cache.Store
	redisClient, err := redisx.Connect(context.Background(), cfg.RedisURL)
	switch {
	case err == nil:
		defer redisClient.Close()
		cacheStore = cache.NewRedisStore(redisClient.Unwrap())
		readiness = append(readiness, server.ReadinessCheck{Name: "redis", Address: redisClient.Unwrap().Options().Addr, Check: redisClient.Ping})
		slog.Info("Redis cache store initialized")
	case *noDB || (cfg.IsDevelopment() && !cfg.IsSet("RETICORA_REDIS_URL")):
		slog.Warn("Redis unreachable; using the per-process in-memory cache (development only)", "error", err)
		cacheStore = cache.NewMemoryStore()
	default:
		slog.Error("failed to connect to Redis", "error", err)
		os.Exit(1)
	}
	// Deactivation ends sessions immediately through the blacklist in the
	// shared cache store (AUT-02, TLC-04).
	// Refresh tokens rotate in the same store (AUT-02).
	var refreshSessions *identity.RefreshSessions
	if sessionIssuer != nil {
		sessionIssuer.WithRevocations(identity.NewSessionRevocations(cacheStore))
		refreshSessions = identity.NewRefreshSessions(cacheStore, sessionIssuer.Revocations())
	}

	// Repositories. PostgreSQL is the only supported production backend; the
	// in-memory implementations are reserved for tests and the explicit --no-db
	// development mode. A configured database that cannot be reached is a fatal
	// startup error rather than a silent downgrade to per-replica memory state.
	var (
		repos        server.Repositories
		auditHandler *audit.Handler
		auditPool    *pgxpool.Pool
		blobStore    blob.Store
	)

	if *noDB {
		slog.Warn("running with --no-db: all state is in-memory and lost on restart")
		// The derived secrets come from a derivation root of this process.
		if err = encryptor.UseEphemeralDerivationRoot(); err != nil {
			slog.Error("failed to draw the derivation root", "error", err)
			os.Exit(1)
		}
		repos = server.MemoryRepositories()
		// File-backed blob storage keeps asynchronous exports usable in the
		// development mode; the signed URL is a development-only file: URL.
		blobDir := cfg.BlobDir
		if blobDir == "" {
			blobDir = filepath.Join(os.TempDir(), "reticora-dev-blobs")
		}
		blobStore = blob.NewFileStore(blobDir)
	} else {
		if cfg.DatabaseURL == "" {
			slog.Error("RETICORA_DATABASE_URL is required; start with --no-db for an ephemeral development server")
			os.Exit(1)
		}
		pool, err := database.NewPool(context.Background(), cfg.DatabaseURL)
		if err != nil {
			slog.Error("failed to connect to database", "error", err, "url", config.MaskDSN(cfg.DatabaseURL))
			os.Exit(1)
		}
		defer pool.Close()

		slog.Info("connected to PostgreSQL", "url", config.MaskDSN(cfg.DatabaseURL))
		dbCheck := server.DatabaseReadiness(pool, server.RequiredExtensions)
		if err := dbCheck.Check(context.Background()); err != nil {
			slog.Error("database is not ready", "error", err)
			os.Exit(1)
		}
		readiness = append(readiness, dbCheck)
		// The derived secrets (Derive) come from the derivation root of the
		// installation, not from the master key, so they survive a
		// master-key rotation (SEC-06, WP-459). The first start draws it;
		// a root the master keys do not unseal stops the start: a new one
		// would change every derived secret.
		rootCreated, rootErr := crypto.LoadDerivationRoot(context.Background(), pool, encryptor)
		if rootErr != nil {
			slog.Error("failed to open the derivation root of the derived secrets", "error", rootErr)
			os.Exit(1)
		}
		if rootCreated {
			slog.Info("created the derivation root of the derived secrets: the keys of the collector NATS endpoint and " +
				"the pagination cursor key changed once; restart nats-edge if it was running (docs/ops/workers.md, key rotation)")
		}
		repos = server.PostgresRepositories(pool, audit.NewPGRecorder())
		auditHandler = audit.NewHandler(pool)
		auditPool = pool
		// Personal data in audit, CI history and events is stored as vault
		// surrogates (SEC-05).
		pii.SetDefault(pii.NewVault(pool, encryptor))

		// Asynchronous exports render into object storage and are served via
		// signed URLs. Without object storage, job creation answers 503 but
		// the streaming export stays available. Configured object storage
		// that cannot be used is fatal outside development.
		if cfg.S3Configured() {
			// /readyz probes the host:port the S3 client connects to, also
			// for a URL or an endpoint without port (F-127).
			s3Check, s3Err := server.ObjectStorageReadiness(cfg.S3Endpoint, cfg.S3UseSSL)
			var s3Store *blob.S3Store
			if s3Err == nil {
				s3Store, s3Err = setupObjectStorage(cfg)
			}
			switch {
			case s3Err == nil:
				blobStore = s3Store
				readiness = append(readiness, s3Check)
			case cfg.IsDevelopment():
				slog.Warn("S3 blob storage unavailable; asynchronous export jobs are disabled", "error", s3Err)
			default:
				slog.Error("S3 blob storage unavailable", "endpoint", cfg.S3Endpoint, "error", s3Err)
				os.Exit(1)
			}
		} else {
			slog.Warn("object storage not configured (RETICORA_S3_*); asynchronous export jobs are disabled")
		}
	}
	// Pagination cursors are signed with a key derived from the derivation
	// root, so every replica accepts the cursors of the others (API-02),
	// also after a master-key rotation.
	if err = api.SetCursorKey(encryptor.Derive("reticora/pagination-cursor/v1")); err != nil {
		slog.Error("failed to set the cursor key", "error", err)
		os.Exit(1)
	}
	// With NATS the discovery ingest runs over ingest.> (REC-01): HTTP
	// ingest publishes, the consumer processes. NATS is reported by /readyz.
	var ingestQueue *discovery.NATSIngestQueue
	if cfg.IsSet("RETICORA_NATS_URL") {
		addr, natsErr := hostPort(cfg.NATSUrl, "4222")
		if natsErr != nil {
			slog.Error("invalid RETICORA_NATS_URL", "error", natsErr)
			os.Exit(1)
		}
		readiness = append(readiness, server.TCPReadiness("nats", addr))
		queueCtx, cancelQueue := context.WithTimeout(context.Background(), 30*time.Second)
		ingestQueue, natsErr = discovery.NewNATSIngestQueue(queueCtx, cfg.NATSUrl, cfg.IngestLanes)
		cancelQueue()
		if natsErr != nil {
			slog.Error("failed to set up the ingest queue on NATS", "error", natsErr)
			os.Exit(1)
		}
		defer ingestQueue.Close()
		slog.Info("ingest lanes", "lanes", ingestQueue.Lanes())
	}
	// Jobs are distributed over the JOBS work queue on NATS (TEC-14); job
	// kinds register their handlers with jobQueue.Handle.
	var jobQueue *jobs.Queue
	// The domain events reach RETICORA_EVENTS over the same connection.
	var eventStream jetstream.JetStream
	// coreNATS also carries the collectors' requests from the endpoint
	// (credential bundles, COL-02).
	var coreNATS *nats.Conn
	if cfg.IsSet("RETICORA_NATS_URL") && repos.Jobs != nil {
		nc, natsErr := nats.Connect(cfg.NATSUrl, nats.Name("reticora-jobs"), nats.MaxReconnects(-1))
		if natsErr != nil {
			slog.Error("failed to connect to NATS for the job queue", "error", natsErr)
			os.Exit(1)
		}
		defer nc.Close()
		coreNATS = nc
		// A disable (PATCH /collectors/{id}) is announced on the internal
		// NATS; the edge worker of one replica cuts the collector's open
		// connections (COL-04, F-025).
		if discoveryRepo, ok := repos.Discovery.(*discovery.PGRepository); ok {
			discoveryRepo.WithDisableAnnouncer(nc)
		}
		js, natsErr := jetstream.New(nc)
		if natsErr != nil {
			slog.Error("failed to open JetStream for the job queue", "error", natsErr)
			os.Exit(1)
		}
		eventStream = js
		jobQueue = jobs.NewQueue(jobs.NewService(repos.Jobs), js)
		queueCtx, cancelQueue := context.WithTimeout(context.Background(), 30*time.Second)
		natsErr = jobQueue.Setup(queueCtx)
		cancelQueue()
		if natsErr != nil {
			slog.Error("failed to set up the job queue on NATS", "error", natsErr)
			os.Exit(1)
		}
	}
	if strings.EqualFold(cfg.SearchBackend, "opensearch") {
		if cfg.OpenSearchURL == "" {
			slog.Error("RETICORA_OPENSEARCH_URL is required when RETICORA_SEARCH_BACKEND=opensearch")
			os.Exit(1)
		}
		osBackend := search.NewOpenSearchBackend(search.OpenSearchConfig{
			URL: cfg.OpenSearchURL, Username: cfg.OpenSearchUsername, Password: cfg.OpenSearchPassword, Index: cfg.OpenSearchIndex,
		}, nil)
		if err := osBackend.Ping(context.Background()); err != nil {
			slog.Error("failed to connect to OpenSearch", "error", err)
			os.Exit(1)
		}
		// Apply the index template on every startup so the mapping is explicit
		// and reproducible instead of relying on dynamic mapping guesses.
		if err := osBackend.EnsureIndexTemplate(context.Background()); err != nil {
			slog.Error("failed to apply OpenSearch index template", "error", err)
			os.Exit(1)
		}
		if pgSearch, ok := repos.Search.(*search.PGRepository); ok {
			repos.Search = &search.HybridBackend{Remote: osBackend, Source: pgSearch}
		} else {
			repos.Search = osBackend
		}
	}
	// A rack placement moves the CI (and the CIs of its asset) in SQL; the
	// remote index learns of the new client and site only through this
	// decorator (TEC-12, WP-346). The PostgreSQL index follows by trigger.
	if hybrid, ok := repos.Search.(*search.HybridBackend); ok {
		repos.Rack = rack.NewIndexingRepository(repos.Rack, hybrid)
	}
	// In the air-gapped profile no external AI call is made at all (AI-02).
	aiProvider := ai.NewProvider(&ai.ProviderConfig{
		BaseURL: cfg.LLMBaseURL, APIKey: cfg.LLMAPIKey, ChatModel: cfg.LLMChatModel, EmbeddingModel: cfg.LLMEmbeddingModel,
	}, cfg.AirGapped, nil)
	slog.Info("AI provider configured", "air_gapped", cfg.AirGapped,
		"chat_enabled", aiProvider.Enabled(), "embeddings_enabled", aiProvider.EmbeddingsEnabled())

	// The entitlement cache lives in the shared store (Redis), so a change
	// invalidates every instance (ENT-03).
	entitlementSvc := entitlement.NewService(repos.Entitlement, entitlement.Options{
		Cache:   cacheStore,
		Enforce: cfg.EntitlementEnforcement,
	})
	// A missing entitlement row means not entitled (ENT-05): every
	// organization gets the missing rows of its plan at each start (F-210).
	if err = provisionEntitlements(context.Background(), entitlementSvc, auditPool, cfg.DefaultPlan); err != nil {
		slog.Error("entitlement provisioning failed", "error", err)
		os.Exit(1)
	}
	slog.Info("entitlement enforcement configured",
		"default_plan", cfg.DefaultPlan, "enforced", cfg.EntitlementEnforcement)

	// Webhook and connector destinations and own SMTP servers are user
	// input: every outbound call goes through the egress client or dialer
	// (SEC-08).
	egressPolicy := egress.Options{AllowPrivate: cfg.EgressAllowPrivate}
	// Notifications (NTF-01, NTF-04): channels with encrypted provider
	// settings, the in-app center and the delivery queue of the notifier.
	smtpSender := centralSMTP(cfg)
	// A configured central SMTP server is a dependency of the instance
	// (OPS-01, F-126): while it is unreachable, /readyz answers 503 and the
	// instance leaves the load balancer.
	if smtpSender != nil {
		readiness = append(readiness, server.SMTPReadiness(cfg.SMTPHost, cfg.SMTPPort, cfg.SMTPTLS))
	}
	notifications := notify.NewMemoryCenter()
	if auditPool != nil {
		notifications = &notify.Center{
			Channels:    notify.NewPGRepository(auditPool, notify.NewSealer(encryptor)),
			Inbox:       notify.NewPGInbox(auditPool),
			Preferences: notify.NewPGPreferences(auditPool),
			Deliveries:  notify.NewPGDeliveryStore(auditPool),
		}
	}
	notifier := notify.NewNotifier(notifications.Channels, notifications.Deliveries, notifications.Preferences,
		notify.NewSender(map[string]notify.Provider{
			notify.ProviderCentralSMTP: notify.NewEmailProvider(smtpSender),
			notify.ProviderSMTP:        notify.NewEmailProvider(smtpSender).WithEgress(egressPolicy),
			notify.ProviderInApp:       notify.NewInAppProvider(notifications.Inbox),
			notify.ProviderWebhook:     notify.NewWebhookProvider(egress.NewClient(egressPolicy)),
		})).
		// E-mail channels send only with notifications_email (ENT-05).
		WithEntitlements(entitlementSvc)
	if auditPool != nil {
		// E-mails of the categories a user bundles become digest items;
		// the scheduler kind notify.digest queues them per window (NTF-04).
		notifier.WithDigest(notify.NewPGDigestStore(auditPool))
	}
	webhookDispatcher := webhook.NewDispatcher(repos.Webhook, egress.NewClient(egressPolicy), webhook.DispatcherOptions{
		// Subscriptions bound to a service account receive only events whose
		// object the account may read (RBA-08).
		Access: user.ServiceAccountAccess{Repo: repos.ServiceAccounts},
		// Events without client_id/site_id in the payload are scoped by
		// the CIs and assets they reference.
		Objects:    objectResolverOf(auditPool),
		Deliveries: repos.WebhookDeliveries,
		// The retry worker runs under its operator switch below (OPS-02).
		PollInterval: -1,
	})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := webhookDispatcher.Shutdown(ctx); err != nil {
			slog.Error("webhook dispatcher shutdown error", "error", err)
		}
	}()

	credentialService := credential.NewService(repos.Credential, encryptor)
	// The internal CA of the collector certificates (COL-04) and the
	// collector NATS endpoint (COL-08).
	collectorPKI := collectorCA(auditPool, encryptor)
	// Late modules (phase P2 and later, add-ons) are off unless
	// RETICORA_MODULES enables them (WP-214, E-03).
	modules, err := server.ParseModules(cfg.Modules)
	if err != nil {
		slog.Error("invalid module configuration", "error", err)
		os.Exit(1)
	}
	slog.Info("late modules", "enabled", modules.EnabledKeys())
	// CI writes keep the search index in sync and, only with the module ai
	// (F-301), the retrieval chunks; the consent is the one the router wires
	// into the Ask path (AI-02).
	repos.CI = bootstrap.NewCIRepository(repos.CI, repos.Search, repos.AI, aiProvider,
		ai.NewOrgConsent(repos.AI, entitlementSvc), modules)
	// A worker of a switched-off module does not start (WP-214).
	workerEnabled := workerSwitch(cfg, modules)
	// The backlog per lane as the ingest worker's 30-s sampler read it last,
	// informational: it never takes the instance out of the load balancer,
	// and a request never asks NATS (WP-334, OPS-05). Only the ingest worker
	// samples, so an instance without it (the API deployment) has no
	// detail instead of an "unavailable" one on every probe.
	if ingestQueue != nil && workerEnabled(config.WorkerIngest) {
		readiness = append(readiness, server.ReadinessCheck{Name: "ingest", Detail: ingestQueue.ReadinessDetail})
	}
	// Every grant of org_admin binds the IdP account to MFA (WP-358).
	mfa := mfaEnforcer(cfg)
	// Session tokens are always verified cryptographically unless the operator
	// explicitly opted into the insecure development mode. API keys are
	// verified against the database when available, which gives service
	// tokens the same authenticated principal as interactive users.
	// A key acts with the intersection of its permissions and its owner's
	// current role assignments (AUT-04).
	apiKeys := identity.NewAPIKeyServiceWithStore(repos.APIKeys).WithOwnerAccess(repos.Permission).
		WithServiceAccountAccess(user.ServiceAccountAccess{Repo: repos.ServiceAccounts})
	// A nil *SessionIssuer must reach the middleware as a nil interface:
	// only then does the development login (AUT-03, development only) apply.
	var sessionVerifier middleware.SessionVerifier
	if sessionIssuer != nil {
		sessionVerifier = sessionIssuer
	}
	authMiddleware := middleware.AuthMiddlewareWithAPIKeys(sessionVerifier, apiKeys)
	if sessionIssuer == nil {
		slog.Warn("INSECURE DEVELOPMENT MODE: bearer tokens are accepted without signature verification")
	}

	if err = middleware.SetTrustedProxies(cfg.TrustedProxies); err != nil {
		slog.Error("invalid RETICORA_TRUSTED_PROXIES", "error", err)
		os.Exit(1)
	}

	handler, err := newHTTPHandler(repos, server.Options{
		Version:            version,
		Modules:            modules,
		MetricsTenantLabel: cfg.MetricsTenantLabel,
		Entitlements:       entitlementSvc,
		Dispatcher:         webhookDispatcher,
		CIService:          ci.NewServiceWithLimits(repos.CI, entitlementSvc).WithFieldResolver(repos.CIType),
		Credentials:        credentialService,
		OIDC:               oidcProvider,
		Sessions:           sessionIssuer,
		RefreshSessions:    refreshSessions,
		Operator: operator.Config{
			BreakGlassToken: cfg.OperatorToken,
			Group:           cfg.OperatorGroup,
			MFAACRValues:    strings.Split(cfg.OperatorMFAACR, ","),
		},
		OperatorPool:         auditPool,
		OpsAlerts:            operator.AlertConfig{Token: cfg.OpsAlertToken, OrgID: cfg.OpsAlertOrgID},
		Notifier:             notifier,
		UserProvisioner:      userProvisioner(repos),
		DefaultProvisionRole: cfg.DefaultProvisionRole,
		Audit:                auditHandler,
		AuditPool:            auditPool,
		AIProvider:           aiProvider,
		Blobs:                blobStore,
		Egress:               egressPolicy,
		Readiness:            readiness,
		IngestQueue:          ingestQueue,
		JobQueue:             jobQueue,
		Signup:               signupService(cfg, auditPool, smtpSender),
		Notifications:        notifications,
		Events:               emitterOf(auditPool),
		SignupProtection:     signupProtection(cfg, cacheStore),
		// Invitations need the database; in --no-db mode they answer 503.
		InvitationAccounts:  invitationAccounts(cfg),
		InvitationAcceptURL: invitationAcceptURL(cfg),
		MFA:                 mfa,
		CollectorCA:         collectorPKI,
	}, httpStages{
		preAuth:     middleware.PreAuthRateLimiter(cfg.PreAuthRateLimitRPM, cacheStore),
		auth:        authMiddleware,
		tenant:      middleware.TenantMiddleware,
		entitlement: entitlementSvc.Middleware,
		rateLimit:   middleware.RateLimiterWithLimits(middleware.RateLimits{PerCredential: cfg.RateLimitRPM, PerOrganization: cfg.RateLimitOrgRPM}, cacheStore),
		validation:  middleware.OpenAPIValidation,
		idempotency: middleware.IdempotencyWithStore(cacheStore),
	})
	if err != nil {
		slog.Error("failed to build API router", "error", err)
		os.Exit(1)
	}

	// The scheduler of the system jobs (JOB-02, OPS-03): advisory-lock
	// leader and stored catch-up state on PostgreSQL, process-local with
	// --no-db.
	var systemScheduler *jobs.Scheduler
	if auditPool != nil {
		systemScheduler = jobs.NewScheduler(jobs.NewPGLocker(auditPool), jobs.NewPGStateStore(auditPool))
	} else {
		systemScheduler = jobs.NewScheduler(jobs.NewMemoryLocker(), jobs.NewMemoryStateStore())
	}
	if err = registerSystemJobs(systemScheduler, &systemJobDeps{cfg: cfg, pool: auditPool, encryptor: encryptor,
		repos: &repos, modules: modules, entitlements: entitlementSvc, dispatcher: webhookDispatcher,
		jobQueue: jobQueue, notifier: notifier}); err != nil {
		slog.Error("failed to register a system job", "error", err)
		os.Exit(1)
	}

	// Background workers, each under its operator switch
	// RETICORA_WORKER_<NAME>_ENABLED (OPS-02), so they can run on dedicated
	// instances (workers.go).
	workers, err := buildWorkers(&workerDeps{cfg: cfg, pool: auditPool, encryptor: encryptor, repos: &repos, mfa: mfa,
		dispatcher: webhookDispatcher, scheduler: systemScheduler, blobs: blobStore, eventStream: eventStream,
		notifier: notifier, collectorPKI: collectorPKI, coreNATS: coreNATS, credentials: credentialService,
		jobQueue: jobQueue, ingestQueue: ingestQueue})
	if err != nil {
		slog.Error("failed to set up the workers", "error", err)
		os.Exit(1)
	}
	workerSet, err := server.StartWorkers(workerEnabled, workers...)
	if err != nil {
		slog.Error("worker registration failed", "error", err)
		os.Exit(1)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := workerSet.Stop(ctx); err != nil {
			slog.Error("worker shutdown error", "error", err)
		}
	}()

	// The profiling listener (RETICORA_PPROF_ADDR, WP-438): off unless set,
	// loopback only and development only (config.Validate).
	if cfg.PprofAddr != "" {
		profiling, addr, perr := startProfiling(cfg.PprofAddr)
		if perr != nil {
			slog.Error("failed to start the profiling listener", "error", perr)
			os.Exit(1)
		}
		defer profiling.Close()
		slog.Warn("profiling listener enabled", "addr", addr.String())
	}

	server := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		slog.Info("starting server", "port", cfg.Port, "environment", cfg.Environment)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down server")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		slog.Error("server shutdown error", "error", err)
	}
}

// httpStages are the configured stages of the API-04 request chain.
type httpStages struct {
	preAuth, auth, tenant, entitlement, rateLimit func(http.Handler) http.Handler
	// validation and idempotency run per route after its authorization.
	validation, idempotency func(http.Handler) http.Handler
}

// newHTTPHandler builds the API router and the middleware chain the server
// serves (API-04):
// RequestID/Tracing -> Recovery -> Security-Headers -> Pre-Auth-Limit
// (per IP, AUT-10) -> Auth -> Tenant -> Entitlement -> Rate-Limit ->
// [per route: Authz -> Validation -> Idempotency] -> Handler.
// Validation and idempotency are route middleware (server.Options), so
// they run after the route's authorization. The HTTP metrics middleware
// sits just inside the tenant middleware so the organization_id label is
// populated from the request context.
func newHTTPHandler(repos server.Repositories, opts server.Options, stages httpStages) (http.Handler, error) {
	opts.RouteMiddleware = []func(http.Handler) http.Handler{stages.validation, stages.idempotency}
	mux, httpMetrics, err := server.NewRouter(repos, opts)
	if err != nil {
		return nil, err
	}
	return middleware.Chain(
		middleware.RequestID,
		middleware.Recovery,
		middleware.Logger,
		middleware.SecurityHeaders,
		stages.preAuth,
		stages.auth,
		stages.tenant,
		httpMetrics,
		stages.entitlement,
		stages.rateLimit,
	)(mux), nil
}

// provisionEntitlements stores the missing entitlement rows of its plan for
// every organization (F-210); existing rows stay unchanged and the added
// feature keys are logged per organization. Without a database (--no-db)
// the demo organization gets the default plan.
func provisionEntitlements(ctx context.Context, svc *entitlement.Service, pool *pgxpool.Pool, defaultPlan string) error {
	plans := map[string]entitlement.Plan{}
	if pool == nil {
		plans[demoOrganizationID] = entitlement.Plan(defaultPlan)
	} else {
		stored, err := database.OrganizationPlans(ctx, pool)
		if err != nil {
			return err
		}
		for id, plan := range stored {
			plans[id] = entitlement.Plan(plan)
		}
	}
	n, err := svc.ProvisionOrganizations(ctx, plans)
	if n > 0 {
		slog.Info("entitlements provisioned", "organizations", n)
	}
	return err
}

// demoOrganizationID is the organization of migration 000054.
const demoOrganizationID = "00000000-0000-0000-0000-000000000001"

// userProvisioner adapts the user repository to the identity provisioning
// port. Only the PostgreSQL-backed repository supports durable first-login
// provisioning; the in-memory variant returns nil so --no-db smoke tests keep
// their previous behavior.
func userProvisioner(repos server.Repositories) identity.UserProvisioner {
	p, ok := repos.User.(*user.PGRepository)
	if !ok {
		return nil
	}
	return p
}

// loadSessionIssuer reads and parses the RS256 session signing key. A nil
// issuer (unverified development mode) is only returned when the operator
// explicitly opted in via RETICORA_ALLOW_INSECURE_DEV_AUTH; otherwise a
// missing or unreadable key is an error so the server fails fast instead of
// silently accepting unsigned tokens.
func loadSessionIssuer(cfg *config.Config) (*identity.SessionIssuer, error) {
	if cfg.SessionKeyPath == "" {
		if cfg.AllowInsecureDevAuth {
			slog.Warn("RETICORA_SESSION_KEY_PATH not set and RETICORA_ALLOW_INSECURE_DEV_AUTH=true; " +
				"session tokens will NOT be signature-verified — never use this outside local development")
			return nil, nil
		}
		return nil, fmt.Errorf("RETICORA_SESSION_KEY_PATH is required; " +
			"set RETICORA_ALLOW_INSECURE_DEV_AUTH=true to explicitly opt into insecure development mode")
	}

	keyData, err := os.ReadFile(cfg.SessionKeyPath)
	if err != nil {
		if cfg.AllowInsecureDevAuth {
			slog.Warn("failed to read session key and RETICORA_ALLOW_INSECURE_DEV_AUTH=true; "+
				"session tokens will NOT be signature-verified", "path", cfg.SessionKeyPath, "error", err)
			return nil, nil
		}
		return nil, fmt.Errorf("read session key %q: %w", cfg.SessionKeyPath, err)
	}

	issuer, err := identity.NewSessionIssuer(keyData)
	if err != nil {
		return nil, fmt.Errorf("parse session key %q: %w", cfg.SessionKeyPath, err)
	}
	// Previous signing keys stay valid for verification during a rotation
	// (SEC-06); tokens name their key in the kid header.
	for _, path := range strings.Split(cfg.SessionPreviousKeyPaths, ",") {
		if path = strings.TrimSpace(path); path == "" {
			continue
		}
		previous, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, fmt.Errorf("read previous session key %q: %w", path, readErr)
		}
		if err = issuer.WithPreviousKeys(previous); err != nil {
			return nil, fmt.Errorf("parse previous session key %q: %w", path, err)
		}
	}
	return issuer, nil
}

// setupObjectStorage connects the S3-compatible store and creates the buckets
// the platform writes to, so exports and documents work out of the box.
func setupObjectStorage(cfg *config.Config) (*blob.S3Store, error) {
	store, err := blob.NewS3Store(cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3UseSSL)
	if err != nil {
		return nil, err
	}
	for _, bucket := range []string{export.ExportBucket, "reticora-documents", cfg.S3Bucket} {
		if bucket == "" {
			continue
		}
		if err := store.EnsureBucket(context.Background(), bucket); err != nil {
			return nil, err
		}
		// Versioned buckets keep overwritten and deleted objects (NFR-05).
		// A store without versioning support keeps working, loudly.
		if err := store.EnableVersioning(context.Background(), bucket); err != nil {
			slog.Error("object storage: bucket versioning could not be enabled", "bucket", bucket, "error", err)
		}
	}
	return store, nil
}

// hostPort extracts host:port from a URL such as nats://nats:4222.
func hostPort(rawURL, defaultPort string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("no host in %q", rawURL)
	}
	port := u.Port()
	if port == "" {
		port = defaultPort
	}
	return net.JoinHostPort(u.Hostname(), port), nil
}

// runIssuerCheck fetches the discovery document of the configured issuer
// through the same HTTP client the server uses for sign-in (including
// RETICORA_OIDC_CA_CERT_FILE) and reports the result on stdout/stderr
// (INS-02). The installer runs it inside the backend container.
func runIssuerCheck(cfg *config.Config) int {
	client, err := identity.NewHTTPClientWithCA(cfg.OIDCCACertFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "OIDC CA bundle %s: %v\n", cfg.OIDCCACertFile, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err = identity.CheckIssuer(ctx, client, cfg.OIDCIssuerURL); err != nil {
		fmt.Fprintf(os.Stderr, "OIDC issuer check failed: %v\n", err)
		return 1
	}
	fmt.Printf("OIDC issuer %s is reachable\n", cfg.OIDCIssuerURL)
	return 0
}

// centralSMTP is the installation's SMTP server (TEC-13); nil without
// RETICORA_SMTP_HOST.
func centralSMTP(cfg *config.Config) *notify.SMTPSender {
	if cfg.SMTPHost == "" {
		return nil
	}
	sender, err := notify.NewSMTPSender(&notify.SMTPConfig{Host: cfg.SMTPHost, Port: cfg.SMTPPort, Username: cfg.SMTPUsername,
		Password: cfg.SMTPPassword, From: cfg.SMTPFrom, TLS: cfg.SMTPTLS})
	if err != nil {
		slog.Error("invalid central SMTP configuration", "error", err)
		os.Exit(1)
	}
	slog.Info("central SMTP configured", "host", cfg.SMTPHost, "port", cfg.SMTPPort, "tls", cfg.SMTPTLS)
	return sender
}

// signupService builds the self-signup flow (TLC-01). Signups need the
// database, the Keycloak admin client and the central SMTP server for the
// verification e-mail; without one of them every signup answers 503.
func signupService(cfg *config.Config, pool *pgxpool.Pool, smtpSender *notify.SMTPSender) *signup.Service {
	sc := signup.DefaultConfig()
	sc.TrialPlan = entitlement.Plan(cfg.TrialPlan)
	sc.TrialDays = cfg.TrialDays
	sc.TrialMaxCIs = int64(cfg.TrialMaxCIs)
	sc.VerifyURL = cfg.SignupVerifyURL
	var (
		repo     signup.Repository
		accounts signup.Accounts
	)
	if pool != nil {
		// New organizations get an e-mail channel over the central SMTP
		// server when there is one (F-157).
		repo = signup.NewPGRepository(pool).WithCentralSMTP(smtpSender != nil)
	}
	if kc := keycloakAccounts(cfg); kc != nil {
		accounts = kc
	}
	var mailer signup.Mailer
	if smtpSender != nil {
		mailer = notify.SignupMailer{Sender: smtpSender}
	}
	svc := signup.NewService(repo, mailer, accounts, sc)
	slog.Info("self-signup configured", "available", svc.Available())
	return svc
}

// keycloakAccounts is the Keycloak admin client that creates the accounts
// of signups, operator-created organizations and accepted invitations; nil
// without RETICORA_KEYCLOAK_ADMIN_CLIENT_ID. Its calls go to the issuer URL
// like those of the OIDC provider and therefore trust the same PEM bundle
// (RETICORA_OIDC_CA_CERT_FILE): with a private CA, or with a certificate the
// server image does not know, the admin token request otherwise fails with
// an x509 error while the sign-in works (WP-646). With
// RETICORA_KEYCLOAK_INTERNAL_URL the calls go to Keycloak directly, since the
// public reverse proxy does not expose the admin REST API.
func keycloakAccounts(cfg *config.Config) *signup.KeycloakAccounts {
	if cfg.KeycloakAdminClientID == "" {
		return nil
	}
	client, err := identity.NewHTTPClientWithCA(cfg.OIDCCACertFile)
	if err != nil {
		slog.Error("Keycloak admin client disabled", "path", cfg.OIDCCACertFile, "error", err)
		return nil
	}
	client, err = signup.RouteThroughInternalURL(client, cfg.OIDCIssuerURL, cfg.KeycloakInternalURL)
	if err != nil {
		slog.Error("Keycloak admin client disabled", "error", err)
		return nil
	}
	kc, err := signup.NewKeycloakAccounts(cfg.OIDCIssuerURL, cfg.KeycloakAdminClientID, cfg.KeycloakAdminClientSecret, client)
	if err != nil {
		slog.Error("Keycloak admin client disabled", "error", err)
		return nil
	}
	return kc
}

// invitationAccounts lets accepted invitations create their IdP account
// (TLC-03); nil leaves the account to the IdP.
func invitationAccounts(cfg *config.Config) user.InvitationAccounts {
	if kc := keycloakAccounts(cfg); kc != nil {
		return kc
	}
	return nil
}

// invitationAcceptURL is the page the link of the invitation mail opens;
// without it issuing and renewing invitations answers 503.
func invitationAcceptURL(cfg *config.Config) string {
	if cfg.InvitationAcceptURL == "" {
		slog.Warn("no RETICORA_INVITATION_ACCEPT_URL: issuing and renewing invitations answers 503 until it is configured")
	}
	return cfg.InvitationAcceptURL
}

// mfaEnforcer binds the IdP accounts of new org_admins to MFA through the
// Keycloak admin client (WP-358). Without the client every org_admin grant
// fails (503) outside development; in development nothing is bound.
func mfaEnforcer(cfg *config.Config) user.MFAEnforcer {
	if kc := keycloakAccounts(cfg); kc != nil {
		return kc
	}
	if cfg.IsDevelopment() {
		slog.Warn("no Keycloak admin client: org_admins are not bound to MFA (development only)")
		return nil
	}
	slog.Warn("no Keycloak admin client (RETICORA_KEYCLOAK_ADMIN_CLIENT_ID): granting org_admin fails until it is configured")
	return user.UnavailableMFA{}
}

// reconcileMFA binds the org_admins that exist already to MFA (WP-358). A
// run with failures (Keycloak still starting, for instance) is repeated a
// few times; the run is idempotent.
func reconcileMFA(ctx context.Context, users *user.PGRepository) {
	for attempt := 1; ; attempt++ {
		bound, failed, err := users.ReconcileMFA(ctx)
		if err != nil {
			slog.ErrorContext(ctx, "mfa reconciliation", "attempt", attempt, "error", err)
		} else {
			slog.InfoContext(ctx, "mfa reconciliation finished", "attempt", attempt, "bound", bound, "failed", failed)
		}
		if (err == nil && failed == 0) || attempt == 5 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

// signupProtection builds the captcha, the disposable-domain policy and the
// per-IP limit of the signup (WP-112, E-33). Without a configured key a
// random one is drawn: challenges then verify only on this instance.
func signupProtection(cfg *config.Config, store cache.Store) *signup.Protection {
	key := []byte(cfg.SignupCaptchaKey)
	if len(key) == 0 {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			slog.Error("self-signup protection disabled", "error", err)
			return nil
		}
		slog.Warn("RETICORA_SIGNUP_CAPTCHA_KEY not set; signup challenges verify only on this instance")
	}
	domains, err := signup.NewDomainPolicy(splitList(cfg.SignupBlockedDomains), splitList(cfg.SignupAllowedDomains))
	if err != nil {
		slog.Error("self-signup protection disabled", "error", err)
		return nil
	}
	p, err := signup.NewProtection(key, store, domains)
	if err != nil {
		slog.Error("self-signup protection disabled", "error", err)
		return nil
	}
	return p
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// emitterOf reports facts into the event outbox of the database; nil
// without one.
func emitterOf(pool *pgxpool.Pool) events.Emitter {
	if pool == nil {
		return nil
	}
	return events.NewPGEmitter(pool)
}

// objectResolverOf resolves the objects of webhook events in PostgreSQL;
// without a database (--no-db) bound subscriptions receive only events whose
// payload carries the scope.
func objectResolverOf(pool *pgxpool.Pool) webhook.ObjectResolver {
	if pool == nil {
		return nil
	}
	return webhook.NewPGObjectResolver(pool)
}

// collectorCA is the internal CA of the collector certificates (COL-04);
// it lives in the database, so --no-db mode has none.
func collectorCA(pool *pgxpool.Pool, enc *crypto.EnvelopeEncryptor) *discovery.CA {
	if pool == nil {
		return nil
	}
	return discovery.NewCA(pool, enc)
}
