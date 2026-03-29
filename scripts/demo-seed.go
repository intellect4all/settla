// demo-seed.go — Standalone demo seed program for Settla.
// Seeds tenants, API keys, treasury positions, and ledger accounts across all three databases.
//
// Usage:
//
//	go run scripts/demo-seed.go --tenant-count=10
//	go run scripts/demo-seed.go --tenant-count=20000 --verbose
//	go run scripts/demo-seed.go --cleanup
package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

// ============================================================================
// Tier constants
// ============================================================================

const (
	TierEnterprise = "enterprise"
	TierGrowth     = "growth"
	TierStarter    = "starter"
)

// UUID v5 namespace for deterministic tenant IDs.
var demoNamespace = uuid.MustParse("d3e05eed-0000-4000-a000-000000000000")

// Preserved seed tenant IDs — never deleted by cleanup.
var preservedTenantIDs = [...]string{
	"a0000000-0000-0000-0000-000000000001", // Lemfi
	"b0000000-0000-0000-0000-000000000002", // Fincra
}

// ============================================================================
// Fintech name pool (120 names)
// ============================================================================

var fintechNames = []string{
	// African fintechs (primary)
	"Korapay", "Chipper Cash", "PiggyVest", "Kuda", "Opay", "PalmPay",
	"Flutterwave", "Paystack", "Interswitch", "Moniepoint", "Carbon", "FairMoney",
	"Cowrywise", "Brass", "Grey Finance", "Lemonade Finance", "Eversend", "Thepeer",
	"Mono", "Okra", "Stitch", "Pawapay", "Nala", "Wave", "Taptap Send",
	"Jumo", "Branch", "Tala", "MFS Africa", "Cellulant", "Paga", "VoguePay",
	"SimpleFi", "AZA Finance", "Leatherback", "Payday", "Send", "Zone", "Anchor",
	"Bloc", "Prospa", "Mkobo", "OnePipe", "Indicina", "Lendsqr", "CreditRegistry",
	"Aella", "Renmoney", "Page", "Barter", "Buycoins", "Quidax", "Bundle Africa",
	"Patricia", "Luno", "Yellow Card", "Risevest", "Bamboo", "Chaka", "Trove",
	"GetEquity", "Hisa", "Daba Finance", "TeamApt", "Sparkle", "Fairmoney",
	"Nomba", "Kudigo", "Payaza", "Credo", "SquadCo", "BudPay", "Fincra Pay",
	"Seerbit", "Monnify", "VFD Tech", "Woven Finance", "Sudo Africa",
	"Lazerpay", "Bitnob", "Kotani Pay", "Fonbnk", "Hover Dev", "Pezesha",
	"Umba", "FairPay", "Lidya", "Raven Bank", "Kora", "Mintyn", "Earnipay",
	// Global payment companies
	"Wise", "Revolut", "Nium", "Rapyd", "Airwallex", "Thunes", "Currencycloud",
	"dLocal", "Modulr", "Stripe Atlas", "Adyen", "Checkout", "Worldpay",
	"Remitly", "WorldRemit", "TransferGo", "Tilt", "Payoneer", "Paysend",
	"Xoom", "Azimo", "Skrill", "Neteller", "Veem", "Marqeta",
	"Plaid Connect", "Sardine", "Unit", "Moov", "Column", "Treasury Prime",
	"Cross River", "Galileo", "Tabapay", "Dwolla",
}

// ============================================================================
// Types
// ============================================================================

type SeedTenant struct {
	ID                  uuid.UUID
	Name                string
	Slug                string
	Tier                string
	OnRampBPS           int
	OffRampBPS          int
	MinFeeUSD           string
	MaxFeeUSD           string
	DailyLimitUSD       string
	PerTransferLimit    string
	MaxPendingTransfers int
	SettlementModel     string
	WebhookURL          string
	WebhookSecret       string
	LiveKeyRaw          string
	LiveKeyHash         string
	LiveKeyPrefix       string
	TestKeyRaw          string
	TestKeyHash         string
	TestKeyPrefix       string
	// Treasury balances
	GBPBalance  string
	NGNBalance  string
	USDBalance  string
	USDTBalance string
}

type config struct {
	tenantCount   int
	transferDBURL string
	ledgerDBURL   string
	treasuryDBURL string
	environment   string
	cleanup       bool
	verbose       bool
}

// ============================================================================
// Main
// ============================================================================

func main() {
	cfg := config{}
	flag.IntVar(&cfg.tenantCount, "tenant-count", 10, "Number of tenants to seed")
	flag.StringVar(&cfg.transferDBURL, "transfer-db-url",
		"postgres://settla:settla@localhost:5434/settla_transfer?sslmode=disable",
		"Transfer DB connection string")
	flag.StringVar(&cfg.ledgerDBURL, "ledger-db-url",
		"postgres://settla:settla@localhost:5433/settla_ledger?sslmode=disable",
		"Ledger DB connection string")
	flag.StringVar(&cfg.treasuryDBURL, "treasury-db-url",
		"postgres://settla:settla@localhost:5435/settla_treasury?sslmode=disable",
		"Treasury DB connection string")
	flag.StringVar(&cfg.environment, "environment", "local", "Environment: local, staging, production")
	flag.BoolVar(&cfg.cleanup, "cleanup", false, "Remove all seeded data instead of creating")
	flag.BoolVar(&cfg.verbose, "verbose", false, "Verbose logging")
	flag.Parse()

	if cfg.environment == "production" {
		log.Fatal("ERROR: refusing to seed production database. Use staging or local.")
	}

	start := time.Now()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	if cfg.cleanup {
		if err := runCleanup(ctx, cfg); err != nil {
			log.Fatalf("cleanup failed: %v", err)
		}
		log.Printf("cleanup completed in %v", time.Since(start).Round(time.Millisecond))
		return
	}

	if err := runSeed(ctx, cfg); err != nil {
		log.Fatalf("seed failed: %v", err)
	}
	log.Printf("seed completed in %v", time.Since(start).Round(time.Millisecond))
}

// ============================================================================
// Seed orchestration
// ============================================================================

func runSeed(ctx context.Context, cfg config) error {
	transferDB, err := sql.Open("postgres", cfg.transferDBURL)
	if err != nil {
		return fmt.Errorf("open transfer DB: %w", err)
	}
	defer transferDB.Close()
	transferDB.SetMaxOpenConns(10)

	ledgerDB, err := sql.Open("postgres", cfg.ledgerDBURL)
	if err != nil {
		return fmt.Errorf("open ledger DB: %w", err)
	}
	defer ledgerDB.Close()
	ledgerDB.SetMaxOpenConns(10)

	treasuryDB, err := sql.Open("postgres", cfg.treasuryDBURL)
	if err != nil {
		return fmt.Errorf("open treasury DB: %w", err)
	}
	defer treasuryDB.Close()
	treasuryDB.SetMaxOpenConns(10)

	// Verify connectivity
	for name, db := range map[string]*sql.DB{
		"transfer": transferDB,
		"ledger":   ledgerDB,
		"treasury": treasuryDB,
	} {
		if err := db.PingContext(ctx); err != nil {
			return fmt.Errorf("ping %s DB: %w", name, err)
		}
	}
	log.Println("connected to all databases")

	// Generate tenant configurations
	tenants := generateTenants(cfg.tenantCount)
	log.Printf("generated %d tenant configurations (enterprise: %d, growth: %d, starter: %d)",
		len(tenants), countTier(tenants, TierEnterprise), countTier(tenants, TierGrowth), countTier(tenants, TierStarter))

	// Phase 1: Tenants
	log.Println("phase 1/6: seeding tenants...")
	if err := seedTenants(ctx, transferDB, tenants, cfg.verbose); err != nil {
		return fmt.Errorf("seed tenants: %w", err)
	}

	// Phase 2: API keys
	log.Println("phase 2/6: seeding API keys...")
	if err := seedAPIKeys(ctx, transferDB, tenants, cfg.verbose); err != nil {
		return fmt.Errorf("seed API keys: %w", err)
	}

	// Phase 3: Treasury positions
	log.Println("phase 3/6: seeding treasury positions...")
	if err := seedTreasuryPositions(ctx, treasuryDB, tenants, cfg.verbose); err != nil {
		return fmt.Errorf("seed treasury positions: %w", err)
	}

	// Phase 4: Ledger accounts
	log.Println("phase 4/6: seeding ledger accounts...")
	if err := seedLedgerAccounts(ctx, ledgerDB, tenants, cfg.verbose); err != nil {
		return fmt.Errorf("seed ledger accounts: %w", err)
	}

	// Phase 5: Virtual accounts
	log.Println("phase 5/6: seeding virtual accounts...")
	if err := seedVirtualAccounts(ctx, transferDB, tenants, cfg.verbose); err != nil {
		return fmt.Errorf("seed virtual accounts: %w", err)
	}

	// Phase 6: Enable bank deposits on tenants
	log.Println("phase 6/6: enabling bank deposits...")
	if err := enableBankDeposits(ctx, transferDB, tenants); err != nil {
		return fmt.Errorf("enable bank deposits: %w", err)
	}

	// Summary
	log.Println("=== Seed Summary ===")
	log.Printf("  Tenants:            %d", len(tenants))
	log.Printf("  API keys:           %d (2 per tenant)", len(tenants)*2)
	log.Printf("  Treasury positions: %d (4 per tenant)", len(tenants)*4)
	log.Printf("  Ledger accounts:    %d (8 per tenant)", len(tenants)*8)
	log.Printf("  Virtual accounts:   %d (3 per tenant)", len(tenants)*3)

	if cfg.verbose && len(tenants) <= 20 {
		log.Println("\n=== Sample Tenants ===")
		for i, t := range tenants {
			if i >= 10 {
				log.Printf("  ... and %d more", len(tenants)-10)
				break
			}
			log.Printf("  %-25s  tier=%-10s  slug=%-30s  live_key=%s...", t.Name, t.Tier, t.Slug, t.LiveKeyRaw[:20])
		}
	}

	return nil
}

// ============================================================================
// Tenant generation
// ============================================================================

func generateTenants(count int) []SeedTenant {
	tenants := make([]SeedTenant, 0, count)

	for i := range count {
		name := fintechNames[i%len(fintechNames)]
		// For names beyond the pool, append a numeric suffix
		if i >= len(fintechNames) {
			name = fmt.Sprintf("%s %d", fintechNames[i%len(fintechNames)], i/len(fintechNames)+1)
		}

		slug := toSlug(name)
		id := uuid.NewSHA1(demoNamespace, fmt.Appendf(nil, "demo-tenant-%d-%s", i, slug))
		tier := assignTier(i, count)

		t := SeedTenant{
			ID:   id,
			Name: name,
			Slug: slug,
			Tier: tier,
		}

		// Fee schedule by tier
		switch tier {
		case TierEnterprise:
			t.OnRampBPS = 25
			t.OffRampBPS = 20
			t.MinFeeUSD = "0.25"
			t.MaxFeeUSD = "1000.00"
			t.DailyLimitUSD = "10000000.00000000"
			t.PerTransferLimit = "1000000.00000000"
			t.MaxPendingTransfers = 10000
			t.SettlementModel = "PREFUNDED"
			t.GBPBalance = "1000000.00000000"
			t.NGNBalance = "500000000.00000000"
			t.USDBalance = "500000.00000000"
			t.USDTBalance = "100000.00000000"
		case TierGrowth:
			t.OnRampBPS = 35
			t.OffRampBPS = 30
			t.MinFeeUSD = "0.50"
			t.MaxFeeUSD = "500.00"
			t.DailyLimitUSD = "1000000.00000000"
			t.PerTransferLimit = "100000.00000000"
			t.MaxPendingTransfers = 5000
			t.SettlementModel = "NET_SETTLEMENT"
			t.GBPBalance = "100000.00000000"
			t.NGNBalance = "50000000.00000000"
			t.USDBalance = "50000.00000000"
			t.USDTBalance = "10000.00000000"
		default: // TierStarter
			t.OnRampBPS = 50
			t.OffRampBPS = 45
			t.MinFeeUSD = "1.00"
			t.MaxFeeUSD = "250.00"
			t.DailyLimitUSD = "100000.00000000"
			t.PerTransferLimit = "10000.00000000"
			t.MaxPendingTransfers = 1000
			t.SettlementModel = "PREFUNDED"
			t.GBPBalance = "10000.00000000"
			t.NGNBalance = "5000000.00000000"
			t.USDBalance = "5000.00000000"
			t.USDTBalance = "1000.00000000"
		}

		t.WebhookURL = fmt.Sprintf("https://webhook.example.com/%s", slug)
		t.WebhookSecret = deterministicWebhookSecret(slug)

		// Generate API keys
		t.LiveKeyRaw = fmt.Sprintf("sk_live_%s_%s", slug, id.String()[:8])
		t.LiveKeyHash = hashAPIKey(t.LiveKeyRaw)
		t.LiveKeyPrefix = t.LiveKeyRaw[:12]

		t.TestKeyRaw = fmt.Sprintf("sk_test_%s_%s", slug, id.String()[:8])
		t.TestKeyHash = hashAPIKey(t.TestKeyRaw)
		t.TestKeyPrefix = t.TestKeyRaw[:12]

		tenants = append(tenants, t)
	}

	return tenants
}

// assignTier uses a Zipf-like distribution: top 1% enterprise, next 10% growth, rest starter.
func assignTier(index, total int) string {
	if total <= 0 {
		return TierStarter
	}
	// Use rank-based assignment
	rank := float64(index) / float64(total)
	if rank < 0.01 || (total < 100 && index == 0) {
		return TierEnterprise
	}
	if rank < 0.11 || (total < 20 && index <= 1) {
		return TierGrowth
	}
	return TierStarter
}

func countTier(tenants []SeedTenant, tier string) int {
	n := 0
	for _, t := range tenants {
		if t.Tier == tier {
			n++
		}
	}
	return n
}

// ============================================================================
// Phase 1: Seed tenants
// ============================================================================

func seedTenants(ctx context.Context, db *sql.DB, tenants []SeedTenant, _ bool) error {
	const batchSize = 500
	seeded := 0

	for batchStart := 0; batchStart < len(tenants); batchStart += batchSize {
		batchEnd := min(batchStart+batchSize, len(tenants))
		batch := tenants[batchStart:batchEnd]

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx at offset %d: %w", batchStart, err)
		}

		var sb strings.Builder
		sb.WriteString(`INSERT INTO tenants (id, name, slug, status, fee_schedule, settlement_model,
			webhook_url, webhook_secret, daily_limit_usd, per_transfer_limit, max_pending_transfers,
			kyb_status, kyb_verified_at, metadata, created_at, updated_at)
			VALUES `)

		args := make([]any, 0, len(batch)*14)
		for i, t := range batch {
			if i > 0 {
				sb.WriteString(",")
			}
			base := i * 14
			fmt.Fprintf(&sb,
				"($%d,$%d,$%d,'ACTIVE',$%d,$%d,$%d,$%d,$%d,$%d,$%d,'VERIFIED',NOW(),$%d,NOW(),NOW())",
				base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9, base+10, base+11,
			)
			feeJSON := fmt.Sprintf(
				`{"onramp_bps": %d, "offramp_bps": %d, "min_fee_usd": "%s", "max_fee_usd": "%s", "bank_collection_bps": %d, "bank_collection_min_fee_usd": "0.50", "bank_collection_max_fee_usd": "250.00"}`,
				t.OnRampBPS, t.OffRampBPS, t.MinFeeUSD, t.MaxFeeUSD, t.OnRampBPS,
			)
			globalIdx := batchStart + i
			trafficWeight := zipfWeight(globalIdx, len(tenants))
			metaJSON := fmt.Sprintf(`{"tier": "%s", "seeded_by": "demo-seed", "traffic_weight": %.6f}`, t.Tier, trafficWeight)
			args = append(args,
				t.ID.String(), t.Name, t.Slug, feeJSON, t.SettlementModel,
				t.WebhookURL, t.WebhookSecret, t.DailyLimitUSD, t.PerTransferLimit,
				t.MaxPendingTransfers, metaJSON,
			)
		}
		sb.WriteString(` ON CONFLICT (slug) DO UPDATE SET
			name = EXCLUDED.name,
			fee_schedule = EXCLUDED.fee_schedule,
			settlement_model = EXCLUDED.settlement_model,
			webhook_url = EXCLUDED.webhook_url,
			daily_limit_usd = EXCLUDED.daily_limit_usd,
			per_transfer_limit = EXCLUDED.per_transfer_limit,
			max_pending_transfers = EXCLUDED.max_pending_transfers,
			metadata = EXCLUDED.metadata,
			updated_at = NOW()`)

		if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert tenants at offset %d: %w", batchStart, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit tenants at offset %d: %w", batchStart, err)
		}

		seeded += len(batch)
		if seeded%1000 == 0 || seeded == len(tenants) {
			log.Printf("  tenants: %d/%d", seeded, len(tenants))
		}
	}

	return nil
}

// ============================================================================
// Phase 2: Seed API keys
// ============================================================================

func seedAPIKeys(ctx context.Context, db *sql.DB, tenants []SeedTenant, _ bool) error {
	const batchSize = 500
	seeded := 0

	for batchStart := 0; batchStart < len(tenants); batchStart += batchSize {
		batchEnd := min(batchStart+batchSize, len(tenants))
		batch := tenants[batchStart:batchEnd]

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx at offset %d: %w", batchStart, err)
		}

		var sb strings.Builder
		sb.WriteString(`INSERT INTO api_keys (id, tenant_id, key_hash, key_prefix, environment, name, is_active, created_at) VALUES `)

		args := make([]any, 0, len(batch)*12)
		paramIdx := 0
		for i, t := range batch {
			// Live key
			liveKeyID := uuid.NewSHA1(demoNamespace, fmt.Appendf(nil, "api-key-live-%s", t.ID.String()))
			// Test key
			testKeyID := uuid.NewSHA1(demoNamespace, fmt.Appendf(nil, "api-key-test-%s", t.ID.String()))

			if i > 0 {
				sb.WriteString(",")
			}
			// Live key row
			fmt.Fprintf(&sb, "($%d,$%d,$%d,$%d,'LIVE',$%d,true,NOW())",
				paramIdx+1, paramIdx+2, paramIdx+3, paramIdx+4, paramIdx+5)
			args = append(args, liveKeyID.String(), t.ID.String(), t.LiveKeyHash, t.LiveKeyPrefix,
				fmt.Sprintf("%s live key", t.Slug))
			paramIdx += 5

			// Test key row
			fmt.Fprintf(&sb, ",($%d,$%d,$%d,$%d,'TEST',$%d,true,NOW())",
				paramIdx+1, paramIdx+2, paramIdx+3, paramIdx+4, paramIdx+5)
			args = append(args, testKeyID.String(), t.ID.String(), t.TestKeyHash, t.TestKeyPrefix,
				fmt.Sprintf("%s test key", t.Slug))
			paramIdx += 5
		}
		sb.WriteString(` ON CONFLICT (key_hash) DO NOTHING`)

		if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert api keys at offset %d: %w", batchStart, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit api keys at offset %d: %w", batchStart, err)
		}

		seeded += len(batch)
		if seeded%1000 == 0 || seeded == len(tenants) {
			log.Printf("  api keys: %d/%d tenants", seeded, len(tenants))
		}
	}

	return nil
}

// ============================================================================
// Phase 3: Seed treasury positions
// ============================================================================

func seedTreasuryPositions(ctx context.Context, db *sql.DB, tenants []SeedTenant, _ bool) error {
	const batchSize = 250 // 4 positions per tenant = 1000 rows per batch
	seeded := 0

	for batchStart := 0; batchStart < len(tenants); batchStart += batchSize {
		batchEnd := min(batchStart+batchSize, len(tenants))
		batch := tenants[batchStart:batchEnd]

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx at offset %d: %w", batchStart, err)
		}

		var sb strings.Builder
		sb.WriteString(`INSERT INTO positions (tenant_id, currency, location, balance, locked, min_balance, target_balance, updated_at) VALUES `)

		args := make([]any, 0, len(batch)*24)
		paramIdx := 0
		first := true

		for _, t := range batch {
			positions := []struct {
				currency, location, balance, minBalance, targetBalance string
			}{
				{"GBP", "bank:gbp", t.GBPBalance, scaleMinBalance(t.GBPBalance, "0.05"), scaleTargetBalance(t.GBPBalance, "0.75")},
				{"NGN", "bank:ngn", t.NGNBalance, scaleMinBalance(t.NGNBalance, "0.02"), scaleTargetBalance(t.NGNBalance, "0.60")},
				{"USD", "bank:usd", t.USDBalance, scaleMinBalance(t.USDBalance, "0.05"), scaleTargetBalance(t.USDBalance, "0.75")},
				{"USDT", "chain:tron", t.USDTBalance, scaleMinBalance(t.USDTBalance, "0.10"), scaleTargetBalance(t.USDTBalance, "0.80")},
			}

			for _, p := range positions {
				if !first {
					sb.WriteString(",")
				}
				first = false
				fmt.Fprintf(&sb, "($%d,$%d,$%d,$%d,0,$%d,$%d,NOW())",
					paramIdx+1, paramIdx+2, paramIdx+3, paramIdx+4, paramIdx+5, paramIdx+6)
				args = append(args, t.ID.String(), p.currency, p.location, p.balance, p.minBalance, p.targetBalance)
				paramIdx += 6
			}
		}
		sb.WriteString(` ON CONFLICT (tenant_id, currency, location) DO UPDATE SET
			balance = EXCLUDED.balance,
			min_balance = EXCLUDED.min_balance,
			target_balance = EXCLUDED.target_balance,
			updated_at = NOW()`)

		if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert treasury positions at offset %d: %w", batchStart, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit treasury positions at offset %d: %w", batchStart, err)
		}

		seeded += len(batch)
		if seeded%1000 == 0 || seeded == len(tenants) {
			log.Printf("  treasury positions: %d/%d tenants", seeded, len(tenants))
		}
	}

	return nil
}

// ============================================================================
// Phase 4: Seed ledger accounts
// ============================================================================

func seedLedgerAccounts(ctx context.Context, db *sql.DB, tenants []SeedTenant, _ bool) error {
	const batchSize = 125 // 8 accounts per tenant = 1000 rows per batch
	seeded := 0

	for batchStart := 0; batchStart < len(tenants); batchStart += batchSize {
		batchEnd := min(batchStart+batchSize, len(tenants))
		batch := tenants[batchStart:batchEnd]

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx at offset %d: %w", batchStart, err)
		}

		type acctDef struct {
			codeSuffix, name, acctType, currency, normalBalance string
		}
		accountDefs := []acctDef{
			{"assets:bank:gbp:clearing", "GBP Clearing", "ASSET", "GBP", "DEBIT"},
			{"assets:bank:ngn:clearing", "NGN Clearing", "ASSET", "NGN", "DEBIT"},
			{"assets:bank:usd:clearing", "USD Clearing", "ASSET", "USD", "DEBIT"},
			{"assets:crypto:usdt:tron", "USDT on Tron", "ASSET", "USDT", "DEBIT"},
			{"liabilities:settlement:gbp", "GBP Settlement", "LIABILITY", "GBP", "CREDIT"},
			{"liabilities:settlement:ngn", "NGN Settlement", "LIABILITY", "NGN", "CREDIT"},
			{"revenue:fees:gbp", "GBP Fees", "REVENUE", "GBP", "CREDIT"},
			{"revenue:fees:ngn", "NGN Fees", "REVENUE", "NGN", "CREDIT"},
		}

		var sb strings.Builder
		sb.WriteString(`INSERT INTO accounts (tenant_id, code, name, type, currency, normal_balance, metadata, created_at, updated_at) VALUES `)

		args := make([]any, 0, len(batch)*len(accountDefs)*7)
		paramIdx := 0
		first := true

		for _, t := range batch {
			for _, def := range accountDefs {
				if !first {
					sb.WriteString(",")
				}
				first = false
				code := fmt.Sprintf("tenant:%s:%s", t.Slug, def.codeSuffix)
				name := fmt.Sprintf("%s %s", t.Name, def.name)
				fmt.Fprintf(&sb, "($%d,$%d,$%d,$%d,$%d,$%d,'{\"seeded_by\": \"demo-seed\"}'::jsonb,NOW(),NOW())",
					paramIdx+1, paramIdx+2, paramIdx+3, paramIdx+4, paramIdx+5, paramIdx+6)
				args = append(args, t.ID.String(), code, name, def.acctType, def.currency, def.normalBalance)
				paramIdx += 6
			}
		}
		sb.WriteString(` ON CONFLICT (code) DO NOTHING`)

		if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert ledger accounts at offset %d: %w", batchStart, err)
		}

		// Also seed balance_snapshots for any newly created accounts
		_, err = tx.ExecContext(ctx, `INSERT INTO balance_snapshots (account_id, balance, version)
			SELECT id, 0, 0 FROM accounts WHERE metadata->>'seeded_by' = 'demo-seed'
			ON CONFLICT (account_id) DO NOTHING`)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert balance snapshots at offset %d: %w", batchStart, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit ledger accounts at offset %d: %w", batchStart, err)
		}

		seeded += len(batch)
		if seeded%1000 == 0 || seeded == len(tenants) {
			log.Printf("  ledger accounts: %d/%d tenants", seeded, len(tenants))
		}
	}

	return nil
}

// ============================================================================
// Phase 5: Seed virtual accounts (3 per tenant: GBP, NGN, USD)
// ============================================================================

// demoBankingPartnerID is a deterministic UUID for the demo banking partner.
var demoBankingPartnerID = uuid.NewSHA1(demoNamespace, []byte("demo-banking-partner"))

func seedVirtualAccounts(ctx context.Context, db *sql.DB, tenants []SeedTenant, _ bool) error {
	// Ensure the demo banking partner exists.
	_, err := db.ExecContext(ctx, `INSERT INTO banking_partners (id, name, webhook_secret, supported_currencies, is_active, metadata, created_at, updated_at)
		VALUES ($1, 'Demo Bank', 'whsec_demo_bank_partner', '{GBP,NGN,USD}', true, '{"seeded_by": "demo-seed"}'::jsonb, NOW(), NOW())
		ON CONFLICT (id) DO NOTHING`, demoBankingPartnerID.String())
	if err != nil {
		return fmt.Errorf("upsert banking partner: %w", err)
	}

	currencies := []string{"GBP", "NGN", "USD"}
	const batchSize = 200 // 3 accounts per tenant = 600 rows per batch
	seeded := 0

	for batchStart := 0; batchStart < len(tenants); batchStart += batchSize {
		batchEnd := min(batchStart+batchSize, len(tenants))
		batch := tenants[batchStart:batchEnd]

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx at offset %d: %w", batchStart, err)
		}

		var sb strings.Builder
		sb.WriteString(`INSERT INTO virtual_account_pool (id, tenant_id, banking_partner_id, account_number, account_name, sort_code, currency, account_type, available, created_at, updated_at) VALUES `)

		args := make([]any, 0, len(batch)*len(currencies)*9)
		paramIdx := 0
		first := true

		for _, t := range batch {
			for _, ccy := range currencies {
				vaID := uuid.NewSHA1(demoNamespace, fmt.Appendf(nil, "va-%s-%s", t.ID.String(), ccy))
				accountNumber := fmt.Sprintf("VA%s%s", strings.ToUpper(t.Slug[:min(len(t.Slug), 8)]), ccy)
				accountName := fmt.Sprintf("%s %s Account", t.Name, ccy)
				sortCode := fmt.Sprintf("00-%02d-%02d", (batchStart+1)%100, paramIdx%100)

				if !first {
					sb.WriteString(",")
				}
				first = false
				fmt.Fprintf(&sb, "($%d,$%d,$%d,$%d,$%d,$%d,$%d,'PERMANENT',true,NOW(),NOW())",
					paramIdx+1, paramIdx+2, paramIdx+3, paramIdx+4, paramIdx+5, paramIdx+6, paramIdx+7)
				args = append(args,
					vaID.String(), t.ID.String(), demoBankingPartnerID.String(),
					accountNumber, accountName, sortCode, ccy,
				)
				paramIdx += 7
			}
		}
		sb.WriteString(` ON CONFLICT (account_number) DO NOTHING`)

		if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert virtual accounts at offset %d: %w", batchStart, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit virtual accounts at offset %d: %w", batchStart, err)
		}

		seeded += len(batch)
		if seeded%1000 == 0 || seeded == len(tenants) {
			log.Printf("  virtual accounts: %d/%d tenants (%d accounts)", seeded, len(tenants), seeded*3)
		}
	}

	return nil
}

// enableBankDeposits sets bank_deposits_enabled=true and configures banking for all seeded tenants.
func enableBankDeposits(ctx context.Context, db *sql.DB, tenants []SeedTenant) error {
	const batchSize = 500

	for batchStart := 0; batchStart < len(tenants); batchStart += batchSize {
		batchEnd := min(batchStart+batchSize, len(tenants))
		batch := tenants[batchStart:batchEnd]

		placeholders := make([]string, len(batch))
		args := make([]any, len(batch))
		for i, t := range batch {
			placeholders[i] = fmt.Sprintf("$%d", i+1)
			args[i] = t.ID.String()
		}
		inClause := strings.Join(placeholders, ",")

		_, err := db.ExecContext(ctx, fmt.Sprintf(
			`UPDATE tenants SET
				bank_deposits_enabled = true,
				default_banking_partner = 'Demo Bank',
				bank_supported_currencies = '{GBP,NGN,USD}'
			WHERE id IN (%s)`, inClause), args...)
		if err != nil {
			return fmt.Errorf("enable bank deposits at offset %d: %w", batchStart, err)
		}
	}

	return nil
}

// ============================================================================
// Cleanup
// ============================================================================

func runCleanup(ctx context.Context, cfg config) error {
	transferDB, err := sql.Open("postgres", cfg.transferDBURL)
	if err != nil {
		return fmt.Errorf("open transfer DB: %w", err)
	}
	defer transferDB.Close()

	ledgerDB, err := sql.Open("postgres", cfg.ledgerDBURL)
	if err != nil {
		return fmt.Errorf("open ledger DB: %w", err)
	}
	defer ledgerDB.Close()

	treasuryDB, err := sql.Open("postgres", cfg.treasuryDBURL)
	if err != nil {
		return fmt.Errorf("open treasury DB: %w", err)
	}
	defer treasuryDB.Close()

	// Identify seeded tenants (metadata contains "demo-seed") excluding preserved ones
	log.Println("identifying demo-seeded tenants...")
	rows, err := transferDB.QueryContext(ctx,
		`SELECT id, slug FROM tenants WHERE metadata->>'seeded_by' = 'demo-seed'
		 AND id NOT IN ($1, $2)`,
		preservedTenantIDs[0],
		preservedTenantIDs[1],
	)
	if err != nil {
		return fmt.Errorf("query seeded tenants: %w", err)
	}
	var tenantIDs []string
	var tenantSlugs []string
	for rows.Next() {
		var id, slug string
		if err := rows.Scan(&id, &slug); err != nil {
			rows.Close()
			return fmt.Errorf("scan tenant: %w", err)
		}
		tenantIDs = append(tenantIDs, id)
		tenantSlugs = append(tenantSlugs, slug)
	}
	rows.Close()

	if len(tenantIDs) == 0 {
		log.Println("no demo-seeded tenants found, nothing to clean up")
		return nil
	}
	log.Printf("found %d demo-seeded tenants to remove", len(tenantIDs))

	// Build IN clause placeholders
	placeholders := make([]string, len(tenantIDs))
	tenantArgs := make([]any, len(tenantIDs))
	for i, id := range tenantIDs {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		tenantArgs[i] = id
	}
	inClause := strings.Join(placeholders, ",")

	// Ledger: remove accounts and balance snapshots
	log.Println("cleaning ledger accounts...")
	_, err = ledgerDB.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM balance_snapshots WHERE account_id IN (SELECT id FROM accounts WHERE tenant_id IN (%s))`, inClause), tenantArgs...)
	if err != nil {
		log.Printf("  warning: balance_snapshots cleanup: %v", err)
	}
	res, err := ledgerDB.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM accounts WHERE tenant_id IN (%s)`, inClause), tenantArgs...)
	if err != nil {
		log.Printf("  warning: accounts cleanup: %v", err)
	} else {
		n, _ := res.RowsAffected()
		log.Printf("  removed %d ledger accounts", n)
	}

	// Treasury: remove positions
	log.Println("cleaning treasury positions...")
	res, err = treasuryDB.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM positions WHERE tenant_id IN (%s)`, inClause), tenantArgs...)
	if err != nil {
		log.Printf("  warning: positions cleanup: %v", err)
	} else {
		n, _ := res.RowsAffected()
		log.Printf("  removed %d treasury positions", n)
	}

	// Transfer: remove virtual accounts, API keys, then tenants
	log.Println("cleaning virtual accounts...")
	res, err = transferDB.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM virtual_account_pool WHERE tenant_id IN (%s)`, inClause), tenantArgs...)
	if err != nil {
		log.Printf("  warning: virtual_account_pool cleanup: %v", err)
	} else {
		n, _ := res.RowsAffected()
		log.Printf("  removed %d virtual accounts", n)
	}

	log.Println("cleaning API keys...")
	res, err = transferDB.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM api_keys WHERE tenant_id IN (%s)`, inClause), tenantArgs...)
	if err != nil {
		return fmt.Errorf("cleanup api_keys: %w", err)
	}
	n, _ := res.RowsAffected()
	log.Printf("  removed %d API keys", n)

	log.Println("cleaning tenants...")
	res, err = transferDB.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM tenants WHERE id IN (%s)`, inClause), tenantArgs...)
	if err != nil {
		return fmt.Errorf("cleanup tenants: %w", err)
	}
	n, _ = res.RowsAffected()
	log.Printf("  removed %d tenants", n)

	return nil
}

// ============================================================================
// Helpers
// ============================================================================

// toSlug converts a fintech name to a URL-safe slug.
func toSlug(name string) string {
	var sb strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			sb.WriteRune(r)
			prevDash = false
		} else if !prevDash && sb.Len() > 0 {
			sb.WriteRune('-')
			prevDash = true
		}
	}
	s := sb.String()
	return strings.TrimRight(s, "-")
}

// hashAPIKey computes SHA-256 or HMAC-SHA-256 of the raw key, matching the gateway logic.
func hashAPIKey(rawKey string) string {
	secret := os.Getenv("SETTLA_API_KEY_HMAC_SECRET")
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(rawKey))
		return hex.EncodeToString(mac.Sum(nil))
	}
	hash := sha256.Sum256([]byte(rawKey))
	return hex.EncodeToString(hash[:])
}

// deterministicWebhookSecret generates a deterministic webhook secret from the slug.
func deterministicWebhookSecret(slug string) string {
	hash := sha256.Sum256([]byte("whsec_demo_" + slug))
	return "whsec_" + hex.EncodeToString(hash[:16])
}

// scaleMinBalance computes minBalance as a fraction of the balance string.
// fraction is a string like "0.05" representing 5%.
func scaleMinBalance(balance, fraction string) string {
	b := parseFloat(balance)
	f := parseFloat(fraction)
	result := b * f
	return fmt.Sprintf("%.8f", result)
}

// scaleTargetBalance computes targetBalance as a fraction of the balance string.
func scaleTargetBalance(balance, fraction string) string {
	b := parseFloat(balance)
	f := parseFloat(fraction)
	result := b * f
	return fmt.Sprintf("%.8f", result)
}

// zipfWeight computes a normalized Zipf traffic weight for a tenant at the given rank.
// Rank 0 gets the highest weight. Uses exponent s=1.2 to match tests/loadtest/zipf.go.
// Top 1% of tenants receive ~50% of total traffic weight.
func zipfWeight(rank, total int) float64 {
	if total <= 0 {
		return 1.0
	}
	const s = 1.2
	// Harmonic number H(N,s) for normalization.
	var harmonic float64
	for i := 1; i <= total; i++ {
		harmonic += 1.0 / math.Pow(float64(i), s)
	}
	if harmonic == 0 {
		return 1.0 / float64(total)
	}
	weight := (1.0 / math.Pow(float64(rank+1), s)) / harmonic
	return weight
}

// parseFloat parses a numeric string to float64 (for seed balance calculation only,
// NOT for monetary math in the hot path — this is acceptable for seed data generation).
func parseFloat(s string) float64 {
	var f float64
	_, _ = fmt.Sscanf(s, "%f", &f)
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return f
}
