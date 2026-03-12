// Package maintenance provides disk and partition management for Settla databases
// running at 50M transactions/day scale.
//
// At this volume, the system generates approximately:
//   - 50M+ outbox rows/day (processed and discarded within 48 hours)
//   - 50M transfer rows/day (monthly partitions)
//   - 50M+ transfer event rows/day (monthly partitions)
//   - 200-250M ledger entry lines/day (weekly partitions)
//
// Without active partition management, tables would grow unbounded and queries
// would degrade. This package provides three key managers:
//
//   - PartitionManager: creates future partitions ahead of time and drops old outbox
//     partitions. Uses CREATE TABLE IF NOT EXISTS / DROP TABLE IF EXISTS for
//     idempotency. NEVER uses DELETE for bulk cleanup — always DROP PARTITION,
//     which is instant regardless of row count.
//
//   - VacuumManager: runs VACUUM ANALYZE on hot tables at appropriate intervals.
//     NEVER uses VACUUM FULL, which blocks all operations on the table.
//
//   - CapacityMonitor: tracks database size, growth rates, and alerts when
//     thresholds are crossed (70% warn, 85% critical). Exposes metrics as
//     Prometheus gauges.
package maintenance
