<template>
  <div class="animate-fade-in">
    <div class="flex items-center justify-between mb-6">
      <div>
        <h1 class="text-2xl font-semibold text-surface-100">Tenants</h1>
        <p class="text-sm text-surface-500 mt-0.5">Manage fintech tenants, KYB, and limits</p>
      </div>
      <AppButton variant="secondary" size="sm" icon="refresh-cw" @click="fetchTenants">Refresh</AppButton>
    </div>

    <!-- Sample data notice -->
    <AlertBanner
      v-if="usingSampleData"
      type="info"
      title="Showing sample data"
      description="Live tenant data will appear when the gateway is connected."
      class="mb-6"
    />

    <!-- Filters -->
    <div class="flex items-center gap-3 mb-4">
      <select v-model="statusFilter" class="input text-sm">
        <option value="">All statuses</option>
        <option value="ACTIVE">Active</option>
        <option value="SUSPENDED">Suspended</option>
        <option value="ONBOARDING">Onboarding</option>
      </select>
      <select v-model="kybFilter" class="input text-sm">
        <option value="">All KYB</option>
        <option value="VERIFIED">Verified</option>
        <option value="PENDING">Pending</option>
        <option value="IN_REVIEW">In Review</option>
        <option value="REJECTED">Rejected</option>
      </select>
    </div>

    <DataTable
      :columns="columns"
      :rows="filteredTenants"
      row-key="id"
      :loading="loading"
      empty-message="No tenants match your filters"
      :searchable="false"
      :page-size="25"
      @row-click="goToTenant"
    >
      <template #cell-name="{ row }">
        <div>
          <span class="text-surface-200 font-medium">{{ row.name }}</span>
          <span class="text-xs text-surface-500 ml-2">{{ row.slug }}</span>
        </div>
      </template>
      <template #cell-status="{ value }">
        <StatusBadge :status="value" />
      </template>
      <template #cell-kyb_status="{ value }">
        <StatusBadge :status="value" />
      </template>
      <template #cell-settlement_model="{ value }">
        <span class="text-xs px-2 py-0.5 bg-surface-800 rounded text-surface-400">{{ value }}</span>
      </template>
      <template #cell-daily_limit_usd="{ value }">
        <MoneyDisplay :amount="value" currency="USD" size="sm" :compact="true" />
      </template>
      <template #cell-created_at="{ value }">
        <span class="text-xs text-surface-400">{{ formatDate(value) }}</span>
      </template>
    </DataTable>
  </div>
</template>

<script setup lang="ts">
import type { Tenant } from '~/types'

const router = useRouter()
const api = useApi()

const tenants = ref<Tenant[]>([])
const loading = ref(true)
const usingSampleData = ref(false)
const statusFilter = ref('')
const kybFilter = ref('')

const columns = [
  { key: 'name', label: 'Tenant', sortable: true },
  { key: 'status', label: 'Status', sortable: true },
  { key: 'kyb_status', label: 'KYB', sortable: true },
  { key: 'settlement_model', label: 'Model', sortable: true },
  { key: 'daily_limit_usd', label: 'Daily Limit', sortable: true },
  { key: 'created_at', label: 'Created', sortable: true },
]

const filteredTenants = computed(() => {
  return tenants.value.filter(t => {
    if (statusFilter.value && t.status !== statusFilter.value) return false
    if (kybFilter.value && t.kyb_status !== kybFilter.value) return false
    return true
  })
})

async function fetchTenants() {
  loading.value = true
  try {
    const result = await api.listTenants({ limit: 100 })
    tenants.value = result.tenants ?? []
    usingSampleData.value = false
  } catch {
    usingSampleData.value = true
    tenants.value = [
      {
        id: 'a0000000-0000-0000-0000-000000000001',
        name: 'Lemfi',
        slug: 'lemfi',
        status: 'ACTIVE',
        fee_schedule: { on_ramp_bps: 40, off_ramp_bps: 35, min_fee_usd: '0.50', max_fee_usd: '500.00' },
        settlement_model: 'PREFUNDED',
        daily_limit_usd: '25000000.00',
        per_transfer_limit: '100000.00',
        kyb_status: 'VERIFIED',
        created_at: '2025-01-15T10:00:00Z',
        updated_at: new Date().toISOString(),
      },
      {
        id: 'b0000000-0000-0000-0000-000000000002',
        name: 'Fincra',
        slug: 'fincra',
        status: 'ACTIVE',
        fee_schedule: { on_ramp_bps: 25, off_ramp_bps: 20, min_fee_usd: '0.25', max_fee_usd: '250.00' },
        settlement_model: 'NET_SETTLEMENT',
        daily_limit_usd: '50000000.00',
        per_transfer_limit: '250000.00',
        kyb_status: 'VERIFIED',
        created_at: '2025-02-01T14:00:00Z',
        updated_at: new Date().toISOString(),
      },
    ]
  } finally {
    loading.value = false
  }
}

function goToTenant(row: Tenant) {
  router.push(`/tenants/${row.id}`)
}

function formatDate(ts: string) {
  if (!ts) return '\u2014'
  return new Date(ts).toLocaleDateString('en-GB', { day: '2-digit', month: 'short', year: 'numeric' })
}

onMounted(fetchTenants)
</script>
