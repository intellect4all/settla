package transferdb

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	pgx "github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/store/rls"
)

// PortalStoreAdapter implements grpc.TenantPortalStore using SQLC-generated queries.
type PortalStoreAdapter struct {
	q          *Queries
	tenants    *TenantStoreAdapter
	appPool    *pgxpool.Pool // optional: RLS-enforced pool
	rlsEnabled bool          // true when appPool is configured; false means RLS is bypassed
}

// NewPortalStoreAdapter creates a new PortalStoreAdapter.
func NewPortalStoreAdapter(q *Queries) *PortalStoreAdapter {
	a := &PortalStoreAdapter{
		q:       q,
		tenants: NewTenantStoreAdapter(q),
	}
	a.rlsEnabled = (a.appPool != nil)
	if !a.rlsEnabled {
		slog.Warn("settla-store: PortalStoreAdapter RLS pool not configured, tenant isolation relies on application-layer filters only")
	}
	return a
}

// WithPortalAppPool configures the RLS-enforced pool for tenant-scoped operations.
func (s *PortalStoreAdapter) WithPortalAppPool(pool *pgxpool.Pool) *PortalStoreAdapter {
	s.appPool = pool
	s.rlsEnabled = (pool != nil)
	return s
}

func (s *PortalStoreAdapter) GetTenant(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error) {
	return s.tenants.GetTenant(ctx, tenantID)
}

func (s *PortalStoreAdapter) UpdateWebhookConfig(ctx context.Context, tenantID uuid.UUID, webhookURL, webhookSecret string) error {
	params := UpdateTenantWebhookParams{
		ID:            tenantID,
		WebhookUrl:    pgtype.Text{String: webhookURL, Valid: true},
		WebhookSecret: pgtype.Text{String: webhookSecret, Valid: true},
	}

	if s.appPool != nil {
		return rls.WithTenantTx(ctx, s.appPool, tenantID, func(tx pgx.Tx) error {
			return s.q.WithTx(tx).UpdateTenantWebhook(ctx, params)
		})
	}

	slog.Warn("settla-store: RLS bypassed", "method", "UpdateWebhookConfig", "tenant_id", tenantID)
	return s.q.UpdateTenantWebhook(ctx, params)
}

func (s *PortalStoreAdapter) ListAPIKeys(ctx context.Context, tenantID uuid.UUID) ([]domain.APIKey, error) {
	parseKeys := func(rows []ListAPIKeysByTenantRow) []domain.APIKey {
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
		return keys
	}

	if s.appPool != nil {
		var keys []domain.APIKey
		err := rls.WithTenantReadTx(ctx, s.appPool, tenantID, func(tx pgx.Tx) error {
			rows, err := s.q.WithTx(tx).ListAPIKeysByTenant(ctx, tenantID)
			if err != nil {
				return err
			}
			keys = parseKeys(rows)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("settla-portal: listing API keys for tenant %s: %w", tenantID, err)
		}
		return keys, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "ListAPIKeys", "tenant_id", tenantID)
	rows, err := s.q.ListAPIKeysByTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("settla-portal: listing API keys for tenant %s: %w", tenantID, err)
	}
	return parseKeys(rows), nil
}

func (s *PortalStoreAdapter) CreateAPIKey(ctx context.Context, key *domain.APIKey) error {
	var expiresAt pgtype.Timestamptz
	if key.ExpiresAt != nil {
		expiresAt = pgtype.Timestamptz{Time: *key.ExpiresAt, Valid: true}
	}

	params := CreateAPIKeyParams{
		TenantID:    key.TenantID,
		KeyHash:     key.KeyHash,
		KeyPrefix:   key.KeyPrefix,
		Environment: key.Environment,
		Name:        textFromString(key.Name),
		ExpiresAt:   expiresAt,
	}

	if s.appPool != nil {
		return rls.WithTenantTx(ctx, s.appPool, key.TenantID, func(tx pgx.Tx) error {
			row, err := s.q.WithTx(tx).CreateAPIKey(ctx, params)
			if err != nil {
				return fmt.Errorf("settla-portal: creating API key for tenant %s: %w", key.TenantID, err)
			}
			key.ID = row.ID
			key.CreatedAt = row.CreatedAt
			return nil
		})
	}

	slog.Warn("settla-store: RLS bypassed", "method", "CreateAPIKey", "tenant_id", key.TenantID)
	row, err := s.q.CreateAPIKey(ctx, params)
	if err != nil {
		return fmt.Errorf("settla-portal: creating API key for tenant %s: %w", key.TenantID, err)
	}
	key.ID = row.ID
	key.CreatedAt = row.CreatedAt
	return nil
}

func (s *PortalStoreAdapter) DeactivateAPIKeyByTenant(ctx context.Context, tenantID, keyID uuid.UUID) error {
	params := DeactivateAPIKeyByTenantParams{
		KeyID:    keyID,
		TenantID: tenantID,
	}

	if s.appPool != nil {
		return rls.WithTenantTx(ctx, s.appPool, tenantID, func(tx pgx.Tx) error {
			return s.q.WithTx(tx).DeactivateAPIKeyByTenant(ctx, params)
		})
	}

	slog.Warn("settla-store: RLS bypassed", "method", "DeactivateAPIKeyByTenant", "tenant_id", tenantID)
	return s.q.DeactivateAPIKeyByTenant(ctx, params)
}

func (s *PortalStoreAdapter) GetAPIKeyByIDAndTenant(ctx context.Context, tenantID, keyID uuid.UUID) (*domain.APIKey, error) {
	toKey := func(row GetAPIKeyByIDAndTenantRow) *domain.APIKey {
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
		return key
	}

	if s.appPool != nil {
		var result *domain.APIKey
		err := rls.WithTenantReadTx(ctx, s.appPool, tenantID, func(tx pgx.Tx) error {
			row, err := s.q.WithTx(tx).GetAPIKeyByIDAndTenant(ctx, GetAPIKeyByIDAndTenantParams{
				KeyID:    keyID,
				TenantID: tenantID,
			})
			if err != nil {
				return err
			}
			result = toKey(row)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("settla-portal: getting API key %s for tenant %s: %w", keyID, tenantID, err)
		}
		return result, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "GetAPIKeyByIDAndTenant", "tenant_id", tenantID)
	row, err := s.q.GetAPIKeyByIDAndTenant(ctx, GetAPIKeyByIDAndTenantParams{
		KeyID:    keyID,
		TenantID: tenantID,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-portal: getting API key %s for tenant %s: %w", keyID, tenantID, err)
	}
	return toKey(row), nil
}

func (s *PortalStoreAdapter) GetDashboardMetrics(ctx context.Context, tenantID uuid.UUID, todayStart, sevenDaysAgo, thirtyDaysAgo time.Time) (*domain.DashboardMetrics, error) {
	params := GetTenantDashboardMetricsParams{
		TenantID:      tenantID,
		TodayStart:    todayStart,
		SevenDaysAgo:  sevenDaysAgo,
		ThirtyDaysAgo: thirtyDaysAgo,
	}
	toMetrics := func(row GetTenantDashboardMetricsRow) *domain.DashboardMetrics {
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
		}
	}

	if s.appPool != nil {
		var result *domain.DashboardMetrics
		err := rls.WithTenantReadTx(ctx, s.appPool, tenantID, func(tx pgx.Tx) error {
			row, err := s.q.WithTx(tx).GetTenantDashboardMetrics(ctx, params)
			if err != nil {
				return err
			}
			result = toMetrics(row)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("settla-portal: getting dashboard metrics for tenant %s: %w", tenantID, err)
		}
		return result, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "GetDashboardMetrics", "tenant_id", tenantID)
	row, err := s.q.GetTenantDashboardMetrics(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("settla-portal: getting dashboard metrics for tenant %s: %w", tenantID, err)
	}
	return toMetrics(row), nil
}

func (s *PortalStoreAdapter) GetTransferStatsHourly(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.TransferStatsBucket, error) {
	params := GetTransferStatsHourlyParams{
		TenantID: tenantID,
		FromTime: from,
		ToTime:   to,
	}
	toBuckets := func(rows []GetTransferStatsHourlyRow) []domain.TransferStatsBucket {
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
		return buckets
	}

	if s.appPool != nil {
		var buckets []domain.TransferStatsBucket
		err := rls.WithTenantReadTx(ctx, s.appPool, tenantID, func(tx pgx.Tx) error {
			rows, err := s.q.WithTx(tx).GetTransferStatsHourly(ctx, params)
			if err != nil {
				return err
			}
			buckets = toBuckets(rows)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("settla-portal: getting hourly transfer stats: %w", err)
		}
		return buckets, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "GetTransferStatsHourly", "tenant_id", tenantID)
	rows, err := s.q.GetTransferStatsHourly(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("settla-portal: getting hourly transfer stats: %w", err)
	}
	return toBuckets(rows), nil
}

func (s *PortalStoreAdapter) GetTransferStatsDaily(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.TransferStatsBucket, error) {
	params := GetTransferStatsDailyParams{
		TenantID: tenantID,
		FromTime: from,
		ToTime:   to,
	}
	toBuckets := func(rows []GetTransferStatsDailyRow) []domain.TransferStatsBucket {
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
		return buckets
	}

	if s.appPool != nil {
		var buckets []domain.TransferStatsBucket
		err := rls.WithTenantReadTx(ctx, s.appPool, tenantID, func(tx pgx.Tx) error {
			rows, err := s.q.WithTx(tx).GetTransferStatsDaily(ctx, params)
			if err != nil {
				return err
			}
			buckets = toBuckets(rows)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("settla-portal: getting daily transfer stats: %w", err)
		}
		return buckets, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "GetTransferStatsDaily", "tenant_id", tenantID)
	rows, err := s.q.GetTransferStatsDaily(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("settla-portal: getting daily transfer stats: %w", err)
	}
	return toBuckets(rows), nil
}

func (s *PortalStoreAdapter) GetFeeReportByCorridor(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.FeeReportEntry, error) {
	params := GetFeeReportByCorridorParams{
		TenantID: tenantID,
		FromTime: from,
		ToTime:   to,
	}
	toEntries := func(rows []GetFeeReportByCorridorRow) []domain.FeeReportEntry {
		entries := make([]domain.FeeReportEntry, len(rows))
		for i, row := range rows {
			entries[i] = domain.FeeReportEntry{
				SourceCurrency: domain.Currency(row.SourceCurrency),
				DestCurrency:   domain.Currency(row.DestCurrency),
				TransferCount:  row.TransferCount,
				TotalVolumeUSD: decimalFromNumeric(row.TotalVolumeUsd),
				OnRampFeesUSD:  decimalFromNumeric(row.OnRampFeesUsd),
				OffRampFeesUSD: decimalFromNumeric(row.OffRampFeesUsd),
				NetworkFeesUSD: decimalFromNumeric(row.NetworkFeesUsd),
				TotalFeesUSD:   decimalFromNumeric(row.TotalFeesUsd),
			}
		}
		return entries
	}

	if s.appPool != nil {
		var entries []domain.FeeReportEntry
		err := rls.WithTenantReadTx(ctx, s.appPool, tenantID, func(tx pgx.Tx) error {
			rows, err := s.q.WithTx(tx).GetFeeReportByCorridor(ctx, params)
			if err != nil {
				return err
			}
			entries = toEntries(rows)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("settla-portal: getting fee report: %w", err)
		}
		return entries, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "GetFeeReportByCorridor", "tenant_id", tenantID)
	rows, err := s.q.GetFeeReportByCorridor(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("settla-portal: getting fee report: %w", err)
	}
	return toEntries(rows), nil
}

// stringFromText converts pgtype.Text to string.
func stringFromText(t pgtype.Text) string {
	if !t.Valid {
		return ""
	}
	return t.String
}
