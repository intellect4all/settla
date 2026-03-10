package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// DashboardMetrics contains aggregated business KPIs for the tenant portal dashboard.
// These are domain read-model DTOs (not infrastructure/prometheus metrics).
type DashboardMetrics struct {
	TransfersToday int64
	VolumeTodayUSD decimal.Decimal
	CompletedToday int64
	FailedToday    int64

	Transfers7D int64
	Volume7DUSD decimal.Decimal
	Fees7DUSD   decimal.Decimal

	Transfers30D   int64
	Volume30DUSD   decimal.Decimal
	Fees30DUSD     decimal.Decimal
	SuccessRate30D decimal.Decimal
}

// TransferStatsBucket holds time-bucketed transfer statistics.
type TransferStatsBucket struct {
	Timestamp time.Time
	Total     int64
	Completed int64
	Failed    int64
	VolumeUSD decimal.Decimal
	FeesUSD   decimal.Decimal
}

// StatusCount holds a transfer status and its count.
type StatusCount struct {
	Status string
	Count  int64
}

// CorridorMetrics holds per-corridor analytics.
type CorridorMetrics struct {
	SourceCurrency string
	DestCurrency   string
	TransferCount  int64
	VolumeUSD      decimal.Decimal
	FeesUSD        decimal.Decimal
	Completed      int64
	Failed         int64
	SuccessRate    decimal.Decimal
	AvgLatencyMs   int32
}

// LatencyPercentiles holds transfer completion latency distribution.
type LatencyPercentiles struct {
	SampleCount int64
	P50Ms       int32
	P90Ms       int32
	P95Ms       int32
	P99Ms       int32
}

// VolumeComparison holds current vs. previous period comparison data.
type VolumeComparison struct {
	CurrentCount      int64
	CurrentVolumeUSD  decimal.Decimal
	CurrentFeesUSD    decimal.Decimal
	PreviousCount     int64
	PreviousVolumeUSD decimal.Decimal
	PreviousFeesUSD   decimal.Decimal
}

// ActivityItem represents a recent transfer activity event.
type ActivityItem struct {
	TransferID     string
	ExternalRef    string
	Status         string
	SourceCurrency string
	SourceAmount   decimal.Decimal
	DestCurrency   string
	DestAmount     decimal.Decimal
	UpdatedAt      time.Time
	FailureReason  string
}

// FeeReportEntry holds fee data grouped by currency corridor.
type FeeReportEntry struct {
	SourceCurrency string
	DestCurrency   string
	TransferCount  int64
	TotalVolumeUSD decimal.Decimal
	OnRampFeesUSD  decimal.Decimal
	OffRampFeesUSD decimal.Decimal
	NetworkFeesUSD decimal.Decimal
	TotalFeesUSD   decimal.Decimal
}
