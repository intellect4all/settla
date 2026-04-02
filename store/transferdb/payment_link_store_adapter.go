package transferdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	pgx "github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/store/rls"
)

// PaymentLinkStoreAdapter implements the payment link store interface using SQLC-generated queries.
type PaymentLinkStoreAdapter struct {
	q          *Queries
	pool       TxBeginner
	appPool    *pgxpool.Pool // optional: RLS-enforced pool
	rlsEnabled bool          // true when appPool is configured; false means RLS is bypassed
}

// NewPaymentLinkStoreAdapter creates a new PaymentLinkStoreAdapter.
func NewPaymentLinkStoreAdapter(q *Queries, pool TxBeginner) *PaymentLinkStoreAdapter {
	a := &PaymentLinkStoreAdapter{q: q, pool: pool}
	a.rlsEnabled = (a.appPool != nil)
	if !a.rlsEnabled {
		slog.Warn("settla-store: PaymentLinkStoreAdapter RLS pool not configured, tenant isolation relies on application-layer filters only")
	}
	return a
}

// WithPaymentLinkAppPool configures the RLS-enforced pool for tenant-scoped operations.
func (s *PaymentLinkStoreAdapter) WithPaymentLinkAppPool(pool *pgxpool.Pool) *PaymentLinkStoreAdapter {
	s.appPool = pool
	s.rlsEnabled = (pool != nil)
	return s
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

	params := CreatePaymentLinkParams{
		TenantID:      link.TenantID,
		ShortCode:     link.ShortCode,
		Description:   link.Description,
		SessionConfig: configJSON,
		UseLimit:      useLimit,
		ExpiresAt:     expiresAt,
		RedirectUrl:   link.RedirectURL,
		Status:        string(link.Status),
	}

	createFn := func(q *Queries) error {
		row, err := q.CreatePaymentLink(ctx, params)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "payment_links_short_code_key" {
				return domain.ErrShortCodeCollision()
			}
			return fmt.Errorf("settla-payment-link-store: creating link: %w", err)
		}
		link.ID = row.ID
		link.CreatedAt = row.CreatedAt
		link.UpdatedAt = row.UpdatedAt
		link.UseCount = int(row.UseCount)
		return nil
	}

	if s.appPool != nil {
		return rls.WithTenantTx(ctx, s.appPool, link.TenantID, func(tx pgx.Tx) error {
			return createFn(s.q.WithTx(tx))
		})
	}

	slog.Warn("settla-store: RLS bypassed", "method", "Create", "tenant_id", link.TenantID)
	return createFn(s.q)
}

// GetByID retrieves a payment link by tenant and ID.
func (s *PaymentLinkStoreAdapter) GetByID(ctx context.Context, tenantID, linkID uuid.UUID) (*domain.PaymentLink, error) {
	if s.appPool != nil {
		var result *domain.PaymentLink
		err := rls.WithTenantReadTx(ctx, s.appPool, tenantID, func(tx pgx.Tx) error {
			row, err := s.q.WithTx(tx).GetPaymentLinkByID(ctx, GetPaymentLinkByIDParams{
				ID:       linkID,
				TenantID: tenantID,
			})
			if err != nil {
				if err == pgx.ErrNoRows {
					return nil
				}
				return err
			}
			result, err = paymentLinkFromRow(row)
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("settla-payment-link-store: getting link %s: %w", linkID, err)
		}
		return result, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "GetByID", "tenant_id", tenantID)
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
	if s.appPool != nil {
		var links []domain.PaymentLink
		var total int64
		err := rls.WithTenantReadTx(ctx, s.appPool, tenantID, func(tx pgx.Tx) error {
			qtx := s.q.WithTx(tx)
			rows, err := qtx.ListPaymentLinksByTenant(ctx, ListPaymentLinksByTenantParams{
				TenantID: tenantID,
				Limit:    int32(limit),
				Offset:   int32(offset),
			})
			if err != nil {
				return err
			}
			total, err = qtx.CountPaymentLinksByTenant(ctx, tenantID)
			if err != nil {
				return err
			}
			links = make([]domain.PaymentLink, len(rows))
			for i, row := range rows {
				link, err := paymentLinkFromRow(row)
				if err != nil {
					return err
				}
				links[i] = *link
			}
			return nil
		})
		if err != nil {
			return nil, 0, fmt.Errorf("settla-payment-link-store: listing links: %w", err)
		}
		return links, total, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "List", "tenant_id", tenantID)
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

// ListCursor retrieves payment links for a tenant using cursor-based pagination.
// Returns links with created_at < cursor, ordered by created_at DESC.
func (s *PaymentLinkStoreAdapter) ListCursor(ctx context.Context, tenantID uuid.UUID, pageSize int, cursor time.Time) ([]domain.PaymentLink, error) {
	const query = `SELECT id, tenant_id, short_code, description, session_config, use_limit, use_count, expires_at, redirect_url, status, created_at, updated_at
FROM payment_links WHERE tenant_id = $1 AND created_at < $2 ORDER BY created_at DESC LIMIT $3`

	scanRows := func(db DBTX) ([]domain.PaymentLink, error) {
		rows, err := db.Query(ctx, query, tenantID, cursor, pageSize)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var links []domain.PaymentLink
		for rows.Next() {
			var row PaymentLink
			if err := rows.Scan(
				&row.ID, &row.TenantID, &row.ShortCode, &row.Description,
				&row.SessionConfig, &row.UseLimit, &row.UseCount, &row.ExpiresAt,
				&row.RedirectUrl, &row.Status, &row.CreatedAt, &row.UpdatedAt,
			); err != nil {
				return nil, err
			}
			link, err := paymentLinkFromRow(row)
			if err != nil {
				return nil, err
			}
			links = append(links, *link)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return links, nil
	}

	if s.appPool != nil {
		var links []domain.PaymentLink
		err := rls.WithTenantReadTx(ctx, s.appPool, tenantID, func(tx pgx.Tx) error {
			var txErr error
			links, txErr = scanRows(tx)
			return txErr
		})
		if err != nil {
			return nil, fmt.Errorf("settla-payment-link-store: list cursor: %w", err)
		}
		return links, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "ListCursor", "tenant_id", tenantID)
	links, err := scanRows(s.q.db)
	if err != nil {
		return nil, fmt.Errorf("settla-payment-link-store: list cursor: %w", err)
	}
	return links, nil
}

// IncrementUseCount increments the use_count of a payment link.
func (s *PaymentLinkStoreAdapter) IncrementUseCount(ctx context.Context, linkID uuid.UUID) error {
	return s.q.IncrementPaymentLinkUseCount(ctx, linkID)
}

// UpdateStatus sets the status of a payment link.
func (s *PaymentLinkStoreAdapter) UpdateStatus(ctx context.Context, tenantID, linkID uuid.UUID, status domain.PaymentLinkStatus) error {
	params := UpdatePaymentLinkStatusParams{
		ID:       linkID,
		TenantID: tenantID,
		Status:   string(status),
	}

	if s.appPool != nil {
		return rls.WithTenantTx(ctx, s.appPool, tenantID, func(tx pgx.Tx) error {
			return s.q.WithTx(tx).UpdatePaymentLinkStatus(ctx, params)
		})
	}

	slog.Warn("settla-store: RLS bypassed", "method", "UpdateStatus", "tenant_id", tenantID)
	return s.q.UpdatePaymentLinkStatus(ctx, params)
}


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
