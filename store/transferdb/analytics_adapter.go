package transferdb

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/intellect4all/settla/domain"
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
	q *Queries
}

// NewAnalyticsAdapter creates a new AnalyticsAdapter.
func NewAnalyticsAdapter(q *Queries) *AnalyticsAdapter {
	return &AnalyticsAdapter{q: q}
}

func (a *AnalyticsAdapter) GetTransferStatusDistribution(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.StatusCount, error) {
	rows, err := a.q.GetTransferStatusDistribution(ctx, GetTransferStatusDistributionParams{
		TenantID: tenantID,
		FromTime: from,
		ToTime:   to,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-analytics: getting status distribution: %w", err)
	}

	result := make([]domain.StatusCount, len(rows))
	for i, row := range rows {
		result[i] = domain.StatusCount{
			Status: string(row.Status),
			Count:  row.Count,
		}
	}
	return result, nil
}

func (a *AnalyticsAdapter) GetCorridorMetrics(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.CorridorMetrics, error) {
	rows, err := a.q.GetCorridorMetrics(ctx, GetCorridorMetricsParams{
		TenantID: tenantID,
		FromTime: from,
		ToTime:   to,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-analytics: getting corridor metrics: %w", err)
	}

	result := make([]domain.CorridorMetrics, len(rows))
	for i, row := range rows {
		result[i] = domain.CorridorMetrics{
			SourceCurrency: row.SourceCurrency,
			DestCurrency:   row.DestCurrency,
			TransferCount:  row.TransferCount,
			VolumeUSD:      decimalFromNumeric(row.VolumeUsd),
			FeesUSD:        decimalFromNumeric(row.FeesUsd),
			Completed:      row.Completed,
			Failed:         row.Failed,
			SuccessRate:    decimalFromNumeric(row.SuccessRate),
			AvgLatencyMs:   row.AvgLatencyMs,
		}
	}
	return result, nil
}

func (a *AnalyticsAdapter) GetTransferLatencyPercentiles(ctx context.Context, tenantID uuid.UUID, from, to time.Time) (*domain.LatencyPercentiles, error) {
	row, err := a.q.GetTransferLatencyPercentiles(ctx, GetTransferLatencyPercentilesParams{
		TenantID: tenantID,
		FromTime: from,
		ToTime:   to,
	})
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
	row, err := a.q.GetVolumeComparison(ctx, GetVolumeComparisonParams{
		TenantID:      tenantID,
		PreviousStart: previousStart,
		CurrentStart:  currentStart,
		CurrentEnd:    currentEnd,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-analytics: getting volume comparison: %w", err)
	}

	return &domain.VolumeComparison{
		CurrentCount:      row.CurrentCount,
		CurrentVolumeUSD:  decimalFromNumeric(row.CurrentVolumeUsd),
		CurrentFeesUSD:    decimalFromNumeric(row.CurrentFeesUsd),
		PreviousCount:     row.PreviousCount,
		PreviousVolumeUSD: decimalFromNumeric(row.PreviousVolumeUsd),
		PreviousFeesUSD:   decimalFromNumeric(row.PreviousFeesUsd),
	}, nil
}

func (a *AnalyticsAdapter) GetRecentActivity(ctx context.Context, tenantID uuid.UUID, limit int32) ([]domain.ActivityItem, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	rows, err := a.q.GetRecentActivity(ctx, GetRecentActivityParams{
		TenantID: tenantID,
		PageSize: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-analytics: getting recent activity: %w", err)
	}

	items := make([]domain.ActivityItem, len(rows))
	for i, row := range rows {
		items[i] = domain.ActivityItem{
			TransferID:     row.TransferID.String(),
			ExternalRef:    stringFromText(row.ExternalRef),
			Status:         string(row.Status),
			SourceCurrency: row.SourceCurrency,
			SourceAmount:   decimalFromNumeric(row.SourceAmount),
			DestCurrency:   row.DestCurrency,
			DestAmount:     decimalFromNumeric(row.DestAmount),
			UpdatedAt:      row.UpdatedAt,
			FailureReason:  stringFromText(row.FailureReason),
		}
	}
	return items, nil
}
