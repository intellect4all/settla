package grpc

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/intellect4all/settla/domain"
)

// mustJSON marshals v to JSON, returning nil on error.
// Used for fire-and-forget audit logging where marshalling failure is non-critical.
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// extractActorFromRequest returns an actor identifier from the HTTP request.
// It checks the X-Ops-Api-Key header (ops), X-Actor-ID header, or falls back
// to the remote address.
func extractActorFromRequest(r *http.Request) string {
	if actor := r.Header.Get("X-Actor-ID"); actor != "" {
		return actor
	}
	if r.Header.Get("X-Ops-Api-Key") != "" {
		return "ops:" + r.RemoteAddr
	}
	return "http:" + r.RemoteAddr
}

// auditLog is a fire-and-forget helper that logs an audit entry asynchronously.
// It silently drops errors — audit logging must never block the request path.
func auditLog(ctx context.Context, logger domain.AuditLogger, slogger *slog.Logger, entry domain.AuditEntry) {
	if logger == nil {
		return
	}
	go func() {
		if err := logger.Log(context.Background(), entry); err != nil {
			if slogger != nil {
				slogger.Warn("settla-audit: failed to write audit entry",
					"action", entry.Action,
					"entity_type", entry.EntityType,
					"error", err,
				)
			}
		}
	}()
}

// uuidPtr returns a pointer to the given UUID. Convenience helper for audit entries.
func uuidPtr(id uuid.UUID) *uuid.UUID {
	return &id
}
