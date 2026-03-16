CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_api_keys_active_hash
  ON api_keys(key_hash)
  WHERE is_active = true;
