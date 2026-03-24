package transferdb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/intellect4all/settla/domain"
)

// PortalAuthStoreAdapter implements the PortalAuthStore interface for portal authentication.
type PortalAuthStoreAdapter struct {
	q           *Queries
	pool        *pgxpool.Pool
	tenantIndex TenantIndexer
}

// TenantIndexer is satisfied by *cache.TenantIndex. Defined here to avoid
// a compile-time dependency from store → cache.
type TenantIndexer interface {
	Add(ctx context.Context, tenantID uuid.UUID) error
	Remove(ctx context.Context, tenantID uuid.UUID) error
}

// NewPortalAuthStoreAdapter creates a new PortalAuthStoreAdapter.
// tenantIndex may be nil if Redis tenant tracking is not configured.
func NewPortalAuthStoreAdapter(q *Queries, pool *pgxpool.Pool, tenantIndex TenantIndexer) *PortalAuthStoreAdapter {
	return &PortalAuthStoreAdapter{q: q, pool: pool, tenantIndex: tenantIndex}
}

// CreateTenantWithUser creates a tenant and its first portal user atomically in a single transaction.
func (s *PortalAuthStoreAdapter) CreateTenantWithUser(ctx context.Context, tenant *domain.Tenant, user *domain.PortalUser) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("settla-auth: beginning transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	qtx := s.q.WithTx(tx)

	// Validate fee schedule before persisting
	if err := tenant.FeeSchedule.Validate(); err != nil {
		return err
	}

	feeJSON := fmt.Sprintf(
		`{"onramp_bps": %d, "offramp_bps": %d, "min_fee_usd": "%s", "max_fee_usd": "%s"}`,
		tenant.FeeSchedule.OnRampBPS, tenant.FeeSchedule.OffRampBPS,
		tenant.FeeSchedule.MinFeeUSD.String(), tenant.FeeSchedule.MaxFeeUSD.String(),
	)

	tenantRow, err := qtx.CreateTenant(ctx, CreateTenantParams{
		Name:             tenant.Name,
		Slug:             tenant.Slug,
		Status:           string(tenant.Status),
		FeeSchedule:      []byte(feeJSON),
		SettlementModel:  string(tenant.SettlementModel),
		WebhookUrl:       pgtype.Text{},
		WebhookSecret:    pgtype.Text{},
		DailyLimitUsd:    numericFromDecimal(tenant.DailyLimitUSD),
		PerTransferLimit: numericFromDecimal(tenant.PerTransferLimit),
		KybStatus:        string(tenant.KYBStatus),
		Metadata:         []byte(`{}`),
	})
	if err != nil {
		return fmt.Errorf("settla-auth: creating tenant: %w", err)
	}

	tenant.ID = tenantRow.ID
	user.TenantID = tenantRow.ID

	var emailTokenHash pgtype.Text
	if user.EmailTokenHash != "" {
		emailTokenHash = pgtype.Text{String: user.EmailTokenHash, Valid: true}
	}
	var emailTokenExpiresAt pgtype.Timestamptz
	if user.EmailTokenExpiresAt != nil {
		emailTokenExpiresAt = pgtype.Timestamptz{Time: *user.EmailTokenExpiresAt, Valid: true}
	}

	userRow, err := qtx.CreatePortalUser(ctx, CreatePortalUserParams{
		TenantID:            user.TenantID,
		Email:               user.Email,
		PasswordHash:        user.PasswordHash,
		DisplayName:         user.DisplayName,
		Role:                string(user.Role),
		EmailTokenHash:      emailTokenHash,
		EmailTokenExpiresAt: emailTokenExpiresAt,
	})
	if err != nil {
		return fmt.Errorf("settla-auth: creating portal user: %w", err)
	}

	user.ID = userRow.ID

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("settla-auth: committing transaction: %w", err)
	}

	return nil
}

// GetPortalUserByEmail retrieves a portal user by email with tenant info.
// Returns (user, tenantName, tenantSlug, tenantStatus, kybStatus, error).
func (s *PortalAuthStoreAdapter) GetPortalUserByEmail(ctx context.Context, email string) (*domain.PortalUser, string, string, string, string, error) {
	row, err := s.q.GetPortalUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", "", "", "", nil
		}
		return nil, "", "", "", "", fmt.Errorf("settla-auth: getting portal user by email: %w", err)
	}

	user := portalUserFromEmailRow(row)
	return user, row.TenantName, row.TenantSlug, row.TenantStatus, row.KybStatus, nil
}

// GetPortalUserByID retrieves a portal user by ID with tenant info.
func (s *PortalAuthStoreAdapter) GetPortalUserByID(ctx context.Context, id uuid.UUID) (*domain.PortalUser, string, string, string, string, error) {
	row, err := s.q.GetPortalUserByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", "", "", "", nil
		}
		return nil, "", "", "", "", fmt.Errorf("settla-auth: getting portal user by ID: %w", err)
	}

	user := portalUserFromIDRow(row)
	return user, row.TenantName, row.TenantSlug, row.TenantStatus, row.KybStatus, nil
}

// VerifyEmail marks a portal user's email as verified by token hash.
func (s *PortalAuthStoreAdapter) VerifyEmail(ctx context.Context, tokenHash string) error {
	return s.q.VerifyPortalUserEmail(ctx, pgtype.Text{String: tokenHash, Valid: true})
}

// UpdateLastLogin updates the last login timestamp for a portal user.
func (s *PortalAuthStoreAdapter) UpdateLastLogin(ctx context.Context, userID uuid.UUID) error {
	return s.q.UpdatePortalUserLastLogin(ctx, userID)
}

// GetTenantBySlug retrieves a tenant by slug (for slug conflict detection).
func (s *PortalAuthStoreAdapter) GetTenantBySlug(ctx context.Context, slug string) (*domain.Tenant, error) {
	row, err := s.q.GetTenantBySlug(ctx, slug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("settla-auth: getting tenant by slug: %w", err)
	}
	t, _ := tenantFromRow(row)
	return t, nil
}

// UpdateTenantKYB updates the KYB status for a tenant.
func (s *PortalAuthStoreAdapter) UpdateTenantKYB(ctx context.Context, tenantID uuid.UUID, kybStatus string) error {
	return s.q.UpdateTenantKYB(ctx, UpdateTenantKYBParams{
		ID:        tenantID,
		KybStatus: kybStatus,
	})
}

// UpdateTenantStatus updates the status of a tenant and syncs the Redis tenant index.
func (s *PortalAuthStoreAdapter) UpdateTenantStatus(ctx context.Context, tenantID uuid.UUID, status string) error {
	if err := s.q.UpdateTenantStatus(ctx, UpdateTenantStatusParams{
		ID:     tenantID,
		Status: status,
	}); err != nil {
		return err
	}

	if s.tenantIndex != nil {
		switch status {
		case "ACTIVE":
			_ = s.tenantIndex.Add(ctx, tenantID)
		case "SUSPENDED":
			_ = s.tenantIndex.Remove(ctx, tenantID)
		}
	}
	return nil
}

// UpdateTenantMetadata updates the metadata JSONB field for a tenant.
func (s *PortalAuthStoreAdapter) UpdateTenantMetadata(ctx context.Context, tenantID uuid.UUID, metadata []byte) error {
	return s.q.UpdateTenantMetadata(ctx, UpdateTenantMetadataParams{
		ID:       tenantID,
		Metadata: metadata,
	})
}

// GetTenant retrieves a tenant by ID.
func (s *PortalAuthStoreAdapter) GetTenant(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error) {
	row, err := s.q.GetTenant(ctx, tenantID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("settla-auth: getting tenant: %w", err)
	}
	t, _ := tenantFromRow(row)
	return t, nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

func portalUserFromEmailRow(row GetPortalUserByEmailRow) *domain.PortalUser {
	return buildPortalUser(
		row.ID, row.TenantID, row.Email, row.PasswordHash, row.DisplayName,
		row.Role, row.EmailVerified, row.EmailTokenHash, row.EmailTokenExpiresAt,
		row.LastLoginAt, row.CreatedAt, row.UpdatedAt,
	)
}

func portalUserFromIDRow(row GetPortalUserByIDRow) *domain.PortalUser {
	return buildPortalUser(
		row.ID, row.TenantID, row.Email, row.PasswordHash, row.DisplayName,
		row.Role, row.EmailVerified, row.EmailTokenHash, row.EmailTokenExpiresAt,
		row.LastLoginAt, row.CreatedAt, row.UpdatedAt,
	)
}

func buildPortalUser(
	id, tenantID uuid.UUID, email, passwordHash, displayName, role string,
	emailVerified bool, emailTokenHash pgtype.Text, emailTokenExpiresAt pgtype.Timestamptz,
	lastLoginAt pgtype.Timestamptz, createdAt, updatedAt time.Time,
) *domain.PortalUser {
	user := &domain.PortalUser{
		ID:            id,
		TenantID:      tenantID,
		Email:         email,
		PasswordHash:  passwordHash,
		DisplayName:   displayName,
		Role:          domain.PortalUserRole(role),
		EmailVerified: emailVerified,
		CreatedAt:     createdAt,
		UpdatedAt:     updatedAt,
	}

	if emailTokenHash.Valid {
		user.EmailTokenHash = emailTokenHash.String
	}
	if emailTokenExpiresAt.Valid {
		t := emailTokenExpiresAt.Time
		user.EmailTokenExpiresAt = &t
	}
	if lastLoginAt.Valid {
		t := lastLoginAt.Time
		user.LastLoginAt = &t
	}

	return user
}
