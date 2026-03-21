package main

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"

	settlagrpc "github.com/intellect4all/settla/api/grpc"
	"github.com/intellect4all/settla/core"
	settladb "github.com/intellect4all/settla/db"
	"github.com/intellect4all/settla/db/automigrate"
	bankdepositcore "github.com/intellect4all/settla/core/bankdeposit"
	depositcore "github.com/intellect4all/settla/core/deposit"
	paymentlinkcore "github.com/intellect4all/settla/core/paymentlink"
	"github.com/intellect4all/settla/core/analytics"
	"github.com/intellect4all/settla/core/maintenance"
	"github.com/intellect4all/settla/core/reconciliation"
	"github.com/intellect4all/settla/core/settlement"
	"github.com/intellect4all/settla/domain"
	pb "github.com/intellect4all/settla/gen/settla/v1"
	"github.com/intellect4all/settla/ledger"
	"github.com/intellect4all/settla/node/messaging"
	"github.com/intellect4all/settla/observability"
	"github.com/intellect4all/settla/observability/healthcheck"
	"github.com/intellect4all/settla/observability/synthetic"
	"github.com/intellect4all/settla/rail/blockchain"
	"github.com/intellect4all/settla/rail/provider"
	"github.com/intellect4all/settla/rail/provider/mock"
	settlaprovider "github.com/intellect4all/settla/rail/provider/settla"
	"github.com/intellect4all/settla/rail/router"
	"github.com/intellect4all/settla/resilience"
	"github.com/intellect4all/settla/resilience/drain"
	"github.com/intellect4all/settla/resilience/featureflag"
	"github.com/intellect4all/settla/store/ledgerdb"
	"github.com/intellect4all/settla/store/transferdb"
	"github.com/intellect4all/settla/store/treasurydb"
	"github.com/intellect4all/settla/treasury"
)

const version = "0.6.0"

func main() {
	logger := observability.NewLogger("settla-server", version)
	metrics := observability.NewMetrics()

	logger.Info("settla-server starting...")

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// ── Infrastructure ──────────────────────────────────────────────

	// Transfer DB (PgBouncer :6434)
	transferDBURL := envOrDefault("SETTLA_TRANSFER_DB_URL",
		"postgres://settla:settla@localhost:6434/settla_transfer?sslmode=disable")
	transferPool, err := newPgxPool(ctx, transferDBURL)
	if err != nil {
		logger.Error("settla-server: failed to connect to transfer DB", "url", transferDBURL, "error", err)
		os.Exit(1)
	}
	defer transferPool.Close()
	logger.Info("settla-server: connected to transfer DB")

	// Ledger DB (PgBouncer :6433)
	ledgerDBURL := envOrDefault("SETTLA_LEDGER_DB_URL",
		"postgres://settla:settla@localhost:6433/settla_ledger?sslmode=disable")
	ledgerPool, err := newPgxPool(ctx, ledgerDBURL)
	if err != nil {
		logger.Warn("settla-server: ledger DB unavailable, PG read path disabled", "error", err)
		ledgerPool = nil
	} else {
		defer ledgerPool.Close()
		logger.Info("settla-server: connected to ledger DB")
	}

	// Treasury DB (PgBouncer :6435)
	treasuryDBURL := envOrDefault("SETTLA_TREASURY_DB_URL",
		"postgres://settla:settla@localhost:6435/settla_treasury?sslmode=disable")
	treasuryPool, err := newPgxPool(ctx, treasuryDBURL)
	if err != nil {
		logger.Warn("settla-server: treasury DB unavailable, using stub store", "error", err)
		treasuryPool = nil
	} else {
		defer treasuryPool.Close()
		logger.Info("settla-server: connected to treasury DB")
	}

	// ── Auto-migrate ────────────────────────────────────────────────
	// Uses SETTLA_*_DB_MIGRATE_URL (raw Postgres) if set, otherwise
	// falls back to SETTLA_*_DB_URL. PgBouncer URLs work when pool_mode
	// is session, but raw Postgres is preferred for advisory locks.
	{
		transferMigrateURL := envOrDefault("SETTLA_TRANSFER_DB_MIGRATE_URL", transferDBURL)
		sub, _ := fs.Sub(settladb.TransferMigrations, "migrations/transfer")
		if err := automigrate.Run(automigrate.Transfer, transferMigrateURL, sub, logger); err != nil {
			logger.Error("settla-server: transfer DB migration failed", "error", err)
			os.Exit(1)
		}

		ledgerMigrateURL := envOrDefault("SETTLA_LEDGER_DB_MIGRATE_URL", ledgerDBURL)
		if ledgerPool != nil {
			sub, _ = fs.Sub(settladb.LedgerMigrations, "migrations/ledger")
			if err := automigrate.Run(automigrate.Ledger, ledgerMigrateURL, sub, logger); err != nil {
				logger.Error("settla-server: ledger DB migration failed", "error", err)
				os.Exit(1)
			}
		}

		treasuryMigrateURL := envOrDefault("SETTLA_TREASURY_DB_MIGRATE_URL", treasuryDBURL)
		if treasuryPool != nil {
			sub, _ = fs.Sub(settladb.TreasuryMigrations, "migrations/treasury")
			if err := automigrate.Run(automigrate.Treasury, treasuryMigrateURL, sub, logger); err != nil {
				logger.Error("settla-server: treasury DB migration failed", "error", err)
				os.Exit(1)
			}
		}
	}

	// NATS JetStream
	natsURL := envOrDefault("SETTLA_NATS_URL", "nats://localhost:4222")
	numPartitions := envIntOrDefault("SETTLA_NODE_PARTITIONS", messaging.DefaultPartitions)
	// SETTLA_NATS_REPLICAS controls JetStream stream replication factor.
	// 1 = dev/staging (single broker), 3 = production (3-node cluster, R=3).
	natsReplicas := envIntOrDefault("SETTLA_NATS_REPLICAS", 1)
	var publisher domain.EventPublisher
	natsClient, err := messaging.NewClient(natsURL, numPartitions, logger,
		messaging.WithReplicas(natsReplicas),
	)
	if err != nil {
		logger.Warn("settla-server: NATS unavailable, events will be dropped", "error", err)
		publisher = &stubPublisher{}
	} else {
		defer natsClient.Close()
		if err := natsClient.EnsureStream(ctx); err != nil {
			logger.Error("settla-server: failed to ensure NATS stream", "error", err)
			os.Exit(1)
		}
		rawPublisher := messaging.NewPublisher(natsClient)
		natsCB := resilience.NewCircuitBreaker("nats-publisher",
			resilience.WithFailureThreshold(5),
			resilience.WithResetTimeout(10*time.Second),
		)
		publisher = messaging.NewCircuitBreakerPublisher(rawPublisher, natsCB)
		logger.Info("settla-server: connected to NATS JetStream")
	}

	// Transfer App DB (RLS-enforced, settla_app role) — optional in dev, required in production
	var transferAppPool *pgxpool.Pool
	if appURL := os.Getenv("SETTLA_TRANSFER_APP_DB_URL"); appURL != "" {
		transferAppPool, err = newPgxPool(ctx, appURL)
		if err != nil {
			logger.Warn("settla-server: transfer app DB (RLS) unavailable, using owner pool", "error", err)
		} else {
			defer transferAppPool.Close()
			logger.Info("settla-server: connected to transfer app DB (RLS enforced)")
		}
	}

	// In production, RLS enforcement is mandatory — running without it means
	// all queries bypass row-level security, risking cross-tenant data leakage.
	if os.Getenv("SETTLA_ENV") == "production" && transferAppPool == nil {
		logger.Error("settla-server: FATAL — production requires RLS-enforced DB pool (SETTLA_TRANSFER_APP_DB_URL)")
		os.Exit(1)
	} else if transferAppPool == nil && os.Getenv("SETTLA_ENV") != "production" {
		logger.Warn("settla-server: RLS not enforced — SETTLA_TRANSFER_APP_DB_URL is unset, all queries bypass row-level security")
	}

	// ── Stores ──────────────────────────────────────────────────────

	transferQueries := transferdb.New(transferPool)
	storeOpts := []transferdb.TransferStoreOption{
		transferdb.WithTxPool(transferPool),
	}
	if transferAppPool != nil {
		storeOpts = append(storeOpts, transferdb.WithAppPool(transferAppPool))
	}
	transferStore := transferdb.NewTransferStoreAdapterWithOptions(transferQueries, storeOpts...)
	tenantStore := transferdb.NewTenantStoreAdapter(transferQueries)

	// Treasury store: real Postgres or in-memory stub
	var treasuryStore treasury.Store
	if treasuryPool != nil {
		treasuryStore = newPostgresTreasuryStore(treasurydb.New(treasuryPool), treasuryPool)
	} else {
		treasuryStore = &stubTreasuryStore{}
	}

	// ── Module initialization ───────────────────────────────────────
	// Each module depends on interfaces from domain/, not concrete types.
	// Any module can be extracted to a gRPC service by swapping the constructor.

	// Ledger: dual-backend (TigerBeetle writes + Postgres reads)
	batchWindowMs := envIntOrDefault("SETTLA_LEDGER_BATCH_WINDOW_MS", 10)
	batchMaxSize := envIntOrDefault("SETTLA_LEDGER_BATCH_MAX_SIZE", 500)
	var tbClient ledger.TBClient
	if tbAddrs := os.Getenv("SETTLA_TIGERBEETLE_ADDRESSES"); tbAddrs != "" {
		addresses := ledger.ParseTBAddresses(tbAddrs)
		var tbErr error
		tbClient, tbErr = ledger.NewRealTBClient(0, addresses)
		if tbErr != nil {
			logger.Error("settla-server: failed to connect to TigerBeetle", "addresses", tbAddrs, "error", tbErr)
			os.Exit(1)
		}
		defer tbClient.Close()
		logger.Info("settla-server: connected to TigerBeetle", "addresses", addresses)
	} else {
		if os.Getenv("SETTLA_ENV") == "production" {
			logger.Error("settla-server: FATAL — production requires TigerBeetle (SETTLA_TIGERBEETLE_ADDRESSES must be set)")
			os.Exit(1)
		}
		logger.Warn("settla-server: SETTLA_TIGERBEETLE_ADDRESSES not set, ledger running in stub mode")
	}

	var ledgerSvc *ledger.Service
	if ledgerPool != nil {
		pgBackend := ledger.NewPGBackend(ledgerdb.New(ledgerPool), logger)
		ledgerSvc = ledger.NewService(tbClient, pgBackend, publisher, logger, metrics,
			ledger.WithBatchWindow(time.Duration(batchWindowMs)*time.Millisecond),
			ledger.WithBatchMaxSize(batchMaxSize),
		)
	} else {
		ledgerSvc = ledger.NewService(tbClient, nil, publisher, logger, metrics,
			ledger.WithBatchWindow(time.Duration(batchWindowMs)*time.Millisecond),
			ledger.WithBatchMaxSize(batchMaxSize),
		)
	}
	ledgerSvc.Start()
	defer ledgerSvc.Stop()

	// Treasury: in-memory reservations + background DB flush
	flushIntervalMs := envIntOrDefault("SETTLA_TREASURY_FLUSH_INTERVAL_MS", 100)
	treasurySvc := treasury.NewManager(treasuryStore, publisher, logger, metrics,
		treasury.WithFlushInterval(time.Duration(flushIntervalMs)*time.Millisecond),
	)
	if err := treasurySvc.LoadPositions(ctx); err != nil {
		logger.Error("settla-server: failed to load treasury positions", "error", err)
		os.Exit(1)
	}
	treasurySvc.Start()
	defer treasurySvc.Stop()
	logger.Info("settla-server: treasury loaded", "positions", treasurySvc.PositionCount())

	// Partition Manager: runs a background scheduler to create future partitions
	// and drop expired ones. Runs daily; no-ops if DBs are unavailable.
	// Transfer DB manages outbox (daily partitions, 48h retention) and transfers/events (monthly).
	// The partition manager uses the raw pgxpool which satisfies DBExecutor via pgxPoolAdapter.
	partitionMgr := maintenance.NewPartitionManager(
		newPgxPoolDBExecutor(transferPool),
		logger,
	)
	partitionSchedulerInterval := envIntOrDefault("SETTLA_PARTITION_SCHEDULE_HOURS", 24)
	partitionCtx, partitionCancel := context.WithCancel(ctx)
	defer partitionCancel()
	go func() {
		// Run immediately on startup, then on a fixed schedule.
		if err := partitionMgr.ManagePartitions(partitionCtx); err != nil {
			logger.Warn("settla-server: partition management startup run failed", "error", err)
		}
		ticker := time.NewTicker(time.Duration(partitionSchedulerInterval) * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := partitionMgr.ManagePartitions(partitionCtx); err != nil {
					logger.Warn("settla-server: partition management run failed", "error", err)
				}
			case <-partitionCtx.Done():
				logger.Info("settla-server: partition manager stopped")
				return
			}
		}
	}()
	logger.Info("settla-server: partition manager scheduled",
		"interval_hours", partitionSchedulerInterval,
	)

	// Vacuum Manager: runs VACUUM ANALYZE on hot tables at configured intervals.
	// Uses the Transfer DB executor (outbox + transfers are the hottest tables).
	// Vacuum for treasury positions runs via the same executor but the SQL
	// is a no-op on a different DB — the VacuumManager is intentionally
	// DB-agnostic and targets the executor it is given. In production a
	// separate VacuumManager instance wired to the treasury pool would be
	// added; for now one instance covers the Transfer DB hot tables.
	vacuumMgr := maintenance.NewVacuumManager(
		newPgxPoolDBExecutor(transferPool),
		logger,
	)
	vacuumCheckInterval := envIntOrDefault("SETTLA_VACUUM_CHECK_INTERVAL_MINUTES", 5)
	vacuumCtx, vacuumCancel := context.WithCancel(ctx)
	defer vacuumCancel()
	go func() {
		ticker := time.NewTicker(time.Duration(vacuumCheckInterval) * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := vacuumMgr.RunDueVacuums(vacuumCtx); err != nil {
					logger.Warn("settla-server: vacuum run failed", "error", err)
				}
			case <-vacuumCtx.Done():
				logger.Info("settla-server: vacuum manager stopped")
				return
			}
		}
	}()
	logger.Info("settla-server: vacuum manager scheduled",
		"check_interval_minutes", vacuumCheckInterval,
	)

	// Capacity Monitor: checks DB sizes every 15 minutes and exports Prometheus
	// gauges. Alerts are logged; Prometheus alerting rules fire on the gauges.
	// maxSizeBytes = 10 TiB — appropriate for a 50M tx/day workload.
	const tenTiB = 10 * 1024 * 1024 * 1024 * 1024
	capacityMetrics := maintenance.NewCapacityMetrics()
	capacityMon := maintenance.NewCapacityMonitor(
		newPgxPoolDBExecutor(transferPool),
		logger,
		[]string{"settla_transfer", "settla_ledger", "settla_treasury"},
		tenTiB,
		capacityMetrics,
	)
	capacityCheckInterval := envIntOrDefault("SETTLA_CAPACITY_CHECK_INTERVAL_MINUTES", 15)
	capacityCtx, capacityCancel := context.WithCancel(ctx)
	defer capacityCancel()
	go func() {
		// Run immediately on startup.
		if _, err := capacityMon.CheckCapacity(capacityCtx); err != nil {
			logger.Warn("settla-server: capacity check startup run failed", "error", err)
		}
		ticker := time.NewTicker(time.Duration(capacityCheckInterval) * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if _, err := capacityMon.CheckCapacity(capacityCtx); err != nil {
					logger.Warn("settla-server: capacity check failed", "error", err)
				}
			case <-capacityCtx.Done():
				logger.Info("settla-server: capacity monitor stopped")
				return
			}
		}
	}()
	logger.Info("settla-server: capacity monitor scheduled",
		"check_interval_minutes", capacityCheckInterval,
	)

	// Reconciler: 6 consistency checks run on a configurable schedule.
	// Checks cover: transfer state, outbox health, provider tx staleness,
	// daily volume, settlement fee reconciliation (ENG-8), and — when the
	// ledger DB is available — treasury-ledger balance alignment.
	reconAdapter := transferdb.NewReconciliationAdapter(transferQueries)
	reconChecks := []reconciliation.Check{
		reconciliation.NewTransferStateCheck(reconAdapter, logger, nil),
		reconciliation.NewOutboxCheck(reconAdapter, logger, 0),
		reconciliation.NewProviderTxCheck(reconAdapter, logger, 0),
		reconciliation.NewDailyVolumeCheck(reconAdapter, logger),
		reconciliation.NewSettlementFeeCheck(reconAdapter, logger, decimal.Zero),
		reconciliation.NewDepositCheck(reconAdapter, logger, 0, 0, 0),
		reconciliation.NewBankDepositCheck(reconAdapter, logger, 0, 0),
	}
	if ledgerPool != nil {
		ledgerReconAdapter := ledgerdb.NewLedgerReconciliationAdapter(ledgerdb.New(ledgerPool))
		reconChecks = append(reconChecks, reconciliation.NewTreasuryLedgerCheck(
			treasurySvc,
			ledgerReconAdapter,
			reconAdapter,
			reconAdapter,
			logger,
			decimal.NewFromFloat(0.01),
		))
	}
	reconciler := reconciliation.NewReconciler(reconChecks, reconAdapter, logger)

	// Feature flags: load from config file with background hot-reload (30s).
	flagConfigPath := envOrDefault("SETTLA_FEATURE_FLAGS_PATH", "deploy/config/features.json")
	flagManager := featureflag.NewManager(flagConfigPath, logger)
	go flagManager.Start(ctx)
	reconciler.WithFeatureFlags(flagManager)
	logger.Info("settla-server: feature flags loaded", "config_path", flagConfigPath)

	reconIntervalMinutes := envIntOrDefault("SETTLA_RECONCILIATION_INTERVAL_MINUTES", 5)
	reconCtx, reconCancel := context.WithCancel(ctx)
	defer reconCancel()
	go func() {
		ticker := time.NewTicker(time.Duration(reconIntervalMinutes) * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if _, err := reconciler.Run(reconCtx); err != nil {
					logger.Warn("settla-server: reconciliation run failed", "error", err)
				}
			case <-reconCtx.Done():
				logger.Info("settla-server: reconciler stopped")
				return
			}
		}
	}()
	logger.Info("settla-server: reconciler scheduled",
		"interval_minutes", reconIntervalMinutes,
		"checks", len(reconChecks),
	)

	// Settlement scheduler: calculates daily net settlements for NET_SETTLEMENT tenants.
	// Runs once per day (00:30 UTC default). Gate behind SETTLA_SETTLEMENT_ENABLED.
	if envOrDefault("SETTLA_SETTLEMENT_ENABLED", "true") == "true" {
		settlementStore := transferdb.NewSettlementAdapter(transferQueries)
		calculator := settlement.NewCalculator(settlementStore, settlementStore, settlementStore, logger)
		scheduler := settlement.NewScheduler(calculator, settlementStore, logger)
		settlementCtx, settlementCancel := context.WithCancel(ctx)
		defer settlementCancel()
		go func() {
			if err := scheduler.Start(settlementCtx); err != nil && err != context.Canceled {
				logger.Error("settla-server: settlement scheduler stopped with error", "error", err)
			}
		}()
		logger.Info("settla-server: settlement scheduler started")
	} else {
		logger.Info("settla-server: settlement scheduler disabled (SETTLA_SETTLEMENT_ENABLED=false)")
	}

	// Synthetic canary: runs a lightweight test transfer through the full pipeline
	// to verify end-to-end health. Disabled by default.
	if envOrDefault("SETTLA_SYNTHETIC_CANARY_ENABLED", "false") == "true" {
		canaryInterval := time.Duration(envIntOrDefault("SETTLA_SYNTHETIC_INTERVAL_S", 30)) * time.Second
		canary := synthetic.NewCanary(synthetic.Config{
			Enabled:     true,
			GatewayURL:  envOrDefault("SETTLA_SYNTHETIC_GATEWAY_URL", "http://gateway:3000"),
			APIKey:      os.Getenv("SETTLA_SYNTHETIC_API_KEY"),
			Interval:    canaryInterval,
		}, logger)
		canary.Start()
		defer canary.Stop()
		logger.Info("settla-server: synthetic canary started",
			"interval", canaryInterval,
			"gateway_url", envOrDefault("SETTLA_SYNTHETIC_GATEWAY_URL", "http://gateway:3000"),
		)
	}

	// ── Config validation ──────────────────────────────────────────────
	if natsReplicas < 1 || natsReplicas > 5 {
		logger.Error("settla-server: SETTLA_NATS_REPLICAS must be between 1 and 5",
			"nats_replicas", natsReplicas,
		)
		os.Exit(1)
	}

	// Rail: provider registry — mode-switched via SETTLA_PROVIDER_MODE
	providerMode := provider.ProviderModeFromEnv()
	switch providerMode {
	case provider.ProviderModeMock, provider.ProviderModeTestnet, provider.ProviderModeLive:
		// valid
	default:
		logger.Error("settla-server: unknown SETTLA_PROVIDER_MODE",
			"mode", string(providerMode),
			"valid_values", "mock, testnet, live",
		)
		os.Exit(1)
	}

	var providerReg *provider.Registry
	switch providerMode {
	case provider.ProviderModeTestnet:
		providerReg = initTestnetProviders(logger)
	default:
		providerReg = provider.NewRegistry()
		registerMockProviders(providerReg)
	}
	logger.Info("settla-server: provider mode", "mode", string(providerMode))
	// Router: smart routing with tenant fee schedules
	railRouter := router.NewRouter(providerReg, tenantStore, logger)
	coreRouterAdapter := router.NewCoreRouterAdapter(railRouter, tenantStore, logger)

	// Core engine (pure state machine — outbox pattern, no side-effect deps)
	engine := core.NewEngine(
		transferStore,
		tenantStore,
		coreRouterAdapter,
		providerReg,
		logger,
		metrics,
	)

	// Deposit engine (pure state machine for crypto deposit sessions)
	depositStoreAdapter := transferdb.NewDepositStoreAdapter(transferQueries, transferPool)
	depositEngine := depositcore.NewEngine(depositStoreAdapter, tenantStore, logger)
	logger.Info("settla-server: deposit engine initialized")

	// Bank deposit engine (pure state machine for bank deposit sessions)
	bankDepositStoreAdapter := transferdb.NewBankDepositStoreAdapter(transferQueries, transferPool)
	bankDepositEngine := bankdepositcore.NewEngine(bankDepositStoreAdapter, tenantStore, logger)
	logger.Info("settla-server: bank deposit engine initialized")

	// Payment link service (CRUD + redemption via deposit engine)
	paymentLinkStore := transferdb.NewPaymentLinkStoreAdapter(transferQueries, transferPool)
	paymentLinkBaseURL := envOrDefault("SETTLA_PAYMENT_LINK_BASE_URL", "http://localhost:3003/p")
	paymentLinkSvc := paymentlinkcore.NewService(paymentLinkStore, depositEngine, tenantStore, logger, paymentLinkBaseURL)
	logger.Info("settla-server: payment link service initialized")

	// ── Graceful drain ──────────────────────────────────────────────
	drainTimeout := time.Duration(envIntOrDefault("SETTLA_DRAIN_TIMEOUT_MS", 45000)) * time.Millisecond
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
	if natsClient != nil {
		checks = append(checks, healthcheck.NewNATSCheck(func(_ context.Context) error {
			if !natsClient.Conn.IsConnected() {
				return fmt.Errorf("NATS connection not active")
			}
			return nil
		}))
	}
	// TigerBeetle health check: wired when the TB client is connected.
	if tbClient != nil {
		checks = append(checks, healthcheck.NewCallbackCheck("tigerbeetle", true,
			func(ctx context.Context) error {
				// LookupAccounts with a zero ID returns empty (not an error),
				// confirming the client can reach the cluster.
				_, err := tbClient.LookupAccounts([]ledger.ID128{{}})
				return err
			},
		))
	}
	checks = append(checks, healthcheck.NewGoroutineCheck(100000))
	checker := healthcheck.NewChecker(logger, checks, healthcheck.WithVersion(version))
	healthHandler := healthcheck.NewHandler(checker, 100000)

	// ── HTTP health/readiness server ────────────────────────────────
	httpPort := envOrDefault("SETTLA_SERVER_HTTP_PORT", "8080")

	opsStore := transferdb.NewOpsAdapter(transferQueries)
	auditAdapter := transferdb.NewAuditAdapter(transferPool)
	logger.Info("settla-server: audit logger initialized")

	mux := http.NewServeMux()
	healthHandler.Register(mux)
	mux.Handle("/metrics", promhttp.Handler())
	settlagrpc.RegisterOpsHandlers(mux, opsStore, logger, auditAdapter)

	httpServer := &http.Server{
		Addr:    net.JoinHostPort("0.0.0.0", httpPort),
		Handler: drain.HTTPMiddleware(drainer, mux),
	}

	go func() {
		logger.Info("http server listening", "port", httpPort)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("http server failed", "error", err)
			os.Exit(1)
		}
	}()

	// ── Config: JWT secret (must be validated before gRPC server creation) ──
	jwtSecret := os.Getenv("SETTLA_JWT_SECRET")
	if jwtSecret == "" {
		logger.Error("SETTLA_JWT_SECRET must be set — refusing to start without a JWT signing secret")
		os.Exit(1)
	}

	// ── gRPC server ────────────────────────────────────────────────
	grpcPort := envOrDefault("SETTLA_SERVER_GRPC_PORT", "9090")

	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			drain.GRPCUnaryInterceptor(drainer),
			observability.UnaryServerInterceptor(metrics),
		),
		grpc.StreamInterceptor(drain.GRPCStreamInterceptor(drainer)),
		grpc.MaxConcurrentStreams(1000),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    30 * time.Second,
			Timeout: 10 * time.Second,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	)

	authStore := &apiKeyValidatorAdapter{q: transferQueries}
	portalStore := transferdb.NewPortalStoreAdapter(transferQueries)
	webhookMgmtStore := transferdb.NewWebhookAdapter(transferQueries)
	analyticsStore := transferdb.NewAnalyticsAdapter(transferQueries)
	extAnalyticsStore := transferdb.NewExtendedAnalyticsAdapter(transferQueries, transferPool)
	exportStore := transferdb.NewExportAdapter(transferQueries, transferPool)
	snapshotStore := transferdb.NewSnapshotAdapter(transferQueries, transferPool)
	// Wrap ledger with circuit breaker for gRPC callers.
	ledgerCB := resilience.NewCircuitBreaker("ledger",
		resilience.WithFailureThreshold(5),
		resilience.WithResetTimeout(30*time.Second),
	)
	ledgerWithCB := resilience.NewCircuitBreakerLedger(ledgerSvc, ledgerCB)
	portalAuthStore := transferdb.NewPortalAuthStoreAdapter(transferQueries, transferPool)
	grpcSvc := settlagrpc.NewServer(engine, treasurySvc, ledgerWithCB, logger,
		settlagrpc.WithAuthStore(authStore),
		settlagrpc.WithTenantPortalStore(portalStore),
		settlagrpc.WithWebhookManagementStore(webhookMgmtStore),
		settlagrpc.WithAnalyticsStore(analyticsStore),
		settlagrpc.WithExtendedAnalyticsStore(extAnalyticsStore),
		settlagrpc.WithExportStore(exportStore),
		settlagrpc.WithPortalAuthStore(portalAuthStore),
		settlagrpc.WithJWTSecret(jwtSecret),
		settlagrpc.WithDepositEngine(depositEngine),
		settlagrpc.WithBankDepositEngine(bankDepositEngine),
		settlagrpc.WithPaymentLinkService(paymentLinkSvc),
		settlagrpc.WithPaymentLinkBaseURL(paymentLinkBaseURL),
		settlagrpc.WithAuditLogger(auditAdapter),
	)
	pb.RegisterSettlementServiceServer(grpcServer, grpcSvc)
	pb.RegisterTreasuryServiceServer(grpcServer, grpcSvc)
	pb.RegisterLedgerServiceServer(grpcServer, grpcSvc)
	pb.RegisterAuthServiceServer(grpcServer, grpcSvc)
	pb.RegisterTenantPortalServiceServer(grpcServer, grpcSvc)
	pb.RegisterPortalAuthServiceServer(grpcServer, grpcSvc)
	pb.RegisterDepositServiceServer(grpcServer, grpcSvc)
	pb.RegisterBankDepositServiceServer(grpcServer, grpcSvc)
	pb.RegisterPaymentLinkServiceServer(grpcServer, grpcSvc)
	pb.RegisterAnalyticsServiceServer(grpcServer, grpcSvc)

	// Analytics snapshot scheduler
	if envOrDefault("SETTLA_ANALYTICS_SNAPSHOT_ENABLED", "true") == "true" {
		snapshotScheduler := analytics.NewSnapshotScheduler(
			&compositeAnalyticsQuerier{analytics: analyticsStore, ext: extAnalyticsStore},
			snapshotStore,
			logger,
		)
		snapshotCtx, snapshotCancel := context.WithCancel(ctx)
		defer snapshotCancel()
		go func() {
			if err := snapshotScheduler.Start(snapshotCtx); err != nil && err != context.Canceled {
				logger.Error("settla-server: analytics snapshot scheduler stopped with error", "error", err)
			}
		}()
		logger.Info("settla-server: analytics snapshot scheduler started")
	}

	// Analytics export pipeline
	exportStoragePath := envOrDefault("SETTLA_EXPORT_STORAGE_PATH", "/tmp/settla-exports")
	analyticsExporter := analytics.NewExporter(
		&compositeExportSource{analytics: analyticsStore, ext: extAnalyticsStore},
		exportStore,
		exportStoragePath,
		logger,
	)
	exportCtx, exportCancel := context.WithCancel(ctx)
	defer exportCancel()
	go func() {
		if err := analyticsExporter.Start(exportCtx); err != nil && err != context.Canceled {
			logger.Error("settla-server: analytics exporter stopped with error", "error", err)
		}
	}()
	logger.Info("settla-server: analytics exporter started", "storage_path", exportStoragePath)

	// Health check service
	healthSvc := health.NewServer()
	healthpb.RegisterHealthServer(grpcServer, healthSvc)
	healthSvc.SetServingStatus("settla.v1.SettlementService", healthpb.HealthCheckResponse_SERVING)
	healthSvc.SetServingStatus("settla.v1.TreasuryService", healthpb.HealthCheckResponse_SERVING)
	healthSvc.SetServingStatus("settla.v1.LedgerService", healthpb.HealthCheckResponse_SERVING)
	healthSvc.SetServingStatus("settla.v1.TenantPortalService", healthpb.HealthCheckResponse_SERVING)
	healthSvc.SetServingStatus("settla.v1.DepositService", healthpb.HealthCheckResponse_SERVING)
	healthSvc.SetServingStatus("settla.v1.BankDepositService", healthpb.HealthCheckResponse_SERVING)
	healthSvc.SetServingStatus("settla.v1.PaymentLinkService", healthpb.HealthCheckResponse_SERVING)

	if os.Getenv("SETTLA_ENV") != "production" {
		reflection.Register(grpcServer)
	}

	grpcLis, err := net.Listen("tcp", net.JoinHostPort("0.0.0.0", grpcPort))
	if err != nil {
		logger.Error("failed to listen for gRPC", "port", grpcPort, "error", err)
		os.Exit(1)
	}

	go func() {
		logger.Info("grpc server listening", "port", grpcPort)
		if err := grpcServer.Serve(grpcLis); err != nil {
			logger.Error("grpc server failed", "error", err)
			os.Exit(1)
		}
	}()

	// Signal that startup is complete — enables readiness/startup probes.
	checker.MarkStartupComplete()

	logger.Info("settla-server ready",
		"http_port", httpPort,
		"grpc_port", grpcPort,
		"treasury_positions", treasurySvc.PositionCount(),
	)

	<-ctx.Done()

	logger.Info("settla-server shutting down...")

	// Graceful shutdown order:
	// 1. Drain: reject new requests, wait for in-flight to complete
	// 2. Set gRPC health to NOT_SERVING so LB stops sending traffic
	// 3. Stop gRPC + HTTP servers
	// 4. Treasury final flush (persists in-flight reservations)
	// 5. Stop ledger sync/batcher
	// 6. Close DB pools (handled by defers)
	if err := drainer.Drain(); err != nil {
		logger.Warn("settla-server: drain incomplete", "error", err)
	}
	healthSvc.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
	grpcServer.GracefulStop()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("settla-server: HTTP server shutdown error", "error", err)
	}

	logger.Info("settla-server shutdown complete")
}

// registerMockProviders populates the registry with mock providers for development.
func registerMockProviders(reg *provider.Registry) {
	delayMs := envIntOrDefault("SETTLA_MOCK_DELAY_MS", 500)
	delay := time.Duration(delayMs) * time.Millisecond

	// On-ramp: GBP→USDT
	reg.RegisterOnRamp(mock.NewOnRampProvider("mock-onramp-gbp",
		[]domain.CurrencyPair{{From: domain.CurrencyGBP, To: domain.CurrencyUSDT}},
		decimal.NewFromFloat(1.25), decimal.NewFromFloat(0.50), delay,
	))

	// On-ramp: NGN→USDT
	reg.RegisterOnRamp(mock.NewOnRampProvider("mock-onramp-ngn",
		[]domain.CurrencyPair{{From: domain.CurrencyNGN, To: domain.CurrencyUSDT}},
		decimal.NewFromFloat(0.00065), decimal.NewFromFloat(0.50), delay,
	))

	// Off-ramp: USDT→NGN (fee is 0.50 USDT per transaction)
	reg.RegisterOffRamp(mock.NewOffRampProvider("mock-offramp-ngn",
		[]domain.CurrencyPair{{From: domain.CurrencyUSDT, To: domain.CurrencyNGN}},
		decimal.NewFromFloat(1550), decimal.NewFromFloat(0.50), delay,
	))

	// Off-ramp: USDT→GBP
	reg.RegisterOffRamp(mock.NewOffRampProvider("mock-offramp-gbp",
		[]domain.CurrencyPair{{From: domain.CurrencyUSDT, To: domain.CurrencyGBP}},
		decimal.NewFromFloat(0.80), decimal.NewFromFloat(0.30), delay,
	))

	// Blockchain: Tron
	reg.RegisterBlockchainClient(mock.NewBlockchainClient("tron", decimal.NewFromFloat(0.10)))
}

// initTestnetProviders creates a registry with Settla testnet on/off-ramp
// providers backed by real blockchain clients (Tron Nile, Sepolia, etc.).
func initTestnetProviders(logger *slog.Logger) *provider.Registry {
	// Blockchain registry from env (RPC URLs).
	chainCfg := blockchain.LoadConfigFromEnv()
	chainReg, err := blockchain.NewRegistryFromConfig(chainCfg, logger)
	if err != nil {
		logger.Error("settla-server: failed to create blockchain registry, falling back to mock", "error", err)
		reg := provider.NewRegistry()
		registerMockProviders(reg)
		return reg
	}

	// Shared dependencies for Settla providers.
	fxOracle := settlaprovider.NewFXOracle()
	fiatSim := settlaprovider.NewFiatSimulator(settlaprovider.DefaultSimulatorConfig())

	// On-ramp: fiat → stablecoin (real blockchain delivery).
	onRamp := settlaprovider.NewOnRampProvider(fxOracle, fiatSim, chainReg, nil, /* wallet manager — nil for read-only mode */
		settlaprovider.DefaultOnRampConfig(),
	)

	// Off-ramp: stablecoin → fiat (simulated crypto receipt + simulated payout).
	offRamp := settlaprovider.NewOffRampProvider(fxOracle, fiatSim, chainReg, nil, /* wallet manager */ logger)

	// Build provider registry from testnet deps.
	var chains []domain.BlockchainClient
	for _, ch := range chainReg.Chains() {
		c, _ := chainReg.GetClient(ch)
		if c != nil {
			chains = append(chains, c)
		}
	}

	reg := provider.NewRegistryFromMode(provider.ProviderModeTestnet, &provider.SettlaProviderDeps{
		OnRamp:  onRamp,
		OffRamp: offRamp,
		Chains:  chains,
	}, logger)

	return reg
}

// newPgxPool creates a pgxpool. In development we connect directly to Postgres.
// In production with PgBouncer, configure statement_pool_mode or session mode.
func newPgxPool(ctx context.Context, connString string) (*pgxpool.Pool, error) {
	return pgxpool.New(ctx, connString)
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

// ── Postgres Treasury Store ─────────────────────────────────────────────────

type postgresTreasuryStore struct {
	q    *treasurydb.Queries
	pool *pgxpool.Pool
}

func newPostgresTreasuryStore(q *treasurydb.Queries, pool *pgxpool.Pool) *postgresTreasuryStore {
	return &postgresTreasuryStore{q: q, pool: pool}
}

func (s *postgresTreasuryStore) LoadAllPositions(ctx context.Context) ([]domain.Position, error) {
	rows, err := s.q.ListAllPositions(ctx)
	if err != nil {
		return nil, fmt.Errorf("settla-treasury: loading positions: %w", err)
	}
	positions := make([]domain.Position, len(rows))
	for i, row := range rows {
		positions[i] = domain.Position{
			ID:            row.ID,
			TenantID:      row.TenantID,
			Currency:      domain.Currency(row.Currency),
			Location:      row.Location,
			Balance:       decimalFromNumeric(row.Balance),
			Locked:        decimalFromNumeric(row.Locked),
			MinBalance:    decimalFromNumeric(row.MinBalance),
			TargetBalance: decimalFromNumeric(row.TargetBalance),
			UpdatedAt:     row.UpdatedAt,
		}
	}
	return positions, nil
}

func (s *postgresTreasuryStore) UpdatePosition(ctx context.Context, id uuid.UUID, balance, locked decimal.Decimal) error {
	return s.q.UpdatePositionBalances(ctx, treasurydb.UpdatePositionBalancesParams{
		ID:      id,
		Balance: numericFromDecimal(balance),
		Locked:  numericFromDecimal(locked),
	})
}

func (s *postgresTreasuryStore) RecordHistory(ctx context.Context, positionID, tenantID uuid.UUID, balance, locked decimal.Decimal, triggerType string) error {
	_, err := s.q.CreatePositionHistory(ctx, treasurydb.CreatePositionHistoryParams{
		PositionID:  positionID,
		TenantID:    tenantID,
		Balance:     numericFromDecimal(balance),
		Locked:      numericFromDecimal(locked),
		TriggerType: pgtype.Text{String: triggerType, Valid: true},
		TriggerRef:  pgtype.UUID{},
	})
	return err
}

func (s *postgresTreasuryStore) LogReserveOp(ctx context.Context, op treasury.ReserveOp) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO reserve_ops (id, tenant_id, currency, location, amount, reference, op_type, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT DO NOTHING`,
		op.ID, op.TenantID, string(op.Currency), op.Location, op.Amount.String(), op.Reference, string(op.OpType), op.CreatedAt,
	)
	return err
}

func (s *postgresTreasuryStore) LogReserveOps(ctx context.Context, ops []treasury.ReserveOp) error {
	for _, op := range ops {
		if err := s.LogReserveOp(ctx, op); err != nil {
			return err
		}
	}
	return nil
}

func (s *postgresTreasuryStore) GetUncommittedOps(ctx context.Context) ([]treasury.ReserveOp, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT r.id, r.tenant_id, r.currency, r.location, r.amount, r.reference, r.op_type, r.created_at
		 FROM reserve_ops r
		 WHERE r.op_type = 'reserve'
		   AND NOT EXISTS (
		       SELECT 1 FROM reserve_ops c
		       WHERE c.reference = r.reference
		         AND c.op_type IN ('commit', 'release')
		   )
		 ORDER BY r.created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("settla-treasury: loading uncommitted ops: %w", err)
	}
	defer rows.Close()

	var ops []treasury.ReserveOp
	for rows.Next() {
		var op treasury.ReserveOp
		var currency, opType, amount string
		if err := rows.Scan(&op.ID, &op.TenantID, &currency, &op.Location, &amount, &op.Reference, &opType, &op.CreatedAt); err != nil {
			return nil, err
		}
		op.Currency = domain.Currency(currency)
		op.OpType = treasury.ReserveOpType(opType)
		op.Amount, _ = decimal.NewFromString(amount)
		ops = append(ops, op)
	}
	return ops, rows.Err()
}

func (s *postgresTreasuryStore) MarkOpCompleted(ctx context.Context, opID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE reserve_ops SET completed = true WHERE id = $1`, opID)
	return err
}

func (s *postgresTreasuryStore) CleanupOldOps(ctx context.Context, before time.Time) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM reserve_ops
		WHERE created_at < $1
		  AND (
		      op_type IN ('commit', 'release')
		      OR EXISTS (
		          SELECT 1 FROM reserve_ops c
		          WHERE c.reference = reserve_ops.reference
		            AND c.op_type IN ('commit', 'release')
		      )
		  )`, before)
	return err
}

// ── Conversion helpers ──────────────────────────────────────────────────────

func numericFromDecimal(d decimal.Decimal) pgtype.Numeric {
	n := pgtype.Numeric{}
	_ = n.Scan(d.String())
	return n
}

func decimalFromNumeric(n pgtype.Numeric) decimal.Decimal {
	if !n.Valid || n.Int == nil {
		return decimal.Zero
	}
	return decimal.NewFromBigInt(n.Int, n.Exp)
}

// ── API Key Validator Adapter ────────────────────────────────────────────────

type apiKeyValidatorAdapter struct {
	q *transferdb.Queries
}

func (a *apiKeyValidatorAdapter) ValidateAPIKey(ctx context.Context, keyHash string) (settlagrpc.APIKeyResult, error) {
	row, err := a.q.ValidateAPIKey(ctx, keyHash)
	if err != nil {
		return settlagrpc.APIKeyResult{}, err
	}

	return settlagrpc.APIKeyResult{
		TenantID:         row.TenantUuid.String(),
		Slug:             row.Slug,
		Status:           row.TenantStatus,
		FeeScheduleJSON:  string(row.FeeSchedule),
		DailyLimitUSD:    decimalFromNumeric(row.DailyLimitUsd).String(),
		PerTransferLimit: decimalFromNumeric(row.PerTransferLimit).String(),
	}, nil
}

// ── Composite Analytics Adapters ─────────────────────────────────────────────
// Bridge the AnalyticsAdapter and ExtendedAnalyticsAdapter into the unified
// interfaces required by the snapshot scheduler and exporter.

type compositeAnalyticsQuerier struct {
	analytics *transferdb.AnalyticsAdapter
	ext       *transferdb.ExtendedAnalyticsAdapter
}

func (c *compositeAnalyticsQuerier) GetCorridorMetrics(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.CorridorMetrics, error) {
	return c.analytics.GetCorridorMetrics(ctx, tenantID, from, to)
}

func (c *compositeAnalyticsQuerier) GetFeeBreakdown(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.FeeBreakdownEntry, error) {
	return c.ext.GetFeeBreakdown(ctx, tenantID, from, to)
}

func (c *compositeAnalyticsQuerier) GetTransferLatencyPercentiles(ctx context.Context, tenantID uuid.UUID, from, to time.Time) (*domain.LatencyPercentiles, error) {
	return c.analytics.GetTransferLatencyPercentiles(ctx, tenantID, from, to)
}

func (c *compositeAnalyticsQuerier) GetCryptoDepositAnalytics(ctx context.Context, tenantID uuid.UUID, from, to time.Time) (*domain.DepositAnalytics, error) {
	return c.ext.GetCryptoDepositAnalytics(ctx, tenantID, from, to)
}

func (c *compositeAnalyticsQuerier) GetBankDepositAnalytics(ctx context.Context, tenantID uuid.UUID, from, to time.Time) (*domain.DepositAnalytics, error) {
	return c.ext.GetBankDepositAnalytics(ctx, tenantID, from, to)
}

type compositeExportSource struct {
	analytics *transferdb.AnalyticsAdapter
	ext       *transferdb.ExtendedAnalyticsAdapter
}

func (c *compositeExportSource) GetFeeBreakdown(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.FeeBreakdownEntry, error) {
	return c.ext.GetFeeBreakdown(ctx, tenantID, from, to)
}

func (c *compositeExportSource) GetProviderPerformance(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.ProviderPerformance, error) {
	return c.ext.GetProviderPerformance(ctx, tenantID, from, to)
}

func (c *compositeExportSource) GetCorridorMetrics(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.CorridorMetrics, error) {
	return c.analytics.GetCorridorMetrics(ctx, tenantID, from, to)
}
