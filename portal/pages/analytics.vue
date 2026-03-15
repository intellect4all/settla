<template>
  <div class="space-y-6">
    <!-- Header -->
    <div class="flex items-center justify-between">
      <h1 class="text-xl font-semibold text-surface-100">Analytics</h1>
      <div class="flex items-center gap-2">
        <button
          v-for="p in periods"
          :key="p.value"
          :class="[
            store.period === p.value
              ? 'bg-violet-600 text-white'
              : 'bg-surface-800 text-surface-400 hover:text-surface-200',
          ]"
          class="px-3 py-1.5 rounded-lg text-xs font-medium transition-colors"
          @click="store.fetchAll(p.value)"
        >
          {{ p.label }}
        </button>
      </div>
    </div>

    <!-- Volume Comparison Cards -->
    <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
      <div class="bg-surface-800 rounded-xl p-4 border border-surface-700">
        <p class="text-xs text-surface-500 uppercase tracking-wide">Total Transfers</p>
        <p class="text-2xl font-bold text-surface-100 mt-1">{{ store.totalTransfers.toLocaleString() }}</p>
        <ChangeIndicator :value="store.countChangePercent" label="vs prev period" />
      </div>
      <div class="bg-surface-800 rounded-xl p-4 border border-surface-700">
        <p class="text-xs text-surface-500 uppercase tracking-wide">Volume (USD)</p>
        <p class="text-2xl font-bold text-surface-100 mt-1">${{ formatVolume(store.comparison?.current_volume_usd) }}</p>
        <ChangeIndicator :value="store.volumeChangePercent" label="vs prev period" />
      </div>
      <div class="bg-surface-800 rounded-xl p-4 border border-surface-700">
        <p class="text-xs text-surface-500 uppercase tracking-wide">Fees Collected</p>
        <p class="text-2xl font-bold text-surface-100 mt-1">${{ formatVolume(store.comparison?.current_fees_usd) }}</p>
      </div>
      <div class="bg-surface-800 rounded-xl p-4 border border-surface-700">
        <p class="text-xs text-surface-500 uppercase tracking-wide">Success Rate</p>
        <p class="text-2xl font-bold text-surface-100 mt-1">{{ successRate }}%</p>
      </div>
    </div>

    <!-- Charts Row -->
    <div class="grid grid-cols-1 lg:grid-cols-3 gap-4">
      <!-- Volume Chart (span 2) -->
      <div class="lg:col-span-2 bg-surface-800 rounded-xl p-4 border border-surface-700">
        <h3 class="text-sm font-medium text-surface-300 mb-3">Transfer Volume</h3>
        <div v-if="store.chartLoading" class="flex items-center justify-center h-60">
          <span class="text-surface-500 text-sm">Loading...</span>
        </div>
        <VolumeChart v-else :buckets="store.chartBuckets" :height="260" />
      </div>

      <!-- Corridor Pie -->
      <div class="bg-surface-800 rounded-xl p-4 border border-surface-700">
        <div class="flex items-center justify-between mb-3">
          <h3 class="text-sm font-medium text-surface-300">Corridors</h3>
          <select
            v-model="corridorMetric"
            class="text-xs bg-surface-700 text-surface-300 border border-surface-600 rounded px-2 py-1"
          >
            <option value="volume">Volume</option>
            <option value="count">Count</option>
            <option value="fees">Fees</option>
          </select>
        </div>
        <CorridorChart
          v-if="store.corridors.length"
          :corridors="store.topCorridors"
          :metric="corridorMetric"
          :height="220"
        />
        <p v-else class="text-surface-500 text-sm text-center py-16">No corridor data</p>
      </div>
    </div>

    <!-- Latency + Status Row -->
    <div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
      <!-- Latency -->
      <div class="bg-surface-800 rounded-xl p-4 border border-surface-700">
        <h3 class="text-sm font-medium text-surface-300 mb-3">Latency Percentiles</h3>
        <LatencyChart v-if="store.latency" :latency="store.latency" :height="200" />
        <p v-else class="text-surface-500 text-sm text-center py-12">No latency data</p>
        <p v-if="store.latency" class="text-xs text-surface-500 mt-2 text-center">
          Based on {{ store.latency.sample_count.toLocaleString() }} completed transfers
        </p>
      </div>

      <!-- Status Distribution -->
      <div class="bg-surface-800 rounded-xl p-4 border border-surface-700">
        <h3 class="text-sm font-medium text-surface-300 mb-3">Status Distribution</h3>
        <div v-if="store.statusDistribution.length" class="space-y-2">
          <div
            v-for="s in store.statusDistribution"
            :key="s.status"
            class="flex items-center gap-3"
          >
            <span
              class="w-2.5 h-2.5 rounded-full shrink-0"
              :class="statusColor(s.status)"
            />
            <span class="text-sm text-surface-300 flex-1">{{ s.status }}</span>
            <span class="text-sm font-medium text-surface-100">{{ s.count.toLocaleString() }}</span>
            <span class="text-xs text-surface-500 w-12 text-right">
              {{ store.totalTransfers ? ((s.count / store.totalTransfers) * 100).toFixed(1) : 0 }}%
            </span>
          </div>
        </div>
        <p v-else class="text-surface-500 text-sm text-center py-12">No data</p>
      </div>
    </div>

    <!-- Corridor Table -->
    <div class="bg-surface-800 rounded-xl p-4 border border-surface-700">
      <h3 class="text-sm font-medium text-surface-300 mb-3">Corridor Performance</h3>
      <div v-if="store.corridors.length" class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead>
            <tr class="text-surface-500 text-xs uppercase border-b border-surface-700">
              <th class="text-left py-2 px-3">Corridor</th>
              <th class="text-right py-2 px-3">Transfers</th>
              <th class="text-right py-2 px-3">Volume</th>
              <th class="text-right py-2 px-3">Fees</th>
              <th class="text-right py-2 px-3">Success</th>
              <th class="text-right py-2 px-3">Avg Latency</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="c in store.corridors"
              :key="`${c.source_currency}-${c.dest_currency}`"
              class="border-b border-surface-700/50 hover:bg-surface-700/30"
            >
              <td class="py-2 px-3 text-surface-200">
                {{ c.source_currency }} → {{ c.dest_currency }}
              </td>
              <td class="py-2 px-3 text-right text-surface-300">{{ c.transfer_count.toLocaleString() }}</td>
              <td class="py-2 px-3 text-right text-surface-300">${{ formatVolume(c.volume_usd) }}</td>
              <td class="py-2 px-3 text-right text-surface-300">${{ formatVolume(c.fees_usd) }}</td>
              <td class="py-2 px-3 text-right">
                <span :class="parseFloat(c.success_rate) >= 95 ? 'text-green-400' : parseFloat(c.success_rate) >= 90 ? 'text-amber-400' : 'text-red-400'">
                  {{ parseFloat(c.success_rate).toFixed(1) }}%
                </span>
              </td>
              <td class="py-2 px-3 text-right text-surface-300">{{ c.avg_latency_ms.toLocaleString() }}ms</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-else class="text-surface-500 text-sm text-center py-8">No corridor data</p>
    </div>

    <!-- Recent Activity -->
    <div class="bg-surface-800 rounded-xl p-4 border border-surface-700">
      <h3 class="text-sm font-medium text-surface-300 mb-3">Recent Activity</h3>
      <div v-if="store.activityLoading" class="text-center py-8">
        <span class="text-surface-500 text-sm">Loading...</span>
      </div>
      <div v-else-if="store.activity.length" class="space-y-2">
        <NuxtLink
          v-for="item in store.activity"
          :key="item.transfer_id"
          :to="`/transfers/${item.transfer_id}`"
          class="flex items-center gap-3 px-3 py-2 rounded-lg hover:bg-surface-700/50 transition-colors"
        >
          <span
            class="w-2 h-2 rounded-full shrink-0"
            :class="statusColor(item.status)"
          />
          <span class="text-sm text-surface-300 truncate flex-1">
            {{ item.external_ref || item.transfer_id.slice(0, 8) }}
          </span>
          <span class="text-xs text-surface-400">
            {{ item.source_amount }} {{ item.source_currency }} → {{ item.dest_amount }} {{ item.dest_currency }}
          </span>
          <span
            class="text-xs px-2 py-0.5 rounded-full"
            :class="statusBadge(item.status)"
          >
            {{ item.status }}
          </span>
          <span class="text-xs text-surface-500">
            {{ timeAgo(item.updated_at) }}
          </span>
        </NuxtLink>
      </div>
      <p v-else class="text-surface-500 text-sm text-center py-8">No recent activity</p>
    </div>
  </div>
</template>

<script setup lang="ts">
const store = useAnalyticsStore()

const periods = [
  { value: '24h', label: '24 Hours' },
  { value: '7d', label: '7 Days' },
  { value: '30d', label: '30 Days' },
]

const corridorMetric = ref<'volume' | 'count' | 'fees'>('volume')

const successRate = computed(() => {
  const completed = store.statusDistribution.find(s => s.status === 'COMPLETED')?.count || 0
  const total = store.totalTransfers
  if (!total) return '0.0'
  return ((completed / total) * 100).toFixed(1)
})

function formatVolume(val?: string) {
  if (!val) return '0'
  const n = parseFloat(val)
  if (n >= 1e6) return `${(n / 1e6).toFixed(2)}M`
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}K`
  return n.toFixed(2)
}

function statusColor(status: string) {
  const map: Record<string, string> = {
    COMPLETED: 'bg-green-400',
    FAILED: 'bg-red-400',
    REFUNDED: 'bg-amber-400',
    CREATED: 'bg-blue-400',
    FUNDED: 'bg-cyan-400',
    ON_RAMPING: 'bg-violet-400',
    SETTLING: 'bg-violet-400',
    OFF_RAMPING: 'bg-indigo-400',
    COMPLETING: 'bg-emerald-400',
    REFUNDING: 'bg-orange-400',
  }
  return map[status] || 'bg-surface-500'
}

function statusBadge(status: string) {
  const map: Record<string, string> = {
    COMPLETED: 'bg-green-900/50 text-green-400',
    FAILED: 'bg-red-900/50 text-red-400',
    REFUNDED: 'bg-amber-900/50 text-amber-400',
  }
  return map[status] || 'bg-surface-700 text-surface-400'
}

function timeAgo(iso: string) {
  const diff = Date.now() - new Date(iso).getTime()
  const mins = Math.floor(diff / 60000)
  if (mins < 1) return 'just now'
  if (mins < 60) return `${mins}m ago`
  const hrs = Math.floor(mins / 60)
  if (hrs < 24) return `${hrs}h ago`
  return `${Math.floor(hrs / 24)}d ago`
}

onMounted(() => {
  store.fetchAll()
})
</script>
