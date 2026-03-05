-- Reverse of 000001_create_ledger.up.sql
-- Drop in reverse dependency order.
-- Partitioned child tables are dropped automatically with their parent.

DROP TABLE IF EXISTS balance_snapshots;
DROP TABLE IF EXISTS entry_lines;
DROP TABLE IF EXISTS journal_entries;
DROP TABLE IF EXISTS accounts;
