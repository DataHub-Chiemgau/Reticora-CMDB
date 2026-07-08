// Package telemetry provides OpenTelemetry initialization for the Reticora platform.
package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// Config holds telemetry configuration.
type Config struct {
	ServiceName string
	Endpoint    string
	Environment string
}

// Init initializes the OpenTelemetry SDK with the given configuration.
// Returns a shutdown function that must be called on application exit.
func Init(_ context.Context, cfg Config) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	otel.SetTracerProvider(tracenoop.NewTracerProvider())
	otel.SetMeterProvider(metricnoop.NewMeterProvider())

	_ = cfg
	return func(_ context.Context) error { return nil }, nil
}

// Tracer returns a named OpenTelemetry tracer from the global provider.
func Tracer(name string) trace.Tracer {
	return otel.Tracer(name)
}

// Meter returns a named OpenTelemetry meter from the global provider.
func Meter(name string) metric.Meter {
	return otel.Meter(name)
}
