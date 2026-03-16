-- Step 1: Create new partitioned table
CREATE TABLE provider_transactions_partitioned (
    LIKE provider_transactions INCLUDING DEFAULTS INCLUDING CONSTRAINTS
) PARTITION BY RANGE (created_at);

-- Step 2: Create default partition for existing data
CREATE TABLE provider_transactions_default PARTITION OF provider_transactions_partitioned DEFAULT;

-- Step 3: Create monthly partitions for next 6 months
DO $$
DECLARE
    start_date DATE := DATE_TRUNC('month', CURRENT_DATE);
    partition_date DATE;
    partition_name TEXT;
BEGIN
    FOR i IN 0..5 LOOP
        partition_date := start_date + (i || ' months')::INTERVAL;
        partition_name := 'provider_transactions_' || TO_CHAR(partition_date, 'YYYY_MM');
        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS %I PARTITION OF provider_transactions_partitioned
             FOR VALUES FROM (%L) TO (%L)',
            partition_name,
            partition_date,
            partition_date + INTERVAL '1 month'
        );
    END LOOP;
END $$;

-- Step 4: Copy data and swap tables
INSERT INTO provider_transactions_partitioned SELECT * FROM provider_transactions;
ALTER TABLE provider_transactions RENAME TO provider_transactions_old;
ALTER TABLE provider_transactions_partitioned RENAME TO provider_transactions;

-- Step 5: Recreate indexes on partitioned table
CREATE INDEX IF NOT EXISTS idx_provider_txns_external_id ON provider_transactions(external_id) WHERE external_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uk_provider_txns_provider_external_id ON provider_transactions(provider, external_id);
