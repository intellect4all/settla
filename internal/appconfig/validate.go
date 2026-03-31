package appconfig

import (
	"fmt"
	"strings"
)

// Validate checks BaseConfig fields that are shared by both binaries.
// Returns an error listing every invalid or missing field.
func (c *BaseConfig) Validate() []string {
	var errs []string

	// ── Always required (all environments) ──────────────────────────
	if c.TransferDBURL == "" {
		errs = append(errs, "SETTLA_TRANSFER_DB_URL is required")
	}
	if c.LedgerDBURL == "" {
		errs = append(errs, "SETTLA_LEDGER_DB_URL is required")
	}
	if c.TreasuryDBURL == "" {
		errs = append(errs, "SETTLA_TREASURY_DB_URL is required")
	}
	if c.TigerBeetleAddresses == "" {
		errs = append(errs, "SETTLA_TIGERBEETLE_ADDRESSES is required — TigerBeetle is integral to Settla, there is no stub mode")
	}
	if c.NATSURL == "" {
		errs = append(errs, "SETTLA_NATS_URL is required")
	}

	// ── Range checks ────────────────────────────────────────────────
	if c.NATSInitialReplicas < 1 || c.NATSInitialReplicas > 5 {
		errs = append(errs, fmt.Sprintf("SETTLA_NATS_REPLICAS must be between 1 and 5, got %d", c.NATSInitialReplicas))
	}
	if c.NodePartitions < 1 || c.NodePartitions > 256 {
		errs = append(errs, fmt.Sprintf("SETTLA_NODE_PARTITIONS must be between 1 and 256, got %d", c.NodePartitions))
	}

	// ── SSL enforcement ─────────────────────────────────────────────
	if c.Env.RequiresSSL() {
		for _, pair := range []struct{ name, url string }{
			{"SETTLA_TRANSFER_DB_URL", c.TransferDBURL},
			{"SETTLA_LEDGER_DB_URL", c.LedgerDBURL},
			{"SETTLA_TREASURY_DB_URL", c.TreasuryDBURL},
		} {
			if pair.url != "" && strings.Contains(pair.url, "sslmode=disable") {
				errs = append(errs, fmt.Sprintf("%s must not use sslmode=disable in %s — use sslmode=verify-ca or sslmode=verify-full", pair.name, c.Env))
			}
		}
	}

	// ── Conditional requirements (prod/staging) ─────────────────────
	if c.Env.RequiresAuth() {
		if c.NATSToken == "" && c.NATSUser == "" {
			errs = append(errs, fmt.Sprintf("NATS authentication required in %s — set SETTLA_NATS_TOKEN or SETTLA_NATS_USER/SETTLA_NATS_PASSWORD", c.Env))
		}
		if c.NATSUser != "" && c.NATSPassword == "" {
			errs = append(errs, "SETTLA_NATS_PASSWORD is required when SETTLA_NATS_USER is set")
		}
		if c.RedisURL == "" {
			errs = append(errs, fmt.Sprintf("SETTLA_REDIS_URL is required in %s", c.Env))
		}
	}

	return errs
}

// Validate checks all ServerConfig fields including base config.
func (c *ServerConfig) Validate() error {
	errs := c.BaseConfig.Validate()

	if c.JWTSecret == "" {
		errs = append(errs, "SETTLA_JWT_SECRET is required — refusing to start without a JWT signing secret")
	}

	if c.Env.RequiresRLS() && c.TransferAppDBURL == "" {
		errs = append(errs, fmt.Sprintf("SETTLA_TRANSFER_APP_DB_URL is required in %s for RLS enforcement", c.Env))
	}

	if c.Env.RequiresSSL() && c.TransferAppDBURL != "" && strings.Contains(c.TransferAppDBURL, "sslmode=disable") {
		errs = append(errs, fmt.Sprintf("SETTLA_TRANSFER_APP_DB_URL must not use sslmode=disable in %s", c.Env))
	}

	if len(errs) > 0 {
		return fmt.Errorf("settla-server config validation failed:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// Validate checks all NodeConfig fields including base config.
func (c *NodeConfig) Validate() error {
	errs := c.BaseConfig.Validate()

	if c.PartitionID >= 0 && c.PartitionID >= c.NodePartitions {
		errs = append(errs, fmt.Sprintf("SETTLA_NODE_PARTITION_ID (%d) must be < SETTLA_NODE_PARTITIONS (%d)", c.PartitionID, c.NodePartitions))
	}

	if len(errs) > 0 {
		return fmt.Errorf("settla-node config validation failed:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}
