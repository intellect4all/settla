<template>
  <div class="space-y-6">
    <div class="flex items-center justify-between">
      <div>
        <h1 class="text-xl font-semibold text-surface-100">Treasury</h1>
        <p class="text-sm text-surface-500 mt-1">Your prefunded liquidity positions</p>
      </div>
      <div class="flex items-center gap-2">
        <span v-if="stream.connected.value" class="text-xs text-emerald-400 flex items-center gap-1">
          <span class="w-1.5 h-1.5 rounded-full bg-emerald-400 animate-pulse" />
          Live
        </span>
      </div>
    </div>

    <!-- Skeleton loading state -->
    <template v-if="store.loading">
      <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
        <SkeletonLoader v-for="i in 6" :key="i" variant="card" height="180px" />
      </div>
    </template>

    <template v-else>
      <!-- Alerts -->
      <div v-if="store.alertPositions.length" class="bg-amber-900/20 border border-amber-700 rounded-lg p-4 animate-fade-in">
        <p class="text-sm font-medium text-amber-300 mb-2">Low balance alerts</p>
        <ul class="space-y-1">
          <li v-for="pos in store.alertPositions" :key="pos.id" class="text-sm text-amber-400">
            {{ pos.currency }} ({{ pos.location }}): {{ money.format(pos.available, pos.currency) }} available,
            min {{ money.format(pos.min_balance, pos.currency) }}
          </li>
        </ul>
      </div>

      <div v-if="store.positions.length" class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
        <div
          v-for="(pos, i) in store.positions" :key="pos.id"
          class="bg-surface-900 rounded-lg border p-5 animate-fade-in"
          :class="isLow(pos) ? 'border-amber-700' : 'border-surface-800'"
          :style="{ animationDelay: `${i * 40}ms`, animationFillMode: 'backwards' }"
        >
          <div class="flex items-center justify-between mb-3">
            <h3 class="text-base font-semibold text-surface-100">{{ pos.currency }}</h3>
            <span class="text-xs text-surface-500 bg-surface-800 px-2 py-0.5 rounded">{{ pos.location }}</span>
          </div>

          <dl class="space-y-2 text-sm">
            <div class="flex justify-between">
              <dt class="text-surface-500">Balance</dt>
              <dd class="text-surface-200 font-mono"><MoneyDisplay :amount="pos.balance" :currency="pos.currency" /></dd>
            </div>
            <div class="flex justify-between">
              <dt class="text-surface-500">Locked</dt>
              <dd class="text-surface-400 font-mono"><MoneyDisplay :amount="pos.locked" :currency="pos.currency" /></dd>
            </div>
            <div class="flex justify-between font-semibold">
              <dt class="text-surface-400">Available</dt>
              <dd :class="isLow(pos) ? 'text-amber-400' : 'text-emerald-400'" class="font-mono">
                <MoneyDisplay :amount="pos.available" :currency="pos.currency" />
              </dd>
            </div>
          </dl>

          <div v-if="pos.min_balance && pos.min_balance !== '0'" class="mt-3 pt-3 border-t border-surface-800">
            <div class="flex justify-between text-xs">
              <span class="text-surface-500">Min balance</span>
              <span class="text-surface-400 font-mono">{{ money.format(pos.min_balance, pos.currency) }}</span>
            </div>
            <div class="flex justify-between text-xs mt-1">
              <span class="text-surface-500">Target</span>
              <span class="text-surface-400 font-mono">{{ money.format(pos.target_balance, pos.currency) }}</span>
            </div>
          </div>

          <!-- Action buttons -->
          <div class="mt-4 pt-3 border-t border-surface-800 flex gap-2">
            <button
              class="flex-1 text-xs bg-emerald-600 hover:bg-emerald-500 text-white py-1.5 px-3 rounded transition-colors"
              @click="openTopUp(pos)"
            >
              Top Up
            </button>
            <button
              class="flex-1 text-xs bg-surface-700 hover:bg-surface-600 text-surface-200 py-1.5 px-3 rounded transition-colors"
              @click="openWithdraw(pos)"
            >
              Withdraw
            </button>
          </div>

          <p class="text-[10px] text-surface-600 mt-3">
            Updated {{ new Date(pos.updated_at).toLocaleString('en-GB', { day: '2-digit', month: 'short', hour: '2-digit', minute: '2-digit' }) }}
          </p>
        </div>
      </div>

      <EmptyState v-else title="No positions" description="Treasury positions will appear once configured" icon="wallet" />

      <!-- Transaction History -->
      <div v-if="store.transactions.length" class="mt-8">
        <h2 class="text-lg font-semibold text-surface-100 mb-4">Transaction History</h2>
        <div class="bg-surface-900 rounded-lg border border-surface-800 overflow-hidden">
          <table class="w-full text-sm">
            <thead>
              <tr class="border-b border-surface-800 text-surface-400">
                <th class="text-left px-4 py-3 font-medium">Type</th>
                <th class="text-left px-4 py-3 font-medium">Currency</th>
                <th class="text-right px-4 py-3 font-medium">Amount</th>
                <th class="text-left px-4 py-3 font-medium">Status</th>
                <th class="text-left px-4 py-3 font-medium">Date</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="tx in store.transactions" :key="tx.id"
                class="border-b border-surface-800/50 last:border-0"
              >
                <td class="px-4 py-3 text-surface-200">
                  <span
                    class="inline-flex items-center gap-1 text-xs font-medium px-2 py-0.5 rounded"
                    :class="txTypeClass(tx.type)"
                  >
                    {{ txTypeLabel(tx.type) }}
                  </span>
                </td>
                <td class="px-4 py-3 text-surface-300">{{ tx.currency }}</td>
                <td class="px-4 py-3 text-right font-mono text-surface-200">{{ tx.amount }}</td>
                <td class="px-4 py-3">
                  <span
                    class="text-xs font-medium px-2 py-0.5 rounded"
                    :class="statusClass(tx.status)"
                  >
                    {{ tx.status }}
                  </span>
                </td>
                <td class="px-4 py-3 text-surface-400 text-xs">
                  {{ new Date(tx.createdAt).toLocaleString('en-GB', { day: '2-digit', month: 'short', hour: '2-digit', minute: '2-digit' }) }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </template>

    <!-- Top-Up Modal -->
    <Teleport to="body">
      <div v-if="showTopUpModal" class="fixed inset-0 bg-black/60 flex items-center justify-center z-50" @click.self="showTopUpModal = false">
        <div class="bg-surface-900 border border-surface-700 rounded-xl p-6 w-full max-w-md mx-4">
          <h3 class="text-lg font-semibold text-surface-100 mb-4">Top Up Position</h3>
          <p class="text-sm text-surface-400 mb-4">{{ selectedPosition?.currency }} at {{ selectedPosition?.location }}</p>

          <div class="space-y-4">
            <div>
              <label class="block text-sm text-surface-400 mb-1">Amount</label>
              <input
                v-model="topUpAmount"
                type="text"
                placeholder="0.00"
                class="w-full bg-surface-800 border border-surface-700 rounded-lg px-3 py-2 text-surface-100 focus:border-emerald-500 focus:outline-none"
              />
            </div>
            <div>
              <label class="block text-sm text-surface-400 mb-1">Method</label>
              <select
                v-model="topUpMethod"
                class="w-full bg-surface-800 border border-surface-700 rounded-lg px-3 py-2 text-surface-100 focus:border-emerald-500 focus:outline-none"
              >
                <option value="bank_transfer">Bank Transfer</option>
                <option value="crypto">Crypto</option>
              </select>
            </div>
          </div>

          <div class="flex gap-3 mt-6">
            <button class="flex-1 bg-surface-700 text-surface-300 py-2 rounded-lg hover:bg-surface-600 transition-colors" @click="showTopUpModal = false">Cancel</button>
            <button
              class="flex-1 bg-emerald-600 text-white py-2 rounded-lg hover:bg-emerald-500 transition-colors disabled:opacity-50"
              :disabled="!topUpAmount || submitting"
              @click="submitTopUp"
            >
              {{ submitting ? 'Processing...' : 'Top Up' }}
            </button>
          </div>
        </div>
      </div>
    </Teleport>

    <!-- Withdraw Modal -->
    <Teleport to="body">
      <div v-if="showWithdrawModal" class="fixed inset-0 bg-black/60 flex items-center justify-center z-50" @click.self="showWithdrawModal = false">
        <div class="bg-surface-900 border border-surface-700 rounded-xl p-6 w-full max-w-md mx-4">
          <h3 class="text-lg font-semibold text-surface-100 mb-4">Withdraw from Position</h3>
          <p class="text-sm text-surface-400 mb-1">{{ selectedPosition?.currency }} at {{ selectedPosition?.location }}</p>
          <p class="text-xs text-surface-500 mb-4">Available: {{ selectedPosition?.available }}</p>

          <div class="space-y-4">
            <div>
              <label class="block text-sm text-surface-400 mb-1">Amount</label>
              <input
                v-model="withdrawAmount"
                type="text"
                placeholder="0.00"
                class="w-full bg-surface-800 border border-surface-700 rounded-lg px-3 py-2 text-surface-100 focus:border-blue-500 focus:outline-none"
              />
            </div>
            <div>
              <label class="block text-sm text-surface-400 mb-1">Method</label>
              <select
                v-model="withdrawMethod"
                class="w-full bg-surface-800 border border-surface-700 rounded-lg px-3 py-2 text-surface-100 focus:border-blue-500 focus:outline-none"
              >
                <option value="bank_transfer">Bank Transfer</option>
                <option value="crypto">Crypto</option>
              </select>
            </div>
            <div>
              <label class="block text-sm text-surface-400 mb-1">Destination</label>
              <input
                v-model="withdrawDestination"
                type="text"
                :placeholder="withdrawMethod === 'crypto' ? 'Wallet address' : 'Bank account reference'"
                class="w-full bg-surface-800 border border-surface-700 rounded-lg px-3 py-2 text-surface-100 focus:border-blue-500 focus:outline-none"
              />
            </div>
          </div>

          <div class="flex gap-3 mt-6">
            <button class="flex-1 bg-surface-700 text-surface-300 py-2 rounded-lg hover:bg-surface-600 transition-colors" @click="showWithdrawModal = false">Cancel</button>
            <button
              class="flex-1 bg-blue-600 text-white py-2 rounded-lg hover:bg-blue-500 transition-colors disabled:opacity-50"
              :disabled="!withdrawAmount || !withdrawDestination || submitting"
              @click="submitWithdraw"
            >
              {{ submitting ? 'Processing...' : 'Withdraw' }}
            </button>
          </div>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<script setup lang="ts">
import type { Position } from '~/types'

const store = useTreasuryStore()
const money = useMoney()
const stream = usePositionStream()

// Modal state
const showTopUpModal = ref(false)
const showWithdrawModal = ref(false)
const selectedPosition = ref<Position | null>(null)
const topUpAmount = ref('')
const topUpMethod = ref('bank_transfer')
const withdrawAmount = ref('')
const withdrawMethod = ref('bank_transfer')
const withdrawDestination = ref('')
const submitting = ref(false)

function isLow(pos: Position) {
  const available = parseFloat(pos.available || '0')
  const min = parseFloat(pos.min_balance || '0')
  return min > 0 && available < min
}

function openTopUp(pos: Position) {
  selectedPosition.value = pos
  topUpAmount.value = ''
  topUpMethod.value = 'bank_transfer'
  showTopUpModal.value = true
}

function openWithdraw(pos: Position) {
  selectedPosition.value = pos
  withdrawAmount.value = ''
  withdrawMethod.value = 'bank_transfer'
  withdrawDestination.value = ''
  showWithdrawModal.value = true
}

async function submitTopUp() {
  if (!selectedPosition.value || !topUpAmount.value) return
  submitting.value = true
  try {
    await store.requestTopUp(
      selectedPosition.value.currency,
      selectedPosition.value.location,
      topUpAmount.value,
      topUpMethod.value,
    )
    showTopUpModal.value = false
  } catch (e) {
    alert(e instanceof Error ? e.message : 'Top-up failed')
  } finally {
    submitting.value = false
  }
}

async function submitWithdraw() {
  if (!selectedPosition.value || !withdrawAmount.value || !withdrawDestination.value) return
  submitting.value = true
  try {
    await store.requestWithdrawal(
      selectedPosition.value.currency,
      selectedPosition.value.location,
      withdrawAmount.value,
      withdrawMethod.value,
      withdrawDestination.value,
    )
    showWithdrawModal.value = false
  } catch (e) {
    alert(e instanceof Error ? e.message : 'Withdrawal failed')
  } finally {
    submitting.value = false
  }
}

function txTypeLabel(type: string) {
  const labels: Record<string, string> = {
    TOP_UP: 'Top Up',
    WITHDRAWAL: 'Withdrawal',
    DEPOSIT_CREDIT: 'Deposit',
    INTERNAL_REBALANCE: 'Rebalance',
  }
  return labels[type] || type
}

function txTypeClass(type: string) {
  const classes: Record<string, string> = {
    TOP_UP: 'bg-emerald-900/30 text-emerald-400',
    WITHDRAWAL: 'bg-blue-900/30 text-blue-400',
    DEPOSIT_CREDIT: 'bg-purple-900/30 text-purple-400',
    INTERNAL_REBALANCE: 'bg-surface-800 text-surface-300',
  }
  return classes[type] || 'bg-surface-800 text-surface-300'
}

function statusClass(status: string) {
  const classes: Record<string, string> = {
    PENDING: 'bg-yellow-900/30 text-yellow-400',
    PROCESSING: 'bg-blue-900/30 text-blue-400',
    COMPLETED: 'bg-emerald-900/30 text-emerald-400',
    FAILED: 'bg-red-900/30 text-red-400',
  }
  return classes[status] || 'bg-surface-800 text-surface-300'
}

onMounted(() => {
  store.fetchPositions()
  store.fetchTransactions()
  stream.connect()
})
</script>
