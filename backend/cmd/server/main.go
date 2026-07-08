// Package main provides the entry point for the Reticora Cloud backend server.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/graphqlbff"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/stocktake"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
	"github.com/go-chi/chi/v5"
)

func main() {
	cfg := config.Load()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	shutdown, err := observability.Init(cfg.OTelEndpoint, "reticora-server", cfg.Environment)
	if err != nil {
		slog.Error("failed to init observability", "error", err)
		os.Exit(1)
	}
	defer shutdown(context.Background())

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

	// Repositories (memory for dev, replace with PG in production)
	ciRepo := ci.NewMemoryRepository()
	relRepo := relationship.NewMemoryRepository()
	webhookRepo := webhook.NewMemoryRepository()
	discoveryRepo := discovery.NewMemoryRepository()
	entitlementSvc := entitlement.NewService()
	assetRepo := asset.NewMemoryRepository()
	assignmentRepo := assignment.NewMemoryRepository()
	documentRepo := document.NewMemoryRepository()
	stocktakeRepo := stocktake.NewMemoryRepository()
	ticketRepo := ticket.NewMemoryRepository()
	userRepo := user.NewMemoryRepository()
	webhookDispatcher := webhook.NewDispatcher(webhookRepo, nil)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := webhookDispatcher.Shutdown(ctx); err != nil {
			slog.Error("webhook dispatcher shutdown error", "error", err)
		}
	}()

	// Handlers
	identityHandler := identity.NewHandler(oidcProvider, sessionIssuer)
	ciHandler := ci.NewHandler(ciRepo, webhookDispatcher)
	relHandler := relationship.NewHandler(relRepo)
	webhookHandler := webhook.NewHandler(webhookRepo)
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

	mux := chi.NewRouter()

	// Health and metrics endpoints (no auth)
	mux.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"ok"}`)
	})
	mux.Get("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "# Reticora metrics endpoint\n")
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

	// Middleware chain per spec:
	// RequestID/Tracing -> Panic-Recovery -> Auth -> Tenant -> Entitlement ->
	// Rate-Limit -> POST-Idempotency -> Handler
	handler := middleware.Chain(
		middleware.RequestID,
		middleware.Recovery,
		middleware.Logger,
		middleware.AuthMiddleware,
		middleware.TenantMiddleware,
		middleware.RateLimiter(cfg.RateLimitRPM),
		middleware.Idempotency,
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
