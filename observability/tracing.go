package observability

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// InitTracer sets up an OpenTelemetry TracerProvider with OTLP gRPC exporter.
// Configuration is driven by standard OTEL_* environment variables:
//   - OTEL_EXPORTER_OTLP_ENDPOINT (e.g. "localhost:4317")
//   - OTEL_TRACES_SAMPLER (e.g. "parentbased_traceidratio")
//   - OTEL_TRACES_SAMPLER_ARG (e.g. "0.1" for 10% sampling)
//   - OTEL_SERVICE_NAME (fallback to serviceName parameter)
//
// Returns a shutdown function that should be deferred.
// If the OTLP endpoint is not configured, returns a no-op shutdown.
func InitTracer(ctx context.Context, serviceName, version string, logger *slog.Logger) (func(context.Context) error, error) {
	exporter, err := otlptracegrpc.New(ctx)
	if err != nil {
		logger.Warn("settla-tracing: OTLP exporter unavailable, tracing disabled", "error", err)
		return func(context.Context) error { return nil }, nil
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceNameKey.String(serviceName),
			semconv.ServiceVersionKey.String(version),
		),
	)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)

	logger.Info("settla-tracing: OpenTelemetry tracer initialized",
		"service", serviceName,
		"version", version,
	)

	return tp.Shutdown, nil
}
