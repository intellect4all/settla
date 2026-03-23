CREATE INDEX IF NOT EXISTS idx_manual_reviews_status_created
  ON manual_reviews (tenant_id, status, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_net_settlements_status_created
  ON net_settlements (tenant_id, status, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_net_settlements_period
  ON net_settlements (status, period_start, period_end);

CREATE INDEX IF NOT EXISTS idx_reconciliation_reports_run_at
  ON reconciliation_reports (run_at DESC);
