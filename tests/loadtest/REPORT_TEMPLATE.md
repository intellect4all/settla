# Settla Load Test & Benchmark Report

**Generated:** {{generated_at}}
**Environment:** {{environment}}
**Git SHA:** {{git_sha}}

---

## Executive Summary

| Metric | Target | Actual | Status |
|--------|--------|--------|--------|
| Sustained TPS | 580 | {{sustained_tps}} | {{sustained_status}} |
| Peak TPS | 5,000 | {{peak_tps}} | {{peak_status}} |
| Tenant Scale | 20K–100K | {{tenant_scale}} | {{scale_status}} |
| Settlement Batch | < 2h for 20K tenants | {{settlement_time}} | {{settlement_status}} |

**Overall Result:** {{overall_result}} ({{scenarios_passed}}/{{scenarios_total}} scenarios passed)

---

## Scenario Results

### Scenario A — Smoke Test (Sanity)

| Metric | Threshold | Actual | Pass |
|--------|-----------|--------|------|
| Duration | 60s | {{a_duration}} | — |
| TPS | 10 | {{a_tps}} | — |
| Error Rate | 0% | {{a_error_rate}} | {{a_error_pass}} |
| p50 Latency | < 500ms | {{a_p50}} | {{a_p50_pass}} |
| p99 Latency | < 2s | {{a_p99}} | {{a_p99_pass}} |
| Stuck Transfers | 0 | {{a_stuck}} | {{a_stuck_pass}} |

### Scenario B — Sustained Normal Load

| Metric | Threshold | Actual | Pass |
|--------|-----------|--------|------|
| Target TPS | 580 | {{b_tps}} | — |
| Tenants | 50 | 50 | — |
| Currency Mix | NGN 70%, USD 20%, GBP 10% | — | — |
| p50 Latency | < 100ms | {{b_p50}} | {{b_p50_pass}} |
| p99 Latency | < 500ms | {{b_p99}} | {{b_p99_pass}} |
| Error Rate | < 0.1% | {{b_error_rate}} | {{b_error_pass}} |
| Stuck Transfers | 0 | {{b_stuck}} | {{b_stuck_pass}} |

### Scenario C — Peak Burst

| Metric | Threshold | Actual | Pass |
|--------|-----------|--------|------|
| Peak TPS | 5,000 | {{c_tps}} | — |
| Tenants | 200 | 200 | — |
| p50 Latency | < 200ms | {{c_p50}} | {{c_p50_pass}} |
| p99 Latency | < 1s | {{c_p99}} | {{c_p99_pass}} |
| Error Rate | < 1% | {{c_error_rate}} | {{c_error_pass}} |
| Load Shedding | 503 + Retry-After | {{c_503_count}} | {{c_shed_pass}} |
| Data Corruption | 0 | {{c_corruption}} | {{c_corruption_pass}} |

### Scenario D — Soak Test

| Metric | Threshold | Actual | Pass |
|--------|-----------|--------|------|
| Duration | 1 hour | {{d_duration}} | — |
| TPS | 580 | {{d_tps}} | — |
| RSS Growth | < ±10% | {{d_rss_growth}} | {{d_rss_pass}} |
| Goroutine Growth | stable | {{d_goroutines}} | {{d_goroutine_pass}} |
| Connection Pool | no exhaustion | {{d_pool}} | {{d_pool_pass}} |
| Queue Depth | no growth | {{d_queue}} | {{d_queue_pass}} |

### Scenario E — Spike Test

| Metric | Threshold | Actual | Pass |
|--------|-----------|--------|------|
| Spike | 100 → 5,000 TPS instant | — | — |
| Recovery Time | < 30s | {{e_recovery}} | {{e_recovery_pass}} |
| Data Loss | 0 | {{e_data_loss}} | {{e_loss_pass}} |
| Backpressure | 503 during spike | {{e_503}} | {{e_bp_pass}} |

### Scenario F — Single Tenant Hot-Spot

| Metric | Threshold | Actual | Pass |
|--------|-----------|--------|------|
| Total TPS | 580 | {{f_tps}} | — |
| Hot Tenant Traffic | 80% | {{f_hot_pct}} | — |
| Rate Limiting | active on hot tenant | {{f_rate_limit}} | {{f_rl_pass}} |
| Cold Tenant p99 | unaffected | {{f_cold_p99}} | {{f_cold_pass}} |
| Mutex Starvation | none | {{f_mutex}} | {{f_mutex_pass}} |

### Scenario G — Tenant Scale: 20K

| Metric | Threshold | Actual | Pass |
|--------|-----------|--------|------|
| Tenants | 20,000 | 20,000 | — |
| TPS | 580 (Zipf) | {{g_tps}} | — |
| Top 1% Traffic | ~50% | {{g_top1}} | — |
| Auth Cache Memory | stable | {{g_cache_mem}} | {{g_cache_pass}} |
| RSS Memory | stable | {{g_rss}} | {{g_rss_pass}} |
| Goroutines | stable | {{g_goroutines}} | {{g_goroutine_pass}} |
| p99 Latency | < 500ms | {{g_p99}} | {{g_p99_pass}} |
| Auth Cache Hit Rate | > 90% | {{g_hit_rate}} | {{g_hit_pass}} |

### Scenario H — Tenant Scale: 100K

| Metric | Threshold | Actual | Pass |
|--------|-----------|--------|------|
| Tenants | 100,000 | 100,000 | — |
| TPS | 580 (Zipf) | {{h_tps}} | — |
| Tenant Lookup Latency | no degradation | {{h_lookup}} | {{h_lookup_pass}} |
| Partition Distribution | even | {{h_partitions}} | {{h_partition_pass}} |
| p99 Latency | < 750ms | {{h_p99}} | {{h_p99_pass}} |

### Scenario I — 20K Tenants at 5K TPS

| Metric | Threshold | Actual | Pass |
|--------|-----------|--------|------|
| Tenants | 20,000 | 20,000 | — |
| Peak TPS | 5,000 | {{i_tps}} | — |
| Auth Cache Thrashing | none | {{i_thrash}} | {{i_thrash_pass}} |
| Treasury Overflow | none | {{i_treasury}} | {{i_treasury_pass}} |
| NATS Partition Skew | < 20% | {{i_skew}} | {{i_skew_pass}} |
| PgBouncer Wait | < 100ms | {{i_pgb_wait}} | {{i_pgb_pass}} |

### Scenario J — Settlement Batch at Scale

| Metric | Threshold | Actual | Pass |
|--------|-----------|--------|------|
| Pre-traffic | 580 TPS × 1h = ~2M transfers | {{j_transfers}} | — |
| Tenants | 20,000 | 20,000 | — |
| Settlement Duration | < 2 hours | {{j_duration}} | {{j_duration_pass}} |
| Per-tenant p50 | — | {{j_per_tenant_p50}} | — |
| Per-tenant p99 | — | {{j_per_tenant_p99}} | — |
| Tenants Skipped | 0 | {{j_skipped}} | {{j_skip_pass}} |
| Ledger Reconciliation | pass | {{j_reconciliation}} | {{j_recon_pass}} |

---

## Component Microbenchmarks

### Treasury Reserve/Consume Cycle

| Benchmark | ns/op | B/op | allocs/op |
|-----------|-------|------|-----------|
| BenchmarkReserve_Concurrent | {{tb_reserve}} | {{tb_reserve_b}} | {{tb_reserve_a}} |
| BenchmarkCommitReservation | {{tb_commit}} | {{tb_commit_b}} | {{tb_commit_a}} |
| BenchmarkRelease | {{tb_release}} | {{tb_release_b}} | {{tb_release_a}} |

### Auth Cache Lookup at Scale

| Tenant Count | ns/op (L1 hit) | ns/op (L2 hit) | ns/op (L3 miss) |
|-------------|-----------------|-----------------|------------------|
| 1K | {{ac_1k_l1}} | {{ac_1k_l2}} | {{ac_1k_l3}} |
| 10K | {{ac_10k_l1}} | {{ac_10k_l2}} | {{ac_10k_l3}} |
| 50K | {{ac_50k_l1}} | {{ac_50k_l2}} | {{ac_50k_l3}} |
| 100K | {{ac_100k_l1}} | {{ac_100k_l2}} | {{ac_100k_l3}} |

### sync.Map at Scale (per-tenant daily volume cache)

| Entry Count | Read ns/op | Write ns/op |
|-------------|------------|-------------|
| 1K | {{sm_1k_r}} | {{sm_1k_w}} |
| 10K | {{sm_10k_r}} | {{sm_10k_w}} |
| 50K | {{sm_50k_r}} | — |
| 100K | {{sm_100k_r}} | {{sm_100k_w}} |

### Per-Tenant Mutex Pool

| Tenant Count | Lock/Unlock ns/op |
|-------------|-------------------|
| 1K | {{mu_1k}} |
| 10K | {{mu_10k}} |
| 100K | {{mu_100k}} |

### Outbox Relay Batch Publish

| Batch Size | ns/op | throughput/sec |
|-----------|-------|----------------|
| 10 | {{ob_10}} | {{ob_10_tps}} |
| 100 | {{ob_100}} | {{ob_100_tps}} |
| 500 | {{ob_500}} | {{ob_500_tps}} |

### Transfer State Machine Transition

| Benchmark | ns/op |
|-----------|-------|
| BenchmarkProcessTransfer_FullPipeline | {{sm_pipeline}} |
| BenchmarkProcessTransferConcurrent | {{sm_concurrent}} |

### Zipf Distribution Sampling

| Tenant Count | ns/op |
|-------------|-------|
| 1K | {{zf_1k}} |
| 10K | {{zf_10k}} |
| 100K | {{zf_100k}} |

---

## Seed Data Provisioning Times

| Tier | Tenant Count | Target | Actual | Pass |
|------|-------------|--------|--------|------|
| Small | 50 | < 5s | {{seed_50}} | {{seed_50_pass}} |
| Medium | 20,000 | < 2 min | {{seed_20k}} | {{seed_20k_pass}} |
| Large | 100,000 | < 10 min | {{seed_100k}} | {{seed_100k_pass}} |

---

## Infrastructure Health During Tests

### PgBouncer

| Metric | Scenario B | Scenario C | Scenario I |
|--------|-----------|-----------|-----------|
| Active Connections | {{pgb_b_active}} | {{pgb_c_active}} | {{pgb_i_active}} |
| Waiting Clients | {{pgb_b_wait}} | {{pgb_c_wait}} | {{pgb_i_wait}} |
| Pool Utilization | {{pgb_b_util}} | {{pgb_c_util}} | {{pgb_i_util}} |

### NATS JetStream

| Metric | Scenario B | Scenario C | Scenario I |
|--------|-----------|-----------|-----------|
| Stream Depth | {{nats_b_depth}} | {{nats_c_depth}} | {{nats_i_depth}} |
| Consumer Pending | {{nats_b_pending}} | {{nats_c_pending}} | {{nats_i_pending}} |
| Partition Skew % | {{nats_b_skew}} | {{nats_c_skew}} | {{nats_i_skew}} |

### Redis

| Metric | Scenario B | Scenario D | Scenario G |
|--------|-----------|-----------|-----------|
| Connected Clients | {{redis_b_clients}} | {{redis_d_clients}} | {{redis_g_clients}} |
| Memory Used | {{redis_b_mem}} | {{redis_d_mem}} | {{redis_g_mem}} |

---

## Conclusions

{{conclusions}}

---

*Report generated by `make bench-report`. Raw JSON results in `tests/loadtest/results/`.*
