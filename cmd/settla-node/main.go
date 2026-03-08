package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/shopspring/decimal"
	"github.com/intellect4all/settla/core"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/ledger"
	"github.com/intellect4all/settla/node/messaging"
	"github.com/intellect4all/settla/node/worker"
	"github.com/intellect4all/settla/observability"
	"github.com/intellect4all/settla/rail/provider"
	"github.com/intellect4all/settla/rail/provider/mock"
	"github.com/intellect4all/settla/rail/router"
	"github.com/intellect4all/settla/store/ledgerdb"
	"github.com/intellect4all/settla/store/transferdb"
	"github.com/intellect4all/settla/store/treasurydb"
	"github.com/intellect4all/settla/treasury"
)

const nodeVersion = "0.6.0"

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

	// ── NATS client ────────────────────────────────────────────────
	client, err := messaging.NewClient(natsURL, numPartitions, logger)
	if err != nil {
		logger.Error("settla-node: failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	defer client.Close()

	if err := client.EnsureStream(ctx); err != nil {
		logger.Error("settla-node: failed to ensure stream", "error", err)
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

	// ── NATS publisher ────────────────────────────────────────────
	publisher := messaging.NewPublisher(client)

	// ── Stores ────────────────────────────────────────────────────
	transferQueries := transferdb.New(transferPool)
	transferStore := transferdb.NewTransferStoreAdapter(transferQueries)
	tenantStore := transferdb.NewTenantStoreAdapter(transferQueries)

	var treasuryStore treasury.Store
	if treasuryPool != nil {
		treasuryStore = newPostgresTreasuryStore(treasurydb.New(treasuryPool))
	} else {
		treasuryStore = &stubTreasuryStore{}
	}

	// ── Modules ───────────────────────────────────────────────────
	batchWindowMs := envIntOrDefault("SETTLA_LEDGER_BATCH_WINDOW_MS", 10)
	var ledgerSvc *ledger.Service
	if ledgerPool != nil {
		pgBackend := ledger.NewPGBackend(ledgerdb.New(ledgerPool), logger)
		ledgerSvc = ledger.NewService(nil, pgBackend, publisher, logger, metrics,
			ledger.WithBatchWindow(time.Duration(batchWindowMs)*time.Millisecond),
		)
	} else {
		ledgerSvc = ledger.NewService(nil, nil, publisher, logger, metrics,
			ledger.WithBatchWindow(time.Duration(batchWindowMs)*time.Millisecond),
		)
	}
	ledgerSvc.Start()
	defer ledgerSvc.Stop()

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

	// Rail: provider registry with mock providers
	providerReg := provider.NewRegistry()
	registerMockProviders(providerReg)
	coreAdapter := &coreRegistryAdapter{reg: providerReg}

	// Router: smart routing with tenant fee schedules
	railRouter := router.NewRouter(providerReg, tenantStore, logger)
	coreRouterAdapter := router.NewCoreRouterAdapter(railRouter, tenantStore, logger)

	// Core engine (real, not stub)
	engine := core.NewEngine(
		transferStore,
		tenantStore,
		ledgerSvc,
		treasurySvc,
		coreRouterAdapter,
		coreAdapter,
		publisher,
		logger,
		metrics,
	)

	// ── Metrics endpoint ──────────────────────────────────────────
	metricsPort := envOrDefault("SETTLA_NODE_METRICS_PORT", "9091")
	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.Handler())
	metricsServer := &http.Server{
		Addr:    net.JoinHostPort("0.0.0.0", metricsPort),
		Handler: metricsMux,
	}
	go func() {
		logger.Info("metrics server listening", "port", metricsPort)
		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("metrics server failed", "error", err)
		}
	}()

	// ── Start workers ──────────────────────────────────────────────
	var workers []*worker.TransferWorker
	var wg sync.WaitGroup

	if partitionID >= 0 {
		// Production: single partition per instance
		logger.Info("settla-node: starting single partition worker",
			"partition", partitionID,
			"total_partitions", numPartitions,
		)
		w := worker.NewTransferWorker(partitionID, engine, client, logger, metrics)
		workers = append(workers, w)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w.Start(ctx); err != nil {
				logger.Error("settla-node: worker failed", "partition", partitionID, "error", err)
			}
		}()
	} else {
		// Dev mode: handle all partitions in a single instance
		logger.Info("settla-node: dev mode — starting all partition workers",
			"total_partitions", numPartitions,
		)
		for i := 0; i < numPartitions; i++ {
			w := worker.NewTransferWorker(i, engine, client, logger, metrics)
			workers = append(workers, w)
			wg.Add(1)
			go func(partition int) {
				defer wg.Done()
				if err := w.Start(ctx); err != nil {
					logger.Error("settla-node: worker failed", "partition", partition, "error", err)
				}
			}(i)
		}
	}

	logger.Info("settla-node ready")

	<-ctx.Done()

	logger.Info("settla-node shutting down...")

	for _, w := range workers {
		w.Stop()
	}
	wg.Wait()

	logger.Info("settla-node shutdown complete")
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
