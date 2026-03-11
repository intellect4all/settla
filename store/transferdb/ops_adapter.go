package transferdb

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OpsStore defines the cross-tenant ops queries needed by the dashboard.
type OpsStore interface {
	// ListManualReviews returns manual reviews. Pass a non-nil tenantID to
	// restrict to a single tenant (tenant-scoped view); nil returns all tenants (admin view).
	ListManualReviews(ctx context.Context, tenantID *uuid.UUID, status string) ([]OpsManualReview, error)
	ResolveManualReview(ctx context.Context, id uuid.UUID, newStatus, resolution, resolvedBy string) error
	GetLatestReconciliationReport(ctx context.Context) (*OpsReconciliationReport, error)
	// ListNetSettlements returns net settlements. Pass a non-nil tenantID to
	// restrict to a single tenant; nil returns all tenants (admin view).
	ListNetSettlements(ctx context.Context, tenantID *uuid.UUID) ([]OpsNetSettlement, error)
	MarkSettlementPaid(ctx context.Context, id uuid.UUID, paymentRef string) error
}

// OpsManualReview is a cross-tenant view of a manual review with transfer details.
type OpsManualReview struct {
	ID             uuid.UUID  `json:"id"`
	TransferID     uuid.UUID  `json:"transfer_id"`
	TenantID       uuid.UUID  `json:"tenant_id"`
	TenantName     string     `json:"tenant_name"`
	Status         string     `json:"status"`
	Reason         string     `json:"reason"`
	TransferStatus string     `json:"transfer_status"`
	SourceAmount   string     `json:"source_amount"`
	SourceCurrency string     `json:"source_currency"`
	DestCurrency   string     `json:"dest_currency"`
	FailureCode    string     `json:"failure_code,omitempty"`
	EscalatedAt    time.Time  `json:"escalated_at"`
	ReviewedAt     *time.Time `json:"reviewed_at,omitempty"`
	ReviewedBy     string     `json:"reviewed_by,omitempty"`
	Notes          string     `json:"notes,omitempty"`
}

// OpsReconciliationReport is a reconciliation report as needed by the ops dashboard.
type OpsReconciliationReport struct {
	ID            uuid.UUID `json:"id"`
	JobName       string    `json:"job_name"`
	RanAt         time.Time `json:"ran_at"`
	DurationMs    int32     `json:"duration_ms"`
	ChecksRun     int32     `json:"checks_run"`
	ChecksPassed  int32     `json:"checks_passed"`
	NeedsReview   bool      `json:"needs_review"`
	Discrepancies []byte    `json:"discrepancies"` // raw JSONB
}

// OpsNetSettlement is a cross-tenant view of a net settlement with tenant details.
type OpsNetSettlement struct {
	ID            uuid.UUID  `json:"id"`
	TenantID      uuid.UUID  `json:"tenant_id"`
	TenantName    string     `json:"tenant_name"`
	PeriodStart   time.Time  `json:"period_start"`
	PeriodEnd     time.Time  `json:"period_end"`
	Corridors     []byte     `json:"corridors"`
	NetByCurrency []byte     `json:"net_by_currency"`
	TotalFeesUsd  string     `json:"total_fees_usd"`
	Instructions  []byte     `json:"instructions"`
	Status        string     `json:"status"`
	DueDate       *time.Time `json:"due_date,omitempty"`
	SettledAt     *time.Time `json:"settled_at,omitempty"`
}

// Compile-time interface check.
var _ OpsStore = (*OpsStoreAdapter)(nil)

// OpsStoreAdapter implements OpsStore using raw pgx queries (cross-tenant, not
// restricted to a single tenant_id like the SQLC-generated queries).
type OpsStoreAdapter struct {
	pool *pgxpool.Pool
	q    *Queries
}

// NewOpsAdapter creates a new OpsStoreAdapter.
func NewOpsAdapter(pool *pgxpool.Pool, q *Queries) *OpsStoreAdapter {
	return &OpsStoreAdapter{pool: pool, q: q}
}

// ListManualReviews returns up to 100 manual reviews. When tenantID is non-nil,
// only reviews for that tenant are returned. When status is non-empty, only
// reviews with that status are returned. The two filters are combined with AND.
func (a *OpsStoreAdapter) ListManualReviews(ctx context.Context, tenantID *uuid.UUID, status string) ([]OpsManualReview, error) {
	const baseQuery = `
		SELECT
			mr.id,
			mr.transfer_id,
			mr.tenant_id,
			COALESCE(t.name, '') AS tenant_name,
			mr.status,
			mr.transfer_status,
			mr.attempted_recoveries,
			COALESCE(tr.source_amount, 0) AS source_amount,
			COALESCE(tr.source_currency, '') AS source_currency,
			COALESCE(tr.dest_currency, '') AS dest_currency,
			COALESCE(tr.failure_code, '') AS failure_code,
			mr.created_at,
			mr.resolved_at,
			COALESCE(mr.resolved_by, '') AS resolved_by,
			COALESCE(mr.resolution, '') AS resolution
		FROM manual_reviews mr
		LEFT JOIN tenants t ON t.id = mr.tenant_id
		LEFT JOIN transfers tr ON tr.id = mr.transfer_id
	`

	var rows interface{ Next() bool; Scan(...any) error; Close(); Err() error }
	var err error

	switch {
	case tenantID != nil && status != "":
		r, qErr := a.pool.Query(ctx, baseQuery+` WHERE mr.tenant_id = $1 AND mr.status = $2 ORDER BY mr.created_at DESC LIMIT 100`, *tenantID, status)
		rows, err = r, qErr
	case tenantID != nil:
		r, qErr := a.pool.Query(ctx, baseQuery+` WHERE mr.tenant_id = $1 ORDER BY mr.created_at DESC LIMIT 100`, *tenantID)
		rows, err = r, qErr
	case status != "":
		r, qErr := a.pool.Query(ctx, baseQuery+` WHERE mr.status = $1 ORDER BY mr.created_at DESC LIMIT 100`, status)
		rows, err = r, qErr
	default:
		r, qErr := a.pool.Query(ctx, baseQuery+` ORDER BY mr.created_at DESC LIMIT 100`)
		rows, err = r, qErr
	}
	if err != nil {
		return nil, fmt.Errorf("settla-ops: listing manual reviews: %w", err)
	}
	defer rows.Close()

	var reviews []OpsManualReview
	for rows.Next() {
		var (
			rev                 OpsManualReview
			sourceAmountNumeric any
			resolvedAt          *time.Time
			attemptedRecoveries int32
		)
		if err := rows.Scan(
			&rev.ID,
			&rev.TransferID,
			&rev.TenantID,
			&rev.TenantName,
			&rev.Status,
			&rev.TransferStatus,
			&attemptedRecoveries,
			&sourceAmountNumeric,
			&rev.SourceCurrency,
			&rev.DestCurrency,
			&rev.FailureCode,
			&rev.EscalatedAt,
			&resolvedAt,
			&rev.ReviewedBy,
			&rev.Notes,
		); err != nil {
			return nil, fmt.Errorf("settla-ops: scanning manual review row: %w", err)
		}

		// Build human-readable reason.
		rev.Reason = fmt.Sprintf("Transfer stuck in %s state", rev.TransferStatus)
		if attemptedRecoveries > 0 {
			rev.Reason += fmt.Sprintf(" after %d recovery attempts", attemptedRecoveries)
		}

		// Convert source_amount from pgtype.Numeric (comes through as interface{}).
		// We scan it as a generic and use pgx's own numeric scanning.
		rev.SourceAmount = "0"
		if sourceAmountNumeric != nil {
			// Re-scan into pgtype.Numeric via the pool row scan machinery.
			// Simpler: format via Stringer if available.
			rev.SourceAmount = fmt.Sprintf("%v", sourceAmountNumeric)
		}

		rev.ReviewedAt = resolvedAt
		reviews = append(reviews, rev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("settla-ops: iterating manual review rows: %w", err)
	}
	return reviews, nil
}

// ResolveManualReview updates a manual review status.
func (a *OpsStoreAdapter) ResolveManualReview(ctx context.Context, id uuid.UUID, newStatus, resolution, resolvedBy string) error {
	const query = `
		UPDATE manual_reviews
		SET status = $2, resolution = $3, resolved_by = $4, resolved_at = now()
		WHERE id = $1
	`
	_, err := a.pool.Exec(ctx, query, id, newStatus, resolution, resolvedBy)
	if err != nil {
		return fmt.Errorf("settla-ops: resolving manual review %s: %w", id, err)
	}
	return nil
}

// GetLatestReconciliationReport returns the most recent reconciliation report.
func (a *OpsStoreAdapter) GetLatestReconciliationReport(ctx context.Context) (*OpsReconciliationReport, error) {
	rows, err := a.q.ListReconciliationReports(ctx, ListReconciliationReportsParams{Limit: 1, Offset: 0})
	if err != nil {
		return nil, fmt.Errorf("settla-ops: listing reconciliation reports: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	r := rows[0]
	return &OpsReconciliationReport{
		ID:            r.ID,
		JobName:       r.JobName,
		RanAt:         r.RunAt,
		DurationMs:    r.DurationMs,
		ChecksRun:     r.ChecksRun,
		ChecksPassed:  r.ChecksPassed,
		NeedsReview:   r.NeedsReview,
		Discrepancies: r.Discrepancies,
	}, nil
}

// ListNetSettlements returns up to 50 net settlements, joined with tenant names.
// When tenantID is non-nil, only settlements for that tenant are returned.
func (a *OpsStoreAdapter) ListNetSettlements(ctx context.Context, tenantID *uuid.UUID) ([]OpsNetSettlement, error) {
	const baseQuery = `
		SELECT
			ns.id,
			ns.tenant_id,
			COALESCE(t.name, '') AS tenant_name,
			ns.period_start,
			ns.period_end,
			ns.corridors,
			ns.net_by_currency,
			ns.total_fees_usd,
			ns.instructions,
			ns.status,
			ns.due_date,
			ns.settled_at
		FROM net_settlements ns
		LEFT JOIN tenants t ON t.id = ns.tenant_id
	`
	var (
		rows interface{ Next() bool; Scan(...any) error; Close(); Err() error }
		err  error
	)
	if tenantID != nil {
		var r interface{ Next() bool; Scan(...any) error; Close(); Err() error }
		r, err = a.pool.Query(ctx, baseQuery+` WHERE ns.tenant_id = $1 ORDER BY ns.created_at DESC LIMIT 50`, *tenantID)
		rows = r
	} else {
		var r interface{ Next() bool; Scan(...any) error; Close(); Err() error }
		r, err = a.pool.Query(ctx, baseQuery+` ORDER BY ns.created_at DESC LIMIT 50`)
		rows = r
	}
	if err != nil {
		return nil, fmt.Errorf("settla-ops: listing net settlements: %w", err)
	}
	defer rows.Close()

	var settlements []OpsNetSettlement
	for rows.Next() {
		var (
			s               OpsNetSettlement
			totalFeesNumeric any
			dueDate         *time.Time
		)
		if err := rows.Scan(
			&s.ID,
			&s.TenantID,
			&s.TenantName,
			&s.PeriodStart,
			&s.PeriodEnd,
			&s.Corridors,
			&s.NetByCurrency,
			&totalFeesNumeric,
			&s.Instructions,
			&s.Status,
			&dueDate,
			&s.SettledAt,
		); err != nil {
			return nil, fmt.Errorf("settla-ops: scanning net settlement row: %w", err)
		}

		s.TotalFeesUsd = "0"
		if totalFeesNumeric != nil {
			s.TotalFeesUsd = fmt.Sprintf("%v", totalFeesNumeric)
		}
		s.DueDate = dueDate
		settlements = append(settlements, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("settla-ops: iterating net settlement rows: %w", err)
	}
	return settlements, nil
}

// MarkSettlementPaid marks a net settlement as paid and records the payment reference.
func (a *OpsStoreAdapter) MarkSettlementPaid(ctx context.Context, id uuid.UUID, paymentRef string) error {
	const query = `
		UPDATE net_settlements
		SET status = 'paid',
		    settled_at = now(),
		    instructions = jsonb_set(
		        COALESCE(instructions, '[]'::jsonb),
		        '{payment_ref}',
		        to_jsonb($2::text)
		    )
		WHERE id = $1
	`
	_, err := a.pool.Exec(ctx, query, id, paymentRef)
	if err != nil {
		return fmt.Errorf("settla-ops: marking settlement %s as paid: %w", id, err)
	}
	return nil
}
