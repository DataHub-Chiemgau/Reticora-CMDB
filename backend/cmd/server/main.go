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

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
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

	ciRepo := ci.NewMemoryRepository()
	relRepo := relationship.NewMemoryRepository()
	webhookRepo := webhook.NewMemoryRepository()
	discoveryRepo := discovery.NewMemoryRepository()
	entitlementSvc := entitlement.NewService()
	webhookDispatcher := webhook.NewDispatcher(webhookRepo, nil)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := webhookDispatcher.Shutdown(ctx); err != nil {
			slog.Error("webhook dispatcher shutdown error", "error", err)
		}
	}()

	ciHandler := ci.NewHandler(ciRepo, webhookDispatcher)
	relHandler := relationship.NewHandler(relRepo)
	webhookHandler := webhook.NewHandler(webhookRepo)
	discoveryHandler := discovery.NewHandler(discoveryRepo, ciRepo)
	exportHandler := export.NewHandler(ciRepo)
	entitlementHandler := entitlement.NewHandler(entitlementSvc)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"ok"}`)
	})

	entitlementHandler.RegisterRoutes(mux)
	ciHandler.RegisterRoutes(mux)
	relHandler.RegisterRoutes(mux)
	webhookHandler.RegisterRoutes(mux)
	discoveryHandler.RegisterRoutes(mux)
	exportHandler.RegisterRoutes(mux)

	handler := middleware.Chain(
		middleware.Recovery,
		middleware.Logger,
		middleware.RequestID,
		middleware.TenantMiddleware,
	)(mux)

	server := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		slog.Info("starting server", "port", cfg.Port)
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
