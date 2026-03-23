-- Prevent duplicate net settlements for the same tenant + period.
-- Note: CONCURRENTLY removed because golang-migrate runs migrations in transactions.
CREATE UNIQUE INDEX IF NOT EXISTS uk_net_settlements_tenant_period
ON net_settlements (tenant_id, period_start, period_end);
