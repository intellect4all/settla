// Package reconciliation implements Settla's automated reconciliation engine.
//
// The engine runs 5 independent checks to verify system consistency:
//
//  1. Treasury-Ledger Balance Check: compares treasury position balances against
//     ledger account balances for each tenant/currency/location pair. Any mismatch
//     above 0.01 USD equivalent is flagged as a failure.
//
//  2. Transfer State Consistency: identifies transfers stuck in non-terminal states
//     (FUNDED, ON_RAMPING, SETTLING, OFF_RAMPING) beyond configurable time thresholds.
//
//  3. Outbox Health: detects unpublished outbox entries older than a configurable
//     max age (default 5 minutes) and rows leaked into the default partition.
//
//  4. Provider Transaction Reconciliation: counts provider transactions stuck in
//     "pending" status beyond a configurable threshold (default 1 hour).
//
//  5. Daily Volume Sanity: compares today's transaction volume against the 7-day
//     rolling average, warning at 200% and failing at 500%.
//
//  6. Settlement Fee Reconciliation: re-sums FeeBreakdown.TotalFeeUSD from
//     completed transfer rows for the most recent settlement period and compares
//     against net_settlements.total_fees_usd. A difference greater than 0.01 USD
//     is flagged as a failure.
//
// All checks are read-only and produce a reconciliation report stored via [ReportStore].
// The Reconciler orchestrates execution and determines the overall pass/fail status.
package reconciliation
