package transferdb

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/core"
	"github.com/intellect4all/settla/domain"
)

// Compile-time interface checks.
var (
	_ core.TransferStore = (*TransferStoreAdapter)(nil)
	_ core.TenantStore   = (*TenantStoreAdapter)(nil)
)

// TransferStoreAdapter implements core.TransferStore using SQLC-generated queries
// against the Transfer DB.
type TransferStoreAdapter struct {
	q *Queries
}

// NewTransferStoreAdapter creates a new TransferStoreAdapter.
func NewTransferStoreAdapter(q *Queries) *TransferStoreAdapter {
	return &TransferStoreAdapter{q: q}
}

func (s *TransferStoreAdapter) CreateTransfer(ctx context.Context, transfer *domain.Transfer) error {
	feesJSON, err := json.Marshal(transfer.Fees)
	if err != nil {
		return fmt.Errorf("settla-core: marshalling fees: %w", err)
	}
	senderJSON, err := json.Marshal(transfer.Sender)
	if err != nil {
		return fmt.Errorf("settla-core: marshalling sender: %w", err)
	}
	recipientJSON, err := json.Marshal(transfer.Recipient)
	if err != nil {
		return fmt.Errorf("settla-core: marshalling recipient: %w", err)
	}

	row, err := s.q.CreateTransfer(ctx, CreateTransferParams{
		TenantID:          transfer.TenantID,
		ExternalRef:       textFromString(transfer.ExternalRef),
		IdempotencyKey:    textFromString(transfer.IdempotencyKey),
		Status:            string(transfer.Status),
		SourceCurrency:    string(transfer.SourceCurrency),
		SourceAmount:      numericFromDecimal(transfer.SourceAmount),
		DestCurrency:      string(transfer.DestCurrency),
		DestAmount:        numericFromDecimal(transfer.DestAmount),
		StableCoin:        textFromString(string(transfer.StableCoin)),
		StableAmount:      numericFromDecimal(transfer.StableAmount),
		Chain:             textFromString(transfer.Chain),
		FxRate:            numericFromDecimal(transfer.FXRate),
		Fees:              feesJSON,
		Sender:            senderJSON,
		Recipient:         recipientJSON,
		QuoteID:           uuidFromPtr(transfer.QuoteID),
		OnRampProviderID:  textFromString(transfer.OnRampProviderID),
		OffRampProviderID: textFromString(transfer.OffRampProviderID),
	})
	if err != nil {
		return fmt.Errorf("settla-core: creating transfer: %w", err)
	}

	transfer.ID = row.ID
	transfer.Version = row.Version
	transfer.CreatedAt = row.CreatedAt
	transfer.UpdatedAt = row.UpdatedAt
	return nil
}

func (s *TransferStoreAdapter) GetTransfer(ctx context.Context, tenantID, transferID uuid.UUID) (*domain.Transfer, error) {
	// When tenantID is uuid.Nil, look up by ID only (used by worker pipeline).
	if tenantID == uuid.Nil {
		row, err := s.q.GetTransferByID(ctx, transferID)
		if err != nil {
			return nil, fmt.Errorf("settla-core: getting transfer %s: %w", transferID, err)
		}
		return transferFromRow(row)
	}
	row, err := s.q.GetTransfer(ctx, GetTransferParams{
		ID:       transferID,
		TenantID: tenantID,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-core: getting transfer %s: %w", transferID, err)
	}
	return transferFromRow(row)
}

func (s *TransferStoreAdapter) GetTransferByIdempotencyKey(ctx context.Context, tenantID uuid.UUID, key string) (*domain.Transfer, error) {
	row, err := s.q.GetTransferByIdempotencyKey(ctx, GetTransferByIdempotencyKeyParams{
		TenantID:       tenantID,
		IdempotencyKey: textFromString(key),
	})
	if err != nil {
		return nil, fmt.Errorf("settla-core: getting transfer by idempotency key: %w", err)
	}
	return transferFromRow(row)
}

func (s *TransferStoreAdapter) UpdateTransfer(ctx context.Context, transfer *domain.Transfer) error {
	// Use optimistic lock: only update if version matches
	err := s.q.UpdateTransferStatusWithVersion(ctx, UpdateTransferStatusWithVersionParams{
		ID:       transfer.ID,
		TenantID: transfer.TenantID,
		Status:   string(transfer.Status),
		Version:  transfer.Version - 1, // TransitionTo already incremented Version
	})
	if err != nil {
		return fmt.Errorf("settla-core: updating transfer %s: %w", transfer.ID, err)
	}
	return nil
}

func (s *TransferStoreAdapter) CreateTransferEvent(ctx context.Context, event *domain.TransferEvent) error {
	metadataJSON, _ := json.Marshal(event.Metadata)
	_, err := s.q.CreateTransferEvent(ctx, CreateTransferEventParams{
		TransferID:  event.TransferID,
		TenantID:    event.TenantID,
		FromStatus:  textFromString(string(event.FromStatus)),
		ToStatus:    string(event.ToStatus),
		Metadata:    metadataJSON,
		ProviderRef: textFromString(event.ProviderRef),
	})
	if err != nil {
		return fmt.Errorf("settla-core: creating transfer event: %w", err)
	}
	return nil
}

func (s *TransferStoreAdapter) GetTransferEvents(ctx context.Context, tenantID, transferID uuid.UUID) ([]domain.TransferEvent, error) {
	rows, err := s.q.ListTransferEvents(ctx, ListTransferEventsParams{
		TenantID:   tenantID,
		TransferID: transferID,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-core: getting transfer events: %w", err)
	}
	events := make([]domain.TransferEvent, len(rows))
	for i, row := range rows {
		events[i] = domain.TransferEvent{
			ID:          row.ID,
			TransferID:  row.TransferID,
			TenantID:    row.TenantID,
			FromStatus:  domain.TransferStatus(row.FromStatus.String),
			ToStatus:    domain.TransferStatus(row.ToStatus),
			OccurredAt:  row.OccurredAt,
			ProviderRef: row.ProviderRef.String,
		}
		if row.Metadata != nil {
			_ = json.Unmarshal(row.Metadata, &events[i].Metadata)
		}
	}
	return events, nil
}

func (s *TransferStoreAdapter) GetDailyVolume(ctx context.Context, tenantID uuid.UUID, date time.Time) (decimal.Decimal, error) {
	startOfDay := date.Truncate(24 * time.Hour)
	endOfDay := startOfDay.Add(24 * time.Hour)

	result, err := s.q.SumDailyVolumeByTenant(ctx, SumDailyVolumeByTenantParams{
		TenantID:    tenantID,
		CreatedAt:   startOfDay,
		CreatedAt_2: endOfDay,
	})
	if err != nil {
		return decimal.Zero, fmt.Errorf("settla-core: getting daily volume: %w", err)
	}
	return decimalFromNumeric(result), nil
}

func (s *TransferStoreAdapter) CreateQuote(ctx context.Context, quote *domain.Quote) error {
	feesJSON, _ := json.Marshal(quote.Fees)
	routeJSON, _ := json.Marshal(quote.Route)

	row, err := s.q.CreateQuote(ctx, CreateQuoteParams{
		TenantID:       quote.TenantID,
		SourceCurrency: string(quote.SourceCurrency),
		SourceAmount:   numericFromDecimal(quote.SourceAmount),
		DestCurrency:   string(quote.DestCurrency),
		DestAmount:     numericFromDecimal(quote.DestAmount),
		StableAmount:   numericFromDecimal(quote.StableAmount),
		FxRate:         numericFromDecimal(quote.FXRate),
		Fees:           feesJSON,
		Route:          routeJSON,
		ExpiresAt:      quote.ExpiresAt,
	})
	if err != nil {
		return fmt.Errorf("settla-core: creating quote: %w", err)
	}
	quote.ID = row.ID
	quote.CreatedAt = row.CreatedAt
	return nil
}

func (s *TransferStoreAdapter) GetQuote(ctx context.Context, tenantID, quoteID uuid.UUID) (*domain.Quote, error) {
	row, err := s.q.GetQuote(ctx, GetQuoteParams{
		ID:       quoteID,
		TenantID: tenantID,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-core: getting quote %s: %w", quoteID, err)
	}
	return quoteFromRow(row)
}

func (s *TransferStoreAdapter) ListTransfers(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]domain.Transfer, error) {
	rows, err := s.q.ListTransfersByTenant(ctx, ListTransfersByTenantParams{
		TenantID: tenantID,
		Limit:    int32(limit),
		Offset:   int32(offset),
	})
	if err != nil {
		return nil, fmt.Errorf("settla-core: listing transfers for tenant %s: %w", tenantID, err)
	}
	transfers := make([]domain.Transfer, len(rows))
	for i, row := range rows {
		t, err := transferFromRow(row)
		if err != nil {
			return nil, err
		}
		transfers[i] = *t
	}
	return transfers, nil
}

// TenantStoreAdapter implements core.TenantStore using SQLC-generated queries.
type TenantStoreAdapter struct {
	q *Queries
}

// NewTenantStoreAdapter creates a new TenantStoreAdapter.
func NewTenantStoreAdapter(q *Queries) *TenantStoreAdapter {
	return &TenantStoreAdapter{q: q}
}

func (s *TenantStoreAdapter) GetTenant(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error) {
	row, err := s.q.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("settla-core: getting tenant %s: %w", tenantID, err)
	}
	return tenantFromRow(row)
}

func (s *TenantStoreAdapter) GetTenantBySlug(ctx context.Context, slug string) (*domain.Tenant, error) {
	row, err := s.q.GetTenantBySlug(ctx, slug)
	if err != nil {
		return nil, fmt.Errorf("settla-core: getting tenant by slug %s: %w", slug, err)
	}
	return tenantFromRow(row)
}

// --- Conversion helpers ---

func transferFromRow(row Transfer) (*domain.Transfer, error) {
	t := &domain.Transfer{
		ID:             row.ID,
		TenantID:       row.TenantID,
		ExternalRef:    row.ExternalRef.String,
		IdempotencyKey: row.IdempotencyKey.String,
		Status:         domain.TransferStatus(row.Status),
		Version:        row.Version,
		SourceCurrency: domain.Currency(row.SourceCurrency),
		SourceAmount:   decimalFromNumeric(row.SourceAmount),
		DestCurrency:   domain.Currency(row.DestCurrency),
		DestAmount:     decimalFromNumeric(row.DestAmount),
		StableCoin:     domain.Currency(row.StableCoin.String),
		StableAmount:   decimalFromNumeric(row.StableAmount),
		Chain:          row.Chain.String,
		FXRate:            decimalFromNumeric(row.FxRate),
		OnRampProviderID:  row.OnRampProviderID.String,
		OffRampProviderID: row.OffRampProviderID.String,
		CreatedAt:         row.CreatedAt,
		UpdatedAt:         row.UpdatedAt,
		FailureReason:     row.FailureReason.String,
		FailureCode:       row.FailureCode.String,
	}
	if row.QuoteID.Valid {
		id := uuid.UUID(row.QuoteID.Bytes)
		t.QuoteID = &id
	}
	if row.FundedAt.Valid {
		t.FundedAt = &row.FundedAt.Time
	}
	if row.CompletedAt.Valid {
		t.CompletedAt = &row.CompletedAt.Time
	}
	if row.FailedAt.Valid {
		t.FailedAt = &row.FailedAt.Time
	}
	if row.Fees != nil {
		_ = json.Unmarshal(row.Fees, &t.Fees)
	}
	if row.Sender != nil {
		_ = json.Unmarshal(row.Sender, &t.Sender)
	}
	if row.Recipient != nil {
		_ = json.Unmarshal(row.Recipient, &t.Recipient)
	}
	return t, nil
}

func tenantFromRow(row Tenant) (*domain.Tenant, error) {
	t := &domain.Tenant{
		ID:               row.ID,
		Name:             row.Name,
		Slug:             row.Slug,
		Status:           domain.TenantStatus(row.Status),
		SettlementModel:  domain.SettlementModel(row.SettlementModel),
		WebhookURL:       row.WebhookUrl.String,
		WebhookSecret:    row.WebhookSecret.String,
		DailyLimitUSD:    decimalFromNumeric(row.DailyLimitUsd),
		PerTransferLimit: decimalFromNumeric(row.PerTransferLimit),
		KYBStatus:        domain.KYBStatus(row.KybStatus),
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}
	if row.KybVerifiedAt.Valid {
		t.KYBVerifiedAt = &row.KybVerifiedAt.Time
	}
	if row.FeeSchedule != nil {
		_ = json.Unmarshal(row.FeeSchedule, &t.FeeSchedule)
	}
	if row.Metadata != nil {
		_ = json.Unmarshal(row.Metadata, &t.Metadata)
	}
	return t, nil
}

func quoteFromRow(row Quote) (*domain.Quote, error) {
	q := &domain.Quote{
		ID:             row.ID,
		TenantID:       row.TenantID,
		SourceCurrency: domain.Currency(row.SourceCurrency),
		SourceAmount:   decimalFromNumeric(row.SourceAmount),
		DestCurrency:   domain.Currency(row.DestCurrency),
		DestAmount:     decimalFromNumeric(row.DestAmount),
		StableAmount:   decimalFromNumeric(row.StableAmount),
		FXRate:         decimalFromNumeric(row.FxRate),
		ExpiresAt:      row.ExpiresAt,
		CreatedAt:      row.CreatedAt,
	}
	if row.Fees != nil {
		_ = json.Unmarshal(row.Fees, &q.Fees)
	}
	if row.Route != nil {
		_ = json.Unmarshal(row.Route, &q.Route)
	}
	return q, nil
}

func textFromString(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

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

func uuidFromPtr(id *uuid.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: *id, Valid: true}
}
