ALTER TABLE transfers DROP COLUMN IF EXISTS fee_schedule_snapshot;
ALTER TABLE tenants DROP COLUMN IF EXISTS fee_schedule_version;
