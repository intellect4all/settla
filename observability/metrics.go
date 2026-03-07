package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics holds all Prometheus metrics for Settla services.
// Instantiated once per process and passed to modules via constructors.
type Metrics struct {
	// ── Transfer metrics ────────────────────────────────────────────
	TransfersTotal    *prometheus.CounterVec
	TransferDuration  *prometheus.HistogramVec

	// ── Ledger metrics ──────────────────────────────────────────────
	LedgerPostingsTotal   *prometheus.CounterVec
	LedgerTBWritesTotal   prometheus.Counter
	LedgerTBWriteLatency  prometheus.Histogram
	LedgerTBBatchSize     prometheus.Histogram
	LedgerPGSyncLag       prometheus.Gauge

	// ── Treasury metrics ────────────────────────────────────────────
	TreasuryReserveTotal   *prometheus.CounterVec
	TreasuryReserveLatency prometheus.Histogram
	TreasuryFlushLag       prometheus.Gauge
	TreasuryFlushDuration  prometheus.Histogram
	TreasuryBalance        *prometheus.GaugeVec
	TreasuryLocked         *prometheus.GaugeVec

	// ── Provider metrics ────────────────────────────────────────────
	ProviderRequestsTotal *prometheus.CounterVec
	ProviderLatency       *prometheus.HistogramVec

	// ── NATS metrics ────────────────────────────────────────────────
	NATSMessagesTotal      *prometheus.CounterVec
	NATSPartitionQueueDepth *prometheus.GaugeVec

	// ── gRPC server metrics ─────────────────────────────────────────
	GRPCRequestsTotal  *prometheus.CounterVec
	GRPCRequestLatency *prometheus.HistogramVec
}

// NewMetrics registers and returns all Settla Prometheus metrics.
func NewMetrics() *Metrics {
	return &Metrics{
		// Transfer
		TransfersTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "settla_transfers_total",
			Help: "Total transfers by tenant, status, and corridor.",
		}, []string{"tenant", "status", "corridor"}),

		TransferDuration: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "settla_transfer_duration_seconds",
			Help:    "Transfer end-to-end duration by corridor and chain.",
			Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60, 120},
		}, []string{"tenant", "corridor", "chain"}),

		// Ledger
		LedgerPostingsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "settla_ledger_postings_total",
			Help: "Total ledger postings by reference type.",
		}, []string{"reference_type"}),

		LedgerTBWritesTotal: promauto.NewCounter(prometheus.CounterOpts{
			Name: "settla_ledger_tb_writes_total",
			Help: "Total TigerBeetle write operations.",
		}),

		LedgerTBWriteLatency: promauto.NewHistogram(prometheus.HistogramOpts{
			Name:    "settla_ledger_tb_write_latency_seconds",
			Help:    "TigerBeetle write latency.",
			Buckets: []float64{0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05, 0.1},
		}),

		LedgerTBBatchSize: promauto.NewHistogram(prometheus.HistogramOpts{
			Name:    "settla_ledger_tb_batch_size",
			Help:    "TigerBeetle batch sizes.",
			Buckets: []float64{1, 5, 10, 50, 100, 250, 500},
		}),

		LedgerPGSyncLag: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "settla_ledger_pg_sync_lag_seconds",
			Help: "Postgres sync delay behind TigerBeetle.",
		}),

		// Treasury
		TreasuryReserveTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "settla_treasury_reserve_total",
			Help: "Total treasury reservations by tenant and currency.",
		}, []string{"tenant", "currency"}),

		TreasuryReserveLatency: promauto.NewHistogram(prometheus.HistogramOpts{
			Name:    "settla_treasury_reserve_latency_seconds",
			Help:    "Treasury reserve CAS loop latency (should be sub-microsecond).",
			Buckets: []float64{0.0000001, 0.0000005, 0.000001, 0.000005, 0.00001, 0.0001, 0.001},
		}),

		TreasuryFlushLag: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "settla_treasury_flush_lag_seconds",
			Help: "Time since last successful treasury flush to Postgres.",
		}),

		TreasuryFlushDuration: promauto.NewHistogram(prometheus.HistogramOpts{
			Name:    "settla_treasury_flush_duration_seconds",
			Help:    "Duration of treasury flush cycle.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1},
		}),

		TreasuryBalance: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "settla_treasury_balance",
			Help: "Current treasury balance by tenant, currency, and location.",
		}, []string{"tenant", "currency", "location"}),

		TreasuryLocked: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "settla_treasury_locked",
			Help: "Current treasury locked amount by tenant, currency, and location.",
		}, []string{"tenant", "currency", "location"}),

		// Provider
		ProviderRequestsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "settla_provider_requests_total",
			Help: "Provider requests by provider, operation, and status.",
		}, []string{"provider", "operation", "status"}),

		ProviderLatency: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "settla_provider_latency_seconds",
			Help:    "Provider call latency by provider and operation.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 2, 5, 10, 30},
		}, []string{"provider", "operation"}),

		// NATS
		NATSMessagesTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "settla_nats_messages_total",
			Help: "NATS messages processed by partition and status.",
		}, []string{"partition", "status"}),

		NATSPartitionQueueDepth: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "settla_nats_partition_queue_depth",
			Help: "NATS partition consumer pending message count.",
		}, []string{"partition"}),

		// gRPC
		GRPCRequestsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "settla_grpc_requests_total",
			Help: "gRPC requests by service, method, and status code.",
		}, []string{"service", "method", "code"}),

		GRPCRequestLatency: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "settla_grpc_request_latency_seconds",
			Help:    "gRPC request latency by service and method.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5},
		}, []string{"service", "method"}),
	}
}
