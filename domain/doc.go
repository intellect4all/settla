// Package domain defines shared domain types, interfaces, and errors
// used across all Settla modules.
//
// This package contains value objects (Money, Currency), ledger types
// (JournalEntry, EntryLine), transfer aggregate with state machine,
// provider interfaces (OnRampProvider, OffRampProvider, BlockchainClient),
// treasury interface (TreasuryManager), and domain error types.
//
// All tenant-specific types include a TenantID field to enforce strict
// multi-tenancy isolation. Every query that returns tenant data must
// filter by tenant_id.
//
// Dependencies are limited to stdlib + shopspring/decimal + google/uuid.
// No infrastructure imports (DB, HTTP, Redis, NATS, TigerBeetle) are
// allowed in this package.
package domain
