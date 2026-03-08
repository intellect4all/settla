<template>
  <div class="card p-4">
    <div class="flex items-center justify-between mb-3">
      <div>
        <h4 class="text-sm font-semibold text-surface-200">{{ position.currency }}</h4>
        <p class="text-xs text-surface-500 mt-0.5">{{ position.location }}</p>
      </div>
      <span
        :class="healthColor"
        class="w-2.5 h-2.5 rounded-full"
      />
    </div>

    <!-- Balance bar -->
    <div class="mb-3">
      <div class="flex justify-between text-xs text-surface-500 mb-1">
        <span>Available</span>
        <span>Locked</span>
      </div>
      <div class="h-2 bg-surface-800 rounded-full overflow-hidden flex">
        <div
          :style="{ width: availablePercent + '%' }"
          class="bg-emerald-500 rounded-l-full transition-all duration-500"
        />
        <div
          :style="{ width: lockedPercent + '%' }"
          class="bg-amber-500 rounded-r-full transition-all duration-500"
        />
      </div>
    </div>

    <!-- Amounts -->
    <div class="grid grid-cols-3 gap-2 text-center">
      <div>
        <p class="text-xs text-surface-500 mb-0.5">Balance</p>
        <MoneyDisplay :amount="position.balance" :currency="position.currency" size="sm" />
      </div>
      <div>
        <p class="text-xs text-surface-500 mb-0.5">Locked</p>
        <MoneyDisplay :amount="position.locked" :currency="position.currency" size="sm" color="negative" />
      </div>
      <div>
        <p class="text-xs text-surface-500 mb-0.5">Available</p>
        <MoneyDisplay :amount="position.available" :currency="position.currency" size="sm" color="positive" />
      </div>
    </div>

    <!-- Min balance warning -->
    <div v-if="isBelowMin" class="mt-3 flex items-center gap-1.5 text-xs text-amber-400 bg-amber-500/10 rounded-lg px-2.5 py-1.5">
      <span>&#9888;</span>
      Below minimum ({{ format(position.min_balance, position.currency) }})
    </div>
  </div>
</template>

<script setup lang="ts">
import type { Position } from '~/types'
import Decimal from 'decimal.js'

const props = defineProps<{
  position: Position
}>()

const { format } = useMoney()

const balance = computed(() => new Decimal(props.position.balance || '0'))
const locked = computed(() => new Decimal(props.position.locked || '0'))
const available = computed(() => new Decimal(props.position.available || '0'))
const minBalance = computed(() => new Decimal(props.position.min_balance || '0'))

const lockedPercent = computed(() => {
  if (balance.value.isZero()) return 0
  return locked.value.div(balance.value).mul(100).toNumber()
})

const availablePercent = computed(() => {
  if (balance.value.isZero()) return 0
  return available.value.div(balance.value).mul(100).toNumber()
})

const isBelowMin = computed(() => {
  return !minBalance.value.isZero() && balance.value.lt(minBalance.value)
})

const healthColor = computed(() => {
  if (isBelowMin.value) return 'bg-red-500 animate-pulse'
  // Below 120% of min = yellow warning
  if (!minBalance.value.isZero() && balance.value.lt(minBalance.value.mul(1.2))) return 'bg-amber-500'
  return 'bg-emerald-500'
})
</script>
