<template>
  <div class="space-y-6">
    <div>
      <h1 class="text-xl font-semibold text-surface-100">Dashboard</h1>
      <p class="text-sm text-surface-500 mt-1">Overview of your settlement activity</p>
    </div>

    <!-- Skeleton loading state -->
    <template v-if="store.loading">
      <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
        <SkeletonLoader v-for="i in 4" :key="i" variant="card" height="100px" />
      </div>
      <div class="grid grid-cols-1 sm:grid-cols-3 gap-4">
        <SkeletonLoader v-for="i in 3" :key="i" variant="card" height="100px" />
      </div>
      <SkeletonLoader variant="card" height="56px" />
      <SkeletonLoader variant="chart" height="200px" />
    </template>

    <template v-else-if="store.metrics">
      <!-- KPI Cards with stagger -->
      <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
        <div v-for="(card, i) in kpiCards" :key="card.label" class="animate-slide-up" :style="{ animationDelay: `${i * 50}ms`, animationFillMode: 'backwards' }">
          <SummaryCard :label="card.label" :value="card.value" :variant="card.variant" />
        </div>
      </div>

      <!-- 7d / 30d summary -->
      <div class="grid grid-cols-1 sm:grid-cols-3 gap-4">
        <SummaryCard label="Volume (7d)" :value="money.formatCompact(store.metrics.volume_7d_usd, 'USD')" />
        <SummaryCard label="Fees (7d)" :value="money.format(store.metrics.fees_7d_usd, 'USD')" />
        <SummaryCard label="Success Rate (30d)" :value="`${store.successRate}%`" :variant="parseFloat(store.successRate) >= 99 ? 'success' : 'warning'" />
      </div>

      <!-- Daily limit usage -->
      <div class="card p-5">
        <div class="flex items-center justify-between mb-2">
          <span class="text-sm text-surface-400">Daily Limit Usage</span>
          <span class="text-sm font-mono text-surface-300">
            {{ money.format(store.metrics.daily_usage_usd, 'USD') }} / {{ money.format(store.metrics.daily_limit_usd, 'USD') }}
          </span>
        </div>
        <div class="w-full h-2 bg-surface-800 rounded-full overflow-hidden">
          <div
            class="h-full rounded-full transition-all duration-500"
            :class="store.dailyUsagePercent > 90 ? 'bg-red-500' : store.dailyUsagePercent > 70 ? 'bg-amber-500' : 'bg-violet-500'"
            :style="{ width: `${store.dailyUsagePercent}%` }"
          />
        </div>
        <p class="text-xs text-surface-500 mt-1">{{ store.dailyUsagePercent.toFixed(1) }}% used</p>
      </div>

      <!-- Transfer volume chart -->
      <div class="card p-5">
        <div class="flex items-center justify-between mb-4">
          <h2 class="text-sm font-medium text-surface-300">Transfer Volume</h2>
          <div class="flex gap-1">
            <button
              v-for="p in chartPeriods" :key="p.value"
              class="px-2 py-1 text-xs rounded transition-colors"
              :class="store.chartPeriod === p.value ? 'bg-violet-600 text-white' : 'text-surface-400 hover:bg-surface-800'"
              @click="store.fetchChart(p.value, p.granularity)"
            >
              {{ p.label }}
            </button>
          </div>
        </div>

        <div v-if="store.chartLoading" class="flex items-center justify-center h-32">
          <Icon name="loader" :size="20" class="animate-spin text-surface-500" />
        </div>
        <div v-else-if="store.chartBuckets.length" class="grid grid-cols-12 gap-px items-end h-32">
          <div
            v-for="(bucket, i) in displayBuckets" :key="i"
            class="bg-violet-500/80 rounded-t hover:bg-violet-400 transition-colors relative group"
            :style="{ height: `${barHeight(bucket.total)}%`, minHeight: bucket.total > 0 ? '4px' : '0' }"
          >
            <div class="absolute bottom-full mb-1 left-1/2 -translate-x-1/2 bg-surface-800 text-surface-200 text-[10px] px-1.5 py-0.5 rounded whitespace-nowrap opacity-0 group-hover:opacity-100 pointer-events-none transition-opacity">
              {{ bucket.total }} txns
            </div>
          </div>
        </div>
        <EmptyState v-else title="No data" description="No transfer activity in this period" icon="bar-chart" />
      </div>
    </template>

    <EmptyState v-else-if="store.error" :title="store.error" icon="alert-triangle" />
  </div>
</template>

<script setup lang="ts">
const store = useDashboardStore()
const money = useMoney()

const chartPeriods = [
  { label: '24h', value: '24h', granularity: 'hour' },
  { label: '7d', value: '7d', granularity: 'hour' },
  { label: '30d', value: '30d', granularity: 'day' },
]

const kpiCards = computed(() => {
  if (!store.metrics) return []
  return [
    { label: 'Transfers Today', value: String(store.metrics.transfers_today), variant: undefined },
    { label: 'Volume Today', value: money.formatCompact(store.metrics.volume_today_usd, 'USD'), variant: undefined },
    { label: 'Completed', value: String(store.metrics.completed_today), variant: 'success' as const },
    { label: 'Failed', value: String(store.metrics.failed_today), variant: store.metrics.failed_today > 0 ? 'danger' as const : undefined },
  ]
})

const displayBuckets = computed(() => store.chartBuckets.slice(-12))
const maxTotal = computed(() => Math.max(...displayBuckets.value.map(b => b.total), 1))
function barHeight(total: number) { return (total / maxTotal.value) * 100 }

onMounted(() => {
  store.fetchMetrics()
  store.fetchChart()
})
</script>
