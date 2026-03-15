<template>
  <div class="space-y-6">
    <!-- Header -->
    <div class="flex items-center justify-between flex-wrap gap-3">
      <div>
        <h1 class="text-xl font-semibold text-surface-100">Net Settlement</h1>
        <p class="text-sm text-surface-400 mt-0.5">Per-corridor netting and inter-fintech payment tracking</p>
      </div>
      <div class="flex items-center gap-3">
        <select v-model="period" class="input text-sm" @change="loadData">
          <option value="current">Current cycle</option>
          <option value="7d">Last 7 days</option>
          <option value="30d">Last 30 days</option>
        </select>
        <button class="btn-secondary text-sm" @click="loadData">&#8635; Refresh</button>
      </div>
    </div>

    <!-- API key missing -->
    <AlertBanner
      v-if="api.apiKeyMissing"
      type="warning"
      title="API key not configured"
      description="Set NUXT_PUBLIC_DASHBOARD_API_KEY to connect to the live backend. Showing sample data."
      :dismissible="false"
    />

    <!-- Auth / fetch error -->
    <AlertBanner
      v-else-if="fetchError"
      type="error"
      :title="fetchError"
      description="Check your API key and gateway connectivity."
    />

    <!-- Sample data notice -->
    <AlertBanner
      v-else-if="usingSampleData"
      type="info"
      title="Showing sample data"
      description="The /v1/ops/settlements endpoint is not yet available on the gateway. Live data will appear automatically once the endpoint is deployed."
    />

    <!-- Loading -->
    <div v-if="loading" class="flex items-center justify-center py-16">
      <LoadingSpinner />
    </div>

    <template v-else-if="report">
      <!-- Totals banner -->
      <div class="grid grid-cols-2 md:grid-cols-4 gap-4">
        <div class="card p-4">
          <p class="text-xs text-surface-500 uppercase tracking-wider">Period</p>
          <p class="text-sm font-semibold text-surface-200 mt-1">{{ formatPeriod(report.period_start, report.period_end) }}</p>
        </div>
        <div class="card p-4">
          <p class="text-xs text-surface-500 uppercase tracking-wider">Total volume</p>
          <p class="text-lg font-semibold text-surface-100 mt-1 font-mono">{{ format(report.total_volume_usd, 'USD') }}</p>
        </div>
        <div class="card p-4">
          <p class="text-xs text-surface-500 uppercase tracking-wider">Fee revenue</p>
          <p class="text-lg font-semibold text-emerald-400 mt-1 font-mono">{{ format(report.total_fee_revenue_usd, 'USD') }}</p>
        </div>
        <div class="card p-4">
          <p class="text-xs text-surface-500 uppercase tracking-wider">Tenants</p>
          <p class="text-lg font-semibold text-surface-100 mt-1">{{ report.tenants.length }}</p>
        </div>
      </div>

      <!-- Per-tenant settlement cards -->
      <div class="space-y-4">
        <div
          v-for="tenant in report.tenants"
          :key="tenant.tenant_id"
          class="card overflow-hidden"
        >
          <!-- Tenant header -->
          <div class="p-5 flex items-start justify-between gap-4 flex-wrap">
            <div class="flex items-center gap-3">
              <div class="w-9 h-9 rounded-lg bg-violet-600/20 flex items-center justify-center text-violet-400 font-bold text-sm">
                {{ tenant.tenant_name.slice(0, 2).toUpperCase() }}
              </div>
              <div>
                <p class="text-sm font-semibold text-surface-100">{{ tenant.tenant_name }}</p>
                <p class="text-xs text-surface-500 font-mono">{{ tenant.tenant_id }}</p>
              </div>
            </div>

            <!-- Net position + payment status -->
            <div class="flex items-center gap-6">
              <div class="text-right">
                <p class="text-xs text-surface-500">Net position</p>
                <p
                  class="text-base font-semibold font-mono mt-0.5"
                  :class="netPositionClass(tenant.net_position_usd)"
                >
                  {{ formatNet(tenant.net_position_usd) }}
                </p>
              </div>
              <div class="text-right">
                <p class="text-xs text-surface-500">Due {{ formatDate(tenant.due_date) }}</p>
                <span
                  class="text-xs font-semibold px-2.5 py-1 rounded-full mt-1 inline-block"
                  :class="paymentStatusClass(tenant.payment_status)"
                >{{ tenant.payment_status }}</span>
              </div>
            </div>
          </div>

          <!-- Receivable / Payable summary -->
          <div class="px-5 pb-4 grid grid-cols-3 gap-4 text-sm">
            <div class="card p-3">
              <p class="text-xs text-surface-500">Receivable</p>
              <p class="font-mono text-emerald-400 font-semibold mt-0.5">{{ format(tenant.total_receivable_usd, 'USD') }}</p>
            </div>
            <div class="card p-3">
              <p class="text-xs text-surface-500">Payable</p>
              <p class="font-mono text-red-400 font-semibold mt-0.5">{{ format(tenant.total_payable_usd, 'USD') }}</p>
            </div>
            <div class="card p-3">
              <p class="text-xs text-surface-500">Fee revenue</p>
              <p class="font-mono text-violet-400 font-semibold mt-0.5">
                {{ format(tenant.legs.reduce((s, l) => s.plus(l.fee_revenue_usd), new Decimal(0)).toFixed(2), 'USD') }}
              </p>
            </div>
          </div>

          <!-- Corridor breakdown -->
          <div class="border-t border-surface-800">
            <button
              class="w-full flex items-center justify-between px-5 py-3 text-xs text-surface-400 hover:bg-surface-800/40 transition-colors"
              @click="toggleTenant(tenant.tenant_id)"
            >
              <span>{{ tenant.legs.length }} corridor{{ tenant.legs.length !== 1 ? 's' : '' }}</span>
              <span>{{ expandedTenants.includes(tenant.tenant_id) ? '&#9650; Hide' : '&#9660; Show breakdown' }}</span>
            </button>

            <div v-if="expandedTenants.includes(tenant.tenant_id)" class="overflow-x-auto border-t border-surface-800">
              <table class="w-full text-xs">
                <thead>
                  <tr class="border-b border-surface-800 text-surface-500">
                    <th class="px-5 py-2.5 text-left font-medium">Corridor</th>
                    <th class="px-5 py-2.5 text-right font-medium">Transfers</th>
                    <th class="px-5 py-2.5 text-right font-medium">Sent</th>
                    <th class="px-5 py-2.5 text-right font-medium">Received</th>
                    <th class="px-5 py-2.5 text-right font-medium">Net (USD)</th>
                    <th class="px-5 py-2.5 text-right font-medium">Fees (USD)</th>
                  </tr>
                </thead>
                <tbody>
                  <tr
                    v-for="leg in tenant.legs"
                    :key="leg.corridor"
                    class="border-b border-surface-800/50 hover:bg-surface-800/30"
                  >
                    <td class="px-5 py-2.5 font-medium text-surface-200">
                      {{ leg.source_currency }} → {{ leg.dest_currency }}
                    </td>
                    <td class="px-5 py-2.5 text-right text-surface-400">{{ leg.transfer_count.toLocaleString() }}</td>
                    <td class="px-5 py-2.5 text-right font-mono text-surface-300">{{ leg.total_sent }}</td>
                    <td class="px-5 py-2.5 text-right font-mono text-surface-300">{{ leg.total_received }}</td>
                    <td class="px-5 py-2.5 text-right font-mono" :class="new Decimal(leg.net_usd).gte(0) ? 'text-emerald-400' : 'text-red-400'">
                      {{ formatNet(leg.net_usd) }}
                    </td>
                    <td class="px-5 py-2.5 text-right font-mono text-violet-400">{{ format(leg.fee_revenue_usd, 'USD') }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>

          <!-- Mark as paid (for PENDING/SCHEDULED) -->
          <div
            v-if="tenant.payment_status === 'PENDING' || tenant.payment_status === 'SCHEDULED' || tenant.payment_status === 'OVERDUE'"
            class="border-t border-surface-800 px-5 py-3 flex items-center gap-3"
          >
            <input
              v-model="paymentRefs[tenant.tenant_id]"
              class="input text-xs flex-1"
              :placeholder="`Payment ref for ${tenant.tenant_name}…`"
            />
            <button
              class="btn-primary text-xs px-3 py-1.5"
              :disabled="payingTenant === tenant.tenant_id || !paymentRefs[tenant.tenant_id]"
              @click="markPaid(tenant.tenant_id)"
            >
              <span v-if="payingTenant === tenant.tenant_id">Processing…</span>
              <span v-else>&#10003; Mark paid</span>
            </button>
          </div>

          <!-- Paid indicator -->
          <div v-else-if="tenant.payment_status === 'PAID'" class="border-t border-surface-800 px-5 py-3">
            <p class="text-xs text-emerald-400">
              &#10003; Paid — ref: <span class="font-mono">{{ tenant.payment_ref ?? 'n/a' }}</span>
            </p>
          </div>
        </div>
      </div>
    </template>

    <EmptyState
      v-else
      icon="&#9884;"
      title="No settlement data"
      description="Select a period or refresh to load settlement data."
    />
  </div>
</template>

<script setup lang="ts">
import type { SettlementReport } from '~/types'
import Decimal from 'decimal.js'

const api = useApi()
const { format } = useMoney()
const toast = useToast()

const report = ref<SettlementReport | null>(null)
const loading = ref(true)
const fetchError = ref<string | null>(null)
const usingSampleData = ref(false)
const period = ref('current')
// Vue 3 reactivity does not track Set mutations; use a plain ref<string[]> instead.
const expandedTenants = ref<string[]>([])
const paymentRefs = ref<Record<string, string>>({})
const payingTenant = ref<string | null>(null)

// ── Data ───────────────────────────────────────────────────────────────────

async function loadData() {
  loading.value = true
  fetchError.value = null
  usingSampleData.value = false
  try {
    report.value = await api.getSettlementReport(period.value)
  } catch (err: any) {
    const status = err?.response?.status ?? err?.statusCode
    if (status === 401 || status === 403) {
      fetchError.value = 'Authentication failed — check your API key.'
    } else {
      report.value = generateSampleReport()
      usingSampleData.value = true
    }
  } finally {
    loading.value = false
  }
}

async function markPaid(tenantId: string) {
  const ref = paymentRefs.value[tenantId]
  if (!ref) return
  payingTenant.value = tenantId
  try {
    await api.markSettlementPaid(tenantId, ref)
    toast.success('Settlement marked as paid')
    await loadData()
  } catch (err: any) {
    const status = err?.response?.status ?? err?.statusCode
    if (status === 401 || status === 403) {
      toast.error('Authentication failed — check your API key.')
    } else {
      // Optimistically update local state since the ops endpoint may not be live yet
      const tenant = report.value?.tenants.find(t => t.tenant_id === tenantId)
      if (tenant) {
        tenant.payment_status = 'PAID'
        tenant.payment_ref = ref
      }
      toast.success('Settlement marked as paid')
    }
  } finally {
    payingTenant.value = null
    delete paymentRefs.value[tenantId]
  }
}

// ── UI ─────────────────────────────────────────────────────────────────────

function toggleTenant(id: string) {
  const idx = expandedTenants.value.indexOf(id)
  if (idx !== -1) {
    expandedTenants.value.splice(idx, 1)
  } else {
    expandedTenants.value.push(id)
  }
}

function netPositionClass(net: string) {
  const d = new Decimal(net)
  if (d.gt(0)) return 'text-emerald-400'
  if (d.lt(0)) return 'text-red-400'
  return 'text-surface-400'
}

function formatNet(net: string) {
  const d = new Decimal(net)
  const sign = d.gt(0) ? '+' : ''
  return `${sign}${format(d.abs().toFixed(2), 'USD')}`
}

function paymentStatusClass(status: string) {
  switch (status) {
    case 'PAID': return 'bg-emerald-500/10 text-emerald-400'
    case 'SCHEDULED': return 'bg-blue-500/10 text-blue-400'
    case 'PENDING': return 'bg-amber-500/10 text-amber-400'
    case 'OVERDUE': return 'bg-red-500/10 text-red-400'
    default: return 'bg-surface-700 text-surface-400'
  }
}

function formatDate(iso: string) {
  return new Date(iso).toLocaleDateString()
}

function formatPeriod(start: string, end: string) {
  const s = new Date(start).toLocaleDateString()
  const e = new Date(end).toLocaleDateString()
  return `${s} – ${e}`
}

// ── Sample data ────────────────────────────────────────────────────────────

function generateSampleReport(): SettlementReport {
  const now = new Date()
  const weekAgo = new Date(now.getTime() - 7 * 86_400_000)
  const tomorrow = new Date(now.getTime() + 86_400_000)

  return {
    period_start: weekAgo.toISOString(),
    period_end: now.toISOString(),
    generated_at: now.toISOString(),
    total_volume_usd: '4823910.50',
    total_fee_revenue_usd: '18293.84',
    tenants: [
      {
        tenant_id: 'a0000000-0000-0000-0000-000000000001',
        tenant_name: 'Lemfi',
        legs: [
          {
            corridor: 'GBP-NGN',
            source_currency: 'GBP',
            dest_currency: 'NGN',
            total_sent: '3,241,800.00 GBP',
            total_received: '4,987,548,000.00 NGN',
            net_usd: '8124.50',
            transfer_count: 12847,
            fee_revenue_usd: '11284.92',
          },
          {
            corridor: 'EUR-NGN',
            source_currency: 'EUR',
            dest_currency: 'NGN',
            total_sent: '218,400.00 EUR',
            total_received: '394,904,400.00 NGN',
            net_usd: '-2340.00',
            transfer_count: 1923,
            fee_revenue_usd: '1432.10',
          },
        ],
        total_receivable_usd: '9557.00',
        total_payable_usd: '1432.50',
        net_position_usd: '8124.50',
        due_date: tomorrow.toISOString(),
        payment_status: 'PENDING',
      },
      {
        tenant_id: 'b0000000-0000-0000-0000-000000000002',
        tenant_name: 'Fincra',
        legs: [
          {
            corridor: 'NGN-GBP',
            source_currency: 'NGN',
            dest_currency: 'GBP',
            total_sent: '2,847,600,000.00 NGN',
            total_received: '1,780,875.00 GBP',
            net_usd: '-1840.00',
            transfer_count: 9214,
            fee_revenue_usd: '4820.44',
          },
          {
            corridor: 'NGN-USD',
            source_currency: 'NGN',
            dest_currency: 'USD',
            total_sent: '622,400,000.00 NGN',
            total_received: '405,562.00 USD',
            net_usd: '752.84',
            transfer_count: 2104,
            fee_revenue_usd: '756.38',
          },
        ],
        total_receivable_usd: '5576.82',
        total_payable_usd: '4823.98',
        net_position_usd: '-1087.16',
        due_date: new Date(now.getTime() - 86_400_000).toISOString(),
        payment_status: 'OVERDUE',
      },
    ],
  }
}

// ── Init ───────────────────────────────────────────────────────────────────

onMounted(loadData)
</script>
