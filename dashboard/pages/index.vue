<template>
  <div>
    <!-- Header -->
    <div class="flex items-center justify-between mb-6">
      <div>
        <h1 class="text-2xl font-semibold text-surface-100">Dashboard</h1>
        <p class="text-sm text-surface-500 mt-0.5">Settlement engine overview</p>
      </div>
      <div class="flex items-center gap-3">
        <span class="text-xs text-surface-600">
          Updated {{ lastUpdated }}
        </span>
        <button class="btn-secondary text-xs" @click="refreshAll">
          Refresh
        </button>
      </div>
    </div>

    <!-- Summary Cards -->
    <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4 mb-6">
      <SummaryCard
        label="Transfers Today"
        :value="String(summary.totalTransfers)"
        :subtitle="`${summary.inProgress} in progress`"
      />
      <SummaryCard
        label="Success Rate"
        :value="summary.successRate + '%'"
        subtitle="Completed / Total"
      />
      <SummaryCard label="Volume Processed">
        <template #default>
          <MoneyDisplay :amount="summary.volumeUSD" currency="USD" size="xl" :compact="true" />
        </template>
      </SummaryCard>
      <SummaryCard
        label="Active Positions"
        :value="String(positionsData?.positions?.length ?? 0)"
        :subtitle="alertCount > 0 ? `${alertCount} alerts` : 'All healthy'"
      />
    </div>

    <!-- Alerts -->
    <div v-if="alertPositions.length > 0" class="space-y-2 mb-6">
      <AlertBanner
        v-for="pos in alertPositions"
        :key="pos.id"
        type="warning"
        :title="`Low liquidity: ${pos.currency} at ${pos.location}`"
        :description="`Balance ${format(pos.balance, pos.currency)} is below minimum ${format(pos.min_balance, pos.currency)}`"
      />
    </div>

    <!-- Content Grid -->
    <div class="grid grid-cols-1 xl:grid-cols-3 gap-6">
      <!-- Recent Transfers (2/3 width) -->
      <div class="xl:col-span-2">
        <DataTable
          title="Recent Transfers"
          :columns="transferColumns"
          :rows="transfers"
          row-key="id"
          :loading="transfersLoading"
          empty-message="No transfers yet"
          @row-click="goToTransfer"
        >
          <template #cell-status="{ value }">
            <StatusBadge :status="value" />
          </template>
          <template #cell-amount="{ row }">
            <MoneyDisplay :amount="row.source_amount" :currency="row.source_currency" size="sm" />
          </template>
          <template #cell-corridor="{ row }">
            <span class="text-xs font-mono">
              {{ row.source_currency }} &rarr; {{ row.dest_currency }}
            </span>
          </template>
          <template #cell-created_at="{ value }">
            <span class="text-xs text-surface-400">{{ formatTime(value) }}</span>
          </template>
          <template #cell-duration="{ row }">
            <span class="text-xs text-surface-400">{{ getDuration(row) }}</span>
          </template>
        </DataTable>
      </div>

      <!-- Liquidity Health (1/3 width) -->
      <div>
        <h3 class="text-sm font-semibold text-surface-200 mb-3">Liquidity Health</h3>
        <div v-if="positionsLoading" class="py-8">
          <LoadingSpinner />
        </div>
        <div v-else class="space-y-3">
          <PositionCard
            v-for="pos in topPositions"
            :key="pos.id"
            :position="pos"
          />
          <p v-if="!topPositions.length" class="text-sm text-surface-500 text-center py-4">
            No positions found
          </p>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import type { Transfer, Position } from '~/types'
import Decimal from 'decimal.js'

interface Column {
  key: string
  label: string
  width?: string
  sortable?: boolean
}

const router = useRouter()
const config = useRuntimeConfig()
const api = useApi()
const { format } = useMoney()
const { error: showError } = useToast()

// Transfer polling
const { data: transfersData, loading: transfersLoading, refresh: refreshTransfers } = usePolling(
  () => api.listTransfers({ page_size: 20 }),
  config.public.pollIntervalTransfers as number,
  { immediate: true },
)

// Positions polling
const { data: positionsData, loading: positionsLoading, refresh: refreshPositions } = usePolling(
  () => api.getPositions(),
  config.public.pollIntervalTreasury as number,
  { immediate: true },
)

// Liquidity polling
const { data: liquidityData, refresh: refreshLiquidity } = usePolling(
  () => api.getLiquidity().catch(() => null),
  config.public.pollIntervalTreasury as number,
  { immediate: true },
)

const transfers = computed(() => transfersData.value?.transfers ?? [])

const summary = computed(() => {
  const txns = transfers.value
  const total = txns.length
  const completed = txns.filter((t: Transfer) => t.status === 'COMPLETED').length
  const inProgress = txns.filter((t: Transfer) =>
    !['COMPLETED', 'FAILED', 'REFUNDED'].includes(t.status),
  ).length
  const successRate = total > 0 ? ((completed / total) * 100).toFixed(1) : '0.0'

  let volume = new Decimal(0)
  for (const t of txns) {
    if (t.status === 'COMPLETED' && t.source_amount) {
      volume = volume.add(new Decimal(t.source_amount))
    }
  }

  return {
    totalTransfers: total,
    inProgress,
    successRate,
    volumeUSD: volume.toFixed(2),
  }
})

const alertPositions = computed(() => liquidityData.value?.alert_positions ?? [])
const alertCount = computed(() => alertPositions.value.length)

const topPositions = computed(() => {
  const positions = positionsData.value?.positions ?? []
  return positions.slice(0, 6)
})

const lastUpdated = computed(() => {
  return new Date().toLocaleTimeString('en-GB', {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
})

const transferColumns: Column[] = [
  { key: 'status', label: 'Status', width: '140px' },
  { key: 'amount', label: 'Amount' },
  { key: 'corridor', label: 'Corridor' },
  { key: 'created_at', label: 'Created', sortable: true },
  { key: 'duration', label: 'Duration', width: '100px' },
]

function formatTime(ts: string) {
  if (!ts) return '\u2014'
  const d = new Date(ts)
  return d.toLocaleTimeString('en-GB', {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
}

function getDuration(t: Transfer) {
  if (!t.created_at) return '\u2014'
  const end = t.completed_at || t.failed_at || new Date().toISOString()
  const ms = new Date(end).getTime() - new Date(t.created_at).getTime()
  if (ms < 1000) return `${ms}ms`
  if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
  return `${(ms / 60000).toFixed(1)}min`
}

function goToTransfer(row: Transfer) {
  router.push(`/transfers/${row.id}`)
}

function refreshAll() {
  refreshTransfers()
  refreshPositions()
  refreshLiquidity()
}
</script>
