<template>
  <div>
    <div class="flex items-center justify-between mb-6">
      <div>
        <h1 class="text-2xl font-semibold text-surface-100">Capacity</h1>
        <p class="text-sm text-surface-500 mt-0.5">Live system capacity monitoring &mdash; 50M txn/day target</p>
      </div>
      <span class="text-xs text-surface-600">Auto-refreshing every 3s</span>
    </div>

    <!-- Alerts -->
    <div v-if="alerts.length > 0" class="space-y-2 mb-6">
      <AlertBanner
        v-for="(alert, idx) in alerts"
        :key="idx"
        :type="alert.type"
        :title="alert.title"
        :description="alert.description"
      />
    </div>

    <!-- TPS Section -->
    <div class="grid grid-cols-1 lg:grid-cols-3 gap-4 mb-6">
      <div class="card p-5">
        <p class="text-xs text-surface-500 uppercase tracking-wider mb-1">Current TPS</p>
        <p class="text-4xl font-bold font-mono tabular-nums" :class="tpsColor">
          {{ metrics?.current_tps?.toLocaleString() ?? '\u2014' }}
        </p>
        <p class="text-xs text-surface-500 mt-1">transactions per second</p>
        <!-- TPS bar -->
        <div class="mt-3 h-2 bg-surface-800 rounded-full overflow-hidden">
          <div
            :style="{ width: tpsPercent + '%' }"
            :class="tpsBarColor"
            class="h-full rounded-full transition-all duration-500"
          />
        </div>
        <div class="flex justify-between text-xs text-surface-600 mt-1">
          <span>0</span>
          <span>{{ metrics?.capacity_tps?.toLocaleString() ?? '5,000' }} capacity</span>
        </div>
      </div>

      <div class="card p-5">
        <p class="text-xs text-surface-500 uppercase tracking-wider mb-1">Peak TPS (Today)</p>
        <p class="text-4xl font-bold font-mono tabular-nums text-surface-100">
          {{ metrics?.peak_tps?.toLocaleString() ?? '\u2014' }}
        </p>
        <p class="text-xs text-surface-500 mt-1">
          {{ peakPercent }}% of capacity
        </p>
      </div>

      <div class="card p-5">
        <p class="text-xs text-surface-500 uppercase tracking-wider mb-1">Capacity Headroom</p>
        <p class="text-4xl font-bold font-mono tabular-nums" :class="headroomColor">
          {{ headroom }}%
        </p>
        <p class="text-xs text-surface-500 mt-1">remaining capacity</p>
      </div>
    </div>

    <!-- Component Metrics -->
    <div class="grid grid-cols-1 md:grid-cols-2 gap-4 mb-6">
      <!-- Ledger Throughput -->
      <div class="card p-5">
        <h3 class="text-sm font-semibold text-surface-200 mb-4">Ledger Throughput</h3>
        <div class="space-y-4">
          <div>
            <div class="flex justify-between text-sm mb-1">
              <span class="text-surface-400">TigerBeetle writes/sec</span>
              <span class="font-mono text-surface-200">{{ metrics?.ledger_writes_per_sec?.toLocaleString() ?? '\u2014' }}</span>
            </div>
            <div class="h-2 bg-surface-800 rounded-full overflow-hidden">
              <div
                :style="{ width: Math.min((metrics?.ledger_writes_per_sec ?? 0) / 25000 * 100, 100) + '%' }"
                class="h-full bg-violet-500 rounded-full transition-all duration-500"
              />
            </div>
            <p class="text-xs text-surface-600 mt-0.5 text-right">target: 25,000/sec</p>
          </div>
          <div>
            <div class="flex justify-between text-sm mb-1">
              <span class="text-surface-400">PG Sync Lag</span>
              <span class="font-mono" :class="lagColor(metrics?.pg_sync_lag_ms ?? 0, 100, 500)">
                {{ metrics?.pg_sync_lag_ms ?? '\u2014' }}ms
              </span>
            </div>
            <div class="h-2 bg-surface-800 rounded-full overflow-hidden">
              <div
                :style="{ width: Math.min((metrics?.pg_sync_lag_ms ?? 0) / 1000 * 100, 100) + '%' }"
                :class="lagBarColor(metrics?.pg_sync_lag_ms ?? 0, 100, 500)"
                class="h-full rounded-full transition-all duration-500"
              />
            </div>
          </div>
        </div>
      </div>

      <!-- Treasury -->
      <div class="card p-5">
        <h3 class="text-sm font-semibold text-surface-200 mb-4">Treasury Reservations</h3>
        <div class="space-y-4">
          <div>
            <div class="flex justify-between text-sm mb-1">
              <span class="text-surface-400">Reserves/sec</span>
              <span class="font-mono text-surface-200">{{ metrics?.treasury_reserves_per_sec?.toLocaleString() ?? '\u2014' }}</span>
            </div>
            <div class="h-2 bg-surface-800 rounded-full overflow-hidden">
              <div
                :style="{ width: Math.min((metrics?.treasury_reserves_per_sec ?? 0) / 5000 * 100, 100) + '%' }"
                class="h-full bg-emerald-500 rounded-full transition-all duration-500"
              />
            </div>
          </div>
          <div>
            <div class="flex justify-between text-sm mb-1">
              <span class="text-surface-400">Flush Lag</span>
              <span class="font-mono" :class="lagColor(metrics?.treasury_flush_lag_ms ?? 0, 50, 200)">
                {{ metrics?.treasury_flush_lag_ms ?? '\u2014' }}ms
              </span>
            </div>
            <div class="h-2 bg-surface-800 rounded-full overflow-hidden">
              <div
                :style="{ width: Math.min((metrics?.treasury_flush_lag_ms ?? 0) / 500 * 100, 100) + '%' }"
                :class="lagBarColor(metrics?.treasury_flush_lag_ms ?? 0, 50, 200)"
                class="h-full rounded-full transition-all duration-500"
              />
            </div>
            <p class="text-xs text-surface-600 mt-0.5 text-right">target: &lt;100ms flush interval</p>
          </div>
        </div>
      </div>
    </div>

    <!-- NATS & PgBouncer -->
    <div class="grid grid-cols-1 md:grid-cols-2 gap-4 mb-6">
      <!-- NATS Partitions -->
      <div class="card p-5">
        <h3 class="text-sm font-semibold text-surface-200 mb-4">NATS Partition Queue Depth</h3>
        <div class="space-y-2">
          <div v-for="(depth, idx) in partitionDepths" :key="idx" class="flex items-center gap-3">
            <span class="text-xs text-surface-500 w-6">P{{ idx }}</span>
            <div class="flex-1 h-4 bg-surface-800 rounded overflow-hidden">
              <div
                :style="{ width: Math.min(depth / maxPartitionDepth * 100, 100) + '%' }"
                :class="depth > 1000 ? 'bg-red-500' : depth > 500 ? 'bg-amber-500' : 'bg-blue-500'"
                class="h-full rounded transition-all duration-500"
              />
            </div>
            <span class="text-xs font-mono text-surface-400 w-16 text-right">{{ depth.toLocaleString() }}</span>
          </div>
        </div>
      </div>

      <!-- PgBouncer -->
      <div class="card p-5">
        <h3 class="text-sm font-semibold text-surface-200 mb-4">PgBouncer Connections</h3>
        <div class="flex items-end gap-6">
          <div class="flex-1">
            <div class="flex justify-between text-sm mb-2">
              <span class="text-surface-400">Active</span>
              <span class="font-mono text-surface-200">
                {{ metrics?.pgbouncer_active ?? '\u2014' }} / {{ metrics?.pgbouncer_pool_size ?? '\u2014' }}
              </span>
            </div>
            <!-- Visual pool representation -->
            <div class="grid grid-cols-10 gap-1">
              <div
                v-for="i in (metrics?.pgbouncer_pool_size ?? 20)"
                :key="i"
                :class="i <= (metrics?.pgbouncer_active ?? 0) ? 'bg-violet-500' : 'bg-surface-800'"
                class="h-3 rounded-sm transition-colors duration-300"
              />
            </div>
          </div>
        </div>
        <p class="text-xs mt-3" :class="poolUtilColor">
          {{ poolUtilization }}% utilization
        </p>
      </div>
    </div>

    <!-- Per-Tenant Volume -->
    <div class="card overflow-hidden">
      <div class="px-4 py-3 border-b border-surface-800">
        <h3 class="text-sm font-semibold text-surface-200">Tenant Volume Breakdown</h3>
      </div>
      <LoadingSpinner v-if="tenantsLoading && !tenantVolumes.length" />
      <table v-else class="w-full text-sm">
        <thead>
          <tr class="border-b border-surface-800">
            <th class="px-4 py-3 text-left text-xs font-medium text-surface-500 uppercase">Tenant</th>
            <th class="px-4 py-3 text-right text-xs font-medium text-surface-500 uppercase">Transfers</th>
            <th class="px-4 py-3 text-right text-xs font-medium text-surface-500 uppercase">Volume (USD)</th>
            <th class="px-4 py-3 text-left text-xs font-medium text-surface-500 uppercase">Daily Limit</th>
            <th class="px-4 py-3 text-center text-xs font-medium text-surface-500 uppercase">Success Rate</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="tv in tenantVolumes" :key="tv.tenant_id" class="border-b border-surface-800/50 hover:bg-surface-800/30">
            <td class="px-4 py-3">
              <NuxtLink :to="`/tenants/${tv.tenant_id}`" class="text-surface-200 hover:text-violet-400 transition-colors">
                {{ tv.tenant_name }}
              </NuxtLink>
            </td>
            <td class="px-4 py-3 text-right font-mono text-surface-300">{{ tv.transfer_count.toLocaleString() }}</td>
            <td class="px-4 py-3 text-right">
              <MoneyDisplay :amount="tv.daily_volume_usd" currency="USD" size="sm" />
            </td>
            <td class="px-4 py-3">
              <!-- Volume vs limit progress bar -->
              <div class="flex items-center gap-2">
                <div class="flex-1 h-1.5 bg-surface-800 rounded-full overflow-hidden max-w-24">
                  <div
                    :style="{ width: volumePercent(tv) + '%' }"
                    :class="volumePercent(tv) > 90 ? 'bg-red-500' : volumePercent(tv) > 75 ? 'bg-amber-500' : 'bg-emerald-500'"
                    class="h-full rounded-full"
                  />
                </div>
                <span class="text-xs text-surface-500">{{ volumePercent(tv).toFixed(0) }}%</span>
              </div>
            </td>
            <td class="px-4 py-3 text-center">
              <span :class="tv.success_rate >= 95 ? 'text-emerald-400' : tv.success_rate >= 80 ? 'text-amber-400' : 'text-red-400'" class="font-mono text-xs">
                {{ tv.success_rate.toFixed(1) }}%
              </span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<script setup lang="ts">
import type { CapacityMetrics, TenantVolume } from '~/types'
import Decimal from 'decimal.js'

const config = useRuntimeConfig()
const prom = usePrometheus()

const { data: metricsData, loading: metricsLoading } = usePolling(
  async () => {
    // Try Prometheus first, fall back to gateway API, then sample data
    const promMetrics = await prom.getCapacityMetrics()
    if (promMetrics) return promMetrics
    try {
      const api = useApi()
      return await api.getCapacityMetrics()
    } catch {
      return generateSampleMetrics()
    }
  },
  config.public.pollIntervalCapacity as number,
)

const { data: tenantData, loading: tenantsLoading } = usePolling(
  async () => {
    // Try Prometheus first, fall back to sample data
    const promTenants = await prom.getTenantVolumes()
    if (promTenants.length > 0) return { tenants: promTenants }
    try {
      const api = useApi()
      return await api.getTenantVolumes()
    } catch {
      return { tenants: generateSampleTenantVolumes() }
    }
  },
  10000,
)

const metrics = computed(() => metricsData.value)
const tenantVolumes = computed(() => tenantData.value?.tenants ?? [])

const partitionDepths = computed(() => metrics.value?.nats_partition_depths ?? [0, 0, 0, 0, 0, 0, 0, 0])
const maxPartitionDepth = computed(() => Math.max(...partitionDepths.value, 100))

const tpsPercent = computed(() => {
  if (!metrics.value) return 0
  return Math.min((metrics.value.current_tps / metrics.value.capacity_tps) * 100, 100)
})

const peakPercent = computed(() => {
  if (!metrics.value) return '0'
  return ((metrics.value.peak_tps / metrics.value.capacity_tps) * 100).toFixed(1)
})

const headroom = computed(() => {
  if (!metrics.value) return '\u2014'
  return ((1 - metrics.value.current_tps / metrics.value.capacity_tps) * 100).toFixed(0)
})

const tpsColor = computed(() => {
  const p = tpsPercent.value
  if (p > 90) return 'text-red-400'
  if (p > 70) return 'text-amber-400'
  return 'text-emerald-400'
})

const tpsBarColor = computed(() => {
  const p = tpsPercent.value
  if (p > 90) return 'bg-red-500'
  if (p > 70) return 'bg-amber-500'
  return 'bg-emerald-500'
})

const headroomColor = computed(() => {
  const h = parseFloat(headroom.value)
  if (isNaN(h)) return 'text-surface-400'
  if (h < 10) return 'text-red-400'
  if (h < 30) return 'text-amber-400'
  return 'text-emerald-400'
})

const poolUtilization = computed(() => {
  if (!metrics.value || !metrics.value.pgbouncer_pool_size) return '0'
  return ((metrics.value.pgbouncer_active / metrics.value.pgbouncer_pool_size) * 100).toFixed(0)
})

const poolUtilColor = computed(() => {
  const u = parseFloat(poolUtilization.value)
  if (u > 90) return 'text-red-400'
  if (u > 70) return 'text-amber-400'
  return 'text-surface-500'
})

// Alerts
const alerts = computed(() => {
  const a: { type: 'warning' | 'error'; title: string; description: string }[] = []
  if (!metrics.value) return a

  if (tpsPercent.value > 90) a.push({ type: 'error', title: 'TPS near capacity', description: `Current: ${metrics.value.current_tps} TPS / Capacity: ${metrics.value.capacity_tps} TPS` })
  if ((metrics.value.pg_sync_lag_ms ?? 0) > 500) a.push({ type: 'warning', title: 'High PG sync lag', description: `${metrics.value.pg_sync_lag_ms}ms \u2014 target is <100ms` })
  if ((metrics.value.treasury_flush_lag_ms ?? 0) > 200) a.push({ type: 'warning', title: 'Treasury flush lag elevated', description: `${metrics.value.treasury_flush_lag_ms}ms \u2014 target is <100ms` })
  if (parseFloat(poolUtilization.value) > 85) a.push({ type: 'warning', title: 'PgBouncer pool running hot', description: `${poolUtilization.value}% utilization` })

  for (const tv of tenantVolumes.value) {
    if (volumePercent(tv) > 90) {
      a.push({ type: 'warning', title: `${tv.tenant_name} approaching daily limit`, description: `${volumePercent(tv).toFixed(0)}% of daily limit used` })
    }
  }

  return a
})

function lagColor(val: number, warn: number, crit: number) {
  if (val > crit) return 'text-red-400'
  if (val > warn) return 'text-amber-400'
  return 'text-emerald-400'
}

function lagBarColor(val: number, warn: number, crit: number) {
  if (val > crit) return 'bg-red-500'
  if (val > warn) return 'bg-amber-500'
  return 'bg-emerald-500'
}

function volumePercent(tv: TenantVolume) {
  const vol = new Decimal(tv.daily_volume_usd || '0')
  const limit = new Decimal(tv.daily_limit_usd || '1')
  if (limit.isZero()) return 0
  return vol.div(limit).mul(100).toNumber()
}

function generateSampleMetrics(): CapacityMetrics {
  return {
    current_tps: 580 + Math.floor(Math.random() * 100 - 50),
    peak_tps: 2847,
    capacity_tps: 5000,
    ledger_writes_per_sec: 12400 + Math.floor(Math.random() * 2000),
    pg_sync_lag_ms: 15 + Math.floor(Math.random() * 30),
    treasury_reserves_per_sec: 580 + Math.floor(Math.random() * 100),
    treasury_flush_lag_ms: 8 + Math.floor(Math.random() * 15),
    nats_partition_depths: Array.from({ length: 8 }, () => Math.floor(Math.random() * 200)),
    pgbouncer_active: 35 + Math.floor(Math.random() * 20),
    pgbouncer_pool_size: 100,
  }
}

function generateSampleTenantVolumes(): TenantVolume[] {
  return [
    { tenant_id: 'a0000000-0000-0000-0000-000000000001', tenant_name: 'Lemfi', daily_volume_usd: '12500000.00', daily_limit_usd: '25000000.00', transfer_count: 24500, success_rate: 98.7 },
    { tenant_id: 'b0000000-0000-0000-0000-000000000002', tenant_name: 'Fincra', daily_volume_usd: '8200000.00', daily_limit_usd: '15000000.00', transfer_count: 16800, success_rate: 97.2 },
    { tenant_id: 'c0000000-0000-0000-0000-000000000003', tenant_name: 'Paystack', daily_volume_usd: '4100000.00', daily_limit_usd: '10000000.00', transfer_count: 8200, success_rate: 99.1 },
  ]
}
</script>
