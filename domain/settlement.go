package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// NetSettlement represents a computed net settlement for a tenant over a period.
type NetSettlement struct {
	ID            uuid.UUID               `json:"id"`
	TenantID      uuid.UUID               `json:"tenant_id"`
	TenantName    string                  `json:"tenant_name"`
	PeriodStart   time.Time               `json:"period_start"`
	PeriodEnd     time.Time               `json:"period_end"`
	Corridors     []CorridorPosition      `json:"corridors"`
	NetByCurrency []CurrencyNet           `json:"net_by_currency"`
	TotalFeesUSD  decimal.Decimal         `json:"total_fees_usd"`
	Instructions  []SettlementInstruction `json:"instructions"`
	FeeScheduleSnapshot *FeeSchedule           `json:"fee_schedule_snapshot,omitempty"` // tenant fee schedule at time of calculation, for audit trail
	Status              string                  `json:"status"`                          // "pending", "approved", "settled", "overdue"
	DueDate             *time.Time              `json:"due_date"`                        // when payment is expected
	CreatedAt           time.Time               `json:"created_at"`
}

// CorridorPosition aggregates transfer volume for a single corridor (e.g., GBP->NGN).
type CorridorPosition struct {
	SourceCurrency string          `json:"source_currency"`
	DestCurrency   string          `json:"dest_currency"`
	TotalSource    decimal.Decimal `json:"total_source"`
	TotalDest      decimal.Decimal `json:"total_dest"`
	TransferCount  int             `json:"transfer_count"`
}

// CurrencyNet represents the net position for a single currency.
// A positive amount means the tenant owes Settla; negative means Settla owes the tenant.
type CurrencyNet struct {
	Currency string          `json:"currency"`
	Inflows  decimal.Decimal `json:"inflows"`  // sum of amounts received in this currency
	Outflows decimal.Decimal `json:"outflows"` // sum of amounts sent in this currency
	Net      decimal.Decimal `json:"net"`      // inflows - outflows
}

// SettlementInstruction is a human-readable directive for settling a net position.
type SettlementInstruction struct {
	Direction   string          `json:"direction"`   // "tenant_owes_settla" or "settla_owes_tenant"
	Currency    string          `json:"currency"`
	Amount      decimal.Decimal `json:"amount"`
	Description string          `json:"description"` // e.g., "Fincra owes Settla 1.35B NGN"
}

// SettlementCycleStatus represents the lifecycle state of a settlement cycle.
type SettlementCycleStatus string

const (
	SettlementCycleStatusOpen      SettlementCycleStatus = "OPEN"
	SettlementCycleStatusClosed    SettlementCycleStatus = "CLOSED"
	SettlementCycleStatusSettled   SettlementCycleStatus = "SETTLED"
	SettlementCycleStatusCancelled SettlementCycleStatus = "CANCELLED"
)

// SettlementCycle aggregates net settlements over a time period (typically daily).
type SettlementCycle struct {
	ID          uuid.UUID             `json:"id"`
	PeriodStart time.Time             `json:"period_start"`
	PeriodEnd   time.Time             `json:"period_end"`
	Status      SettlementCycleStatus `json:"status"`
	Settlements []NetSettlement       `json:"settlements"`
	CreatedAt   time.Time             `json:"created_at"`
}
