CREATE INDEX IF NOT EXISTS idx_provider_txns_external_id
  ON provider_transactions(external_id)
  WHERE external_id IS NOT NULL;
