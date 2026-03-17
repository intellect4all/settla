package transferdb

import (
	"context"
	"time"

	"github.com/intellect4all/settla/domain"
)

// RecoveryQueryAdapter implements core/recovery.TransferQueryStore using SQLC
// generated queries against the transfer DB. It queries transfers stuck in
// non-terminal states.
type RecoveryQueryAdapter struct {
	queries    *Queries
	maxResults int32
}

// NewRecoveryQueryAdapter creates a recovery query adapter backed by the transfer DB.
// maxResults controls the LIMIT on stuck transfer queries; if <= 0, defaults to 5000.
func NewRecoveryQueryAdapter(queries *Queries, maxResults int) *RecoveryQueryAdapter {
	if maxResults <= 0 {
		maxResults = 5000
	}
	return &RecoveryQueryAdapter{queries: queries, maxResults: int32(maxResults)}
}

// ListStuckTransfers returns transfers whose status matches the given non-terminal
// state and whose updated_at is older than olderThan. Used by the recovery
// detector to identify transfers that need automated recovery or escalation.
//
// NOTE: This is an intentional admin-only system process that scans across all
// tenants. It is NOT exposed via any tenant-facing API. Each returned row
// includes tenant_id so downstream recovery logic remains tenant-aware.
func (a *RecoveryQueryAdapter) ListStuckTransfers(ctx context.Context, status domain.TransferStatus, olderThan time.Time) ([]*domain.Transfer, error) {
	rows, err := a.queries.ListStuckTransfers(ctx, ListStuckTransfersParams{
		Status:    TransferStatusEnum(status),
		UpdatedAt: olderThan,
		Limit:     a.maxResults,
	})
	if err != nil {
		return nil, err
	}

	transfers := make([]*domain.Transfer, len(rows))
	for i, r := range rows {
		transfers[i] = &domain.Transfer{
			ID:        r.ID,
			TenantID:  r.TenantID,
			Status:    domain.TransferStatus(r.Status),
			UpdatedAt: r.UpdatedAt,
			CreatedAt: r.CreatedAt,
		}
	}
	return transfers, nil
}
