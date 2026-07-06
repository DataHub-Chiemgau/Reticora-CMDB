// Package observability sets up OpenTelemetry tracing and metrics.
package observability

import (
	"context"
	"log/slog"
)

// ShutdownFunc is a function that flushes and shuts down telemetry exporters.
type ShutdownFunc func(ctx context.Context) error

// Init initializes the OpenTelemetry SDK with the given OTLP endpoint.
// If endpoint is empty, a no-op provider is used (suitable for development).
func Init(endpoint, serviceName, environment string) (ShutdownFunc, error) {
	if endpoint == "" {
		slog.Info("observability: no OTEL endpoint configured, using no-op")
		return func(ctx context.Context) error { return nil }, nil
	}

	slog.Info("observability: initializing OTLP exporter",
		"endpoint", endpoint,
		"service", serviceName,
		"env", environment,
	)

	// TODO: Initialize real OTLP trace/metric exporters with:
	// - go.opentelemetry.io/otel
	// - go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp
	// - go.opentelemetry.io/otel/sdk/trace
	// For now return a no-op shutdown to allow compilation without heavy dependencies.
	return func(ctx context.Context) error {
		slog.Info("observability: shutting down")
		return nil
	}, nil
}
