package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SeedRunner provisions tenants and treasury positions in bulk for scale tests.
// Uses batch INSERT (batches of 1000 rows) and COPY where available for speed.
// Target performance: 50 tenants < 5s, 20K tenants < 2 min, 100K tenants < 10 min.
type SeedRunner struct {
	transferDBURL string
	treasuryDBURL string
	tenantCount   int
	logger        *slog.Logger
	cleanupOnly   bool
}

// RunSeed is the entrypoint for the seed sub-command.
func RunSeed(args []string) error {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	transferDB := fs.String("transfer-db", "", "Transfer DB URL (required)")
	treasuryDB := fs.String("treasury-db", "", "Treasury DB URL (required)")
	count := fs.Int("count", 50, "Number of tenants to provision (default 50)")
	cleanup := fs.Bool("cleanup", false, "Remove previously seeded scale-test tenants only")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *transferDB == "" || *treasuryDB == "" {
		return fmt.Errorf("both -transfer-db and -treasury-db are required")
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	runner := &SeedRunner{
		transferDBURL: *transferDB,
		treasuryDBURL: *treasuryDB,
		tenantCount:   *count,
		logger:        logger,
		cleanupOnly:   *cleanup,
	}

	return runner.Run(context.Background())
}

// Run executes the seeding process.
func (s *SeedRunner) Run(ctx context.Context) error {
	start := time.Now()

	// Connect to databases
	transferPool, err := pgxpool.New(ctx, s.transferDBURL)
	if err != nil {
		return fmt.Errorf("connect transfer DB: %w", err)
	}
	defer transferPool.Close()

	treasuryPool, err := pgxpool.New(ctx, s.treasuryDBURL)
	if err != nil {
		return fmt.Errorf("connect treasury DB: %w", err)
	}
	defer treasuryPool.Close()

	if s.cleanupOnly {
		return s.cleanup(ctx, transferPool, treasuryPool)
	}

	s.logger.Info("starting tenant provisioning", "count", s.tenantCount)

	// Generate tenant configs
	tenants := GenerateScaleTenants(s.tenantCount, DefaultCurrencyMix())

	// Phase 1: Insert tenants and API keys into transfer DB
	if err := s.seedTenants(ctx, transferPool, tenants); err != nil {
		return fmt.Errorf("seed tenants: %w", err)
	}

	// Phase 2: Insert treasury positions
	if err := s.seedTreasury(ctx, treasuryPool, tenants); err != nil {
		return fmt.Errorf("seed treasury: %w", err)
	}

	elapsed := time.Since(start)
	s.logger.Info("provisioning complete",
		"tenants", s.tenantCount,
		"elapsed", elapsed.Round(time.Millisecond),
	)

	return nil
}

// seedTenants inserts tenants and API keys in batches of 1000.
func (s *SeedRunner) seedTenants(ctx context.Context, pool *pgxpool.Pool, tenants []TenantConfig) error {
	const batchSize = 1000

	s.logger.Info("seeding tenants", "total", len(tenants), "batch_size", batchSize)

	for batchStart := 0; batchStart < len(tenants); batchStart += batchSize {
		batchEnd := batchStart + batchSize
		if batchEnd > len(tenants) {
			batchEnd = len(tenants)
		}
		batch := tenants[batchStart:batchEnd]

		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin tx at batch %d: %w", batchStart, err)
		}

		// Batch insert tenants
		pgxBatch := &pgx.Batch{}
		for _, t := range batch {
			slug := fmt.Sprintf("scale-tenant-%s", t.ID[:8])
			name := fmt.Sprintf("Scale Tenant %s", t.ID[:8])
			feeJSON := `{"onramp_bps": 40, "offramp_bps": 25, "min_fee_usd": "0.50", "max_fee_usd": "500.00", "crypto_collection_bps": 50, "crypto_collection_max_fee_usd": "100.00", "bank_collection_bps": 30, "bank_collection_min_fee_usd": "1.00", "bank_collection_max_fee_usd": "200.00"}`

			pgxBatch.Queue(
				`INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model, daily_limit_usd, per_transfer_limit, kyb_status, kyb_verified_at, created_at, updated_at)
				 VALUES ($1, $2, $3, 'ACTIVE', $4::jsonb, 'PREFUNDED', 9999999999, 1000000, 'VERIFIED', NOW(), NOW(), NOW())
				 ON CONFLICT (id) DO NOTHING`,
				t.ID, name, slug, feeJSON,
			)

			// API key
			keyHash := hashLoadtestAPIKey(t.APIKey)
			keyID := fmt.Sprintf("%s-aaaa-4000-a000-%012x", t.ID[:8], batchStart+1)
			pgxBatch.Queue(
				`INSERT INTO api_keys (id, tenant_id, key_hash, key_prefix, environment, name, is_active, created_at)
				 VALUES ($1, $2, $3, $4, 'LIVE', 'scale-test-key', true, NOW())
				 ON CONFLICT (id) DO NOTHING`,
				keyID, t.ID, keyHash, t.APIKey[:14],
			)
		}

		br := tx.SendBatch(ctx, pgxBatch)
		for i := 0; i < pgxBatch.Len(); i++ {
			if _, err := br.Exec(); err != nil {
				_ = br.Close()
				_ = tx.Rollback(ctx)
				return fmt.Errorf("exec batch item %d at offset %d: %w", i, batchStart, err)
			}
		}
		if err := br.Close(); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("close batch at offset %d: %w", batchStart, err)
		}

		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit batch at offset %d: %w", batchStart, err)
		}

		if (batchStart+batchSize)%10000 == 0 || batchEnd == len(tenants) {
			s.logger.Info("tenants seeded", "progress", fmt.Sprintf("%d/%d", batchEnd, len(tenants)))
		}
	}

	return nil
}

// seedTreasury inserts treasury positions for each tenant.
func (s *SeedRunner) seedTreasury(ctx context.Context, pool *pgxpool.Pool, tenants []TenantConfig) error {
	const batchSize = 500 // 2 rows per tenant = 1000 rows per batch

	s.logger.Info("seeding treasury positions", "tenants", len(tenants))

	for batchStart := 0; batchStart < len(tenants); batchStart += batchSize {
		batchEnd := batchStart + batchSize
		if batchEnd > len(tenants) {
			batchEnd = len(tenants)
		}
		batch := tenants[batchStart:batchEnd]

		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin treasury tx at batch %d: %w", batchStart, err)
		}

		pgxBatch := &pgx.Batch{}
		for _, t := range batch {
			// Position for tenant's primary currency
			loc := fmt.Sprintf("bank:%s", toLower(t.Currency))
			pgxBatch.Queue(
				`INSERT INTO positions (tenant_id, currency, location, available, reserved, total, min_balance, target_balance, updated_at)
				 VALUES ($1, $2, $3, 999999999, 0, 999999999, 100000, 500000, NOW())
				 ON CONFLICT DO NOTHING`,
				t.ID, t.Currency, loc,
			)

			// USDT position on Tron
			pgxBatch.Queue(
				`INSERT INTO positions (tenant_id, currency, location, available, reserved, total, min_balance, target_balance, updated_at)
				 VALUES ($1, 'USDT', 'chain:tron', 999999999, 0, 999999999, 100000, 500000, NOW())
				 ON CONFLICT DO NOTHING`,
				t.ID,
			)
		}

		br := tx.SendBatch(ctx, pgxBatch)
		for i := 0; i < pgxBatch.Len(); i++ {
			if _, err := br.Exec(); err != nil {
				_ = br.Close()
				_ = tx.Rollback(ctx)
				return fmt.Errorf("exec treasury batch item %d at offset %d: %w", i, batchStart, err)
			}
		}
		if err := br.Close(); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("close treasury batch at offset %d: %w", batchStart, err)
		}

		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit treasury batch at offset %d: %w", batchStart, err)
		}

		if (batchStart+batchSize)%10000 == 0 || batchEnd == len(tenants) {
			s.logger.Info("treasury positions seeded", "progress", fmt.Sprintf("%d/%d", batchEnd, len(tenants)))
		}
	}

	return nil
}

// cleanup removes all scale-test tenants (slug LIKE 'scale-tenant-%').
func (s *SeedRunner) cleanup(ctx context.Context, transferPool, treasuryPool *pgxpool.Pool) error {
	s.logger.Info("cleaning up scale-test data")

	// Treasury positions first (references tenant_id)
	tag, err := treasuryPool.Exec(ctx,
		`DELETE FROM positions WHERE tenant_id IN (SELECT id FROM positions WHERE tenant_id::text LIKE '________-0000-4000-8000-%')`)
	if err != nil {
		// Treasury may not have the same tenants — best effort
		s.logger.Warn("treasury cleanup", "error", err)
	} else {
		s.logger.Info("treasury positions cleaned", "rows", tag.RowsAffected())
	}

	// API keys
	tag, err = transferPool.Exec(ctx,
		`DELETE FROM api_keys WHERE tenant_id IN (SELECT id FROM tenants WHERE slug LIKE 'scale-tenant-%')`)
	if err != nil {
		return fmt.Errorf("cleanup api_keys: %w", err)
	}
	s.logger.Info("api keys cleaned", "rows", tag.RowsAffected())

	// Tenants
	tag, err = transferPool.Exec(ctx,
		`DELETE FROM tenants WHERE slug LIKE 'scale-tenant-%'`)
	if err != nil {
		return fmt.Errorf("cleanup tenants: %w", err)
	}
	s.logger.Info("tenants cleaned", "rows", tag.RowsAffected())

	return nil
}

// hashLoadtestAPIKey computes the API key hash using the same algorithm as the
// gateway and gRPC server. Uses HMAC-SHA256 when SETTLA_API_KEY_HMAC_SECRET is
// set, plain SHA-256 otherwise.
func hashLoadtestAPIKey(rawKey string) string {
	secret := os.Getenv("SETTLA_API_KEY_HMAC_SECRET")
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(rawKey))
		return hex.EncodeToString(mac.Sum(nil))
	}
	hash := sha256.Sum256([]byte(rawKey))
	return hex.EncodeToString(hash[:])
}
