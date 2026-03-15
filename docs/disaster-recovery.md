# Settla Disaster Recovery Plan

## Overview

This document defines the disaster recovery (DR) strategy for Settla's settlement infrastructure. The design principle is: **no single failure should cause data loss or prolonged downtime**. Stateless services recover automatically; stateful services use replication and snapshots.

---

## RPO/RTO Summary

| Component | RPO (Data Loss) | RTO (Recovery Time) | Replication | Backup |
|-----------|-----------------|---------------------|-------------|--------|
| TigerBeetle | 0 (synchronous replication) | < 5 min | 3-node cluster (production) | Hourly snapshots to S3 |
| PostgreSQL (Ledger) | 0 (Patroni sync replication) | < 10 sec | Patroni 3-node cluster (auto-failover) | Daily full + continuous WAL archive |
| PostgreSQL (Transfer) | 0 (Patroni sync replication) | < 10 sec | Patroni 3-node cluster (auto-failover) | Daily full + continuous WAL archive |
| PostgreSQL (Treasury) | 0 (Patroni sync replication) | < 10 sec | Patroni 3-node cluster (auto-failover) | Daily full + continuous WAL archive |
| Redis | N/A (cache, reconstructible) | < 10 sec | Redis Sentinel (3 replicas + 3 sentinels, auto-failover) | None (cache only) |
| NATS JetStream | 0 (R=3 replication) | < 2 min | 3-node cluster, R=3 streams | Stream snapshots |
| settla-server | N/A (stateless*) | < 30 sec | 6+ replicas across 3 AZs | N/A |
| settla-node | N/A (stateless*) | < 30 sec | 8 instances | N/A |
| Gateway | N/A (stateless) | < 30 sec | 4+ replicas across 3 AZs | N/A |

*settla-server holds in-memory treasury positions; these are reconstructible from the treasury DB within seconds on restart.

---

## Failure Scenarios

### Scenario 1: Single Pod Crash

**Trigger:** OOM kill, application panic, node eviction.

**Impact:** Minimal. Other replicas absorb traffic immediately.

**Recovery:** Automatic.

| Step | Action | Time |
|------|--------|------|
| 1 | Kubernetes detects pod failure via liveness probe (gRPC :9090 for settla-server) | 15 sec |
| 2 | Pod removed from Service endpoints; traffic routed to healthy pods | Immediate |
| 3 | Replacement pod scheduled by Deployment/StatefulSet controller | 5--10 sec |
| 4 | New pod passes readiness probe, receives traffic | 10--15 sec |
| **Total** | | **< 45 sec** |

**Data loss:** Zero. The preStop hook (`sleep 15`) ensures in-flight requests drain and treasury positions flush before termination.

**Protection mechanisms:**
- PodDisruptionBudget: `minAvailable: 4` for settla-server (see `deploy/k8s/base/settla-server/pdb.yaml`)
- TopologySpreadConstraints ensure pods distributed across AZs (see `deploy/k8s/overlays/production/patches/production-topology.yaml`)
- HPA maintains minimum 4 replicas, scales to 12 (see `deploy/k8s/base/settla-server/hpa.yaml`)

---

### Scenario 2: Single Database Failure (PostgreSQL Primary)

**Trigger:** Primary Postgres becomes unresponsive (disk failure, OOM, network partition).

**Impact:** Write operations to the affected bounded context fail. Read replicas continue serving read traffic. Other bounded contexts are unaffected (separate databases).

**Recovery:** Automatic via Patroni.

Each bounded context database (ledger, transfer, treasury) runs a 3-node Patroni cluster with synchronous replication. Patroni handles leader election via Kubernetes endpoints and promotes a synchronous replica automatically when the primary fails.

| Step | Action | Time |
|------|--------|------|
| 1 | Patroni detects primary failure (health check interval: 2s, TTL: 10s) | 6--10 sec |
| 2 | Patroni promotes synchronous replica to leader | < 2 sec |
| 3 | Kubernetes Service selector routes to new leader automatically | Immediate |
| 4 | AlertManager fires `SettlaPostgresFailover` alert → PagerDuty + Slack | 15 sec |
| 5 | Auto-remediation script verifies new leader and replica health | 30 sec |
| 6 | Create replacement replica from new primary | Background |
| **Total** | | **< 10 sec** |

**Data loss:** Zero — Patroni is configured with `synchronous_mode: true` and `maximum_lag_on_failover: 1048576` (1MB). Only synchronous replicas are promoted.

**Infrastructure:** See `deploy/k8s/infrastructure/patroni/` for StatefulSet, ConfigMap, and RBAC configuration.

**Runbook:** `deploy/runbooks/database-failover.md`

---

### Scenario 3: TigerBeetle Cluster Failure

**Trigger:** Quorum loss in TigerBeetle cluster (2+ of 3 nodes fail simultaneously).

**Impact:** Critical. All ledger writes stop. Transfers cannot progress past the `ledger_posted` state. Quotes can still be created (read-only). Treasury reservations continue (in-memory).

**Recovery:**

| Step | Action | Time |
|------|--------|------|
| 1 | Alert fires on TigerBeetle TCP health check failure | 15 sec |
| 2 | On-call paged (P1 -- Critical) | Immediate |
| 3 | Attempt restart of failed nodes | 2 min |
| 4 | If data corruption: restore from latest snapshot (S3, hourly) | 15--25 min |
| 5 | Replay any ledger entries from PostgreSQL that were written after the snapshot | 5 min |
| 6 | Verify TigerBeetle balances match PostgreSQL read model | 5 min |
| 7 | Resume transfer processing; stalled transfers auto-retry via NATS redelivery | 2 min |
| **Total (restart works)** | | **< 5 min** |
| **Total (restore from snapshot)** | | **< 30 min** |

**Data loss:** Zero if restart succeeds (TigerBeetle replicates synchronously within the cluster). If restoring from snapshot, entries between last snapshot and failure must be replayed from the PostgreSQL read model (CQRS sync writes to PG within seconds of TB write).

**Runbook:** `deploy/runbooks/tigerbeetle-recovery.md`

**Mitigation:** TigerBeetle's 3-node cluster tolerates 1 node failure with zero data loss. Simultaneous multi-node failure requires correlated failure (AZ outage, operator error).

---

### Scenario 4: Full Region Failure

**Trigger:** AWS region outage (us-east-1 unavailable).

**Impact:** Complete service outage until DR region activation.

**Recovery:**

| Step | Action | Time |
|------|--------|------|
| 1 | AWS region outage detected (CloudWatch cross-region alarm, external monitoring) | 5 min |
| 2 | Incident commander declares DR activation | 5 min |
| 3 | DNS failover to DR region (Route 53 health check, TTL 60s) | 1--2 min |
| 4 | DR Kubernetes cluster activates (warm standby, scaled down) | 5 min |
| 5 | PostgreSQL cross-region replicas promoted to primary | 5 min |
| 6 | TigerBeetle restored from latest cross-region snapshot (S3 cross-region replication) | 15 min |
| 7 | NATS cluster bootstrapped from DR snapshot | 5 min |
| 8 | Application pods scaled up, health checks pass | 5 min |
| 9 | Verification: end-to-end smoke test | 5 min |
| **Total** | | **30--60 min** |

**Data loss:** Up to the replication lag of the cross-region PostgreSQL replica (typically < 5 seconds for async replication). TigerBeetle snapshot may be up to 1 hour old; gap reconciled from PostgreSQL read model.

**DR region strategy:** Warm standby in us-west-2.
- Infrastructure pre-provisioned (EKS cluster, RDS replicas, S3 buckets)
- Application images pre-pulled
- Scaled to 25% of production capacity; scale up on activation

---

### Scenario 5: Data Corruption

**Trigger:** Bug deploys corrupted data, operator error, SQL injection (despite parameterized queries).

**Impact:** Depends on scope. May affect a single tenant or all tenants.

**Recovery -- Application Bug:**

| Step | Action | Time |
|------|--------|------|
| 1 | Detect via monitoring (error rate spike, balance discrepancy alert) | 5 min |
| 2 | Rollback deployment (`kubectl rollout undo deployment/settla-server`) | 1 min |
| 3 | Identify affected data scope | 15 min |
| 4 | Issue correcting ledger entries (never delete/modify existing entries) | 30 min |
| 5 | Reconcile TigerBeetle and PostgreSQL balances | 15 min |
| **Total** | | **30--60 min** |

**Critical rule:** Never delete or modify existing ledger entries. Corrections are always additive (reversing entries). This preserves the audit trail required by VASP regulations (see `docs/compliance.md`).

**Recovery -- Database Corruption:**

| Step | Action | Time |
|------|--------|------|
| 1 | Stop writes to affected database (drain PgBouncer) | 1 min |
| 2 | Assess corruption scope using `pg_catalog` checks | 10 min |
| 3 | If localized: repair specific tables/rows | 30 min |
| 4 | If widespread: restore from point-in-time recovery (PITR) | 1--2 hours |
| 5 | Reconcile TigerBeetle (source of truth for balances) against restored PG | 30 min |
| **Total** | | **1--3 hours** |

---

## Backup Strategy

### PostgreSQL

| Backup Type | Frequency | Retention | Storage |
|-------------|-----------|-----------|---------|
| Full base backup | Daily at 02:00 UTC | 30 days | S3 (same region) + S3 (DR region) |
| WAL archive | Continuous | 7 days | S3 (same region) |
| Point-in-time recovery | Any point in last 7 days | 7 days | WAL replay on base backup |
| Monthly archive | First of month | 7 years | S3 Glacier |

### TigerBeetle

| Backup Type | Frequency | Retention | Storage |
|-------------|-----------|-----------|---------|
| Data file snapshot | Hourly | 72 hours | S3 (same region) + S3 (DR region) |
| Pre-deploy snapshot | Before every deployment | 7 days | S3 |

### NATS JetStream

| Backup Type | Frequency | Retention | Storage |
|-------------|-----------|-----------|---------|
| Stream snapshot | Every 6 hours | 48 hours | S3 |

### Backup Verification

- **Daily:** Automated restore test of latest PostgreSQL backup to a throwaway instance; verify row counts and checksums
- **Weekly:** TigerBeetle snapshot restore test; verify balance reconciliation against PostgreSQL
- **Monthly:** Full DR simulation (restore all components in DR region, run end-to-end test)

---

## Communication Plan

### During an Incident

AlertManager (`deploy/k8s/base/alertmanager/`) routes alerts by severity:

| Severity | Route | Action |
|----------|-------|--------|
| Critical (P1) | PagerDuty | Immediate page + Slack #settla-incidents |
| Warning (P2) | Slack #settla-incidents | Auto-remediation triggered |
| Info | Slack #settla-ops | Logged for review |

| Audience | Channel | Timing |
|----------|---------|--------|
| On-call SRE | PagerDuty (via AlertManager) | Immediate (automated) |
| Engineering team | Slack #settla-incidents (via AlertManager) | Immediate (automated) |
| Tenant technical contacts | Status page + email | Within 15 minutes for P1 |
| Tenant account managers | Internal Slack | Within 30 minutes for P1 |

### Post-Incident

- P1/P2 incidents require a post-mortem within 48 hours
- Post-mortem shared with affected tenants (sanitized, no internal details)
- Action items tracked in issue tracker with SLA for completion

---

## DR Testing Schedule

| Test | Frequency | Duration | Scope |
|------|-----------|----------|-------|
| Pod failure (kill random pod) | Weekly (automated) | 5 min | Verify auto-recovery |
| Patroni failover | Monthly | 15 min | Kill primary, verify auto-promotion < 10s |
| Redis Sentinel failover | Monthly | 10 min | Kill master, verify Sentinel promotion |
| TigerBeetle restore | Monthly | 1 hour | Snapshot restore + reconciliation |
| Full DR activation | Quarterly | 4 hours | Failover to DR region, run traffic |
| Chaos testing | Monthly | 2 hours | `make chaos` scenarios |

---

## Automated Remediation

AlertManager triggers automated remediation scripts for common failure patterns. The remediation handler (`deploy/remediation/auto-remediation.sh`) executes idempotent recovery actions:

| Alert | Remediation | Action |
|-------|-------------|--------|
| `SettlaHighErrorRate` | Canary rollback | Argo Rollouts abort if canary is active |
| `SettlaPodCrashLooping` | Pod restart | Delete crash-looping pod, let Deployment reschedule |
| `SettlaHighLatency` | Scale up | Increase HPA replicas by 50% |
| `SettlaNATSConsumerLag` | Consumer restart | Restart settla-node pods to reconnect consumers |
| `SettlaPostgresConnectionExhaustion` | PgBouncer recycle | SIGHUP PgBouncer to close idle connections |
| `SettlaHighMemoryUsage` | Pod restart | Delete highest-memory pod |
| `SettlaHighDiskUsage` | Partition cleanup | Run `make partition-cleanup` to drop expired partitions |

All remediation actions log to Slack `#settla-incidents` and include before/after metrics. Scripts are idempotent and safe to run concurrently.

---

## Resilience Infrastructure

The following resilience patterns are implemented at the application level:

### Graceful Drain (`resilience/drain/`)

During shutdown (SIGTERM), the drainer:
1. Stops accepting new requests (HTTP 503 + `Connection: close`, gRPC `UNAVAILABLE`)
2. Tracks in-flight requests via atomic counters (zero allocation)
3. Blocks until all in-flight requests complete or drain timeout (default 15s) is reached

Both `settla-server` and `settla-node` use drain middleware on HTTP and gRPC servers.

### Circuit Breakers (`resilience/circuitbreaker.go`)

General-purpose circuit breaker (Closed → Open → HalfOpen) with configurable failure thresholds and Prometheus metrics. Applied at infrastructure boundaries (DB, NATS, Redis, external providers).

### Bulkhead Isolation (`resilience/bulkhead.go`)

Semaphore-based concurrency limiting prevents cascade failures. Each infrastructure dependency has an independent concurrency limit.

### Adaptive Load Shedding (`resilience/loadshed.go`)

AIMD-based (Additive Increase Multiplicative Decrease) adaptive concurrency limiting using Little's Law. Automatically sheds load when the system is saturated before cascading failures occur.

### Feature Flags (`resilience/featureflag/`)

File-based feature flags with hot-reload (30s interval), environment variable overrides (`SETTLA_FF_*`), and per-tenant consistent hash rollout. Enables incremental rollout of new features without redeployment.

### Deep Health Checks (`observability/healthcheck/`)

Kubernetes-native health endpoints:
- `/health/live` — liveness: goroutine count only (no external deps)
- `/health/ready` — readiness: all required checks (Postgres pools, NATS, Redis)
- `/health/startup` — 503 until `MarkStartupComplete()` called
- `/health` — full report with latency and status per dependency

All checks run in parallel with 100ms timeout and 1s result caching.

### Synthetic Canary (`observability/synthetic/`)

Test transaction every 30s through the full pipeline (quote → transfer → poll → verify). Provides end-to-end latency and success metrics for SLI burn rate alerting.

### SLI Burn Rate Alerting (`deploy/prometheus/alerts/`)

Google SRE error budget approach with three burn rate windows:
- **Fast burn** (14.4x, 5min/1hr): pages immediately, catches acute failures
- **Medium burn** (6x, 30min/6hr): pages within 30min, catches sustained degradation
- **Slow burn** (3x, 2hr/24hr): tickets, catches gradual drift
