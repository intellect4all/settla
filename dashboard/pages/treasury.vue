<template>
  <div>
    <div class="flex items-center justify-between mb-6">
      <div>
        <h1 class="text-2xl font-semibold text-surface-100">Treasury</h1>
        <p class="text-sm text-surface-500 mt-0.5">
          Position management &amp; liquidity monitoring
        </p>
      </div>
      <button class="btn-secondary text-sm" @click="refresh">Refresh</button>
    </div>

    <!-- Total Available by Currency -->
    <div v-if="totalAvailable && Object.keys(totalAvailable).length" class="flex flex-wrap gap-4 mb-6">
      <div
        v-for="(amount, currency) in totalAvailable"
        :key="currency"
        class="card px-4 py-3"
      >
        <p class="text-xs text-surface-500 mb-0.5">{{ currency }} Available</p>
        <MoneyDisplay
          :amount="amount"
          :currency="String(currency)"
          size="lg"
          color="positive"
        />
      </div>
    </div>

    <!-- Alerts -->
    <div v-if="alertPositions.length > 0" class="space-y-2 mb-6">
      <AlertBanner
        v-for="pos in alertPositions"
        :key="pos.id"
        type="warning"
        :title="`${pos.currency} at ${pos.location} below minimum`"
        :description="`Available: ${format(pos.available, pos.currency)} / Min: ${format(pos.min_balance, pos.currency)}`"
      />
    </div>

    <!-- Available vs Locked Overview -->
    <div v-if="positions.length" class="card p-5 mb-6">
      <h3 class="text-sm font-semibold text-surface-200 mb-4">Available vs Locked</h3>
      <div class="space-y-3">
        <div v-for="pos in positions" :key="pos.id" class="flex items-center gap-3">
          <span
            class="text-xs text-surface-400 w-32 truncate"
            :title="pos.currency + ' ' + pos.location"
          >
            {{ pos.currency }} {{ pos.location.split(':').pop() }}
          </span>
          <div class="flex-1 h-5 bg-surface-800 rounded-full overflow-hidden flex">
            <div
              :style="{ width: availPercent(pos) + '%' }"
              class="bg-emerald-500/80 transition-all duration-500"
            />
            <div
              :style="{ width: lockPercent(pos) + '%' }"
              class="bg-amber-500/80 transition-all duration-500"
            />
          </div>
          <span class="text-xs text-surface-500 w-28 text-right font-mono">
            {{ format(pos.balance, pos.currency) }}
          </span>
        </div>
      </div>
      <div class="flex gap-6 mt-3 text-xs text-surface-500">
        <span class="flex items-center gap-1.5">
          <span class="w-2.5 h-2.5 rounded bg-emerald-500/80" /> Available
        </span>
        <span class="flex items-center gap-1.5">
          <span class="w-2.5 h-2.5 rounded bg-amber-500/80" /> Locked
        </span>
      </div>
    </div>

    <LoadingSpinner v-if="loading && !positions.length" full-page size="lg" />

    <!-- Position Cards Grid -->
    <template v-if="positions.length">
      <h3 class="text-sm font-semibold text-surface-200 mb-3">All Positions</h3>
      <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
        <PositionCard
          v-for="pos in positions"
          :key="pos.id"
          :position="pos"
        />
      </div>
    </template>

    <EmptyState
      v-if="!loading && !positions.length"
      title="No positions"
      description="Treasury positions will appear here once configured."
    />
  </div>
</template>

<script setup lang="ts">
import type { Position } from '~/types'
import Decimal from 'decimal.js'

const config = useRuntimeConfig()
const api = useApi()
const { format } = useMoney()

const { data: positionsData, loading, refresh: refreshPositions } = usePolling(
  () => api.getPositions(),
  config.public.pollIntervalTreasury as number,
  { immediate: true },
)

const { data: liquidityData, refresh: refreshLiquidity } = usePolling(
  () => api.getLiquidity().catch(() => null),
  config.public.pollIntervalTreasury as number,
  { immediate: true },
)

const positions = computed(() => positionsData.value?.positions ?? [])
const totalAvailable = computed(() => liquidityData.value?.total_available ?? {})
const alertPositions = computed(() => liquidityData.value?.alert_positions ?? [])

function availPercent(pos: Position) {
  const bal = new Decimal(pos.balance || '0')
  if (bal.isZero()) return 0
  return new Decimal(pos.available || '0').div(bal).mul(100).toNumber()
}

function lockPercent(pos: Position) {
  const bal = new Decimal(pos.balance || '0')
  if (bal.isZero()) return 0
  return new Decimal(pos.locked || '0').div(bal).mul(100).toNumber()
}

function refresh() {
  refreshPositions()
  refreshLiquidity()
}
</script>
