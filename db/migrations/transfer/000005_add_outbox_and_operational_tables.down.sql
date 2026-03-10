-- Reverse consolidated migration: drop all new tables, revert enums.

ALTER TABLE tenants DROP COLUMN IF EXISTS webhook_events;
DROP TABLE IF EXISTS webhook_event_subscriptions;
DROP TABLE IF EXISTS webhook_deliveries;
DROP TABLE IF EXISTS reconciliation_reports CASCADE;
DROP TABLE IF EXISTS net_settlements CASCADE;
DROP TABLE IF EXISTS compensation_records CASCADE;
DROP TABLE IF EXISTS manual_reviews CASCADE;
DROP TABLE IF EXISTS outbox CASCADE;

-- Revert transfers.status back to TEXT with CHECK
ALTER TABLE transfers ALTER COLUMN status DROP DEFAULT;
ALTER TABLE transfers
  ALTER COLUMN status TYPE TEXT
  USING status::TEXT;
ALTER TABLE transfers ALTER COLUMN status SET DEFAULT 'CREATED';
ALTER TABLE transfers
  ADD CONSTRAINT transfers_status_check CHECK (status IN (
    'CREATED','FUNDED','ON_RAMPING','SETTLING','OFF_RAMPING',
    'COMPLETED','FAILED','REFUNDING','REFUNDED'
  ));

-- Revert provider_transactions.tx_type back to TEXT with CHECK
ALTER TABLE provider_transactions
  ALTER COLUMN tx_type TYPE TEXT
  USING tx_type::TEXT;
ALTER TABLE provider_transactions
  ADD CONSTRAINT provider_transactions_tx_type_check CHECK (tx_type IN (
    'ON_RAMP','OFF_RAMP','BLOCKCHAIN'
  ));

DROP TYPE IF EXISTS transfer_status_enum;
DROP TYPE IF EXISTS compensation_strategy_enum;
DROP TYPE IF EXISTS review_status_enum;
DROP TYPE IF EXISTS provider_tx_type_enum;
