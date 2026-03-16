-- ============================================================================
-- Consolidated migration: constraints + indexes
-- Merges original migrations 000008, 000009, 000010, 000012, 000016, 000017
-- ============================================================================

-- Unique constraints (from 000008)
ALTER TABLE manual_reviews
  ADD CONSTRAINT uk_manual_reviews_transfer_id UNIQUE (transfer_id);

ALTER TABLE compensation_records
  ADD CONSTRAINT uk_compensation_records_transfer_id UNIQUE (transfer_id);

-- Foreign keys (from 000009)
-- NOTE: FKs from non-partitioned tables TO partitioned tables (transfers)
-- are NOT supported in PostgreSQL < 17. Referential integrity to the transfers
-- table is enforced at the application layer.

ALTER TABLE provider_transactions
  ADD CONSTRAINT fk_provider_txns_tenant
  FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE manual_reviews
  ADD CONSTRAINT fk_manual_reviews_tenant
  FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE compensation_records
  ADD CONSTRAINT fk_compensation_tenant
  FOREIGN KEY (tenant_id) REFERENCES tenants(id);

ALTER TABLE net_settlements
  ADD CONSTRAINT fk_net_settlements_tenant
  FOREIGN KEY (tenant_id) REFERENCES tenants(id);

-- Outbox relay covering index (from 000010)
CREATE INDEX IF NOT EXISTS idx_outbox_relay_covering
  ON outbox (published, created_at ASC, published_at)
  WHERE published = false;

-- Provider transaction unique index for exactly-once semantics (from 000012)
CREATE UNIQUE INDEX IF NOT EXISTS uk_provider_txns_transfer_type
  ON provider_transactions(tenant_id, transfer_id, tx_type);

-- Analytics covering index for GROUP BY tenant_id + time range + status (from 000016)
CREATE INDEX IF NOT EXISTS idx_transfers_analytics
    ON transfers (tenant_id, created_at DESC, status);

-- Provider transaction lookup index (from 000017)
CREATE INDEX IF NOT EXISTS idx_provider_txns_transfer_type ON provider_transactions(transfer_id, tx_type);
