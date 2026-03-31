package appconfig

import (
	"os"
	"strconv"
)

// DefaultCapacityMaxBytes is 10 TiB — appropriate for a 50M tx/day workload.
// Override via SETTLA_CAPACITY_MAX_BYTES for deployments with different storage budgets.
const DefaultCapacityMaxBytes int64 = 10 * 1024 * 1024 * 1024 * 1024

// BaseConfig holds infrastructure configuration shared by settla-server and settla-node.
// All database URLs are required — there are no fallback defaults. If a required
// field is missing, LoadServerConfig/LoadNodeConfig returns an error listing every
// missing field so developers can fix all issues in one pass.
type BaseConfig struct {
	Env Env

	// Databases — all required, no fallback URLs.
	TransferDBURL string
	LedgerDBURL   string
	TreasuryDBURL string

	// Read replica URLs — optional. When set, read-only queries (List*, analytics,
	// reconciliation) are routed to these pools to offload the primary.
	TransferDBReadURL string
	LedgerDBReadURL   string
	TreasuryDBReadURL string

	// Migration URLs — default to their respective DB URLs if unset.
	// Migrations connect to raw Postgres (not PgBouncer) because goose uses
	// pg_advisory_lock which requires session-level state.
	TransferDBMigrateURL string
	LedgerDBMigrateURL   string
	TreasuryDBMigrateURL string

	// DB connection pool tuning.
	DBMaxConns int // default: 50
	DBMinConns int // default: 10

	// TigerBeetle — required in all environments. The double-entry ledger engine
	// is integral to Settla; there is no stub mode.
	TigerBeetleAddresses string

	// NATS JetStream — required, no localhost fallback.
	NATSURL      string
	NATSToken    string
	NATSUser     string
	NATSPassword string
	// NATSInitialReplicas is used only when creating JetStream streams for the
	// first time. Once streams exist, NATS manages replicas server-side. Do not
	// treat this as a runtime assertion — Kubernetes may scale NATS independently.
	NATSInitialReplicas int // default: 1

	// Redis — required in prod/staging for tenant index + daily volume counters.
	// Optional in development.
	RedisURL string

	// Feature flags — JSON config file path with background hot-reload.
	FeatureFlagsPath string // default: "deploy/config/features.json"

	// Ledger tuning.
	LedgerBatchWindowMs int // default: 10
	LedgerBatchMaxSize  int // default: 500

	// Treasury tuning.
	TreasuryFlushIntervalMs      int    // default: 100
	TreasurySyncThresholds       string // raw CSV: "NGN:200000000,GHS:2000000"
	TreasurySyncThresholdDefault string

	// Graceful drain timeout.
	DrainTimeoutMs int // default: 45000

	// Provider mode: mock, mock-http, testnet, live.
	ProviderMode        string
	WalletEncryptionKey string
	MasterSeedHex       string
	WalletStoragePath   string // default: ".settla/wallets"

	// Logging.
	LogLevel string // default: "info"

	// NATS partition count — shared by both binaries for stream creation/subscription.
	NodePartitions int // default: 8

	// Pprof — shared by both binaries.
	PprofPort    string // default: "6060"
	PprofEnabled bool   // default: true
}

// ServerConfig holds settla-server-specific configuration.
type ServerConfig struct {
	BaseConfig

	HTTPPort string // default: "8080"
	GRPCPort string // default: "9090"

	JWTSecret        string // required
	APIKeyHMACSecret string
	OpsAPIKey        string

	// RLS-enforced app pool — required in prod/staging for row-level security.
	TransferAppDBURL string

	// Scheduler intervals.
	PartitionScheduleHours        int // default: 24
	VacuumCheckIntervalMinutes    int // default: 5
	CapacityCheckIntervalMinutes  int // default: 15
	ReconciliationIntervalMinutes int // default: 5

	// Capacity monitor — max database size before alerting.
	CapacityMaxBytes int64 // default: DefaultCapacityMaxBytes (10 TiB)

	// Settlement.
	SettlementEnabled bool // default: true

	// Synthetic canary.
	SyntheticCanaryEnabled bool
	SyntheticIntervalSec   int    // default: 30
	SyntheticGatewayURL    string // default: "http://gateway:3000"
	SyntheticAPIKey        string

	// Analytics.
	AnalyticsSnapshotEnabled bool   // default: true
	ExportStoragePath        string // default: "/tmp/settla-exports"

	// Payment links.
	PaymentLinkBaseURL string // default: "http://localhost:3003/p"
}

// NodeConfig holds settla-node-specific configuration.
type NodeConfig struct {
	BaseConfig

	MetricsPort string // default: "9091"
	PartitionID int    // default: -1 (all partitions in dev mode)

	Workers WorkerPoolSizes

	// Outbox relay.
	RelayBatchSize      int // default: 500
	RelayPollIntervalMs int // default: 20

	// Recovery detector.
	RecoveryMaxResults int // default: 5000

	// Email.
	ResendAPIKey string
	EmailFrom    string // default: "notifications@settla.io"

	// Chain monitor RPC.
	EthRPCURL          string
	EthRPCAPIKey       string
	EthRPCBackupURL    string
	EthRPCBackupAPIKey string
	BaseRPCURL         string
	BaseRPCAPIKey      string
	BaseRPCBackupURL   string
	BaseRPCBackupAPIKey string
	TronRPCURL         string
	TronRPCAPIKey      string

	// Address pool.
	PoolRefillIntervalSec int // default: 60

	// Signing service.
	SigningServiceURL string
}

// WorkerPoolSizes defines the maximum concurrent handlers for each worker type.
type WorkerPoolSizes struct {
	Transfer    int // default: 8
	Provider    int // default: 16
	Blockchain  int // default: 16
	Ledger      int // default: 8
	Treasury    int // default: 8
	Webhook     int // default: 32
	InboundWH   int // default: 8
	Deposit     int // default: 8
	Email       int // default: 8
	BankDeposit int // default: 8
}

// LoadServerConfig reads all environment variables for settla-server, validates
// them, and returns a fully populated ServerConfig. Returns an error listing
// every missing or invalid field so developers can fix all issues in one pass.
func LoadServerConfig() (*ServerConfig, error) {
	base, err := loadBaseConfig()
	if err != nil {
		return nil, err
	}
	cfg := &ServerConfig{
		BaseConfig:                    *base,
		HTTPPort:                      getEnvOr("SETTLA_SERVER_HTTP_PORT", "8080"),
		GRPCPort:                      getEnvOr("SETTLA_SERVER_GRPC_PORT", "9090"),
		JWTSecret:                     os.Getenv("SETTLA_JWT_SECRET"),
		APIKeyHMACSecret:              os.Getenv("SETTLA_API_KEY_HMAC_SECRET"),
		OpsAPIKey:                     os.Getenv("SETTLA_OPS_API_KEY"),
		TransferAppDBURL:              os.Getenv("SETTLA_TRANSFER_APP_DB_URL"),
		PartitionScheduleHours:        getEnvIntOr("SETTLA_PARTITION_SCHEDULE_HOURS", 24),
		VacuumCheckIntervalMinutes:    getEnvIntOr("SETTLA_VACUUM_CHECK_INTERVAL_MINUTES", 5),
		CapacityCheckIntervalMinutes:  getEnvIntOr("SETTLA_CAPACITY_CHECK_INTERVAL_MINUTES", 15),
		ReconciliationIntervalMinutes: getEnvIntOr("SETTLA_RECONCILIATION_INTERVAL_MINUTES", 5),
		CapacityMaxBytes:              getEnvInt64Or("SETTLA_CAPACITY_MAX_BYTES", DefaultCapacityMaxBytes),
		SettlementEnabled:             getEnvOr("SETTLA_SETTLEMENT_ENABLED", "true") == "true",
		SyntheticCanaryEnabled:        getEnvOr("SETTLA_SYNTHETIC_CANARY_ENABLED", "false") == "true",
		SyntheticIntervalSec:          getEnvIntOr("SETTLA_SYNTHETIC_INTERVAL_S", 30),
		SyntheticGatewayURL:           getEnvOr("SETTLA_SYNTHETIC_GATEWAY_URL", "http://gateway:3000"),
		SyntheticAPIKey:               os.Getenv("SETTLA_SYNTHETIC_API_KEY"),
		AnalyticsSnapshotEnabled:      getEnvOr("SETTLA_ANALYTICS_SNAPSHOT_ENABLED", "true") == "true",
		ExportStoragePath:             getEnvOr("SETTLA_EXPORT_STORAGE_PATH", "/tmp/settla-exports"),
		PaymentLinkBaseURL:            getEnvOr("SETTLA_PAYMENT_LINK_BASE_URL", "http://localhost:3003/p"),
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// LoadNodeConfig reads all environment variables for settla-node, validates
// them, and returns a fully populated NodeConfig.
func LoadNodeConfig() (*NodeConfig, error) {
	base, err := loadBaseConfig()
	if err != nil {
		return nil, err
	}
	cfg := &NodeConfig{
		BaseConfig:          *base,
		MetricsPort:         getEnvOr("SETTLA_NODE_METRICS_PORT", "9091"),
		PartitionID:         getEnvIntOr("SETTLA_NODE_PARTITION_ID", -1),
		RelayBatchSize:      getEnvIntOr("SETTLA_RELAY_BATCH_SIZE", 500),
		RelayPollIntervalMs: getEnvIntOr("SETTLA_RELAY_POLL_INTERVAL_MS", 20),
		RecoveryMaxResults:  getEnvIntOr("SETTLA_RECOVERY_MAX_RESULTS", 5000),
		ResendAPIKey:        os.Getenv("SETTLA_RESEND_API_KEY"),
		EmailFrom:           getEnvOr("SETTLA_EMAIL_FROM", "notifications@settla.io"),
		EthRPCURL:           os.Getenv("SETTLA_ETH_RPC_URL"),
		EthRPCAPIKey:        os.Getenv("SETTLA_ETH_RPC_API_KEY"),
		EthRPCBackupURL:     os.Getenv("SETTLA_ETH_RPC_BACKUP_URL"),
		EthRPCBackupAPIKey:  os.Getenv("SETTLA_ETH_RPC_BACKUP_API_KEY"),
		BaseRPCURL:          os.Getenv("SETTLA_BASE_RPC_URL"),
		BaseRPCAPIKey:       os.Getenv("SETTLA_BASE_RPC_API_KEY"),
		BaseRPCBackupURL:    os.Getenv("SETTLA_BASE_RPC_BACKUP_URL"),
		BaseRPCBackupAPIKey: os.Getenv("SETTLA_BASE_RPC_BACKUP_API_KEY"),
		TronRPCURL:          os.Getenv("SETTLA_TRON_RPC_URL"),
		TronRPCAPIKey:       os.Getenv("SETTLA_TRON_API_KEY"),
		PoolRefillIntervalSec: getEnvIntOr("SETTLA_POOL_REFILL_INTERVAL_SEC", 60),
		SigningServiceURL:   os.Getenv("SETTLA_SIGNING_SERVICE_URL"),
		Workers: WorkerPoolSizes{
			Transfer:    getEnvIntOr("SETTLA_WORKER_POOL_TRANSFER", 8),
			Provider:    getEnvIntOr("SETTLA_WORKER_POOL_PROVIDER", 16),
			Blockchain:  getEnvIntOr("SETTLA_WORKER_POOL_BLOCKCHAIN", 16),
			Ledger:      getEnvIntOr("SETTLA_WORKER_POOL_LEDGER", 8),
			Treasury:    getEnvIntOr("SETTLA_WORKER_POOL_TREASURY", 8),
			Webhook:     getEnvIntOr("SETTLA_WORKER_POOL_WEBHOOK", 32),
			InboundWH:   getEnvIntOr("SETTLA_WORKER_POOL_INBOUND_WH", 8),
			Deposit:     getEnvIntOr("SETTLA_WORKER_POOL_DEPOSIT", 8),
			Email:       getEnvIntOr("SETTLA_WORKER_POOL_EMAIL", 8),
			BankDeposit: getEnvIntOr("SETTLA_WORKER_POOL_BANK_DEPOSIT", 8),
		},
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func loadBaseConfig() (*BaseConfig, error) {
	envStr := getEnvOr("SETTLA_ENV", "development")
	env, err := ParseEnv(envStr)
	if err != nil {
		return nil, err
	}

	transferDBURL := os.Getenv("SETTLA_TRANSFER_DB_URL")
	ledgerDBURL := os.Getenv("SETTLA_LEDGER_DB_URL")
	treasuryDBURL := os.Getenv("SETTLA_TREASURY_DB_URL")

	cfg := &BaseConfig{
		Env:                          env,
		TransferDBURL:                transferDBURL,
		LedgerDBURL:                  ledgerDBURL,
		TreasuryDBURL:                treasuryDBURL,
		TransferDBReadURL:            os.Getenv("SETTLA_TRANSFER_DB_READ_URL"),
		LedgerDBReadURL:              os.Getenv("SETTLA_LEDGER_DB_READ_URL"),
		TreasuryDBReadURL:            os.Getenv("SETTLA_TREASURY_DB_READ_URL"),
		TransferDBMigrateURL:         getEnvOr("SETTLA_TRANSFER_DB_MIGRATE_URL", transferDBURL),
		LedgerDBMigrateURL:          getEnvOr("SETTLA_LEDGER_DB_MIGRATE_URL", ledgerDBURL),
		TreasuryDBMigrateURL:         getEnvOr("SETTLA_TREASURY_DB_MIGRATE_URL", treasuryDBURL),
		DBMaxConns:                   getEnvIntOr("SETTLA_DB_MAX_CONNS", 50),
		DBMinConns:                   getEnvIntOr("SETTLA_DB_MIN_CONNS", 10),
		TigerBeetleAddresses:         os.Getenv("SETTLA_TIGERBEETLE_ADDRESSES"),
		NATSURL:                      os.Getenv("SETTLA_NATS_URL"),
		NATSToken:                    os.Getenv("SETTLA_NATS_TOKEN"),
		NATSUser:                     os.Getenv("SETTLA_NATS_USER"),
		NATSPassword:                 os.Getenv("SETTLA_NATS_PASSWORD"),
		NATSInitialReplicas:          getEnvIntOr("SETTLA_NATS_REPLICAS", 1),
		RedisURL:                     os.Getenv("SETTLA_REDIS_URL"),
		FeatureFlagsPath:             getEnvOr("SETTLA_FEATURE_FLAGS_PATH", "deploy/config/features.json"),
		LedgerBatchWindowMs:          getEnvIntOr("SETTLA_LEDGER_BATCH_WINDOW_MS", 10),
		LedgerBatchMaxSize:           getEnvIntOr("SETTLA_LEDGER_BATCH_MAX_SIZE", 500),
		TreasuryFlushIntervalMs:      getEnvIntOr("SETTLA_TREASURY_FLUSH_INTERVAL_MS", 100),
		TreasurySyncThresholds:       os.Getenv("SETTLA_TREASURY_SYNC_THRESHOLDS"),
		TreasurySyncThresholdDefault: os.Getenv("SETTLA_TREASURY_SYNC_THRESHOLD_DEFAULT"),
		DrainTimeoutMs:               getEnvIntOr("SETTLA_DRAIN_TIMEOUT_MS", 45000),
		ProviderMode:                 getEnvOr("SETTLA_PROVIDER_MODE", "mock"),
		WalletEncryptionKey:          os.Getenv("SETTLA_WALLET_ENCRYPTION_KEY"),
		MasterSeedHex:                os.Getenv("SETTLA_MASTER_SEED"),
		WalletStoragePath:            getEnvOr("SETTLA_WALLET_STORAGE_PATH", ".settla/wallets"),
		LogLevel:                     getEnvOr("SETTLA_LOG_LEVEL", "info"),
		NodePartitions:               getEnvIntOr("SETTLA_NODE_PARTITIONS", 8),
		PprofPort:                    getEnvOr("SETTLA_PPROF_PORT", "6060"),
		PprofEnabled:                 getEnvOr("SETTLA_PPROF", "true") == "true",
	}
	return cfg, nil
}

// ── Internal helpers (not exported — env reads are confined to this file) ────

func getEnvOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvIntOr(key string, fallback int) int {
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

func getEnvInt64Or(key string, fallback int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}
