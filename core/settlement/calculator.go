package settlement

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
}

// TenantStore provides access to tenant configuration.
type TenantStore interface {
	// GetTenant retrieves a tenant by ID.
	GetTenant(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error)
	// ListTenantsBySettlementModel returns all tenants using the given settlement model.
	ListTenantsBySettlementModel(ctx context.Context, model domain.SettlementModel) ([]domain.Tenant, error)
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

	// 2. Query completed transfers for the period
	transfers, err := c.transferStore.ListCompletedTransfersByPeriod(ctx, tenantID, periodStart, periodEnd)
	if err != nil {
		return nil, fmt.Errorf("settla-settlement: listing transfers for tenant %s: %w", tenantID, err)
	}

	c.logger.Info("settla-settlement: calculating net settlement",
		"tenant_id", tenantID,
		"tenant_name", tenant.Name,
		"period_start", periodStart,
		"period_end", periodEnd,
		"transfer_count", len(transfers),
	)

	// 3. Group by corridor and compute per-currency nets
	corridors := c.groupByCorridors(transfers)
	netByCurrency := c.computeNetByCurrency(transfers)

	// 4. Calculate total fees
	totalFees := decimal.Zero
	for _, t := range transfers {
		totalFees = totalFees.Add(t.Fees)
	}

	// 5. Generate settlement instructions
	instructions := c.generateInstructions(tenant.Name, netByCurrency, totalFees)

	// 6. Build and persist the settlement record
	dueDate := periodEnd.AddDate(0, 0, DefaultSettlementDays) // T+3 settlement
	// Snapshot the tenant's fee schedule at calculation time for audit trail reconstruction.
	feeSnapshot := tenant.FeeSchedule
	settlement := &NetSettlement{
		ID:                  uuid.New(),
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

// groupByCorridors aggregates transfers by source/dest currency pair.
func (c *Calculator) groupByCorridors(transfers []TransferSummary) []CorridorPosition {
	type corridorKey struct {
		source, dest string
	}
	grouped := make(map[corridorKey]*CorridorPosition)

	for _, t := range transfers {
		key := corridorKey{source: t.SourceCurrency, dest: t.DestCurrency}
		pos, ok := grouped[key]
		if !ok {
			pos = &CorridorPosition{
				SourceCurrency: t.SourceCurrency,
				DestCurrency:   t.DestCurrency,
				TotalSource:    decimal.Zero,
				TotalDest:      decimal.Zero,
			}
			grouped[key] = pos
		}
		pos.TotalSource = pos.TotalSource.Add(t.SourceAmount)
		pos.TotalDest = pos.TotalDest.Add(t.DestAmount)
		pos.TransferCount++
	}

	result := make([]CorridorPosition, 0, len(grouped))
	for _, pos := range grouped {
		result = append(result, *pos)
	}
	return result
}

// computeNetByCurrency computes net position per currency.
// Source amounts are outflows (tenant sends money in this currency).
// Dest amounts are inflows (tenant receives money in this currency).
func (c *Calculator) computeNetByCurrency(transfers []TransferSummary) []CurrencyNet {
	nets := make(map[string]*CurrencyNet)

	for _, t := range transfers {
		// Source currency: tenant is sending, so it's an outflow for the tenant
		srcNet, ok := nets[t.SourceCurrency]
		if !ok {
			srcNet = &CurrencyNet{
				Currency: t.SourceCurrency,
				Inflows:  decimal.Zero,
				Outflows: decimal.Zero,
			}
			nets[t.SourceCurrency] = srcNet
		}
		srcNet.Outflows = srcNet.Outflows.Add(t.SourceAmount)

		// Dest currency: tenant is receiving, so it's an inflow for the tenant
		destNet, ok := nets[t.DestCurrency]
		if !ok {
			destNet = &CurrencyNet{
				Currency: t.DestCurrency,
				Inflows:  decimal.Zero,
				Outflows: decimal.Zero,
			}
			nets[t.DestCurrency] = destNet
		}
		destNet.Inflows = destNet.Inflows.Add(t.DestAmount)
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
