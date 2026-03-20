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
          class="px-3 py-1.5 rounded-lg text-xs font-medium transition-colors focus-ring"
          @click="store.fetchAll(p.value)"
        >
          {{ p.label }}
        </button>
      </div>
    </div>

    <!-- Tabs -->
    <div class="flex gap-1 bg-surface-800 rounded-lg p-1 border border-surface-700">
      <button
        v-for="tab in tabs"
        :key="tab.key"
        :class="[
          activeTab === tab.key
            ? 'bg-violet-600 text-white'
            : 'text-surface-400 hover:text-surface-200',
        ]"
        class="px-3 py-1.5 rounded-md text-xs font-medium transition-colors focus-ring"
        @click="switchTab(tab.key)"
      >
        {{ tab.label }}
      </button>
    </div>

    <!-- ═══ Overview Tab ═══ -->
    <template v-if="activeTab === 'overview'">

    <!-- Skeleton loading for overview -->
    <template v-if="store.loading">
      <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
        <SkeletonLoader v-for="i in 4" :key="i" variant="card" height="100px" />
      </div>
      <div class="grid grid-cols-1 lg:grid-cols-3 gap-4">
        <div class="lg:col-span-2">
          <SkeletonLoader variant="chart" height="280px" />
        </div>
        <SkeletonLoader variant="chart" height="280px" />
      </div>
      <div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
        <SkeletonLoader variant="chart" height="240px" />
        <SkeletonLoader variant="card" height="240px" />
      </div>
    </template>

    <template v-else>
    <!-- Volume Comparison Cards -->
    <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
      <div v-for="(card, i) in overviewCards" :key="card.label" class="bg-surface-800 rounded-xl p-4 border border-surface-700 animate-slide-up" :style="{ animationDelay: `${i * 50}ms`, animationFillMode: 'backwards' }">
        <p class="text-xs text-surface-500 uppercase tracking-wide">{{ card.label }}</p>
        <p class="text-2xl font-bold text-surface-100 mt-1">{{ card.value }}</p>
        <ChangeIndicator v-if="card.change !== undefined" :value="card.change" label="vs prev period" />
      </div>
    </div>

    <!-- Charts Row -->
    <div class="grid grid-cols-1 lg:grid-cols-3 gap-4">
      <!-- Volume Chart (span 2) -->
      <div class="lg:col-span-2 bg-surface-800 rounded-xl p-4 border border-surface-700 animate-fade-in">
        <h3 class="text-sm font-medium text-surface-300 mb-3">Transfer Volume</h3>
        <div v-if="store.chartLoading" class="flex items-center justify-center h-60">
          <Icon name="loader" :size="20" class="animate-spin text-surface-500" />
        </div>
        <VolumeChart v-else :buckets="store.chartBuckets" :height="260" />
      </div>

      <!-- Corridor Pie -->
      <div class="bg-surface-800 rounded-xl p-4 border border-surface-700 animate-fade-in">
        <div class="flex items-center justify-between mb-3">
          <h3 class="text-sm font-medium text-surface-300">Corridors</h3>
          <select
            v-model="corridorMetric"
            class="input text-xs"
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
      <div class="bg-surface-800 rounded-xl p-4 border border-surface-700 animate-fade-in">
        <h3 class="text-sm font-medium text-surface-300 mb-3">Latency Percentiles</h3>
        <LatencyChart v-if="store.latency" :latency="store.latency" :height="200" />
        <p v-else class="text-surface-500 text-sm text-center py-12">No latency data</p>
        <p v-if="store.latency" class="text-xs text-surface-500 mt-2 text-center">
          Based on {{ store.latency.sample_count.toLocaleString() }} completed transfers
        </p>
      </div>

      <!-- Status Distribution -->
      <div class="bg-surface-800 rounded-xl p-4 border border-surface-700 animate-fade-in">
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
    <div class="bg-surface-800 rounded-xl p-4 border border-surface-700 animate-fade-in">
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
    <div class="bg-surface-800 rounded-xl p-4 border border-surface-700 animate-fade-in">
      <h3 class="text-sm font-medium text-surface-300 mb-3">Recent Activity</h3>
      <div v-if="store.activityLoading" class="py-4">
        <SkeletonLoader variant="table" :lines="5" />
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

    </template>
    </template>

    <!-- ═══ Fees Tab ═══ -->
    <template v-if="activeTab === 'fees'">
      <div class="bg-surface-800 rounded-xl p-4 border border-surface-700 animate-fade-in">
        <h3 class="text-sm font-medium text-surface-300 mb-3">Fee Breakdown by Corridor</h3>
        <div v-if="store.feeBreakdown.length" class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead>
              <tr class="text-surface-500 text-xs uppercase border-b border-surface-700">
                <th class="text-left py-2 px-3">Corridor</th>
                <th class="text-right py-2 px-3">Transfers</th>
                <th class="text-right py-2 px-3">Volume</th>
                <th class="text-right py-2 px-3">On-Ramp</th>
                <th class="text-right py-2 px-3">Off-Ramp</th>
                <th class="text-right py-2 px-3">Network</th>
                <th class="text-right py-2 px-3">Total</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="f in store.feeBreakdown" :key="`${f.source_currency}-${f.dest_currency}`" class="border-b border-surface-700/50">
                <td class="py-2 px-3 text-surface-200">{{ f.source_currency }} → {{ f.dest_currency }}</td>
                <td class="py-2 px-3 text-right text-surface-300">{{ f.transfer_count.toLocaleString() }}</td>
                <td class="py-2 px-3 text-right text-surface-300">${{ formatVolume(f.volume_usd) }}</td>
                <td class="py-2 px-3 text-right text-surface-300">${{ formatVolume(f.on_ramp_fees_usd) }}</td>
                <td class="py-2 px-3 text-right text-surface-300">${{ formatVolume(f.off_ramp_fees_usd) }}</td>
                <td class="py-2 px-3 text-right text-surface-300">${{ formatVolume(f.network_fees_usd) }}</td>
                <td class="py-2 px-3 text-right font-medium text-surface-100">${{ formatVolume(f.total_fees_usd) }}</td>
              </tr>
            </tbody>
            <tfoot>
              <tr class="border-t border-surface-600">
                <td colspan="6" class="py-2 px-3 text-right text-surface-400 font-medium">Total Fees</td>
                <td class="py-2 px-3 text-right font-bold text-surface-100">${{ formatVolume(store.totalFeesUsd) }}</td>
              </tr>
            </tfoot>
          </table>
        </div>
        <p v-else class="text-surface-500 text-sm text-center py-8">No fee data</p>
      </div>
    </template>

    <!-- ═══ Providers Tab ═══ -->
    <template v-if="activeTab === 'providers'">
      <div class="bg-surface-800 rounded-xl p-4 border border-surface-700 animate-fade-in">
        <h3 class="text-sm font-medium text-surface-300 mb-3">Provider Performance</h3>
        <div v-if="store.providerPerformance.length" class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead>
              <tr class="text-surface-500 text-xs uppercase border-b border-surface-700">
                <th class="text-left py-2 px-3">Provider</th>
                <th class="text-left py-2 px-3">Corridor</th>
                <th class="text-right py-2 px-3">Transactions</th>
                <th class="text-right py-2 px-3">Success Rate</th>
                <th class="text-right py-2 px-3">Avg Settlement</th>
                <th class="text-right py-2 px-3">Volume</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="p in store.providerPerformance" :key="`${p.provider}-${p.source_currency}-${p.dest_currency}`" class="border-b border-surface-700/50">
                <td class="py-2 px-3 text-surface-200 font-mono text-xs">{{ p.provider }}</td>
                <td class="py-2 px-3 text-surface-300">{{ p.source_currency }} → {{ p.dest_currency }}</td>
                <td class="py-2 px-3 text-right text-surface-300">{{ p.transaction_count.toLocaleString() }}</td>
                <td class="py-2 px-3 text-right">
                  <span :class="parseFloat(p.success_rate) >= 95 ? 'text-green-400' : parseFloat(p.success_rate) >= 90 ? 'text-amber-400' : 'text-red-400'">
                    {{ parseFloat(p.success_rate).toFixed(1) }}%
                  </span>
                </td>
                <td class="py-2 px-3 text-right text-surface-300">{{ p.avg_settlement_ms.toLocaleString() }}ms</td>
                <td class="py-2 px-3 text-right text-surface-300">${{ formatVolume(p.total_volume) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <p v-else class="text-surface-500 text-sm text-center py-8">No provider data</p>
      </div>
    </template>

    <!-- ═══ Deposits Tab ═══ -->
    <template v-if="activeTab === 'deposits'">
      <div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
        <div class="bg-surface-800 rounded-xl p-4 border border-surface-700 animate-fade-in">
          <h3 class="text-sm font-medium text-surface-300 mb-3">Crypto Deposits</h3>
          <div v-if="store.depositAnalytics?.crypto" class="space-y-3">
            <div class="grid grid-cols-2 gap-3">
              <div><p class="text-xs text-surface-500">Total Sessions</p><p class="text-lg font-bold text-surface-100">{{ store.depositAnalytics.crypto.total_sessions }}</p></div>
              <div><p class="text-xs text-surface-500">Conversion Rate</p><p class="text-lg font-bold text-surface-100">{{ store.depositAnalytics.crypto.conversion_rate }}%</p></div>
              <div><p class="text-xs text-surface-500">Total Received</p><p class="text-lg font-bold text-surface-100">${{ formatVolume(store.depositAnalytics.crypto.total_received) }}</p></div>
              <div><p class="text-xs text-surface-500">Total Fees</p><p class="text-lg font-bold text-surface-100">${{ formatVolume(store.depositAnalytics.crypto.total_fees) }}</p></div>
            </div>
          </div>
          <p v-else class="text-surface-500 text-sm text-center py-8">No crypto deposit data</p>
        </div>
        <div class="bg-surface-800 rounded-xl p-4 border border-surface-700 animate-fade-in">
          <h3 class="text-sm font-medium text-surface-300 mb-3">Bank Deposits</h3>
          <div v-if="store.depositAnalytics?.bank" class="space-y-3">
            <div class="grid grid-cols-2 gap-3">
              <div><p class="text-xs text-surface-500">Total Sessions</p><p class="text-lg font-bold text-surface-100">{{ store.depositAnalytics.bank.total_sessions }}</p></div>
              <div><p class="text-xs text-surface-500">Conversion Rate</p><p class="text-lg font-bold text-surface-100">{{ store.depositAnalytics.bank.conversion_rate }}%</p></div>
              <div><p class="text-xs text-surface-500">Total Received</p><p class="text-lg font-bold text-surface-100">${{ formatVolume(store.depositAnalytics.bank.total_received) }}</p></div>
              <div><p class="text-xs text-surface-500">Total Fees</p><p class="text-lg font-bold text-surface-100">${{ formatVolume(store.depositAnalytics.bank.total_fees) }}</p></div>
            </div>
          </div>
          <p v-else class="text-surface-500 text-sm text-center py-8">No bank deposit data</p>
        </div>
      </div>
    </template>

    <!-- ═══ Reconciliation Tab ═══ -->
    <template v-if="activeTab === 'reconciliation'">
      <div class="bg-surface-800 rounded-xl p-4 border border-surface-700 animate-fade-in">
        <h3 class="text-sm font-medium text-surface-300 mb-3">System Health</h3>
        <div v-if="store.reconciliation" class="grid grid-cols-2 lg:grid-cols-4 gap-4">
          <div>
            <p class="text-xs text-surface-500">Pass Rate</p>
            <p class="text-2xl font-bold" :class="parseFloat(store.reconciliation.pass_rate) >= 99 ? 'text-green-400' : 'text-amber-400'">
              {{ store.reconciliation.pass_rate }}%
            </p>
          </div>
          <div>
            <p class="text-xs text-surface-500">Total Runs</p>
            <p class="text-2xl font-bold text-surface-100">{{ store.reconciliation.total_runs }}</p>
          </div>
          <div>
            <p class="text-xs text-surface-500">Checks Failed</p>
            <p class="text-2xl font-bold" :class="store.reconciliation.checks_failed > 0 ? 'text-red-400' : 'text-green-400'">
              {{ store.reconciliation.checks_failed }}
            </p>
          </div>
          <div>
            <p class="text-xs text-surface-500">Needs Review</p>
            <p class="text-2xl font-bold" :class="store.reconciliation.needs_review_count > 0 ? 'text-amber-400' : 'text-green-400'">
              {{ store.reconciliation.needs_review_count }}
            </p>
          </div>
          <div class="col-span-2 lg:col-span-4">
            <p class="text-xs text-surface-500">Last Run</p>
            <p class="text-sm text-surface-300">{{ store.reconciliation.last_run_at ? new Date(store.reconciliation.last_run_at).toLocaleString() : 'Never' }}</p>
          </div>
        </div>
        <p v-else class="text-surface-500 text-sm text-center py-8">No reconciliation data</p>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
const store = useAnalyticsStore()

const periods = [
  { value: '24h', label: '24 Hours' },
  { value: '7d', label: '7 Days' },
  { value: '30d', label: '30 Days' },
]

const activeTab = ref('overview')

const tabs = [
  { key: 'overview', label: 'Overview' },
  { key: 'fees', label: 'Fees' },
  { key: 'providers', label: 'Providers' },
  { key: 'deposits', label: 'Deposits' },
  { key: 'reconciliation', label: 'Reconciliation' },
]

function switchTab(tab: string) {
  activeTab.value = tab
  if (tab === 'fees' && !store.feeBreakdown.length) store.fetchFeeBreakdown()
  if (tab === 'providers' && !store.providerPerformance.length) store.fetchProviderPerformance()
  if (tab === 'deposits') store.fetchDepositAnalytics()
  if (tab === 'reconciliation' && !store.reconciliation) store.fetchReconciliationSummary()
}

const corridorMetric = ref<'volume' | 'count' | 'fees'>('volume')

const overviewCards = computed(() => [
  { label: 'Total Transfers', value: store.totalTransfers.toLocaleString(), change: store.countChangePercent },
  { label: 'Volume (USD)', value: `$${formatVolume(store.comparison?.current_volume_usd)}`, change: store.volumeChangePercent },
  { label: 'Fees Collected', value: `$${formatVolume(store.comparison?.current_fees_usd)}`, change: undefined },
  { label: 'Success Rate', value: `${successRate.value}%`, change: undefined },
])

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
