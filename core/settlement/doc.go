// Package settlement implements net settlement calculation for tenants using the
// NET_SETTLEMENT model.
//
// Instead of settling each transfer individually (as in the PREFUNDED model),
// net settlement aggregates all completed transfers over a period (typically one day)
// and produces a single settlement instruction per currency pair per tenant.
//
// For example, if Fincra processed 10,000 GBP->NGN transfers and 3,000 NGN->GBP
// transfers in a day, the net settlement calculator computes:
//   - The net NGN owed by one party to the other
//   - The net GBP owed by one party to the other
//   - Total fees in USD
//
// This dramatically reduces the number of actual settlement transactions from
// thousands per day to a handful of net positions.
//
// The Scheduler runs daily at 00:30 UTC, calculating the previous day's net
// settlement for all NET_SETTLEMENT tenants. It also tracks overdue payments
// with escalating actions: reminder at 3 days, warning at 5 days, suspend at 7 days.
package settlement
