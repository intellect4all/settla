#!/usr/bin/env python3
"""
Benchmark Comparison Script for Settla CI

Compares current benchmark results against a JSON baseline.
Exits 1 if any metric regresses by more than 20%.

Usage:
    # Compare against baseline (CI mode)
    python3 scripts/compare_benchmarks.py tests/benchmarks/baseline.json bench-results.txt

    # Update baseline from current results
    python3 scripts/compare_benchmarks.py --update-baseline tests/benchmarks/baseline.json bench-results.txt
"""

import json
import re
import sys
from pathlib import Path
from typing import Dict, Optional, Tuple


def parse_bench_results(filepath: str) -> Dict[str, float]:
    """Parse Go benchmark output and return benchmark_name -> ns/op mapping."""
    results = {}
    pattern = r'^(Benchmark\S+?)(?:-\d+)?\s+\d+\s+([\d.]+)\s+ns/op'

    with open(filepath) as f:
        for line in f:
            match = re.match(pattern, line.strip())
            if match:
                name = match.group(1)
                ns_per_op = float(match.group(2))
                results[name] = ns_per_op

    return results


def format_ns(ns: float) -> str:
    """Format nanoseconds in human-readable form."""
    if ns < 1000:
        return f"{ns:.0f}ns"
    elif ns < 1_000_000:
        return f"{ns / 1000:.2f}us"
    elif ns < 1_000_000_000:
        return f"{ns / 1_000_000:.2f}ms"
    else:
        return f"{ns / 1_000_000_000:.2f}s"


def compare(
    baseline: Dict[str, float], current: Dict[str, float],
    warn_threshold: float = 0.10, fail_threshold: float = 0.20
) -> Tuple[bool, str]:
    """Compare current results against baseline.

    Returns (all_passed, summary_text).
    - WARN:    current ns/op > baseline * (1 + warn_threshold)  [10% default]
    - REGRESS: current ns/op > baseline * (1 + fail_threshold)  [20% default, exits 1]
    """
    lines = []
    regressions = 0
    warnings = 0
    improvements = 0
    unchanged = 0

    lines.append(f"{'Benchmark':<55} {'Baseline':>12} {'Current':>12} {'Change':>10} {'Status':>8}")
    lines.append("-" * 100)

    # Compare benchmarks present in both baseline and current
    common_keys = sorted(set(baseline.keys()) & set(current.keys()))

    for name in common_keys:
        base_ns = baseline[name]
        curr_ns = current[name]

        if base_ns == 0:
            change_pct = 0.0
        else:
            change_pct = (curr_ns - base_ns) / base_ns

        if change_pct > fail_threshold:
            status = "REGRESS"
            regressions += 1
        elif change_pct > warn_threshold:
            status = "WARN"
            warnings += 1
        elif change_pct < -0.05:
            status = "FASTER"
            improvements += 1
        else:
            status = "OK"
            unchanged += 1

        lines.append(
            f"{name:<55} {format_ns(base_ns):>12} {format_ns(curr_ns):>12} "
            f"{change_pct:>+9.1%} {status:>8}"
        )

    # New benchmarks (in current but not baseline)
    new_keys = sorted(set(current.keys()) - set(baseline.keys()))
    if new_keys:
        lines.append("")
        lines.append("New benchmarks (no baseline):")
        for name in new_keys:
            lines.append(f"  {name:<55} {format_ns(current[name]):>12}")

    # Missing benchmarks (in baseline but not current)
    missing_keys = sorted(set(baseline.keys()) - set(current.keys()))
    if missing_keys:
        lines.append("")
        lines.append("Missing benchmarks (in baseline but not in current run):")
        for name in missing_keys:
            lines.append(f"  {name}")

    lines.append("")
    lines.append(f"Summary: {len(common_keys)} compared, {improvements} faster, "
                 f"{unchanged} unchanged, {warnings} warnings (>{warn_threshold:.0%}), "
                 f"{regressions} regressions (>{fail_threshold:.0%})")

    passed = regressions == 0
    if not passed:
        lines.append(f"\nFAILED: {regressions} benchmark(s) regressed by more than {fail_threshold:.0%}")
    elif warnings > 0:
        lines.append(f"\nPASSED WITH WARNINGS: {warnings} benchmark(s) slowed by {warn_threshold:.0%}-{fail_threshold:.0%}")
    else:
        lines.append("\nPASSED: No significant regressions detected")

    return passed, "\n".join(lines)


def update_baseline(baseline_path: str, current: Dict[str, float]) -> None:
    """Write current results as the new baseline."""
    Path(baseline_path).parent.mkdir(parents=True, exist_ok=True)
    with open(baseline_path, "w") as f:
        json.dump(current, f, indent=2, sort_keys=True)
    print(f"Baseline updated at {baseline_path} with {len(current)} benchmarks")


def main():
    args = sys.argv[1:]

    if "--update-baseline" in args:
        args.remove("--update-baseline")
        if len(args) != 2:
            print("Usage: compare_benchmarks.py --update-baseline <baseline.json> <bench-results.txt>")
            sys.exit(1)
        baseline_path, results_path = args
        current = parse_bench_results(results_path)
        if not current:
            print("ERROR: No benchmark results parsed from", results_path)
            sys.exit(1)
        update_baseline(baseline_path, current)
        sys.exit(0)

    if len(args) != 2:
        print("Usage: compare_benchmarks.py <baseline.json> <bench-results.txt>")
        print("       compare_benchmarks.py --update-baseline <baseline.json> <bench-results.txt>")
        sys.exit(1)

    baseline_path, results_path = args

    # Load baseline
    try:
        with open(baseline_path) as f:
            baseline = json.load(f)
    except FileNotFoundError:
        print(f"Baseline file not found: {baseline_path}")
        print("Run with --update-baseline to create it from current results")
        sys.exit(1)

    # Parse current results
    current = parse_bench_results(results_path)
    if not current:
        print("ERROR: No benchmark results parsed from", results_path)
        sys.exit(1)

    # Compare
    passed, summary = compare(baseline, current)
    print(summary)
    sys.exit(0 if passed else 1)


if __name__ == "__main__":
    main()
