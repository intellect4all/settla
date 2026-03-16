package domain

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// AdminCaller identifies who is making a cross-tenant admin query and why.
type AdminCaller struct {
	Service string // "settlement_scheduler", "ops_api", "reconciliation"
	Reason  string // "scheduled_run", "manual_review_list", etc.
}

// AuditEntry represents a single audit log record.
type AuditEntry struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	ActorType  string // "user", "system", "api_key"
	ActorID    string
	Action     string
	EntityType string
	EntityID   *uuid.UUID
	OldValue   json.RawMessage
	NewValue   json.RawMessage
	Metadata   json.RawMessage
	CreatedAt  time.Time
}

// AuditLogger logs and queries audit entries.
type AuditLogger interface {
	Log(ctx context.Context, entry AuditEntry) error
	List(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]AuditEntry, error)
	ListByEntity(ctx context.Context, entityType string, entityID uuid.UUID, limit int) ([]AuditEntry, error)
}
