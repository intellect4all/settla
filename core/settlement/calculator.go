package settlement

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

// DefaultSettlementDays is the number of business days after the period end
// by which the net settlement is due (T+3).
const DefaultSettlementDays = 3

// Type aliases so existing code within and outside the package continues to compile.
type NetSettlement = domain.NetSettlement
type CorridorPosition = domain.CorridorPosition
type CurrencyNet = domain.CurrencyNet
type SettlementInstruction = domain.SettlementInstruction

// TransferStore provides access to completed transfers for a given period.
type TransferStore interface {
	// ListCompletedTransfersByPeriod returns summaries of all completed transfers
	// for a tenant within the given time range [start, end).
	ListCompletedTransfersByPeriod(ctx context.Context, tenantID uuid.UUID, start, end time.Time) ([]TransferSummary, error)

	// AggregateCompletedTransfersByPeriod returns pre-aggregated corridor summaries
	// (GROUP BY source_currency, dest_currency) for a tenant within the given time range.
	// This avoids materializing individual transfer rows for large tenants.
	AggregateCompletedTransfersByPeriod(ctx context.Context, tenantID uuid.UUID, start, end time.Time) ([]CorridorAggregate, error)
}

// CorridorAggregate is a pre-aggregated summary of completed transfers for a single
// corridor (source_currency → dest_currency), computed server-side via GROUP BY.
type CorridorAggregate struct {
	SourceCurrency string
	DestCurrency   string
	TotalSource    decimal.Decimal
	TotalDest      decimal.Decimal
	TransferCount  int64
	TotalFeesUSD   decimal.Decimal
}

// TenantStore provides access to tenant configuration.
type TenantStore interface {
	// GetTenant retrieves a tenant by ID.
	GetTenant(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error)
	// ListTenantsBySettlementModel returns tenants using the given settlement model, paginated.
	ListTenantsBySettlementModel(ctx context.Context, model domain.SettlementModel, limit, offset int32) ([]domain.Tenant, error)
	// ListActiveTenantIDsBySettlementModel returns a cursor-paginated batch of active
	// tenant IDs for the given settlement model, ordered by ID. Pass uuid.Nil for
	// afterID to start from the beginning.
	ListActiveTenantIDsBySettlementModel(ctx context.Context, model domain.SettlementModel, limit int32, afterID uuid.UUID) ([]uuid.UUID, error)
	// CountActiveTenantsBySettlementModel returns the number of active tenants
	// for the given settlement model.
	CountActiveTenantsBySettlementModel(ctx context.Context, model domain.SettlementModel) (int64, error)
}

// SettlementStore persists and retrieves net settlement records.
type SettlementStore interface {
	// CreateNetSettlement persists a new net settlement record.
	CreateNetSettlement(ctx context.Context, settlement *NetSettlement) error
	// GetNetSettlement retrieves a net settlement by ID.
	GetNetSettlement(ctx context.Context, id uuid.UUID) (*NetSettlement, error)
	// ListPendingSettlements returns all settlements with status "pending" or "overdue".
	// caller identifies who is making the cross-tenant query and why.
	ListPendingSettlements(ctx context.Context, caller domain.AdminCaller) ([]NetSettlement, error)
	// UpdateSettlementStatus updates the status of a net settlement.
	UpdateSettlementStatus(ctx context.Context, id uuid.UUID, status string) error
}

// TransferSummary is a lightweight projection of a completed transfer,
// containing only the fields needed for net settlement calculation.
type TransferSummary struct {
	TransferID     uuid.UUID       // included for dispute traceability
	SourceCurrency string
	SourceAmount   decimal.Decimal
	DestCurrency   string
	DestAmount     decimal.Decimal
	Fees           decimal.Decimal // total fees in USD
}

// Calculator computes net settlement positions for tenants on the NET_SETTLEMENT model.
// Instead of settling each transfer individually, net settlement aggregates completed
// transfers over a period and produces a single settlement instruction per currency pair.
type Calculator struct {
	transferStore  TransferStore
	tenantStore    TenantStore
	store          SettlementStore
	logger         *slog.Logger
}

// NewCalculator creates a new net settlement calculator.
func NewCalculator(
	transferStore TransferStore,
	tenantStore TenantStore,
	store SettlementStore,
	logger *slog.Logger,
) *Calculator {
	return &Calculator{
		transferStore:  transferStore,
		tenantStore:    tenantStore,
		store:          store,
		logger:         logger.With("module", "core.settlement"),
	}
}

// CalculateNetSettlement computes the net settlement for a tenant over the given period.
//
// Steps:
//  1. Query completed transfers for tenant in [periodStart, periodEnd)
//  2. Group by corridor (source_currency -> dest_currency)
//  3. Net per currency (sum inflows - sum outflows)
//  4. Apply fee schedule from tenant
//  5. Generate settlement instructions
//  6. Store in net_settlements table
//  7. Return the settlement record
func (c *Calculator) CalculateNetSettlement(
	ctx context.Context,
	tenantID uuid.UUID,
	periodStart, periodEnd time.Time,
) (*NetSettlement, error) {
	// 0. Validate period range
	if !periodStart.Before(periodEnd) {
		return nil, fmt.Errorf("settla-settlement: invalid period range: periodStart (%s) must be before periodEnd (%s)",
			periodStart.Format(time.RFC3339), periodEnd.Format(time.RFC3339))
	}

	// 1. Load tenant for fee schedule and name
	tenant, err := c.tenantStore.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("settla-settlement: loading tenant %s: %w", tenantID, err)
	}

	if tenant.SettlementModel != domain.SettlementModelNetSettlement {
		return nil, fmt.Errorf("settla-settlement: tenant %s uses %s model, not NET_SETTLEMENT",
			tenantID, tenant.SettlementModel)
	}

	// 2. Query pre-aggregated corridor summaries (DB-side GROUP BY)
	aggregates, err := c.transferStore.AggregateCompletedTransfersByPeriod(ctx, tenantID, periodStart, periodEnd)
	if err != nil {
		return nil, fmt.Errorf("settla-settlement: aggregating transfers for tenant %s: %w", tenantID, err)
	}

	var totalTransfers int64
	for _, a := range aggregates {
		totalTransfers += a.TransferCount
	}

	c.logger.Info("settla-settlement: calculating net settlement",
		"tenant_id", tenantID,
		"tenant_name", tenant.Name,
		"period_start", periodStart,
		"period_end", periodEnd,
		"corridors", len(aggregates),
		"transfer_count", totalTransfers,
	)

	// 3. Build corridors and per-currency nets from aggregated data
	corridors := c.corridorsFromAggregates(aggregates)
	netByCurrency := c.netByCurrencyFromAggregates(aggregates)

	// 4. Calculate total fees
	totalFees := decimal.Zero
	for _, a := range aggregates {
		totalFees = totalFees.Add(a.TotalFeesUSD)
	}

	// 5. Generate settlement instructions
	instructions := c.generateInstructions(tenant.Name, netByCurrency, totalFees)

	// 6. Build and persist the settlement record
	dueDate := periodEnd.AddDate(0, 0, DefaultSettlementDays) // T+3 settlement
	// Snapshot the tenant's fee schedule at calculation time for audit trail reconstruction.
	feeSnapshot := tenant.FeeSchedule
	settlement := &NetSettlement{
		ID:                  uuid.Must(uuid.NewV7()),
		TenantID:            tenantID,
		TenantName:          tenant.Name,
		PeriodStart:         periodStart,
		PeriodEnd:           periodEnd,
		Corridors:           corridors,
		NetByCurrency:       netByCurrency,
		TotalFeesUSD:        totalFees,
		Instructions:        instructions,
		FeeScheduleSnapshot: &feeSnapshot,
		Status:              "pending",
		DueDate:             &dueDate,
		CreatedAt:           time.Now().UTC(),
	}

	if err := c.store.CreateNetSettlement(ctx, settlement); err != nil {
		// If a settlement for this tenant+period already exists (unique constraint),
		// return the error with context so the scheduler can treat it as idempotent.
		if strings.Contains(err.Error(), "uk_net_settlements_tenant_period") ||
			strings.Contains(err.Error(), "duplicate key") {
			c.logger.Info("settla-settlement: settlement already exists for period, skipping",
				"tenant_id", tenantID,
				"period_start", periodStart,
				"period_end", periodEnd,
			)
			return settlement, nil
		}
		return nil, fmt.Errorf("settla-settlement: persisting net settlement for tenant %s: %w", tenantID, err)
	}

	c.logger.Info("settla-settlement: net settlement created",
		"settlement_id", settlement.ID,
		"tenant_id", tenantID,
		"corridors", len(corridors),
		"total_fees_usd", totalFees.StringFixed(2),
		"instructions", len(instructions),
	)

	return settlement, nil
}

// corridorsFromAggregates converts pre-aggregated DB rows into CorridorPosition slices.
func (c *Calculator) corridorsFromAggregates(aggregates []CorridorAggregate) []CorridorPosition {
	result := make([]CorridorPosition, 0, len(aggregates))
	for _, a := range aggregates {
		result = append(result, CorridorPosition{
			SourceCurrency: a.SourceCurrency,
			DestCurrency:   a.DestCurrency,
			TotalSource:    a.TotalSource,
			TotalDest:      a.TotalDest,
			TransferCount:  int(a.TransferCount),
		})
	}
	return result
}

// netByCurrencyFromAggregates computes per-currency net positions from aggregated corridors.
func (c *Calculator) netByCurrencyFromAggregates(aggregates []CorridorAggregate) []CurrencyNet {
	nets := make(map[string]*CurrencyNet)

	for _, a := range aggregates {
		// Source currency: outflow
		srcNet, ok := nets[a.SourceCurrency]
		if !ok {
			srcNet = &CurrencyNet{Currency: a.SourceCurrency, Inflows: decimal.Zero, Outflows: decimal.Zero}
			nets[a.SourceCurrency] = srcNet
		}
		srcNet.Outflows = srcNet.Outflows.Add(a.TotalSource)

		// Dest currency: inflow
		destNet, ok := nets[a.DestCurrency]
		if !ok {
			destNet = &CurrencyNet{Currency: a.DestCurrency, Inflows: decimal.Zero, Outflows: decimal.Zero}
			nets[a.DestCurrency] = destNet
		}
		destNet.Inflows = destNet.Inflows.Add(a.TotalDest)
	}

	result := make([]CurrencyNet, 0, len(nets))
	for _, net := range nets {
		net.Net = net.Inflows.Sub(net.Outflows)
		result = append(result, *net)
	}
	return result
}

// generateInstructions builds human-readable settlement directives from net positions.
func (c *Calculator) generateInstructions(
	tenantName string,
	netByCurrency []CurrencyNet,
	totalFees decimal.Decimal,
) []SettlementInstruction {
	var instructions []SettlementInstruction

	for _, net := range netByCurrency {
		if net.Net.IsZero() {
			continue
		}

		var inst SettlementInstruction
		inst.Currency = net.Currency

		if net.Net.IsPositive() {
			// Positive net = tenant has more inflows than outflows = Settla owes tenant
			inst.Direction = "settla_owes_tenant"
			inst.Amount = net.Net
			inst.Description = fmt.Sprintf("Settla owes %s %s %s",
				tenantName, formatAmount(net.Net), net.Currency)
		} else {
			// Negative net = tenant has more outflows than inflows = tenant owes Settla
			inst.Direction = "tenant_owes_settla"
			inst.Amount = net.Net.Abs()
			inst.Description = fmt.Sprintf("%s owes Settla %s %s",
				tenantName, formatAmount(net.Net.Abs()), net.Currency)
		}

		instructions = append(instructions, inst)
	}

	// Add fee instruction if there are any fees. Round to 2dp (USD precision).
	if totalFees.IsPositive() {
		roundedFees := totalFees.Round(2)
		instructions = append(instructions, SettlementInstruction{
			Direction:   "tenant_owes_settla",
			Currency:    "USD",
			Amount:      roundedFees,
			Description: fmt.Sprintf("Fees: %s USD", formatAmount(roundedFees)),
		})
	}

	return instructions
}

// formatAmount formats a decimal amount with appropriate abbreviation.
func formatAmount(amount decimal.Decimal) string {
	billion := decimal.NewFromInt(1_000_000_000)
	million := decimal.NewFromInt(1_000_000)
	thousand := decimal.NewFromInt(1_000)

	if amount.GreaterThanOrEqual(billion) {
		return amount.Div(billion).StringFixed(2) + "B"
	}
	if amount.GreaterThanOrEqual(million) {
		return amount.Div(million).StringFixed(2) + "M"
	}
	if amount.GreaterThanOrEqual(thousand) {
		return amount.Div(thousand).StringFixed(0) + "K"
	}
	return amount.StringFixed(2)
}

// MarshalCorridors serializes corridor positions to JSON for database storage.
func MarshalCorridors(corridors []CorridorPosition) ([]byte, error) {
	return json.Marshal(corridors)
}

// MarshalNetByCurrency serializes currency nets to JSON for database storage.
func MarshalNetByCurrency(nets []CurrencyNet) ([]byte, error) {
	return json.Marshal(nets)
}

// MarshalInstructions serializes settlement instructions to JSON for database storage.
func MarshalInstructions(instructions []SettlementInstruction) ([]byte, error) {
	return json.Marshal(instructions)
}
