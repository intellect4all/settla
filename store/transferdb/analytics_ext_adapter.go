package transferdb

import (
	"context"
	"encoding/json"
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
	ForEachActiveTenant(ctx context.Context, batchSize int32, fn func(ids []uuid.UUID) error) error
}

// ExtendedAnalyticsAdapter implements ExtendedAnalyticsStore.
type ExtendedAnalyticsAdapter struct {
	q          *Queries
	pool       *pgxpool.Pool // owner pool for system-level queries
	appPool    *pgxpool.Pool // optional: RLS-enforced pool
	rlsEnabled bool          // true when appPool is configured
}

// NewExtendedAnalyticsAdapter creates a new ExtendedAnalyticsAdapter.
func NewExtendedAnalyticsAdapter(q *Queries, pool *pgxpool.Pool) *ExtendedAnalyticsAdapter {
	a := &ExtendedAnalyticsAdapter{q: q, pool: pool}
	a.rlsEnabled = (a.appPool != nil)
	if !a.rlsEnabled {
		slog.Warn("settla-store: ExtendedAnalyticsAdapter RLS pool not configured, tenant isolation relies on application-layer filters only")
	}
	return a
}

// WithExtAnalyticsAppPool configures the RLS-enforced pool for tenant-scoped operations.
func (a *ExtendedAnalyticsAdapter) WithExtAnalyticsAppPool(pool *pgxpool.Pool) *ExtendedAnalyticsAdapter {
	a.appPool = pool
	a.rlsEnabled = (pool != nil)
	return a
}

func (a *ExtendedAnalyticsAdapter) GetFeeBreakdown(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.FeeBreakdownEntry, error) {
	params := GetFeeBreakdownParams{TenantID: tenantID, FromTime: from, ToTime: to}
	toResult := func(rows []GetFeeBreakdownRow) []domain.FeeBreakdownEntry {
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
		return result
	}

	if a.appPool != nil {
		var result []domain.FeeBreakdownEntry
		err := rls.WithTenantReadTx(ctx, a.appPool, tenantID, func(tx pgx.Tx) error {
			rows, err := a.q.WithTx(tx).GetFeeBreakdown(ctx, params)
			if err != nil {
				return err
			}
			result = toResult(rows)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("settla-analytics: getting fee breakdown: %w", err)
		}
		return result, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "GetFeeBreakdown", "tenant_id", tenantID)
	rows, err := a.q.GetFeeBreakdown(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("settla-analytics: getting fee breakdown: %w", err)
	}
	return toResult(rows), nil
}

func (a *ExtendedAnalyticsAdapter) GetProviderPerformance(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.ProviderPerformance, error) {
	params := GetProviderPerformanceParams{TenantID: tenantID, FromTime: from, ToTime: to}
	toResult := func(rows []GetProviderPerformanceRow) []domain.ProviderPerformance {
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
		return result
	}

	if a.appPool != nil {
		var result []domain.ProviderPerformance
		err := rls.WithTenantReadTx(ctx, a.appPool, tenantID, func(tx pgx.Tx) error {
			rows, err := a.q.WithTx(tx).GetProviderPerformance(ctx, params)
			if err != nil {
				return err
			}
			result = toResult(rows)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("settla-analytics: getting provider performance: %w", err)
		}
		return result, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "GetProviderPerformance", "tenant_id", tenantID)
	rows, err := a.q.GetProviderPerformance(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("settla-analytics: getting provider performance: %w", err)
	}
	return toResult(rows), nil
}

func (a *ExtendedAnalyticsAdapter) GetCryptoDepositAnalytics(ctx context.Context, tenantID uuid.UUID, from, to time.Time) (*domain.DepositAnalytics, error) {
	params := GetCryptoDepositAnalyticsParams{TenantID: tenantID, FromTime: from, ToTime: to}

	if a.appPool != nil {
		var result *domain.DepositAnalytics
		err := rls.WithTenantReadTx(ctx, a.appPool, tenantID, func(tx pgx.Tx) error {
			row, err := a.q.WithTx(tx).GetCryptoDepositAnalytics(ctx, params)
			if err != nil {
				return err
			}
			result = depositAnalyticsFromCryptoRow(row)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("settla-analytics: getting crypto deposit analytics: %w", err)
		}
		return result, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "GetCryptoDepositAnalytics", "tenant_id", tenantID)
	row, err := a.q.GetCryptoDepositAnalytics(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("settla-analytics: getting crypto deposit analytics: %w", err)
	}
	return depositAnalyticsFromCryptoRow(row), nil
}

func depositAnalyticsFromCryptoRow(row GetCryptoDepositAnalyticsRow) *domain.DepositAnalytics {
	return &domain.DepositAnalytics{
		TotalSessions:     row.TotalSessions,
		CompletedSessions: row.CompletedSessions,
		ExpiredSessions:   row.ExpiredSessions,
		FailedSessions:    row.FailedSessions,
		ConversionRate:    decimalFromNumeric(row.ConversionRate),
		TotalReceived:     decimalFromNumeric(row.TotalReceived),
		TotalFees:         decimalFromNumeric(row.TotalFees),
		TotalNet:          decimalFromNumeric(row.TotalNet),
	}
}

func (a *ExtendedAnalyticsAdapter) GetBankDepositAnalytics(ctx context.Context, tenantID uuid.UUID, from, to time.Time) (*domain.DepositAnalytics, error) {
	params := GetBankDepositAnalyticsParams{TenantID: tenantID, FromTime: from, ToTime: to}

	if a.appPool != nil {
		var result *domain.DepositAnalytics
		err := rls.WithTenantReadTx(ctx, a.appPool, tenantID, func(tx pgx.Tx) error {
			row, err := a.q.WithTx(tx).GetBankDepositAnalytics(ctx, params)
			if err != nil {
				return err
			}
			result = depositAnalyticsFromBankRow(row)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("settla-analytics: getting bank deposit analytics: %w", err)
		}
		return result, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "GetBankDepositAnalytics", "tenant_id", tenantID)
	row, err := a.q.GetBankDepositAnalytics(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("settla-analytics: getting bank deposit analytics: %w", err)
	}
	return depositAnalyticsFromBankRow(row), nil
}

func depositAnalyticsFromBankRow(row GetBankDepositAnalyticsRow) *domain.DepositAnalytics {
	return &domain.DepositAnalytics{
		TotalSessions:     row.TotalSessions,
		CompletedSessions: row.CompletedSessions,
		ExpiredSessions:   row.ExpiredSessions,
		FailedSessions:    row.FailedSessions,
		ConversionRate:    decimalFromNumeric(row.ConversionRate),
		TotalReceived:     decimalFromNumeric(row.TotalReceived),
		TotalFees:         decimalFromNumeric(row.TotalFees),
		TotalNet:          decimalFromNumeric(row.TotalNet),
	}
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
	params := GetDailySnapshotsParams{
		TenantID:   tenantID,
		MetricType: metricType,
		FromDate:   pgtype.Date{Time: fromDate, Valid: true},
		ToDate:     pgtype.Date{Time: toDate, Valid: true},
	}

	if a.appPool != nil {
		var result []domain.DailySnapshot
		err := rls.WithTenantReadTx(ctx, a.appPool, tenantID, func(tx pgx.Tx) error {
			rows, err := a.q.WithTx(tx).GetDailySnapshots(ctx, params)
			if err != nil {
				return err
			}
			result = dailySnapshotsFromRows(rows)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("settla-analytics: getting daily snapshots: %w", err)
		}
		return result, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "GetDailySnapshots", "tenant_id", tenantID)
	rows, err := a.q.GetDailySnapshots(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("settla-analytics: getting daily snapshots: %w", err)
	}
	return dailySnapshotsFromRows(rows), nil
}

func dailySnapshotsFromRows(rows []AnalyticsDailySnapshot) []domain.DailySnapshot {
	result := make([]domain.DailySnapshot, len(rows))
	for i, row := range rows {
		result[i] = domain.DailySnapshot{
			ID:             row.ID,
			TenantID:       row.TenantID,
			SnapshotDate:   row.SnapshotDate.Time,
			MetricType:     row.MetricType,
			SourceCurrency: domain.Currency(row.SourceCurrency),
			DestCurrency:   domain.Currency(row.DestCurrency),
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
	return result
}

// ExportAdapter implements ExportStore.
type ExportAdapter struct {
	q          *Queries
	pool       *pgxpool.Pool // owner pool for system-level queries
	appPool    *pgxpool.Pool // optional: RLS-enforced pool
	rlsEnabled bool          // true when appPool is configured
}

// NewExportAdapter creates a new ExportAdapter.
func NewExportAdapter(q *Queries, pool *pgxpool.Pool) *ExportAdapter {
	a := &ExportAdapter{q: q, pool: pool}
	a.rlsEnabled = (a.appPool != nil)
	if !a.rlsEnabled {
		slog.Warn("settla-store: ExportAdapter RLS pool not configured, tenant isolation relies on application-layer filters only")
	}
	return a
}

// WithExportAppPool configures the RLS-enforced pool for tenant-scoped operations.
func (a *ExportAdapter) WithExportAppPool(pool *pgxpool.Pool) *ExportAdapter {
	a.appPool = pool
	a.rlsEnabled = (pool != nil)
	return a
}

func (a *ExportAdapter) CreateExportJob(ctx context.Context, tenantID uuid.UUID, exportType string, parameters map[string]any) (*domain.ExportJob, error) {
	paramsJSON, err := json.Marshal(parameters)
	if err != nil {
		return nil, fmt.Errorf("settla-analytics: marshaling export parameters: %w", err)
	}

	params := CreateExportJobParams{
		TenantID:   tenantID,
		ExportType: exportType,
		Parameters: paramsJSON,
	}

	if a.appPool != nil {
		var result *domain.ExportJob
		err := rls.WithTenantTx(ctx, a.appPool, tenantID, func(tx pgx.Tx) error {
			row, err := a.q.WithTx(tx).CreateExportJob(ctx, params)
			if err != nil {
				return err
			}
			result = mapExportJobRow(row)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("settla-analytics: creating export job: %w", err)
		}
		return result, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "CreateExportJob", "tenant_id", tenantID)
	row, err := a.q.CreateExportJob(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("settla-analytics: creating export job: %w", err)
	}
	return mapExportJobRow(row), nil
}

func (a *ExportAdapter) GetExportJob(ctx context.Context, id, tenantID uuid.UUID) (*domain.ExportJob, error) {
	params := GetExportJobParams{ID: id, TenantID: tenantID}

	if a.appPool != nil {
		var result *domain.ExportJob
		err := rls.WithTenantReadTx(ctx, a.appPool, tenantID, func(tx pgx.Tx) error {
			row, err := a.q.WithTx(tx).GetExportJob(ctx, params)
			if err != nil {
				return err
			}
			result = mapExportJobRow(row)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("settla-analytics: getting export job: %w", err)
		}
		return result, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "GetExportJob", "tenant_id", tenantID)
	row, err := a.q.GetExportJob(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("settla-analytics: getting export job: %w", err)
	}
	return mapExportJobRow(row), nil
}

func (a *ExportAdapter) ListExportJobs(ctx context.Context, tenantID uuid.UUID, limit int32) ([]domain.ExportJob, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	params := ListExportJobsParams{TenantID: tenantID, PageSize: limit}

	if a.appPool != nil {
		var result []domain.ExportJob
		err := rls.WithTenantReadTx(ctx, a.appPool, tenantID, func(tx pgx.Tx) error {
			rows, err := a.q.WithTx(tx).ListExportJobs(ctx, params)
			if err != nil {
				return err
			}
			result = make([]domain.ExportJob, len(rows))
			for i, row := range rows {
				result[i] = *mapExportJobRow(row)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("settla-analytics: listing export jobs: %w", err)
		}
		return result, nil
	}

	slog.Warn("settla-store: RLS bypassed", "method", "ListExportJobs", "tenant_id", tenantID)
	rows, err := a.q.ListExportJobs(ctx, params)
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
	q             *Queries
	pool          *pgxpool.Pool
	tenantForEach func(ctx context.Context, batchSize int32, fn func(ids []uuid.UUID) error) error
}

// NewSnapshotAdapter creates a new SnapshotAdapter.
func NewSnapshotAdapter(q *Queries, pool *pgxpool.Pool) *SnapshotAdapter {
	a := &SnapshotAdapter{q: q, pool: pool}
	// Default: paginated Postgres fallback
	a.tenantForEach = func(ctx context.Context, batchSize int32, fn func(ids []uuid.UUID) error) error {
		fetcher := func(ctx context.Context, limit, offset int32) ([]uuid.UUID, error) {
			return q.ListActiveTenantIDsPaginated(ctx, ListActiveTenantIDsPaginatedParams{
				Limit: limit, Offset: offset,
			})
		}
		return domain.ForEachTenantBatch(ctx, fetcher, batchSize, fn)
	}
	return a
}

// WithTenantForEach overrides the default Postgres-based tenant iteration
// with a Redis-backed TenantIndex or other implementation.
func (a *SnapshotAdapter) WithTenantForEach(fn func(ctx context.Context, batchSize int32, fnInner func(ids []uuid.UUID) error) error) {
	a.tenantForEach = fn
}

func (a *SnapshotAdapter) UpsertDailySnapshot(ctx context.Context, snap domain.DailySnapshot) error {
	return a.q.UpsertDailySnapshot(ctx, UpsertDailySnapshotParams{
		TenantID:       snap.TenantID,
		SnapshotDate:   pgtype.Date{Time: snap.SnapshotDate, Valid: true},
		MetricType:     snap.MetricType,
		SourceCurrency: string(snap.SourceCurrency),
		DestCurrency:   string(snap.DestCurrency),
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

func (a *SnapshotAdapter) ForEachActiveTenant(ctx context.Context, batchSize int32, fn func(ids []uuid.UUID) error) error {
	return a.tenantForEach(ctx, batchSize, fn)
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
