// Package main provides the entry point for the Reticora Cloud backend server.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ai"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	redisx "github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/redis"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reservation"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
	"github.com/jackc/pgx/v5/pgxpool"
)

// version is set at build-time via -ldflags.
var version = "dev"

func main() {
	noDB := flag.Bool("no-db", false,
		"run with in-memory repositories for local development; all data is lost on restart")
	flag.Parse()

	cfg := config.Load()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

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
	encryptor, err = crypto.NewEnvelopeEncryptor(cfg.MasterKey)
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

	// Initialize Redis-backed cache store
	var cacheStore cache.Store
	if cfg.RedisURL != "" {
		redisClient, err := redisx.Connect(context.Background(), cfg.RedisURL)
		if err != nil {
			slog.Warn("failed to connect to Redis, falling back to in-memory cache", "error", err)
			cacheStore = cache.NewMemoryStore()
		} else {
			defer redisClient.Close()
			cacheStore = cache.NewRedisStore(redisClient.Unwrap())
			slog.Info("Redis cache store initialized")
		}
	} else {
		slog.Info("RETICORA_REDIS_URL not set, using in-memory cache")
		cacheStore = cache.NewMemoryStore()
	}

	// Repositories. PostgreSQL is the only supported production backend; the
	// in-memory implementations are reserved for tests and the explicit --no-db
	// development mode. A configured database that cannot be reached is a fatal
	// startup error rather than a silent downgrade to per-replica memory state.
	var (
		repos        server.Repositories
		auditHandler *audit.Handler
		auditPool    *pgxpool.Pool
		apiKeyStore  identity.APIKeyStore
		blobStore    blob.Store
	)

	if *noDB {
		slog.Warn("running with --no-db: all state is in-memory and lost on restart")
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
			slog.Error("failed to connect to database", "error", err, "url", maskDSN(cfg.DatabaseURL))
			os.Exit(1)
		}
		defer pool.Close()

		slog.Info("connected to PostgreSQL", "url", maskDSN(cfg.DatabaseURL))
		repos = server.PostgresRepositories(pool, audit.NewPGRecorder())
		auditHandler = audit.NewHandler(pool)
		auditPool = pool
		apiKeyStore = identity.NewPGAPIKeyStore(pool)

		// Asynchronous exports render into object storage and are served via
		// signed URLs. A missing or unreachable store disables job creation
		// (503) but never the streaming export.
		s3Store, s3Err := blob.NewS3Store(cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3UseSSL)
		if s3Err != nil {
			slog.Warn("S3 blob storage unavailable; asynchronous export jobs are disabled", "error", s3Err)
		} else {
			// Bootstrap the buckets the platform writes to so exports and
			// documents work out of the box instead of failing on first use.
			for _, bucket := range []string{export.ExportBucket, "reticora-documents", cfg.S3Bucket} {
				if bucket == "" {
					continue
				}
				if err := s3Store.EnsureBucket(context.Background(), bucket); err != nil {
					slog.Warn("failed to ensure S3 bucket", "bucket", bucket, "error", err)
				}
			}
			blobStore = s3Store
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
	// Keep the tenant search index in sync with every CI write (REST handler,
	// collector bulk ingest, workflow executor). Indexing is best-effort: a
	// failing search backend never breaks CI persistence, and the index can
	// always be rebuilt via POST /api/v1/search/reindex.
	repos.CI = ci.NewIndexingRepository(repos.CI, search.NewCIIndexer(repos.Search))
	aiProvider := ai.NewOpenAIProvider(ai.ProviderConfig{
		BaseURL: cfg.LLMBaseURL, APIKey: cfg.LLMAPIKey, ChatModel: cfg.LLMChatModel, EmbeddingModel: cfg.LLMEmbeddingModel,
	}, nil)
	// Mirror CI mutations into the retrieval chunk store so the governed RAG
	// assistant has tenant-owned content to ground its answers. Chunks are
	// embedded when an embedding model is configured and fall back to lexical
	// scoring otherwise.
	repos.CI = ci.NewIndexingRepository(repos.CI, ai.NewCIChunkIndexer(repos.AI, aiProvider))

	entitlementSvc := entitlement.NewService(repos.Entitlement, entitlement.Options{
		DefaultPlan: entitlement.Plan(cfg.DefaultPlan),
		Enforce:     cfg.EntitlementEnforcement,
	})
	slog.Info("entitlement enforcement configured",
		"default_plan", cfg.DefaultPlan, "enforced", cfg.EntitlementEnforcement)

	webhookDispatcher := webhook.NewDispatcher(repos.Webhook, nil, webhook.DispatcherOptions{
		Deliveries: repos.WebhookDeliveries,
	})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := webhookDispatcher.Shutdown(ctx); err != nil {
			slog.Error("webhook dispatcher shutdown error", "error", err)
		}
	}()

	mux, httpMetrics, err := server.NewRouter(repos, server.Options{
		Version:              version,
		MetricsTenantLabel:   cfg.MetricsTenantLabel,
		Entitlements:         entitlementSvc,
		Dispatcher:           webhookDispatcher,
		CIService:            ci.NewServiceWithLimits(repos.CI, entitlementSvc),
		Credentials:          credential.NewService(repos.Credential, encryptor),
		OIDC:                 oidcProvider,
		Sessions:             sessionIssuer,
		UserProvisioner:      userProvisioner(repos),
		DefaultProvisionRole: cfg.DefaultProvisionRole,
		Audit:                auditHandler,
		AuditPool:            auditPool,
		AIProvider:           aiProvider,
		Blobs:                blobStore,
	})
	if err != nil {
		slog.Error("failed to build API router", "error", err)
		os.Exit(1)
	}

	// Process queued asynchronous export jobs until shutdown. All replicas
	// share the queue via FOR UPDATE SKIP LOCKED, so jobs are never run twice.
	if blobStore != nil {
		exportWorker := export.NewJobWorker(repos.ExportJobs, repos.CI, blobStore)
		workerCtx, stopWorker := context.WithCancel(context.Background())
		defer stopWorker()
		go exportWorker.Run(workerCtx, 2*time.Second)
	}

	// Evaluate monitoring alert rules against the metric store until shutdown.
	// Fired alerts are logged via the default notifier; evaluation state is
	// persisted in the alert store so restarts neither re-notify nor lose
	// pending durations.
	if alertStore, ok := repos.Metrics.(monitoring.EvaluatingStore); ok {
		evaluator := monitoring.NewEvaluator(repos.Metrics, alertStore.AlertStore(), nil)
		evalCtx, stopEvaluator := context.WithCancel(context.Background())
		defer stopEvaluator()
		go evaluator.Run(evalCtx, time.Minute)
	}

	// Release expired reservations until shutdown (spec §10). The sweeper is
	// interval-driven and shares no state with request handling.
	sweeper := reservation.NewSweeper(repos.Reservation, webhookDispatcher, time.Minute)
	sweepCtx, stopSweeper := context.WithCancel(context.Background())
	defer stopSweeper()
	go sweeper.Run(sweepCtx)

	// Session tokens are always verified cryptographically unless the operator
	// explicitly opted into the insecure development mode. API keys are
	// verified against the database when available, which gives service
	// tokens the same authenticated principal as interactive users.
	authMiddleware := middleware.AuthMiddlewareWithAPIKeys(sessionIssuer, identity.NewAPIKeyServiceWithStore(apiKeyStore))
	if sessionIssuer == nil {
		slog.Warn("INSECURE DEVELOPMENT MODE: bearer tokens are accepted without signature verification")
	}

	// Middleware chain per spec:
	// RequestID/Tracing -> Panic-Recovery -> Security-Headers -> Auth ->
	// Tenant -> Entitlement -> Rate-Limit -> POST-Idempotency -> Handler
	// The HTTP metrics middleware sits just inside the tenant middleware so the
	// organization_id label is populated from the request context.
	handler := middleware.Chain(
		middleware.RequestID,
		middleware.Recovery,
		middleware.Logger,
		middleware.SecurityHeaders,
		middleware.OpenAPIValidation,
		authMiddleware,
		middleware.TenantMiddleware,
		httpMetrics,
		entitlementSvc.Middleware,
		middleware.RateLimiterWithStore(cfg.RateLimitRPM, cacheStore),
		middleware.IdempotencyWithStore(cacheStore),
	)(mux)

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
	return issuer, nil
}

// maskDSN hides password from database URL for logging.
func maskDSN(dsn string) string {
	// Mask the password portion of user:password@host/db
	atIdx := strings.Index(dsn, "@")
	if atIdx < 0 {
		return dsn
	}
	colonIdx := strings.Index(dsn, "://")
	if colonIdx < 0 {
		return "***"
	}
	prefix := dsn[:colonIdx+3]
	rest := dsn[colonIdx+3:]
	passStart := strings.Index(rest, ":")
	passEnd := strings.Index(rest, "@")
	if passStart < 0 || passEnd < 0 || passStart >= passEnd {
		return dsn
	}
	return prefix + rest[:passStart+1] + "***" + rest[passEnd:]
}
