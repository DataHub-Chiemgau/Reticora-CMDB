// Package telemetry provides OpenTelemetry initialization for the Reticora platform.
package telemetry

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
)

// Config holds telemetry configuration.
type Config struct {
	ServiceName string
	Endpoint    string
	Environment string
}

// Init initializes the OpenTelemetry SDK with the given configuration.
// Returns a shutdown function that must be called on application exit.
func Init(ctx context.Context, cfg Config) (func(context.Context) error, error) {
	if cfg.Endpoint == "" {
		// No-op: telemetry disabled
		return func(_ context.Context) error { return nil }, nil
	}

	// Set global text map propagator for trace context propagation
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// TODO: Initialize OTLP exporter, tracer provider, and meter provider
	// when the full OpenTelemetry SDK dependencies are added.
	_ = fmt.Sprintf("telemetry endpoint: %s", cfg.Endpoint)

	return func(_ context.Context) error { return nil }, nil
}
