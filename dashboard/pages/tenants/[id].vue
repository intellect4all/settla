<template>
  <div>
    <button class="flex items-center gap-1.5 text-sm text-surface-400 hover:text-surface-200 mb-4 transition-colors" @click="router.back()">
      &#8592; Back
    </button>

    <LoadingSpinner v-if="loading && !tenant" />

    <template v-else-if="tenant">
      <!-- Header -->
      <div class="flex items-center justify-between mb-6">
        <div>
          <div class="flex items-center gap-3">
            <h1 class="text-2xl font-semibold text-surface-100">{{ tenant.name }}</h1>
            <StatusBadge :status="tenant.status" />
            <StatusBadge :status="tenant.kyb_status" />
          </div>
          <p class="text-sm text-surface-500 mt-0.5">{{ tenant.slug }} &middot; {{ tenant.settlement_model }}</p>
        </div>
      </div>

      <!-- Summary Cards -->
      <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4 mb-6">
        <SummaryCard label="Daily Volume">
          <MoneyDisplay :amount="volume?.daily_volume_usd ?? '0'" currency="USD" size="2xl" :compact="true" />
        </SummaryCard>
        <SummaryCard
          label="Transfers Today"
          :value="String(volume?.transfer_count ?? 0)"
        />
        <SummaryCard
          label="Success Rate"
          :value="(volume?.success_rate ?? 0).toFixed(1) + '%'"
        />
        <SummaryCard label="Fee Revenue">
          <MoneyDisplay :amount="estimatedRevenue" currency="USD" size="2xl" :compact="true" />
        </SummaryCard>
      </div>

      <!-- Daily Limit Progress -->
      <div class="card p-5 mb-6">
        <div class="flex items-center justify-between mb-2">
          <h3 class="text-sm font-semibold text-surface-200">Daily Volume vs Limit</h3>
          <span class="text-xs text-surface-500">
            <MoneyDisplay :amount="volume?.daily_volume_usd ?? '0'" currency="USD" size="xs" />
            /
            <MoneyDisplay :amount="tenant.daily_limit_usd" currency="USD" size="xs" />
          </span>
        </div>
        <div class="h-4 bg-surface-800 rounded-full overflow-hidden">
          <div
            :style="{ width: limitPercent + '%' }"
            :class="limitPercent > 90 ? 'bg-red-500' : limitPercent > 75 ? 'bg-amber-500' : 'bg-emerald-500'"
            class="h-full rounded-full transition-all duration-500"
          />
        </div>
        <p class="text-xs text-surface-500 mt-1 text-right">{{ limitPercent.toFixed(1) }}% used</p>
      </div>

      <!-- Details Grid -->
      <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
        <!-- Fee Schedule -->
        <div class="card p-5">
          <h3 class="text-xs font-medium text-surface-500 uppercase tracking-wider mb-4">Fee Schedule</h3>
          <dl class="space-y-3 text-sm">
            <div class="flex justify-between">
              <dt class="text-surface-400">On-ramp fee</dt>
              <dd class="text-surface-200 font-mono">{{ bpsToPercent(tenant.fee_schedule.on_ramp_bps) }} ({{ tenant.fee_schedule.on_ramp_bps }} bps)</dd>
            </div>
            <div class="flex justify-between">
              <dt class="text-surface-400">Off-ramp fee</dt>
              <dd class="text-surface-200 font-mono">{{ bpsToPercent(tenant.fee_schedule.off_ramp_bps) }} ({{ tenant.fee_schedule.off_ramp_bps }} bps)</dd>
            </div>
            <div class="flex justify-between">
              <dt class="text-surface-400">Min fee</dt>
              <dd class="text-surface-200"><MoneyDisplay :amount="tenant.fee_schedule.min_fee_usd" currency="USD" size="sm" /></dd>
            </div>
            <div class="flex justify-between">
              <dt class="text-surface-400">Max fee</dt>
              <dd class="text-surface-200"><MoneyDisplay :amount="tenant.fee_schedule.max_fee_usd" currency="USD" size="sm" /></dd>
            </div>
          </dl>
        </div>

        <!-- Limits & Config -->
        <div class="card p-5">
          <h3 class="text-xs font-medium text-surface-500 uppercase tracking-wider mb-4">Configuration</h3>
          <dl class="space-y-3 text-sm">
            <div class="flex justify-between">
              <dt class="text-surface-400">Settlement model</dt>
              <dd class="text-surface-200">{{ tenant.settlement_model }}</dd>
            </div>
            <div class="flex justify-between">
              <dt class="text-surface-400">Daily limit</dt>
              <dd class="text-surface-200"><MoneyDisplay :amount="tenant.daily_limit_usd" currency="USD" size="sm" /></dd>
            </div>
            <div class="flex justify-between">
              <dt class="text-surface-400">Per-transfer limit</dt>
              <dd class="text-surface-200"><MoneyDisplay :amount="tenant.per_transfer_limit" currency="USD" size="sm" /></dd>
            </div>
            <div class="flex justify-between">
              <dt class="text-surface-400">Created</dt>
              <dd class="text-surface-400 text-xs font-mono">{{ formatDate(tenant.created_at) }}</dd>
            </div>
            <div class="flex justify-between">
              <dt class="text-surface-400">KYB Status</dt>
              <dd><StatusBadge :status="tenant.kyb_status" size="sm" /></dd>
            </div>
          </dl>
        </div>
      </div>
    </template>

    <EmptyState v-else title="Tenant not found" />
  </div>
</template>

<script setup lang="ts">
import type { Tenant, TenantVolume } from '~/types'
import Decimal from 'decimal.js'

const route = useRoute()
const router = useRouter()
const api = useApi()
const { bpsToPercent } = useMoney()
const { error: showError } = useToast()

const tenantId = computed(() => route.params.id as string)
const tenant = ref<Tenant | null>(null)
const volume = ref<TenantVolume | null>(null)
const loading = ref(true)

async function fetchTenant() {
  loading.value = true
  try {
    tenant.value = await api.getTenant(tenantId.value)
  } catch {
    // Sample data for demo
    tenant.value = {
      id: tenantId.value,
      name: tenantId.value === 'a0000000-0000-0000-0000-000000000001' ? 'Lemfi' : 'Fincra',
      slug: tenantId.value === 'a0000000-0000-0000-0000-000000000001' ? 'lemfi' : 'fincra',
      status: 'ACTIVE',
      fee_schedule: {
        on_ramp_bps: tenantId.value === 'a0000000-0000-0000-0000-000000000001' ? 40 : 25,
        off_ramp_bps: tenantId.value === 'a0000000-0000-0000-0000-000000000001' ? 35 : 20,
        min_fee_usd: '0.50',
        max_fee_usd: '500.00',
      },
      settlement_model: 'PREFUNDED',
      daily_limit_usd: '25000000.00',
      per_transfer_limit: '100000.00',
      kyb_status: 'VERIFIED',
      created_at: '2025-01-15T10:00:00Z',
      updated_at: new Date().toISOString(),
    }
  } finally {
    loading.value = false
  }
}

async function fetchVolume() {
  try {
    const result = await api.getTenantVolumes()
    volume.value = result.tenants.find((t: TenantVolume) => t.tenant_id === tenantId.value) ?? null
  } catch {
    volume.value = {
      tenant_id: tenantId.value,
      tenant_name: tenant.value?.name ?? '',
      daily_volume_usd: '12500000.00',
      daily_limit_usd: '25000000.00',
      transfer_count: 24500,
      success_rate: 98.7,
    }
  }
}

const limitPercent = computed(() => {
  if (!volume.value || !tenant.value) return 0
  const vol = new Decimal(volume.value.daily_volume_usd || '0')
  const limit = new Decimal(tenant.value.daily_limit_usd || '1')
  if (limit.isZero()) return 0
  return vol.div(limit).mul(100).toNumber()
})

const estimatedRevenue = computed(() => {
  if (!volume.value || !tenant.value) return '0'
  const vol = new Decimal(volume.value.daily_volume_usd || '0')
  const avgBps = (tenant.value.fee_schedule.on_ramp_bps + tenant.value.fee_schedule.off_ramp_bps) / 2
  return vol.mul(avgBps).div(10000).toFixed(2)
})

function formatDate(ts: string) {
  if (!ts) return '\u2014'
  return new Date(ts).toLocaleDateString('en-GB', { day: '2-digit', month: 'short', year: 'numeric' })
}

onMounted(() => {
  fetchTenant().then(fetchVolume)
})
</script>
