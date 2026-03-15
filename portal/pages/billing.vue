<template>
  <div class="space-y-6">
    <div class="flex items-start justify-between flex-wrap gap-4">
      <div>
        <h1 class="text-xl font-semibold text-surface-100">Fees & Billing</h1>
        <p class="text-sm text-surface-500 mt-1">Fee breakdown by corridor</p>
      </div>
      <div class="flex items-center gap-2">
        <input v-model="fromDate" type="date" class="input-field text-sm" />
        <span class="text-surface-500 text-sm">to</span>
        <input v-model="toDate" type="date" class="input-field text-sm" />
        <button class="btn-primary text-sm" @click="loadReport">Apply</button>
      </div>
    </div>

    <LoadingSpinner v-if="store.loading" size="lg" full-page />

    <template v-else>
      <!-- Total -->
      <div v-if="store.entries.length" class="bg-surface-900 rounded-lg border border-surface-800 p-5 flex items-center justify-between">
        <span class="text-sm text-surface-400">Total fees in period</span>
        <span class="text-xl font-semibold text-surface-100">
          <MoneyDisplay :amount="store.totalFeesUsd" currency="USD" />
        </span>
      </div>

      <DataTable
        v-if="store.entries.length"
        :columns="columns"
        :data="store.entries"
      />

      <EmptyState v-else title="No fee data" description="Fee data will appear once transfers are processed" icon="&#x2637;" />
    </template>
  </div>
</template>

<script setup lang="ts">
import type { FeeReportEntry, Column } from '~/types'

const store = useFeesStore()
const money = useMoney()

const fromDate = ref('')
const toDate = ref('')

const columns: Column<FeeReportEntry>[] = [
  {
    key: 'corridor',
    label: 'Corridor',
    sortable: true,
    render: (_, row) => `${row.source_currency} → ${row.dest_currency}`,
  },
  { key: 'transfer_count', label: 'Transfers', sortable: true, align: 'right' },
  {
    key: 'total_volume_usd',
    label: 'Volume (USD)',
    sortable: true,
    align: 'right',
    render: (v) => money.format(v, 'USD'),
  },
  {
    key: 'on_ramp_fees_usd',
    label: 'On-ramp Fee',
    align: 'right',
    render: (v) => money.format(v, 'USD'),
  },
  {
    key: 'network_fees_usd',
    label: 'Network Fee',
    align: 'right',
    render: (v) => money.format(v, 'USD'),
  },
  {
    key: 'off_ramp_fees_usd',
    label: 'Off-ramp Fee',
    align: 'right',
    render: (v) => money.format(v, 'USD'),
  },
  {
    key: 'total_fees_usd',
    label: 'Total Fee',
    sortable: true,
    align: 'right',
    render: (v) => money.format(v, 'USD'),
  },
]

function loadReport() {
  store.fetchReport(fromDate.value || undefined, toDate.value || undefined)
}

onMounted(() => {
  store.fetchReport()
})
</script>
