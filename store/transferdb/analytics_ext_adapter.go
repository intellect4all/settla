package transferdb

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/intellect4all/settla/domain"
)

// ExtendedAnalyticsStore provides the new analytics data access methods.
type ExtendedAnalyticsStore interface {
	GetFeeBreakdown(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.FeeBreakdownEntry, error)
	GetProviderPerformance(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.ProviderPerformance, error)
	GetCryptoDepositAnalytics(ctx context.Context, tenantID uuid.UUID, from, to time.Time) (*domain.DepositAnalytics, error)
	GetBankDepositAnalytics(ctx context.Context, tenantID uuid.UUID, from, to time.Time) (*domain.DepositAnalytics, error)
	GetReconciliationSummary(ctx context.Context, from, to time.Time) (*domain.ReconciliationSummary, error)
	GetDailySnapshots(ctx context.Context, tenantID uuid.UUID, metricType string, fromDate, toDate time.Time) ([]domain.DailySnapshot, error)
}

// ExportStore provides export job CRUD operations.
type ExportStore interface {
	CreateExportJob(ctx context.Context, tenantID uuid.UUID, exportType string, parameters map[string]any) (*domain.ExportJob, error)
	GetExportJob(ctx context.Context, id, tenantID uuid.UUID) (*domain.ExportJob, error)
	ListExportJobs(ctx context.Context, tenantID uuid.UUID, limit int32) ([]domain.ExportJob, error)
	ListPendingExportJobs(ctx context.Context, batchSize int32) ([]domain.ExportJob, error)
	UpdateExportJobStatus(ctx context.Context, id uuid.UUID, status, filePath, downloadURL string, downloadExpiresAt *time.Time, rowCount int64, errorMessage string, completedAt *time.Time) error
}

// SnapshotStore provides snapshot upsert operations for the nightly job.
type SnapshotStore interface {
	UpsertDailySnapshot(ctx context.Context, snap domain.DailySnapshot) error
	ListActiveTenantIDs(ctx context.Context) ([]uuid.UUID, error)
}

// ExtendedAnalyticsAdapter implements ExtendedAnalyticsStore.
type ExtendedAnalyticsAdapter struct {
	q    *Queries
	pool *pgxpool.Pool
}

// NewExtendedAnalyticsAdapter creates a new ExtendedAnalyticsAdapter.
func NewExtendedAnalyticsAdapter(q *Queries, pool *pgxpool.Pool) *ExtendedAnalyticsAdapter {
	return &ExtendedAnalyticsAdapter{q: q, pool: pool}
}

func (a *ExtendedAnalyticsAdapter) GetFeeBreakdown(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.FeeBreakdownEntry, error) {
	rows, err := a.q.GetFeeBreakdown(ctx, GetFeeBreakdownParams{
		TenantID: tenantID,
		FromTime: from,
		ToTime:   to,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-analytics: getting fee breakdown: %w", err)
	}

	result := make([]domain.FeeBreakdownEntry, len(rows))
	for i, row := range rows {
		result[i] = domain.FeeBreakdownEntry{
			SourceCurrency: row.SourceCurrency,
			DestCurrency:   row.DestCurrency,
			TransferCount:  row.TransferCount,
			VolumeUSD:      decimalFromNumeric(row.VolumeUsd),
			OnRampFeesUSD:  decimalFromNumeric(row.OnRampFeesUsd),
			OffRampFeesUSD: decimalFromNumeric(row.OffRampFeesUsd),
			NetworkFeesUSD: decimalFromNumeric(row.NetworkFeesUsd),
			TotalFeesUSD:   decimalFromNumeric(row.TotalFeesUsd),
		}
	}
	return result, nil
}

func (a *ExtendedAnalyticsAdapter) GetProviderPerformance(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.ProviderPerformance, error) {
	rows, err := a.q.GetProviderPerformance(ctx, GetProviderPerformanceParams{
		TenantID: tenantID,
		FromTime: from,
		ToTime:   to,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-analytics: getting provider performance: %w", err)
	}

	result := make([]domain.ProviderPerformance, len(rows))
	for i, row := range rows {
		result[i] = domain.ProviderPerformance{
			Provider:         row.Provider,
			SourceCurrency:   row.SourceCurrency,
			DestCurrency:     row.DestCurrency,
			TransactionCount: row.TransactionCount,
			Completed:        row.Completed,
			Failed:           row.Failed,
			SuccessRate:      decimalFromNumeric(row.SuccessRate),
			AvgSettlementMs:  row.AvgSettlementMs,
			TotalVolume:      decimalFromNumeric(row.TotalVolume),
		}
	}
	return result, nil
}

func (a *ExtendedAnalyticsAdapter) GetCryptoDepositAnalytics(ctx context.Context, tenantID uuid.UUID, from, to time.Time) (*domain.DepositAnalytics, error) {
	row, err := a.q.GetCryptoDepositAnalytics(ctx, GetCryptoDepositAnalyticsParams{
		TenantID: tenantID,
		FromTime: from,
		ToTime:   to,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-analytics: getting crypto deposit analytics: %w", err)
	}

	return &domain.DepositAnalytics{
		TotalSessions:     row.TotalSessions,
		CompletedSessions: row.CompletedSessions,
		ExpiredSessions:   row.ExpiredSessions,
		FailedSessions:    row.FailedSessions,
		ConversionRate:    decimalFromNumeric(row.ConversionRate),
		TotalReceived:     decimalFromNumeric(row.TotalReceived),
		TotalFees:         decimalFromNumeric(row.TotalFees),
		TotalNet:          decimalFromNumeric(row.TotalNet),
	}, nil
}

func (a *ExtendedAnalyticsAdapter) GetBankDepositAnalytics(ctx context.Context, tenantID uuid.UUID, from, to time.Time) (*domain.DepositAnalytics, error) {
	row, err := a.q.GetBankDepositAnalytics(ctx, GetBankDepositAnalyticsParams{
		TenantID: tenantID,
		FromTime: from,
		ToTime:   to,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-analytics: getting bank deposit analytics: %w", err)
	}

	return &domain.DepositAnalytics{
		TotalSessions:     row.TotalSessions,
		CompletedSessions: row.CompletedSessions,
		ExpiredSessions:   row.ExpiredSessions,
		FailedSessions:    row.FailedSessions,
		ConversionRate:    decimalFromNumeric(row.ConversionRate),
		TotalReceived:     decimalFromNumeric(row.TotalReceived),
		TotalFees:         decimalFromNumeric(row.TotalFees),
		TotalNet:          decimalFromNumeric(row.TotalNet),
	}, nil
}

func (a *ExtendedAnalyticsAdapter) GetReconciliationSummary(ctx context.Context, from, to time.Time) (*domain.ReconciliationSummary, error) {
	row, err := a.q.GetReconciliationSummary(ctx, GetReconciliationSummaryParams{
		FromTime: from,
		ToTime:   to,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-analytics: getting reconciliation summary: %w", err)
	}

	summary := &domain.ReconciliationSummary{
		TotalRuns:        row.TotalRuns,
		ChecksPassed:     row.ChecksPassed,
		ChecksFailed:     row.ChecksFailed,
		PassRate:         decimalFromNumeric(row.PassRate),
		NeedsReviewCount: row.NeedsReviewCount,
	}
	// MAX(run_at) returns interface{} — extract time.Time if non-nil
	if t, ok := row.LastRunAt.(time.Time); ok {
		summary.LastRunAt = &t
	}
	return summary, nil
}

func (a *ExtendedAnalyticsAdapter) GetDailySnapshots(ctx context.Context, tenantID uuid.UUID, metricType string, fromDate, toDate time.Time) ([]domain.DailySnapshot, error) {
	rows, err := a.q.GetDailySnapshots(ctx, GetDailySnapshotsParams{
		TenantID:   tenantID,
		MetricType: metricType,
		FromDate:   pgtype.Date{Time: fromDate, Valid: true},
		ToDate:     pgtype.Date{Time: toDate, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("settla-analytics: getting daily snapshots: %w", err)
	}

	result := make([]domain.DailySnapshot, len(rows))
	for i, row := range rows {
		result[i] = domain.DailySnapshot{
			ID:             row.ID,
			TenantID:       row.TenantID,
			SnapshotDate:   row.SnapshotDate.Time,
			MetricType:     row.MetricType,
			SourceCurrency: row.SourceCurrency,
			DestCurrency:   row.DestCurrency,
			Provider:       row.Provider,
			TransferCount:  row.TransferCount,
			CompletedCount: row.CompletedCount,
			FailedCount:    row.FailedCount,
			VolumeUSD:      decimalFromNumeric(row.VolumeUsd),
			FeesUSD:        decimalFromNumeric(row.FeesUsd),
			OnRampFeesUSD:  decimalFromNumeric(row.OnRampFeesUsd),
			OffRampFeesUSD: decimalFromNumeric(row.OffRampFeesUsd),
			NetworkFeesUSD: decimalFromNumeric(row.NetworkFeesUsd),
			AvgLatencyMs:   row.AvgLatencyMs,
			P50LatencyMs:   row.P50LatencyMs,
			P90LatencyMs:   row.P90LatencyMs,
			P95LatencyMs:   row.P95LatencyMs,
			SuccessRate:    decimalFromNumeric(row.SuccessRate),
			CreatedAt:      row.CreatedAt,
		}
	}
	return result, nil
}

// ExportAdapter implements ExportStore.
type ExportAdapter struct {
	q    *Queries
	pool *pgxpool.Pool
}

// NewExportAdapter creates a new ExportAdapter.
func NewExportAdapter(q *Queries, pool *pgxpool.Pool) *ExportAdapter {
	return &ExportAdapter{q: q, pool: pool}
}

func (a *ExportAdapter) CreateExportJob(ctx context.Context, tenantID uuid.UUID, exportType string, parameters map[string]any) (*domain.ExportJob, error) {
	paramsJSON, err := json.Marshal(parameters)
	if err != nil {
		return nil, fmt.Errorf("settla-analytics: marshaling export parameters: %w", err)
	}

	row, err := a.q.CreateExportJob(ctx, CreateExportJobParams{
		TenantID:   tenantID,
		ExportType: exportType,
		Parameters: paramsJSON,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-analytics: creating export job: %w", err)
	}

	return mapExportJobRow(row), nil
}

func (a *ExportAdapter) GetExportJob(ctx context.Context, id, tenantID uuid.UUID) (*domain.ExportJob, error) {
	row, err := a.q.GetExportJob(ctx, GetExportJobParams{
		ID:       id,
		TenantID: tenantID,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-analytics: getting export job: %w", err)
	}

	return mapExportJobRow(row), nil
}

func (a *ExportAdapter) ListExportJobs(ctx context.Context, tenantID uuid.UUID, limit int32) ([]domain.ExportJob, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	rows, err := a.q.ListExportJobs(ctx, ListExportJobsParams{
		TenantID: tenantID,
		PageSize: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-analytics: listing export jobs: %w", err)
	}

	result := make([]domain.ExportJob, len(rows))
	for i, row := range rows {
		result[i] = *mapExportJobRow(row)
	}
	return result, nil
}

func (a *ExportAdapter) ListPendingExportJobs(ctx context.Context, batchSize int32) ([]domain.ExportJob, error) {
	rows, err := a.q.ListPendingExportJobs(ctx, batchSize)
	if err != nil {
		return nil, fmt.Errorf("settla-analytics: listing pending export jobs: %w", err)
	}

	result := make([]domain.ExportJob, len(rows))
	for i, row := range rows {
		result[i] = *mapExportJobRow(row)
	}
	return result, nil
}

func (a *ExportAdapter) UpdateExportJobStatus(ctx context.Context, id uuid.UUID, status, filePath, downloadURL string, downloadExpiresAt *time.Time, rowCount int64, errorMessage string, completedAt *time.Time) error {
	params := UpdateExportJobStatusParams{
		ID:           id,
		Status:       status,
		RowCount:     rowCount,
	}

	if filePath != "" {
		params.FilePath = pgtype.Text{String: filePath, Valid: true}
	}
	if downloadURL != "" {
		params.DownloadUrl = pgtype.Text{String: downloadURL, Valid: true}
	}
	if downloadExpiresAt != nil {
		params.DownloadExpiresAt = pgtype.Timestamptz{Time: *downloadExpiresAt, Valid: true}
	}
	if errorMessage != "" {
		params.ErrorMessage = pgtype.Text{String: errorMessage, Valid: true}
	}
	if completedAt != nil {
		params.CompletedAt = pgtype.Timestamptz{Time: *completedAt, Valid: true}
	}

	return a.q.UpdateExportJobStatus(ctx, params)
}

// SnapshotAdapter implements SnapshotStore.
type SnapshotAdapter struct {
	q    *Queries
	pool *pgxpool.Pool
}

// NewSnapshotAdapter creates a new SnapshotAdapter.
func NewSnapshotAdapter(q *Queries, pool *pgxpool.Pool) *SnapshotAdapter {
	return &SnapshotAdapter{q: q, pool: pool}
}

func (a *SnapshotAdapter) UpsertDailySnapshot(ctx context.Context, snap domain.DailySnapshot) error {
	return a.q.UpsertDailySnapshot(ctx, UpsertDailySnapshotParams{
		TenantID:       snap.TenantID,
		SnapshotDate:   pgtype.Date{Time: snap.SnapshotDate, Valid: true},
		MetricType:     snap.MetricType,
		SourceCurrency: snap.SourceCurrency,
		DestCurrency:   snap.DestCurrency,
		Provider:       snap.Provider,
		TransferCount:  snap.TransferCount,
		CompletedCount: snap.CompletedCount,
		FailedCount:    snap.FailedCount,
		VolumeUsd:      numericFromDecimal(snap.VolumeUSD),
		FeesUsd:        numericFromDecimal(snap.FeesUSD),
		OnRampFeesUsd:  numericFromDecimal(snap.OnRampFeesUSD),
		OffRampFeesUsd: numericFromDecimal(snap.OffRampFeesUSD),
		NetworkFeesUsd: numericFromDecimal(snap.NetworkFeesUSD),
		AvgLatencyMs:   snap.AvgLatencyMs,
		P50LatencyMs:   snap.P50LatencyMs,
		P90LatencyMs:   snap.P90LatencyMs,
		P95LatencyMs:   snap.P95LatencyMs,
		SuccessRate:    numericFromDecimal(snap.SuccessRate),
	})
}

func (a *SnapshotAdapter) ListActiveTenantIDs(ctx context.Context) ([]uuid.UUID, error) {
	return a.q.ListActiveTenantIDs(ctx)
}

// mapExportJobRow converts the SQLC-generated AnalyticsExportJob model to domain.ExportJob.
func mapExportJobRow(r AnalyticsExportJob) *domain.ExportJob {
	job := &domain.ExportJob{
		ID:         r.ID,
		TenantID:   r.TenantID,
		Status:     r.Status,
		ExportType: r.ExportType,
		RowCount:   r.RowCount,
		CreatedAt:  r.CreatedAt,
	}

	if len(r.Parameters) > 0 {
		_ = json.Unmarshal(r.Parameters, &job.Parameters)
	}
	if r.FilePath.Valid {
		job.FilePath = r.FilePath.String
	}
	if r.DownloadUrl.Valid {
		job.DownloadURL = r.DownloadUrl.String
	}
	if r.DownloadExpiresAt.Valid {
		t := r.DownloadExpiresAt.Time
		job.DownloadExpiresAt = &t
	}
	if r.ErrorMessage.Valid {
		job.ErrorMessage = r.ErrorMessage.String
	}
	if r.CompletedAt.Valid {
		t := r.CompletedAt.Time
		job.CompletedAt = &t
	}
	return job
}
