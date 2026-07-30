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

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/cache"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/graphqlbff"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	redisx "github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/redis"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/stocktake"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
	"github.com/go-chi/chi/v5"
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

	// Initialize session issuer (optional in dev mode)
	var sessionIssuer *identity.SessionIssuer
	if cfg.SessionKeyPath != "" {
		keyData, err := os.ReadFile(cfg.SessionKeyPath)
		if err != nil {
			slog.Warn("failed to read session key, using dev mode", "error", err)
		} else {
			sessionIssuer, err = identity.NewSessionIssuer(keyData)
			if err != nil {
				slog.Warn("failed to init session issuer, using dev mode", "error", err)
			}
		}
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
		ciRepo            ci.Repository
		relRepo           relationship.Repository
		webhookRepo       webhook.Repository
		discoveryRepo     discovery.Repository
		assetRepo         asset.Repository
		assignmentRepo    assignment.Repository
		documentRepo      document.Repository
		stocktakeRepo     stocktake.Repository
		ticketRepo        ticket.Repository
		userRepo          user.Repository
		credentialRepo    credential.Repository
		entitlementRepo   entitlement.Repository
		webhookDeliveries webhook.DeliveryStore
		auditHandler      *audit.Handler
	)

	if *noDB {
		slog.Warn("running with --no-db: all state is in-memory and lost on restart")
		ciRepo = ci.NewMemoryRepository()
		relRepo = relationship.NewMemoryRepository()
		webhookRepo = webhook.NewMemoryRepository()
		discoveryRepo = discovery.NewMemoryRepository()
		assetRepo = asset.NewMemoryRepository()
		assignmentRepo = assignment.NewMemoryRepository()
		documentRepo = document.NewMemoryRepository()
		stocktakeRepo = stocktake.NewMemoryRepository()
		ticketRepo = ticket.NewMemoryRepository()
		userRepo = user.NewMemoryRepository()
		credentialRepo = credential.NewMemoryRepository()
		entitlementRepo = entitlement.NewMemoryRepository()
		webhookDeliveries = webhook.NewMemoryDeliveryStore()
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
		auditRecorder := audit.NewPGRecorder()
		auditHandler = audit.NewHandler(pool)
		ciRepo = ci.NewPGRepositoryWithAudit(pool, auditRecorder)
		relRepo = relationship.NewPGRepository(pool)
		webhookRepo = webhook.NewPGRepository(pool)
		discoveryRepo = discovery.NewPGRepository(pool)
		assetRepo = asset.NewPGRepository(pool)
		assignmentRepo = assignment.NewPGRepository(pool)
		documentRepo = document.NewPGRepository(pool)
		stocktakeRepo = stocktake.NewPGRepository(pool)
		ticketRepo = ticket.NewPGRepository(pool)
		userRepo = user.NewPGRepository(pool)
		credentialRepo = credential.NewPGRepository(pool)
		entitlementRepo = entitlement.NewPGRepository(pool)
		webhookDeliveries = webhook.NewPGDeliveryStore(pool)
	}

	entitlementSvc := entitlement.NewService(entitlementRepo, entitlement.Options{
		DefaultPlan: entitlement.Plan(cfg.DefaultPlan),
		Enforce:     cfg.EntitlementEnforcement,
	})
	slog.Info("entitlement enforcement configured",
		"default_plan", cfg.DefaultPlan, "enforced", cfg.EntitlementEnforcement)

	webhookDispatcher := webhook.NewDispatcher(webhookRepo, nil, webhook.DispatcherOptions{
		Deliveries: webhookDeliveries,
	})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := webhookDispatcher.Shutdown(ctx); err != nil {
			slog.Error("webhook dispatcher shutdown error", "error", err)
		}
	}()

	// Handlers
	ciSvc := ci.NewServiceWithLimits(ciRepo, entitlementSvc)
	identityHandler := identity.NewHandler(oidcProvider, sessionIssuer)
	ciHandler := ci.NewHandler(ciSvc, webhookDispatcher)
	relHandler := relationship.NewHandler(relRepo)
	webhookHandler := webhook.NewHandler(webhookRepo, webhookDispatcher)
	discoveryHandler := discovery.NewHandler(discoveryRepo, ciRepo)
	exportHandler := export.NewHandler(ciRepo)
	entitlementHandler := entitlement.NewHandler(entitlementSvc)
	assetHandler := asset.NewHandler(assetRepo)
	assignmentHandler := assignment.NewHandler(assignmentRepo)
	documentHandler := document.NewHandler(documentRepo)
	stocktakeHandler := stocktake.NewHandler(stocktakeRepo)
	ticketHandler := ticket.NewHandler(ticketRepo)
	userHandler := user.NewHandler(userRepo)
	monitoringHandler := monitoring.NewHandler(monitoring.NewMemoryMetricStore())
	graphqlHandler := graphqlbff.NewHandler(ciRepo, relRepo)

	// Credential handler (requires envelope encryption)
	credentialHandler := credential.NewHandler(credential.NewService(credentialRepo, encryptor))

	mux := chi.NewRouter()

	// Health and metrics endpoints (no auth)
	mux.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"ok"}`)
	})
	mux.Get("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		// Expose application metrics in Prometheus exposition format.
		// When the OpenTelemetry SDK is fully initialized with a Prometheus exporter,
		// replace this with promhttp.Handler() from the OTel prometheus bridge.
		fmt.Fprintf(w, "# HELP reticora_up Whether the Reticora server is up.\n")
		fmt.Fprintf(w, "# TYPE reticora_up gauge\n")
		fmt.Fprintf(w, "reticora_up 1\n")
		fmt.Fprintf(w, "# HELP reticora_info Build and version information.\n")
		fmt.Fprintf(w, "# TYPE reticora_info gauge\n")
		fmt.Fprintf(w, "reticora_info{version=\"%s\"} 1\n", version)
	})

	// Register routes
	identityHandler.RegisterRoutes(mux)
	entitlementHandler.RegisterRoutes(mux)
	ciHandler.RegisterRoutes(mux)
	relHandler.RegisterRoutes(mux)
	webhookHandler.RegisterRoutes(mux)
	discoveryHandler.RegisterRoutes(mux)
	exportHandler.RegisterRoutes(mux)
	assetHandler.RegisterRoutes(mux)
	assignmentHandler.RegisterRoutes(mux)
	documentHandler.RegisterRoutes(mux)
	stocktakeHandler.RegisterRoutes(mux)
	ticketHandler.RegisterRoutes(mux)
	userHandler.RegisterRoutes(mux)
	monitoringHandler.RegisterRoutes(mux)
	graphqlHandler.RegisterRoutes(mux)
	credentialHandler.RegisterRoutes(mux)
	if auditHandler != nil {
		auditHandler.RegisterRoutes(mux)
	}

	// Session tokens are verified cryptographically whenever a session key is
	// configured. Without a key the server falls back to unverified claim
	// parsing, which is only acceptable for local development.
	authMiddleware := middleware.AuthMiddleware
	if sessionIssuer != nil {
		authMiddleware = middleware.AuthMiddlewareWithVerifier(sessionIssuer)
	} else {
		slog.Warn("RETICORA_SESSION_KEY_PATH not set; bearer tokens are accepted without signature verification")
	}

	// Middleware chain per spec:
	// RequestID/Tracing -> Panic-Recovery -> Auth -> Tenant -> Entitlement ->
	// Rate-Limit -> POST-Idempotency -> Handler
	handler := middleware.Chain(
		middleware.RequestID,
		middleware.Recovery,
		middleware.Logger,
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
