# ADR-010: Decimal-Only Monetary Math

**Status:** Accepted
**Date:** 2026-03-08
**Authors:** Engineering Team

## Context

Settla is a settlement engine. Every operation — quoting, fee calculation, ledger posting, treasury reservation — involves monetary arithmetic. The correctness of this arithmetic is not a performance optimization or a nice-to-have; it is a fundamental requirement. A rounding error in a settlement engine means real money goes to the wrong place.

The threshold that makes this non-negotiable:

- **IEEE 754 float64 has ~15-17 significant decimal digits of precision.** A transfer of $999,999.99 converted at 1,550.00 NGN/USD produces 1,549,999,984.50 NGN — a 15-digit number. This is at the edge of float64 precision. Add a fee calculation (e.g., 40 basis points: multiply by 0.004) and intermediate results can exceed 15 significant digits, introducing rounding errors.
- **Concrete example**: `999999.99 * 1550.0` in float64 yields `1549999984.4999998` instead of `1549999984.50`. That 0.0000002 NGN error compounds across 50M transactions/day. Over a month: 50M x 30 x $0.0000002 = $300 in cumulative drift. Small per transaction, but unacceptable for a settlement engine where every cent must be accounted for.
- **Balanced posting invariant**: Every ledger entry must satisfy `sum(debits) == sum(credits)` exactly. Float arithmetic cannot guarantee this — `0.1 + 0.2 != 0.3` in IEEE 754. A "balanced" entry that is off by 1 ULP (unit in the last place) violates the fundamental accounting equation.

We needed to decide between:

1. **Integer arithmetic in minor units** — store cents/kobo as int64, convert at display
2. **Arbitrary-precision decimal** — use a decimal library that performs exact base-10 arithmetic
3. **Float64 with rounding** — use float64 and round at boundaries

## Decision

We chose **arbitrary-precision decimal libraries** for ALL monetary amounts:

- **Go**: `shopspring/decimal` — the `domain.Money` type wraps `decimal.Decimal` for amount fields
- **TypeScript**: `decimal.js` — used in the gateway and webhook services for any monetary computation

**The rule is absolute: never use float/float64/number for money.** This is enforced by:

1. **Type system**: `domain.Money` contains a `decimal.Decimal`, not a `float64`. Functions that accept monetary amounts take `Money` or `decimal.Decimal`, never `float64`.
2. **Code review**: Any PR that introduces `float64` (Go) or bare `number` (TypeScript) for a monetary value is rejected.
3. **Proto definitions**: Monetary amounts in Protocol Buffers use `string` representation (not `double`), parsed into decimal types on both sides.

All monetary operations — addition, subtraction, multiplication by rates, fee calculations, FX conversions — use decimal arithmetic. Rounding is explicit and happens only at defined boundaries (e.g., rounding to currency's minor unit precision after FX conversion), never implicitly via floating-point truncation.

## Consequences

### Benefits
- **Exact arithmetic**: `0.1 + 0.2 == 0.3` is true. Ledger entries balance exactly, not approximately.
- **Auditability**: Every intermediate calculation produces a reproducible, exact result. Two systems computing the same fee on the same amount will always agree, regardless of platform or compiler.
- **Regulatory compliance**: Financial regulations require exact accounting. "Close enough" is not acceptable for settlement systems. Decimal arithmetic satisfies audit requirements without epsilon-comparison workarounds.
- **Composability**: Fee calculations, FX conversions, and ledger postings can be chained without accumulating rounding drift. The result after 10 operations is as precise as after 1.

### Trade-offs
- **~10x slower than float64**: Decimal multiplication is roughly 10x slower than float64 multiplication on modern CPUs. A float64 multiply takes ~1ns; a `shopspring/decimal` multiply takes ~10-15ns.
- **Higher memory per value**: A `decimal.Decimal` is 40+ bytes on the heap (big.Int + exponent); a `float64` is 8 bytes on the stack. At 50M transactions/day with ~4 monetary fields per transaction, this is ~8GB additional heap allocation per day compared to float64.
- **String serialization overhead**: Monetary amounts travel as strings in protobuf and JSON (`"1549999984.50"` instead of the 8-byte double `1549999984.5`). This increases payload sizes by ~10-15 bytes per monetary field.

### Mitigations
- **Compute is not the bottleneck**: At 580 TPS sustained, the settlement engine performs ~2,300 decimal operations per second (4 per transfer). At 15ns each, that is ~35 microseconds of CPU time per second — utterly negligible. The bottleneck is I/O (database writes, network), not arithmetic.
- **Memory is cheap**: The additional 8GB/day of heap allocation is handled by Go's garbage collector without measurable GC pause impact. The working set at any moment is far smaller (in-flight transfers only).
- **Correctness is non-negotiable**: For a settlement engine moving real money between financial institutions, the cost of a rounding error (reconciliation failures, regulatory findings, customer disputes) vastly exceeds the cost of slightly slower arithmetic.

## References

- [What Every Computer Scientist Should Know About Floating-Point Arithmetic](https://docs.oracle.com/cd/E19957-01/806-3568/ncg_goldberg.html) — David Goldberg
- [shopspring/decimal](https://github.com/shopspring/decimal) — Arbitrary-precision fixed-point decimal for Go
- [decimal.js](https://mikemcl.github.io/decimal.js/) — Arbitrary-precision Decimal type for JavaScript
