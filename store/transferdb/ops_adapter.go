package transferdb

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/intellect4all/settla/domain"
)

// OpsStore defines the cross-tenant ops queries needed by the dashboard.
type OpsStore interface {
	// ListManualReviews returns manual reviews. Pass a non-nil tenantID to
	// restrict to a single tenant (tenant-scoped view); nil returns all tenants (admin view).
	// caller identifies who is making the query and why (for audit logging).
	ListManualReviews(ctx context.Context, caller domain.AdminCaller, tenantID *uuid.UUID, status string) ([]OpsManualReview, error)
	ResolveManualReview(ctx context.Context, id uuid.UUID, newStatus, resolution, resolvedBy string) error
	GetLatestReconciliationReport(ctx context.Context) (*OpsReconciliationReport, error)
	// ListNetSettlements returns net settlements. Pass a non-nil tenantID to
	// restrict to a single tenant; nil returns all tenants (admin view).
	// caller identifies who is making the query and why (for audit logging).
	ListNetSettlements(ctx context.Context, caller domain.AdminCaller, tenantID *uuid.UUID) ([]OpsNetSettlement, error)
	MarkSettlementPaid(ctx context.Context, id uuid.UUID, paymentRef string) error

	// Tenant management
	ListAllTenants(ctx context.Context, limit, offset int32) ([]OpsTenant, error)
	GetTenantByID(ctx context.Context, id uuid.UUID) (*OpsTenant, error)
	UpdateTenantStatus(ctx context.Context, id uuid.UUID, status string) error
	UpdateTenantKYBStatus(ctx context.Context, id uuid.UUID, kybStatus string) error
	UpdateTenantFees(ctx context.Context, id uuid.UUID, feeSchedule []byte) error
	UpdateTenantLimits(ctx context.Context, id uuid.UUID, dailyLimitUsd, perTransferLimit string) error
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

// OpsTenant is a tenant as seen by the ops dashboard (no secrets like webhook_secret).
type OpsTenant struct {
	ID               uuid.UUID       `json:"id"`
	Name             string          `json:"name"`
	Slug             string          `json:"slug"`
	Status           string          `json:"status"`
	FeeSchedule      json.RawMessage `json:"fee_schedule"`
	SettlementModel  string          `json:"settlement_model"`
	DailyLimitUsd    string          `json:"daily_limit_usd"`
	PerTransferLimit string          `json:"per_transfer_limit"`
	KybStatus        string          `json:"kyb_status"`
	KybVerifiedAt    *time.Time      `json:"kyb_verified_at,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

// Compile-time interface check.
var _ OpsStore = (*OpsStoreAdapter)(nil)

// OpsStoreAdapter implements OpsStore using SQLC-generated queries.
type OpsStoreAdapter struct {
	q *Queries
}

// NewOpsAdapter creates a new OpsStoreAdapter.
func NewOpsAdapter(q *Queries) *OpsStoreAdapter {
	return &OpsStoreAdapter{q: q}
}

// ListManualReviews returns up to 100 manual reviews. When tenantID is non-nil,
// only reviews for that tenant are returned. When status is non-empty, only
// reviews with that status are returned. The two filters are combined with AND.
func (a *OpsStoreAdapter) ListManualReviews(ctx context.Context, caller domain.AdminCaller, tenantID *uuid.UUID, status string) ([]OpsManualReview, error) {
	slog.Info("admin cross-tenant query", "caller", caller.Service, "reason", caller.Reason, "method", "ListManualReviews")
	rows, err := a.q.ListOpsManualReviews(ctx, ListOpsManualReviewsParams{
		FilterTenantID: uuidFromPtr(tenantID),
		FilterStatus:   pgtypeTextFromString(status),
	})
	if err != nil {
		return nil, fmt.Errorf("settla-ops: listing manual reviews: %w", err)
	}

	reviews := make([]OpsManualReview, 0, len(rows))
	for _, r := range rows {
		rev := OpsManualReview{
			ID:             r.ID,
			TransferID:     r.TransferID,
			TenantID:       r.TenantID,
			TenantName:     r.TenantName,
			Status:         string(r.Status),
			TransferStatus: r.TransferStatus,
			SourceCurrency: r.SourceCurrency,
			DestCurrency:   r.DestCurrency,
			FailureCode:    r.FailureCode,
			EscalatedAt:    r.EscalatedAt,
			ReviewedBy:     r.ResolvedBy,
			Notes:          r.Resolution,
		}

		// Build human-readable reason.
		rev.Reason = fmt.Sprintf("Transfer stuck in %s state", rev.TransferStatus)
		if r.AttemptedRecoveries > 0 {
			rev.Reason += fmt.Sprintf(" after %d recovery attempts", r.AttemptedRecoveries)
		}

		rev.SourceAmount = decimalFromNumeric(r.SourceAmount).String()
		rev.ReviewedAt = timePtrFromPgtypeTz(r.ResolvedAt)
		reviews = append(reviews, rev)
	}
	return reviews, nil
}

// ResolveManualReview updates a manual review status.
func (a *OpsStoreAdapter) ResolveManualReview(ctx context.Context, id uuid.UUID, newStatus, resolution, resolvedBy string) error {
	err := a.q.ResolveManualReview(ctx, ResolveManualReviewParams{
		ID:         id,
		Status:     ReviewStatusEnum(newStatus),
		Resolution: pgtype.Text{String: resolution, Valid: resolution != ""},
		ResolvedBy: pgtype.Text{String: resolvedBy, Valid: resolvedBy != ""},
	})
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
func (a *OpsStoreAdapter) ListNetSettlements(ctx context.Context, caller domain.AdminCaller, tenantID *uuid.UUID) ([]OpsNetSettlement, error) {
	slog.Info("admin cross-tenant query", "caller", caller.Service, "reason", caller.Reason, "method", "ListNetSettlements")
	rows, err := a.q.ListOpsNetSettlements(ctx, uuidFromPtr(tenantID))
	if err != nil {
		return nil, fmt.Errorf("settla-ops: listing net settlements: %w", err)
	}

	settlements := make([]OpsNetSettlement, 0, len(rows))
	for _, r := range rows {
		s := OpsNetSettlement{
			ID:            r.ID,
			TenantID:      r.TenantID,
			TenantName:    r.TenantName,
			PeriodStart:   r.PeriodStart,
			PeriodEnd:     r.PeriodEnd,
			Corridors:     r.Corridors,
			NetByCurrency: r.NetByCurrency,
			TotalFeesUsd:  decimalFromNumeric(r.TotalFeesUsd).String(),
			Instructions:  r.Instructions,
			Status:        r.Status,
			DueDate:       timePtrFromPgtypeDate(r.DueDate),
			SettledAt:     timePtrFromPgtypeTz(r.SettledAt),
		}
		settlements = append(settlements, s)
	}
	return settlements, nil
}

// ListAllTenants returns up to `limit` tenants starting at `offset`.
func (a *OpsStoreAdapter) ListAllTenants(ctx context.Context, limit, offset int32) ([]OpsTenant, error) {
	rows, err := a.q.ListTenants(ctx, ListTenantsParams{Limit: limit, Offset: offset})
	if err != nil {
		return nil, fmt.Errorf("settla-ops: listing tenants: %w", err)
	}
	tenants := make([]OpsTenant, 0, len(rows))
	for _, t := range rows {
		tenants = append(tenants, tenantToOps(t))
	}
	return tenants, nil
}

// GetTenantByID returns a single tenant by ID.
func (a *OpsStoreAdapter) GetTenantByID(ctx context.Context, id uuid.UUID) (*OpsTenant, error) {
	t, err := a.q.GetTenant(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("settla-ops: getting tenant %s: %w", id, err)
	}
	ot := tenantToOps(t)
	return &ot, nil
}

// UpdateTenantStatus updates a tenant's status (ACTIVE, SUSPENDED, ONBOARDING).
func (a *OpsStoreAdapter) UpdateTenantStatus(ctx context.Context, id uuid.UUID, status string) error {
	err := a.q.UpdateTenantStatus(ctx, UpdateTenantStatusParams{ID: id, Status: status})
	if err != nil {
		return fmt.Errorf("settla-ops: updating tenant %s status: %w", id, err)
	}
	return nil
}

// UpdateTenantKYBStatus updates a tenant's KYB status.
func (a *OpsStoreAdapter) UpdateTenantKYBStatus(ctx context.Context, id uuid.UUID, kybStatus string) error {
	err := a.q.UpdateTenantKYB(ctx, UpdateTenantKYBParams{ID: id, KybStatus: kybStatus})
	if err != nil {
		return fmt.Errorf("settla-ops: updating tenant %s KYB status: %w", id, err)
	}
	return nil
}

// UpdateTenantFees updates a tenant's fee schedule.
func (a *OpsStoreAdapter) UpdateTenantFees(ctx context.Context, id uuid.UUID, feeSchedule []byte) error {
	err := a.q.UpdateTenantFeeSchedule(ctx, UpdateTenantFeeScheduleParams{ID: id, FeeSchedule: feeSchedule})
	if err != nil {
		return fmt.Errorf("settla-ops: updating tenant %s fees: %w", id, err)
	}
	return nil
}

// UpdateTenantLimits updates a tenant's daily and per-transfer limits.
func (a *OpsStoreAdapter) UpdateTenantLimits(ctx context.Context, id uuid.UUID, dailyLimitUsd, perTransferLimit string) error {
	daily := pgtype.Numeric{}
	if err := daily.Scan(dailyLimitUsd); err != nil {
		return fmt.Errorf("settla-ops: parsing daily_limit_usd %q: %w", dailyLimitUsd, err)
	}
	perTx := pgtype.Numeric{}
	if err := perTx.Scan(perTransferLimit); err != nil {
		return fmt.Errorf("settla-ops: parsing per_transfer_limit %q: %w", perTransferLimit, err)
	}
	err := a.q.UpdateTenantLimits(ctx, UpdateTenantLimitsParams{
		ID:               id,
		DailyLimitUsd:    daily,
		PerTransferLimit: perTx,
	})
	if err != nil {
		return fmt.Errorf("settla-ops: updating tenant %s limits: %w", id, err)
	}
	return nil
}

// tenantToOps converts a SQLC Tenant to an OpsTenant (strips secrets).
func tenantToOps(t Tenant) OpsTenant {
	dailyLimit := numericToString(t.DailyLimitUsd)
	perTx := numericToString(t.PerTransferLimit)
	var kybAt *time.Time
	if t.KybVerifiedAt.Valid {
		ts := t.KybVerifiedAt.Time
		kybAt = &ts
	}
	return OpsTenant{
		ID:               t.ID,
		Name:             t.Name,
		Slug:             t.Slug,
		Status:           t.Status,
		FeeSchedule:      json.RawMessage(t.FeeSchedule),
		SettlementModel:  t.SettlementModel,
		DailyLimitUsd:    dailyLimit,
		PerTransferLimit: perTx,
		KybStatus:        t.KybStatus,
		KybVerifiedAt:    kybAt,
		CreatedAt:        t.CreatedAt,
		UpdatedAt:        t.UpdatedAt,
	}
}

// numericToString converts a pgtype.Numeric to a string, returning "0" if invalid.
func numericToString(n pgtype.Numeric) string {
	if !n.Valid {
		return "0"
	}
	f, err := n.Float64Value()
	if err != nil || !f.Valid {
		return "0"
	}
	return fmt.Sprintf("%.2f", f.Float64)
}

// MarkSettlementPaid marks a net settlement as paid and records the payment reference.
func (a *OpsStoreAdapter) MarkSettlementPaid(ctx context.Context, id uuid.UUID, paymentRef string) error {
	err := a.q.MarkSettlementPaid(ctx, MarkSettlementPaidParams{
		ID:         id,
		PaymentRef: paymentRef,
	})
	if err != nil {
		return fmt.Errorf("settla-ops: marking settlement %s as paid: %w", id, err)
	}
	return nil
}

// pgtypeTextFromString returns a valid pgtype.Text when s is non-empty, otherwise invalid (NULL).
func pgtypeTextFromString(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}
