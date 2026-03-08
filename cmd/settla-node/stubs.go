package main

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/rail/provider"
	"github.com/intellect4all/settla/store/treasurydb"
)

// stubTreasuryStore returns empty data when treasury DB is unavailable.
type stubTreasuryStore struct{}

func (s *stubTreasuryStore) LoadAllPositions(ctx context.Context) ([]domain.Position, error) {
	return nil, nil
}

func (s *stubTreasuryStore) UpdatePosition(ctx context.Context, id uuid.UUID, balance, locked decimal.Decimal) error {
	return nil
}

func (s *stubTreasuryStore) RecordHistory(ctx context.Context, positionID, tenantID uuid.UUID, balance, locked decimal.Decimal, triggerType string) error {
	return nil
}

// coreRegistryAdapter wraps provider.Registry to satisfy core.ProviderRegistry.
type coreRegistryAdapter struct {
	reg *provider.Registry
}

func (a *coreRegistryAdapter) GetOnRampProvider(id string) domain.OnRampProvider {
	p, err := a.reg.GetOnRamp(id)
	if err != nil {
		return nil
	}
	return p
}

func (a *coreRegistryAdapter) GetOffRampProvider(id string) domain.OffRampProvider {
	p, err := a.reg.GetOffRamp(id)
	if err != nil {
		return nil
	}
	return p
}

func (a *coreRegistryAdapter) GetBlockchainClient(chain string) domain.BlockchainClient {
	c, err := a.reg.GetBlockchainClient(chain)
	if err != nil {
		return nil
	}
	return c
}

// ── Postgres Treasury Store ─────────────────────────────────────────────────

type postgresTreasuryStore struct {
	q *treasurydb.Queries
}

func newPostgresTreasuryStore(q *treasurydb.Queries) *postgresTreasuryStore {
	return &postgresTreasuryStore{q: q}
}

func (s *postgresTreasuryStore) LoadAllPositions(ctx context.Context) ([]domain.Position, error) {
	rows, err := s.q.ListAllPositions(ctx)
	if err != nil {
		return nil, fmt.Errorf("settla-treasury: loading positions: %w", err)
	}
	positions := make([]domain.Position, len(rows))
	for i, row := range rows {
		positions[i] = domain.Position{
			ID:            row.ID,
			TenantID:      row.TenantID,
			Currency:      domain.Currency(row.Currency),
			Location:      row.Location,
			Balance:       decimalFromNumeric(row.Balance),
			Locked:        decimalFromNumeric(row.Locked),
			MinBalance:    decimalFromNumeric(row.MinBalance),
			TargetBalance: decimalFromNumeric(row.TargetBalance),
			UpdatedAt:     row.UpdatedAt,
		}
	}
	return positions, nil
}

func (s *postgresTreasuryStore) UpdatePosition(ctx context.Context, id uuid.UUID, balance, locked decimal.Decimal) error {
	return s.q.UpdatePositionBalances(ctx, treasurydb.UpdatePositionBalancesParams{
		ID:      id,
		Balance: numericFromDecimal(balance),
		Locked:  numericFromDecimal(locked),
	})
}

func (s *postgresTreasuryStore) RecordHistory(ctx context.Context, positionID, tenantID uuid.UUID, balance, locked decimal.Decimal, triggerType string) error {
	_, err := s.q.CreatePositionHistory(ctx, treasurydb.CreatePositionHistoryParams{
		PositionID:  positionID,
		TenantID:    tenantID,
		Balance:     numericFromDecimal(balance),
		Locked:      numericFromDecimal(locked),
		TriggerType: pgtype.Text{String: triggerType, Valid: true},
		TriggerRef:  pgtype.UUID{},
	})
	return err
}

// ── Conversion helpers ──────────────────────────────────────────────────────

func numericFromDecimal(d decimal.Decimal) pgtype.Numeric {
	n := pgtype.Numeric{}
	_ = n.Scan(d.String())
	return n
}

func decimalFromNumeric(n pgtype.Numeric) decimal.Decimal {
	if !n.Valid || n.Int == nil {
		return decimal.Zero
	}
	return decimal.NewFromBigInt(n.Int, n.Exp)
}
