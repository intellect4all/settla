<template>
  <div class="space-y-6">
    <div>
      <h1 class="text-xl font-semibold text-surface-100">Crypto Deposit Settings</h1>
      <p class="text-sm text-surface-500 mt-1">Configure how your tenant handles crypto deposits</p>
    </div>

    <div
      v-if="loadError"
      class="rounded-lg border border-yellow-800 bg-yellow-900/20 px-4 py-3 animate-fade-in"
    >
      <p class="text-sm font-medium text-yellow-400">Settings not loaded from server</p>
      <p class="text-xs text-yellow-500 mt-0.5">Showing defaults. Changes will be saved once the settings endpoint is available.</p>
    </div>

    <div v-if="loading" class="space-y-4">
      <SkeletonLoader variant="card" height="80px" />
      <SkeletonLoader variant="card" height="100px" />
      <SkeletonLoader variant="card" height="120px" />
      <SkeletonLoader variant="card" height="140px" />
    </div>

    <template v-else>
      <!-- Enable / Disable -->
      <div class="card p-6 animate-fade-in">
        <div class="flex items-center justify-between">
          <div>
            <h2 class="text-sm font-medium text-surface-300">Crypto Deposits</h2>
            <p class="text-xs text-surface-500 mt-1">Enable or disable crypto deposit sessions for your tenant</p>
          </div>
          <button
            class="relative inline-flex h-6 w-11 items-center rounded-full transition-colors"
            :class="form.crypto_enabled ? 'bg-brand-600' : 'bg-surface-700'"
            @click="form.crypto_enabled = !form.crypto_enabled"
          >
            <span
              class="inline-block h-4 w-4 rounded-full bg-white transition-transform"
              :class="form.crypto_enabled ? 'translate-x-6' : 'translate-x-1'"
            />
          </button>
        </div>
      </div>

      <!-- Supported Chains -->
      <div class="card p-6 animate-fade-in space-y-3">
        <h2 class="text-sm font-medium text-surface-300">Supported Chains</h2>
        <p class="text-xs text-surface-500">Select which blockchains tenants can deposit on</p>
        <div class="flex gap-6 pt-1">
          <label v-for="chain in availableChains" :key="chain" class="flex items-center gap-2 cursor-pointer">
            <input
              type="checkbox"
              :value="chain"
              v-model="form.supported_chains"
              class="h-4 w-4 rounded border-surface-600 bg-surface-800 text-brand-600 focus:ring-brand-500"
            />
            <span class="text-sm text-surface-200">{{ chain }}</span>
          </label>
        </div>
      </div>

      <!-- Settlement Preference -->
      <div class="card p-6 animate-fade-in space-y-3">
        <h2 class="text-sm font-medium text-surface-300">Default Settlement Preference</h2>
        <p class="text-xs text-surface-500">How received crypto should be handled after confirmation</p>
        <div class="flex flex-col gap-3 pt-1">
          <label v-for="opt in settlementOptions" :key="opt.value" class="flex items-start gap-3 cursor-pointer">
            <input
              type="radio"
              :value="opt.value"
              v-model="form.default_settlement_pref"
              name="settlement_pref"
              class="mt-0.5 h-4 w-4 border-surface-600 bg-surface-800 text-brand-600 focus:ring-brand-500"
            />
            <div>
              <span class="text-sm text-surface-200">{{ opt.label }}</span>
              <p class="text-xs text-surface-500">{{ opt.description }}</p>
            </div>
          </label>
        </div>
      </div>

      <!-- Payment Tolerance & Session TTL -->
      <div class="card p-6 animate-fade-in space-y-4">
        <h2 class="text-sm font-medium text-surface-300">Deposit Parameters</h2>

        <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
          <div>
            <label class="text-xs text-surface-400 block mb-1">Payment Tolerance (BPS)</label>
            <div class="flex items-center gap-3">
              <input
                v-model.number="form.payment_tolerance_bps"
                type="range"
                min="0"
                max="200"
                step="5"
                class="flex-1 accent-brand-600"
              />
              <input
                v-model.number="form.payment_tolerance_bps"
                type="number"
                min="0"
                max="200"
                class="input w-20 text-sm text-right font-mono"
              />
            </div>
            <p class="text-xs text-surface-500 mt-1">Acceptable underpayment tolerance. 50 bps = 0.5%</p>
          </div>

          <div>
            <label class="text-xs text-surface-400 block mb-1">Session TTL (minutes)</label>
            <input
              v-model.number="sessionTtlMinutes"
              type="number"
              min="5"
              max="1440"
              class="input w-full text-sm font-mono"
            />
            <p class="text-xs text-surface-500 mt-1">How long a deposit session stays active before expiring</p>
          </div>
        </div>
      </div>

      <!-- Min Confirmations -->
      <div class="card p-6 animate-fade-in space-y-4">
        <h2 class="text-sm font-medium text-surface-300">Minimum Confirmations</h2>
        <p class="text-xs text-surface-500">Required block confirmations before crediting a deposit</p>

        <div class="grid grid-cols-1 md:grid-cols-3 gap-6">
          <div>
            <label class="text-xs text-surface-400 block mb-1">Tron</label>
            <input
              v-model.number="form.min_confirmations_tron"
              type="number"
              min="1"
              class="input w-full text-sm font-mono"
            />
          </div>
          <div>
            <label class="text-xs text-surface-400 block mb-1">Ethereum</label>
            <input
              v-model.number="form.min_confirmations_eth"
              type="number"
              min="1"
              class="input w-full text-sm font-mono"
            />
          </div>
          <div>
            <label class="text-xs text-surface-400 block mb-1">Base</label>
            <input
              v-model.number="form.min_confirmations_base"
              type="number"
              min="1"
              class="input w-full text-sm font-mono"
            />
          </div>
        </div>
      </div>

      <!-- Save -->
      <div class="flex items-center justify-end gap-3">
        <p v-if="saveError" class="text-xs text-red-400">{{ saveError }}</p>
        <p v-if="saveSuccess" class="text-xs text-emerald-400">Settings saved successfully</p>
        <AppButton :loading="saving" @click="handleSave">Save Settings</AppButton>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import type { CryptoSettings } from '~/types'

const api = usePortalApi()

const loading = ref(true)
const saving = ref(false)
const saveError = ref('')
const saveSuccess = ref(false)
const loadError = ref(false)

const availableChains = ['Tron', 'Ethereum', 'Base']

const settlementOptions = [
  { value: 'AUTO_CONVERT', label: 'Auto Convert', description: 'Automatically convert received crypto to fiat at the best available rate' },
  { value: 'HOLD', label: 'Hold', description: 'Keep received crypto as-is until manually converted' },
  { value: 'THRESHOLD', label: 'Threshold', description: 'Auto-convert only when balance exceeds a configured threshold' },
]

const form = reactive<CryptoSettings>({
  crypto_enabled: false,
  supported_chains: ['Tron'],
  default_settlement_pref: 'AUTO_CONVERT',
  payment_tolerance_bps: 50,
  default_session_ttl_secs: 3600,
  min_confirmations_tron: 19,
  min_confirmations_eth: 12,
  min_confirmations_base: 12,
})

const sessionTtlMinutes = computed({
  get: () => Math.round(form.default_session_ttl_secs / 60),
  set: (v: number) => { form.default_session_ttl_secs = v * 60 },
})

async function handleSave() {
  saving.value = true
  saveError.value = ''
  saveSuccess.value = false
  try {
    const res = await api.updateCryptoSettings({ ...form })
    Object.assign(form, res)
    saveSuccess.value = true
    setTimeout(() => { saveSuccess.value = false }, 3000)
  } catch (e: any) {
    saveError.value = e.data?.message ?? e.message ?? 'Failed to save settings'
  } finally {
    saving.value = false
  }
}

onMounted(async () => {
  try {
    const res = await api.getCryptoSettings()
    Object.assign(form, res)
  } catch {
    // Use defaults if endpoint not yet available
    loadError.value = true
  } finally {
    loading.value = false
  }
})
</script>
