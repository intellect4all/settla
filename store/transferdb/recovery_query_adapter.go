package transferdb

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/intellect4all/settla/domain"
)

// RecoveryQueryAdapter implements core/recovery.TransferQueryStore using raw SQL
// against the transfer DB. It queries transfers stuck in non-terminal states.
type RecoveryQueryAdapter struct {
	pool *pgxpool.Pool
}

// NewRecoveryQueryAdapter creates a recovery query adapter backed by the transfer DB.
func NewRecoveryQueryAdapter(pool *pgxpool.Pool) *RecoveryQueryAdapter {
	return &RecoveryQueryAdapter{pool: pool}
}

// ListStuckTransfers returns transfers whose status matches the given non-terminal
// state and whose updated_at is older than olderThan. Used by the recovery
// detector to identify transfers that need automated recovery or escalation.
//
// NOTE: This is an intentional admin-only system process that scans across all
// tenants. It is NOT exposed via any tenant-facing API. Each returned row
// includes tenant_id so downstream recovery logic remains tenant-aware.
func (a *RecoveryQueryAdapter) ListStuckTransfers(ctx context.Context, status domain.TransferStatus, olderThan time.Time) ([]*domain.Transfer, error) {
	rows, err := a.pool.Query(ctx,
		`SELECT id, tenant_id, status, updated_at, created_at
		 FROM transfers
		 WHERE status = $1
		   AND updated_at < $2
		   AND status NOT IN ('COMPLETED', 'FAILED')
		 ORDER BY updated_at ASC
		 LIMIT 1000`,
		string(status), olderThan,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var transfers []*domain.Transfer
	for rows.Next() {
		t := &domain.Transfer{}
		if err := rows.Scan(&t.ID, &t.TenantID, &t.Status, &t.UpdatedAt, &t.CreatedAt); err != nil {
			return nil, err
		}
		transfers = append(transfers, t)
	}
	return transfers, rows.Err()
}
