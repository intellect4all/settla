package main

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
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
	"github.com/intellect4all/settla/internal/appconfig"
	"github.com/intellect4all/settla/ledger"
	"github.com/intellect4all/settla/node/chainmonitor"
	"github.com/intellect4all/settla/node/chainmonitor/rpc"
	"github.com/intellect4all/settla/node/chainmonitor/wallet"
	"github.com/intellect4all/settla/node/messaging"
	"github.com/intellect4all/settla/node/outbox"
	"github.com/intellect4all/settla/node/worker"
	"github.com/intellect4all/settla/observability"
	"github.com/intellect4all/settla/observability/healthcheck"
	_ "github.com/intellect4all/settla/rail/provider/all"
	"github.com/intellect4all/settla/rail/router"
	"github.com/intellect4all/settla/resilience"
	"github.com/intellect4all/settla/resilience/drain"
	"github.com/intellect4all/settla/resilience/featureflag"
	"github.com/intellect4all/settla/store/ledgerdb"
	"github.com/intellect4all/settla/store/dbpool"
	"github.com/intellect4all/settla/store/transferdb"
	"github.com/intellect4all/settla/store/treasurydb"
	"github.com/intellect4all/settla/treasury"
)

// nodeVersion is set at build time via: go build -ldflags "-X main.nodeVersion=1.2.3"
// Falls back to "dev" if not injected.
var nodeVersion = "dev"

func main() {
	logger := observability.NewLogger("settla-node", nodeVersion)
	metrics := observability.NewMetrics()

	cfg, err := appconfig.LoadNodeConfig()
	if err != nil {
		logger.Error("settla-node: config error", "error", err)
		os.Exit(1)
	}
	logger.Info("settla-node starting...", "env", cfg.Env)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	tracerShutdown, err := observability.InitTracer(ctx, "settla-node", nodeVersion, logger)
	if err != nil {
		logger.Warn("settla-node: tracer init failed, continuing without tracing", "error", err)
	} else {
		defer tracerShutdown(ctx)
	}

	natsClient := connectNATS(ctx, cfg, logger)

	pools := connectDatabases(ctx, cfg, logger, metrics)

	// RoutedPools route Query/QueryRow to replica, Exec/CopyFrom to primary.
	// See store/dbpool package for details and read-your-writes guidance.
	transferRP := dbpool.New(pools.transfer, pools.transferRead)
	defer transferRP.Close()
	ledgerPool := pools.ledger // ledger has no replica in node (no read-heavy paths)
	treasuryRP := dbpool.New(pools.treasury, pools.treasuryRead)
	defer treasuryRP.Close()

	transferPool := pools.transfer // primary — for transactions

	runMigrations(cfg, logger)

	// SQLC Queries are backed by RoutedPool — reads go to replica automatically.
	transferQueries := transferdb.New(transferRP)
	transferStore := transferdb.NewTransferStoreAdapterWithOptions(transferQueries,
		transferdb.WithTxPool(transferPool),
	)
	tenantStore := transferdb.NewTenantStoreAdapter(transferQueries)
	tenantIndex := connectRedis(ctx, cfg, transferQueries, logger)
	treasuryStore := treasurydb.NewTreasuryStoreAdapter(treasurydb.New(treasuryRP))

	rawPublisher := messaging.NewPublisher(natsClient)
	publisherCB := resilience.NewCircuitBreaker("nats-publisher",
		resilience.WithFailureThreshold(5),
		resilience.WithResetTimeout(10*time.Second),
		resilience.WithHalfOpenMax(3),
		resilience.WithSuccessThreshold(2),
	)
	publisher := messaging.NewCircuitBreakerPublisher(rawPublisher, publisherCB)

	tbClient := connectTigerBeetle(cfg, logger)
	defer tbClient.Close()

	ledgerSvc := initLedger(cfg, tbClient, ledgerPool, publisher, logger, metrics)
	defer ledgerSvc.Stop()

	treasurySvc := initTreasury(ctx, cfg, treasuryStore, publisher, logger, metrics)
	defer treasurySvc.Stop()

	// Rail: provider registry
	providerReg, _, walletMgr := bootstrapProviders(cfg, logger)
	if walletMgr != nil {
		defer walletMgr.Close()
	}

	// Router + Core engine
	railRouter := router.NewRouter(providerReg, tenantStore, logger)
	coreRouterAdapter := router.NewCoreRouterAdapter(railRouter, tenantStore, logger)
	engine := core.NewEngine(transferStore, tenantStore, coreRouterAdapter, providerReg, logger, metrics)

	// Feature flags
	flagManager := featureflag.NewManager(cfg.FeatureFlagsPath, logger)
	go flagManager.Start(ctx)
	logger.Info("settla-node: feature flags loaded", "config_path", cfg.FeatureFlagsPath)

	// Compensation + Recovery
	compensationStore := transferdb.NewCompensationStoreAdapter(transferQueries)
	compensationExecutor := compensation.NewExecutor(compensationStore, engine, logger)
	logger.Info("settla-node: compensation executor ready", "executor", fmt.Sprintf("%T", compensationExecutor))

	reviewStore := transferdb.NewReviewStoreAdapter(transferQueries)
	recoveryQueryStore := transferdb.NewRecoveryQueryAdapter(transferQueries, cfg.RecoveryMaxResults)
	recoveryDetector := recovery.NewDetector(recoveryQueryStore, reviewStore, engine, &stubProviderStatusChecker{}, logger)

	// Deposit engines
	depositStoreAdapter := transferdb.NewDepositStoreAdapter(transferQueries, transferPool)
	depositEngine := depositcore.NewEngine(depositStoreAdapter, tenantStore, logger)

	bankDepositStoreAdapter := transferdb.NewBankDepositStoreAdapter(transferQueries, transferPool)
	bankDepositEngine := bankdepositcore.NewEngine(bankDepositStoreAdapter, tenantStore, logger)

	bankPartnerRegistry := bank.NewRegistry()
	bankPartnerRegistry.Register(bankmock.NewMockSettlaBank())
	if !cfg.Env.IsDev() {
		logger.Warn("settla-node: PRODUCTION — using mock bank partner; wire real bank integrations before launch")
	}

	// Address pool
	var addressDeriver transferdb.HDWalletDeriver
	if cfg.SigningServiceURL != "" {
		addressDeriver = wallet.NewTronDeriver(cfg.SigningServiceURL, &http.Client{Timeout: 10 * time.Second}, logger)
		logger.Info("settla-node: address deriver configured", "mode", "signing-service")
	} else {
		addressDeriver = wallet.NewStaticPoolDeriver(wallet.DefaultTestAddresses(), logger)
		if !cfg.Env.IsDev() {
			logger.Warn("settla-node: PRODUCTION — using static test addresses; set SETTLA_SIGNING_SERVICE_URL for real address derivation")
		} else {
			logger.Info("settla-node: address deriver configured", "mode", "static-pool")
		}
	}
	addressPoolMgr := transferdb.NewAddressPoolManager(transferQueries, addressDeriver, transferdb.DefaultPoolConfig(), logger)

	// Email sender
	emailSender := initEmailSender(cfg, logger)

	// Provider maps
	onRampProviders := buildOnRampMap(providerReg, logger)
	offRampProviders := buildOffRampMap(providerReg, logger)
	blockchainClients := buildBlockchainMap(providerReg, logger)
	providerTxStore := transferdb.NewProviderTxAdapter(transferPool)
	webhookLogStore := transferdb.NewProviderWebhookLogAdapter(transferPool)

	drainTimeout := time.Duration(cfg.DrainTimeoutMs) * time.Millisecond
	drainer := drain.NewDrainer(drainTimeout, logger)

	checker := buildHealthChecker(transferPool, ledgerPool, pools.treasury, natsClient, tbClient, logger)
	healthHandler := healthcheck.NewHandler(checker, 100000)

	dlqMetrics := worker.NewDLQMetrics()
	dlqMonitor := worker.NewDLQMonitor(natsClient, logger, dlqMetrics)

	metricsMux := http.NewServeMux()
	healthHandler.Register(metricsMux)
	metricsMux.Handle("/metrics", promhttp.Handler())
	if cfg.PprofEnabled {
		metricsMux.HandleFunc("/debug/pprof/", pprof.Index)
		metricsMux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		metricsMux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		metricsMux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		metricsMux.HandleFunc("/debug/pprof/trace", pprof.Trace)
		logger.Info("settla-node: pprof enabled on /debug/pprof/")
	}
	settlagrpc.RegisterDLQHandlers(metricsMux, newDLQInspectorAdapter(dlqMonitor), logger)
	metricsServer := &http.Server{
		Addr:    net.JoinHostPort("0.0.0.0", cfg.MetricsPort),
		Handler: drain.HTTPMiddleware(drainer, metricsMux),
	}
	go func() {
		logger.Info("metrics + health server listening", "port", cfg.MetricsPort)
		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("metrics server failed", "error", err)
		}
	}()

	relay := startOutboxRelay(ctx, cfg, transferQueries, natsClient, logger)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := relay.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Error("settla-node: outbox relay failed", "error", err)
		}
	}()
	logger.Info("settla-node: outbox relay started")

	transferWorkers := startTransferWorkers(ctx, &wg, cfg, engine, natsClient, logger, metrics)

	// Register transfer worker liveness checks.
	var livenessWorkers []healthcheck.WorkerLivenessSource
	for _, tw := range transferWorkers {
		livenessWorkers = append(livenessWorkers, tw)
	}
	if len(livenessWorkers) > 0 {
		checker.RegisterCheck(healthcheck.NewWorkerLivenessCheck(
			"transfer_workers", livenessWorkers, 5*time.Minute,
		))
	}

	treasuryWorker := worker.NewTreasuryWorker(treasurySvc, natsClient, logger, messaging.WithPoolSize(cfg.Workers.Treasury))
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := treasuryWorker.Start(ctx); err != nil {
			logger.Error("settla-node: treasury worker failed", "error", err)
		}
	}()

	ledgerCB := resilience.NewCircuitBreaker("ledger",
		resilience.WithFailureThreshold(5),
		resilience.WithResetTimeout(30*time.Second),
	)
	ledgerWithCB := resilience.NewCircuitBreakerLedger(ledgerSvc, ledgerCB)

	type stoppable interface{ Stop() }
	var partitionedWorkers []stoppable

	// startWorker launches a worker in a goroutine with error logging.
	// If a worker's Start returns an error (and the context isn't cancelled),
	// it is logged as an error so operators and health checks can detect it.
	startWorker := func(name string, partition int, start func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := start(ctx); err != nil && ctx.Err() == nil {
				logger.Error("settla-node: worker failed",
					"worker", name,
					"partition", partition,
					"error", err,
				)
			}
		}()
	}

	startPartitionedWorkers := func(startP, endP int) {
		for i := startP; i < endP; i++ {
			p := i
			lw := worker.NewLedgerWorker(p, ledgerWithCB, natsClient, logger, messaging.WithPoolSize(cfg.Workers.Ledger))
			partitionedWorkers = append(partitionedWorkers, lw)
			startWorker("ledger", p, lw.Start)

			pw := worker.NewProviderWorker(p, onRampProviders, offRampProviders, providerTxStore, engine, natsClient, logger, nil, messaging.WithPoolSize(cfg.Workers.Provider))
			partitionedWorkers = append(partitionedWorkers, pw)
			startWorker("provider", p, pw.Start)

			iww := worker.NewInboundWebhookWorker(p, providerTxStore, engine, natsClient, logger,
				func(slug string) domain.WebhookNormalizer { return providerReg.GetNormalizer(slug) },
				webhookLogStore, messaging.WithPoolSize(cfg.Workers.InboundWH))
			partitionedWorkers = append(partitionedWorkers, iww)
			startWorker("inbound-webhook", p, iww.Start)

			bw := worker.NewBlockchainWorker(p, blockchainClients, providerTxStore, engine, natsClient, logger, reviewStore, messaging.WithPoolSize(cfg.Workers.Blockchain))
			partitionedWorkers = append(partitionedWorkers, bw)
			startWorker("blockchain", p, bw.Start)

			ww := worker.NewWebhookWorker(p, tenantStore, natsClient, logger, nil, nil, messaging.WithPoolSize(cfg.Workers.Webhook))
			partitionedWorkers = append(partitionedWorkers, ww)
			startWorker("webhook", p, ww.Start)

			dw := worker.NewDepositWorker(p, depositEngine, treasurySvc, natsClient, logger, messaging.WithPoolSize(cfg.Workers.Deposit))
			partitionedWorkers = append(partitionedWorkers, dw)
			startWorker("deposit", p, dw.Start)

			ew := worker.NewEmailWorker(p, tenantStore, emailSender, natsClient, logger, messaging.WithPoolSize(cfg.Workers.Email))
			partitionedWorkers = append(partitionedWorkers, ew)
			startWorker("email", p, ew.Start)

			bdw := worker.NewBankDepositWorker(p, bankDepositEngine, treasurySvc, bankDepositStoreAdapter, bankDepositStoreAdapter, bankPartnerRegistry, natsClient, logger, metrics, messaging.WithPoolSize(cfg.Workers.BankDeposit))
			partitionedWorkers = append(partitionedWorkers, bdw)
			startWorker("bank-deposit", p, bdw.Start)
		}
	}

	if cfg.PartitionID >= 0 {
		logger.Info("settla-node: starting partitioned workers", "partition", cfg.PartitionID, "total_partitions", cfg.NodePartitions)
		startPartitionedWorkers(cfg.PartitionID, cfg.PartitionID+1)
	} else {
		logger.Info("settla-node: dev mode — starting all partition workers", "total_partitions", cfg.NodePartitions)
		startPartitionedWorkers(0, cfg.NodePartitions)
	}

	startBackgroundJob(&wg, ctx, "recovery detector", func(ctx context.Context) error { return recoveryDetector.Run(ctx) })
	startBackgroundJob(&wg, ctx, "DLQ monitor", func(ctx context.Context) error { return dlqMonitor.Start(ctx) })

	depositExpiryJob := worker.NewDepositExpiryJob(depositStoreAdapter, depositEngine, logger)
	wg.Add(1)
	go func() { defer wg.Done(); depositExpiryJob.Run(ctx) }()

	bankDepositExpiryJob := worker.NewBankDepositExpiryJob(bankDepositStoreAdapter, bankDepositEngine, logger)
	wg.Add(1)
	go func() { defer wg.Done(); bankDepositExpiryJob.Run(ctx) }()

	startVirtualAccountProvisioner(&wg, ctx, bankDepositStoreAdapter, bankPartnerRegistry, tenantIndex, transferQueries, logger)
	startChainMonitor(&wg, ctx, cfg, transferQueries, transferPool, logger)
	startAddressPoolRefill(&wg, ctx, cfg, addressPoolMgr, tenantIndex, transferQueries, logger)
	startConsumerLagMetrics(ctx, natsClient)

	checker.MarkStartupComplete()
	logger.Info("settla-node ready",
		"transfer_workers", len(transferWorkers),
		"dedicated_workers", 9,
		"outbox_relay", true,
	)

	<-ctx.Done()

	logger.Info("settla-node shutting down...")
	if err := drainer.Drain(); err != nil {
		logger.Warn("settla-node: drain incomplete", "error", err)
	}
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		if err := natsClient.Conn.Drain(); err != nil {
			logger.Warn("settla-node: NATS drain error", "error", err)
		}
	}()
	select {
	case <-drainDone:
	case <-time.After(15 * time.Second):
		logger.Warn("settla-node: NATS drain timed out after 15s")
	}
	for _, w := range transferWorkers {
		w.Stop()
	}
	treasuryWorker.Stop()
	for _, w := range partitionedWorkers {
		w.Stop()
	}
	dlqMonitor.Stop()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := metricsServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("settla-node: metrics server shutdown error", "error", err)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		logger.Info("settla-node shutdown complete")
	case <-time.After(60 * time.Second):
		logger.Error("settla-node shutdown timed out after 60s")
	}
}


func connectNATS(ctx context.Context, cfg *appconfig.NodeConfig, logger *slog.Logger) *messaging.Client {
	var opts []messaging.ClientOption
	opts = append(opts, messaging.WithReplicas(cfg.NATSInitialReplicas))
	if cfg.NATSToken != "" {
		opts = append(opts, messaging.WithNATSToken(cfg.NATSToken))
	} else if cfg.NATSUser != "" {
		opts = append(opts, messaging.WithNATSUserInfo(cfg.NATSUser, cfg.NATSPassword))
	}
	natsClient, err := messaging.NewClient(cfg.NATSURL, cfg.NodePartitions, logger, opts...)
	if err != nil {
		logger.Error("settla-node: failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	if err := natsClient.EnsureStreams(ctx); err != nil {
		logger.Error("settla-node: failed to ensure streams", "error", err)
		os.Exit(1)
	}
	return natsClient
}

type dbPools struct {
	transfer, ledger, treasury                   *pgxpool.Pool
	transferRead, ledgerRead, treasuryRead       *pgxpool.Pool
}

func connectDatabases(ctx context.Context, cfg *appconfig.NodeConfig, logger *slog.Logger, metrics *observability.Metrics) dbPools {
	var pools dbPools
	var err error

	pools.transfer, err = appconfig.NewPgxPool(ctx, cfg.TransferDBURL, cfg.DBMaxConns, cfg.DBMinConns)
	if err != nil {
		logger.Error("settla-node: failed to connect to transfer DB", "error", err)
		os.Exit(1)
	}
	observability.RegisterPoolMetrics(ctx, pools.transfer, "transfer", metrics)
	logger.Info("settla-node: connected to transfer DB")

	pools.ledger, err = appconfig.NewPgxPool(ctx, cfg.LedgerDBURL, cfg.DBMaxConns, cfg.DBMinConns)
	if err != nil {
		logger.Error("settla-node: failed to connect to ledger DB", "error", err)
		os.Exit(1)
	}
	observability.RegisterPoolMetrics(ctx, pools.ledger, "ledger", metrics)
	logger.Info("settla-node: connected to ledger DB")

	pools.treasury, err = appconfig.NewPgxPool(ctx, cfg.TreasuryDBURL, cfg.DBMaxConns, cfg.DBMinConns)
	if err != nil {
		logger.Error("settla-node: failed to connect to treasury DB", "error", err)
		os.Exit(1)
	}
	observability.RegisterPoolMetrics(ctx, pools.treasury, "treasury", metrics)
	logger.Info("settla-node: connected to treasury DB")

	// Connect read replicas when configured.
	if cfg.TransferDBReadURL != "" {
		pools.transferRead, err = appconfig.NewPgxPool(ctx, cfg.TransferDBReadURL, cfg.DBMaxConns, cfg.DBMinConns)
		if err != nil {
			logger.Warn("settla-node: failed to connect to transfer read replica, falling back to primary", "error", err)
		} else {
			observability.RegisterPoolMetrics(ctx, pools.transferRead, "transfer_read", metrics)
			logger.Info("settla-node: connected to transfer read replica")
		}
	}
	if cfg.TreasuryDBReadURL != "" {
		pools.treasuryRead, err = appconfig.NewPgxPool(ctx, cfg.TreasuryDBReadURL, cfg.DBMaxConns, cfg.DBMinConns)
		if err != nil {
			logger.Warn("settla-node: failed to connect to treasury read replica, falling back to primary", "error", err)
		} else {
			observability.RegisterPoolMetrics(ctx, pools.treasuryRead, "treasury_read", metrics)
			logger.Info("settla-node: connected to treasury read replica")
		}
	}

	return pools
}

func runMigrations(cfg *appconfig.NodeConfig, logger *slog.Logger) {
	sub, err := fs.Sub(settladb.TransferMigrations, "migrations/transfer")
	if err != nil {
		logger.Error("settla-node: failed to load transfer migrations", "error", err)
		os.Exit(1)
	}
	if err := automigrate.Run(automigrate.Transfer, cfg.TransferDBMigrateURL, sub, logger); err != nil {
		logger.Error("settla-node: transfer DB migration failed", "error", err)
		os.Exit(1)
	}
	sub, err = fs.Sub(settladb.LedgerMigrations, "migrations/ledger")
	if err != nil {
		logger.Error("settla-node: failed to load ledger migrations", "error", err)
		os.Exit(1)
	}
	if err := automigrate.Run(automigrate.Ledger, cfg.LedgerDBMigrateURL, sub, logger); err != nil {
		logger.Error("settla-node: ledger DB migration failed", "error", err)
		os.Exit(1)
	}
	sub, err = fs.Sub(settladb.TreasuryMigrations, "migrations/treasury")
	if err != nil {
		logger.Error("settla-node: failed to load treasury migrations", "error", err)
		os.Exit(1)
	}
	if err := automigrate.Run(automigrate.Treasury, cfg.TreasuryDBMigrateURL, sub, logger); err != nil {
		logger.Error("settla-node: treasury DB migration failed", "error", err)
		os.Exit(1)
	}
}

func connectRedis(ctx context.Context, cfg *appconfig.NodeConfig, transferQueries *transferdb.Queries, logger *slog.Logger) *cache.TenantIndex {
	if cfg.RedisURL == "" {
		return nil
	}
	redisOpts, err := cache.ParseRedisURL(cfg.RedisURL)
	if err != nil || redisOpts == nil {
		logger.Warn("settla-node: invalid Redis URL, tenant index disabled", "error", err)
		return nil
	}
	redisClient := cache.NewRedisClientFromOpts(redisOpts)
	if pingErr := redisClient.Ping(ctx).Err(); pingErr != nil {
		logger.Warn("settla-node: Redis unavailable", "error", pingErr)
		redisClient.Close()
		return nil
	}
	paginatedFetcher := func(ctx context.Context, limit, offset int32) ([]uuid.UUID, error) {
		return transferQueries.ListActiveTenantIDsPaginated(ctx, transferdb.ListActiveTenantIDsPaginatedParams{
			Limit: limit, Offset: offset,
		})
	}
	tenantIndex := cache.NewTenantIndex(redisClient, paginatedFetcher, logger)
	if rebuildErr := tenantIndex.Rebuild(ctx); rebuildErr != nil {
		logger.Warn("settla-node: tenant index initial rebuild failed", "error", rebuildErr)
	} else {
		count, _ := tenantIndex.Count(ctx)
		logger.Info("settla-node: tenant index initialized", "tenants", count)
	}
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
	return tenantIndex
}

func connectTigerBeetle(cfg *appconfig.NodeConfig, logger *slog.Logger) ledger.TBClient {
	addresses := ledger.ParseTBAddresses(cfg.TigerBeetleAddresses)
	tbClient, err := ledger.NewRealTBClient(0, addresses)
	if err != nil {
		logger.Error("settla-node: failed to connect to TigerBeetle", "addresses", cfg.TigerBeetleAddresses, "error", err)
		os.Exit(1)
	}
	logger.Info("settla-node: connected to TigerBeetle", "addresses", addresses)
	return tbClient
}


func initLedger(cfg *appconfig.NodeConfig, tbClient ledger.TBClient, ledgerPool *pgxpool.Pool, publisher *messaging.CircuitBreakerPublisher, logger *slog.Logger, metrics *observability.Metrics) *ledger.Service {
	pgBackend := ledger.NewPGBackend(ledgerdb.New(ledgerPool), logger)
	svc := ledger.NewService(tbClient, pgBackend, publisher, logger, metrics,
		ledger.WithBatchWindow(time.Duration(cfg.LedgerBatchWindowMs)*time.Millisecond),
		ledger.WithBatchMaxSize(cfg.LedgerBatchMaxSize),
	)
	svc.Start()
	return svc
}

func initTreasury(ctx context.Context, cfg *appconfig.NodeConfig, store treasury.Store, publisher *messaging.CircuitBreakerPublisher, logger *slog.Logger, metrics *observability.Metrics) *treasury.Manager {
	mgr := treasury.NewManager(store, publisher, logger, metrics,
		treasury.WithFlushInterval(time.Duration(cfg.TreasuryFlushIntervalMs)*time.Millisecond),
	)
	if err := mgr.LoadPositions(ctx); err != nil {
		logger.Error("settla-node: failed to load treasury positions", "error", err)
		os.Exit(1)
	}
	mgr.Start()
	logger.Info("settla-node: treasury loaded", "positions", mgr.PositionCount())
	return mgr
}

func initEmailSender(cfg *appconfig.NodeConfig, logger *slog.Logger) worker.EmailSender {
	if cfg.ResendAPIKey != "" {
		logger.Info("settla-node: email sender configured", "provider", "resend", "from", cfg.EmailFrom)
		return worker.NewResendEmailSender(cfg.ResendAPIKey, cfg.EmailFrom, logger)
	}
	logger.Info("settla-node: email sender configured", "provider", "log-only")
	return worker.NewLogEmailSender(logger)
}

func buildHealthChecker(transferPool, ledgerPool, treasuryPool *pgxpool.Pool, natsClient *messaging.Client, tbClient ledger.TBClient, logger *slog.Logger) *healthcheck.Checker {
	checks := []healthcheck.Check{
		healthcheck.NewCallbackCheck("postgres_transfer", false,
			func(ctx context.Context) error { return transferPool.Ping(ctx) }),
		healthcheck.NewCallbackCheck("postgres_ledger", false,
			func(ctx context.Context) error { return ledgerPool.Ping(ctx) }),
		healthcheck.NewCallbackCheck("postgres_treasury", false,
			func(ctx context.Context) error { return treasuryPool.Ping(ctx) }),
		healthcheck.NewNATSCheck(func(ctx context.Context) error {
			if !natsClient.Conn.IsConnected() {
				return fmt.Errorf("NATS connection not active")
			}
			for _, sd := range messaging.AllStreams() {
				if _, err := natsClient.JS.Stream(ctx, sd.Name); err != nil {
					return fmt.Errorf("stream %s not accessible: %w", sd.Name, err)
				}
			}
			return nil
		}),
		healthcheck.NewCallbackCheck("tigerbeetle", false,
			func(ctx context.Context) error {
				_, err := tbClient.LookupAccounts([]ledger.ID128{{}})
				return err
			}),
		healthcheck.NewGoroutineCheck(100000),
	}
	return healthcheck.NewChecker(logger, checks, healthcheck.WithVersion(nodeVersion))
}

func startOutboxRelay(ctx context.Context, cfg *appconfig.NodeConfig, transferQueries *transferdb.Queries, natsClient *messaging.Client, logger *slog.Logger) *outbox.Relay {
	outboxStore := transferdb.NewOutboxRelayAdapter(transferQueries)
	natsPublisher := outbox.NewNATSPublisher(natsClient)
	return outbox.NewRelay(outboxStore, natsPublisher, logger,
		outbox.WithNumPartitions(cfg.NodePartitions),
		outbox.WithBatchSize(int32(cfg.RelayBatchSize)),
		outbox.WithPollInterval(time.Duration(cfg.RelayPollIntervalMs)*time.Millisecond),
		outbox.WithMetrics(outbox.NewRelayMetrics()),
	)
}

func startTransferWorkers(ctx context.Context, wg *sync.WaitGroup, cfg *appconfig.NodeConfig, engine *core.Engine, natsClient *messaging.Client, logger *slog.Logger, metrics *observability.Metrics) []*worker.TransferWorker {
	var workers []*worker.TransferWorker
	startOne := func(p int) {
		w := worker.NewTransferWorker(p, engine, natsClient, logger, metrics, messaging.WithPoolSize(cfg.Workers.Transfer))
		workers = append(workers, w)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w.Start(ctx); err != nil {
				logger.Error("settla-node: transfer worker failed", "partition", p, "error", err)
			}
		}()
	}
	if cfg.PartitionID >= 0 {
		logger.Info("settla-node: starting transfer worker", "partition", cfg.PartitionID)
		startOne(cfg.PartitionID)
	} else {
		logger.Info("settla-node: dev mode — starting all transfer workers", "partitions", cfg.NodePartitions)
		for i := range cfg.NodePartitions {
			startOne(i)
		}
	}
	return workers
}

func startBackgroundJob(wg *sync.WaitGroup, ctx context.Context, name string, fn func(context.Context) error) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := fn(ctx); err != nil && ctx.Err() == nil {
			slog.Error("settla-node: "+name+" failed", "error", err)
		}
	}()
	slog.Info("settla-node: " + name + " started")
}

func startVirtualAccountProvisioner(wg *sync.WaitGroup, ctx context.Context, bankDepositStore *transferdb.BankDepositStoreAdapter, bankPartnerRegistry *bank.Registry, tenantIndex *cache.TenantIndex, transferQueries *transferdb.Queries, logger *slog.Logger) {
	vaForEach := func(ctx context.Context, batchSize int32, fn func(ids []uuid.UUID) error) error {
		if tenantIndex != nil {
			return tenantIndex.ForEach(ctx, batchSize, fn)
		}
		fetcher := func(ctx context.Context, limit, offset int32) ([]uuid.UUID, error) {
			return transferQueries.ListActiveTenantIDsPaginated(ctx, transferdb.ListActiveTenantIDsPaginatedParams{Limit: limit, Offset: offset})
		}
		return domain.ForEachTenantBatch(ctx, fetcher, batchSize, fn)
	}
	vaProvisioner := worker.NewVirtualAccountProvisioner(bankDepositStore, bankPartnerRegistry, vaForEach, logger)
	wg.Add(1)
	go func() { defer wg.Done(); vaProvisioner.Run(ctx) }()
	logger.Info("settla-node: virtual account provisioner started")
}

func startChainMonitor(wg *sync.WaitGroup, ctx context.Context, cfg *appconfig.NodeConfig, transferQueries *transferdb.Queries, transferPool *pgxpool.Pool, logger *slog.Logger) {
	checkpointStore := transferdb.NewCheckpointStoreAdapter(transferQueries)
	checkpointMgr := chainmonitor.NewCheckpointManager(checkpointStore)
	addressStore := transferdb.NewAddressStoreAdapter(transferQueries)
	tokenStore := transferdb.NewTokenStoreAdapter(transferQueries)
	outboxWriter := transferdb.NewOutboxWriterAdapter(transferQueries, transferPool)
	addresses := chainmonitor.NewAddressSet()
	tokens := chainmonitor.NewTokenRegistry()
	var chainPollers []chainmonitor.ChainPoller

	if cfg.EthRPCURL != "" {
		ethCfg := chainmonitor.DefaultEthereumConfig()
		ethCfg.RPCURL = cfg.EthRPCURL
		ethCfg.APIKey = cfg.EthRPCAPIKey
		if cfg.EthRPCBackupURL != "" {
			ethCfg.BackupRPCURL = cfg.EthRPCBackupURL
			ethCfg.BackupAPIKey = cfg.EthRPCBackupAPIKey
		}
		providers := []*rpc.Provider{{Name: "eth-primary", RPCURL: ethCfg.RPCURL, APIKey: ethCfg.APIKey}}
		if ethCfg.BackupRPCURL != "" {
			providers = append(providers, &rpc.Provider{Name: "eth-backup", RPCURL: ethCfg.BackupRPCURL, APIKey: ethCfg.BackupAPIKey})
		}
		chainPollers = append(chainPollers, chainmonitor.NewEVMPoller(ethCfg, rpc.NewEVMClient(providers, logger), addresses, tokens, checkpointMgr, outboxWriter, logger))
	}

	if cfg.BaseRPCURL != "" {
		baseCfg := chainmonitor.DefaultBaseConfig()
		baseCfg.RPCURL = cfg.BaseRPCURL
		baseCfg.APIKey = cfg.BaseRPCAPIKey
		if cfg.BaseRPCBackupURL != "" {
			baseCfg.BackupRPCURL = cfg.BaseRPCBackupURL
			baseCfg.BackupAPIKey = cfg.BaseRPCBackupAPIKey
		}
		providers := []*rpc.Provider{{Name: "base-primary", RPCURL: baseCfg.RPCURL, APIKey: baseCfg.APIKey}}
		if baseCfg.BackupRPCURL != "" {
			providers = append(providers, &rpc.Provider{Name: "base-backup", RPCURL: baseCfg.BackupRPCURL, APIKey: baseCfg.BackupAPIKey})
		}
		chainPollers = append(chainPollers, chainmonitor.NewEVMPoller(baseCfg, rpc.NewEVMClient(providers, logger), addresses, tokens, checkpointMgr, outboxWriter, logger))
	}

	if cfg.TronRPCURL != "" {
		tronCfg := chainmonitor.DefaultTronConfig()
		tronCfg.RPCURL = cfg.TronRPCURL
		tronCfg.APIKey = cfg.TronRPCAPIKey
		providers := []*rpc.Provider{{Name: "tron-primary", RPCURL: tronCfg.RPCURL, APIKey: tronCfg.APIKey}}
		chainPollers = append(chainPollers, chainmonitor.NewTronPoller(tronCfg, rpc.NewTronClient(providers, logger), addresses, tokens, checkpointMgr, outboxWriter, logger))
	}

	if len(chainPollers) > 0 {
		monitor := chainmonitor.NewMonitor(chainmonitor.DefaultMonitorConfig(), chainPollers, addresses, tokens, addressStore, tokenStore, logger)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := monitor.Start(ctx); err != nil {
				logger.Error("settla-node: chain monitor stopped", "error", err)
			}
		}()
		logger.Info("settla-node: chain monitor started", "chains", len(chainPollers))
	}
}

func startAddressPoolRefill(wg *sync.WaitGroup, ctx context.Context, cfg *appconfig.NodeConfig, addressPoolMgr *transferdb.AddressPoolManager, tenantIndex *cache.TenantIndex, transferQueries *transferdb.Queries, logger *slog.Logger) {
	poolRefillInterval := time.Duration(cfg.PoolRefillIntervalSec) * time.Second
	poolRefillForEach := func(ctx context.Context, fn func(ids []uuid.UUID) error) error {
		if tenantIndex != nil {
			return tenantIndex.ForEach(ctx, domain.DefaultTenantBatchSize, fn)
		}
		fetcher := func(ctx context.Context, limit, offset int32) ([]uuid.UUID, error) {
			return transferQueries.ListActiveTenantIDsPaginated(ctx, transferdb.ListActiveTenantIDsPaginatedParams{Limit: limit, Offset: offset})
		}
		return domain.ForEachTenantBatch(ctx, fetcher, domain.DefaultTenantBatchSize, fn)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(poolRefillInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				chains := []string{"tron"}
				if cfg.EthRPCURL != "" {
					chains = append(chains, "ethereum")
				}
				if cfg.BaseRPCURL != "" {
					chains = append(chains, "base")
				}
				_ = poolRefillForEach(ctx, func(ids []uuid.UUID) error {
					for _, tid := range ids {
						for _, chain := range chains {
							if generated, err := addressPoolMgr.RefillIfNeeded(ctx, tid, chain); err != nil {
								logger.Error("settla-node: pool refill failed", "tenant_id", tid, "chain", chain, "error", err)
							} else if generated > 0 {
								logger.Info("settla-node: pool refilled", "tenant_id", tid, "chain", chain, "generated", generated)
							}
						}
					}
					return nil
				})
			}
		}
	}()
}

func startConsumerLagMetrics(ctx context.Context, natsClient *messaging.Client) {
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
}
