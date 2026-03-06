package core

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// TransferStore is the core engine's port for persisting transfer aggregates.
// This is richer than domain.TransferStore — it includes event persistence,
// daily volume queries, and optimistic-lock-aware updates.
type TransferStore interface {
	CreateTransfer(ctx context.Context, transfer *domain.Transfer) error
	GetTransfer(ctx context.Context, tenantID, transferID uuid.UUID) (*domain.Transfer, error)
	GetTransferByIdempotencyKey(ctx context.Context, tenantID uuid.UUID, key string) (*domain.Transfer, error)
	UpdateTransfer(ctx context.Context, transfer *domain.Transfer) error
	CreateTransferEvent(ctx context.Context, event *domain.TransferEvent) error
	GetTransferEvents(ctx context.Context, tenantID, transferID uuid.UUID) ([]domain.TransferEvent, error)
	GetDailyVolume(ctx context.Context, tenantID uuid.UUID, date time.Time) (decimal.Decimal, error)
	CreateQuote(ctx context.Context, quote *domain.Quote) error
	GetQuote(ctx context.Context, tenantID, quoteID uuid.UUID) (*domain.Quote, error)
	ListTransfers(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]domain.Transfer, error)
}

// TenantStore is the core engine's port for reading tenant configuration.
type TenantStore interface {
	GetTenant(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error)
	GetTenantBySlug(ctx context.Context, slug string) (*domain.Tenant, error)
}

// Router selects optimal routes and provides FX quotes for settlement corridors.
type Router interface {
	GetQuote(ctx context.Context, tenantID uuid.UUID, req domain.QuoteRequest) (*domain.Quote, error)
}

// ProviderRegistry looks up on-ramp, off-ramp, and blockchain providers by ID.
type ProviderRegistry interface {
	GetOnRampProvider(id string) domain.OnRampProvider
	GetOffRampProvider(id string) domain.OffRampProvider
	GetBlockchainClient(chain string) domain.BlockchainClient
}

// CreateTransferRequest is the input for creating a new settlement transfer.
type CreateTransferRequest struct {
	ExternalRef    string
	IdempotencyKey string
	SourceCurrency domain.Currency
	SourceAmount   decimal.Decimal
	DestCurrency   domain.Currency
	Sender         domain.Sender
	Recipient      domain.Recipient
	QuoteID        *uuid.UUID
}
