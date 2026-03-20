<template>
  <div class="space-y-6">
    <div>
      <h1 class="text-xl font-semibold text-surface-100">Transfers</h1>
      <p class="text-sm text-surface-500 mt-1">View and track your settlement transfers</p>
    </div>

    <template v-if="store.loading && !store.transfers.length">
      <div class="card p-4">
        <SkeletonLoader variant="table" :lines="8" />
      </div>
    </template>

    <template v-else>
      <DataTable
        v-if="store.transfers.length"
        :columns="columns"
        :rows="store.transfers"
        :loading="store.loading"
        searchable
        :search-keys="['id', 'external_ref', 'status', 'source_currency', 'dest_currency']"
        @row-click="(row: Transfer) => navigateTo(`/transfers/${row.id}`)"
      />

      <EmptyState v-else title="No transfers" description="Your settlement transfers will appear here" icon="arrows-right-left" />

      <div v-if="store.nextPageToken" class="flex justify-center">
        <AppButton :loading="store.loading" @click="store.fetchTransfers(store.nextPageToken)">
          Load more
        </AppButton>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import type { Transfer, Column } from '~/types'

const store = useTransferStore()
const money = useMoney()

const columns: Column<Transfer>[] = [
  { key: 'external_ref', label: 'Reference', sortable: true },
  { key: 'status', label: 'Status', sortable: true },
  {
    key: 'source_amount',
    label: 'Send',
    sortable: true,
    align: 'right',
    render: (_, row) => `${money.format(row.source_amount, row.source_currency)} ${row.source_currency}`,
  },
  {
    key: 'dest_amount',
    label: 'Receive',
    sortable: true,
    align: 'right',
    render: (_, row) => `${money.format(row.dest_amount, row.dest_currency)} ${row.dest_currency}`,
  },
  {
    key: 'fees',
    label: 'Fee',
    align: 'right',
    render: (_, row) => money.format(row.fees?.total_fee_usd ?? '0', 'USD'),
  },
  {
    key: 'created_at',
    label: 'Created',
    sortable: true,
    render: (v) => new Date(v).toLocaleDateString('en-GB', { day: '2-digit', month: 'short', hour: '2-digit', minute: '2-digit' }),
  },
]

onMounted(() => {
  store.fetchTransfers()
})
</script>
