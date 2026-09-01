// Package observability sets up OpenTelemetry tracing and metrics.
package observability

import (
	"context"
	"fmt"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// ShutdownFunc is a function that flushes and shuts down telemetry exporters.
type ShutdownFunc func(ctx context.Context) error

// Init initializes the OpenTelemetry SDK. With an empty endpoint it installs
// no-op providers (suitable for development) and only activates context
// propagation. With a configured OTLP HTTP endpoint it sets up real trace and
// metric exporters with the service and environment resource attributes.
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

	ctx := context.Background()
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.DeploymentEnvironment(environment),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("observability: build resource: %w", err)
	}

	traceExporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("observability: build trace exporter: %w", err)
	}
	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tracerProvider)

	metricExporter, err := otlpmetrichttp.New(ctx,
		otlpmetrichttp.WithEndpoint(endpoint),
		otlpmetrichttp.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("observability: build metric exporter: %w", err)
	}
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExporter)),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(meterProvider)

	slog.Info("observability: OTLP exporters initialized",
		"endpoint", endpoint,
		"service", serviceName,
		"env", environment,
	)

	return func(ctx context.Context) error {
		slog.Info("observability: shutting down telemetry")
		// A briefly unreachable collector must not fail server shutdown; the
		// final batch is best-effort. Trace shutdown runs first so in-flight
		// spans are flushed before the process exits.
		if err := tracerProvider.Shutdown(ctx); err != nil {
			slog.Warn("observability: trace shutdown flush failed", "error", err)
		}
		if err := meterProvider.Shutdown(ctx); err != nil {
			slog.Warn("observability: metric shutdown flush failed", "error", err)
		}
		return nil
	}, nil
}

// Tracer returns a named tracer from the global provider.
func Tracer(name string) any {
	return otel.Tracer(name)
}
