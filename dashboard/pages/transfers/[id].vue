<template>
  <div class="animate-fade-in">
    <!-- Back -->
    <button
      class="flex items-center gap-1.5 text-sm text-surface-400 hover:text-surface-200 mb-4 transition-colors focus-ring"
      @click="router.back()"
    >
      <Icon name="chevron-right" :size="14" class="rotate-180" /> Back to transfers
    </button>

    <template v-if="loading && !transfer">
      <div class="space-y-4">
        <SkeletonLoader variant="card" height="80px" />
        <div class="grid grid-cols-1 lg:grid-cols-3 gap-6">
          <div class="lg:col-span-2 space-y-4">
            <SkeletonLoader variant="card" height="200px" />
            <SkeletonLoader variant="card" height="160px" />
          </div>
          <SkeletonLoader variant="card" height="380px" />
        </div>
      </div>
    </template>

    <template v-else-if="transfer">
      <!-- Header -->
      <div class="flex items-center justify-between mb-6">
        <div>
          <div class="flex items-center gap-3">
            <h1 class="text-2xl font-semibold text-surface-100">Transfer</h1>
            <StatusBadge :status="transfer.status" size="base" />
          </div>
          <div class="flex items-center gap-4 mt-1">
            <span class="text-xs font-mono text-surface-500">{{ transfer.id }}</span>
            <span v-if="transfer.external_ref" class="text-xs text-surface-600">
              ref: {{ transfer.external_ref }}
            </span>
          </div>
        </div>
        <AppButton variant="secondary" size="sm" icon="refresh-cw" @click="refresh">Refresh</AppButton>
      </div>

      <div class="grid grid-cols-1 lg:grid-cols-3 gap-6">
        <!-- Left: Transfer Details -->
        <div class="lg:col-span-2 space-y-4">
          <!-- Amounts -->
          <div class="card p-5">
            <h3 class="text-xs font-medium text-surface-500 uppercase tracking-wider mb-4">
              Settlement
            </h3>
            <div class="flex items-center gap-6 flex-wrap">
              <div>
                <p class="text-xs text-surface-500 mb-1">Source</p>
                <MoneyDisplay
                  :amount="transfer.source_amount"
                  :currency="transfer.source_currency"
                  size="xl"
                />
                <p class="text-xs text-surface-500 mt-0.5">{{ transfer.source_currency }}</p>
              </div>
              <Icon name="chevron-right" :size="24" class="text-surface-600" />
              <div v-if="transfer.stable_amount">
                <p class="text-xs text-surface-500 mb-1">Stablecoin</p>
                <MoneyDisplay
                  :amount="transfer.stable_amount"
                  :currency="transfer.stable_coin"
                  size="xl"
                />
                <p class="text-xs text-surface-500 mt-0.5">
                  {{ transfer.stable_coin }} on {{ transfer.chain }}
                </p>
              </div>
              <Icon v-if="transfer.stable_amount" name="chevron-right" :size="24" class="text-surface-600" />
              <div>
                <p class="text-xs text-surface-500 mb-1">Destination</p>
                <MoneyDisplay
                  :amount="transfer.dest_amount || '\u2014'"
                  :currency="transfer.dest_currency"
                  size="xl"
                />
                <p class="text-xs text-surface-500 mt-0.5">{{ transfer.dest_currency }}</p>
              </div>
            </div>
            <div v-if="transfer.fx_rate" class="mt-3 text-xs text-surface-500">
              FX Rate: <span class="font-mono">{{ transfer.fx_rate }}</span>
            </div>
          </div>

          <!-- Fee Breakdown -->
          <div class="card p-5">
            <h3 class="text-xs font-medium text-surface-500 uppercase tracking-wider mb-4">
              Fee Breakdown
            </h3>
            <div v-if="transfer.fees" class="space-y-3">
              <!-- Fee bar -->
              <div class="h-3 bg-surface-800 rounded-full overflow-hidden flex">
                <div
                  :style="{ width: feePercent('on_ramp_fee') + '%' }"
                  class="bg-blue-500"
                  title="On-ramp"
                />
                <div
                  :style="{ width: feePercent('network_fee') + '%' }"
                  class="bg-violet-500"
                  title="Network"
                />
                <div
                  :style="{ width: feePercent('off_ramp_fee') + '%' }"
                  class="bg-emerald-500"
                  title="Off-ramp"
                />
              </div>
              <!-- Legend -->
              <div class="grid grid-cols-3 gap-4 text-sm">
                <div class="flex items-center gap-2">
                  <span class="w-2.5 h-2.5 rounded bg-blue-500 shrink-0" />
                  <div>
                    <p class="text-surface-400">On-ramp</p>
                    <MoneyDisplay :amount="transfer.fees.on_ramp_fee" currency="USD" size="sm" />
                  </div>
                </div>
                <div class="flex items-center gap-2">
                  <span class="w-2.5 h-2.5 rounded bg-violet-500 shrink-0" />
                  <div>
                    <p class="text-surface-400">Network</p>
                    <MoneyDisplay :amount="transfer.fees.network_fee" currency="USD" size="sm" />
                  </div>
                </div>
                <div class="flex items-center gap-2">
                  <span class="w-2.5 h-2.5 rounded bg-emerald-500 shrink-0" />
                  <div>
                    <p class="text-surface-400">Off-ramp</p>
                    <MoneyDisplay :amount="transfer.fees.off_ramp_fee" currency="USD" size="sm" />
                  </div>
                </div>
              </div>
              <div class="pt-2 border-t border-surface-800 flex justify-between">
                <span class="text-sm text-surface-400">Total fees</span>
                <MoneyDisplay :amount="transfer.fees.total_fee_usd" currency="USD" size="sm" />
              </div>
            </div>
            <p v-else class="text-sm text-surface-500">No fee data available</p>
          </div>

          <!-- Parties -->
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <div class="card p-5">
              <div class="flex items-center justify-between mb-2">
                <h3 class="text-sm font-medium text-surface-400">Sender</h3>
                <button
                  class="text-xs px-2 py-1 rounded bg-surface-700 text-surface-300 hover:bg-surface-600 transition-colors"
                  @click="showPII = !showPII"
                >
                  {{ showPII ? 'Hide PII' : 'Reveal PII' }}
                </button>
              </div>
              <dl class="space-y-2 text-sm">
                <div v-if="transfer.sender?.name">
                  <dt class="text-surface-500">Name</dt>
                  <dd class="text-surface-200">{{ showPII ? transfer.sender.name : maskPII(transfer.sender.name, 'name') }}</dd>
                </div>
                <div v-if="transfer.sender?.email">
                  <dt class="text-surface-500">Email</dt>
                  <dd class="text-surface-200">{{ showPII ? transfer.sender.email : maskPII(transfer.sender.email, 'email') }}</dd>
                </div>
                <div v-if="transfer.sender?.country">
                  <dt class="text-surface-500">Country</dt>
                  <dd class="text-surface-200">{{ transfer.sender.country }}</dd>
                </div>
                <p
                  v-if="!transfer.sender?.name && !transfer.sender?.email"
                  class="text-surface-500"
                >
                  No sender data
                </p>
              </dl>
            </div>
            <div class="card p-5">
              <h3 class="text-xs font-medium text-surface-500 uppercase tracking-wider mb-3">
                Recipient
              </h3>
              <dl class="space-y-2 text-sm">
                <div v-if="transfer.recipient?.name">
                  <dt class="text-surface-500">Name</dt>
                  <dd class="text-surface-200">{{ showPII ? transfer.recipient.name : maskPII(transfer.recipient.name, 'name') }}</dd>
                </div>
                <div v-if="transfer.recipient?.bank_name">
                  <dt class="text-surface-500">Bank</dt>
                  <dd class="text-surface-200">{{ showPII ? transfer.recipient.bank_name : maskPII(transfer.recipient.bank_name, 'name') }}</dd>
                </div>
                <div v-if="transfer.recipient?.account_number">
                  <dt class="text-surface-500">Account</dt>
                  <dd class="text-surface-200 font-mono">
                    {{ showPII ? transfer.recipient.account_number : maskPII(transfer.recipient.account_number, 'account') }}
                  </dd>
                </div>
                <div v-if="transfer.recipient?.country">
                  <dt class="text-surface-500">Country</dt>
                  <dd class="text-surface-200">{{ transfer.recipient.country }}</dd>
                </div>
                <p v-if="!transfer.recipient" class="text-surface-500">No recipient data</p>
              </dl>
            </div>
          </div>

          <!-- Failure info -->
          <div v-if="transfer.failure_reason" class="card p-5 border-red-500/20">
            <h3 class="text-xs font-medium text-red-400 uppercase tracking-wider mb-2">
              Failure Details
            </h3>
            <p class="text-sm text-red-300">{{ transfer.failure_reason }}</p>
            <p
              v-if="transfer.failure_code"
              class="text-xs text-red-400/60 font-mono mt-1"
            >
              Code: {{ transfer.failure_code }}
            </p>
          </div>
        </div>

        <!-- Right: Timeline -->
        <div>
          <div class="card p-5">
            <h3 class="text-xs font-medium text-surface-500 uppercase tracking-wider mb-4">
              Timeline
            </h3>
            <SkeletonLoader v-if="eventsLoading && !events.length" variant="text" :lines="5" />
            <TimelineView v-else-if="events.length" :events="events" />
            <p v-else class="text-sm text-surface-500">No events recorded</p>
          </div>

          <!-- Timestamps -->
          <div class="card p-5 mt-4">
            <h3 class="text-xs font-medium text-surface-500 uppercase tracking-wider mb-3">
              Timestamps
            </h3>
            <dl class="space-y-2 text-xs">
              <div class="flex justify-between">
                <dt class="text-surface-500">Created</dt>
                <dd class="text-surface-300 font-mono">{{ formatTS(transfer.created_at) }}</dd>
              </div>
              <div v-if="transfer.funded_at" class="flex justify-between">
                <dt class="text-surface-500">Funded</dt>
                <dd class="text-surface-300 font-mono">{{ formatTS(transfer.funded_at) }}</dd>
              </div>
              <div v-if="transfer.completed_at" class="flex justify-between">
                <dt class="text-surface-500">Completed</dt>
                <dd class="text-surface-300 font-mono">{{ formatTS(transfer.completed_at) }}</dd>
              </div>
              <div v-if="transfer.failed_at" class="flex justify-between">
                <dt class="text-surface-500">Failed</dt>
                <dd class="text-surface-300 font-mono">{{ formatTS(transfer.failed_at) }}</dd>
              </div>
              <div class="flex justify-between">
                <dt class="text-surface-500">Updated</dt>
                <dd class="text-surface-300 font-mono">{{ formatTS(transfer.updated_at) }}</dd>
              </div>
            </dl>
          </div>
        </div>
      </div>
    </template>

    <EmptyState
      v-else
      title="Transfer not found"
      description="The transfer you're looking for doesn't exist or has been removed."
    />
  </div>
</template>

<script setup lang="ts">
import type { Transfer, TransferEvent } from '~/types'
import Decimal from 'decimal.js'

const route = useRoute()
const router = useRouter()
const api = useApi()
const { error: showError } = useToast()

const transferId = computed(() => route.params.id as string)

// Transfer data
const transfer = ref<Transfer | null>(null)
const loading = ref(true)
const events = ref<TransferEvent[]>([])
const eventsLoading = ref(true)
const showPII = ref(false)

function maskPII(value: string, type: 'name' | 'email' | 'account' | 'default' = 'default'): string {
  if (!value) return ''
  switch (type) {
    case 'name': return value[0] + '***'
    case 'email': {
      const [u, d] = value.split('@')
      return u[0] + '***@' + (d || '***')
    }
    case 'account': return '****' + value.slice(-4)
    default: return value[0] + '***'
  }
}

async function fetchTransfer() {
  loading.value = true
  try {
    transfer.value = await api.getTransfer(transferId.value)
  } catch (e) {
    showError('Failed to load transfer')
  } finally {
    loading.value = false
  }
}

async function fetchEvents() {
  eventsLoading.value = true
  try {
    const result = await api.getTransferEvents(transferId.value)
    events.value = result.events
  } catch {
    // Events endpoint may not exist yet - generate from transfer timestamps
    if (transfer.value) {
      events.value = generateEventsFromTransfer(transfer.value)
    }
  } finally {
    eventsLoading.value = false
  }
}

function generateEventsFromTransfer(t: Transfer): TransferEvent[] {
  const fakeEvents: TransferEvent[] = []
  const statuses: Array<{ status: string; time?: string }> = [
    { status: 'CREATED', time: t.created_at },
  ]
  if (t.funded_at) statuses.push({ status: 'FUNDED', time: t.funded_at })
  if (t.completed_at) {
    statuses.push({ status: 'ON_RAMPING' })
    statuses.push({ status: 'SETTLING' })
    statuses.push({ status: 'OFF_RAMPING' })
    statuses.push({ status: 'COMPLETING' })
    statuses.push({ status: 'COMPLETED', time: t.completed_at })
  }
  if (t.failed_at) statuses.push({ status: 'FAILED', time: t.failed_at })

  for (let i = 0; i < statuses.length; i++) {
    fakeEvents.push({
      id: `evt-${i}`,
      transfer_id: t.id,
      tenant_id: t.tenant_id,
      from_status: i > 0 ? (statuses[i - 1].status as any) : '',
      to_status: statuses[i].status as any,
      occurred_at: statuses[i].time || t.updated_at,
    })
  }
  return fakeEvents
}

function refresh() {
  fetchTransfer().then(fetchEvents)
}

function feePercent(key: 'on_ramp_fee' | 'network_fee' | 'off_ramp_fee') {
  if (!transfer.value?.fees) return 0
  const total = new Decimal(transfer.value.fees.total_fee_usd || '0')
  if (total.isZero()) return 33
  const part = new Decimal(transfer.value.fees[key] || '0')
  return part.div(total).mul(100).toNumber()
}

function formatTS(ts?: string) {
  if (!ts) return '\u2014'
  return new Date(ts).toLocaleString('en-GB', {
    day: '2-digit',
    month: 'short',
    year: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  })
}

onMounted(() => {
  fetchTransfer().then(fetchEvents)
})
</script>
