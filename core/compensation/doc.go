// Package compensation implements compensation and refund flows for partial
// transfer failures in the Settla settlement engine.
//
// When a transfer fails partway through the pipeline (e.g., on-ramp succeeded
// but off-ramp failed), the system must determine the correct recovery strategy
// based on what steps have already completed:
//
//   - SIMPLE_REFUND: Nothing completed beyond funding — release treasury
//     reservation and reverse any ledger entries. The tenant gets back their
//     full source amount with zero FX loss.
//
//   - REVERSE_ONRAMP: On-ramp completed (fiat converted to stablecoin) but
//     off-ramp failed — sell the stablecoin back to source currency. The tenant
//     bears any FX loss from rate movement between the original conversion and
//     the reversal.
//
//   - CREDIT_STABLECOIN: On-ramp completed but off-ramp failed — credit the
//     tenant's stablecoin position instead of converting back. No FX loss since
//     the tenant keeps the stablecoins.
//
//   - MANUAL_REVIEW: The transfer is in an ambiguous state (e.g., blockchain
//     transaction pending) and cannot be automatically compensated. A human
//     operator must investigate.
//
// All compensation actions flow through the engine's outbox pattern — the
// Executor produces outbox entries for workers to process, never executing
// side effects directly.
package compensation
