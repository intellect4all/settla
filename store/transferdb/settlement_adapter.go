package transferdb

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/core/settlement"
	"github.com/intellect4all/settla/domain"
)

// Compile-time interface checks.
var (
	_ settlement.TransferStore    = (*SettlementAdapter)(nil)
	_ settlement.SettlementStore  = (*SettlementAdapter)(nil)
	_ settlement.TenantStore      = (*SettlementAdapter)(nil)
)

// SettlementAdapter implements settlement.TransferStore, settlement.SettlementStore,
// and settlement.TenantStore using raw SQL against the Transfer DB pool.
// Uses raw queries rather than SQLC-generated code because the settlement SQL
// queries involve JOINs and JSON column handling that are simpler to manage directly.
type SettlementAdapter struct {
	pool *pgxpool.Pool
	q    *Queries
}

// NewSettlementAdapter creates a new adapter for settlement store interfaces.
func NewSettlementAdapter(pool *pgxpool.Pool, q *Queries) *SettlementAdapter {
	return &SettlementAdapter{pool: pool, q: q}
}

// ListCompletedTransfersByPeriod returns summaries of completed transfers for a tenant
// within the given time range [start, end).
func (a *SettlementAdapter) ListCompletedTransfersByPeriod(
	ctx context.Context,
	tenantID uuid.UUID,
	start, end time.Time,
) ([]settlement.TransferSummary, error) {
	rows, err := a.pool.Query(ctx, `
		SELECT source_currency, source_amount, dest_currency, dest_amount,
		       COALESCE((fees->>'total_usd')::NUMERIC(28,8), 0) AS fees_usd
		FROM transfers
		WHERE tenant_id = $1
		  AND status = 'COMPLETED'
		  AND completed_at >= $2
		  AND completed_at < $3`,
		tenantID, start, end,
	)
	if err != nil {
		return nil, fmt.Errorf("settla-settlement: listing completed transfers: %w", err)
	}
	defer rows.Close()

	var summaries []settlement.TransferSummary
	for rows.Next() {
		var s settlement.TransferSummary
		var srcAmt, destAmt, fees string
		if err := rows.Scan(&s.SourceCurrency, &srcAmt, &s.DestCurrency, &destAmt, &fees); err != nil {
			return nil, fmt.Errorf("settla-settlement: scanning transfer summary: %w", err)
		}
		s.SourceAmount, _ = decimal.NewFromString(srcAmt)
		s.DestAmount, _ = decimal.NewFromString(destAmt)
		s.Fees, _ = decimal.NewFromString(fees)
		summaries = append(summaries, s)
	}
	return summaries, rows.Err()
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

	_, err = a.pool.Exec(ctx, `
		INSERT INTO net_settlements (
		    id, tenant_id, period_start, period_end,
		    corridors, net_by_currency, total_fees_usd,
		    instructions, status, due_date
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		s.ID, s.TenantID, s.PeriodStart, s.PeriodEnd,
		corridorsJSON, netJSON, s.TotalFeesUSD.String(),
		instrJSON, s.Status, s.DueDate,
	)
	if err != nil {
		return fmt.Errorf("settla-settlement: creating net settlement: %w", err)
	}
	return nil
}

// GetNetSettlement retrieves a net settlement by ID.
func (a *SettlementAdapter) GetNetSettlement(ctx context.Context, id uuid.UUID) (*domain.NetSettlement, error) {
	row := a.pool.QueryRow(ctx, `
		SELECT ns.id, ns.tenant_id, t.name, ns.period_start, ns.period_end,
		       ns.corridors, ns.net_by_currency, ns.total_fees_usd,
		       ns.instructions, ns.status, ns.due_date, ns.created_at
		FROM net_settlements ns
		JOIN tenants t ON t.id = ns.tenant_id
		WHERE ns.id = $1`, id)

	var s domain.NetSettlement
	var corridorsJSON, netJSON, instrJSON []byte
	var feesStr string
	if err := row.Scan(
		&s.ID, &s.TenantID, &s.TenantName, &s.PeriodStart, &s.PeriodEnd,
		&corridorsJSON, &netJSON, &feesStr,
		&instrJSON, &s.Status, &s.DueDate, &s.CreatedAt,
	); err != nil {
		return nil, fmt.Errorf("settla-settlement: getting net settlement %s: %w", id, err)
	}

	s.TotalFeesUSD, _ = decimal.NewFromString(feesStr)
	_ = json.Unmarshal(corridorsJSON, &s.Corridors)
	_ = json.Unmarshal(netJSON, &s.NetByCurrency)
	_ = json.Unmarshal(instrJSON, &s.Instructions)

	return &s, nil
}

// ListPendingSettlements returns all settlements with status "pending" or "overdue".
func (a *SettlementAdapter) ListPendingSettlements(ctx context.Context) ([]domain.NetSettlement, error) {
	rows, err := a.pool.Query(ctx, `
		SELECT ns.id, ns.tenant_id, t.name, ns.period_start, ns.period_end,
		       ns.corridors, ns.net_by_currency, ns.total_fees_usd,
		       ns.instructions, ns.status, ns.due_date, ns.created_at
		FROM net_settlements ns
		JOIN tenants t ON t.id = ns.tenant_id
		WHERE ns.status IN ('pending', 'overdue')
		ORDER BY ns.due_date ASC`)
	if err != nil {
		return nil, fmt.Errorf("settla-settlement: listing pending settlements: %w", err)
	}
	defer rows.Close()

	var settlements []domain.NetSettlement
	for rows.Next() {
		var s domain.NetSettlement
		var corridorsJSON, netJSON, instrJSON []byte
		var feesStr string
		if err := rows.Scan(
			&s.ID, &s.TenantID, &s.TenantName, &s.PeriodStart, &s.PeriodEnd,
			&corridorsJSON, &netJSON, &feesStr,
			&instrJSON, &s.Status, &s.DueDate, &s.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("settla-settlement: scanning pending settlement: %w", err)
		}
		s.TotalFeesUSD, _ = decimal.NewFromString(feesStr)
		_ = json.Unmarshal(corridorsJSON, &s.Corridors)
		_ = json.Unmarshal(netJSON, &s.NetByCurrency)
		_ = json.Unmarshal(instrJSON, &s.Instructions)
		settlements = append(settlements, s)
	}
	return settlements, rows.Err()
}

// UpdateSettlementStatus updates the status of a net settlement.
func (a *SettlementAdapter) UpdateSettlementStatus(ctx context.Context, id uuid.UUID, status string) error {
	_, err := a.pool.Exec(ctx, `
		UPDATE net_settlements
		SET status = $2,
		    settled_at = CASE WHEN $2 = 'settled' THEN now() ELSE settled_at END
		WHERE id = $1`, id, status)
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
	rows, err := a.pool.Query(ctx, `
		SELECT id, name, slug, status, fee_schedule, settlement_model,
		       webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit,
		       kyb_status, kyb_verified_at, metadata, created_at, updated_at, webhook_events
		FROM tenants
		WHERE settlement_model = $1`, string(model))
	if err != nil {
		return nil, fmt.Errorf("settla-settlement: listing tenants by settlement model: %w", err)
	}
	defer rows.Close()

	var tenants []domain.Tenant
	for rows.Next() {
		var row Tenant
		if err := rows.Scan(
			&row.ID, &row.Name, &row.Slug, &row.Status,
			&row.FeeSchedule, &row.SettlementModel,
			&row.WebhookUrl, &row.WebhookSecret,
			&row.DailyLimitUsd, &row.PerTransferLimit,
			&row.KybStatus, &row.KybVerifiedAt,
			&row.Metadata, &row.CreatedAt, &row.UpdatedAt, &row.WebhookEvents,
		); err != nil {
			return nil, fmt.Errorf("settla-settlement: scanning tenant row: %w", err)
		}
		t, err := tenantFromRow(row)
		if err != nil {
			return nil, err
		}
		tenants = append(tenants, *t)
	}
	return tenants, rows.Err()
}
