package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// LedgerSyncQueueFillRatio is a package-level Prometheus gauge that tracks
// the current fill ratio of the ledger sync queue (0.0 to 1.0).
var LedgerSyncQueueFillRatio = promauto.NewGauge(prometheus.GaugeOpts{
	Namespace: "settla",
	Subsystem: "ledger",
	Name:      "sync_queue_fill_ratio",
	Help:      "Current fill ratio of the ledger sync queue (0.0 to 1.0)",
})

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

	// ── Gateway HTTP metrics (registered by TS gateway at :3000/metrics,
	// mirrored here so Go HTTP ops handler can also emit the same metric
	// and Prometheus alert rules reference a single canonical name) ──────
	GatewayRequestsTotal   *prometheus.CounterVec
	GatewayRequestDuration *prometheus.HistogramVec

	// ── gRPC server metrics ─────────────────────────────────────────
	GRPCRequestsTotal  *prometheus.CounterVec
	GRPCRequestLatency *prometheus.HistogramVec

	// ── Deposit metrics ────────────────────────────────────────────
	DepositSessionsTotal    *prometheus.CounterVec
	DepositSessionDuration  *prometheus.HistogramVec
	DepositAddressPoolAvail *prometheus.GaugeVec
	DepositTxDetected       *prometheus.CounterVec
	DepositTxConfirmed      *prometheus.CounterVec
	ChainMonitorBlockLag    *prometheus.GaugeVec
	ChainMonitorPollLatency *prometheus.HistogramVec
	EmailNotificationsTotal *prometheus.CounterVec

	// ── Bank Deposit metrics ──────────────────────────────────────────
	BankDepositSessionsTotal    *prometheus.CounterVec
	BankDepositSessionDuration  *prometheus.HistogramVec
	VirtualAccountPoolAvailable *prometheus.GaugeVec

	// ── Treasury WAL metrics ───────────────────────────────────────────
	TreasuryWALWriteFailures           prometheus.Counter
	TreasuryUncommittedOps             prometheus.Gauge
	TreasuryConsecutiveFlushFailures   prometheus.Gauge

	// ── Outbox relay metrics ───────────────────────────────────────────
	OutboxRelayLag prometheus.Gauge

	// ── DLQ metrics ────────────────────────────────────────────────────
	DLQMessagesTotal *prometheus.CounterVec

	// ── Auth cache metrics ─────────────────────────────────────────────
	AuthCacheHitRate prometheus.Gauge

	// ── Cache metrics ──────────────────────────────────────────────────
	CacheHitsTotal   *prometheus.CounterVec
	CacheMissesTotal *prometheus.CounterVec

	// ── TigerBeetle disaggregated metrics ──────────────────────────────
	TigerBeetleResponseLatency         *prometheus.HistogramVec
	TigerBeetleAccountCreationFailures prometheus.Counter

	// ── NATS partition imbalance metrics ───────────────────────────────
	NATSPartitionLag *prometheus.GaugeVec

	// ── Ledger sync queue metrics ──────────────────────────────────────
	LedgerSyncQueueDepth   prometheus.Gauge
	LedgerSyncQueueDropped prometheus.Counter

	// ── Error tracking metrics ─────────────────────────────────────────
	ErrorsTotal *prometheus.CounterVec

	// ── Stuck transfer detection ───────────────────────────────────────
	StuckTransfersGauge    *prometheus.GaugeVec
	StuckTransferMaxAge    *prometheus.GaugeVec

	// ── Circuit breaker state ─────────────────────────────────────────
	CircuitBreakerState *prometheus.GaugeVec

	// ── Load shedding ─────────────────────────────────────────────────
	LoadSheddingRejectedTotal *prometheus.CounterVec

	// ── Outbox partition health ───────────────────────────────────────
	OutboxPartitionCount       prometheus.Gauge
	OutboxPartitionOldestAge   prometheus.Gauge
	OutboxPartitionDropErrors  prometheus.Counter

	// ── Deposit address generation ────────────────────────────────────
	DepositAddressGeneratedTotal *prometheus.CounterVec

	// ── Treasury recovery metrics ─────────────────────────────────────
	TreasuryRecoveryDuration        *prometheus.HistogramVec
	TreasuryPositionsRecoveredTotal prometheus.Counter

	// ── Database connection pool metrics ──────────────────────────────
	PgxPoolMaxConns     *prometheus.GaugeVec
	PgxPoolCurrentConns *prometheus.GaugeVec
	PgxPoolIdleConns    *prometheus.GaugeVec
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

		// Gateway HTTP (canonical metric — also emitted by TS gateway)
		GatewayRequestsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "settla_gateway_requests_total",
			Help: "Total HTTP requests by tenant, method, path, and status.",
		}, []string{"tenant", "method", "path", "status"}),

		GatewayRequestDuration: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "settla_gateway_request_duration_seconds",
			Help:    "HTTP request duration by method and path.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5},
		}, []string{"method", "path"}),

		// gRPC
		GRPCRequestsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "settla_grpc_requests_total",
			Help: "gRPC requests by service, method, status code, and error reason.",
		}, []string{"service", "method", "code", "reason"}),

		GRPCRequestLatency: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "settla_grpc_request_latency_seconds",
			Help:    "gRPC request latency by service and method.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5},
		}, []string{"service", "method"}),

		// Deposit
		DepositSessionsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "settla_deposit_sessions_total",
			Help: "Total deposit sessions by tenant, status, and chain.",
		}, []string{"tenant", "status", "chain"}),

		DepositSessionDuration: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "settla_deposit_session_duration_seconds",
			Help:    "Deposit session duration from creation to terminal state.",
			Buckets: []float64{60, 300, 600, 1800, 3600, 7200, 14400, 43200, 86400},
		}, []string{"tenant", "chain"}),

		DepositAddressPoolAvail: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "settla_deposit_address_pool_available",
			Help: "Available deposit addresses in pool by tenant and chain.",
		}, []string{"tenant", "chain"}),

		DepositTxDetected: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "settla_deposit_tx_detected_total",
			Help: "Total deposit transactions detected by chain and token.",
		}, []string{"chain", "token"}),

		DepositTxConfirmed: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "settla_deposit_tx_confirmed_total",
			Help: "Total deposit transactions confirmed by chain and token.",
		}, []string{"chain", "token"}),

		ChainMonitorBlockLag: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "settla_chain_monitor_block_lag",
			Help: "Chain monitor block lag (current block - last scanned block).",
		}, []string{"chain"}),

		ChainMonitorPollLatency: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "settla_chain_monitor_poll_latency_seconds",
			Help:    "Chain monitor poll latency by chain.",
			Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30},
		}, []string{"chain"}),

		EmailNotificationsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "settla_email_notifications_total",
			Help: "Total email notifications by tenant, event type, and status.",
		}, []string{"tenant", "event_type", "status"}),

		// Bank Deposit
		BankDepositSessionsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "settla_bank_deposit_sessions_total",
			Help: "Total bank deposit sessions by tenant and status.",
		}, []string{"tenant_id", "status"}),

		BankDepositSessionDuration: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "settla_bank_deposit_session_duration_seconds",
			Help:    "Bank deposit session duration from creation to terminal state.",
			Buckets: []float64{60, 300, 600, 1800, 3600, 7200, 14400, 43200, 86400},
		}, []string{"tenant_id"}),

		VirtualAccountPoolAvailable: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "settla_virtual_account_pool_available",
			Help: "Available virtual accounts in pool by tenant and currency.",
		}, []string{"tenant_id", "currency"}),

		// Treasury WAL
		TreasuryWALWriteFailures: promauto.NewCounter(prometheus.CounterOpts{
			Name: "settla_treasury_wal_write_failures_total",
			Help: "Total number of WAL write failures.",
		}),

		TreasuryUncommittedOps: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "settla_treasury_uncommitted_ops_pending",
			Help: "Number of uncommitted treasury operations pending flush.",
		}),

		TreasuryConsecutiveFlushFailures: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "settla_treasury_consecutive_flush_failures",
			Help: "Number of consecutive treasury flush cycles that failed. Resets to 0 on success.",
		}),

		// Outbox relay
		OutboxRelayLag: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "settla_outbox_relay_lag_seconds",
			Help: "Time lag between oldest unpublished entry and now.",
		}),

		// DLQ metric is registered in node/worker/dlq_monitor.go via promauto.
		// Do NOT re-register here — the DLQ monitor owns this metric.

		// Auth cache
		AuthCacheHitRate: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "settla_auth_cache_hit_rate",
			Help: "Current auth cache hit rate (0.0 to 1.0).",
		}),

		// Cache
		CacheHitsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "settla_cache_hits_total",
			Help: "Total cache hits.",
		}, []string{"cache_type"}),

		CacheMissesTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "settla_cache_misses_total",
			Help: "Total cache misses.",
		}, []string{"cache_type"}),

		// TigerBeetle disaggregated
		TigerBeetleResponseLatency: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "settla_tigerbeetle_response_latency_seconds",
			Help:    "TigerBeetle operation response latency.",
			Buckets: []float64{0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05, 0.1},
		}, []string{"operation"}),

		TigerBeetleAccountCreationFailures: promauto.NewCounter(prometheus.CounterOpts{
			Name: "settla_tigerbeetle_account_creation_failures_total",
			Help: "Total TigerBeetle account creation failures.",
		}),

		// NATS partition imbalance
		NATSPartitionLag: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "settla_nats_partition_lag_messages",
			Help: "Number of pending messages per NATS partition.",
		}, []string{"stream", "partition"}),

		// Ledger sync queue
		LedgerSyncQueueDepth: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "settla_ledger_sync_queue_depth",
			Help: "Current depth of the TigerBeetle-to-Postgres sync queue.",
		}),

		LedgerSyncQueueDropped: promauto.NewCounter(prometheus.CounterOpts{
			Name: "settla_ledger_sync_queue_dropped_total",
			Help: "Total entries dropped from sync queue due to overflow.",
		}),

		// Error tracking
		ErrorsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "settla_errors_total",
			Help: "Total domain errors by module, error code, and retriability.",
		}, []string{"module", "code", "retriable"}),

		// Stuck transfer detection
		StuckTransfersGauge: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "settla_stuck_transfers",
			Help: "Number of transfers stuck in non-terminal states by status.",
		}, []string{"status"}),

		StuckTransferMaxAge: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "settla_stuck_transfer_max_age_seconds",
			Help: "Age of oldest stuck transfer by status.",
		}, []string{"status"}),

		// Circuit breaker state is registered in resilience/circuitbreaker.go via promauto.
		// Do NOT re-register here — the resilience package owns this metric.
		// CircuitBreakerState is kept as a nil field; callers should use the
		// resilience package's cbStateGauge directly.

		// Load shedding metric is registered in resilience/loadshed.go via promauto.
		// Do NOT re-register here — the resilience package owns this metric.

		// Outbox partition health
		OutboxPartitionCount: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "settla_outbox_partition_count",
			Help: "Current number of outbox partitions.",
		}),

		OutboxPartitionOldestAge: promauto.NewGauge(prometheus.GaugeOpts{
			Name: "settla_outbox_partition_oldest_age_days",
			Help: "Age in days of the oldest outbox partition.",
		}),

		OutboxPartitionDropErrors: promauto.NewCounter(prometheus.CounterOpts{
			Name: "settla_outbox_partition_drop_errors_total",
			Help: "Total errors encountered when dropping old outbox partitions.",
		}),

		// Deposit address generation
		DepositAddressGeneratedTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "settla_deposit_address_generated_total",
			Help: "Total deposit addresses generated by tenant, chain, and status.",
		}, []string{"tenant", "chain", "status"}),

		// Treasury recovery
		TreasuryRecoveryDuration: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "settla_treasury_recovery_duration_seconds",
			Help:    "Time taken to recover treasury positions after pod restart.",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30},
		}, []string{"phase"}),

		TreasuryPositionsRecoveredTotal: promauto.NewCounter(prometheus.CounterOpts{
			Name: "settla_treasury_positions_recovered_total",
			Help: "Total number of treasury positions recovered from DB after pod restart.",
		}),

		// Database connection pool
		PgxPoolMaxConns: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "settla_pgxpool_max_conns",
			Help: "Maximum number of connections in the pgxpool.",
		}, []string{"db"}),

		PgxPoolCurrentConns: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "settla_pgxpool_current_conns",
			Help: "Current number of connections in the pgxpool (acquired + idle).",
		}, []string{"db"}),

		PgxPoolIdleConns: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "settla_pgxpool_idle_conns",
			Help: "Number of idle connections in the pgxpool.",
		}, []string{"db"}),
	}
}
