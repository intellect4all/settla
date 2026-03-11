package ledgerdb

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	grpcserver "github.com/intellect4all/settla/api/grpc"
)

// AccountAdapter provides account listing from the Ledger DB.
type AccountAdapter struct {
	pool *pgxpool.Pool
	q    *Queries
}

// NewAccountAdapter creates a new AccountAdapter.
func NewAccountAdapter(pool *pgxpool.Pool, q *Queries) *AccountAdapter {
	return &AccountAdapter{pool: pool, q: q}
}

// Compile-time interface check.
var _ grpcserver.AccountStore = (*AccountAdapter)(nil)

// ListAccountsByTenant returns all accounts belonging to a tenant.
func (a *AccountAdapter) ListAccountsByTenant(ctx context.Context, tenantID uuid.UUID) ([]grpcserver.AccountInfo, error) {
	pgTenantID := pgtype.UUID{Bytes: tenantID, Valid: true}
	rows, err := a.q.ListAccountsByTenant(ctx, pgTenantID)
	if err != nil {
		return nil, fmt.Errorf("settla-ledger-store: listing accounts for tenant %s: %w", tenantID, err)
	}

	accounts := make([]grpcserver.AccountInfo, len(rows))
	for i, row := range rows {
		accounts[i] = grpcserver.AccountInfo{
			ID:       row.ID,
			Code:     row.Code,
			Name:     row.Name,
			Type:     row.Type,
			Currency: row.Currency,
			IsActive: row.IsActive,
		}
		if row.TenantID.Valid {
			tid := uuid.UUID(row.TenantID.Bytes)
			accounts[i].TenantID = &tid
		}
	}
	return accounts, nil
}
