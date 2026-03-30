# Chapter 8.3: Chaos Engineering

**Reading time: 30 minutes**

## Learning Objectives

By the end of this chapter, you will be able to:

1. Apply the inject-observe-remove-verify pattern to infrastructure failure scenarios
2. Explain the eight chaos scenarios Settla tests and what invariant each proves
3. Understand how the transactional outbox guarantees data consistency across crashes
4. Read chaos test output and interpret recovery times and affected request percentages
5. Write a new chaos scenario that injects a specific infrastructure failure

---

## Chaos Engineering Principles

Chaos engineering answers a simple question: "When something breaks in production,
does the system lose money?" For settlement infrastructure, the consequences of
a wrong answer are severe -- an unrecoverable ledger imbalance means real money
is unaccounted for.

Settla's chaos framework follows four principles from Netflix's chaos engineering
discipline, adapted for financial systems:

```
1. DEFINE "steady state" as business output
   --> Transfers complete, debits equal credits, outbox drains

2. HYPOTHESIZE that steady state continues during failure
   --> "If Postgres pauses for 10 seconds, no money is lost"

3. INTRODUCE real-world failure events
   --> Kill containers, pause processes, saturate connections

4. VERIFY the hypothesis by measuring the difference
   --> Check outbox drain, ledger balance, stuck transfers
```

### The Three Invariants

Every chaos scenario verifies the same three invariants after recovery:

```
+------------------------------------------------------------------+
| Invariant 1: OUTBOX FULLY DRAINED                                |
|   Every outbox entry (state change + side effect intent)         |
|   must reach published=true. Zero orphaned entries.              |
+------------------------------------------------------------------+
| Invariant 2: MONEY CONSERVATION                                  |
|   sum(entry_lines WHERE entry_type='DEBIT') ==                   |
|   sum(entry_lines WHERE entry_type='CREDIT')                     |
|   Double-entry bookkeeping is never violated.                    |
+------------------------------------------------------------------+
| Invariant 3: NO STUCK TRANSFERS                                  |
|   Zero transfers in non-terminal state (not COMPLETED,           |
|   FAILED, or REFUNDED) after recovery completes.                 |
+------------------------------------------------------------------+
```

---

## The Chaos Framework

The chaos test framework lives in `tests/chaos/` and consists of two files:

```
tests/chaos/
  main.go       -- ChaosRunner, ChaosLoadGenerator, Docker helpers
  scenario.go   -- Individual scenario implementations, verification helpers
```

### Core Types

```go
type ChaosRunner struct {
    config     ChaosConfig
    logger     *slog.Logger
    httpClient *http.Client
    results    []ScenarioResult
}

type ScenarioResult struct {
    Name               string
    Duration           time.Duration
    RequestsAffected   int64
    TotalRequests      int64
    AffectedPercent    float64
    RecoveryTime       time.Duration
    DataConsistent     bool
    LedgerBalanced     bool
    TreasuryConsistent bool
    StuckTransfers     int64
    Passed             bool
    FailReason         string
}
```

### The Inject-Observe-Remove-Verify Pattern

Every scenario follows the same four-step pattern:

```
    +----------+     +----------+     +----------+     +----------+
    |  INJECT  | --> | OBSERVE  | --> |  REMOVE  | --> |  VERIFY  |
    +----------+     +----------+     +----------+     +----------+
    Kill/pause a     Generate load    Restart the      Check three
    container or     while failure    container or     invariants:
    service          is active        unpause          outbox, ledger,
                                                       stuck transfers
```

Here is how this pattern manifests in the actual code:

```go
func (c *ChaosRunner) scenarioOutboxRelayInterruption(ctx context.Context) ScenarioResult {
    // --- SETUP: Generate background load ---
    gen := NewChaosLoadGenerator(c.config, c.logger)
    stopLoad := gen.Start(ctx)
    defer stopLoad()
    time.Sleep(c.config.LoadDuration)  // Warm up

    // --- INJECT: Kill the outbox relay + workers ---
    gen.MarkInjectionPoint()
    c.dockerCompose("kill", "-s", "SIGKILL", "settla-node")

    // --- OBSERVE: Generate load while relay is dead ---
    // Outbox entries accumulate in the DB but are not published
    time.Sleep(15 * time.Second)

    // --- REMOVE: Restart the relay ---
    c.dockerCompose("up", "-d", "settla-node")
    time.Sleep(c.config.RecoveryWait)
    c.ensureHealthy(ctx)

    // --- VERIFY: Check all three invariants ---
    // Invariant 1: Outbox fully drained
    outboxV, err := c.verifyOutboxDrained(ctx, 60*time.Second)
    if err != nil { /* fail */ }

    // Invariant 2: Money conservation
    moneyCheck, _ := c.verifyMoneyConservation(ctx)
    if !moneyCheck.Balanced { /* fail */ }

    // Invariant 3: No stuck transfers
    stuckCheck, err := c.verifyNoStuckTransfers(ctx, 60*time.Second)
    if err != nil { /* fail */ }

    result.Passed = true
    return result
}
```

---

## The Eight Chaos Scenarios

```
+------+-----------------------------------+-----------------------------+
|  #   | Scenario                          | What Breaks                 |
+------+-----------------------------------+-----------------------------+
|  1   | TigerBeetle Restart               | Ledger write authority      |
|  2   | Postgres Pause                    | Read-side database          |
|  3   | NATS Restart                      | Event bus                   |
|  4   | Redis Failure                     | Cache layer                 |
|  5   | Server Crash                      | Core engine                 |
|  6   | PgBouncer Saturation              | Connection pool             |
|  7   | Outbox Relay Interruption         | Event relay                 |
|  8   | Worker Node Restart               | NATS consumers              |
+------+-----------------------------------+-----------------------------+
```

### Scenario 1: TigerBeetle Restart

**Hypothesis:** When TigerBeetle restarts, ledger writes fail temporarily but
resume after recovery, and no money is lost.

```go
func (c *ChaosRunner) scenarioTigerBeetleRestart(ctx context.Context) ScenarioResult {
    // Warm up load
    gen := NewChaosLoadGenerator(c.config, c.logger)
    stopLoad := gen.Start(ctx)

    // INJECT: Restart TigerBeetle
    c.dockerCompose("restart", "tigerbeetle")

    // Wait for recovery (TigerBeetle replays its WAL on restart)
    c.waitForHealthy(ctx, c.config.ServerURL+"/health", 60*time.Second)

    // VERIFY: Money conservation after TigerBeetle restart
    moneyCheck, _ := c.verifyMoneyConservation(ctx)
    if !moneyCheck.Balanced {
        result.FailReason = "money conservation violated after TigerBeetle restart"
    }
}
```

**Why this matters:** TigerBeetle is the ledger write authority. If its restart
causes ledger entries to be lost or duplicated, the entire accounting system
is compromised. TigerBeetle uses its own WAL for crash recovery -- this
scenario verifies that guarantee under load.

### Scenario 2: Postgres Pause

**Hypothesis:** When Postgres pauses for 10 seconds, transfers continue
(TigerBeetle still handles writes), and the read-side catches up after unpause.

```go
func (c *ChaosRunner) scenarioPostgresPause(ctx context.Context) ScenarioResult {
    // INJECT: Pause postgres-ledger (SIGSTOP)
    c.dockerCompose("pause", "postgres-ledger")
    time.Sleep(10 * time.Second)

    // REMOVE: Unpause
    c.dockerCompose("unpause", "postgres-ledger")

    // VERIFY: Outbox drained + money conservation
    outboxV, _ := c.verifyOutboxDrained(ctx, 60*time.Second)
    moneyCheck, _ := c.verifyMoneyConservation(ctx)
}
```

**Key Insight:** `docker compose pause` sends SIGSTOP, which freezes the
process without killing it. All in-flight queries hang. This simulates
the worst-case network partition -- the database appears to be there (TCP
connection is open) but never responds.

### Scenario 3: NATS Restart

**Hypothesis:** When NATS restarts, event delivery pauses but consumers
reconnect automatically, and no events are duplicated.

```go
func (c *ChaosRunner) scenarioNatsRestart(ctx context.Context) ScenarioResult {
    c.dockerCompose("restart", "nats")
    time.Sleep(c.config.RecoveryWait)
    c.ensureHealthy(ctx)

    // After NATS restart the outbox relay reconnects and re-publishes
    // any entries that were in-flight when NATS went down.
    outboxV, _ := c.verifyOutboxDrained(ctx, 60*time.Second)
}
```

**Why NATS restart does not cause duplicates:** The outbox relay marks entries
as `published=true` only after NATS acknowledges receipt. If NATS dies mid-
publish, the entry remains `published=false` and is re-published on reconnect.
NATS JetStream deduplication (via `Nats-Msg-Id` header) drops the duplicate.

### Scenario 4: Redis Failure

**Hypothesis:** Redis is a cache, not an authority. Transfers must continue
working when Redis is completely down.

```go
func (c *ChaosRunner) scenarioRedisFailure(ctx context.Context) ScenarioResult {
    // INJECT: Stop Redis entirely
    c.dockerCompose("stop", "redis")

    // Run with Redis down for 15 seconds
    time.Sleep(15 * time.Second)

    // Verify transfers still work without Redis
    // (auth falls back to DB, rate limiting degrades gracefully)

    // REMOVE: Restart Redis
    c.dockerCompose("start", "redis")
}
```

**Why this works:** Settla's auth cache has three levels: L1 local (in-process)
-> L2 Redis -> L3 DB. When Redis is down, auth falls back to the DB with
slightly higher latency (~5ms instead of ~0.5ms). Rate limiting degrades
gracefully (local counters still work, just without cross-instance sync).

### Scenario 5: Server Crash (SIGKILL)

**Hypothesis:** When settla-server is killed (simulating OOM kill), the
system recovers without data loss.

```go
func (c *ChaosRunner) scenarioServerCrash(ctx context.Context) ScenarioResult {
    // INJECT: SIGKILL (no graceful shutdown, no treasury flush)
    c.dockerCompose("kill", "-s", "SIGKILL", "settla-server")
    time.Sleep(5 * time.Second)

    // REMOVE: Restart
    c.dockerCompose("up", "-d", "settla-server")
    c.waitForHealthy(ctx, c.config.ServerURL+"/health", 60*time.Second)

    // VERIFY: Treasury positions reconstruct from DB + reserve_ops
    // (crash recovery replays uncommitted ops)
}
```

**Key Insight:** SIGKILL bypasses the graceful shutdown handler. The treasury
flush goroutine does not get a chance to write to Postgres. On restart, the
treasury manager calls `LoadPositions()` which reads the last-flushed state
from Postgres, then replays any uncommitted reserve operations from the
`reserve_ops` table. This is the crash recovery mechanism tested in
`TestCrashRecoveryWithReserveOps`.

### Scenario 6: PgBouncer Saturation

**Hypothesis:** When PgBouncer reaches its connection limit, requests queue
but eventually succeed. No crashes, no data loss.

```go
func (c *ChaosRunner) scenarioPgBouncerSaturation(ctx context.Context) ScenarioResult {
    // Use 2000 TPS to saturate connection pool
    highLoadConfig := c.config
    highLoadConfig.LoadTPS = 2000

    gen := NewChaosLoadGenerator(highLoadConfig, c.logger)
    stopLoad := gen.Start(ctx)

    // Run for 90 seconds at saturating load
    time.Sleep(90 * time.Second)
    stopLoad()

    // VERIFY: System is still healthy, outbox drained
    c.ensureHealthy(ctx)
    c.verifyOutboxDrained(ctx, 60*time.Second)
}
```

### Scenario 7: Outbox Relay Interruption

This is the most important chaos scenario because it tests the core
architectural invariant: the transactional outbox.

```
Normal flow:           Relay crash:            After restart:
+--------+            +--------+              +--------+
| Engine |            | Engine |              | Engine |
+---+----+            +---+----+              +---+----+
    |                     |                       |
    v                     v                       v
+--------+            +--------+              +--------+
| Outbox | --relay--> | Outbox | (entries     | Outbox | --relay--> NATS
| (DB)   |    |       | (DB)   |  accumulate) | (DB)   |    |
+--------+    |       +--------+              +--------+    |
              v           X                                 v
           NATS       relay is dead                      NATS
```

The scenario kills the relay, generates load for 15 seconds (outbox entries
accumulate), restarts the relay, and verifies that every entry is published.

### Scenario 8: Worker Node Restart

**Hypothesis:** When a worker node restarts, NATS redelivers any un-acked
messages, and idempotency keys prevent double execution.

```go
func (c *ChaosRunner) scenarioWorkerNodeRestart(ctx context.Context) ScenarioResult {
    // INJECT: Graceful restart (SIGTERM -> drain -> stop -> start)
    c.dockerCompose("restart", "settla-node")

    // VERIFY after recovery:
    // 1. Outbox fully drained (no messages stuck)
    // 2. Money conservation (no double-posting from redelivered messages)
    // 3. No stuck transfers
}
```

**Why redelivery does not cause double-execution:** Every worker uses the
CHECK-BEFORE-CALL pattern. Before calling an external system (provider,
blockchain), it checks the `provider_transactions` table for the transfer_id.
If a record exists, the call was already made -- the worker skips execution
and acks the NATS message.

---

## Verification Helpers

### Outbox Drain Verification

```go
func (c *ChaosRunner) verifyOutboxDrained(ctx context.Context, timeout time.Duration) (outboxVerification, error) {
    deadline := time.Now().Add(timeout)

    for time.Now().Before(deadline) {
        totalOut, _ := c.dockerExec("postgres-transfer",
            "psql", "-U", "settla", "-d", "settla_transfer", "-t", "-c",
            "SELECT COUNT(*) FROM outbox;")

        unpubOut, _ := c.dockerExec("postgres-transfer",
            "psql", "-U", "settla", "-d", "settla_transfer", "-t", "-c",
            "SELECT COUNT(*) FROM outbox WHERE published = false AND retry_count < max_retries;")

        total := parseCountOutput(totalOut)
        unpub := parseCountOutput(unpubOut)

        if unpub == 0 {
            return outboxVerification{Drained: true, TotalEntries: total}, nil
        }

        time.Sleep(2 * time.Second)
    }

    return outboxVerification{Drained: false}, fmt.Errorf("outbox not fully drained")
}
```

### Money Conservation Verification

```go
func (c *ChaosRunner) verifyMoneyConservation(ctx context.Context) (moneyConservationCheck, error) {
    out, err := c.dockerExec("postgres-ledger",
        "psql", "-U", "settla", "-d", "settla_ledger", "-t", "-c",
        `SELECT
          COALESCE(SUM(CASE WHEN entry_type = 'DEBIT'  THEN amount ELSE 0 END), 0),
          COALESCE(SUM(CASE WHEN entry_type = 'CREDIT' THEN amount ELSE 0 END), 0)
        FROM entry_lines;`)

    debits, credits := parseDebitCreditRow(out)
    balanced := debits == credits

    return moneyConservationCheck{
        TotalDebits: debits, TotalCredits: credits, Balanced: balanced,
    }, nil
}
```

---

## Running Chaos Tests

The `make chaos` target runs all eight scenarios sequentially:

```bash
make chaos
# Equivalent to:
go run ./tests/chaos/ -gateway=http://localhost:3100
```

Each scenario generates background load, injects the failure, observes behavior,
restores the system, and verifies all three invariants (outbox drained, money
conservation, no stuck transfers). The full suite takes approximately 15-20
minutes.

### Output Format

```
+==============================================================+
|              S E T T L A   C H A O S   T E S T S             |
|        Proving recovery from infrastructure failures          |
+==============================================================+

  Scenario 1/8: TigerBeetle Restart
  PASS  TigerBeetle Restart  (duration: 2m15s, recovery: 45s)

  Scenario 2/8: Postgres Pause
  PASS  Postgres Pause       (duration: 1m30s, recovery: 12s)

  ...

  CHAOS TEST SUMMARY
  PASS  TigerBeetle Restart         duration=2m15s  recovery=45s   affected=3.2%
  PASS  Postgres Pause              duration=1m30s  recovery=12s   affected=1.1%
  PASS  NATS Restart                duration=1m45s  recovery=20s   affected=2.5%
  PASS  Redis Failure               duration=1m30s  recovery=5s    affected=0.1%
  PASS  Server Crash                duration=2m00s  recovery=35s   affected=4.0%
  PASS  PgBouncer Saturation        duration=2m30s  recovery=0s    affected=5.2%
  PASS  Outbox Relay Interruption   duration=2m00s  recovery=25s   affected=2.8%
  PASS  Worker Node Restart         duration=1m45s  recovery=15s   affected=1.5%

  8 passed, 0 failed out of 8 scenarios

  ALL SCENARIOS PASSED -- system recovers gracefully from failures
```

---

## Common Mistakes

### Mistake 1: Not Waiting for Health Before Verification

```go
// BAD: Verify immediately after restart
c.dockerCompose("up", "-d", "settla-node")
c.verifyOutboxDrained(ctx, 60*time.Second)  // May fail: node not ready yet

// GOOD: Wait for healthy, then verify
c.dockerCompose("up", "-d", "settla-node")
c.ensureHealthy(ctx)
c.verifyOutboxDrained(ctx, 60*time.Second)
```

### Mistake 2: Using SIGTERM Instead of SIGKILL for Crash Scenarios

SIGTERM allows graceful shutdown. To simulate a true crash (OOM kill, hardware
failure), use SIGKILL which bypasses all cleanup handlers.

### Mistake 3: Not Generating Load During the Failure

Chaos tests must generate load while the failure is active. A test that kills
a service, waits, restarts it, then generates load is testing startup behavior,
not failure recovery.

---

## Exercises

1. **Add a network partition scenario.** Use `docker network disconnect` to
   isolate settla-server from Postgres for 15 seconds. Verify that treasury
   flush fails gracefully and resumes after reconnection.

2. **Test cascading failures.** Kill Redis AND pause Postgres simultaneously.
   Does the system recover when both are restored?

3. **Measure blast radius.** For each scenario, calculate the percentage of
   affected requests. Which failure has the largest blast radius? Why?

---

## What's Next

With chaos tests proving the system recovers from failures, Chapter 8.4 covers
the observability infrastructure that lets you detect failures in production
before they become outages -- Prometheus metrics, SLI/SLO alerting, and
structured logging.
