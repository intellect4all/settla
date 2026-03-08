<template>
  <div>
    <div class="flex items-center justify-between mb-6">
      <div>
        <h1 class="text-2xl font-semibold text-surface-100">Transfers</h1>
        <p class="text-sm text-surface-500 mt-0.5">
          {{ transfersData?.total_count ?? 0 }} total transfers
        </p>
      </div>
      <button class="btn-secondary text-sm" @click="refresh">Refresh</button>
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
        <span class="text-xs font-mono">{{ row.source_currency }} &rarr; {{ row.dest_currency }}</span>
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
  () => api.listTransfers({ page_size: 100 }),
  config.public.pollIntervalTransfers as number,
  { immediate: true },
)

const filteredTransfers = computed(() => {
  let txns = transfersData.value?.transfers ?? []
  if (statusFilter.value) {
    txns = txns.filter((t: Transfer) => t.status === statusFilter.value)
  }
  if (searchQuery.value) {
    const q = searchQuery.value.toLowerCase()
    txns = txns.filter(
      (t: Transfer) =>
        t.id.toLowerCase().includes(q) ||
        (t.external_ref && t.external_ref.toLowerCase().includes(q)) ||
        (t.idempotency_key && t.idempotency_key.toLowerCase().includes(q)),
    )
  }
  return txns
})

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
