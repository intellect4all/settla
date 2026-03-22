package domain

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// FeeBreakdownEntry holds fee revenue grouped by currency corridor.
type FeeBreakdownEntry struct {
	SourceCurrency string
	DestCurrency   string
	TransferCount  int64
	VolumeUSD      decimal.Decimal
	OnRampFeesUSD  decimal.Decimal
	OffRampFeesUSD decimal.Decimal
	NetworkFeesUSD decimal.Decimal
	TotalFeesUSD   decimal.Decimal
}

// ProviderPerformance holds provider-level performance metrics.
type ProviderPerformance struct {
	Provider         string
	SourceCurrency   string
	DestCurrency     string
	TransactionCount int64
	Completed        int64
	Failed           int64
	SuccessRate      decimal.Decimal
	AvgSettlementMs  int32
	TotalVolume      decimal.Decimal
}

// DepositAnalytics holds aggregated deposit session metrics.
type DepositAnalytics struct {
	TotalSessions     int64
	CompletedSessions int64
	ExpiredSessions   int64
	FailedSessions    int64
	ConversionRate    decimal.Decimal
	TotalReceived     decimal.Decimal
	TotalFees         decimal.Decimal
	TotalNet          decimal.Decimal
}

// ReconciliationSummary holds system-wide reconciliation health stats.
type ReconciliationSummary struct {
	TotalRuns        int64
	ChecksPassed     int64
	ChecksFailed     int64
	PassRate         decimal.Decimal
	LastRunAt        *time.Time
	NeedsReviewCount int64
}

// DailySnapshot holds a pre-aggregated daily metric row.
type DailySnapshot struct {
	ID             uuid.UUID
	TenantID       uuid.UUID
	SnapshotDate   time.Time
	MetricType     string
	SourceCurrency Currency
	DestCurrency   Currency
	Provider       string
	TransferCount  int64
	CompletedCount int64
	FailedCount    int64
	VolumeUSD      decimal.Decimal
	FeesUSD        decimal.Decimal
	OnRampFeesUSD  decimal.Decimal
	OffRampFeesUSD decimal.Decimal
	NetworkFeesUSD decimal.Decimal
	AvgLatencyMs   int32
	P50LatencyMs   int32
	P90LatencyMs   int32
	P95LatencyMs   int32
	SuccessRate    decimal.Decimal
	CreatedAt      time.Time
}

// ExportJob tracks an async analytics export.
type ExportJob struct {
	ID                uuid.UUID
	TenantID          uuid.UUID
	Status            string
	ExportType        string
	Parameters        map[string]any
	FilePath          string
	DownloadURL       string
	DownloadExpiresAt *time.Time
	RowCount          int64
	ErrorMessage      string
	CreatedAt         time.Time
	CompletedAt       *time.Time
}
