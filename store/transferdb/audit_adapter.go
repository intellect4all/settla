package transferdb

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/intellect4all/settla/domain"
)

// Compile-time interface check.
var _ domain.AuditLogger = (*AuditAdapter)(nil)

// AuditAdapter implements domain.AuditLogger using SQLC-generated queries
// against the Transfer DB audit_log table.
//
// SECURITY NOTE: The OldValue and NewValue fields may contain sensitive data
// (recipient bank details, blockchain addresses, fee amounts). Production
// deployments should enable at-rest encryption (TDE or AWS RDS encryption)
// on the audit_log table and restrict access to authorized personnel only.
type AuditAdapter struct {
	pool *pgxpool.Pool
	q    *Queries
}

// NewAuditAdapter creates a new AuditAdapter.
func NewAuditAdapter(pool *pgxpool.Pool) *AuditAdapter {
	return &AuditAdapter{pool: pool, q: New(pool)}
}

// Log inserts a single audit entry.
func (a *AuditAdapter) Log(ctx context.Context, entry domain.AuditEntry) error {
	return a.q.InsertAuditEntry(ctx, InsertAuditEntryParams{
		TenantID:   entry.TenantID,
		ActorType:  entry.ActorType,
		ActorID:    entry.ActorID,
		Action:     entry.Action,
		EntityType: entry.EntityType,
		EntityID:   uuidFromPtr(entry.EntityID),
		OldValue:   entry.OldValue,
		NewValue:   entry.NewValue,
		Metadata:   entry.Metadata,
	})
}

// List returns audit entries for a tenant, ordered by created_at DESC.
func (a *AuditAdapter) List(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]domain.AuditEntry, error) {
	rows, err := a.q.ListAuditEntriesByTenant(ctx, ListAuditEntriesByTenantParams{
		TenantID: tenantID,
		Limit:    int32(limit),
		Offset:   int32(offset),
	})
	if err != nil {
		return nil, fmt.Errorf("settla-audit: listing entries for tenant %s: %w", tenantID, err)
	}
	return auditRowsToDomain(rows), nil
}

// ListByEntity returns audit entries for a specific entity, ordered by created_at DESC.
func (a *AuditAdapter) ListByEntity(ctx context.Context, entityType string, entityID uuid.UUID, limit int) ([]domain.AuditEntry, error) {
	rows, err := a.q.ListAuditEntriesByEntity(ctx, ListAuditEntriesByEntityParams{
		EntityType: entityType,
		EntityID:   pgtype.UUID{Bytes: entityID, Valid: true},
		Limit:      int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("settla-audit: listing entries for %s/%s: %w", entityType, entityID, err)
	}
	return auditRowsToDomain(rows), nil
}

// auditRowsToDomain converts SQLC-generated AuditLog rows to domain.AuditEntry slices.
func auditRowsToDomain(rows []AuditLog) []domain.AuditEntry {
	entries := make([]domain.AuditEntry, len(rows))
	for i, r := range rows {
		entries[i] = domain.AuditEntry{
			ID:         r.ID,
			TenantID:   r.TenantID,
			ActorType:  r.ActorType,
			ActorID:    r.ActorID,
			Action:     r.Action,
			EntityType: r.EntityType,
			OldValue:   r.OldValue,
			NewValue:   r.NewValue,
			Metadata:   r.Metadata,
			CreatedAt:  r.CreatedAt,
		}
		if r.EntityID.Valid {
			id := uuid.UUID(r.EntityID.Bytes)
			entries[i].EntityID = &id
		}
	}
	return entries
}
