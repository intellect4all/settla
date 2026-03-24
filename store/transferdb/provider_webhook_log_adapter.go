package transferdb

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ProviderWebhookLogStore persists inbound provider webhooks for deduplication,
// debugging, and audit. Used by InboundWebhookWorker.
type ProviderWebhookLogStore interface {
	// InsertRaw stores the raw webhook payload before normalization.
	// Returns the log ID and created_at, or empty if deduplicated (ON CONFLICT DO NOTHING).
	InsertRaw(ctx context.Context, slug, idempotencyKey string, rawBody []byte, headers map[string]string, sourceIP string) (id uuid.UUID, createdAt time.Time, isDuplicate bool, err error)

	// CheckDuplicate returns true if a webhook with this (slug, key) has already been processed.
	CheckDuplicate(ctx context.Context, slug, idempotencyKey string) (bool, error)

	// MarkProcessed updates the log entry with the normalization result.
	MarkProcessed(ctx context.Context, id uuid.UUID, createdAt time.Time, transferID, tenantID *uuid.UUID, normalized []byte, status, errMsg string) error
}

// ProviderWebhookLogAdapter implements ProviderWebhookLogStore using SQLC-generated queries.
type ProviderWebhookLogAdapter struct {
	q    *Queries
	pool *pgxpool.Pool
}

// NewProviderWebhookLogAdapter creates a new adapter.
func NewProviderWebhookLogAdapter(pool *pgxpool.Pool) *ProviderWebhookLogAdapter {
	return &ProviderWebhookLogAdapter{
		q:    New(pool),
		pool: pool,
	}
}

func (a *ProviderWebhookLogAdapter) InsertRaw(ctx context.Context, slug, idempotencyKey string, rawBody []byte, headers map[string]string, sourceIP string) (uuid.UUID, time.Time, bool, error) {
	headersJSON, err := json.Marshal(headers)
	if err != nil {
		headersJSON = []byte("{}")
	}

	row, err := a.q.InsertProviderWebhookLog(ctx, InsertProviderWebhookLogParams{
		ProviderSlug:   slug,
		IdempotencyKey: idempotencyKey,
		RawBody:        rawBody,
		HttpHeaders:    headersJSON,
		SourceIp:       pgtype.Text{String: sourceIP, Valid: sourceIP != ""},
	})
	if err != nil {
		// ON CONFLICT DO NOTHING returns no rows — pgx returns ErrNoRows
		if err.Error() == "no rows in result set" {
			return uuid.UUID{}, time.Time{}, true, nil
		}
		return uuid.UUID{}, time.Time{}, false, fmt.Errorf("inserting webhook log: %w", err)
	}

	return row.ID, row.CreatedAt, false, nil
}

func (a *ProviderWebhookLogAdapter) CheckDuplicate(ctx context.Context, slug, idempotencyKey string) (bool, error) {
	_, err := a.q.CheckWebhookDuplicate(ctx, CheckWebhookDuplicateParams{
		ProviderSlug:   slug,
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		if err.Error() == "no rows in result set" {
			return false, nil
		}
		return false, fmt.Errorf("checking webhook duplicate: %w", err)
	}
	return true, nil
}

func (a *ProviderWebhookLogAdapter) MarkProcessed(ctx context.Context, id uuid.UUID, createdAt time.Time, transferID, tenantID *uuid.UUID, normalized []byte, status, errMsg string) error {
	params := UpdateWebhookLogProcessedParams{
		ID:         id,
		CreatedAt:  createdAt,
		Normalized: normalized,
		Status:     status,
	}
	if transferID != nil {
		params.TransferID = pgtype.UUID{Bytes: *transferID, Valid: true}
	}
	if tenantID != nil {
		params.TenantID = pgtype.UUID{Bytes: *tenantID, Valid: true}
	}
	if errMsg != "" {
		params.ErrorMessage = pgtype.Text{String: errMsg, Valid: true}
	}

	return a.q.UpdateWebhookLogProcessed(ctx, params)
}
