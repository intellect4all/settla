<template>
  <div class="space-y-6">
    <div class="flex items-center justify-between">
      <div>
        <h1 class="text-xl font-semibold text-surface-100">Crypto Balances</h1>
        <p class="text-sm text-surface-500 mt-1">Held crypto balances across chains</p>
      </div>
      <div class="text-right">
        <p class="text-xs text-surface-500">Total Portfolio Value</p>
        <p class="text-lg font-semibold text-surface-100 font-mono">${{ totalValueUsd }}</p>
      </div>
    </div>

    <SkeletonLoader v-if="loading" variant="table" :lines="4" />

    <template v-else-if="balances.length">
      <div class="card overflow-hidden animate-fade-in">
        <table class="w-full text-sm">
          <thead>
            <tr class="border-b border-surface-800 text-surface-500 text-left">
              <th class="px-4 py-3 font-medium">Chain</th>
              <th class="px-4 py-3 font-medium">Token</th>
              <th class="px-4 py-3 font-medium text-right">Amount</th>
              <th class="px-4 py-3 font-medium text-right">Value (USD)</th>
              <th class="px-4 py-3 font-medium">Status</th>
              <th class="px-4 py-3 font-medium text-right">Action</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="(b, idx) in balances" :key="`${b.chain}-${b.token}-${idx}`"
              class="border-b border-surface-800/50"
            >
              <td class="px-4 py-3 text-surface-200">{{ b.chain }}</td>
              <td class="px-4 py-3 text-surface-200 font-mono">{{ b.token }}</td>
              <td class="px-4 py-3 text-right font-mono text-surface-200">{{ b.amount }}</td>
              <td class="px-4 py-3 text-right font-mono text-surface-300">${{ b.value_usd }}</td>
              <td class="px-4 py-3">
                <span
                  class="text-xs font-medium px-2 py-0.5 rounded"
                  :class="statusClass(b.status)"
                >{{ statusLabel(b.status) }}</span>
              </td>
              <td class="px-4 py-3 text-right">
                <AppButton
                  v-if="b.status === 'held'"
                  size="sm"
                  @click="openConvertModal(b)"
                >Convert</AppButton>
                <span v-else class="text-xs text-surface-500">In progress</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <EmptyState v-else title="No crypto balances" description="Held balances will appear here after deposits are confirmed" icon="wallet" />

    <!-- Convert confirmation modal -->
    <Modal :open="!!convertTarget" title="Convert Balance" @close="convertTarget = null">
      <template v-if="convertTarget">
        <p class="text-sm text-surface-400">
          Convert <span class="text-surface-100 font-mono">{{ convertTarget.amount }} {{ convertTarget.token }}</span>
          on <span class="text-surface-200">{{ convertTarget.chain }}</span> to fiat?
        </p>
        <p class="text-xs text-surface-500 mt-2">
          Estimated value: <span class="font-mono text-surface-300">${{ convertTarget.value_usd }}</span>.
          The conversion will be executed at the current market rate.
        </p>

        <div class="mt-4">
          <label class="text-xs text-surface-400 block mb-1">Amount to convert</label>
          <input
            v-model="convertAmount"
            type="text"
            :placeholder="convertTarget.amount"
            class="input w-full text-sm font-mono"
          />
          <p class="text-xs text-surface-500 mt-1">Leave empty to convert the full balance</p>
        </div>
        <p v-if="convertError" class="text-xs text-red-400 mt-3">{{ convertError }}</p>
      </template>
      <template #footer>
        <AppButton variant="ghost" @click="convertTarget = null">Cancel</AppButton>
        <AppButton :loading="converting" @click="handleConvert">Confirm Convert</AppButton>
      </template>
    </Modal>
  </div>
</template>

<script setup lang="ts">
import type { CryptoBalance } from '~/types'

const api = usePortalApi()

const loading = ref(true)
const balances = ref<CryptoBalance[]>([])
const totalValueUsd = ref('0.00')

const convertTarget = ref<CryptoBalance | null>(null)
const convertAmount = ref('')
const converting = ref(false)
const convertError = ref('')

function statusClass(status: CryptoBalance['status']) {
  const map: Record<string, string> = {
    held: 'bg-orange-900/40 text-orange-400',
    converting: 'bg-blue-900/40 text-blue-400',
  }
  return map[status] ?? 'bg-surface-800 text-surface-400'
}

function statusLabel(status: CryptoBalance['status']) {
  const map: Record<string, string> = {
    held: 'Held',
    converting: 'Converting',
  }
  return map[status] ?? status
}

function openConvertModal(balance: CryptoBalance) {
  convertTarget.value = balance
  convertAmount.value = ''
  convertError.value = ''
}

async function handleConvert() {
  if (!convertTarget.value) return
  converting.value = true
  convertError.value = ''
  try {
    await api.convertCryptoBalance({
      chain: convertTarget.value.chain,
      token: convertTarget.value.token,
      amount: convertAmount.value || convertTarget.value.amount,
    })
    convertTarget.value = null
    await fetchBalances()
  } catch (e: any) {
    convertError.value = e.data?.message ?? e.message ?? 'Failed to convert balance'
  } finally {
    converting.value = false
  }
}

async function fetchBalances() {
  try {
    const res = await api.getCryptoBalances()
    balances.value = res.balances
    totalValueUsd.value = res.total_value_usd
  } catch {
    balances.value = []
    totalValueUsd.value = '0.00'
  }
}

onMounted(async () => {
  await fetchBalances()
  loading.value = false
})
</script>
