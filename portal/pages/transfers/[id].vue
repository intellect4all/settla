<template>
  <div class="space-y-6">
    <div class="flex items-center gap-3">
      <NuxtLink to="/transfers" class="text-surface-500 hover:text-surface-300 text-sm">&larr; Back</NuxtLink>
    </div>

    <LoadingSpinner v-if="store.detailLoading" size="lg" full-page />

    <template v-else-if="transfer">
      <!-- Header -->
      <div class="flex items-start justify-between flex-wrap gap-4">
        <div>
          <h1 class="text-xl font-semibold text-surface-100">{{ transfer.external_ref }}</h1>
          <p class="text-xs text-surface-500 font-mono mt-1">{{ transfer.id }}</p>
        </div>
        <StatusBadge :status="transfer.status" />
      </div>

      <div class="grid grid-cols-1 lg:grid-cols-3 gap-6">
        <!-- Details -->
        <div class="lg:col-span-2 space-y-4">
          <!-- Amounts -->
          <div class="bg-surface-900 rounded-lg border border-surface-800 p-5">
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
          <div class="bg-surface-900 rounded-lg border border-surface-800 p-5">
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
            <div class="bg-surface-900 rounded-lg border border-surface-800 p-5">
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
            <div class="bg-surface-900 rounded-lg border border-surface-800 p-5">
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
          <div v-if="transfer.failure_reason" class="bg-red-900/20 border border-red-800 rounded-lg p-4">
            <h2 class="text-sm font-medium text-red-400 mb-1">Failure</h2>
            <p class="text-sm text-red-300">{{ transfer.failure_reason }}</p>
            <p v-if="transfer.failure_code" class="text-xs text-red-400 font-mono mt-1">Code: {{ transfer.failure_code }}</p>
          </div>
        </div>

        <!-- Timeline -->
        <div class="bg-surface-900 rounded-lg border border-surface-800 p-5">
          <h2 class="text-sm font-medium text-surface-400 mb-4">Event Timeline</h2>
          <TimelineView :events="store.currentEvents" />
        </div>
      </div>
    </template>

    <EmptyState v-else title="Transfer not found" icon="?" />
  </div>
</template>

<script setup lang="ts">
const route = useRoute()
const store = useTransferStore()
const money = useMoney()

const transfer = computed(() => store.currentTransfer)

onMounted(() => {
  const id = route.params.id as string
  store.fetchTransfer(id)
  store.fetchTransferEvents(id)
})
</script>
