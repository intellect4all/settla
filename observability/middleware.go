package observability

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// UnaryServerInterceptor returns a gRPC interceptor that records metrics for
// every unary RPC (request count, latency, status code).
func UnaryServerInterceptor(m *Metrics) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		service, method := splitMethodName(info.FullMethod)
		start := time.Now()

		resp, err := handler(ctx, req)

		duration := time.Since(start).Seconds()
		code := status.Code(err).String()
		reason := ""
		if err != nil {
			if st, ok := status.FromError(err); ok {
				reason = st.Message()
			}
		}

		m.GRPCRequestsTotal.WithLabelValues(service, method, code, reason).Inc()
		m.GRPCRequestLatency.WithLabelValues(service, method).Observe(duration)

		return resp, err
	}
}

// UnaryTraceInterceptor extracts W3C Trace Context from incoming gRPC metadata
// and injects it into the Go context so downstream spans are linked.
func UnaryTraceInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		ctx = extractTraceFromGRPC(ctx)
		return handler(ctx, req)
	}
}

// StreamTraceInterceptor extracts W3C Trace Context from incoming gRPC metadata
// for streaming RPCs so that server-side spans are linked to the caller's trace.
func StreamTraceInterceptor() grpc.StreamServerInterceptor {
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		ctx := extractTraceFromGRPC(ss.Context())
		return handler(srv, &tracedServerStream{ServerStream: ss, ctx: ctx})
	}
}

// tracedServerStream wraps a grpc.ServerStream with a trace-enriched context.
type tracedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *tracedServerStream) Context() context.Context { return s.ctx }

// extractTraceFromGRPC reads W3C traceparent/tracestate from gRPC metadata
// and returns a context with the extracted span context.
func extractTraceFromGRPC(ctx context.Context) context.Context {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx
	}
	return otel.GetTextMapPropagator().Extract(ctx, grpcMetadataCarrier(md))
}

// grpcMetadataCarrier adapts gRPC metadata.MD for OpenTelemetry propagation.
type grpcMetadataCarrier metadata.MD

func (c grpcMetadataCarrier) Get(key string) string {
	vals := metadata.MD(c).Get(key)
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

func (c grpcMetadataCarrier) Set(key, value string) {
	metadata.MD(c).Set(key, value)
}

func (c grpcMetadataCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}

// splitMethodName splits "/package.Service/Method" into ("Service", "Method").
func splitMethodName(fullMethod string) (string, string) {
	fullMethod = strings.TrimPrefix(fullMethod, "/")
	parts := strings.SplitN(fullMethod, "/", 2)
	if len(parts) != 2 {
		return "unknown", fullMethod
	}
	// Extract just the service name (after last dot).
	svc := parts[0]
	if idx := strings.LastIndex(svc, "."); idx >= 0 {
		svc = svc[idx+1:]
	}
	return svc, parts[1]
}

// MetricsHTTPHandler returns an http.Handler that serves Prometheus metrics.
// Import and use: http.Handle("/metrics", promhttp.Handler())
// This is just a convenience re-export note; actual handler comes from promhttp.

// FormatCorridor returns a corridor label like "GBP-NGN".
func FormatCorridor(source, dest string) string {
	return fmt.Sprintf("%s-%s", strings.ToUpper(source), strings.ToUpper(dest))
}
