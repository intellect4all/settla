package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	settlagrpc "github.com/intellect4all/settla/api/grpc"
	"github.com/intellect4all/settla/bank"
	bankmock "github.com/intellect4all/settla/bank/mock"
	"github.com/intellect4all/settla/cache"
	"github.com/intellect4all/settla/core"
	bankdepositcore "github.com/intellect4all/settla/core/bankdeposit"
	"github.com/intellect4all/settla/core/compensation"
	depositcore "github.com/intellect4all/settla/core/deposit"
	"github.com/intellect4all/settla/core/recovery"
	settladb "github.com/intellect4all/settla/db"
	"github.com/intellect4all/settla/db/automigrate"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/ledger"
	"github.com/intellect4all/settla/node/chainmonitor"
	"github.com/intellect4all/settla/node/chainmonitor/rpc"
	"github.com/intellect4all/settla/node/chainmonitor/wallet"
	"github.com/intellect4all/settla/node/messaging"
	"github.com/intellect4all/settla/node/outbox"
	"github.com/intellect4all/settla/node/worker"
	"github.com/intellect4all/settla/observability"
	"github.com/intellect4all/settla/observability/healthcheck"
	"github.com/intellect4all/settla/rail/blockchain"
	"github.com/intellect4all/settla/rail/provider"
	railwallet "github.com/intellect4all/settla/rail/wallet"
	_ "github.com/intellect4all/settla/rail/provider/all" // triggers provider init() self-registration
	"github.com/intellect4all/settla/rail/provider/factory"
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

	// ── Distributed tracing (OpenTelemetry) ─────────────────────────
	tracerShutdown, err := observability.InitTracer(ctx, "settla-node", nodeVersion, logger)
	if err != nil {
		logger.Warn("settla-node: tracer init failed, continuing without tracing", "error", err)
	} else {
		defer tracerShutdown(ctx)
	}

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
	var natsAuthOpts []messaging.ClientOption
	natsAuthOpts = append(natsAuthOpts, messaging.WithReplicas(natsReplicas))
	if natsToken := os.Getenv("SETTLA_NATS_TOKEN"); natsToken != "" {
		natsAuthOpts = append(natsAuthOpts, messaging.WithNATSToken(natsToken))
	} else if natsUser := os.Getenv("SETTLA_NATS_USER"); natsUser != "" {
		natsAuthOpts = append(natsAuthOpts, messaging.WithNATSUserInfo(natsUser, os.Getenv("SETTLA_NATS_PASSWORD")))
	} else if os.Getenv("SETTLA_ENV") == "production" || os.Getenv("SETTLA_ENV") == "staging" {
		logger.Error("settla-node: FATAL — NATS authentication required in " + os.Getenv("SETTLA_ENV") + ". Set SETTLA_NATS_TOKEN or SETTLA_NATS_USER/SETTLA_NATS_PASSWORD")
		os.Exit(1)
	}
	natsClient, err := messaging.NewClient(natsURL, numPartitions, logger,
		natsAuthOpts...,
	)
	if err != nil {
		logger.Error("settla-node: failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	// NOTE: natsClient.Close() is NOT deferred here because we drain the
	// NATS connection explicitly during the shutdown sequence (step 2) before
	// stopping workers, to flush pending ACKs. Calling Drain() twice is a no-op
	// but the explicit sequencing is intentional.

	if err := natsClient.EnsureStreams(ctx); err != nil {
		logger.Error("settla-node: failed to ensure streams", "error", err)
		os.Exit(1)
	}

	// ── Database connections ──────────────────────────────────────
	transferDBURL := envOrDefault("SETTLA_TRANSFER_DB_URL",
		"postgres://settla:settla@localhost:5434/settla_transfer?sslmode=prefer")
	rejectInsecureSSLInProduction("SETTLA_TRANSFER_DB_URL", transferDBURL, logger)
	transferPool, err := newPgxPool(ctx, transferDBURL)
	if err != nil {
		logger.Error("settla-node: failed to connect to transfer DB", "url", transferDBURL, "error", err)
		os.Exit(1)
	}
	defer transferPool.Close()
	observability.RegisterPoolMetrics(ctx, transferPool, "transfer", metrics)
	logger.Info("settla-node: connected to transfer DB")

	ledgerDBURL := envOrDefault("SETTLA_LEDGER_DB_URL",
		"postgres://settla:settla@localhost:5433/settla_ledger?sslmode=prefer")
	rejectInsecureSSLInProduction("SETTLA_LEDGER_DB_URL", ledgerDBURL, logger)
	ledgerPool, err := newPgxPool(ctx, ledgerDBURL)
	if err != nil {
		logger.Warn("settla-node: ledger DB unavailable, PG read path disabled", "error", err)
		ledgerPool = nil
	} else {
		defer ledgerPool.Close()
		observability.RegisterPoolMetrics(ctx, ledgerPool, "ledger", metrics)
		logger.Info("settla-node: connected to ledger DB")
	}

	treasuryDBURL := envOrDefault("SETTLA_TREASURY_DB_URL",
		"postgres://settla:settla@localhost:5435/settla_treasury?sslmode=prefer")
	rejectInsecureSSLInProduction("SETTLA_TREASURY_DB_URL", treasuryDBURL, logger)
	treasuryPool, err := newPgxPool(ctx, treasuryDBURL)
	if err != nil {
		logger.Warn("settla-node: treasury DB unavailable, using stub store", "error", err)
		treasuryPool = nil
	} else {
		defer treasuryPool.Close()
		observability.RegisterPoolMetrics(ctx, treasuryPool, "treasury", metrics)
		logger.Info("settla-node: connected to treasury DB")
	}

	// ── Auto-migrate ────────────────────────────────────────────────
	{
		transferMigrateURL := envOrDefault("SETTLA_TRANSFER_DB_MIGRATE_URL", transferDBURL)
		sub, _ := fs.Sub(settladb.TransferMigrations, "migrations/transfer")
		if err := automigrate.Run(automigrate.Transfer, transferMigrateURL, sub, logger); err != nil {
			logger.Error("settla-node: transfer DB migration failed", "error", err)
			os.Exit(1)
		}

		ledgerMigrateURL := envOrDefault("SETTLA_LEDGER_DB_MIGRATE_URL", ledgerDBURL)
		if ledgerPool != nil {
			sub, _ = fs.Sub(settladb.LedgerMigrations, "migrations/ledger")
			if err := automigrate.Run(automigrate.Ledger, ledgerMigrateURL, sub, logger); err != nil {
				logger.Error("settla-node: ledger DB migration failed", "error", err)
				os.Exit(1)
			}
		}

		treasuryMigrateURL := envOrDefault("SETTLA_TREASURY_DB_MIGRATE_URL", treasuryDBURL)
		if treasuryPool != nil {
			sub, _ = fs.Sub(settladb.TreasuryMigrations, "migrations/treasury")
			if err := automigrate.Run(automigrate.Treasury, treasuryMigrateURL, sub, logger); err != nil {
				logger.Error("settla-node: treasury DB migration failed", "error", err)
				os.Exit(1)
			}
		}
	}

	// ── Stores ────────────────────────────────────────────────────
	transferQueries := transferdb.New(transferPool)
	transferStore := transferdb.NewTransferStoreAdapter(transferQueries, transferPool)
	tenantStore := transferdb.NewTenantStoreAdapter(transferQueries)

	// ── Redis — tenant index (SSCAN-based iteration) ──────────────
	var tenantIndex *cache.TenantIndex
	if redisURL := envOrDefault("SETTLA_REDIS_URL", ""); redisURL != "" {
		if redisOpts, redisErr := cache.ParseRedisURL(redisURL); redisErr != nil {
			logger.Warn("settla-node: invalid SETTLA_REDIS_URL, tenant index disabled", "error", redisErr)
		} else if redisOpts != nil {
			redisClient := cache.NewRedisClientFromOpts(redisOpts)
			if pingErr := redisClient.Ping(ctx).Err(); pingErr != nil {
				logger.Warn("settla-node: Redis unavailable, tenant index will use Postgres fallback", "error", pingErr)
				redisClient.Close()
			} else {
				defer redisClient.Close()
				paginatedFetcher := func(ctx context.Context, limit, offset int32) ([]uuid.UUID, error) {
					return transferQueries.ListActiveTenantIDsPaginated(ctx, transferdb.ListActiveTenantIDsPaginatedParams{
						Limit: limit, Offset: offset,
					})
				}
				tenantIndex = cache.NewTenantIndex(redisClient, paginatedFetcher, logger)
				if rebuildErr := tenantIndex.Rebuild(ctx); rebuildErr != nil {
					logger.Warn("settla-node: tenant index initial rebuild failed", "error", rebuildErr)
				} else {
					count, _ := tenantIndex.Count(ctx)
					logger.Info("settla-node: tenant index initialized", "tenants", count)
				}
				// Periodic reconciliation sweep
				go func() {
					ticker := time.NewTicker(15 * time.Minute)
					defer ticker.Stop()
					for {
						select {
						case <-ctx.Done():
							return
						case <-ticker.C:
							if err := tenantIndex.Rebuild(ctx); err != nil {
								logger.Warn("settla-node: tenant index reconciliation failed", "error", err)
							}
						}
					}
				}()
			}
		}
	}

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

	// Rail: provider registry — factory-bootstrapped via SETTLA_PROVIDER_MODE.
	providerMode := provider.ModeFromEnv()
	var chainReg *blockchain.Registry
	var walletMgr *railwallet.Manager
	if providerMode == provider.ProviderModeTestnet || providerMode == provider.ProviderModeLive {
		// Create wallet manager for blockchain transaction signing.
		encKey := os.Getenv("SETTLA_WALLET_ENCRYPTION_KEY")
		masterSeedHex := os.Getenv("SETTLA_MASTER_SEED")
		storagePath := envOrDefault("SETTLA_WALLET_STORAGE_PATH", ".settla/wallets")

		if encKey != "" && masterSeedHex != "" {
			masterSeed, err := hex.DecodeString(masterSeedHex)
			if err != nil {
				logger.Error("settla-node: invalid SETTLA_MASTER_SEED hex", "error", err)
				os.Exit(1)
			}
			walletMgr, err = railwallet.NewManager(railwallet.ManagerConfig{
				MasterSeed:    masterSeed,
				EncryptionKey: encKey,
				StoragePath:   storagePath,
				Logger:        logger,
			})
			if err != nil {
				logger.Error("settla-node: failed to create wallet manager", "error", err)
				os.Exit(1)
			}
			defer walletMgr.Close()
			logger.Info("settla-node: wallet manager initialized", "storage_path", storagePath)
		} else {
			logger.Warn("settla-node: SETTLA_WALLET_ENCRYPTION_KEY or SETTLA_MASTER_SEED not set — blockchain clients will be read-only")
		}

		chainCfg := blockchain.LoadConfigFromEnv()
		var err error
		chainReg, err = blockchain.NewRegistryFromConfig(chainCfg, walletMgr, logger)
		if err != nil {
			logger.Error("settla-node: failed to create blockchain registry", "error", err)
			os.Exit(1)
		}

		if walletMgr != nil {
			if err := chainReg.RegisterSystemWallets(walletMgr); err != nil {
				logger.Warn("settla-node: some system wallets failed to register", "error", err)
			}
		}
	}
	bootstrapResult, err := factory.Bootstrap(factory.ProviderMode(providerMode), factory.Deps{
		Logger:        logger,
		BlockchainReg: chainReg,
	})
	if err != nil {
		logger.Error("settla-node: provider bootstrap failed", "error", err)
		os.Exit(1)
	}
	providerReg := provider.NewRegistry()
	for _, p := range bootstrapResult.OnRamps {
		providerReg.RegisterOnRamp(p)
	}
	for _, p := range bootstrapResult.OffRamps {
		providerReg.RegisterOffRamp(p)
	}
	for _, c := range bootstrapResult.Blockchains {
		providerReg.RegisterBlockchainClient(c)
	}
	for slug, n := range bootstrapResult.Normalizers {
		providerReg.RegisterNormalizer(slug, n)
	}
	for slug, l := range bootstrapResult.Listeners {
		providerReg.RegisterListener(slug, l)
	}
	if chainReg != nil {
		for _, ch := range chainReg.Chains() {
			c, _ := chainReg.GetClient(ch)
			if c != nil {
				providerReg.RegisterBlockchainClient(c)
			}
		}
	}
	logger.Info("settla-node: provider mode", "mode", string(providerMode))

	// Router (quote-only — used by engine for quotes, not for execution)
	railRouter := router.NewRouter(providerReg, tenantStore, logger)
	coreRouterAdapter := router.NewCoreRouterAdapter(railRouter, tenantStore, logger)

	// Core engine (pure state machine — writes outbox entries atomically)
	engine := core.NewEngine(
		transferStore,
		tenantStore,
		coreRouterAdapter,
		providerReg,
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
	reviewStore := transferdb.NewReviewStoreAdapter(transferQueries)
	recoveryQueryStore := transferdb.NewRecoveryQueryAdapter(transferQueries, envIntOrDefault("SETTLA_RECOVERY_MAX_RESULTS", 5000))
	recoveryDetector := recovery.NewDetector(
		recoveryQueryStore,
		reviewStore,
		engine,
		&stubProviderStatusChecker{},
		logger,
	)

	// ── Deposit engine ──────────────────────────────────────────
	depositStoreAdapter := transferdb.NewDepositStoreAdapter(transferQueries, transferPool)
	depositEngine := depositcore.NewEngine(depositStoreAdapter, tenantStore, logger)
	logger.Info("settla-node: deposit engine initialized")

	// Bank deposit engine
	bankDepositStoreAdapter := transferdb.NewBankDepositStoreAdapter(transferQueries, transferPool)
	bankDepositEngine := bankdepositcore.NewEngine(bankDepositStoreAdapter, tenantStore, logger)
	logger.Info("settla-node: bank deposit engine initialized")

	// Banking partner registry (for refund flow + provisioner)
	bankPartnerRegistry := bank.NewRegistry()
	// Register mock banking partner for dev
	bankPartnerRegistry.Register(bankmock.NewMockSettlaBank())
	logger.Info("settla-node: banking partner registry initialized")

	// ── Address pool manager (HD wallet deriver) ────────────────
	var addressDeriver transferdb.HDWalletDeriver
	if signingURL := os.Getenv("SETTLA_SIGNING_SERVICE_URL"); signingURL != "" {
		addressDeriver = wallet.NewTronDeriver(signingURL, &http.Client{Timeout: 10 * time.Second}, logger)
		logger.Info("settla-node: address deriver configured", "mode", "signing-service", "url", signingURL)
	} else {
		addressDeriver = wallet.NewStaticPoolDeriver(wallet.DefaultTestAddresses(), logger)
		logger.Info("settla-node: address deriver configured", "mode", "static-pool")
	}

	poolCfg := transferdb.DefaultPoolConfig()
	addressPoolMgr := transferdb.NewAddressPoolManager(transferQueries, addressDeriver, poolCfg, logger)
	_ = addressPoolMgr // used by background refill goroutine below

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
	poolDeposit := envIntOrDefault("SETTLA_WORKER_POOL_DEPOSIT", 8)
	poolEmail := envIntOrDefault("SETTLA_WORKER_POOL_EMAIL", 8)
	poolBankDeposit := envIntOrDefault("SETTLA_WORKER_POOL_BANK_DEPOSIT", 8)

	logger.Info("settla-node: worker pool sizes",
		"transfer", poolTransfer,
		"provider", poolProvider,
		"blockchain", poolBlockchain,
		"ledger", poolLedger,
		"treasury", poolTreasury,
		"webhook", poolWebhook,
		"inbound_wh", poolInboundWH,
		"deposit", poolDeposit,
		"email", poolEmail,
		"bank_deposit", poolBankDeposit,
	)

	// ── Email sender ─────────────────────────────────────────────
	var emailSender worker.EmailSender
	if resendKey := os.Getenv("SETTLA_RESEND_API_KEY"); resendKey != "" {
		emailFrom := envOrDefault("SETTLA_EMAIL_FROM", "notifications@settla.io")
		emailSender = worker.NewResendEmailSender(resendKey, emailFrom, logger)
		logger.Info("settla-node: email sender configured", "provider", "resend", "from", emailFrom)
	} else {
		emailSender = worker.NewLogEmailSender(logger)
		logger.Info("settla-node: email sender configured", "provider", "log-only")
	}

	// ── Provider maps (for dedicated workers) ────────────────────
	onRampProviders := buildOnRampMap(providerReg, logger)
	offRampProviders := buildOffRampMap(providerReg, logger)
	blockchainClients := buildBlockchainMap(providerReg, logger)

	// Provider transfer store (DB-backed for atomic CHECK-BEFORE-CALL)
	providerTxStore := transferdb.NewProviderTxAdapter(transferPool)
	webhookLogStore := transferdb.NewProviderWebhookLogAdapter(transferPool)

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
	checks = append(checks, healthcheck.NewNATSCheck(func(ctx context.Context) error {
		if !natsClient.Conn.IsConnected() {
			return fmt.Errorf("NATS connection not active")
		}
		// Verify JetStream streams are accessible
		for _, sd := range messaging.AllStreams() {
			_, err := natsClient.JS.Stream(ctx, sd.Name)
			if err != nil {
				return fmt.Errorf("NATS JetStream stream %s not accessible: %w", sd.Name, err)
			}
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
				nil, // providerCBConfigs — use defaults; will be wired from factory configs later
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
				func(slug string) domain.WebhookNormalizer { return providerReg.GetNormalizer(slug) },
				webhookLogStore,
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
				p, tenantStore, natsClient, logger, nil, nil,
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

			// Deposit worker: processes crypto deposit events
			dw := worker.NewDepositWorker(
				p, depositEngine, treasurySvc, natsClient, logger,
				messaging.WithPoolSize(poolDeposit),
			)
			partitionedWorkers = append(partitionedWorkers, dw)
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := dw.Start(ctx); err != nil {
					logger.Error("settla-node: deposit worker failed", "partition", p, "error", err)
				}
			}()

			// Email worker: sends email notifications for deposit/transfer events
			ew := worker.NewEmailWorker(
				p, tenantStore, emailSender, natsClient, logger,
				messaging.WithPoolSize(poolEmail),
			)
			partitionedWorkers = append(partitionedWorkers, ew)
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := ew.Start(ctx); err != nil {
					logger.Error("settla-node: email worker failed", "partition", p, "error", err)
				}
			}()

			// Bank deposit worker: processes bank deposit events
			bdw := worker.NewBankDepositWorker(
				p, bankDepositEngine, treasurySvc, bankDepositStoreAdapter, bankDepositStoreAdapter, bankPartnerRegistry, natsClient, logger, metrics,
				messaging.WithPoolSize(poolBankDeposit),
			)
			partitionedWorkers = append(partitionedWorkers, bdw)
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := bdw.Start(ctx); err != nil {
					logger.Error("settla-node: bank deposit worker failed", "partition", p, "error", err)
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

	// ── Start deposit expiry job ────────────────────────────────
	depositExpiryJob := worker.NewDepositExpiryJob(depositStoreAdapter, depositEngine, logger)
	wg.Add(1)
	go func() {
		defer wg.Done()
		depositExpiryJob.Run(ctx)
	}()
	logger.Info("settla-node: deposit expiry job started")

	// Bank deposit expiry job
	bankDepositExpiryJob := worker.NewBankDepositExpiryJob(bankDepositStoreAdapter, bankDepositEngine, logger)
	wg.Add(1)
	go func() {
		defer wg.Done()
		bankDepositExpiryJob.Run(ctx)
	}()
	logger.Info("settla-node: bank deposit expiry job started")

	// Virtual account provisioner — uses TenantIndex (Redis) or paginated Postgres fallback
	vaForEach := func(ctx context.Context, batchSize int32, fn func(ids []uuid.UUID) error) error {
		if tenantIndex != nil {
			return tenantIndex.ForEach(ctx, batchSize, fn)
		}
		fetcher := func(ctx context.Context, limit, offset int32) ([]uuid.UUID, error) {
			return transferQueries.ListActiveTenantIDsPaginated(ctx, transferdb.ListActiveTenantIDsPaginatedParams{
				Limit: limit, Offset: offset,
			})
		}
		return domain.ForEachTenantBatch(ctx, fetcher, batchSize, fn)
	}
	vaProvisioner := worker.NewVirtualAccountProvisioner(bankDepositStoreAdapter, bankPartnerRegistry, vaForEach, logger)
	wg.Add(1)
	go func() {
		defer wg.Done()
		vaProvisioner.Run(ctx)
	}()
	logger.Info("settla-node: virtual account provisioner started")

	// ── Chain monitor: shared dependencies ──────────────────────
	checkpointStore := transferdb.NewCheckpointStoreAdapter(transferQueries)
	checkpointMgr := chainmonitor.NewCheckpointManager(checkpointStore)
	addressStore := transferdb.NewAddressStoreAdapter(transferQueries)
	tokenStore := transferdb.NewTokenStoreAdapter(transferQueries)
	outboxWriter := transferdb.NewOutboxWriterAdapter(transferQueries, transferPool)
	addresses := chainmonitor.NewAddressSet()
	tokens := chainmonitor.NewTokenRegistry()
	var chainPollers []chainmonitor.ChainPoller

	// ── Chain monitor: EVM pollers (Ethereum, Base) ──────────────
	// If RPC URLs are configured, create EVM pollers for Ethereum and Base.
	// These use eth_getLogs for batch ERC-20 transfer scanning.
	if ethRPCURL := os.Getenv("SETTLA_ETH_RPC_URL"); ethRPCURL != "" {
		ethCfg := chainmonitor.DefaultEthereumConfig()
		ethCfg.RPCURL = ethRPCURL
		ethCfg.APIKey = os.Getenv("SETTLA_ETH_RPC_API_KEY")
		if backupURL := os.Getenv("SETTLA_ETH_RPC_BACKUP_URL"); backupURL != "" {
			ethCfg.BackupRPCURL = backupURL
			ethCfg.BackupAPIKey = os.Getenv("SETTLA_ETH_RPC_BACKUP_API_KEY")
		}

		providers := []*rpc.Provider{
			{Name: "eth-primary", RPCURL: ethCfg.RPCURL, APIKey: ethCfg.APIKey},
		}
		if ethCfg.BackupRPCURL != "" {
			providers = append(providers, &rpc.Provider{Name: "eth-backup", RPCURL: ethCfg.BackupRPCURL, APIKey: ethCfg.BackupAPIKey})
		}

		ethClient := rpc.NewEVMClient(providers, logger)
		ethPoller := chainmonitor.NewEVMPoller(ethCfg, ethClient, addresses, tokens, checkpointMgr, outboxWriter, logger)
		chainPollers = append(chainPollers, ethPoller)
		logger.Info("settla-node: Ethereum EVM poller configured", "rpc_url", ethRPCURL)
	}

	if baseRPCURL := os.Getenv("SETTLA_BASE_RPC_URL"); baseRPCURL != "" {
		baseCfg := chainmonitor.DefaultBaseConfig()
		baseCfg.RPCURL = baseRPCURL
		baseCfg.APIKey = os.Getenv("SETTLA_BASE_RPC_API_KEY")
		if backupURL := os.Getenv("SETTLA_BASE_RPC_BACKUP_URL"); backupURL != "" {
			baseCfg.BackupRPCURL = backupURL
			baseCfg.BackupAPIKey = os.Getenv("SETTLA_BASE_RPC_BACKUP_API_KEY")
		}

		providers := []*rpc.Provider{
			{Name: "base-primary", RPCURL: baseCfg.RPCURL, APIKey: baseCfg.APIKey},
		}
		if baseCfg.BackupRPCURL != "" {
			providers = append(providers, &rpc.Provider{Name: "base-backup", RPCURL: baseCfg.BackupRPCURL, APIKey: baseCfg.BackupAPIKey})
		}

		baseClient := rpc.NewEVMClient(providers, logger)
		basePoller := chainmonitor.NewEVMPoller(baseCfg, baseClient, addresses, tokens, checkpointMgr, outboxWriter, logger)
		chainPollers = append(chainPollers, basePoller)
		logger.Info("settla-node: Base EVM poller configured", "rpc_url", baseRPCURL)
	}

	// ── Chain monitor: Tron poller ──────────────────────────────
	if tronRPCURL := os.Getenv("SETTLA_TRON_RPC_URL"); tronRPCURL != "" {
		tronCfg := chainmonitor.DefaultTronConfig()
		tronCfg.RPCURL = tronRPCURL
		tronCfg.APIKey = os.Getenv("SETTLA_TRON_API_KEY")

		providers := []*rpc.Provider{
			{Name: "tron-primary", RPCURL: tronCfg.RPCURL, APIKey: tronCfg.APIKey},
		}

		tronClient := rpc.NewTronClient(providers, logger)
		tronPoller := chainmonitor.NewTronPoller(tronCfg, tronClient, addresses, tokens, checkpointMgr, outboxWriter, logger)
		chainPollers = append(chainPollers, tronPoller)
		logger.Info("settla-node: Tron poller configured", "rpc_url", tronRPCURL)
	}

	// ── Chain monitor: start ────────────────────────────────────
	if len(chainPollers) > 0 {
		monitorCfg := chainmonitor.DefaultMonitorConfig()
		monitor := chainmonitor.NewMonitor(monitorCfg, chainPollers, addresses, tokens, addressStore, tokenStore, logger)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := monitor.Start(ctx); err != nil {
				logger.Error("settla-node: chain monitor stopped", "error", err)
			}
		}()
		logger.Info("settla-node: chain monitor started", "chains", len(chainPollers))
	}

	// ── Address pool refill goroutine ────────────────────────────
	// Periodically checks pool levels for all active tenant+chain combinations
	// and refills when below threshold. Uses TenantIndex (Redis SSCAN) or
	// falls back to paginated Postgres queries.
	poolRefillInterval := time.Duration(envIntOrDefault("SETTLA_POOL_REFILL_INTERVAL_SEC", 60)) * time.Second

	// Build a tenant iterator: prefer Redis TenantIndex, fall back to paginated Postgres.
	poolRefillForEach := func(ctx context.Context, fn func(ids []uuid.UUID) error) error {
		if tenantIndex != nil {
			return tenantIndex.ForEach(ctx, domain.DefaultTenantBatchSize, fn)
		}
		fetcher := func(ctx context.Context, limit, offset int32) ([]uuid.UUID, error) {
			return transferQueries.ListActiveTenantIDsPaginated(ctx, transferdb.ListActiveTenantIDsPaginatedParams{
				Limit: limit, Offset: offset,
			})
		}
		return domain.ForEachTenantBatch(ctx, fetcher, domain.DefaultTenantBatchSize, fn)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(poolRefillInterval)
		defer ticker.Stop()
		logger.Info("settla-node: address pool refill goroutine started", "interval", poolRefillInterval)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				chains := []string{"tron"}
				if os.Getenv("SETTLA_ETH_RPC_URL") != "" {
					chains = append(chains, "ethereum")
				}
				if os.Getenv("SETTLA_BASE_RPC_URL") != "" {
					chains = append(chains, "base")
				}
				err := poolRefillForEach(ctx, func(ids []uuid.UUID) error {
					for _, tid := range ids {
						for _, chain := range chains {
							generated, err := addressPoolMgr.RefillIfNeeded(ctx, tid, chain)
							if err != nil {
								logger.Error("settla-node: pool refill failed",
									"tenant_id", tid,
									"chain", chain,
									"error", err,
								)
								continue
							}
							if generated > 0 {
								logger.Info("settla-node: pool refilled",
									"tenant_id", tid,
									"chain", chain,
									"generated", generated,
								)
							}
						}
					}
					return nil
				})
				if err != nil {
					logger.Error("settla-node: pool refill iteration failed", "error", err)
				}
			}
		}
	}()
	logger.Info("settla-node: address pool refill started", "interval", poolRefillInterval)

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
		"dedicated_workers", 9, // treasury, ledger, provider, blockchain, webhook, inbound-webhook, bank-deposit, recovery-detector, dlq-monitor
		"outbox_relay", true,
	)

	<-ctx.Done()

	logger.Info("settla-node shutting down...")

	// 1. Drain: reject new health/metrics requests during shutdown.
	if err := drainer.Drain(); err != nil {
		logger.Warn("settla-node: drain incomplete", "error", err)
	}

	// 2. Drain NATS connection: stop receiving new messages, flush pending ACKs.
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		if err := natsClient.Conn.Drain(); err != nil {
			logger.Warn("settla-node: NATS drain error", "error", err)
		} else {
			logger.Info("settla-node: NATS drain complete")
		}
	}()
	select {
	case <-drainDone:
	case <-time.After(15 * time.Second):
		logger.Warn("settla-node: NATS drain timed out after 15s")
	}

	// 3. Stop all transfer workers
	for _, w := range transferWorkers {
		w.Stop()
	}
	// 4. Stop dedicated workers
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
		m[string(chain)] = c
	}
	return m
}

func rejectInsecureSSLInProduction(envKey, url string, logger *slog.Logger) {
	if os.Getenv("SETTLA_ENV") == "production" && strings.Contains(url, "sslmode=disable") {
		logger.Error("FATAL: sslmode=disable is not allowed in production — use sslmode=verify-ca or sslmode=verify-full",
			"env_key", envKey)
		os.Exit(1)
	}
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

// newPgxPool creates a pgxpool with explicit connection limits.
func newPgxPool(ctx context.Context, connString string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, err
	}
	config.MaxConns = int32(envIntOrDefault("SETTLA_DB_MAX_CONNS", 50))
	config.MinConns = int32(envIntOrDefault("SETTLA_DB_MIN_CONNS", 10))
	config.MaxConnIdleTime = 2 * time.Minute
	config.MaxConnLifetime = 30 * time.Minute
	return pgxpool.NewWithConfig(ctx, config)
}
