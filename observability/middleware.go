package observability

import (
	"context"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc"
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
