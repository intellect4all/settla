// Package recovery implements the Stuck Transfer Detector & Recovery system.
//
// At 50M transactions/day (~580 TPS sustained, 3,000-5,000 TPS peak), some
// transfers will inevitably get stuck due to missed webhooks, provider outages,
// network partitions, or transient infrastructure failures. The recovery
// package provides automated detection and recovery for these stuck transfers.
//
// # Architecture
//
// The Detector runs as a background goroutine on a configurable interval
// (default 60 seconds). Each cycle it queries for transfers stuck in
// non-terminal states (FUNDED, ON_RAMPING, SETTLING, OFF_RAMPING) past
// configurable time thresholds.
//
// Recovery actions are state-specific:
//   - FUNDED: Re-publishes the fund intent (idempotent via outbox dedup).
//   - ON_RAMPING: Queries the on-ramp provider for current status. If the
//     provider reports completed/failed, the engine is notified accordingly.
//   - SETTLING: Queries the blockchain for transaction confirmation status.
//   - OFF_RAMPING: Queries the off-ramp provider for current status.
//
// All recovery actions flow through the Engine (which writes outbox entries),
// preserving the outbox pattern invariant: no direct side effects, everything
// is expressed as outbox intents for workers to execute.
//
// # Escalation
//
// Transfers stuck past the escalation threshold are escalated to manual review
// by creating a record in the manual_reviews table. Escalation is idempotent:
// if a review already exists for the transfer, no duplicate is created.
//
// # Idempotency
//
// The detector is safe to run on multiple replicas concurrently. All recovery
// actions go through the engine, which uses optimistic locking (version checks)
// and outbox deduplication to prevent double-processing.
package recovery
