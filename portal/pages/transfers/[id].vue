<template>
  <div class="space-y-6">
    <div class="flex items-center gap-3">
      <NuxtLink to="/transfers" class="text-surface-500 hover:text-surface-300 text-sm flex items-center gap-1">
        <Icon name="chevron-right" :size="14" class="rotate-180" />
        Back
      </NuxtLink>
    </div>

    <!-- Skeleton loading -->
    <template v-if="store.detailLoading">
      <div class="flex items-start justify-between">
        <div>
          <SkeletonLoader variant="text" :lines="1" width="200px" />
          <SkeletonLoader variant="text" :lines="1" width="300px" />
        </div>
        <SkeletonLoader variant="card" width="100px" height="28px" />
      </div>
      <div class="grid grid-cols-1 lg:grid-cols-3 gap-6">
        <div class="lg:col-span-2 space-y-4">
          <SkeletonLoader variant="card" height="180px" />
          <SkeletonLoader variant="card" height="160px" />
          <div class="grid grid-cols-2 gap-4">
            <SkeletonLoader variant="card" height="120px" />
            <SkeletonLoader variant="card" height="120px" />
          </div>
        </div>
        <SkeletonLoader variant="card" height="400px" />
      </div>
    </template>

    <template v-else-if="transfer">
      <!-- Header -->
      <div class="flex items-start justify-between flex-wrap gap-4 animate-fade-in">
        <div>
          <h1 class="text-xl font-semibold text-surface-100">{{ transfer.external_ref }}</h1>
          <p class="text-xs text-surface-500 font-mono mt-1">{{ transfer.id }}</p>
        </div>
        <StatusBadge :status="transfer.status" role="status" />
      </div>

      <!-- Transfer Pipeline Stepper -->
      <div class="card p-4 animate-fade-in">
        <div class="flex items-center justify-between">
          <div v-for="(step, i) in pipelineSteps" :key="step.label" class="flex items-center" :class="i < pipelineSteps.length - 1 ? 'flex-1' : ''">
            <div class="flex flex-col items-center">
              <div
                :class="step.state === 'complete' ? 'bg-violet-600 text-white' : step.state === 'current' ? 'bg-violet-600/20 text-violet-400 ring-2 ring-violet-500' : 'bg-surface-800 text-surface-500'"
                class="w-8 h-8 rounded-full flex items-center justify-center text-xs font-semibold transition-colors"
              >
                <Icon v-if="step.state === 'complete'" name="check" :size="14" />
                <span v-else>{{ i + 1 }}</span>
              </div>
              <span class="text-[10px] mt-1 font-medium" :class="step.state !== 'pending' ? 'text-surface-300' : 'text-surface-600'">{{ step.label }}</span>
            </div>
            <div v-if="i < pipelineSteps.length - 1" class="flex-1 h-0.5 mx-2 mt-[-12px]" :class="step.state === 'complete' ? 'bg-violet-500' : 'bg-surface-700'" />
          </div>
        </div>
      </div>

      <div class="grid grid-cols-1 lg:grid-cols-3 gap-6">
        <!-- Details -->
        <div class="lg:col-span-2 space-y-4">
          <!-- Amounts -->
          <div class="card p-5 animate-fade-in">
            <h2 class="text-sm font-medium text-surface-400 mb-3">Transfer Details</h2>
            <div class="grid grid-cols-2 gap-4">
              <div>
                <p class="text-xs text-surface-500">Send</p>
                <p class="text-lg font-semibold text-surface-100">
                  <MoneyDisplay :amount="transfer.source_amount" :currency="transfer.source_currency" />
                </p>
              </div>
              <div>
                <p class="text-xs text-surface-500">Receive</p>
                <p class="text-lg font-semibold text-surface-100">
                  <MoneyDisplay :amount="transfer.dest_amount" :currency="transfer.dest_currency" />
                </p>
              </div>
              <div>
                <p class="text-xs text-surface-500">FX Rate</p>
                <p class="text-sm text-surface-300 font-mono">{{ transfer.fx_rate }}</p>
              </div>
              <div>
                <p class="text-xs text-surface-500">Stablecoin</p>
                <p class="text-sm text-surface-300">
                  {{ money.format(transfer.stable_amount, transfer.stable_coin) }} {{ transfer.stable_coin }}
                  <span class="text-surface-500 text-xs ml-1">({{ transfer.chain }})</span>
                </p>
              </div>
            </div>
          </div>

          <!-- Fee Breakdown -->
          <div class="card p-5 animate-fade-in">
            <h2 class="text-sm font-medium text-surface-400 mb-3">Fee Breakdown</h2>
            <div class="space-y-2">
              <div class="flex justify-between text-sm">
                <span class="text-surface-500">On-ramp fee</span>
                <span class="text-surface-300"><MoneyDisplay :amount="transfer.fees.on_ramp_fee" currency="USD" /></span>
              </div>
              <div class="flex justify-between text-sm">
                <span class="text-surface-500">Network fee</span>
                <span class="text-surface-300"><MoneyDisplay :amount="transfer.fees.network_fee" currency="USD" /></span>
              </div>
              <div class="flex justify-between text-sm">
                <span class="text-surface-500">Off-ramp fee</span>
                <span class="text-surface-300"><MoneyDisplay :amount="transfer.fees.off_ramp_fee" currency="USD" /></span>
              </div>
              <div class="flex justify-between text-sm font-semibold border-t border-surface-700 pt-2 mt-2">
                <span class="text-surface-400">Total fee</span>
                <span class="text-surface-100"><MoneyDisplay :amount="transfer.fees.total_fee_usd" currency="USD" /></span>
              </div>
            </div>
          </div>

          <!-- Parties -->
          <div class="grid grid-cols-1 sm:grid-cols-2 gap-4">
            <div class="card p-5 animate-fade-in">
              <h2 class="text-sm font-medium text-surface-400 mb-3">Sender</h2>
              <dl class="space-y-1.5 text-sm">
                <div v-if="transfer.sender?.name" class="flex justify-between">
                  <dt class="text-surface-500">Name</dt>
                  <dd class="text-surface-300">{{ transfer.sender.name }}</dd>
                </div>
                <div v-if="transfer.sender?.email" class="flex justify-between">
                  <dt class="text-surface-500">Email</dt>
                  <dd class="text-surface-300">{{ transfer.sender.email }}</dd>
                </div>
                <div v-if="transfer.sender?.country" class="flex justify-between">
                  <dt class="text-surface-500">Country</dt>
                  <dd class="text-surface-300">{{ transfer.sender.country }}</dd>
                </div>
              </dl>
            </div>
            <div class="card p-5 animate-fade-in">
              <h2 class="text-sm font-medium text-surface-400 mb-3">Recipient</h2>
              <dl class="space-y-1.5 text-sm">
                <div class="flex justify-between">
                  <dt class="text-surface-500">Name</dt>
                  <dd class="text-surface-300">{{ transfer.recipient.name }}</dd>
                </div>
                <div v-if="transfer.recipient.bank_name" class="flex justify-between">
                  <dt class="text-surface-500">Bank</dt>
                  <dd class="text-surface-300">{{ transfer.recipient.bank_name }}</dd>
                </div>
                <div class="flex justify-between">
                  <dt class="text-surface-500">Country</dt>
                  <dd class="text-surface-300">{{ transfer.recipient.country }}</dd>
                </div>
              </dl>
            </div>
          </div>

          <!-- Failure info -->
          <div v-if="transfer.failure_reason" class="bg-red-900/20 border border-red-800 rounded-lg p-4 animate-fade-in">
            <h2 class="text-sm font-medium text-red-400 mb-1">Failure</h2>
            <p class="text-sm text-red-300">{{ transfer.failure_reason }}</p>
            <p v-if="transfer.failure_code" class="text-xs text-red-400 font-mono mt-1">Code: {{ transfer.failure_code }}</p>
          </div>
        </div>

        <!-- Timeline -->
        <div class="card p-5 animate-fade-in">
          <h2 class="text-sm font-medium text-surface-400 mb-4">Event Timeline</h2>
          <TimelineView :events="store.currentEvents" />
        </div>
      </div>
    </template>

    <EmptyState v-else title="Transfer not found" icon="search" />
  </div>
</template>

<script setup lang="ts">
const route = useRoute()
const store = useTransferStore()
const money = useMoney()

const transfer = computed(() => store.currentTransfer)

const statusOrder = ['CREATED', 'FUNDED', 'ON_RAMPING', 'SETTLING', 'OFF_RAMPING', 'COMPLETING', 'COMPLETED']

const pipelineSteps = computed(() => {
  if (!transfer.value) return []
  const status = transfer.value.status
  const steps = [
    { label: 'Created', statuses: ['CREATED'] },
    { label: 'Funded', statuses: ['FUNDED', 'ON_RAMPING'] },
    { label: 'Settling', statuses: ['SETTLING', 'OFF_RAMPING', 'COMPLETING'] },
    { label: 'Completed', statuses: ['COMPLETED'] },
  ]

  if (status === 'FAILED' || status === 'REFUNDING' || status === 'REFUNDED') {
    return steps.map(s => ({ ...s, state: 'pending' as const }))
  }

  const currentIdx = statusOrder.indexOf(status)
  return steps.map((step) => {
    const stepMinIdx = Math.min(...step.statuses.map(s => statusOrder.indexOf(s)).filter(i => i >= 0))
    const stepMaxIdx = Math.max(...step.statuses.map(s => statusOrder.indexOf(s)).filter(i => i >= 0))
    if (currentIdx > stepMaxIdx) return { ...step, state: 'complete' as const }
    if (currentIdx >= stepMinIdx && currentIdx <= stepMaxIdx) return { ...step, state: 'current' as const }
    return { ...step, state: 'pending' as const }
  })
})

onMounted(() => {
  const id = route.params.id as string
  store.fetchTransfer(id)
  store.fetchTransferEvents(id)
})
</script>
