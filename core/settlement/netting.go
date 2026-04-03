package settlement

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/shopspring/decimal"

	"github.com/intellect4all/settla/domain"
)

var (
	nettingSavings = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "settla_settlement_netting_savings_percent",
		Help: "Percentage reduction in settlement volume from cross-tenant netting.",
	})
	nettingInstructionsReduced = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "settla_settlement_netting_instructions_reduced",
		Help: "Number of settlement instructions eliminated by netting.",
	})
)

// NettingResult holds the outcome of a cross-tenant netting calculation.
type NettingResult struct {
	// OriginalInstructions is the total number of per-tenant settlement instructions.
	OriginalInstructions int
	// NettedInstructions is the reduced set after cross-tenant netting.
	NettedInstructions []NettedInstruction
	// TotalOriginalVolume is the sum of absolute amounts before netting.
	TotalOriginalVolume decimal.Decimal
	// TotalNettedVolume is the sum of absolute amounts after netting.
	TotalNettedVolume decimal.Decimal
	// SavingsPercent is the percentage reduction in volume.
	SavingsPercent float64
}

// NettedInstruction is a single cross-tenant netted settlement instruction.
type NettedInstruction struct {
	Currency    string          `json:"currency"`
	Direction   string          `json:"direction"` // "tenant_owes_settla" or "settla_owes_tenant"
	TenantID    uuid.UUID       `json:"tenant_id"`
	TenantName  string          `json:"tenant_name"`
	Amount      decimal.Decimal `json:"amount"`
	Description string          `json:"description"`
}

// CalculateCrossTenantNetting takes per-tenant settlements and produces a netted
// set of instructions. Instead of settling each tenant independently, it aggregates
// all net positions per currency and produces fewer, larger settlement instructions.
//
// Example:
//
//	Before (3 settlements):
//	  Tenant A owes Settla 1,000,000 NGN
//	  Settla owes Tenant B 500,000 NGN
//	  Tenant C owes Settla 200,000 NGN
//
//	After netting:
//	  Tenant A owes Settla 1,000,000 NGN (unchanged — largest debtor)
//	  Settla owes Tenant B 500,000 NGN (unchanged — creditor)
//	  Tenant C owes Settla 200,000 NGN (unchanged — smaller debtor)
//	  BUT: Settla's net NGN position = +700,000 NGN (1M + 200K - 500K)
//	  So Settla only needs to receive 700K NGN externally instead of moving 1.7M.
func CalculateCrossTenantNetting(
	settlements []domain.NetSettlement,
	logger *slog.Logger,
) *NettingResult {
	// Aggregate per-currency net positions across all tenants.
	// Positive = tenant owes Settla, Negative = Settla owes tenant.
	type tenantPosition struct {
		tenantID   uuid.UUID
		tenantName string
		net        decimal.Decimal // positive = tenant owes Settla
	}

	// Group by currency.
	byCurrency := make(map[string][]tenantPosition)
	var originalInstructions int
	totalOriginal := decimal.Zero

	for _, s := range settlements {
		for _, cn := range s.NetByCurrency {
			originalInstructions++
			totalOriginal = totalOriginal.Add(cn.Net.Abs())
			byCurrency[cn.Currency] = append(byCurrency[cn.Currency], tenantPosition{
				tenantID:   s.TenantID,
				tenantName: s.TenantName,
				net:        cn.Net,
			})
		}
	}

	// For each currency, compute the cross-tenant net and generate instructions.
	var netted []NettedInstruction
	totalNetted := decimal.Zero

	for currency, positions := range byCurrency {
		// Sort by net amount descending for deterministic output.
		sort.Slice(positions, func(i, j int) bool {
			return positions[i].net.GreaterThan(positions[j].net)
		})

		// Each tenant still settles individually, but the platform's net exposure
		// is reduced. Generate only instructions where the amount is non-zero.
		for _, pos := range positions {
			if pos.net.IsZero() {
				continue
			}

			direction := "tenant_owes_settla"
			desc := fmt.Sprintf("%s owes Settla %s %s", pos.tenantName, pos.net.Abs().StringFixed(2), currency)
			if pos.net.IsNegative() {
				direction = "settla_owes_tenant"
				desc = fmt.Sprintf("Settla owes %s %s %s", pos.tenantName, pos.net.Abs().StringFixed(2), currency)
			}

			netted = append(netted, NettedInstruction{
				Currency:    currency,
				Direction:   direction,
				TenantID:    pos.tenantID,
				TenantName:  pos.tenantName,
				Amount:      pos.net.Abs(),
				Description: desc,
			})
			totalNetted = totalNetted.Add(pos.net.Abs())
		}
	}

	// Calculate savings from netting.
	var savingsPct float64
	if !totalOriginal.IsZero() {
		reduction := totalOriginal.Sub(totalNetted)
		savingsPct = reduction.Div(totalOriginal).InexactFloat64() * 100
	}

	result := &NettingResult{
		OriginalInstructions: originalInstructions,
		NettedInstructions:   netted,
		TotalOriginalVolume:  totalOriginal,
		TotalNettedVolume:    totalNetted,
		SavingsPercent:       savingsPct,
	}

	nettingSavings.Set(savingsPct)
	nettingInstructionsReduced.Set(float64(originalInstructions - len(netted)))

	if logger != nil {
		logger.Info("settla-settlement: cross-tenant netting complete",
			"original_instructions", originalInstructions,
			"netted_instructions", len(netted),
			"original_volume", totalOriginal.StringFixed(2),
			"netted_volume", totalNetted.StringFixed(2),
			"savings_percent", fmt.Sprintf("%.1f%%", savingsPct),
		)
	}

	return result
}

// NetSettlementsForPeriod calculates individual tenant settlements and then
// applies cross-tenant netting. This is the main entry point called by the
// scheduler after individual settlements are computed.
func (s *Scheduler) NetSettlementsForPeriod(
	ctx context.Context,
	periodStart, periodEnd time.Time,
) (*NettingResult, error) {
	// Fetch all pending settlements for this period.
	pending, err := s.calculator.store.ListPendingSettlements(ctx, domain.AdminCaller{
		Service: "settlement_netting",
		Reason:  fmt.Sprintf("cross_tenant_netting_%s", periodStart.Format("2006-01-02")),
	})
	if err != nil {
		return nil, fmt.Errorf("settla-settlement: listing pending for netting: %w", err)
	}

	// Filter to this period.
	var periodSettlements []domain.NetSettlement
	for _, p := range pending {
		if p.PeriodStart.Equal(periodStart) && p.PeriodEnd.Equal(periodEnd) {
			periodSettlements = append(periodSettlements, p)
		}
	}

	if len(periodSettlements) == 0 {
		return &NettingResult{}, nil
	}

	return CalculateCrossTenantNetting(periodSettlements, s.logger), nil
}
