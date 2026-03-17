package transferdb

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	pgx "github.com/jackc/pgx/v5"

	"github.com/intellect4all/settla/domain"
)

// PaymentLinkStoreAdapter implements the payment link store interface using SQLC-generated queries.
type PaymentLinkStoreAdapter struct {
	q    *Queries
	pool TxBeginner
}

// NewPaymentLinkStoreAdapter creates a new PaymentLinkStoreAdapter.
func NewPaymentLinkStoreAdapter(q *Queries, pool TxBeginner) *PaymentLinkStoreAdapter {
	return &PaymentLinkStoreAdapter{q: q, pool: pool}
}

// Create persists a new payment link.
func (s *PaymentLinkStoreAdapter) Create(ctx context.Context, link *domain.PaymentLink) error {
	configJSON, err := json.Marshal(link.SessionConfig)
	if err != nil {
		return fmt.Errorf("settla-payment-link-store: marshalling session config: %w", err)
	}

	var useLimit pgtype.Int4
	if link.UseLimit != nil {
		useLimit = pgtype.Int4{Int32: int32(*link.UseLimit), Valid: true}
	}

	var expiresAt pgtype.Timestamptz
	if link.ExpiresAt != nil {
		expiresAt = pgtype.Timestamptz{Time: *link.ExpiresAt, Valid: true}
	}

	row, err := s.q.CreatePaymentLink(ctx, CreatePaymentLinkParams{
		TenantID:      link.TenantID,
		ShortCode:     link.ShortCode,
		Description:   link.Description,
		SessionConfig: configJSON,
		UseLimit:      useLimit,
		ExpiresAt:     expiresAt,
		RedirectUrl:   link.RedirectURL,
		Status:        string(link.Status),
	})
	if err != nil {
		return fmt.Errorf("settla-payment-link-store: creating link: %w", err)
	}

	link.ID = row.ID
	link.CreatedAt = row.CreatedAt
	link.UpdatedAt = row.UpdatedAt
	link.UseCount = int(row.UseCount)
	return nil
}

// GetByID retrieves a payment link by tenant and ID.
func (s *PaymentLinkStoreAdapter) GetByID(ctx context.Context, tenantID, linkID uuid.UUID) (*domain.PaymentLink, error) {
	row, err := s.q.GetPaymentLinkByID(ctx, GetPaymentLinkByIDParams{
		ID:       linkID,
		TenantID: tenantID,
	})
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("settla-payment-link-store: getting link %s: %w", linkID, err)
	}
	return paymentLinkFromRow(row)
}

// GetByShortCode retrieves a payment link by short code (public, no tenant filter).
func (s *PaymentLinkStoreAdapter) GetByShortCode(ctx context.Context, shortCode string) (*domain.PaymentLink, error) {
	row, err := s.q.GetPaymentLinkByShortCode(ctx, shortCode)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("settla-payment-link-store: getting link by code %s: %w", shortCode, err)
	}
	return paymentLinkFromRow(row)
}

// List retrieves payment links for a tenant with pagination.
func (s *PaymentLinkStoreAdapter) List(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]domain.PaymentLink, int64, error) {
	rows, err := s.q.ListPaymentLinksByTenant(ctx, ListPaymentLinksByTenantParams{
		TenantID: tenantID,
		Limit:    int32(limit),
		Offset:   int32(offset),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("settla-payment-link-store: listing links: %w", err)
	}

	total, err := s.q.CountPaymentLinksByTenant(ctx, tenantID)
	if err != nil {
		return nil, 0, fmt.Errorf("settla-payment-link-store: counting links: %w", err)
	}

	links := make([]domain.PaymentLink, len(rows))
	for i, row := range rows {
		link, err := paymentLinkFromRow(row)
		if err != nil {
			return nil, 0, err
		}
		links[i] = *link
	}
	return links, total, nil
}

// IncrementUseCount increments the use_count of a payment link.
func (s *PaymentLinkStoreAdapter) IncrementUseCount(ctx context.Context, linkID uuid.UUID) error {
	return s.q.IncrementPaymentLinkUseCount(ctx, linkID)
}

// UpdateStatus sets the status of a payment link.
func (s *PaymentLinkStoreAdapter) UpdateStatus(ctx context.Context, tenantID, linkID uuid.UUID, status domain.PaymentLinkStatus) error {
	return s.q.UpdatePaymentLinkStatus(ctx, UpdatePaymentLinkStatusParams{
		ID:       linkID,
		TenantID: tenantID,
		Status:   string(status),
	})
}

// ── Row conversion helper ────────────────────────────────────────────────────

func paymentLinkFromRow(row PaymentLink) (*domain.PaymentLink, error) {
	link := &domain.PaymentLink{
		ID:          row.ID,
		TenantID:    row.TenantID,
		ShortCode:   row.ShortCode,
		Description: row.Description,
		RedirectURL: row.RedirectUrl,
		Status:      domain.PaymentLinkStatus(row.Status),
		UseCount:    int(row.UseCount),
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}

	if row.UseLimit.Valid {
		v := int(row.UseLimit.Int32)
		link.UseLimit = &v
	}
	if row.ExpiresAt.Valid {
		t := row.ExpiresAt.Time
		link.ExpiresAt = &t
	}

	if err := json.Unmarshal(row.SessionConfig, &link.SessionConfig); err != nil {
		return nil, fmt.Errorf("settla-payment-link-store: unmarshalling session config for link %s: %w", row.ID, err)
	}

	return link, nil
}
