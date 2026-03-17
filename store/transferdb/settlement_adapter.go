package transferdb

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/core/settlement"
	"github.com/intellect4all/settla/domain"
)

// Compile-time interface checks.
var (
	_ settlement.TransferStore   = (*SettlementAdapter)(nil)
	_ settlement.SettlementStore = (*SettlementAdapter)(nil)
	_ settlement.TenantStore     = (*SettlementAdapter)(nil)
)

// SettlementAdapter implements settlement.TransferStore, settlement.SettlementStore,
// and settlement.TenantStore using SQLC-generated queries against the Transfer DB.
type SettlementAdapter struct {
	q *Queries
}

// NewSettlementAdapter creates a new adapter for settlement store interfaces.
func NewSettlementAdapter(q *Queries) *SettlementAdapter {
	return &SettlementAdapter{q: q}
}

// ListCompletedTransfersByPeriod returns summaries of completed transfers for a tenant
// within the given time range [start, end).
func (a *SettlementAdapter) ListCompletedTransfersByPeriod(
	ctx context.Context,
	tenantID uuid.UUID,
	start, end time.Time,
) ([]settlement.TransferSummary, error) {
	rows, err := a.q.ListCompletedTransfersByPeriod(ctx, ListCompletedTransfersByPeriodParams{
		TenantID:      tenantID,
		CompletedAt:   pgtype.Timestamptz{Time: start, Valid: true},
		CompletedAt_2: pgtype.Timestamptz{Time: end, Valid: true},
	})
	if err != nil {
		return nil, fmt.Errorf("settla-settlement: listing completed transfers: %w", err)
	}

	summaries := make([]settlement.TransferSummary, 0, len(rows))
	for _, r := range rows {
		s := settlement.TransferSummary{
			TransferID:     r.ID,
			SourceCurrency: r.SourceCurrency,
			SourceAmount:   decimalFromNumeric(r.SourceAmount),
			DestCurrency:   r.DestCurrency,
			DestAmount:     decimalFromNumeric(r.DestAmount),
		}
		if r.FeesUsd != nil {
			s.Fees, _ = decimal.NewFromString(fmt.Sprintf("%v", r.FeesUsd))
		}
		summaries = append(summaries, s)
	}
	return summaries, nil
}

// CreateNetSettlement persists a new net settlement record.
func (a *SettlementAdapter) CreateNetSettlement(ctx context.Context, s *domain.NetSettlement) error {
	corridorsJSON, err := json.Marshal(s.Corridors)
	if err != nil {
		return fmt.Errorf("settla-settlement: marshalling corridors: %w", err)
	}
	netJSON, err := json.Marshal(s.NetByCurrency)
	if err != nil {
		return fmt.Errorf("settla-settlement: marshalling net_by_currency: %w", err)
	}
	instrJSON, err := json.Marshal(s.Instructions)
	if err != nil {
		return fmt.Errorf("settla-settlement: marshalling instructions: %w", err)
	}

	_, err = a.q.CreateNetSettlement(ctx, CreateNetSettlementParams{
		ID:            s.ID,
		TenantID:      s.TenantID,
		PeriodStart:   s.PeriodStart,
		PeriodEnd:     s.PeriodEnd,
		Corridors:     corridorsJSON,
		NetByCurrency: netJSON,
		TotalFeesUsd:  numericFromDecimal(s.TotalFeesUSD),
		Instructions:  instrJSON,
		Status:        s.Status,
		DueDate:       pgtypeDateFromPtr(s.DueDate),
	})
	if err != nil {
		return fmt.Errorf("settla-settlement: creating net settlement: %w", err)
	}
	return nil
}

// GetNetSettlement retrieves a net settlement by ID.
func (a *SettlementAdapter) GetNetSettlement(ctx context.Context, id uuid.UUID) (*domain.NetSettlement, error) {
	row, err := a.q.GetNetSettlement(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("settla-settlement: getting net settlement %s: %w", id, err)
	}

	s := &domain.NetSettlement{
		ID:           row.ID,
		TenantID:     row.TenantID,
		PeriodStart:  row.PeriodStart,
		PeriodEnd:    row.PeriodEnd,
		TotalFeesUSD: decimalFromNumeric(row.TotalFeesUsd),
		Status:       row.Status,
		CreatedAt:    row.CreatedAt,
	}
	s.DueDate = timePtrFromPgtypeDate(row.DueDate)
	_ = json.Unmarshal(row.Corridors, &s.Corridors)
	_ = json.Unmarshal(row.NetByCurrency, &s.NetByCurrency)
	_ = json.Unmarshal(row.Instructions, &s.Instructions)

	return s, nil
}

// ListPendingSettlements returns all settlements with status "pending" or "overdue"
// across ALL tenants. This is an admin/scheduler operation — callers must provide
// an AdminCaller to identify who is making the cross-tenant query and why.
func (a *SettlementAdapter) ListPendingSettlements(ctx context.Context, caller domain.AdminCaller) ([]domain.NetSettlement, error) {
	slog.Info("admin cross-tenant query", "caller", caller.Service, "reason", caller.Reason, "method", "ListPendingSettlements")
	rows, err := a.q.ListAllPendingSettlements(ctx)
	if err != nil {
		return nil, fmt.Errorf("settla-settlement: listing pending settlements: %w", err)
	}

	settlements := make([]domain.NetSettlement, 0, len(rows))
	for _, r := range rows {
		s := domain.NetSettlement{
			ID:           r.ID,
			TenantID:     r.TenantID,
			TenantName:   r.TenantName,
			PeriodStart:  r.PeriodStart,
			PeriodEnd:    r.PeriodEnd,
			TotalFeesUSD: decimalFromNumeric(r.TotalFeesUsd),
			Status:       r.Status,
			CreatedAt:    r.CreatedAt,
		}
		s.DueDate = timePtrFromPgtypeDate(r.DueDate)
		_ = json.Unmarshal(r.Corridors, &s.Corridors)
		_ = json.Unmarshal(r.NetByCurrency, &s.NetByCurrency)
		_ = json.Unmarshal(r.Instructions, &s.Instructions)
		settlements = append(settlements, s)
	}
	return settlements, nil
}

// UpdateSettlementStatus updates the status of a net settlement.
func (a *SettlementAdapter) UpdateSettlementStatus(ctx context.Context, id uuid.UUID, status string) error {
	err := a.q.UpdateSettlementStatus(ctx, UpdateSettlementStatusParams{
		ID:     id,
		Status: status,
	})
	if err != nil {
		return fmt.Errorf("settla-settlement: updating settlement status: %w", err)
	}
	return nil
}

// GetTenant retrieves a tenant by ID (delegates to SQLC-generated query).
func (a *SettlementAdapter) GetTenant(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error) {
	row, err := a.q.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("settla-settlement: getting tenant %s: %w", tenantID, err)
	}
	return tenantFromRow(row)
}

// ListTenantsBySettlementModel returns all tenants using the given settlement model.
func (a *SettlementAdapter) ListTenantsBySettlementModel(ctx context.Context, model domain.SettlementModel) ([]domain.Tenant, error) {
	rows, err := a.q.ListTenantsBySettlementModel(ctx, string(model))
	if err != nil {
		return nil, fmt.Errorf("settla-settlement: listing tenants by settlement model: %w", err)
	}

	tenants := make([]domain.Tenant, 0, len(rows))
	for _, row := range rows {
		t, err := tenantFromRow(row)
		if err != nil {
			return nil, err
		}
		tenants = append(tenants, *t)
	}
	return tenants, nil
}
