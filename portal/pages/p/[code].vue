<template>
  <div>
    <!-- Loading -->
    <div v-if="loading" class="p-8 space-y-4">
      <SkeletonLoader variant="text" :lines="2" />
      <SkeletonLoader variant="card" height="200px" />
      <SkeletonLoader variant="text" :lines="1" width="60%" />
    </div>

    <!-- Error -->
    <div v-else-if="error" class="p-8 text-center">
      <div class="w-12 h-12 rounded-full bg-red-900/30 flex items-center justify-center mb-4 mx-auto">
        <Icon name="alert-circle" :size="24" class="text-red-400" />
      </div>
      <h2 class="text-lg font-semibold text-surface-100 mb-2">Payment Unavailable</h2>
      <p class="text-surface-400 text-sm">{{ error }}</p>
    </div>

    <!-- Payment Page -->
    <div v-else-if="session" class="p-6 animate-fade-in">
      <!-- Status Banner -->
      <div
        :class="statusBannerClass"
        class="rounded-lg px-4 py-3 mb-6 text-center text-sm font-medium"
      >
        {{ statusLabel }}
      </div>

      <!-- Amount -->
      <div class="text-center mb-6">
        <p class="text-surface-500 text-xs uppercase tracking-wide mb-1">Send exactly</p>
        <p class="text-3xl font-bold text-surface-100">
          {{ session.expectedAmount }} {{ session.token }}
        </p>
        <p class="text-surface-500 text-xs mt-1">on {{ session.chain }}</p>
      </div>

      <!-- Deposit Address -->
      <div v-if="!isTerminal" class="mb-6">
        <div class="card p-4 text-center">
          <p class="text-xs text-surface-500 mb-2">Deposit Address</p>
          <p class="font-mono text-sm text-surface-200 break-all select-all">{{ session.depositAddress }}</p>
          <AppButton variant="secondary" size="sm" class="mt-3" @click="copyAddress">
            {{ copied ? 'Copied!' : 'Copy Address' }}
          </AppButton>
        </div>
      </div>

      <!-- Countdown Timer -->
      <div v-if="!isTerminal && countdown > 0" class="text-center mb-6">
        <p class="text-surface-500 text-xs uppercase tracking-wide mb-1">Expires in</p>
        <p class="text-lg font-mono text-surface-200">{{ formatCountdown(countdown) }}</p>
      </div>

      <!-- Status Progression -->
      <div v-if="session.status !== 'PENDING_PAYMENT' && !isTerminal" class="mb-6">
        <div class="flex items-center justify-center gap-2">
          <div v-for="step in statusSteps" :key="step.key"
            :class="step.active ? 'bg-violet-500' : 'bg-surface-700'"
            class="h-2 w-2 rounded-full transition-colors"
          ></div>
        </div>
        <p class="text-center text-surface-400 text-xs mt-2">{{ statusDetail }}</p>
      </div>

      <!-- Success: Redirect -->
      <div v-if="isSuccess && link?.redirectUrl" class="text-center">
        <p class="text-green-400 text-sm mb-2">Payment received successfully!</p>
        <p class="text-surface-500 text-xs">
          Redirecting in {{ redirectCountdown }}s...
        </p>
      </div>

      <!-- Received Amount -->
      <div v-if="session.receivedAmount && session.receivedAmount !== '0'" class="mt-4 text-center">
        <p class="text-surface-500 text-xs">
          Received: {{ session.receivedAmount }} {{ session.token }}
        </p>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
definePageMeta({
  layout: 'pay',
})

const route = useRoute()
const code = route.params.code as string

const loading = ref(true)
const error = ref<string | null>(null)
const link = ref<any>(null)
const session = ref<any>(null)
const copied = ref(false)
const countdown = ref(0)
const redirectCountdown = ref(5)

let pollTimer: ReturnType<typeof setInterval> | null = null
let countdownTimer: ReturnType<typeof setInterval> | null = null
let redirectTimer: ReturnType<typeof setInterval> | null = null

const config = useRuntimeConfig()
const baseURL = config.public.apiBase as string

async function publicFetch<T>(path: string, opts: { method?: string } = {}): Promise<T> {
  return $fetch<T>(`${baseURL}${path}`, {
    method: (opts.method ?? 'GET') as any,
    headers: { 'Content-Type': 'application/json' },
  })
}

const isTerminal = computed(() => {
  if (!session.value) return false
  return ['SETTLED', 'HELD', 'FAILED', 'CANCELLED', 'EXPIRED'].includes(session.value.status)
})

const isSuccess = computed(() => {
  if (!session.value) return false
  return ['SETTLED', 'HELD', 'CREDITED'].includes(session.value.status)
})

const statusSteps = computed(() => {
  const steps = ['DETECTED', 'CONFIRMED', 'CREDITED', 'SETTLED']
  const currentIdx = steps.indexOf(session.value?.status ?? '')
  return steps.map((key, idx) => ({
    key,
    active: idx <= currentIdx,
  }))
})

const statusLabel = computed(() => {
  const s = session.value?.status
  const labels: Record<string, string> = {
    PENDING_PAYMENT: 'Awaiting Payment',
    DETECTED: 'Payment Detected',
    CONFIRMED: 'Payment Confirmed',
    CREDITING: 'Processing...',
    CREDITED: 'Payment Credited',
    SETTLING: 'Converting...',
    SETTLED: 'Payment Complete',
    HELD: 'Payment Held',
    EXPIRED: 'Payment Expired',
    FAILED: 'Payment Failed',
    CANCELLED: 'Payment Cancelled',
  }
  return labels[s] ?? s
})

const statusDetail = computed(() => {
  const s = session.value?.status
  const details: Record<string, string> = {
    DETECTED: 'Waiting for blockchain confirmations...',
    CONFIRMED: 'Crediting your account...',
    CREDITING: 'Processing ledger entries...',
    CREDITED: 'Initiating settlement...',
    SETTLING: 'Converting to fiat...',
  }
  return details[s] ?? ''
})

const statusBannerClass = computed(() => {
  const s = session.value?.status
  if (isSuccess.value) return 'bg-green-900/30 text-green-400 border border-green-800'
  if (s === 'FAILED' || s === 'CANCELLED') return 'bg-red-900/30 text-red-400 border border-red-800'
  if (s === 'EXPIRED') return 'bg-yellow-900/30 text-yellow-400 border border-yellow-800'
  if (s === 'DETECTED' || s === 'CONFIRMED') return 'bg-blue-900/30 text-blue-400 border border-blue-800'
  return 'bg-surface-800 text-surface-300 border border-surface-700'
})

function formatCountdown(seconds: number) {
  const m = Math.floor(seconds / 60)
  const s = seconds % 60
  return `${m.toString().padStart(2, '0')}:${s.toString().padStart(2, '0')}`
}

function copyAddress() {
  if (session.value?.depositAddress) {
    navigator.clipboard.writeText(session.value.depositAddress)
    copied.value = true
    setTimeout(() => { copied.value = false }, 2000)
  }
}

function startCountdown() {
  if (!session.value?.expiresAt) return
  countdownTimer = setInterval(() => {
    const expiresAt = new Date(session.value.expiresAt).getTime()
    const remaining = Math.max(0, Math.floor((expiresAt - Date.now()) / 1000))
    countdown.value = remaining
    if (remaining <= 0 && countdownTimer) {
      clearInterval(countdownTimer)
    }
  }, 1000)
}

function startPolling() {
  pollTimer = setInterval(async () => {
    if (!session.value?.id || isTerminal.value) {
      if (pollTimer) clearInterval(pollTimer)
      return
    }
    try {
      const result = await publicFetch<any>(`/v1/deposits/${session.value.id}/public-status`)
      if (result) {
        // Merge status fields into session
        Object.assign(session.value, result)
      }
    } catch {
      // Ignore polling errors
    }
  }, 3000)
}

function startRedirectCountdown() {
  redirectTimer = setInterval(() => {
    redirectCountdown.value--
    if (redirectCountdown.value <= 0 && link.value?.redirectUrl) {
      if (redirectTimer) clearInterval(redirectTimer)
      window.location.href = link.value.redirectUrl
    }
  }, 1000)
}

// Watch for success → start redirect
watch(isSuccess, (val) => {
  if (val && link.value?.redirectUrl) {
    startRedirectCountdown()
  }
})

onMounted(async () => {
  try {
    // 1. Resolve the payment link
    const resolveResult = await publicFetch<any>(`/v1/payment-links/resolve/${code}`)
    link.value = resolveResult?.link

    if (!link.value) {
      error.value = 'Payment link not found'
      loading.value = false
      return
    }

    // 2. Redeem the payment link (create deposit session)
    const redeemResult = await publicFetch<any>(`/v1/payment-links/redeem/${code}`, { method: 'POST' })
    session.value = redeemResult?.session
    link.value = redeemResult?.link ?? link.value

    if (!session.value) {
      error.value = 'Failed to create payment session'
      loading.value = false
      return
    }

    loading.value = false

    // 3. Start countdown and polling
    startCountdown()
    startPolling()
  } catch (e: any) {
    const msg = e?.data?.message ?? e?.message ?? 'Something went wrong'
    error.value = msg
    loading.value = false
  }
})

onUnmounted(() => {
  if (pollTimer) clearInterval(pollTimer)
  if (countdownTimer) clearInterval(countdownTimer)
  if (redirectTimer) clearInterval(redirectTimer)
})
</script>
