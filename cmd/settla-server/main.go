package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/intellect4all/settla/domain"
	pb "github.com/intellect4all/settla/gen/settla/v1"
	"github.com/intellect4all/settla/ledger"
	"github.com/intellect4all/settla/node/messaging"
	"github.com/intellect4all/settla/observability"
	"github.com/intellect4all/settla/rail/blockchain"
	"github.com/intellect4all/settla/rail/provider"
	"github.com/intellect4all/settla/rail/provider/mock"
	settlaprovider "github.com/intellect4all/settla/rail/provider/settla"
	"github.com/intellect4all/settla/rail/router"
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

	// NATS JetStream
	natsURL := envOrDefault("SETTLA_NATS_URL", "nats://localhost:4222")
	numPartitions := envIntOrDefault("SETTLA_NODE_PARTITIONS", messaging.DefaultPartitions)
	var publisher domain.EventPublisher
	natsClient, err := messaging.NewClient(natsURL, numPartitions, logger)
	if err != nil {
		logger.Warn("settla-server: NATS unavailable, events will be dropped", "error", err)
		publisher = &stubPublisher{}
	} else {
		defer natsClient.Close()
		if err := natsClient.EnsureStream(ctx); err != nil {
			logger.Error("settla-server: failed to ensure NATS stream", "error", err)
			os.Exit(1)
		}
		publisher = messaging.NewPublisher(natsClient)
		logger.Info("settla-server: connected to NATS JetStream")
	}

	// ── Stores ──────────────────────────────────────────────────────

	transferQueries := transferdb.New(transferPool)
	transferStore := transferdb.NewTransferStoreAdapter(transferQueries)
	tenantStore := transferdb.NewTenantStoreAdapter(transferQueries)

	// Treasury store: real Postgres or in-memory stub
	var treasuryStore treasury.Store
	if treasuryPool != nil {
		treasuryStore = newPostgresTreasuryStore(treasurydb.New(treasuryPool))
	} else {
		treasuryStore = &stubTreasuryStore{}
	}

	// ── Module initialization ───────────────────────────────────────
	// Each module depends on interfaces from domain/, not concrete types.
	// Any module can be extracted to a gRPC service by swapping the constructor.

	// Ledger: dual-backend (nil TBClient = stub mode for now)
	// TODO: wire real TigerBeetle client when available
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

	// Rail: provider registry — mode-switched via SETTLA_PROVIDER_MODE
	providerMode := provider.ProviderModeFromEnv()
	var providerReg *provider.Registry
	switch providerMode {
	case provider.ProviderModeTestnet:
		providerReg = initTestnetProviders(logger)
	default:
		providerReg = provider.NewRegistry()
		registerMockProviders(providerReg)
	}
	logger.Info("settla-server: provider mode", "mode", string(providerMode))
	coreAdapter := &coreRegistryAdapter{reg: providerReg}

	// Router: smart routing with tenant fee schedules
	railRouter := router.NewRouter(providerReg, tenantStore, logger)
	coreRouterAdapter := router.NewCoreRouterAdapter(railRouter, tenantStore, logger)

	// Core engine
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

	// ── HTTP health/readiness server ────────────────────────────────
	httpPort := envOrDefault("SETTLA_SERVER_HTTP_PORT", "8080")

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.Handle("/metrics", promhttp.Handler())

	httpServer := &http.Server{
		Addr:    net.JoinHostPort("0.0.0.0", httpPort),
		Handler: mux,
	}

	go func() {
		logger.Info("http server listening", "port", httpPort)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("http server failed", "error", err)
			os.Exit(1)
		}
	}()

	// ── gRPC server ────────────────────────────────────────────────
	grpcPort := envOrDefault("SETTLA_SERVER_GRPC_PORT", "9090")

	grpcServer := grpc.NewServer(
		grpc.UnaryInterceptor(observability.UnaryServerInterceptor(metrics)),
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
	grpcSvc := settlagrpc.NewServer(engine, treasurySvc, ledgerSvc, logger,
		settlagrpc.WithAuthStore(authStore),
	)
	pb.RegisterSettlementServiceServer(grpcServer, grpcSvc)
	pb.RegisterTreasuryServiceServer(grpcServer, grpcSvc)
	pb.RegisterLedgerServiceServer(grpcServer, grpcSvc)
	pb.RegisterAuthServiceServer(grpcServer, grpcSvc)

	// Health check service
	healthSvc := health.NewServer()
	healthpb.RegisterHealthServer(grpcServer, healthSvc)
	healthSvc.SetServingStatus("settla.v1.SettlementService", healthpb.HealthCheckResponse_SERVING)
	healthSvc.SetServingStatus("settla.v1.TreasuryService", healthpb.HealthCheckResponse_SERVING)
	healthSvc.SetServingStatus("settla.v1.LedgerService", healthpb.HealthCheckResponse_SERVING)

	reflection.Register(grpcServer)

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

	logger.Info("settla-server ready",
		"http_port", httpPort,
		"grpc_port", grpcPort,
		"treasury_positions", treasurySvc.PositionCount(),
	)

	<-ctx.Done()

	logger.Info("settla-server shutting down...")

	// Graceful shutdown order:
	// 1. Stop accepting new RPCs
	// 2. Treasury final flush (persists in-flight reservations)
	// 3. Stop ledger sync/batcher
	// 4. Close DB pools (handled by defers)
	grpcServer.GracefulStop()
	httpServer.Shutdown(context.Background())

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

// ── Adapters ────────────────────────────────────────────────────────────────

// coreRegistryAdapter wraps provider.Registry to satisfy core.ProviderRegistry.
// core.ProviderRegistry returns nil on not-found, provider.Registry returns error.
type coreRegistryAdapter struct {
	reg *provider.Registry
}

func (a *coreRegistryAdapter) GetOnRampProvider(id string) domain.OnRampProvider {
	p, err := a.reg.GetOnRamp(id)
	if err != nil {
		return nil
	}
	return p
}

func (a *coreRegistryAdapter) GetOffRampProvider(id string) domain.OffRampProvider {
	p, err := a.reg.GetOffRamp(id)
	if err != nil {
		return nil
	}
	return p
}

func (a *coreRegistryAdapter) GetBlockchainClient(chain string) domain.BlockchainClient {
	c, err := a.reg.GetBlockchainClient(chain)
	if err != nil {
		return nil
	}
	return c
}

// ── Postgres Treasury Store ─────────────────────────────────────────────────

type postgresTreasuryStore struct {
	q *treasurydb.Queries
}

func newPostgresTreasuryStore(q *treasurydb.Queries) *postgresTreasuryStore {
	return &postgresTreasuryStore{q: q}
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
