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
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/core/reconciliation"
	"github.com/intellect4all/settla/domain"
)

// ReconciliationAdapter implements all reconciliation store interfaces against
// the Transfer DB using SQLC-generated queries. A single struct satisfies:
//   - reconciliation.TransferQuerier
//   - reconciliation.OutboxQuerier
//   - reconciliation.ProviderTxQuerier
//   - reconciliation.VolumeQuerier
//   - reconciliation.TenantLister
//   - reconciliation.SettlementFeeStore
//   - reconciliation.ReportStore
type ReconciliationAdapter struct {
	q *Queries
}

// NewReconciliationAdapter creates a new ReconciliationAdapter backed by the given queries.
func NewReconciliationAdapter(q *Queries) *ReconciliationAdapter {
	return &ReconciliationAdapter{q: q}
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
	_ reconciliation.DepositQuerier     = (*ReconciliationAdapter)(nil)
	_ reconciliation.BankDepositQuerier = (*ReconciliationAdapter)(nil)
)

// CountTransfersInStatus returns the number of transfers in the given status
// whose updated_at is before olderThan.
func (a *ReconciliationAdapter) CountTransfersInStatus(ctx context.Context, status domain.TransferStatus, olderThan time.Time) (int, error) {
	n, err := a.q.CountTransfersInStatus(ctx, CountTransfersInStatusParams{
		Status:    TransferStatusEnum(status),
		UpdatedAt: olderThan,
	})
	if err != nil {
		return 0, fmt.Errorf("settla-reconciliation-store: counting transfers in status %s: %w", status, err)
	}
	return int(n), nil
}

// CountUnpublishedOlderThan returns the number of outbox entries that are
// unpublished and were created before olderThan.
func (a *ReconciliationAdapter) CountUnpublishedOlderThan(ctx context.Context, olderThan time.Time) (int, error) {
	n, err := a.q.CountUnpublishedOlderThan(ctx, olderThan)
	if err != nil {
		return 0, fmt.Errorf("settla-reconciliation-store: counting unpublished outbox entries: %w", err)
	}
	return int(n), nil
}

// CountDefaultPartitionRows returns the number of rows in the outbox_default
// partition, which should always be zero in normal operation.
func (a *ReconciliationAdapter) CountDefaultPartitionRows(ctx context.Context) (int, error) {
	n, err := a.q.CountDefaultPartitionRows(ctx)
	if err != nil {
		return 0, fmt.Errorf("settla-reconciliation-store: counting default partition rows: %w", err)
	}
	return int(n), nil
}

// CountPendingProviderTxOlderThan returns the number of provider transactions
// stuck in 'pending' status with created_at before olderThan.
func (a *ReconciliationAdapter) CountPendingProviderTxOlderThan(ctx context.Context, olderThan time.Time) (int, error) {
	n, err := a.q.CountPendingProviderTxOlderThan(ctx, olderThan)
	if err != nil {
		return 0, fmt.Errorf("settla-reconciliation-store: counting pending provider txs: %w", err)
	}
	return int(n), nil
}

// GetDailyTransferCount returns the total number of transfers created on the
// given UTC date (00:00:00 to 23:59:59.999...).
func (a *ReconciliationAdapter) GetDailyTransferCount(ctx context.Context, date time.Time) (int, error) {
	start := date.UTC().Truncate(24 * time.Hour)
	end := start.Add(24 * time.Hour)
	n, err := a.q.GetDailyTransferCount(ctx, GetDailyTransferCountParams{
		StartTime: start,
		EndTime:   end,
	})
	if err != nil {
		return 0, fmt.Errorf("settla-reconciliation-store: getting daily transfer count: %w", err)
	}
	return int(n), nil
}

// GetAverageDailyTransferCount returns the average number of transfers per day
// over the date range [startDate, endDate).
func (a *ReconciliationAdapter) GetAverageDailyTransferCount(ctx context.Context, startDate, endDate time.Time) (float64, error) {
	avg, err := a.q.GetAverageDailyTransferCount(ctx, GetAverageDailyTransferCountParams{
		StartDate: startDate,
		EndDate:   endDate,
	})
	if err != nil {
		return 0, fmt.Errorf("settla-reconciliation-store: getting average daily transfer count: %w", err)
	}
	return avg, nil
}

// GetTenantSlug returns the slug for a tenant by ID.
func (a *ReconciliationAdapter) GetTenantSlug(ctx context.Context, tenantID uuid.UUID) (string, error) {
	slug, err := a.q.GetTenantSlug(ctx, tenantID)
	if err != nil {
		return "", fmt.Errorf("settla-reconciliation-store: getting tenant slug for %s: %w", tenantID, err)
	}
	return slug, nil
}

// ListActiveTenantIDs returns the UUIDs of all tenants with status 'active'.
func (a *ReconciliationAdapter) ListActiveTenantIDs(ctx context.Context) ([]uuid.UUID, error) {
	ids, err := a.q.ListActiveTenantIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("settla-reconciliation-store: listing active tenants: %w", err)
	}
	return ids, nil
}

// GetLatestNetSettlement returns the most recently created net settlement across
// all tenants, or (nil, nil) if none exists.
func (a *ReconciliationAdapter) GetLatestNetSettlement(ctx context.Context) (*reconciliation.SettlementRecord, error) {
	row, err := a.q.GetLatestNetSettlement(ctx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("settla-reconciliation-store: fetching latest net settlement: %w", err)
	}
	return &reconciliation.SettlementRecord{
		ID:           row.ID,
		TenantID:     row.TenantID,
		PeriodStart:  row.PeriodStart,
		PeriodEnd:    row.PeriodEnd,
		TotalFeesUSD: decimalFromNumeric(row.TotalFeesUsd),
	}, nil
}

// SumCompletedTransferFeesUSD sums FeeBreakdown.TotalFeeUSD from all COMPLETED
// transfers for the given tenant in [start, end). Returns decimal.Zero if no
// transfers match.
func (a *ReconciliationAdapter) SumCompletedTransferFeesUSD(ctx context.Context, tenantID uuid.UUID, start, end time.Time) (decimal.Decimal, error) {
	totalNumeric, err := a.q.SumCompletedTransferFeesUSD(ctx, SumCompletedTransferFeesUSDParams{
		TenantID:  tenantID,
		StartTime: pgtype.Timestamptz{Time: start, Valid: true},
		EndTime:   pgtype.Timestamptz{Time: end, Valid: true},
	})
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

// ── Deposit reconciliation queries ──────────────────────────────────────────

// CountStuckDepositSessions returns the number of deposit sessions in a
// non-terminal status that have not been updated since olderThan.
func (a *ReconciliationAdapter) CountStuckDepositSessions(ctx context.Context, olderThan time.Time) (int, error) {
	n, err := a.q.CountStuckDepositSessions(ctx, olderThan)
	if err != nil {
		return 0, fmt.Errorf("settla-reconciliation-store: counting stuck deposit sessions: %w", err)
	}
	return int(n), nil
}

// CountStaleBlockCheckpoints returns the number of chain monitors whose
// checkpoint has not been updated since olderThan.
func (a *ReconciliationAdapter) CountStaleBlockCheckpoints(ctx context.Context, olderThan time.Time) (int, error) {
	n, err := a.q.CountStaleBlockCheckpoints(ctx, olderThan)
	if err != nil {
		return 0, fmt.Errorf("settla-reconciliation-store: counting stale block checkpoints: %w", err)
	}
	return int(n), nil
}

// CountAvailablePoolAddressesAll returns the total number of undispensed
// addresses in the pool across all tenants and chains.
func (a *ReconciliationAdapter) CountAvailablePoolAddressesAll(ctx context.Context) (int, error) {
	n, err := a.q.CountAvailablePoolAddressesAll(ctx)
	if err != nil {
		return 0, fmt.Errorf("settla-reconciliation-store: counting available pool addresses: %w", err)
	}
	return int(n), nil
}

// CountDepositTxAmountMismatches returns the number of sessions in CONFIRMED
// or later states where received_amount does not match the sum of confirmed
// transaction amounts.
func (a *ReconciliationAdapter) CountDepositTxAmountMismatches(ctx context.Context) (int, error) {
	n, err := a.q.CountDepositTxAmountMismatches(ctx)
	if err != nil {
		return 0, fmt.Errorf("settla-reconciliation-store: counting deposit tx-amount mismatches: %w", err)
	}
	return int(n), nil
}

// ── Bank deposit reconciliation queries ─────────────────────────────────────

// CountStuckBankDepositSessions returns the number of bank deposit sessions in
// PENDING_PAYMENT status that have not been updated since olderThan.
func (a *ReconciliationAdapter) CountStuckBankDepositSessions(ctx context.Context, olderThan time.Time) (int, error) {
	n, err := a.q.CountStuckBankDepositSessions(ctx, olderThan)
	if err != nil {
		return 0, fmt.Errorf("settla-reconciliation-store: counting stuck bank deposit sessions: %w", err)
	}
	return int(n), nil
}

// CountStuckBankDepositCrediting returns the number of bank deposit sessions in
// CREDITING status that have not advanced since olderThan.
func (a *ReconciliationAdapter) CountStuckBankDepositCrediting(ctx context.Context, olderThan time.Time) (int, error) {
	n, err := a.q.CountStuckBankDepositCrediting(ctx, olderThan)
	if err != nil {
		return 0, fmt.Errorf("settla-reconciliation-store: counting stuck bank deposit crediting: %w", err)
	}
	return int(n), nil
}

// CountOrphanedVirtualAccounts returns the number of virtual accounts in the
// pool that are marked as unavailable but whose linked session has reached a
// terminal state (EXPIRED, FAILED, CANCELLED, SETTLED, HELD). These accounts
// should have been recycled by the IntentRecycleVirtualAccount worker.
func (a *ReconciliationAdapter) CountOrphanedVirtualAccounts(ctx context.Context) (int, error) {
	n, err := a.q.CountOrphanedVirtualAccounts(ctx)
	if err != nil {
		return 0, fmt.Errorf("settla-reconciliation-store: counting orphaned virtual accounts: %w", err)
	}
	return int(n), nil
}
