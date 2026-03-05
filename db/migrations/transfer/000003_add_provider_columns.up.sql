-- Add on_ramp/off_ramp provider ID columns to transfers
ALTER TABLE transfers ADD COLUMN IF NOT EXISTS on_ramp_provider_id TEXT;
ALTER TABLE transfers ADD COLUMN IF NOT EXISTS off_ramp_provider_id TEXT;
