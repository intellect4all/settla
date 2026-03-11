package transferdb

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/intellect4all/settla/domain"
)

// PortalStoreAdapter implements grpc.TenantPortalStore using SQLC-generated queries.
type PortalStoreAdapter struct {
	q       *Queries
	tenants *TenantStoreAdapter
}

// NewPortalStoreAdapter creates a new PortalStoreAdapter.
func NewPortalStoreAdapter(q *Queries) *PortalStoreAdapter {
	return &PortalStoreAdapter{
		q:       q,
		tenants: NewTenantStoreAdapter(q),
	}
}

func (s *PortalStoreAdapter) GetTenant(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error) {
	return s.tenants.GetTenant(ctx, tenantID)
}

func (s *PortalStoreAdapter) UpdateWebhookConfig(ctx context.Context, tenantID uuid.UUID, webhookURL, webhookSecret string) error {
	err := s.q.UpdateTenantWebhook(ctx, UpdateTenantWebhookParams{
		ID:            tenantID,
		WebhookUrl:    pgtype.Text{String: webhookURL, Valid: true},
		WebhookSecret: pgtype.Text{String: webhookSecret, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("settla-portal: updating webhook config for tenant %s: %w", tenantID, err)
	}
	return nil
}

func (s *PortalStoreAdapter) ListAPIKeys(ctx context.Context, tenantID uuid.UUID) ([]domain.APIKey, error) {
	rows, err := s.q.ListAPIKeysByTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("settla-portal: listing API keys for tenant %s: %w", tenantID, err)
	}

	keys := make([]domain.APIKey, len(rows))
	for i, row := range rows {
		keys[i] = domain.APIKey{
			ID:          row.ID,
			TenantID:    row.TenantID,
			KeyPrefix:   row.KeyPrefix,
			Environment: row.Environment,
			Name:        stringFromText(row.Name),
			IsActive:    row.IsActive,
			CreatedAt:   row.CreatedAt,
		}
		if row.ExpiresAt.Valid {
			t := row.ExpiresAt.Time
			keys[i].ExpiresAt = &t
		}
	}
	return keys, nil
}

func (s *PortalStoreAdapter) CreateAPIKey(ctx context.Context, key *domain.APIKey) error {
	var expiresAt pgtype.Timestamptz
	if key.ExpiresAt != nil {
		expiresAt = pgtype.Timestamptz{Time: *key.ExpiresAt, Valid: true}
	}

	row, err := s.q.CreateAPIKey(ctx, CreateAPIKeyParams{
		TenantID:    key.TenantID,
		KeyHash:     key.KeyHash,
		KeyPrefix:   key.KeyPrefix,
		Environment: key.Environment,
		Name:        textFromString(key.Name),
		ExpiresAt:   expiresAt,
	})
	if err != nil {
		return fmt.Errorf("settla-portal: creating API key for tenant %s: %w", key.TenantID, err)
	}
	key.ID = row.ID
	key.CreatedAt = row.CreatedAt
	return nil
}

func (s *PortalStoreAdapter) DeactivateAPIKeyByTenant(ctx context.Context, tenantID, keyID uuid.UUID) error {
	err := s.q.DeactivateAPIKeyByTenant(ctx, DeactivateAPIKeyByTenantParams{
		KeyID:    keyID,
		TenantID: tenantID,
	})
	if err != nil {
		return fmt.Errorf("settla-portal: deactivating API key %s for tenant %s: %w", keyID, tenantID, err)
	}
	return nil
}

func (s *PortalStoreAdapter) GetAPIKeyByIDAndTenant(ctx context.Context, tenantID, keyID uuid.UUID) (*domain.APIKey, error) {
	row, err := s.q.GetAPIKeyByIDAndTenant(ctx, GetAPIKeyByIDAndTenantParams{
		KeyID:    keyID,
		TenantID: tenantID,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-portal: getting API key %s for tenant %s: %w", keyID, tenantID, err)
	}

	key := &domain.APIKey{
		ID:          row.ID,
		TenantID:    row.TenantID,
		KeyPrefix:   row.KeyPrefix,
		Environment: row.Environment,
		Name:        stringFromText(row.Name),
		IsActive:    row.IsActive,
		CreatedAt:   row.CreatedAt,
	}
	if row.ExpiresAt.Valid {
		t := row.ExpiresAt.Time
		key.ExpiresAt = &t
	}
	return key, nil
}

func (s *PortalStoreAdapter) GetDashboardMetrics(ctx context.Context, tenantID uuid.UUID, todayStart, sevenDaysAgo, thirtyDaysAgo time.Time) (*domain.DashboardMetrics, error) {
	row, err := s.q.GetTenantDashboardMetrics(ctx, GetTenantDashboardMetricsParams{
		TenantID:      tenantID,
		TodayStart:    todayStart,
		SevenDaysAgo:  sevenDaysAgo,
		ThirtyDaysAgo: thirtyDaysAgo,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-portal: getting dashboard metrics for tenant %s: %w", tenantID, err)
	}

	return &domain.DashboardMetrics{
		TransfersToday: row.TransfersToday,
		VolumeTodayUSD: decimalFromNumeric(row.VolumeTodayUsd),
		CompletedToday: row.CompletedToday,
		FailedToday:    row.FailedToday,
		Transfers7D:    row.Transfers7d,
		Volume7DUSD:    decimalFromNumeric(row.Volume7dUsd),
		Fees7DUSD:      decimalFromNumeric(row.Fees7dUsd),
		Transfers30D:   row.Transfers30d,
		Volume30DUSD:   decimalFromNumeric(row.Volume30dUsd),
		Fees30DUSD:     decimalFromNumeric(row.Fees30dUsd),
		SuccessRate30D: decimalFromNumeric(row.SuccessRate30d),
	}, nil
}

func (s *PortalStoreAdapter) GetTransferStatsHourly(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.TransferStatsBucket, error) {
	rows, err := s.q.GetTransferStatsHourly(ctx, GetTransferStatsHourlyParams{
		TenantID: tenantID,
		FromTime: from,
		ToTime:   to,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-portal: getting hourly transfer stats: %w", err)
	}

	buckets := make([]domain.TransferStatsBucket, len(rows))
	for i, row := range rows {
		buckets[i] = domain.TransferStatsBucket{
			Timestamp: row.Bucket,
			Total:     row.Total,
			Completed: row.Completed,
			Failed:    row.Failed,
			VolumeUSD: decimalFromNumeric(row.VolumeUsd),
			FeesUSD:   decimalFromNumeric(row.FeesUsd),
		}
	}
	return buckets, nil
}

func (s *PortalStoreAdapter) GetTransferStatsDaily(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.TransferStatsBucket, error) {
	rows, err := s.q.GetTransferStatsDaily(ctx, GetTransferStatsDailyParams{
		TenantID: tenantID,
		FromTime: from,
		ToTime:   to,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-portal: getting daily transfer stats: %w", err)
	}

	buckets := make([]domain.TransferStatsBucket, len(rows))
	for i, row := range rows {
		buckets[i] = domain.TransferStatsBucket{
			Timestamp: row.Bucket,
			Total:     row.Total,
			Completed: row.Completed,
			Failed:    row.Failed,
			VolumeUSD: decimalFromNumeric(row.VolumeUsd),
			FeesUSD:   decimalFromNumeric(row.FeesUsd),
		}
	}
	return buckets, nil
}

func (s *PortalStoreAdapter) GetFeeReportByCorridor(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.FeeReportEntry, error) {
	rows, err := s.q.GetFeeReportByCorridor(ctx, GetFeeReportByCorridorParams{
		TenantID: tenantID,
		FromTime: from,
		ToTime:   to,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-portal: getting fee report: %w", err)
	}

	entries := make([]domain.FeeReportEntry, len(rows))
	for i, row := range rows {
		entries[i] = domain.FeeReportEntry{
			SourceCurrency: row.SourceCurrency,
			DestCurrency:   row.DestCurrency,
			TransferCount:  row.TransferCount,
			TotalVolumeUSD: decimalFromNumeric(row.TotalVolumeUsd),
			OnRampFeesUSD:  decimalFromNumeric(row.OnRampFeesUsd),
			OffRampFeesUSD: decimalFromNumeric(row.OffRampFeesUsd),
			NetworkFeesUSD: decimalFromNumeric(row.NetworkFeesUsd),
			TotalFeesUSD:   decimalFromNumeric(row.TotalFeesUsd),
		}
	}
	return entries, nil
}

// stringFromText converts pgtype.Text to string.
func stringFromText(t pgtype.Text) string {
	if !t.Valid {
		return ""
	}
	return t.String
}
