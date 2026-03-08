#!/usr/bin/env python3
"""
Benchmark Results Parser for Settla

Parses Go benchmark output and compares against performance targets.
Outputs PASS/FAIL for each benchmark with target comparison.

Usage:
    go test ./... -bench=Benchmark -benchmem -benchtime=5s | python3 scripts/parse_benchmarks.py
    make bench | python3 scripts/parse_benchmarks.py
"""

import re
import sys
from dataclasses import dataclass
from typing import Dict, List, Optional, Tuple


@dataclass
class BenchmarkResult:
    name: str
    ns_per_op: Optional[float]
    allocs_per_op: Optional[int]
    bytes_per_op: Optional[int]
    passes: int
    target_ns: Optional[float]
    target_ops_sec: Optional[int]


# Target thresholds for each benchmark
# Format: benchmark_name_pattern -> (target_ns_per_op, min_ops_per_sec, description)
#
# Capacity requirement: 50M txn/day = 5,000 TPS peak.
# At 5,000 TPS, a 100μs operation uses 0.5 CPU-sec/sec — well within budget.
# Targets are set with ~2x headroom above measured values for run-to-run variance.
BENCHMARK_TARGETS: Dict[str, Tuple[Optional[float], Optional[int], str]] = {
    # Domain validation - pure computation with decimal arithmetic
    "BenchmarkValidateEntries": (20_000, None, "<20μs per validation"),
    "BenchmarkValidateEntries_TwoLine": (5_000, None, "<5μs for 2-line"),
    "BenchmarkTransferTransitionTo": (10_000, None, "<10μs per transition"),
    "BenchmarkTransferCanTransitionTo": (200, None, "<200ns per check"),
    "BenchmarkTransferTransition_FullLifecycle": (50_000, None, "<50μs full lifecycle"),
    "BenchmarkPositionAvailable": (1_000, None, "<1μs per calculation"),
    "BenchmarkPositionCanLock": (1_000, None, "<1μs per check"),
    "BenchmarkQuoteIsExpired": (500, None, "<500ns per check"),
    "BenchmarkValidateCurrency": (200, None, "<200ns per validation"),
    "BenchmarkMoneyAdd": (1_000, None, "<1μs per addition"),
    "BenchmarkMoneyMul": (1_000, None, "<1μs per multiplication"),

    # Cache - local should be <1μs, Redis <5ms
    "BenchmarkLocalCacheGet": (1_000, None, "<1μs local lookup"),
    "BenchmarkLocalCacheSet": (200_000, None, "<200μs local write (includes alloc/resize)"),
    "BenchmarkLocalCacheSetOverwrite": (1_000, None, "<1μs overwrite"),
    "BenchmarkLocalCacheGetMiss": (1_000, None, "<1μs miss"),
    "BenchmarkLocalCacheDelete": (1_000, None, "<1μs delete"),
    "BenchmarkRedisGet": (5_000_000, None, "<5ms Redis lookup"),
    "BenchmarkRedisSet": (5_000_000, None, "<5ms Redis write"),
    "BenchmarkRedisSetJSON": (5_000_000, None, "<5ms JSON write"),
    "BenchmarkRedisGetJSON": (5_000_000, None, "<5ms JSON read"),
    "BenchmarkIdempotencyCheckSet": (5_000_000, None, "<5ms check-and-set"),
    "BenchmarkIdempotencyCheckDuplicate": (5_000_000, None, "<5ms duplicate check"),
    "BenchmarkTenantCacheGet": (1_000, None, "<1μs cached tenant"),
    "BenchmarkConcurrentLocalCache": (5_000, None, "<5μs concurrent"),
    "BenchmarkConcurrentRedisCache": (5_000_000, None, "<5ms concurrent Redis"),
    "BenchmarkLocalCache_Get": (1_000, None, "<1μs local get"),
    "BenchmarkLocalCache_Set": (200_000, None, "<200μs local set"),
    "BenchmarkTenantCache_L1Hit": (1_000, None, "<1μs L1 hit"),

    # Treasury - in-memory reservation, proves >100K reserves/sec (20x above 5K TPS needed)
    # NOTE: Specific patterns must come before general ones (startswith matching)
    "BenchmarkReserve_Single": (10_000, None, "<10μs per reserve"),
    "BenchmarkReserve_Concurrent_MultiTenant": (50_000, None, "scales linearly"),
    "BenchmarkReserve_Concurrent": (50_000, None, "<50μs concurrent reserve"),
    "BenchmarkReserveConcurrentContention": (50_000, None, "<50μs under contention"),
    "BenchmarkReserve": (10_000, None, "<10μs per reserve (manager test)"),
    "BenchmarkRelease": (10_000, None, "<10μs per release"),
    "BenchmarkCommitReservation": (5_000, None, "<5μs per commit"),
    "BenchmarkGetPosition": (10_000, None, "<10μs lookup"),
    "BenchmarkFlush": (50_000_000, None, "<50ms for 1000 positions"),
    "BenchmarkGetLiquidityReport": (10_000_000, None, "<10ms for 100 positions"),
    "BenchmarkUpdateBalance": (5_000, None, "<5μs per update"),

    # Router - scoring <50μs, full route <200μs
    # NOTE: Specific patterns before general ones (startswith matching)
    "BenchmarkRouteConcurrent": (200_000, None, "<200μs concurrent route"),
    "BenchmarkRouteLargeAmount": (200_000, None, "<200μs large amount"),
    "BenchmarkRouteSmallAmount": (200_000, None, "<200μs small amount"),
    "BenchmarkRoute_MultiChain": (200_000, None, "<200μs multi-chain"),
    "BenchmarkRoute": (200_000, None, "<200μs per route"),
    "BenchmarkScoreRouteConcurrent": (50_000, None, "<50μs concurrent score"),
    "BenchmarkScoreRouteVariations": (50_000, None, "<50μs score variation"),
    "BenchmarkScoreRoute": (50_000, None, "<50μs per score"),
    "BenchmarkGetQuoteConcurrent": (200_000, None, "<200μs concurrent quote"),
    "BenchmarkGetQuote": (200_000, None, "<200μs per quote"),

    # Core engine - orchestration overhead
    # NOTE: Specific patterns before general ones
    "BenchmarkCreateTransferConcurrent": (200_000, None, "<200μs concurrent create"),
    "BenchmarkCreateTransfer": (200_000, None, "<200μs creation"),
    "BenchmarkFundTransfer": (100_000, None, "<100μs funding"),
    "BenchmarkInitiateOnRamp": (100_000, None, "<100μs on-ramp"),
    "BenchmarkSettleOnChain": (200_000, None, "<200μs settlement"),
    "BenchmarkProcessTransferConcurrent": (500_000, None, "<500μs concurrent pipeline"),
    "BenchmarkProcessTransfer_FullPipeline": (500_000, None, "<500μs full pipeline"),
    "BenchmarkGetTransfer": (10_000, None, "<10μs lookup"),
    "BenchmarkCompleteTransfer": (500_000, None, "<500μs completion"),
    "BenchmarkTransferStateTransition": (100_000, None, "<100μs transition"),
    "BenchmarkEngineWithIdempotency": (200_000, None, "<200μs with idempotency"),
    "BenchmarkListTransfers": (10_000_000, None, "<10ms for 100"),

    # Ledger - TigerBeetle write path (mock TB; real TB achieves 1M+ TPS)
    "BenchmarkPostEntries_Single": (500_000, None, "<500μs per posting"),
    "BenchmarkPostEntries_Batch": (10_000_000, None, "<10ms per batched op (includes batch window)"),
    "BenchmarkGetBalance": (100_000, None, "<100μs lookup"),
    "BenchmarkPostEntries_Concurrent": (10_000_000, None, "<10ms concurrent op (includes batch window)"),
    "BenchmarkPostEntries_MultiLine": (1_000_000, None, "<1ms multi-line"),
    "BenchmarkPostEntries_WithAccountCreation": (1_000_000, None, "<1ms with account creation"),
    "BenchmarkEnsureAccounts": (200_000, None, "<200μs per account"),
    "BenchmarkPostEntries_HighThroughput": (500_000, None, "<500μs per high-throughput op"),
    "BenchmarkGetEntries": (10_000_000, None, "<10ms query"),
    "BenchmarkPostEntriesValidation": (10_000, None, "<10μs per validation"),
    "BenchmarkTBCreateTransfers": (200_000, None, "<200μs batch"),
    "BenchmarkTBLookupAccounts": (100_000, None, "<100μs lookup"),
}


def parse_benchmark_line(line: str) -> Optional[BenchmarkResult]:
    """Parse a Go benchmark output line."""
    # Match pattern: BenchmarkName-8    123456    1234.56 ns/op    1234 B/op    12 allocs/op
    pattern = r'^(Benchmark\S+)\s+(\d+)\s+([\d.]+)\s+ns/op(?:\s+(\d+)\s+B/op)?(?:\s+(\d+)\s+allocs/op)?'
    match = re.match(pattern, line)

    if not match:
        return None

    name = match.group(1)
    passes = int(match.group(2))
    ns_per_op = float(match.group(3))

    bytes_per_op = int(match.group(4)) if match.group(4) else None
    allocs_per_op = int(match.group(5)) if match.group(5) else None

    # Find target for this benchmark
    target_ns = None
    target_ops_sec = None
    for pattern, (t_ns, t_ops, _) in BENCHMARK_TARGETS.items():
        if name.startswith(pattern):
            target_ns = t_ns
            target_ops_sec = t_ops
            break

    return BenchmarkResult(
        name=name,
        ns_per_op=ns_per_op,
        allocs_per_op=allocs_per_op,
        bytes_per_op=bytes_per_op,
        passes=passes,
        target_ns=target_ns,
        target_ops_sec=target_ops_sec
    )


def format_ns(ns: float) -> str:
    """Format nanoseconds in human-readable form."""
    if ns < 1000:
        return f"{ns:.0f}ns"
    elif ns < 1_000_000:
        return f"{ns/1000:.2f}μs"
    elif ns < 1_000_000_000:
        return f"{ns/1_000_000:.2f}ms"
    else:
        return f"{ns/1_000_000_000:.2f}s"


def check_result(result: BenchmarkResult) -> Tuple[bool, str]:
    """Check if benchmark meets target. Returns (passed, message)."""
    if result.target_ns is None and result.target_ops_sec is None:
        return True, "NO TARGET"

    # Check ns/op target
    if result.target_ns is not None:
        if result.ns_per_op <= result.target_ns:
            return True, f"PASS: {format_ns(result.ns_per_op)} <= {format_ns(result.target_ns)}"
        else:
            return False, f"FAIL: {format_ns(result.ns_per_op)} > {format_ns(result.target_ns)}"

    # Check ops/sec target (derived from ns/op)
    if result.target_ops_sec is not None:
        ops_sec = 1_000_000_000 / result.ns_per_op
        if ops_sec >= result.target_ops_sec:
            return True, f"PASS: {ops_sec:,.0f} ops/sec >= {result.target_ops_sec:,} ops/sec"
        else:
            return False, f"FAIL: {ops_sec:,.0f} ops/sec < {result.target_ops_sec:,} ops/sec"

    return True, "UNKNOWN"


def main():
    results: List[BenchmarkResult] = []

    # Read from stdin
    for line in sys.stdin:
        line = line.strip()
        result = parse_benchmark_line(line)
        if result:
            results.append(result)

    if not results:
        print("No benchmark results found in input.")
        print("Usage: go test ./... -bench=Benchmark -benchmem | python3 scripts/parse_benchmarks.py")
        sys.exit(1)

    # Print header
    print("=" * 100)
    print(f"{'Benchmark':<50} {'Result':<50}")
    print("=" * 100)

    passed = 0
    failed = 0
    no_target = 0

    for result in results:
        ok, message = check_result(result)
        status = "PASS" if ok else "FAIL"
        if "NO TARGET" in message:
            status = "INFO"
            no_target += 1
        elif ok:
            passed += 1
        else:
            failed += 1

        print(f"{result.name:<50} {message:<50}")

    print("=" * 100)
    print(f"\nSummary: {passed} passed, {failed} failed, {no_target} no target, {len(results)} total")

    # Print performance summary
    print("\n" + "=" * 100)
    print("PERFORMANCE SUMMARY")
    print("=" * 100)

    # Group by component
    components = {
        "Domain": [r for r in results if r.name.startswith("BenchmarkValidate") or r.name.startswith("BenchmarkTransfer") or r.name.startswith("BenchmarkPosition") or r.name.startswith("BenchmarkQuote") or r.name.startswith("BenchmarkMoney")],
        "Cache": [r for r in results if "Cache" in r.name],
        "Treasury": [r for r in results if "Reserve" in r.name or "Release" in r.name or "Flush" in r.name or "Position" in r.name or "Liquidity" in r.name],
        "Router": [r for r in results if "Route" in r.name or "Quote" in r.name or "Score" in r.name],
        "Core": [r for r in results if "Transfer" in r.name or "Create" in r.name or "Fund" in r.name or "Settle" in r.name or "Complete" in r.name or "Engine" in r.name],
        "Ledger": [r for r in results if "Post" in r.name or "Entries" in r.name or "Balance" in r.name or "TB" in r.name],
    }

    for component, comp_results in components.items():
        if comp_results:
            fastest = min(comp_results, key=lambda r: r.ns_per_op)
            slowest = max(comp_results, key=lambda r: r.ns_per_op)
            avg_ns = sum(r.ns_per_op for r in comp_results) / len(comp_results)

            print(f"\n{component}:")
            print(f"  Fastest: {fastest.name} = {format_ns(fastest.ns_per_op)}")
            print(f"  Slowest: {slowest.name} = {format_ns(slowest.ns_per_op)}")
            print(f"  Average: {format_ns(avg_ns)}")

    print("\n" + "=" * 100)

    if failed > 0:
        print(f"\nFAILED: {failed} benchmarks did not meet targets")
        sys.exit(1)
    else:
        print("\nALL BENCHMARKS PASSED")
        sys.exit(0)


if __name__ == "__main__":
    main()
