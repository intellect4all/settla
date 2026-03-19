<template>
  <div class="animate-fade-in">
    <div class="flex items-center justify-between mb-6">
      <div>
        <h1 class="text-2xl font-semibold text-surface-100">Transfers</h1>
        <p class="text-sm text-surface-500 mt-0.5">
          {{ transfersData?.total_count ?? 0 }} total transfers
        </p>
      </div>
      <AppButton variant="secondary" size="sm" icon="refresh-cw" @click="refresh">Refresh</AppButton>
    </div>

    <!-- Filters -->
    <div class="flex items-center gap-3 mb-4">
      <select v-model="statusFilter" class="input text-sm">
        <option value="">All statuses</option>
        <option v-for="s in statuses" :key="s" :value="s">{{ s }}</option>
      </select>
      <input
        v-model="searchQuery"
        type="text"
        placeholder="Search by ID or reference..."
        class="input text-sm flex-1 max-w-sm"
      />
    </div>

    <DataTable
      :columns="columns"
      :rows="filteredTransfers"
      row-key="id"
      :loading="loading"
      empty-message="No transfers match your filters"
      :searchable="false"
      :page-size="25"
      @row-click="goToTransfer"
    >
      <template #cell-status="{ value }">
        <StatusBadge :status="value" />
      </template>
      <template #cell-id="{ value }">
        <span class="font-mono text-xs text-surface-400">{{ value?.slice(0, 8) }}...</span>
      </template>
      <template #cell-amount="{ row }">
        <MoneyDisplay :amount="row.source_amount" :currency="row.source_currency" size="sm" />
      </template>
      <template #cell-corridor="{ row }">
        <span class="text-xs font-mono">{{ row.source_currency }} <Icon name="chevron-right" :size="12" class="inline-block mx-0.5" /> {{ row.dest_currency }}</span>
      </template>
      <template #cell-chain="{ value }">
        <span class="text-xs px-2 py-0.5 bg-surface-800 rounded text-surface-400">{{ value || '\u2014' }}</span>
      </template>
      <template #cell-created_at="{ value }">
        <span class="text-xs text-surface-400">{{ formatDate(value) }}</span>
      </template>
      <template #cell-duration="{ row }">
        <span class="text-xs text-surface-400">{{ getDuration(row) }}</span>
      </template>
    </DataTable>
  </div>
</template>

<script setup lang="ts">
import type { Transfer } from '~/types'

type TransferStatus =
  | 'CREATED'
  | 'FUNDED'
  | 'ON_RAMPING'
  | 'SETTLING'
  | 'OFF_RAMPING'
  | 'COMPLETING'
  | 'COMPLETED'
  | 'FAILED'
  | 'REFUNDING'
  | 'REFUNDED'

interface Column {
  key: string
  label: string
  width?: string
  sortable?: boolean
}

const router = useRouter()
const config = useRuntimeConfig()
const api = useApi()

const statusFilter = ref('')
const searchQuery = ref('')
const debouncedSearch = ref('')
let searchTimer: ReturnType<typeof setTimeout> | null = null

watch(searchQuery, (val) => {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => {
    debouncedSearch.value = val
  }, 300)
})

const statuses: TransferStatus[] = [
  'CREATED',
  'FUNDED',
  'ON_RAMPING',
  'SETTLING',
  'OFF_RAMPING',
  'COMPLETING',
  'COMPLETED',
  'FAILED',
  'REFUNDING',
  'REFUNDED',
]

const { data: transfersData, loading, refresh } = usePolling(
  () => api.listTransfers({
    page_size: 100,
    status: statusFilter.value || undefined,
    search: debouncedSearch.value || undefined,
  }),
  config.public.pollIntervalTransfers as number,
  { immediate: true },
)

// Server-side filtering: re-fetch when filters change
watch([statusFilter, debouncedSearch], () => {
  refresh()
})

const filteredTransfers = computed(() => transfersData.value?.transfers ?? [])

const columns: Column[] = [
  { key: 'status', label: 'Status', width: '140px' },
  { key: 'id', label: 'ID', width: '120px' },
  { key: 'amount', label: 'Amount' },
  { key: 'corridor', label: 'Corridor' },
  { key: 'chain', label: 'Chain', width: '100px' },
  { key: 'created_at', label: 'Created', sortable: true },
  { key: 'duration', label: 'Duration', width: '100px' },
]

function formatDate(ts: string) {
  if (!ts) return '\u2014'
  const d = new Date(ts)
  return (
    d.toLocaleDateString('en-GB', { day: '2-digit', month: 'short' }) +
    ' ' +
    d.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' })
  )
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
</script>
