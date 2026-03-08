# ADR-011: Per-Tenant Fee Schedules

**Status:** Accepted
**Date:** 2026-03-08
**Authors:** Engineering Team

## Context

Settla is a B2B settlement platform. Each tenant is a fintech company (Lemfi, Fincra, Paystack, etc.) that processes payments through Settla's infrastructure. Unlike a B2C product with a single pricing page, B2B settlement pricing is individually negotiated:

- **Volume-based negotiation**: A tenant processing $100M/month negotiates lower rates than one processing $1M/month. Lemfi's negotiated rate (40 bps send / 35 bps receive) differs from Fincra's (25 bps send / 20 bps receive) because of volume commitments and partnership terms.
- **Corridor-specific pricing**: Fees vary by payment corridor (GBP->NGN may have different margins than USD->NGN) due to underlying provider costs, liquidity depth, and regulatory overhead per corridor.
- **Competitive pressure**: If a fintech can get 30 bps from a competitor, Settla must be able to offer 25 bps to that specific tenant without affecting pricing for others. A flat-rate model would either overprice small tenants or underprice large ones.

The threshold: **any pricing model that applies the same fee to all tenants is commercially non-viable for a B2B settlement platform.** Each fintech evaluates Settla against competitors on a per-basis-point level.

## Decision

We chose **per-tenant fee schedules stored in basis points (bps)** in the tenant record within Transfer DB.

Each tenant has a fee schedule with:
- **Send fee (bps)**: Applied to outbound transfers (e.g., 40 bps = 0.40%)
- **Receive fee (bps)**: Applied to inbound settlements (e.g., 35 bps = 0.35%)

Fee application happens at the **router layer** via `CoreRouterAdapter`:

1. The gateway sends a quote or transfer request with the authenticated `tenant_id`
2. The core engine calls `router.GetQuote(ctx, tenantID, request)`
3. `CoreRouterAdapter` calls the domain router to get the provider's raw quote (exchange rate, provider fee)
4. The adapter then loads the tenant's fee schedule and applies the tenant-specific fee on top of the provider cost
5. The final quote returned to the tenant includes: provider rate + provider fee + Settla fee (tenant-specific bps)

Fee amounts are calculated using decimal arithmetic (see ADR-010) and recorded as explicit line items in the ledger entry, ensuring full auditability.

Example for a $10,000 GBP->NGN transfer:
- **Lemfi** (40 bps send): Settla fee = $10,000 x 0.0040 = $40.00
- **Fincra** (25 bps send): Settla fee = $10,000 x 0.0025 = $25.00

Both use the same provider quote and exchange rate; only the Settla margin differs.

## Consequences

### Benefits
- **Commercial flexibility**: Sales can negotiate rates per fintech without engineering involvement. Changing a tenant's fee from 40 bps to 35 bps is a database update, not a code change.
- **Full auditability**: Every transfer's ledger entry records the exact fee amount, the bps rate applied, and the tenant it was charged to. Fee disputes are resolved by querying the ledger.
- **Tenant isolation**: Tenant A's fee schedule is completely independent of Tenant B's. A fee change for one tenant has zero impact on any other tenant's pricing.
- **Simple mental model**: Basis points are the universal language of financial services. "40 bps" is immediately understood by everyone from engineers to finance teams to clients.
- **Composable with routing**: The fee is applied after provider selection, so the smart router's cost/speed/liquidity scoring is independent of tenant-specific pricing. The router optimizes for the best provider; the fee layer adds the margin on top.

### Trade-offs
- **Fee changes require tenant record updates**: There is no self-service fee management or real-time fee negotiation. Changes go through a manual process (update tenant record in Transfer DB). This is acceptable because B2B fee negotiations happen quarterly or annually, not in real-time.
- **No tiered or volume-based automatic discounts**: The current model is flat bps per tenant. If a tenant's volume grows and they qualify for lower rates, someone must manually update the fee schedule. Tiered pricing (e.g., "25 bps for first $10M/month, 20 bps after") would require schema and code changes.
- **No corridor-specific fees yet**: The current implementation applies the same bps to all corridors for a given tenant. Per-corridor fee differentiation (e.g., higher margin on GBP->NGN than USD->NGN) would require extending the fee schedule schema.

### Mitigations
- **Fee schedule is a simple record**: Adding corridor-specific fees or tiered pricing later means extending the fee schedule JSON/columns in the tenant record and updating `CoreRouterAdapter`. The architecture supports this — the adapter already has access to the full transfer context (amount, corridor, currency pair).
- **Tenant cache keeps fee lookups fast**: The tenant record (including fee schedule) is cached in the two-level cache (L1 local 30s, L2 Redis 5min). At 5,000 TPS, fee schedule lookups add ~100ns (L1 hit), not a database round-trip.
- **Audit trail via ledger**: Every fee charged is recorded as a separate posting in the ledger entry. Even if fee schedules change, historical transfers retain the fee that was actually applied.
