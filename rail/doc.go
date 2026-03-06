// Package rail implements Settla Rail — the smart payment router, provider
// abstraction layer, and blockchain client integrations.
//
// Sub-packages:
//   - rail/router: smart routing engine that selects the optimal provider
//     for each settlement based on cost, speed, and availability
//   - rail/provider: provider abstraction and concrete implementations
//   - rail/blockchain: blockchain client abstraction for on-chain settlements
package rail
