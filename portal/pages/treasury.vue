<template>
  <div class="space-y-6">
    <div>
      <h1 class="text-xl font-semibold text-surface-100">Treasury</h1>
      <p class="text-sm text-surface-500 mt-1">Your prefunded liquidity positions</p>
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

          <p class="text-[10px] text-surface-600 mt-3">
            Updated {{ new Date(pos.updated_at).toLocaleString('en-GB', { day: '2-digit', month: 'short', hour: '2-digit', minute: '2-digit' }) }}
          </p>
        </div>
      </div>

      <EmptyState v-else title="No positions" description="Treasury positions will appear once configured" icon="wallet" />
    </template>
  </div>
</template>

<script setup lang="ts">
import type { Position } from '~/types'

const store = useTreasuryStore()
const money = useMoney()

function isLow(pos: Position) {
  const available = parseFloat(pos.available || '0')
  const min = parseFloat(pos.min_balance || '0')
  return min > 0 && available < min
}

onMounted(() => {
  store.fetchPositions()
})
</script>
