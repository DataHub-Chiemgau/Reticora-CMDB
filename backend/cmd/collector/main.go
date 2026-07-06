// Package main provides the entry point for the Reticora Collector.
// The Collector runs in customer networks and handles Discovery, Provisioning and Agent-Relay.
package main

import (
	"context"
	"crypto/tls"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
)

func main() {
	cfg := config.Load()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.LogLevel,
	}))
	slog.SetDefault(logger)

	slog.Info("starting collector", "environment", cfg.Environment)

	// mTLS configuration placeholder – enrollment and cert rotation will be added
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS13,
	}
	_ = tlsConfig

	// NATS connection placeholder
	natsURL := cfg.NATSUrl
	if natsURL == "" {
		natsURL = "nats://localhost:4222"
	}
	slog.Info("collector configured", "nats_url", natsURL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start discovery scheduler (placeholder)
	go runDiscoveryLoop(ctx)

	// Start agent relay listener (placeholder)
	go runAgentRelay(ctx)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	slog.Info("collector shutting down")
	cancel()
}

func runDiscoveryLoop(ctx context.Context) {
	slog.Info("discovery scheduler started")
	<-ctx.Done()
}

func runAgentRelay(ctx context.Context) {
	slog.Info("agent relay started")
	<-ctx.Done()
}
