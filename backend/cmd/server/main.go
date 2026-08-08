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
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	redisx "github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/redis"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
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

	// Initialize OIDC provider
	oidcProvider := identity.NewOIDCProvider(identity.OIDCConfig{
		IssuerURL:    cfg.OIDCIssuerURL,
		ClientID:     cfg.OIDCClientID,
		ClientSecret: cfg.OIDCClientSecret,
		RedirectURL:  cfg.OIDCRedirectURL,
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
		apiKeyStore  identity.APIKeyStore
	)

	if *noDB {
		slog.Warn("running with --no-db: all state is in-memory and lost on restart")
		repos = server.MemoryRepositories()
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
		apiKeyStore = identity.NewPGAPIKeyStore(pool)
	}
	if strings.EqualFold(cfg.SearchBackend, "opensearch") {
		if cfg.OpenSearchURL == "" {
			slog.Error("RETICORA_OPENSEARCH_URL is required when RETICORA_SEARCH_BACKEND=opensearch")
			os.Exit(1)
		}
		osBackend := search.NewOpenSearchBackend(search.OpenSearchConfig{
			URL: cfg.OpenSearchURL, Username: cfg.OpenSearchUsername, Password: cfg.OpenSearchPassword, Index: cfg.OpenSearchIndex,
		}, nil)
		if err := osBackend.Ping(); err != nil {
			slog.Error("failed to connect to OpenSearch", "error", err)
			os.Exit(1)
		}
		if pgSearch, ok := repos.Search.(*search.PGRepository); ok {
			repos.Search = &search.HybridBackend{Remote: osBackend, Source: pgSearch}
		} else {
			repos.Search = osBackend
		}
	}
	aiProvider := ai.NewOpenAIProvider(ai.ProviderConfig{
		BaseURL: cfg.LLMBaseURL, APIKey: cfg.LLMAPIKey, ChatModel: cfg.LLMChatModel, EmbeddingModel: cfg.LLMEmbeddingModel,
	}, nil)

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

	mux, err := server.NewRouter(repos, server.Options{
		Version:      version,
		Entitlements: entitlementSvc,
		Dispatcher:   webhookDispatcher,
		CIService:    ci.NewServiceWithLimits(repos.CI, entitlementSvc),
		Credentials:  credential.NewService(repos.Credential, encryptor),
		OIDC:         oidcProvider,
		Sessions:     sessionIssuer,
		Audit:        auditHandler,
		AIProvider:   aiProvider,
	})
	if err != nil {
		slog.Error("failed to build API router", "error", err)
		os.Exit(1)
	}

	// Session tokens are always verified cryptographically unless the operator
	// explicitly opted into the insecure development mode. API keys are
	// verified against the database when available, which gives service
	// tokens the same authenticated principal as interactive users.
	authMiddleware := middleware.AuthMiddlewareWithAPIKeys(sessionIssuer, identity.NewAPIKeyServiceWithStore(apiKeyStore))
	if sessionIssuer == nil {
		slog.Warn("INSECURE DEVELOPMENT MODE: bearer tokens are accepted without signature verification")
	}

	// Middleware chain per spec:
	// RequestID/Tracing -> Panic-Recovery -> Auth -> Tenant -> Entitlement ->
	// Rate-Limit -> POST-Idempotency -> Handler
	handler := middleware.Chain(
		middleware.RequestID,
		middleware.Recovery,
		middleware.Logger,
		middleware.OpenAPIValidation,
		authMiddleware,
		middleware.TenantMiddleware,
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
