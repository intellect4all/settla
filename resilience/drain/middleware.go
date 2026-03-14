package drain

import (
	"context"
	"net/http"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// HTTPMiddleware returns an http.Handler that rejects new requests during drain
// with 503 Service Unavailable and Connection: close, while tracking in-flight
// requests for graceful shutdown.
func HTTPMiddleware(drainer *Drainer, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !drainer.Accept() {
			w.Header().Set("Connection", "close")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"server is shutting down","code":"SERVICE_UNAVAILABLE"}`))
			return
		}

		done := drainer.Track()
		defer done()

		next.ServeHTTP(w, r)
	})
}

// GRPCUnaryInterceptor returns a gRPC unary server interceptor that rejects
// new requests during drain with codes.Unavailable so clients retry on another pod.
func GRPCUnaryInterceptor(drainer *Drainer) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		if !drainer.Accept() {
			return nil, status.Error(codes.Unavailable, "server is shutting down")
		}

		done := drainer.Track()
		defer done()

		return handler(ctx, req)
	}
}

// GRPCStreamInterceptor returns a gRPC stream server interceptor that rejects
// new streams during drain with codes.Unavailable.
func GRPCStreamInterceptor(drainer *Drainer) grpc.StreamServerInterceptor {
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		if !drainer.Accept() {
			return status.Error(codes.Unavailable, "server is shutting down")
		}

		done := drainer.Track()
		defer done()

		return handler(srv, ss)
	}
}
