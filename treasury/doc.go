// Package treasury implements Settla Treasury — position tracking and
// liquidity management.
//
// It monitors balances across providers, manages funding positions, and
// ensures sufficient liquidity is available for settlement operations.
//
// Key types:
//   - Manager: orchestrates position tracking and rebalancing
//   - Position: a liquidity position at a specific provider or account
package treasury
