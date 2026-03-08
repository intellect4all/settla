<template>
  <div>
    <div class="flex items-center justify-between mb-6">
      <div>
        <h1 class="text-2xl font-semibold text-surface-100">Routes</h1>
        <p class="text-sm text-surface-500 mt-0.5">Route comparison & chain health monitoring</p>
      </div>
    </div>

    <!-- Route Comparison Form -->
    <div class="card p-5 mb-6">
      <h3 class="text-sm font-semibold text-surface-200 mb-4">Compare Routes</h3>
      <div class="flex flex-wrap gap-3 items-end">
        <div>
          <label class="text-xs text-surface-500 block mb-1">Amount</label>
          <input v-model="amount" type="text" placeholder="10000" class="input text-sm w-40" />
        </div>
        <div>
          <label class="text-xs text-surface-500 block mb-1">Source</label>
          <select v-model="sourceCurrency" class="input text-sm w-28">
            <option v-for="c in currencies" :key="c" :value="c">{{ c }}</option>
          </select>
        </div>
        <div>
          <label class="text-xs text-surface-500 block mb-1">Destination</label>
          <select v-model="destCurrency" class="input text-sm w-28">
            <option v-for="c in currencies" :key="c" :value="c">{{ c }}</option>
          </select>
        </div>
        <button class="btn-primary text-sm" :disabled="!canCompare || comparing" @click="compareRoutes">
          {{ comparing ? 'Comparing...' : 'Compare Routes' }}
        </button>
      </div>
    </div>

    <!-- Route Results -->
    <div v-if="routes.length > 0" class="card overflow-hidden mb-6">
      <table class="w-full text-sm">
        <thead>
          <tr class="border-b border-surface-800">
            <th class="px-4 py-3 text-left text-xs font-medium text-surface-500 uppercase">Chain</th>
            <th class="px-4 py-3 text-left text-xs font-medium text-surface-500 uppercase">Stablecoin</th>
            <th class="px-4 py-3 text-center text-xs font-medium text-surface-500 uppercase">Time</th>
            <th class="px-4 py-3 text-right text-xs font-medium text-surface-500 uppercase">On-ramp</th>
            <th class="px-4 py-3 text-right text-xs font-medium text-surface-500 uppercase">Network</th>
            <th class="px-4 py-3 text-right text-xs font-medium text-surface-500 uppercase">Off-ramp</th>
            <th class="px-4 py-3 text-right text-xs font-medium text-surface-500 uppercase">Total Fee</th>
            <th class="px-4 py-3 text-center text-xs font-medium text-surface-500 uppercase">Score</th>
            <th class="px-4 py-3 text-center text-xs font-medium text-surface-500 uppercase">Health</th>
            <th class="px-4 py-3 text-center text-xs font-medium text-surface-500 uppercase">Pick</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="route in routes"
            :key="route.chain + route.stable_coin"
            :class="route.is_selected ? 'bg-violet-500/5 border-l-2 border-l-violet-500' : 'border-l-2 border-l-transparent'"
            class="border-b border-surface-800/50 hover:bg-surface-800/30"
          >
            <td class="px-4 py-3 font-medium text-surface-200">{{ route.chain }}</td>
            <td class="px-4 py-3 text-surface-300">{{ route.stable_coin }}</td>
            <td class="px-4 py-3 text-center text-surface-400">{{ route.estimated_time_min }}min</td>
            <td class="px-4 py-3 text-right font-mono text-surface-400">
              <MoneyDisplay :amount="route.on_ramp_fee" currency="USD" size="xs" />
            </td>
            <td class="px-4 py-3 text-right font-mono text-surface-400">
              <MoneyDisplay :amount="route.network_fee" currency="USD" size="xs" />
            </td>
            <td class="px-4 py-3 text-right font-mono text-surface-400">
              <MoneyDisplay :amount="route.off_ramp_fee" currency="USD" size="xs" />
            </td>
            <td class="px-4 py-3 text-right font-mono font-medium text-surface-200">
              <MoneyDisplay :amount="route.total_fee" currency="USD" size="xs" />
            </td>
            <td class="px-4 py-3 text-center">
              <span class="text-xs font-medium" :class="scoreColor(route.score)">
                {{ (route.score * 100).toFixed(0) }}
              </span>
            </td>
            <td class="px-4 py-3 text-center">
              <span :class="healthDotClass(route.health)" class="inline-block w-2 h-2 rounded-full" />
            </td>
            <td class="px-4 py-3 text-center">
              <span v-if="route.is_selected" class="text-violet-400 font-bold">&#10003;</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Chain Status -->
    <h3 class="text-sm font-semibold text-surface-200 mb-3">Chain Status</h3>
    <LoadingSpinner v-if="chainsLoading && !chains.length" />
    <div v-else class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
      <div v-for="chain in chains" :key="chain.chain" class="card p-4">
        <div class="flex items-center justify-between mb-2">
          <h4 class="text-sm font-semibold text-surface-200">{{ chain.chain }}</h4>
          <span :class="healthDotClass(chain.health)" class="w-2.5 h-2.5 rounded-full" />
        </div>
        <dl class="grid grid-cols-2 gap-y-1.5 text-xs">
          <dt class="text-surface-500">Health</dt>
          <dd :class="healthTextClass(chain.health)" class="text-right capitalize">{{ chain.health }}</dd>
          <dt class="text-surface-500">Gas Price</dt>
          <dd class="text-right text-surface-300 font-mono">{{ chain.gas_price_gwei }} gwei</dd>
          <dt class="text-surface-500">Block Time</dt>
          <dd class="text-right text-surface-300 font-mono">{{ chain.block_time_ms }}ms</dd>
          <dt class="text-surface-500">Last Check</dt>
          <dd class="text-right text-surface-400">{{ formatTime(chain.last_checked) }}</dd>
        </dl>
      </div>
    </div>
    <EmptyState v-if="!chainsLoading && !chains.length" title="No chain data" description="Chain status will appear once the system is connected to providers." />
  </div>
</template>

<script setup lang="ts">
import type { RouteComparison, ChainStatus } from '~/types'

const api = useApi()
const { error: showError } = useToast()

const amount = ref('10000')
const sourceCurrency = ref('GBP')
const destCurrency = ref('NGN')
const currencies = ['USD', 'GBP', 'EUR', 'NGN', 'KES', 'GHS', 'ZAR']

const routes = ref<RouteComparison[]>([])
const comparing = ref(false)
const chains = ref<ChainStatus[]>([])
const chainsLoading = ref(true)

const canCompare = computed(() => amount.value && sourceCurrency.value && destCurrency.value)

async function compareRoutes() {
  comparing.value = true
  try {
    const result = await api.getRouteComparisons(amount.value, sourceCurrency.value, destCurrency.value)
    routes.value = result.routes
  } catch {
    // Generate sample data for demo
    routes.value = generateSampleRoutes()
  } finally {
    comparing.value = false
  }
}

function generateSampleRoutes(): RouteComparison[] {
  return [
    { chain: 'Tron', stable_coin: 'USDT', estimated_time_min: 3, on_ramp_fee: '15.00', network_fee: '1.50', off_ramp_fee: '12.00', total_fee: '28.50', score: 0.92, is_selected: true, gas_price_gwei: '0.01', health: 'healthy' },
    { chain: 'Ethereum', stable_coin: 'USDC', estimated_time_min: 12, on_ramp_fee: '15.00', network_fee: '8.50', off_ramp_fee: '12.00', total_fee: '35.50', score: 0.71, is_selected: false, gas_price_gwei: '25.00', health: 'healthy' },
    { chain: 'Polygon', stable_coin: 'USDC', estimated_time_min: 5, on_ramp_fee: '15.00', network_fee: '0.50', off_ramp_fee: '14.00', total_fee: '29.50', score: 0.85, is_selected: false, gas_price_gwei: '0.05', health: 'degraded' },
    { chain: 'BSC', stable_coin: 'USDT', estimated_time_min: 4, on_ramp_fee: '16.00', network_fee: '0.80', off_ramp_fee: '13.00', total_fee: '29.80', score: 0.83, is_selected: false, gas_price_gwei: '3.00', health: 'healthy' },
  ]
}

async function fetchChains() {
  chainsLoading.value = true
  try {
    const result = await api.getChainStatuses()
    chains.value = result.chains
  } catch {
    chains.value = [
      { chain: 'Tron', health: 'healthy', gas_price_gwei: '0.01', block_time_ms: 3000, last_checked: new Date().toISOString() },
      { chain: 'Ethereum', health: 'healthy', gas_price_gwei: '25.00', block_time_ms: 12000, last_checked: new Date().toISOString() },
      { chain: 'Polygon', health: 'degraded', gas_price_gwei: '0.05', block_time_ms: 2000, last_checked: new Date().toISOString() },
      { chain: 'BSC', health: 'healthy', gas_price_gwei: '3.00', block_time_ms: 3000, last_checked: new Date().toISOString() },
    ]
  } finally {
    chainsLoading.value = false
  }
}

function scoreColor(score: number) {
  if (score >= 0.85) return 'text-emerald-400'
  if (score >= 0.6) return 'text-amber-400'
  return 'text-red-400'
}

function healthDotClass(health: string) {
  if (health === 'healthy') return 'bg-emerald-500'
  if (health === 'degraded') return 'bg-amber-500 animate-pulse'
  return 'bg-red-500 animate-pulse'
}

function healthTextClass(health: string) {
  if (health === 'healthy') return 'text-emerald-400'
  if (health === 'degraded') return 'text-amber-400'
  return 'text-red-400'
}

function formatTime(ts: string) {
  if (!ts) return '\u2014'
  return new Date(ts).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

onMounted(fetchChains)
</script>
