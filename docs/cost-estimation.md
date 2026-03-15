# Settla Infrastructure Cost Estimation

## Overview

Cost estimation for running Settla at production scale (50M transactions/day, ~580 TPS sustained, 3,000--5,000 TPS peak) on AWS in us-east-1. All prices are on-demand; reserved instances reduce compute costs by 30--40%.

---

## Compute (EKS)

| Service | Instance | Count | vCPU | RAM | Monthly Cost |
|---------|----------|-------|------|-----|-------------|
| settla-server | c6g.xlarge (4 vCPU, 8 GB) | 6 | 24 | 48 GB | $882 |
| settla-node | c6g.large (2 vCPU, 4 GB) | 8 | 16 | 32 GB | $588 |
| Gateway (Fastify) | c6g.large (2 vCPU, 4 GB) | 4 | 8 | 16 GB | $294 |
| Webhook dispatcher | t3.medium (2 vCPU, 4 GB) | 2 | 4 | 8 GB | $61 |
| Dashboard (Nuxt) | t3.small (2 vCPU, 2 GB) | 1 | 2 | 2 GB | $15 |
| Tyk API Gateway | c6g.large (2 vCPU, 4 GB) | 2 | 4 | 8 GB | $147 |
| EKS control plane | Managed | 1 | -- | -- | $73 |
| **Subtotal** | | **24** | **58** | **114 GB** | **$2,060** |

### HPA Headroom (Peak Scaling)

At peak (5,000 TPS), HPA scales settla-server from 6 to 12 pods and gateway from 4 to 8 pods. Average utilization across the month is ~70% of peak capacity.

| Service | Peak pods | Additional monthly cost (prorated ~30% of month at peak) |
|---------|-----------|----------------------------------------------------------|
| settla-server | +6 | $441 * 0.3 = $132 |
| Gateway | +4 | $294 * 0.3 = $88 |
| **Peak headroom** | | **$220** |

**Total Compute: ~$2,280/month**

---

## Databases

| Service | Instance | Count | Storage | Monthly Cost |
|---------|----------|-------|---------|-------------|
| PostgreSQL Ledger (primary) | r6g.xlarge (4 vCPU, 32 GB) | 1 | 200 GB gp3 | $280 |
| PostgreSQL Ledger (replicas) | r6g.large (2 vCPU, 16 GB) | 2 | 200 GB gp3 | $296 |
| PostgreSQL Transfer (primary) | r6g.xlarge (4 vCPU, 32 GB) | 1 | 200 GB gp3 | $280 |
| PostgreSQL Transfer (replicas) | r6g.large (2 vCPU, 16 GB) | 2 | 200 GB gp3 | $296 |
| PostgreSQL Treasury (primary) | r6g.large (2 vCPU, 16 GB) | 1 | 50 GB gp3 | $148 |
| PostgreSQL Treasury (replica) | r6g.large (2 vCPU, 16 GB) | 1 | 50 GB gp3 | $148 |
| PgBouncer (3 instances) | t3.small (lightweight proxy) | 3 | -- | $45 |
| **Subtotal** | | | | **$1,493** |

*Note: Using RDS instead of self-managed Postgres adds ~30% cost but reduces operational burden. RDS Multi-AZ costs shown above approximate self-managed equivalents.*

---

## TigerBeetle

| Component | Instance | Count | Storage | Monthly Cost |
|-----------|----------|-------|---------|-------------|
| TigerBeetle cluster | i3.xlarge (4 vCPU, 30.5 GB, NVMe SSD) | 3 | 950 GB NVMe (included) | $729 |

TigerBeetle requires fast local NVMe storage for its LSM-tree. The i3 instance family provides this natively. At 50M transactions/day (~100M ledger entries), storage grows at ~10 GB/month.

**Total TigerBeetle: ~$729/month**

---

## Caching and Messaging

| Service | Instance | Count | Monthly Cost |
|---------|----------|-------|-------------|
| Redis (ElastiCache) | r6g.large (2 vCPU, 13 GB) | 3 primary + 3 replica | $540 |
| NATS JetStream | c6g.large (2 vCPU, 4 GB) | 3 | $221 |
| **Subtotal** | | | **$761** |

---

## Storage

| Type | Size | Monthly Cost |
|------|------|-------------|
| EBS gp3 (databases, 6x 200 GB + 2x 50 GB) | 1,300 GB | $104 |
| EBS gp3 (NATS, TigerBeetle snapshots) | 200 GB | $16 |
| S3 (backups, WAL archives) | ~500 GB/month | $12 |
| S3 Glacier (long-term archive, growing) | ~2 TB cumulative | $8 |
| **Subtotal** | | **$140** |

---

## Networking

| Component | Estimate | Monthly Cost |
|-----------|----------|-------------|
| ALB (Application Load Balancer) | 2 (external + internal) | $36 |
| NAT Gateway (3 AZs) | ~2 TB/month processed | $180 |
| Data transfer (inter-AZ) | ~500 GB/month | $5 |
| Data transfer (internet egress) | ~200 GB/month | $18 |
| Route 53 (DNS) | 2 hosted zones + health checks | $5 |
| **Subtotal** | | **$244** |

---

## Observability

| Tool | Details | Monthly Cost |
|------|---------|-------------|
| Prometheus | Self-hosted on EKS (c6g.large) | $110 |
| Grafana | Self-hosted on EKS (t3.medium) | $30 |
| CloudWatch (logs) | ~50 GB/month ingestion | $25 |
| CloudWatch (metrics) | Custom metrics + alarms | $50 |
| PagerDuty | Team plan (5 users) | $175 |
| **Subtotal** | | **$390** |

*Alternative: Grafana Cloud or Datadog adds ~$500--1,500/month but eliminates self-hosting overhead.*

---

## Summary

| Category | Monthly Cost |
|----------|-------------|
| Compute (EKS + HPA headroom) | $2,280 |
| Databases (PostgreSQL + PgBouncer) | $1,493 |
| TigerBeetle | $729 |
| Cache + Messaging (Redis + NATS) | $761 |
| Storage (EBS + S3) | $140 |
| Networking (ALB + NAT + egress) | $244 |
| Observability | $390 |
| **Total (self-managed)** | **$6,037/month** |
| **Annualized** | **$72,444/year** |

---

## Per-Transaction Cost

```
$6,037 / 50,000,000 transactions/day / 30 days = $0.000004 per transaction
```

Including all infrastructure amortization:
```
$6,037 / (50M * 30) = $0.000004 per transaction
$72,444 / (50M * 365) = $0.000004 per transaction
```

At the full monthly cost against daily volume for operational comparison:
```
$6,037 / 1,500,000,000 monthly transactions = $0.000004
```

---

## Cost Optimization Opportunities

| Optimization | Savings | Effort |
|-------------|---------|--------|
| Reserved Instances (1-year, compute) | ~$800/month (35%) | Low |
| Reserved Instances (1-year, RDS) | ~$500/month (30%) | Low |
| Graviton instances (already used above) | Included | Done |
| Spot instances for settla-node (tolerates interruption) | ~$200/month | Medium |
| S3 Intelligent-Tiering for backups | ~$5/month | Low |
| Right-sizing after 3 months of production data | 10--20% | Medium |
| **Total potential savings** | **~$1,500/month** | |
| **Optimized total** | **~$4,500/month** | |
| **Optimized per-transaction** | **$0.000003** | |

---

## Scaling Cost Projections

| Scale | TPS (sustained) | Monthly Cost | Per-Transaction |
|-------|-----------------|-------------|-----------------|
| 10M tx/day | ~116 TPS | ~$3,500 | $0.000012 |
| 50M tx/day (current) | ~580 TPS | ~$6,037 | $0.000004 |
| 100M tx/day | ~1,160 TPS | ~$9,000 | $0.000003 |
| 500M tx/day | ~5,800 TPS | ~$25,000 | $0.0000017 |

Cost scales sub-linearly due to fixed infrastructure overhead (observability, networking, control plane) and efficient batching at higher throughput.
