package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/shopspring/decimal"

	settlagrpc "github.com/intellect4all/settla/api/grpc"
	"github.com/intellect4all/settla/core"
	"github.com/intellect4all/settla/core/compensation"
	"github.com/intellect4all/settla/core/recovery"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/ledger"
	"github.com/intellect4all/settla/node/messaging"
	"github.com/intellect4all/settla/node/outbox"
	"github.com/intellect4all/settla/node/worker"
	"github.com/intellect4all/settla/observability"
	"github.com/intellect4all/settla/observability/healthcheck"
	"github.com/intellect4all/settla/rail/provider"
	"github.com/intellect4all/settla/rail/provider/mock"
	"github.com/intellect4all/settla/rail/router"
	"github.com/intellect4all/settla/resilience"
	"github.com/intellect4all/settla/resilience/drain"
	"github.com/intellect4all/settla/resilience/featureflag"
	"github.com/intellect4all/settla/store/ledgerdb"
	"github.com/intellect4all/settla/store/transferdb"
	"github.com/intellect4all/settla/store/treasurydb"
	"github.com/intellect4all/settla/treasury"
)

const nodeVersion = "0.7.0"

func main() {
	logger := observability.NewLogger("settla-node", nodeVersion)
	metrics := observability.NewMetrics()

	logger.Info("settla-node starting...")

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// ── Configuration ──────────────────────────────────────────────
	natsURL := envOrDefault("SETTLA_NATS_URL", "nats://localhost:4222")
	numPartitions := envIntOrDefault("SETTLA_NODE_PARTITIONS", messaging.DefaultPartitions)
	partitionID := envIntOrDefault("SETTLA_NODE_PARTITION_ID", -1) // -1 = all partitions (dev mode)
	// SETTLA_NATS_REPLICAS controls JetStream stream replication factor.
	// 1 = dev/staging (single broker), 3 = production (3-node cluster, R=3).
	natsReplicas := envIntOrDefault("SETTLA_NATS_REPLICAS", 1)

	// ── Config validation ─────────────────────────────────────────
	if partitionID >= 0 && partitionID >= numPartitions {
		logger.Error("settla-node: SETTLA_NODE_PARTITION_ID must be < SETTLA_NODE_PARTITIONS",
			"partition_id", partitionID,
			"num_partitions", numPartitions,
		)
		os.Exit(1)
	}
	if natsReplicas < 1 || natsReplicas > 5 {
		logger.Error("settla-node: SETTLA_NATS_REPLICAS must be between 1 and 5",
			"nats_replicas", natsReplicas,
		)
		os.Exit(1)
	}
	if numPartitions < 1 || numPartitions > 256 {
		logger.Warn("settla-node: unusual SETTLA_NODE_PARTITIONS value",
			"num_partitions", numPartitions,
		)
	}

	// ── NATS client ────────────────────────────────────────────────
	natsClient, err := messaging.NewClient(natsURL, numPartitions, logger,
		messaging.WithReplicas(natsReplicas),
	)
	if err != nil {
		logger.Error("settla-node: failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	defer natsClient.Close()

	if err := natsClient.EnsureStreams(ctx); err != nil {
		logger.Error("settla-node: failed to ensure streams", "error", err)
		os.Exit(1)
	}

	// ── Database connections ──────────────────────────────────────
	transferDBURL := envOrDefault("SETTLA_TRANSFER_DB_URL",
		"postgres://settla:settla@localhost:5434/settla_transfer?sslmode=disable")
	transferPool, err := pgxpool.New(ctx, transferDBURL)
	if err != nil {
		logger.Error("settla-node: failed to connect to transfer DB", "url", transferDBURL, "error", err)
		os.Exit(1)
	}
	defer transferPool.Close()
	logger.Info("settla-node: connected to transfer DB")

	ledgerDBURL := envOrDefault("SETTLA_LEDGER_DB_URL",
		"postgres://settla:settla@localhost:5433/settla_ledger?sslmode=disable")
	ledgerPool, err := pgxpool.New(ctx, ledgerDBURL)
	if err != nil {
		logger.Warn("settla-node: ledger DB unavailable, PG read path disabled", "error", err)
		ledgerPool = nil
	} else {
		defer ledgerPool.Close()
		logger.Info("settla-node: connected to ledger DB")
	}

	treasuryDBURL := envOrDefault("SETTLA_TREASURY_DB_URL",
		"postgres://settla:settla@localhost:5435/settla_treasury?sslmode=disable")
	treasuryPool, err := pgxpool.New(ctx, treasuryDBURL)
	if err != nil {
		logger.Warn("settla-node: treasury DB unavailable, using stub store", "error", err)
		treasuryPool = nil
	} else {
		defer treasuryPool.Close()
		logger.Info("settla-node: connected to treasury DB")
	}

	// ── Stores ────────────────────────────────────────────────────
	transferQueries := transferdb.New(transferPool)
	transferStore := transferdb.NewTransferStoreAdapter(transferQueries, transferPool)
	tenantStore := transferdb.NewTenantStoreAdapter(transferQueries)

	var treasuryStore treasury.Store
	if treasuryPool != nil {
		treasuryStore = newPostgresTreasuryStore(treasurydb.New(treasuryPool), treasuryPool)
	} else {
		treasuryStore = &stubTreasuryStore{}
	}

	// ── Modules (for workers, NOT for engine) ────────────────────
	// The engine is a pure state machine and doesn't call these directly.
	// Workers use these modules to execute side effects from outbox intents.

	rawPublisher := messaging.NewPublisher(natsClient)

	// Wrap publisher with circuit breaker to prevent cascading failures when NATS is down.
	publisherCB := resilience.NewCircuitBreaker("nats-publisher",
		resilience.WithFailureThreshold(5),
		resilience.WithResetTimeout(10*time.Second),
		resilience.WithHalfOpenMax(3),
		resilience.WithSuccessThreshold(2),
	)
	publisher := messaging.NewCircuitBreakerPublisher(rawPublisher, publisherCB)

	// Ledger
	batchWindowMs := envIntOrDefault("SETTLA_LEDGER_BATCH_WINDOW_MS", 10)
	batchMaxSize := envIntOrDefault("SETTLA_LEDGER_BATCH_MAX_SIZE", 500)
	var ledgerSvc *ledger.Service
	if ledgerPool != nil {
		pgBackend := ledger.NewPGBackend(ledgerdb.New(ledgerPool), logger)
		ledgerSvc = ledger.NewService(nil, pgBackend, publisher, logger, metrics,
			ledger.WithBatchWindow(time.Duration(batchWindowMs)*time.Millisecond),
			ledger.WithBatchMaxSize(batchMaxSize),
		)
	} else {
		ledgerSvc = ledger.NewService(nil, nil, publisher, logger, metrics,
			ledger.WithBatchWindow(time.Duration(batchWindowMs)*time.Millisecond),
			ledger.WithBatchMaxSize(batchMaxSize),
		)
	}
	ledgerSvc.Start()
	defer ledgerSvc.Stop()

	// Treasury
	flushIntervalMs := envIntOrDefault("SETTLA_TREASURY_FLUSH_INTERVAL_MS", 100)
	treasurySvc := treasury.NewManager(treasuryStore, publisher, logger, metrics,
		treasury.WithFlushInterval(time.Duration(flushIntervalMs)*time.Millisecond),
	)
	if err := treasurySvc.LoadPositions(ctx); err != nil {
		logger.Error("settla-node: failed to load treasury positions", "error", err)
		os.Exit(1)
	}
	treasurySvc.Start()
	defer treasurySvc.Stop()
	logger.Info("settla-node: treasury loaded", "positions", treasurySvc.PositionCount())

	// Rail: provider registry
	providerReg := provider.NewRegistry()
	registerMockProviders(providerReg)

	// Router (quote-only — used by engine for quotes, not for execution)
	railRouter := router.NewRouter(providerReg, tenantStore, logger)
	coreRouterAdapter := router.NewCoreRouterAdapter(railRouter, tenantStore, logger)

	// Core engine (pure state machine — writes outbox entries atomically)
	engine := core.NewEngine(
		transferStore,
		tenantStore,
		coreRouterAdapter,
		logger,
		metrics,
	)

	// Feature flags: load from config file with background hot-reload (30s).
	flagConfigPath := envOrDefault("SETTLA_FEATURE_FLAGS_PATH", "deploy/config/features.json")
	flagManager := featureflag.NewManager(flagConfigPath, logger)
	go flagManager.Start(ctx)
	logger.Info("settla-node: feature flags loaded", "config_path", flagConfigPath)

	// ── Compensation executor (ENG-5) ────────────────────────────
	// CompensationStoreAdapter bridges compensation.CompensationStore → transferdb SQLC.
	compensationStore := transferdb.NewCompensationStoreAdapter(transferQueries)
	compensationExecutor := compensation.NewExecutor(compensationStore, engine, logger)
	_ = compensationExecutor // referenced by recovery detector indirectly via engine

	// ── Recovery detector (PROV-4 + ENG-5) ───────────────────────
	// ReviewStoreAdapter bridges recovery.ReviewStore + worker.BlockchainReviewStore → transferdb.
	reviewStore := transferdb.NewReviewStoreAdapter(transferQueries, transferPool)
	recoveryQueryStore := transferdb.NewRecoveryQueryAdapter(transferPool)
	recoveryDetector := recovery.NewDetector(
		recoveryQueryStore,
		reviewStore,
		engine,
		&stubProviderStatusChecker{},
		logger,
	)

	// ── Worker pool sizes ────────────────────────────────────────
	// Worker pool sizes tuned for 5K TPS peak (3K sustained).
	// Each pool defines the max concurrent handlers for that worker type.
	// At 5K TPS with 8 partitions = 625 events/sec per partition.
	// With ~10ms average handler latency, pool of 8 supports ~800/sec per partition.
	poolTransfer := envIntOrDefault("SETTLA_WORKER_POOL_TRANSFER", 8)
	poolProvider := envIntOrDefault("SETTLA_WORKER_POOL_PROVIDER", 16)
	poolBlockchain := envIntOrDefault("SETTLA_WORKER_POOL_BLOCKCHAIN", 16)
	poolLedger := envIntOrDefault("SETTLA_WORKER_POOL_LEDGER", 8)
	poolTreasury := envIntOrDefault("SETTLA_WORKER_POOL_TREASURY", 8)
	poolWebhook := envIntOrDefault("SETTLA_WORKER_POOL_WEBHOOK", 32)
	poolInboundWH := envIntOrDefault("SETTLA_WORKER_POOL_INBOUND_WH", 8)

	logger.Info("settla-node: worker pool sizes",
		"transfer", poolTransfer,
		"provider", poolProvider,
		"blockchain", poolBlockchain,
		"ledger", poolLedger,
		"treasury", poolTreasury,
		"webhook", poolWebhook,
		"inbound_wh", poolInboundWH,
	)

	// ── Provider maps (for dedicated workers) ────────────────────
	onRampProviders := buildOnRampMap(providerReg, logger)
	offRampProviders := buildOffRampMap(providerReg, logger)
	blockchainClients := buildBlockchainMap(providerReg, logger)

	// Provider transfer store (DB-backed for atomic CHECK-BEFORE-CALL)
	providerTxStore := transferdb.NewProviderTxAdapter(transferPool)

	// ── Graceful drain ──────────────────────────────────────────────
	drainTimeout := time.Duration(envIntOrDefault("SETTLA_DRAIN_TIMEOUT_MS", 15000)) * time.Millisecond
	drainer := drain.NewDrainer(drainTimeout, logger)

	// ── Deep health checks ──────────────────────────────────────────
	var checks []healthcheck.Check
	checks = append(checks, healthcheck.NewCallbackCheck("postgres_transfer", false,
		func(ctx context.Context) error { return transferPool.Ping(ctx) },
	))
	if ledgerPool != nil {
		checks = append(checks, healthcheck.NewCallbackCheck("postgres_ledger", false,
			func(ctx context.Context) error { return ledgerPool.Ping(ctx) },
		))
	}
	if treasuryPool != nil {
		checks = append(checks, healthcheck.NewCallbackCheck("postgres_treasury", false,
			func(ctx context.Context) error { return treasuryPool.Ping(ctx) },
		))
	}
	checks = append(checks, healthcheck.NewNATSCheck(func(_ context.Context) error {
		if !natsClient.Conn.IsConnected() {
			return fmt.Errorf("NATS connection not active")
		}
		return nil
	}))
	checks = append(checks, healthcheck.NewGoroutineCheck(100000))
	checker := healthcheck.NewChecker(logger, checks, healthcheck.WithVersion(nodeVersion))
	healthHandler := healthcheck.NewHandler(checker, 100000)

	// ── DLQ monitor (create early so HTTP handlers can reference it) ──
	dlqMetrics := worker.NewDLQMetrics()
	dlqMonitor := worker.NewDLQMonitor(natsClient, logger, dlqMetrics)

	// ── Metrics + health endpoint ──────────────────────────────────
	metricsPort := envOrDefault("SETTLA_NODE_METRICS_PORT", "9091")
	metricsMux := http.NewServeMux()
	healthHandler.Register(metricsMux)
	metricsMux.Handle("/metrics", promhttp.Handler())
	settlagrpc.RegisterDLQHandlers(metricsMux, newDLQInspectorAdapter(dlqMonitor), logger)
	metricsServer := &http.Server{
		Addr:    net.JoinHostPort("0.0.0.0", metricsPort),
		Handler: drain.HTTPMiddleware(drainer, metricsMux),
	}
	go func() {
		logger.Info("metrics + health server listening", "port", metricsPort)
		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("metrics server failed", "error", err)
		}
	}()

	// ── Start outbox relay ───────────────────────────────────────
	outboxStore := transferdb.NewOutboxRelayAdapter(transferQueries)
	natsPublisher := outbox.NewNATSPublisher(natsClient)
	relayMetrics := outbox.NewRelayMetrics()

	relayBatchSize := envIntOrDefault("SETTLA_RELAY_BATCH_SIZE", 500)
	relayPollMs := envIntOrDefault("SETTLA_RELAY_POLL_INTERVAL_MS", 20)

	relay := outbox.NewRelay(outboxStore, natsPublisher, logger,
		outbox.WithNumPartitions(numPartitions),
		outbox.WithBatchSize(int32(relayBatchSize)),
		outbox.WithPollInterval(time.Duration(relayPollMs)*time.Millisecond),
		outbox.WithMetrics(relayMetrics),
	)

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := relay.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("settla-node: outbox relay failed", "error", err)
		}
	}()
	logger.Info("settla-node: outbox relay started")

	// ── Start transfer workers (saga orchestrator) ───────────────
	var transferWorkers []*worker.TransferWorker

	if partitionID >= 0 {
		// Production: single partition per instance
		logger.Info("settla-node: starting single partition transfer worker",
			"partition", partitionID,
			"total_partitions", numPartitions,
		)
		w := worker.NewTransferWorker(partitionID, engine, natsClient, logger, metrics, messaging.WithPoolSize(poolTransfer))
		transferWorkers = append(transferWorkers, w)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w.Start(ctx); err != nil {
				logger.Error("settla-node: transfer worker failed", "partition", partitionID, "error", err)
			}
		}()
	} else {
		// Dev mode: handle all partitions in a single instance
		logger.Info("settla-node: dev mode — starting all partition transfer workers",
			"total_partitions", numPartitions,
		)
		for i := range numPartitions {
			w := worker.NewTransferWorker(i, engine, natsClient, logger, metrics, messaging.WithPoolSize(poolTransfer))
			transferWorkers = append(transferWorkers, w)
			wg.Add(1)
			go func(partition int) {
				defer wg.Done()
				if err := w.Start(ctx); err != nil {
					logger.Error("settla-node: transfer worker failed", "partition", partition, "error", err)
				}
			}(i)
		}
	}

	// ── Start dedicated workers (side-effect executors) ──────────

	// Treasury worker: executes reserve/release intents
	treasuryWorker := worker.NewTreasuryWorker(treasurySvc, natsClient, logger, messaging.WithPoolSize(poolTreasury))
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := treasuryWorker.Start(ctx); err != nil {
			logger.Error("settla-node: treasury worker failed", "error", err)
		}
	}()
	logger.Info("settla-node: treasury worker started")

	// Ledger worker: executes ledger post/reverse intents (wrapped with circuit breaker)
	ledgerCB := resilience.NewCircuitBreaker("ledger",
		resilience.WithFailureThreshold(5),
		resilience.WithResetTimeout(30*time.Second),
	)
	ledgerWithCB := resilience.NewCircuitBreakerLedger(ledgerSvc, ledgerCB)

	// stoppable collects workers that need graceful shutdown.
	type stoppable interface{ Stop() }
	var partitionedWorkers []stoppable

	// Provider, Ledger, InboundWebhook, Blockchain, and Webhook workers are partitioned like TransferWorker.
	startPartitionedWorkers := func(startPartition, endPartition int) {
		for i := startPartition; i < endPartition; i++ {
			p := i // capture for goroutine

			// Ledger worker
			lw := worker.NewLedgerWorker(p, ledgerWithCB, natsClient, logger, messaging.WithPoolSize(poolLedger))
			partitionedWorkers = append(partitionedWorkers, lw)
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := lw.Start(ctx); err != nil {
					logger.Error("settla-node: ledger worker failed", "partition", p, "error", err)
				}
			}()

			// Provider worker
			pw := worker.NewProviderWorker(
				p, onRampProviders, offRampProviders, providerTxStore, engine, natsClient, logger,
				messaging.WithPoolSize(poolProvider),
			)
			partitionedWorkers = append(partitionedWorkers, pw)
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := pw.Start(ctx); err != nil {
					logger.Error("settla-node: provider worker failed", "partition", p, "error", err)
				}
			}()

			// Inbound webhook worker
			iww := worker.NewInboundWebhookWorker(
				p, providerTxStore, engine, natsClient, logger,
				messaging.WithPoolSize(poolInboundWH),
			)
			partitionedWorkers = append(partitionedWorkers, iww)
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := iww.Start(ctx); err != nil {
					logger.Error("settla-node: inbound webhook worker failed", "partition", p, "error", err)
				}
			}()

			// Blockchain worker: executes on-chain transaction intents (CHECK-BEFORE-CALL)
			bw := worker.NewBlockchainWorker(
				p, blockchainClients, providerTxStore, engine, natsClient, logger,
				reviewStore,
				messaging.WithPoolSize(poolBlockchain),
			)
			partitionedWorkers = append(partitionedWorkers, bw)
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := bw.Start(ctx); err != nil {
					logger.Error("settla-node: blockchain worker failed", "partition", p, "error", err)
				}
			}()

			// Webhook worker: delivers webhooks to tenant endpoints
			ww := worker.NewWebhookWorker(
				p, tenantStore, natsClient, logger, nil,
				messaging.WithPoolSize(poolWebhook),
			)
			partitionedWorkers = append(partitionedWorkers, ww)
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := ww.Start(ctx); err != nil {
					logger.Error("settla-node: webhook worker failed", "partition", p, "error", err)
				}
			}()
		}
	}

	if partitionID >= 0 {
		// Production: single partition per instance
		logger.Info("settla-node: starting partitioned workers (provider, ledger, inbound webhook, blockchain, webhook)",
			"partition", partitionID, "total_partitions", numPartitions)
		startPartitionedWorkers(partitionID, partitionID+1)
	} else {
		// Dev mode: handle all partitions in a single instance
		logger.Info("settla-node: dev mode — starting all partition workers (provider, ledger, inbound webhook, blockchain, webhook)",
			"total_partitions", numPartitions)
		startPartitionedWorkers(0, numPartitions)
	}

	// ── Start recovery detector ──────────────────────────────────
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := recoveryDetector.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("settla-node: recovery detector failed", "error", err)
		}
	}()
	logger.Info("settla-node: recovery detector started")

	// ── Start DLQ monitor ────────────────────────────────────────
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := dlqMonitor.Start(ctx); err != nil && ctx.Err() == nil {
			logger.Error("settla-node: DLQ monitor failed", "error", err)
		}
	}()
	logger.Info("settla-node: DLQ monitor started")

	// ── Consumer lag metrics ──────────────────────────────────────
	consumerLagGauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "settla_nats_consumer_lag",
		Help: "Number of unprocessed messages per NATS consumer",
	}, []string{"stream", "consumer"})
	prometheus.MustRegister(consumerLagGauge)

	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				for _, sd := range messaging.AllStreams() {
					stream, err := natsClient.JS.Stream(ctx, sd.Name)
					if err != nil {
						continue
					}
					for ci := range stream.ListConsumers(ctx).Info() {
						lag := ci.NumPending + uint64(ci.NumAckPending)
						consumerLagGauge.WithLabelValues(sd.Name, ci.Name).Set(float64(lag))
					}
				}
			}
		}
	}()

	checker.MarkStartupComplete()

	logger.Info("settla-node ready",
		"transfer_workers", len(transferWorkers),
		"dedicated_workers", 8, // treasury, ledger, provider, blockchain, webhook, inbound-webhook, recovery-detector, dlq-monitor
		"outbox_relay", true,
	)

	<-ctx.Done()

	logger.Info("settla-node shutting down...")

	// 1. Drain: reject new health/metrics requests during shutdown.
	if err := drainer.Drain(); err != nil {
		logger.Warn("settla-node: drain incomplete", "error", err)
	}

	// 2. Stop all transfer workers
	for _, w := range transferWorkers {
		w.Stop()
	}
	// 3. Stop dedicated workers
	treasuryWorker.Stop()
	for _, w := range partitionedWorkers {
		w.Stop()
	}
	dlqMonitor.Stop()
	// recoveryDetector stops via context cancellation (Run respects ctx)
	// Relay stops via context cancellation

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := metricsServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("settla-node: metrics server shutdown error", "error", err)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		logger.Info("settla-node shutdown complete")
	case <-time.After(60 * time.Second):
		logger.Error("settla-node shutdown timed out after 60s")
	}
}

// buildOnRampMap extracts on-ramp providers from the registry into a map[string]domain.OnRampProvider.
func buildOnRampMap(reg *provider.Registry, logger *slog.Logger) map[string]domain.OnRampProvider {
	m := make(map[string]domain.OnRampProvider)
	for _, id := range reg.ListOnRampIDs(context.Background()) {
		p, err := reg.GetOnRamp(id)
		if err != nil {
			logger.Error("settla-node: failed to get on-ramp provider", "id", id, "error", err)
			continue
		}
		m[id] = p
	}
	return m
}

// buildOffRampMap extracts off-ramp providers from the registry into a map[string]domain.OffRampProvider.
func buildOffRampMap(reg *provider.Registry, logger *slog.Logger) map[string]domain.OffRampProvider {
	m := make(map[string]domain.OffRampProvider)
	for _, id := range reg.ListOffRampIDs(context.Background()) {
		p, err := reg.GetOffRamp(id)
		if err != nil {
			logger.Error("settla-node: failed to get off-ramp provider", "id", id, "error", err)
			continue
		}
		m[id] = p
	}
	return m
}

// buildBlockchainMap extracts blockchain clients from the registry into a map[string]domain.BlockchainClient.
func buildBlockchainMap(reg *provider.Registry, logger *slog.Logger) map[string]domain.BlockchainClient {
	m := make(map[string]domain.BlockchainClient)
	for _, chain := range reg.ListBlockchainChains() {
		c, err := reg.GetBlockchainClient(chain)
		if err != nil {
			logger.Error("settla-node: failed to get blockchain client", "chain", chain, "error", err)
			continue
		}
		m[chain] = c
	}
	return m
}

// registerMockProviders populates the registry with mock providers for development.
func registerMockProviders(reg *provider.Registry) {
	delayMs := envIntOrDefault("SETTLA_MOCK_DELAY_MS", 500)
	delay := time.Duration(delayMs) * time.Millisecond

	reg.RegisterOnRamp(mock.NewOnRampProvider("mock-onramp-gbp",
		[]domain.CurrencyPair{{From: domain.CurrencyGBP, To: domain.CurrencyUSDT}},
		decimal.NewFromFloat(1.25), decimal.NewFromFloat(0.50), delay,
	))
	reg.RegisterOnRamp(mock.NewOnRampProvider("mock-onramp-ngn",
		[]domain.CurrencyPair{{From: domain.CurrencyNGN, To: domain.CurrencyUSDT}},
		decimal.NewFromFloat(0.00065), decimal.NewFromFloat(0.50), delay,
	))
	reg.RegisterOffRamp(mock.NewOffRampProvider("mock-offramp-ngn",
		[]domain.CurrencyPair{{From: domain.CurrencyUSDT, To: domain.CurrencyNGN}},
		decimal.NewFromFloat(1550), decimal.NewFromFloat(0.50), delay,
	))
	reg.RegisterOffRamp(mock.NewOffRampProvider("mock-offramp-gbp",
		[]domain.CurrencyPair{{From: domain.CurrencyUSDT, To: domain.CurrencyGBP}},
		decimal.NewFromFloat(0.80), decimal.NewFromFloat(0.30), delay,
	))
	reg.RegisterBlockchainClient(mock.NewBlockchainClient("tron", decimal.NewFromFloat(0.10)))
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envIntOrDefault(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}
