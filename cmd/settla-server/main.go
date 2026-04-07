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
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/shopspring/decimal"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"

	settlagrpc "github.com/intellect4all/settla/api/grpc"
	"github.com/intellect4all/settla/cache"
	"github.com/intellect4all/settla/core"
	"github.com/intellect4all/settla/core/analytics"
	bankdepositcore "github.com/intellect4all/settla/core/bankdeposit"
	depositcore "github.com/intellect4all/settla/core/deposit"
	"github.com/intellect4all/settla/core/maintenance"
	paymentlinkcore "github.com/intellect4all/settla/core/paymentlink"
	"github.com/intellect4all/settla/core/reconciliation"
	"github.com/intellect4all/settla/core/settlement"
	settladb "github.com/intellect4all/settla/db"
	"github.com/intellect4all/settla/db/automigrate"
	pb "github.com/intellect4all/settla/gen/settla/v1"
	"github.com/intellect4all/settla/internal/appconfig"
	"github.com/intellect4all/settla/ledger"
	"github.com/intellect4all/settla/node/messaging"
	"github.com/intellect4all/settla/observability"
	"github.com/intellect4all/settla/observability/healthcheck"
	"github.com/intellect4all/settla/observability/synthetic"
	"github.com/intellect4all/settla/rail/blockchain"
	"github.com/intellect4all/settla/rail/provider"
	_ "github.com/intellect4all/settla/rail/provider/all"
	"github.com/intellect4all/settla/rail/provider/factory"
	"github.com/intellect4all/settla/rail/router"
	"github.com/intellect4all/settla/rail/wallet"
	"github.com/intellect4all/settla/resilience"
	"github.com/intellect4all/settla/resilience/drain"
	"github.com/intellect4all/settla/resilience/featureflag"
	"github.com/intellect4all/settla/store/dbpool"
	"github.com/intellect4all/settla/store/ledgerdb"
	"github.com/intellect4all/settla/store/transferdb"
	"github.com/intellect4all/settla/store/treasurydb"
	"github.com/intellect4all/settla/treasury"
)

// version is set at build time via: go build -ldflags "-X main.version=1.2.3"
// Falls back to "dev" if not injected.
var version = "dev"

func main() {
	logger := observability.NewLogger("settla-server", version)
	metrics := observability.NewMetrics()

	cfg, err := appconfig.LoadServerConfig()
	if err != nil {
		logger.Error("settla-server: config error", "error", err)
		os.Exit(1)
	}
	logger.Info("settla-server starting...", "env", cfg.Env)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	tracerShutdown, err := observability.InitTracer(ctx, "settla-server", version, logger)
	if err != nil {
		logger.Warn("settla-server: tracer init failed, continuing without tracing", "error", err)
	} else {
		defer tracerShutdown(ctx)
	}

	pools := connectDatabases(ctx, cfg, logger, metrics)

	// RoutedPools route Query/QueryRow to replica, Exec/CopyFrom to primary.
	// See store/dbpool package for details and read-your-writes guidance.
	transferRP := dbpool.New(pools.transfer, pools.transferRead)
	defer transferRP.Close()
	ledgerRP := dbpool.New(pools.ledger, pools.ledgerRead)
	defer ledgerRP.Close()
	treasuryRP := dbpool.New(pools.treasury, pools.treasuryRead)
	defer treasuryRP.Close()

	// Primary pools — used for transactions and migrations.
	transferPool := pools.transfer
	ledgerPool := pools.ledger

	runMigrations(cfg, logger)

	publisher, natsClient := connectNATS(ctx, cfg, logger)
	defer natsClient.Close()

	var transferAppPool *pgxpool.Pool
	if cfg.TransferAppDBURL != "" {
		transferAppPool = connectTransferAppDB(ctx, cfg, logger)
		if transferAppPool != nil {
			defer transferAppPool.Close()
		}
	} else if cfg.Env.IsDev() {
		logger.Warn("settla-server: RLS not enforced — SETTLA_TRANSFER_APP_DB_URL is unset")
	}

	// SQLC Queries are backed by RoutedPool — reads go to replica automatically.
	transferQueries := transferdb.New(transferRP)
	tenantIndex, redisCache := connectRedis(ctx, cfg, transferQueries, logger)

	storeOpts := []transferdb.TransferStoreOption{
		transferdb.WithTxPool(transferPool),
	}
	if transferAppPool != nil {
		storeOpts = append(storeOpts, transferdb.WithAppPool(transferAppPool))
	}
	transferStore := transferdb.NewTransferStoreAdapterWithOptions(transferQueries, storeOpts...)
	tenantStore := transferdb.NewTenantStoreAdapter(transferQueries)
	treasuryStore := treasurydb.NewTreasuryStoreAdapter(treasurydb.New(treasuryRP))

	tbClient := connectTigerBeetle(cfg, logger)
	defer tbClient.Close()

	ledgerSvc := initLedger(cfg, tbClient, ledgerRP, publisher, logger, metrics)
	defer ledgerSvc.Stop()

	treasurySvc := initTreasury(ctx, cfg, treasuryStore, publisher, logger, metrics)
	defer treasurySvc.Stop()

	startPartitionManager(ctx, cfg, transferPool, logger)
	startVacuumManager(ctx, cfg, transferPool, logger)
	startCapacityMonitor(ctx, cfg, transferPool, logger)
	_ = startReconciler(ctx, cfg, transferQueries, ledgerRP, treasurySvc, tenantIndex, logger)
	startSettlementScheduler(ctx, cfg, transferQueries, logger)
	startSyntheticCanary(ctx, cfg, logger)

	providerReg, chainReg, walletMgr := bootstrapProviders(cfg, logger)
	if walletMgr != nil {
		defer walletMgr.Close()
	}

	var routerOpts []router.RouterOption
	if chainReg != nil {
		routerOpts = append(routerOpts, router.WithExplorerUrl(blockchain.Explorer{}))
	}
	railRouter := router.NewRouter(providerReg, tenantStore, logger, routerOpts...)
	coreRouterAdapter := router.NewCoreRouterAdapter(railRouter, tenantStore, logger)

	engine := initEngine(cfg, transferStore, tenantStore, coreRouterAdapter, providerReg, redisCache, logger, metrics)

	depositStoreAdapter := transferdb.NewDepositStoreAdapter(transferQueries, transferPool)
	if transferAppPool != nil {
		depositStoreAdapter.WithDepositAppPool(transferAppPool)
	}
	depositEngine := depositcore.NewEngine(depositStoreAdapter, tenantStore, logger)

	bankDepositStoreAdapter := transferdb.NewBankDepositStoreAdapter(transferQueries, transferPool)
	if transferAppPool != nil {
		bankDepositStoreAdapter.WithBankDepositAppPool(transferAppPool)
	}
	bankDepositEngine := bankdepositcore.NewEngine(bankDepositStoreAdapter, tenantStore, logger)

	paymentLinkStore := transferdb.NewPaymentLinkStoreAdapter(transferQueries, transferPool)
	if transferAppPool != nil {
		paymentLinkStore.WithPaymentLinkAppPool(transferAppPool)
	}
	paymentLinkSvc := paymentlinkcore.NewService(paymentLinkStore, depositEngine, tenantStore, logger, cfg.PaymentLinkBaseURL)

	drainTimeout := time.Duration(cfg.DrainTimeoutMs) * time.Millisecond
	drainer := drain.NewDrainer(drainTimeout, logger)

	checker := buildHealthChecker(transferPool, ledgerPool, pools.treasury, natsClient, tbClient, logger)

	opsStore := transferdb.NewOpsAdapter(transferQueries, tenantIndex)
	auditAdapter := transferdb.NewAuditAdapter(transferPool)

	mux := http.NewServeMux()
	healthcheck.NewHandler(checker, 100000).Register(mux)
	mux.Handle("/metrics", promhttp.Handler())
	if cfg.PprofEnabled {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
		logger.Info("settla-server: pprof enabled on /debug/pprof/")
	}
	settlagrpc.RegisterOpsHandlers(mux, opsStore, logger, auditAdapter)

	httpServer := &http.Server{
		Addr:    net.JoinHostPort("0.0.0.0", cfg.HTTPPort),
		Handler: drain.HTTPMiddleware(drainer, mux),
	}
	go func() {
		logger.Info("http server listening", "port", cfg.HTTPPort)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("http server failed", "error", err)
			os.Exit(1)
		}
	}()

	grpcAuthValidator := &apiKeyValidatorAdapter{q: transferQueries}
	grpcHMACSecret := []byte(cfg.APIKeyHMACSecret)

	grpcServer := grpc.NewServer(
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.ChainUnaryInterceptor(
			drain.GRPCUnaryInterceptor(drainer),
			observability.UnaryServerInterceptor(metrics),
			settlagrpc.APIKeyAuthInterceptor(grpcAuthValidator, grpcHMACSecret, logger),
		),
		grpc.ChainStreamInterceptor(
			drain.GRPCStreamInterceptor(drainer),
			settlagrpc.APIKeyAuthStreamInterceptor(grpcAuthValidator, grpcHMACSecret, logger),
		),
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
	if transferAppPool != nil {
		portalStore.WithPortalAppPool(transferAppPool)
	}
	webhookMgmtStore := transferdb.NewWebhookAdapter(transferQueries)
	analyticsStore := transferdb.NewAnalyticsAdapter(transferQueries)
	if transferAppPool != nil {
		analyticsStore.WithAnalyticsAppPool(transferAppPool)
	}
	extAnalyticsStore := transferdb.NewExtendedAnalyticsAdapter(transferQueries, transferPool)
	if transferAppPool != nil {
		extAnalyticsStore.WithExtAnalyticsAppPool(transferAppPool)
	}
	exportStore := transferdb.NewExportAdapter(transferQueries, transferPool)
	if transferAppPool != nil {
		exportStore.WithExportAppPool(transferAppPool)
	}
	snapshotStore := transferdb.NewSnapshotAdapter(transferQueries, transferPool)
	if tenantIndex != nil {
		snapshotStore.WithTenantForEach(tenantIndex.ForEach)
	}

	ledgerCB := resilience.NewCircuitBreaker("ledger",
		resilience.WithFailureThreshold(5),
		resilience.WithResetTimeout(30*time.Second),
	)
	ledgerWithCB := resilience.NewCircuitBreakerLedger(ledgerSvc, ledgerCB)
	portalAuthStore := transferdb.NewPortalAuthStoreAdapter(transferQueries, transferPool, tenantIndex)

	grpcSvc := settlagrpc.NewServer(engine, treasurySvc, ledgerWithCB, logger,
		settlagrpc.WithAuthStore(authStore),
		settlagrpc.WithTenantPortalStore(portalStore),
		settlagrpc.WithWebhookManagementStore(webhookMgmtStore),
		settlagrpc.WithAnalyticsStore(analyticsStore),
		settlagrpc.WithExtendedAnalyticsStore(extAnalyticsStore),
		settlagrpc.WithExportStore(exportStore),
		settlagrpc.WithPortalAuthStore(portalAuthStore),
		settlagrpc.WithJWTSecret(cfg.JWTSecret),
		settlagrpc.WithDepositEngine(depositEngine),
		settlagrpc.WithBankDepositEngine(bankDepositEngine),
		settlagrpc.WithPaymentLinkService(paymentLinkSvc),
		settlagrpc.WithPaymentLinkBaseURL(cfg.PaymentLinkBaseURL),
		settlagrpc.WithAuditLogger(auditAdapter),
		settlagrpc.WithAPIKeyHMACSecret([]byte(cfg.APIKeyHMACSecret)),
		settlagrpc.WithOpsAPIKey(cfg.OpsAPIKey),
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

	startAnalyticsWorkers(ctx, cfg, analyticsStore, extAnalyticsStore, snapshotStore, exportStore, logger)

	healthSvc := health.NewServer()
	healthpb.RegisterHealthServer(grpcServer, healthSvc)
	healthSvc.SetServingStatus("settla.v1.SettlementService", healthpb.HealthCheckResponse_SERVING)
	healthSvc.SetServingStatus("settla.v1.TreasuryService", healthpb.HealthCheckResponse_SERVING)
	healthSvc.SetServingStatus("settla.v1.LedgerService", healthpb.HealthCheckResponse_SERVING)
	healthSvc.SetServingStatus("settla.v1.TenantPortalService", healthpb.HealthCheckResponse_SERVING)
	healthSvc.SetServingStatus("settla.v1.DepositService", healthpb.HealthCheckResponse_SERVING)
	healthSvc.SetServingStatus("settla.v1.BankDepositService", healthpb.HealthCheckResponse_SERVING)
	healthSvc.SetServingStatus("settla.v1.PaymentLinkService", healthpb.HealthCheckResponse_SERVING)

	if cfg.Env.AllowsReflection() {
		reflection.Register(grpcServer)
	}

	grpcLis, err := net.Listen("tcp", net.JoinHostPort("0.0.0.0", cfg.GRPCPort))
	if err != nil {
		logger.Error("failed to listen for gRPC", "port", cfg.GRPCPort, "error", err)
		os.Exit(1)
	}
	go func() {
		logger.Info("grpc server listening", "port", cfg.GRPCPort)
		if err := grpcServer.Serve(grpcLis); err != nil {
			logger.Error("grpc server failed", "error", err)
			os.Exit(1)
		}
	}()

	checker.MarkStartupComplete()
	logger.Info("settla-server ready",
		"http_port", cfg.HTTPPort,
		"grpc_port", cfg.GRPCPort,
		"treasury_positions", treasurySvc.PositionCount(),
	)

	<-ctx.Done()

	logger.Info("settla-server shutting down...")
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


type dbPools struct {
	transfer, ledger, treasury             *pgxpool.Pool
	transferRead, ledgerRead, treasuryRead *pgxpool.Pool // nil when read replica URLs are not configured
}

func connectDatabases(ctx context.Context, cfg *appconfig.ServerConfig, logger *slog.Logger, metrics *observability.Metrics) dbPools {
	var pools dbPools
	var err error

	pools.transfer, err = appconfig.NewPgxPool(ctx, cfg.TransferDBURL, cfg.DBMaxConns, cfg.DBMinConns)
	if err != nil {
		logger.Error("settla-server: failed to connect to transfer DB", "error", err)
		os.Exit(1)
	}
	observability.RegisterPoolMetrics(ctx, pools.transfer, "transfer", metrics)
	logger.Info("settla-server: connected to transfer DB")

	pools.ledger, err = appconfig.NewPgxPool(ctx, cfg.LedgerDBURL, cfg.DBMaxConns, cfg.DBMinConns)
	if err != nil {
		logger.Error("settla-server: failed to connect to ledger DB", "error", err)
		os.Exit(1)
	}
	observability.RegisterPoolMetrics(ctx, pools.ledger, "ledger", metrics)
	logger.Info("settla-server: connected to ledger DB")

	pools.treasury, err = appconfig.NewPgxPool(ctx, cfg.TreasuryDBURL, cfg.DBMaxConns, cfg.DBMinConns)
	if err != nil {
		logger.Error("settla-server: failed to connect to treasury DB", "error", err)
		os.Exit(1)
	}
	observability.RegisterPoolMetrics(ctx, pools.treasury, "treasury", metrics)
	logger.Info("settla-server: connected to treasury DB")

	// Connect read replicas when configured.
	if cfg.TransferDBReadURL != "" {
		pools.transferRead, err = appconfig.NewPgxPool(ctx, cfg.TransferDBReadURL, cfg.DBMaxConns, cfg.DBMinConns)
		if err != nil {
			logger.Warn("settla-server: failed to connect to transfer read replica, falling back to primary", "error", err)
		} else {
			observability.RegisterPoolMetrics(ctx, pools.transferRead, "transfer_read", metrics)
			logger.Info("settla-server: connected to transfer read replica")
		}
	}
	if cfg.LedgerDBReadURL != "" {
		pools.ledgerRead, err = appconfig.NewPgxPool(ctx, cfg.LedgerDBReadURL, cfg.DBMaxConns, cfg.DBMinConns)
		if err != nil {
			logger.Warn("settla-server: failed to connect to ledger read replica, falling back to primary", "error", err)
		} else {
			observability.RegisterPoolMetrics(ctx, pools.ledgerRead, "ledger_read", metrics)
			logger.Info("settla-server: connected to ledger read replica")
		}
	}
	if cfg.TreasuryDBReadURL != "" {
		pools.treasuryRead, err = appconfig.NewPgxPool(ctx, cfg.TreasuryDBReadURL, cfg.DBMaxConns, cfg.DBMinConns)
		if err != nil {
			logger.Warn("settla-server: failed to connect to treasury read replica, falling back to primary", "error", err)
		} else {
			observability.RegisterPoolMetrics(ctx, pools.treasuryRead, "treasury_read", metrics)
			logger.Info("settla-server: connected to treasury read replica")
		}
	}

	return pools
}

func runMigrations(cfg *appconfig.ServerConfig, logger *slog.Logger) {
	sub, err := fs.Sub(settladb.TransferMigrations, "migrations/transfer")
	if err != nil {
		logger.Error("settla-server: failed to load transfer migrations", "error", err)
		os.Exit(1)
	}
	if err := automigrate.Run(automigrate.Transfer, cfg.TransferDBMigrateURL, sub, logger); err != nil {
		logger.Error("settla-server: transfer DB migration failed", "error", err)
		os.Exit(1)
	}

	sub, err = fs.Sub(settladb.LedgerMigrations, "migrations/ledger")
	if err != nil {
		logger.Error("settla-server: failed to load ledger migrations", "error", err)
		os.Exit(1)
	}
	if err := automigrate.Run(automigrate.Ledger, cfg.LedgerDBMigrateURL, sub, logger); err != nil {
		logger.Error("settla-server: ledger DB migration failed", "error", err)
		os.Exit(1)
	}

	sub, err = fs.Sub(settladb.TreasuryMigrations, "migrations/treasury")
	if err != nil {
		logger.Error("settla-server: failed to load treasury migrations", "error", err)
		os.Exit(1)
	}
	if err := automigrate.Run(automigrate.Treasury, cfg.TreasuryDBMigrateURL, sub, logger); err != nil {
		logger.Error("settla-server: treasury DB migration failed", "error", err)
		os.Exit(1)
	}
}

func connectNATS(ctx context.Context, cfg *appconfig.ServerConfig, logger *slog.Logger) (*messaging.CircuitBreakerPublisher, *messaging.Client) {
	var opts []messaging.ClientOption
	opts = append(opts, messaging.WithReplicas(cfg.NATSInitialReplicas))
	if cfg.NATSToken != "" {
		opts = append(opts, messaging.WithNATSToken(cfg.NATSToken))
	} else if cfg.NATSUser != "" {
		opts = append(opts, messaging.WithNATSUserInfo(cfg.NATSUser, cfg.NATSPassword))
	}

	natsClient, err := messaging.NewClient(cfg.NATSURL, cfg.NodePartitions, logger, opts...)
	if err != nil {
		logger.Error("settla-server: failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	if err := natsClient.EnsureStream(ctx); err != nil {
		logger.Error("settla-server: failed to ensure NATS stream", "error", err)
		os.Exit(1)
	}
	rawPublisher := messaging.NewPublisher(natsClient)
	natsCB := resilience.NewCircuitBreaker("nats-publisher",
		resilience.WithFailureThreshold(5),
		resilience.WithResetTimeout(10*time.Second),
	)
	publisher := messaging.NewCircuitBreakerPublisher(rawPublisher, natsCB)
	logger.Info("settla-server: connected to NATS JetStream")
	return publisher, natsClient
}

func connectTransferAppDB(ctx context.Context, cfg *appconfig.ServerConfig, logger *slog.Logger) *pgxpool.Pool {
	pool, err := appconfig.NewPgxPool(ctx, cfg.TransferAppDBURL, cfg.DBMaxConns, cfg.DBMinConns)
	if err != nil {
		logger.Error("settla-server: failed to connect to transfer app DB (RLS)", "error", err)
		if cfg.Env.RequiresRLS() {
			os.Exit(1)
		}
		return nil
	}
	logger.Info("settla-server: connected to transfer app DB (RLS enforced)")
	return pool
}

func connectRedis(ctx context.Context, cfg *appconfig.ServerConfig, transferQueries *transferdb.Queries, logger *slog.Logger) (*cache.TenantIndex, *cache.RedisCache) {
	if cfg.RedisURL == "" {
		return nil, nil
	}
	redisOpts, redisErr := cache.ParseRedisURL(cfg.RedisURL)
	if redisErr != nil {
		logger.Warn("settla-server: invalid SETTLA_REDIS_URL, tenant index disabled", "error", redisErr)
		return nil, nil
	}
	if redisOpts == nil {
		return nil, nil
	}
	redisClient := cache.NewRedisClientFromOpts(redisOpts)
	if pingErr := redisClient.Ping(ctx).Err(); pingErr != nil {
		logger.Warn("settla-server: Redis unavailable", "error", pingErr)
		redisClient.Close()
		return nil, nil
	}

	redisCache := cache.NewRedisCacheFromClient(redisClient)
	paginatedFetcher := func(ctx context.Context, limit, offset int32) ([]uuid.UUID, error) {
		return transferQueries.ListActiveTenantIDsPaginated(ctx, transferdb.ListActiveTenantIDsPaginatedParams{
			Limit: limit, Offset: offset,
		})
	}
	tenantIndex := cache.NewTenantIndex(redisClient, paginatedFetcher, logger)
	if rebuildErr := tenantIndex.Rebuild(ctx); rebuildErr != nil {
		logger.Warn("settla-server: tenant index initial rebuild failed", "error", rebuildErr)
	} else {
		count, _ := tenantIndex.Count(ctx)
		logger.Info("settla-server: tenant index initialized", "tenants", count)
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
					logger.Warn("settla-server: tenant index reconciliation failed", "error", err)
				}
			}
		}
	}()
	return tenantIndex, redisCache
}

func connectTigerBeetle(cfg *appconfig.ServerConfig, logger *slog.Logger) ledger.TBClient {
	addresses := ledger.ParseTBAddresses(cfg.TigerBeetleAddresses)
	tbClient, err := ledger.NewRealTBClient(0, addresses)
	if err != nil {
		logger.Error("settla-server: failed to connect to TigerBeetle", "addresses", cfg.TigerBeetleAddresses, "error", err)
		os.Exit(1)
	}
	logger.Info("settla-server: connected to TigerBeetle", "addresses", addresses)
	return tbClient
}


func initLedger(cfg *appconfig.ServerConfig, tbClient ledger.TBClient, ledgerDB ledgerdb.DBTX, publisher *messaging.CircuitBreakerPublisher, logger *slog.Logger, metrics *observability.Metrics) *ledger.Service {
	pgBackend := ledger.NewPGBackend(ledgerdb.New(ledgerDB), logger)
	svc := ledger.NewService(tbClient, pgBackend, publisher, logger, metrics,
		ledger.WithBatchWindow(time.Duration(cfg.LedgerBatchWindowMs)*time.Millisecond),
		ledger.WithBatchMaxSize(cfg.LedgerBatchMaxSize),
	)
	svc.Start()
	return svc
}

func initTreasury(ctx context.Context, cfg *appconfig.ServerConfig, store treasury.Store, publisher *messaging.CircuitBreakerPublisher, logger *slog.Logger, metrics *observability.Metrics) *treasury.Manager {
	opts := []treasury.Option{
		treasury.WithFlushInterval(time.Duration(cfg.TreasuryFlushIntervalMs) * time.Millisecond),
	}
	if syncThresholds := parseSyncThresholds(cfg.TreasurySyncThresholds); len(syncThresholds) > 0 {
		opts = append(opts, treasury.WithSyncThresholds(syncThresholds))
	}
	if cfg.TreasurySyncThresholdDefault != "" {
		if amount, err := decimal.NewFromString(cfg.TreasurySyncThresholdDefault); err == nil {
			opts = append(opts, treasury.WithSyncThresholdDefault(amount))
		} else {
			logger.Warn("settla-server: invalid SETTLA_TREASURY_SYNC_THRESHOLD_DEFAULT", "value", cfg.TreasurySyncThresholdDefault, "error", err)
		}
	}
	mgr := treasury.NewManager(store, publisher, logger, metrics, opts...)
	if err := mgr.LoadPositions(ctx); err != nil {
		logger.Error("settla-server: failed to load treasury positions", "error", err)
		os.Exit(1)
	}
	mgr.Start()
	logger.Info("settla-server: treasury loaded", "positions", mgr.PositionCount())
	return mgr
}

func initEngine(cfg *appconfig.ServerConfig, transferStore *transferdb.TransferStoreAdapter, tenantStore *transferdb.TenantStoreAdapter, coreRouter *router.CoreRouterAdapter, providerReg *provider.Registry, redisCache *cache.RedisCache, logger *slog.Logger, metrics *observability.Metrics) *core.Engine {
	var opts []core.EngineOption
	if redisCache != nil {
		opts = append(opts, core.WithDailyVolumeCounter(cache.NewRedisDailyVolumeCounter(redisCache)))
		logger.Info("settla-server: daily volume limit enforcement via Redis")
	}
	if cfg.Env.RequiresAuth() {
		opts = append(opts, core.WithRequireDailyVolumeCounter())
		logger.Info("settla-server: daily volume counter enforcement enabled", "env", cfg.Env)
	}
	return core.NewEngine(transferStore, tenantStore, coreRouter, providerReg, logger, metrics, opts...)
}


func startPartitionManager(ctx context.Context, cfg *appconfig.ServerConfig, transferPool *pgxpool.Pool, logger *slog.Logger) {
	partitionMgr := maintenance.NewPartitionManager(newPgxPoolDBExecutor(transferPool), logger)
	go func() {
		if err := partitionMgr.ManagePartitions(ctx); err != nil {
			logger.Warn("settla-server: partition management startup run failed", "error", err)
		}
		ticker := time.NewTicker(time.Duration(cfg.PartitionScheduleHours) * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := partitionMgr.ManagePartitions(ctx); err != nil {
					logger.Warn("settla-server: partition management run failed", "error", err)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	logger.Info("settla-server: partition manager scheduled", "interval_hours", cfg.PartitionScheduleHours)
}

func startVacuumManager(ctx context.Context, cfg *appconfig.ServerConfig, transferPool *pgxpool.Pool, logger *slog.Logger) {
	vacuumMgr := maintenance.NewVacuumManager(newPgxPoolDBExecutor(transferPool), logger)
	go func() {
		ticker := time.NewTicker(time.Duration(cfg.VacuumCheckIntervalMinutes) * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := vacuumMgr.RunDueVacuums(ctx); err != nil {
					logger.Warn("settla-server: vacuum run failed", "error", err)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	logger.Info("settla-server: vacuum manager scheduled", "check_interval_minutes", cfg.VacuumCheckIntervalMinutes)
}

func startCapacityMonitor(ctx context.Context, cfg *appconfig.ServerConfig, transferPool *pgxpool.Pool, logger *slog.Logger) {
	capacityMon := maintenance.NewCapacityMonitor(
		newPgxPoolDBExecutor(transferPool), logger,
		[]string{"settla_transfer", "settla_ledger", "settla_treasury"},
		cfg.CapacityMaxBytes, maintenance.NewCapacityMetrics(),
	)
	go func() {
		if _, err := capacityMon.CheckCapacity(ctx); err != nil {
			logger.Warn("settla-server: capacity check startup run failed", "error", err)
		}
		ticker := time.NewTicker(time.Duration(cfg.CapacityCheckIntervalMinutes) * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if _, err := capacityMon.CheckCapacity(ctx); err != nil {
					logger.Warn("settla-server: capacity check failed", "error", err)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	logger.Info("settla-server: capacity monitor scheduled", "check_interval_minutes", cfg.CapacityCheckIntervalMinutes)
}

func startReconciler(ctx context.Context, cfg *appconfig.ServerConfig, transferQueries *transferdb.Queries, ledgerDB ledgerdb.DBTX, treasurySvc *treasury.Manager, tenantIndex *cache.TenantIndex, logger *slog.Logger) *reconciliation.Reconciler {
	reconAdapter := transferdb.NewReconciliationAdapter(transferQueries)
	if tenantIndex != nil {
		reconAdapter.WithTenantForEach(tenantIndex.ForEach)
	}
	checks := []reconciliation.Check{
		reconciliation.NewTransferStateCheck(reconAdapter, logger, nil),
		reconciliation.NewOutboxCheck(reconAdapter, logger, 0),
		reconciliation.NewProviderTxCheck(reconAdapter, logger, 0),
		reconciliation.NewDailyVolumeCheck(reconAdapter, logger),
		reconciliation.NewSettlementFeeCheck(reconAdapter, logger, decimal.Zero),
		reconciliation.NewDepositCheck(reconAdapter, logger, 0, 0, 0),
		reconciliation.NewBankDepositCheck(reconAdapter, logger, 0, 0),
	}
	ledgerReconAdapter := ledgerdb.NewLedgerReconciliationAdapter(ledgerdb.New(ledgerDB))
	checks = append(checks, reconciliation.NewTreasuryLedgerCheck(
		treasurySvc, ledgerReconAdapter, reconAdapter, reconAdapter, logger,
		decimal.NewFromFloat(0.01),
	))
	reconciler := reconciliation.NewReconciler(checks, reconAdapter, logger).
		WithMetrics(reconciliation.NewReconcilerMetrics(prometheus.DefaultRegisterer))

	flagManager := featureflag.NewManager(cfg.FeatureFlagsPath, logger)
	go flagManager.Start(ctx)
	reconciler.WithFeatureFlags(flagManager)
	logger.Info("settla-server: feature flags loaded", "config_path", cfg.FeatureFlagsPath)

	go func() {
		ticker := time.NewTicker(time.Duration(cfg.ReconciliationIntervalMinutes) * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if _, err := reconciler.Run(ctx); err != nil {
					logger.Warn("settla-server: reconciliation run failed", "error", err)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	logger.Info("settla-server: reconciler scheduled", "interval_minutes", cfg.ReconciliationIntervalMinutes, "checks", len(checks))
	return reconciler
}

func startSettlementScheduler(ctx context.Context, cfg *appconfig.ServerConfig, transferQueries *transferdb.Queries, logger *slog.Logger) {
	if !cfg.SettlementEnabled {
		logger.Info("settla-server: settlement scheduler disabled")
		return
	}
	settlementStore := transferdb.NewSettlementAdapter(transferQueries)
	calculator := settlement.NewCalculator(settlementStore, settlementStore, settlementStore, logger)
	scheduler := settlement.NewScheduler(calculator, settlementStore, logger)
	go func() {
		if err := scheduler.Start(ctx); err != nil && err != context.Canceled {
			logger.Error("settla-server: settlement scheduler stopped with error", "error", err)
		}
	}()
	logger.Info("settla-server: settlement scheduler started")
}

func startSyntheticCanary(ctx context.Context, cfg *appconfig.ServerConfig, logger *slog.Logger) {
	if !cfg.SyntheticCanaryEnabled {
		return
	}
	canaryInterval := time.Duration(cfg.SyntheticIntervalSec) * time.Second
	canary := synthetic.NewCanary(synthetic.Config{
		Enabled:    true,
		GatewayURL: cfg.SyntheticGatewayURL,
		APIKey:     cfg.SyntheticAPIKey,
		Interval:   canaryInterval,
	}, logger)
	canary.Start()
	logger.Info("settla-server: synthetic canary started", "interval", canaryInterval, "gateway_url", cfg.SyntheticGatewayURL)
}

func bootstrapProviders(cfg *appconfig.ServerConfig, logger *slog.Logger) (*provider.Registry, *blockchain.Registry, *wallet.Manager) {
	providerMode := provider.ProviderMode(cfg.ProviderMode)
	var chainReg *blockchain.Registry
	var walletMgr *wallet.Manager

	if providerMode == provider.ProviderModeTestnet || providerMode == provider.ProviderModeLive {
		if cfg.WalletEncryptionKey != "" && cfg.MasterSeedHex != "" {
			masterSeed, err := hexDecodeSecure(cfg.MasterSeedHex)
			if err != nil {
				logger.Error("settla-server: invalid SETTLA_MASTER_SEED hex", "error", err)
				os.Exit(1)
			}
			walletMgr, err = wallet.NewManager(wallet.ManagerConfig{
				MasterSeed:    masterSeed,
				EncryptionKey: cfg.WalletEncryptionKey,
				StoragePath:   cfg.WalletStoragePath,
				Logger:        logger,
			})
			if err != nil {
				logger.Error("settla-server: failed to create wallet manager", "error", err)
				os.Exit(1)
			}
			logger.Info("settla-server: wallet manager initialized", "storage_path", cfg.WalletStoragePath)
		} else {
			logger.Warn("settla-server: wallet keys not set — blockchain clients will be read-only")
		}

		chainCfg := blockchain.LoadConfigFromEnv()
		var err error
		chainReg, err = blockchain.NewRegistryFromConfig(chainCfg, walletMgr, logger)
		if err != nil {
			logger.Error("settla-server: failed to create blockchain registry", "error", err)
			os.Exit(1)
		}
		if walletMgr != nil {
			if err := chainReg.RegisterSystemWallets(walletMgr); err != nil {
				logger.Warn("settla-server: some system wallets failed to register", "error", err)
			}
		}
	}

	bootstrapResult, err := factory.Bootstrap(factory.ProviderMode(providerMode), factory.Deps{
		Logger:        logger,
		BlockchainReg: chainReg,
	})
	if err != nil {
		logger.Error("settla-server: provider bootstrap failed", "error", err)
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
	logger.Info("settla-server: provider mode", "mode", cfg.ProviderMode)
	return providerReg, chainReg, walletMgr
}

func buildHealthChecker(transferPool, ledgerPool, treasuryPool *pgxpool.Pool, natsClient *messaging.Client, tbClient ledger.TBClient, logger *slog.Logger) *healthcheck.Checker {
	checks := []healthcheck.Check{
		healthcheck.NewCallbackCheck("postgres_transfer", false,
			func(ctx context.Context) error { return transferPool.Ping(ctx) }),
		healthcheck.NewCallbackCheck("postgres_ledger", false,
			func(ctx context.Context) error { return ledgerPool.Ping(ctx) }),
		healthcheck.NewCallbackCheck("postgres_treasury", false,
			func(ctx context.Context) error { return treasuryPool.Ping(ctx) }),
		healthcheck.NewNATSCheck(func(_ context.Context) error {
			if !natsClient.Conn.IsConnected() {
				return fmt.Errorf("NATS connection not active")
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
	return healthcheck.NewChecker(logger, checks, healthcheck.WithVersion(version))
}

func startAnalyticsWorkers(ctx context.Context, cfg *appconfig.ServerConfig, analyticsStore *transferdb.AnalyticsAdapter, extAnalyticsStore *transferdb.ExtendedAnalyticsAdapter, snapshotStore *transferdb.SnapshotAdapter, exportStore *transferdb.ExportAdapter, logger *slog.Logger) {
	if cfg.AnalyticsSnapshotEnabled {
		snapshotScheduler := analytics.NewSnapshotScheduler(
			&compositeAnalyticsQuerier{analytics: analyticsStore, ext: extAnalyticsStore},
			snapshotStore, logger,
		)
		go func() {
			if err := snapshotScheduler.Start(ctx); err != nil && err != context.Canceled {
				logger.Error("settla-server: analytics snapshot scheduler stopped with error", "error", err)
			}
		}()
		logger.Info("settla-server: analytics snapshot scheduler started")
	}

	analyticsExporter := analytics.NewExporter(
		&compositeExportSource{analytics: analyticsStore, ext: extAnalyticsStore},
		exportStore, cfg.ExportStoragePath, logger,
	)
	go func() {
		if err := analyticsExporter.Start(ctx); err != nil && err != context.Canceled {
			logger.Error("settla-server: analytics exporter stopped with error", "error", err)
		}
	}()
	logger.Info("settla-server: analytics exporter started", "storage_path", cfg.ExportStoragePath)
}
