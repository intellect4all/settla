package grpc

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcmd "google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// unauthenticatedMethods is the set of gRPC methods that do not require
// API key authentication. These are public endpoints or portal auth methods
// that use their own JWT-based auth.
var unauthenticatedMethods = map[string]bool{
	// Portal auth — public registration/login flow
	"/settla.v1.PortalAuthService/Register":     true,
	"/settla.v1.PortalAuthService/Login":         true,
	"/settla.v1.PortalAuthService/VerifyEmail":   true,
	"/settla.v1.PortalAuthService/RefreshToken":  true,
	// Health checks
	"/grpc.health.v1.Health/Check": true,
	"/grpc.health.v1.Health/Watch": true,
	// gRPC reflection (dev only, should be disabled in production)
	"/grpc.reflection.v1alpha.ServerReflection/ServerReflectionInfo": true,
	"/grpc.reflection.v1.ServerReflection/ServerReflectionInfo":      true,
}

// opsProtectedMethods require the ops API key (x-ops-api-key header)
// instead of a tenant API key. These are admin-only operations.
var opsProtectedMethods = map[string]bool{
	"/settla.v1.PortalAuthService/ApproveKYB": true,
}

// APIKeyAuthInterceptor creates a gRPC unary interceptor that validates API keys
// on all methods except those in the unauthenticated allowlist.
//
// The interceptor extracts the API key from the "authorization" metadata header,
// hashes it, and validates against the database via the provided validator.
// On success, the tenant context is not propagated (the gateway handles that) —
// this interceptor only ensures that direct gRPC callers are authenticated.
func APIKeyAuthInterceptor(validator APIKeyValidator, hmacSecret []byte, logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		// Skip auth for allowlisted methods.
		if unauthenticatedMethods[info.FullMethod] {
			return handler(ctx, req)
		}

		// Ops-protected methods use separate auth (handled in method body).
		if opsProtectedMethods[info.FullMethod] {
			return handler(ctx, req)
		}

		// Extract API key from metadata.
		md, ok := grpcmd.FromIncomingContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing metadata")
		}

		authValues := md.Get("authorization")
		if len(authValues) == 0 {
			return nil, status.Error(codes.Unauthenticated, "missing authorization header")
		}

		token := strings.TrimPrefix(authValues[0], "Bearer ")
		token = strings.TrimPrefix(token, "bearer ")
		if token == "" || token == authValues[0] {
			return nil, status.Error(codes.Unauthenticated, "invalid authorization format, expected 'Bearer <key>'")
		}

		// Hash the key.
		var keyHash string
		if len(hmacSecret) > 0 {
			mac := hmac.New(sha256.New, hmacSecret)
			mac.Write([]byte(token))
			keyHash = hex.EncodeToString(mac.Sum(nil))
		} else {
			h := sha256.Sum256([]byte(token))
			keyHash = hex.EncodeToString(h[:])
		}

		// Validate against DB.
		result, err := validator.ValidateAPIKey(ctx, keyHash)
		if err != nil {
			logger.Warn("settla-grpc: auth interceptor: invalid API key",
				"method", info.FullMethod,
				"error", err,
			)
			return nil, status.Error(codes.Unauthenticated, "invalid API key")
		}

		// Reject suspended tenants.
		if result.Status != "ACTIVE" {
			return nil, status.Errorf(codes.PermissionDenied, "tenant is %s", result.Status)
		}

		// API key is valid — attach tenant info to metadata for downstream use.
		md = md.Copy()
		md.Set("x-tenant-id", result.TenantID)
		md.Set("x-tenant-slug", result.Slug)
		ctx = grpcmd.NewIncomingContext(ctx, md)

		return handler(ctx, req)
	}
}

// APIKeyAuthStreamInterceptor creates a gRPC stream interceptor for API key auth.
func APIKeyAuthStreamInterceptor(validator APIKeyValidator, hmacSecret []byte, logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		if unauthenticatedMethods[info.FullMethod] {
			return handler(srv, ss)
		}

		md, ok := grpcmd.FromIncomingContext(ss.Context())
		if !ok {
			return status.Error(codes.Unauthenticated, "missing metadata")
		}

		authValues := md.Get("authorization")
		if len(authValues) == 0 {
			return status.Error(codes.Unauthenticated, "missing authorization header")
		}

		token := strings.TrimPrefix(authValues[0], "Bearer ")
		token = strings.TrimPrefix(token, "bearer ")
		if token == "" || token == authValues[0] {
			return status.Error(codes.Unauthenticated, "invalid authorization format")
		}

		var keyHash string
		if len(hmacSecret) > 0 {
			mac := hmac.New(sha256.New, hmacSecret)
			mac.Write([]byte(token))
			keyHash = hex.EncodeToString(mac.Sum(nil))
		} else {
			h := sha256.Sum256([]byte(token))
			keyHash = hex.EncodeToString(h[:])
		}

		if _, err := validator.ValidateAPIKey(ss.Context(), keyHash); err != nil {
			return status.Error(codes.Unauthenticated, "invalid API key")
		}

		return handler(srv, ss)
	}
}
