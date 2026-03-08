package main

import (
	"context"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/intellect4all/settla/domain"
)

// stubPublisher drops all events when NATS is unavailable (development mode).
type stubPublisher struct{}

func (s *stubPublisher) Publish(ctx context.Context, event domain.Event) error {
	return nil
}

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
