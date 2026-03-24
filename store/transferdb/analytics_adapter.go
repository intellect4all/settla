package transferdb

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	pgx "github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/store/rls"
)

// AnalyticsStore provides enhanced analytics data access.
type AnalyticsStore interface {
	GetTransferStatusDistribution(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.StatusCount, error)
	GetCorridorMetrics(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.CorridorMetrics, error)
	GetTransferLatencyPercentiles(ctx context.Context, tenantID uuid.UUID, from, to time.Time) (*domain.LatencyPercentiles, error)
	GetVolumeComparison(ctx context.Context, tenantID uuid.UUID, previousStart, currentStart, currentEnd time.Time) (*domain.VolumeComparison, error)
	GetRecentActivity(ctx context.Context, tenantID uuid.UUID, limit int32) ([]domain.ActivityItem, error)
}

// AnalyticsAdapter implements AnalyticsStore using SQLC-generated queries.
type AnalyticsAdapter struct {
	q          *Queries
	appPool    *pgxpool.Pool // optional: RLS-enforced pool
	rlsEnabled bool          // true when appPool is configured; false means RLS is bypassed
}

// NewAnalyticsAdapter creates a new AnalyticsAdapter.
func NewAnalyticsAdapter(q *Queries) *AnalyticsAdapter {
	a := &AnalyticsAdapter{q: q}
	a.rlsEnabled = (a.appPool != nil)
	if !a.rlsEnabled {
		slog.Warn("settla-store: AnalyticsAdapter RLS pool not configured, tenant isolation relies on application-layer filters only")
	}
	return a
}

// WithAnalyticsAppPool configures the RLS-enforced pool for tenant-scoped operations.
func (a *AnalyticsAdapter) WithAnalyticsAppPool(pool *pgxpool.Pool) *AnalyticsAdapter {
	a.appPool = pool
	a.rlsEnabled = (pool != nil)
	return a
}

func (a *AnalyticsAdapter) GetTransferStatusDistribution(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.StatusCount, error) {
	params := GetTransferStatusDistributionParams{
		TenantID: tenantID,
		FromTime: from,
		ToTime:   to,
	}
	toResult := func(rows []GetTransferStatusDistributionRow) []domain.StatusCount {
		result := make([]domain.StatusCount, len(rows))
		for i, row := range rows {
			result[i] = domain.StatusCount{
				Status: string(row.Status),
				Count:  row.Count,
			}
		}
		return result
	}

	if a.appPool != nil {
		var result []domain.StatusCount
		err := rls.WithTenantReadTx(ctx, a.appPool, tenantID, func(tx pgx.Tx) error {
			rows, err := a.q.WithTx(tx).GetTransferStatusDistribution(ctx, params)
			if err != nil {
				return err
			}
			result = toResult(rows)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("settla-analytics: getting status distribution: %w", err)
		}
		return result, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "GetTransferStatusDistribution", "tenant_id", tenantID)
	rows, err := a.q.GetTransferStatusDistribution(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("settla-analytics: getting status distribution: %w", err)
	}
	return toResult(rows), nil
}

func (a *AnalyticsAdapter) GetCorridorMetrics(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.CorridorMetrics, error) {
	params := GetCorridorMetricsParams{
		TenantID: tenantID,
		FromTime: from,
		ToTime:   to,
	}

	if a.appPool != nil {
		var result []domain.CorridorMetrics
		err := rls.WithTenantReadTx(ctx, a.appPool, tenantID, func(tx pgx.Tx) error {
			rows, err := a.q.WithTx(tx).GetCorridorMetrics(ctx, params)
			if err != nil {
				return err
			}
			result = corridorMetricsFromRows(rows)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("settla-analytics: getting corridor metrics: %w", err)
		}
		return result, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "GetCorridorMetrics", "tenant_id", tenantID)
	rows, err := a.q.GetCorridorMetrics(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("settla-analytics: getting corridor metrics: %w", err)
	}
	return corridorMetricsFromRows(rows), nil
}

func corridorMetricsFromRows(rows []GetCorridorMetricsRow) []domain.CorridorMetrics {
	result := make([]domain.CorridorMetrics, len(rows))
	for i, row := range rows {
		result[i] = domain.CorridorMetrics{
			SourceCurrency: domain.Currency(row.SourceCurrency),
			DestCurrency:   domain.Currency(row.DestCurrency),
			TransferCount:  row.TransferCount,
			VolumeUSD:      decimalFromNumeric(row.VolumeUsd),
			FeesUSD:        decimalFromNumeric(row.FeesUsd),
			Completed:      row.Completed,
			Failed:         row.Failed,
			SuccessRate:    decimalFromNumeric(row.SuccessRate),
			AvgLatencyMs:   row.AvgLatencyMs,
		}
	}
	return result
}

func (a *AnalyticsAdapter) GetTransferLatencyPercentiles(ctx context.Context, tenantID uuid.UUID, from, to time.Time) (*domain.LatencyPercentiles, error) {
	params := GetTransferLatencyPercentilesParams{
		TenantID: tenantID,
		FromTime: from,
		ToTime:   to,
	}

	if a.appPool != nil {
		var result *domain.LatencyPercentiles
		err := rls.WithTenantReadTx(ctx, a.appPool, tenantID, func(tx pgx.Tx) error {
			row, err := a.q.WithTx(tx).GetTransferLatencyPercentiles(ctx, params)
			if err != nil {
				return err
			}
			result = &domain.LatencyPercentiles{
				SampleCount: row.SampleCount,
				P50Ms:       row.P50Ms,
				P90Ms:       row.P90Ms,
				P95Ms:       row.P95Ms,
				P99Ms:       row.P99Ms,
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("settla-analytics: getting latency percentiles: %w", err)
		}
		return result, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "GetTransferLatencyPercentiles", "tenant_id", tenantID)
	row, err := a.q.GetTransferLatencyPercentiles(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("settla-analytics: getting latency percentiles: %w", err)
	}
	return &domain.LatencyPercentiles{
		SampleCount: row.SampleCount,
		P50Ms:       row.P50Ms,
		P90Ms:       row.P90Ms,
		P95Ms:       row.P95Ms,
		P99Ms:       row.P99Ms,
	}, nil
}

func (a *AnalyticsAdapter) GetVolumeComparison(ctx context.Context, tenantID uuid.UUID, previousStart, currentStart, currentEnd time.Time) (*domain.VolumeComparison, error) {
	params := GetVolumeComparisonParams{
		TenantID:      tenantID,
		PreviousStart: previousStart,
		CurrentStart:  currentStart,
		CurrentEnd:    currentEnd,
	}
	toComparison := func(row GetVolumeComparisonRow) *domain.VolumeComparison {
		return &domain.VolumeComparison{
			CurrentCount:      row.CurrentCount,
			CurrentVolumeUSD:  decimalFromNumeric(row.CurrentVolumeUsd),
			CurrentFeesUSD:    decimalFromNumeric(row.CurrentFeesUsd),
			PreviousCount:     row.PreviousCount,
			PreviousVolumeUSD: decimalFromNumeric(row.PreviousVolumeUsd),
			PreviousFeesUSD:   decimalFromNumeric(row.PreviousFeesUsd),
		}
	}

	if a.appPool != nil {
		var result *domain.VolumeComparison
		err := rls.WithTenantReadTx(ctx, a.appPool, tenantID, func(tx pgx.Tx) error {
			row, err := a.q.WithTx(tx).GetVolumeComparison(ctx, params)
			if err != nil {
				return err
			}
			result = toComparison(row)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("settla-analytics: getting volume comparison: %w", err)
		}
		return result, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "GetVolumeComparison", "tenant_id", tenantID)
	row, err := a.q.GetVolumeComparison(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("settla-analytics: getting volume comparison: %w", err)
	}
	return toComparison(row), nil
}

func (a *AnalyticsAdapter) GetRecentActivity(ctx context.Context, tenantID uuid.UUID, limit int32) ([]domain.ActivityItem, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	params := GetRecentActivityParams{
		TenantID: tenantID,
		PageSize: limit,
	}
	toItems := func(rows []GetRecentActivityRow) []domain.ActivityItem {
		items := make([]domain.ActivityItem, len(rows))
		for i, row := range rows {
			items[i] = domain.ActivityItem{
				TransferID:     row.TransferID.String(),
				ExternalRef:    stringFromText(row.ExternalRef),
				Status:         string(row.Status),
				SourceCurrency: domain.Currency(row.SourceCurrency),
				SourceAmount:   decimalFromNumeric(row.SourceAmount),
				DestCurrency:   domain.Currency(row.DestCurrency),
				DestAmount:     decimalFromNumeric(row.DestAmount),
				UpdatedAt:      row.UpdatedAt,
				FailureReason:  stringFromText(row.FailureReason),
			}
		}
		return items
	}

	if a.appPool != nil {
		var items []domain.ActivityItem
		err := rls.WithTenantReadTx(ctx, a.appPool, tenantID, func(tx pgx.Tx) error {
			rows, err := a.q.WithTx(tx).GetRecentActivity(ctx, params)
			if err != nil {
				return err
			}
			items = toItems(rows)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("settla-analytics: getting recent activity: %w", err)
		}
		return items, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "GetRecentActivity", "tenant_id", tenantID)
	rows, err := a.q.GetRecentActivity(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("settla-analytics: getting recent activity: %w", err)
	}
	return toItems(rows), nil
}
