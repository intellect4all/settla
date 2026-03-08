import type { CapacityMetrics, TenantVolume } from '~/types'

/**
 * Composable for querying Prometheus metrics API.
 * Falls back to sample data when Prometheus is unavailable.
 */
export function usePrometheus() {
  const config = useRuntimeConfig()
  const prometheusBase = config.public.prometheusBase as string

  /** Execute a Prometheus instant query. */
  async function query(promql: string): Promise<number> {
    try {
      const res = await $fetch<any>(`${prometheusBase}/api/v1/query`, {
        params: { query: promql },
        timeout: 3000,
      })
      const result = res?.data?.result
      if (result && result.length > 0) {
        return parseFloat(result[0].value[1]) || 0
      }
      return 0
    } catch {
      return NaN
    }
  }

  /** Execute a Prometheus instant query returning multiple results by label. */
  async function queryMulti(promql: string, labelKey: string): Promise<Record<string, number>> {
    try {
      const res = await $fetch<any>(`${prometheusBase}/api/v1/query`, {
        params: { query: promql },
        timeout: 3000,
      })
      const result = res?.data?.result
      if (!result) return {}
      const out: Record<string, number> = {}
      for (const r of result) {
        const label = r.metric?.[labelKey] ?? 'unknown'
        out[label] = parseFloat(r.value[1]) || 0
      }
      return out
    } catch {
      return {}
    }
  }

  /** Fetch all capacity metrics from Prometheus in parallel. */
  async function getCapacityMetrics(): Promise<CapacityMetrics | null> {
    const [
      currentTps,
      peakTps,
      tbWritesPerSec,
      pgSyncLag,
      treasuryReservesPerSec,
      treasuryFlushLag,
      partitionDepths,
    ] = await Promise.all([
      // Current TPS: rate of transfers over last 1m
      query('sum(rate(settla_transfers_total[1m]))'),
      // Peak TPS: max over last 24h (approximated by max over 1h windows)
      query('max_over_time(sum(rate(settla_transfers_total[1m]))[24h:1m])'),
      // TigerBeetle writes/sec
      query('rate(settla_ledger_tb_writes_total[1m])'),
      // PG sync lag in seconds → ms
      query('settla_ledger_pg_sync_lag_seconds'),
      // Treasury reserves/sec
      query('sum(rate(settla_treasury_reserve_total[1m]))'),
      // Treasury flush lag in seconds → ms
      query('settla_treasury_flush_lag_seconds'),
      // NATS partition queue depths
      queryMulti('settla_nats_partition_queue_depth', 'partition'),
    ])

    // If all critical metrics are NaN, Prometheus is unavailable
    if (isNaN(currentTps) && isNaN(tbWritesPerSec)) {
      return null
    }

    // Build partition depths array (8 partitions, 0-indexed)
    const depths: number[] = []
    for (let i = 0; i < 8; i++) {
      depths.push(partitionDepths[String(i)] ?? 0)
    }

    return {
      current_tps: Math.round(isNaN(currentTps) ? 0 : currentTps),
      peak_tps: Math.round(isNaN(peakTps) ? 0 : peakTps),
      capacity_tps: 5000,
      ledger_writes_per_sec: Math.round(isNaN(tbWritesPerSec) ? 0 : tbWritesPerSec),
      pg_sync_lag_ms: Math.round((isNaN(pgSyncLag) ? 0 : pgSyncLag) * 1000),
      treasury_reserves_per_sec: Math.round(isNaN(treasuryReservesPerSec) ? 0 : treasuryReservesPerSec),
      treasury_flush_lag_ms: Math.round((isNaN(treasuryFlushLag) ? 0 : treasuryFlushLag) * 1000),
      nats_partition_depths: depths,
      pgbouncer_active: 0, // PgBouncer doesn't export Prometheus metrics by default
      pgbouncer_pool_size: 100,
    }
  }

  /** Fetch per-tenant volume metrics from Prometheus. */
  async function getTenantVolumes(): Promise<TenantVolume[]> {
    const [volumeByTenant, countByTenant, failedByTenant] = await Promise.all([
      // Sum of transfer amounts by tenant (approximation using counter rate)
      queryMulti('sum(rate(settla_transfers_total[24h])) by (tenant) * 86400', 'tenant'),
      // Transfer count by tenant over 24h
      queryMulti('sum(increase(settla_transfers_total[24h])) by (tenant)', 'tenant'),
      // Failed transfer count
      queryMulti('sum(increase(settla_transfers_total{status="failed"}[24h])) by (tenant)', 'tenant'),
    ])

    if (Object.keys(countByTenant).length === 0) {
      return []
    }

    const tenants: TenantVolume[] = []
    for (const [tenantId, count] of Object.entries(countByTenant)) {
      const failed = failedByTenant[tenantId] ?? 0
      const successRate = count > 0 ? ((count - failed) / count) * 100 : 100

      tenants.push({
        tenant_id: tenantId,
        tenant_name: tenantId.substring(0, 8), // Short ID as name fallback
        daily_volume_usd: String(Math.round(volumeByTenant[tenantId] ?? 0)),
        daily_limit_usd: '25000000', // Would come from tenant config
        transfer_count: Math.round(count),
        success_rate: Math.round(successRate * 10) / 10,
      })
    }

    return tenants
  }

  return {
    query,
    queryMulti,
    getCapacityMetrics,
    getTenantVolumes,
  }
}
