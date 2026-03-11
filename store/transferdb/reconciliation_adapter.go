package transferdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/core/reconciliation"
	"github.com/intellect4all/settla/domain"
)

// ReconciliationAdapter implements all reconciliation store interfaces against
// the Transfer DB. A single struct satisfies:
//   - reconciliation.TransferQuerier
//   - reconciliation.OutboxQuerier
//   - reconciliation.ProviderTxQuerier
//   - reconciliation.VolumeQuerier
//   - reconciliation.TenantLister
//   - reconciliation.SettlementFeeStore
//   - reconciliation.ReportStore
type ReconciliationAdapter struct {
	pool *pgxpool.Pool
	q    *Queries
}

// NewReconciliationAdapter creates a new ReconciliationAdapter backed by the given pool.
func NewReconciliationAdapter(pool *pgxpool.Pool, q *Queries) *ReconciliationAdapter {
	return &ReconciliationAdapter{pool: pool, q: q}
}

// Compile-time interface checks.
var (
	_ reconciliation.TransferQuerier    = (*ReconciliationAdapter)(nil)
	_ reconciliation.OutboxQuerier      = (*ReconciliationAdapter)(nil)
	_ reconciliation.ProviderTxQuerier  = (*ReconciliationAdapter)(nil)
	_ reconciliation.VolumeQuerier      = (*ReconciliationAdapter)(nil)
	_ reconciliation.TenantLister       = (*ReconciliationAdapter)(nil)
	_ reconciliation.TenantSlugResolver = (*ReconciliationAdapter)(nil)
	_ reconciliation.SettlementFeeStore = (*ReconciliationAdapter)(nil)
	_ reconciliation.ReportStore        = (*ReconciliationAdapter)(nil)
)

// CountTransfersInStatus returns the number of transfers in the given status
// whose updated_at is before olderThan.
func (a *ReconciliationAdapter) CountTransfersInStatus(ctx context.Context, status domain.TransferStatus, olderThan time.Time) (int, error) {
	const query = `SELECT COUNT(*) FROM transfers WHERE status = $1 AND updated_at < $2`
	var n int
	if err := a.pool.QueryRow(ctx, query, string(status), olderThan).Scan(&n); err != nil {
		return 0, fmt.Errorf("settla-reconciliation-store: counting transfers in status %s: %w", status, err)
	}
	return n, nil
}

// CountUnpublishedOlderThan returns the number of outbox entries that are
// unpublished and were created before olderThan.
func (a *ReconciliationAdapter) CountUnpublishedOlderThan(ctx context.Context, olderThan time.Time) (int, error) {
	const query = `SELECT COUNT(*) FROM outbox WHERE published = false AND created_at < $1`
	var n int
	if err := a.pool.QueryRow(ctx, query, olderThan).Scan(&n); err != nil {
		return 0, fmt.Errorf("settla-reconciliation-store: counting unpublished outbox entries: %w", err)
	}
	return n, nil
}

// CountDefaultPartitionRows returns the number of rows in the outbox_default
// partition, which should always be zero in normal operation.
func (a *ReconciliationAdapter) CountDefaultPartitionRows(ctx context.Context) (int, error) {
	const query = `SELECT COUNT(*) FROM outbox_default`
	var n int
	if err := a.pool.QueryRow(ctx, query).Scan(&n); err != nil {
		return 0, fmt.Errorf("settla-reconciliation-store: counting default partition rows: %w", err)
	}
	return n, nil
}

// CountPendingProviderTxOlderThan returns the number of provider transactions
// stuck in 'pending' status with created_at before olderThan.
func (a *ReconciliationAdapter) CountPendingProviderTxOlderThan(ctx context.Context, olderThan time.Time) (int, error) {
	const query = `SELECT COUNT(*) FROM provider_transactions WHERE status = 'pending' AND created_at < $1`
	var n int
	if err := a.pool.QueryRow(ctx, query, olderThan).Scan(&n); err != nil {
		return 0, fmt.Errorf("settla-reconciliation-store: counting pending provider txs: %w", err)
	}
	return n, nil
}

// GetDailyTransferCount returns the total number of transfers created on the
// given UTC date (00:00:00 to 23:59:59.999...).
func (a *ReconciliationAdapter) GetDailyTransferCount(ctx context.Context, date time.Time) (int, error) {
	const query = `SELECT COUNT(*) FROM transfers WHERE created_at >= $1 AND created_at < $2`
	start := date.UTC().Truncate(24 * time.Hour)
	end := start.Add(24 * time.Hour)
	var n int
	if err := a.pool.QueryRow(ctx, query, start, end).Scan(&n); err != nil {
		return 0, fmt.Errorf("settla-reconciliation-store: getting daily transfer count: %w", err)
	}
	return n, nil
}

// GetAverageDailyTransferCount returns the average number of transfers per day
// over the date range [startDate, endDate).
func (a *ReconciliationAdapter) GetAverageDailyTransferCount(ctx context.Context, startDate, endDate time.Time) (float64, error) {
	// Divide total count by number of calendar days in the window to get average.
	const query = `
		SELECT COALESCE(
			CAST(COUNT(*) AS float) / GREATEST(
				EXTRACT(EPOCH FROM ($2::timestamptz - $1::timestamptz)) / 86400,
				1
			),
			0
		)
		FROM transfers
		WHERE created_at >= $1 AND created_at < $2
	`
	var avg float64
	if err := a.pool.QueryRow(ctx, query, startDate, endDate).Scan(&avg); err != nil {
		return 0, fmt.Errorf("settla-reconciliation-store: getting average daily transfer count: %w", err)
	}
	return avg, nil
}

// GetTenantSlug returns the slug for a tenant by ID.
func (a *ReconciliationAdapter) GetTenantSlug(ctx context.Context, tenantID uuid.UUID) (string, error) {
	const query = `SELECT slug FROM tenants WHERE id = $1`
	var slug string
	if err := a.pool.QueryRow(ctx, query, tenantID).Scan(&slug); err != nil {
		return "", fmt.Errorf("settla-reconciliation-store: getting tenant slug for %s: %w", tenantID, err)
	}
	return slug, nil
}

// ListActiveTenantIDs returns the UUIDs of all tenants with status 'active'.
func (a *ReconciliationAdapter) ListActiveTenantIDs(ctx context.Context) ([]uuid.UUID, error) {
	const query = `SELECT id FROM tenants WHERE status = 'active'`
	rows, err := a.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("settla-reconciliation-store: listing active tenants: %w", err)
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("settla-reconciliation-store: scanning tenant id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GetLatestNetSettlement returns the most recently created net settlement across
// all tenants, or (nil, nil) if none exists.
func (a *ReconciliationAdapter) GetLatestNetSettlement(ctx context.Context) (*reconciliation.SettlementRecord, error) {
	const query = `
		SELECT id, tenant_id, period_start, period_end, total_fees_usd
		FROM net_settlements
		ORDER BY created_at DESC
		LIMIT 1
	`
	var (
		id, tenantID    uuid.UUID
		periodStart     time.Time
		periodEnd       time.Time
		totalFeesNumeric pgtype.Numeric
	)
	err := a.pool.QueryRow(ctx, query).Scan(&id, &tenantID, &periodStart, &periodEnd, &totalFeesNumeric)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("settla-reconciliation-store: fetching latest net settlement: %w", err)
	}
	return &reconciliation.SettlementRecord{
		ID:           id,
		TenantID:     tenantID,
		PeriodStart:  periodStart,
		PeriodEnd:    periodEnd,
		TotalFeesUSD: decimalFromNumeric(totalFeesNumeric),
	}, nil
}

// SumCompletedTransferFeesUSD sums FeeBreakdown.TotalFeeUSD from all COMPLETED
// transfers for the given tenant in [start, end). Returns decimal.Zero if no
// transfers match.
func (a *ReconciliationAdapter) SumCompletedTransferFeesUSD(ctx context.Context, tenantID uuid.UUID, start, end time.Time) (decimal.Decimal, error) {
	const query = `
		SELECT COALESCE(SUM((fees->>'TotalFeeUSD')::numeric), 0)
		FROM transfers
		WHERE tenant_id = $1
		  AND status = 'COMPLETED'
		  AND completed_at >= $2
		  AND completed_at < $3
	`
	var totalNumeric pgtype.Numeric
	err := a.pool.QueryRow(ctx, query, tenantID, start, end).Scan(&totalNumeric)
	if err != nil {
		return decimal.Zero, fmt.Errorf(
			"settla-reconciliation-store: summing transfer fees for tenant %s: %w",
			tenantID, err,
		)
	}
	return decimalFromNumeric(totalNumeric), nil
}

// CreateReconciliationReport persists a reconciliation.Report to the DB,
// mapping the domain report to the reconciliation_reports schema.
func (a *ReconciliationAdapter) CreateReconciliationReport(ctx context.Context, report *reconciliation.Report) error {
	checksRun := int32(len(report.Results))
	var checksPassed int32
	for _, r := range report.Results {
		if r.Status == "pass" {
			checksPassed++
		}
	}

	discrepanciesJSON, err := json.Marshal(report.Results)
	if err != nil {
		return fmt.Errorf("settla-reconciliation-store: marshalling discrepancies: %w", err)
	}

	_, err = a.q.CreateReconciliationReport(ctx, CreateReconciliationReportParams{
		JobName:       "settla-reconciler",
		RunAt:         report.RunAt,
		DurationMs:    0,
		ChecksRun:     checksRun,
		ChecksPassed:  checksPassed,
		Discrepancies: discrepanciesJSON,
		AutoCorrected: 0,
		NeedsReview:   !report.OverallPass,
	})
	if err != nil {
		return fmt.Errorf("settla-reconciliation-store: storing reconciliation report: %w", err)
	}
	return nil
}

// GetLatestReport returns the most recent reconciliation report, or (nil, nil)
// if none exists yet.
func (a *ReconciliationAdapter) GetLatestReport(ctx context.Context) (*reconciliation.Report, error) {
	rows, err := a.q.ListReconciliationReports(ctx, ListReconciliationReportsParams{Limit: 1, Offset: 0})
	if err != nil {
		return nil, fmt.Errorf("settla-reconciliation-store: fetching latest report: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	r := rows[0]
	var results []reconciliation.CheckResult
	_ = json.Unmarshal(r.Discrepancies, &results)
	return &reconciliation.Report{
		ID:          r.ID,
		RunAt:       r.RunAt,
		OverallPass: !r.NeedsReview,
		Results:     results,
	}, nil
}
