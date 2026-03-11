package transferdb

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ReviewStoreAdapter implements the BlockchainReviewStore interface (node/worker)
// and the ReviewStore interface (core/recovery) using transferdb SQLC queries.
//
// Both interfaces require identical methods, so a single adapter satisfies both.
type ReviewStoreAdapter struct {
	q    *Queries
	pool *pgxpool.Pool
}

// NewReviewStoreAdapter creates a review store adapter backed by the transfer DB.
func NewReviewStoreAdapter(q *Queries, pool *pgxpool.Pool) *ReviewStoreAdapter {
	return &ReviewStoreAdapter{q: q, pool: pool}
}

// CreateManualReview inserts a new manual review record for a stuck transfer.
// If a review already exists for the same transfer+status, the insert is a no-op
// (ON CONFLICT DO NOTHING) to preserve idempotency.
func (a *ReviewStoreAdapter) CreateManualReview(ctx context.Context, transferID, tenantID uuid.UUID, transferStatus string, stuckSince time.Time) error {
	_, err := a.q.CreateManualReview(ctx, CreateManualReviewParams{
		TransferID:          transferID,
		TenantID:            tenantID,
		Status:              "pending",
		TransferStatus:      transferStatus,
		StuckSince:          stuckSince,
		AttemptedRecoveries: 0,
	})
	// Ignore unique violation — review already exists, which is the desired outcome.
	if err != nil && isDuplicateKeyError(err) {
		return nil
	}
	return err
}

// HasActiveReview checks whether an open (pending or investigating) review
// exists for the given transfer.
func (a *ReviewStoreAdapter) HasActiveReview(ctx context.Context, transferID uuid.UUID) (bool, error) {
	var exists bool
	err := a.pool.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM manual_reviews
			WHERE transfer_id = $1
			  AND status IN ('pending', 'investigating')
		)`, transferID,
	).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

// isDuplicateKeyError reports whether err is a PostgreSQL unique constraint violation.
func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	return contains(err.Error(), "duplicate key") || contains(err.Error(), "unique constraint")
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && findStr(s, sub)
}

func findStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
