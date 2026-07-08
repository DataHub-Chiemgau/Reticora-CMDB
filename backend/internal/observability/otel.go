// Package observability sets up OpenTelemetry tracing and metrics.
package observability

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// ShutdownFunc is a function that flushes and shuts down telemetry exporters.
type ShutdownFunc func(ctx context.Context) error

// Init initializes the OpenTelemetry SDK with the given OTLP endpoint.
// If endpoint is empty, a no-op provider is used (suitable for development).
// When a real OTLP exporter is needed, add the SDK dependency and replace the
// noop setup below with otlptracehttp/otlpmetrichttp exporters.
func Init(endpoint, serviceName, environment string) (ShutdownFunc, error) {
	// Always set up propagators for distributed tracing context
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	if endpoint == "" {
		slog.Info("observability: no OTEL endpoint configured, using no-op providers")
		return func(_ context.Context) error { return nil }, nil
	}

	slog.Info("observability: OTLP endpoint configured (SDK exporters pending)",
		"endpoint", endpoint,
		"service", serviceName,
		"env", environment,
	)

	// NOTE: To enable full OTLP export, add these dependencies:
	//   go.opentelemetry.io/otel/sdk
	//   go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp
	//   go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp
	// Then initialize TracerProvider and MeterProvider here.
	// For now, the global noop providers are used but propagation is active.

	return func(_ context.Context) error {
		slog.Info("observability: shutting down telemetry")
		return nil
	}, nil
}

// Tracer returns a named tracer from the global provider.
func Tracer(name string) any {
	return otel.Tracer(name)
}
