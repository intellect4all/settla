ALTER TABLE tenants ADD COLUMN IF NOT EXISTS fee_schedule_version INT NOT NULL DEFAULT 1;
ALTER TABLE transfers ADD COLUMN IF NOT EXISTS fee_schedule_snapshot JSONB;
