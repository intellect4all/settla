<template>
  <div class="space-y-6">
    <div class="flex items-start justify-between flex-wrap gap-4">
      <div>
        <h1 class="text-xl font-semibold text-surface-100">Webhooks</h1>
        <p class="text-sm text-surface-500 mt-1">Configure webhook delivery for transfer events</p>
      </div>
      <div class="flex gap-2">
        <button
          class="btn-secondary text-sm"
          :disabled="store.testing || !store.webhookUrl"
          @click="handleTest"
        >
          {{ store.testing ? 'Testing...' : 'Test Webhook' }}
        </button>
      </div>
    </div>

    <!-- Test result -->
    <div v-if="store.testResult" class="rounded-lg border p-4" :class="store.testResult.success ? 'bg-emerald-900/20 border-emerald-700' : 'bg-red-900/20 border-red-700'">
      <div class="flex items-center justify-between">
        <p class="text-sm font-medium" :class="store.testResult.success ? 'text-emerald-300' : 'text-red-300'">
          {{ store.testResult.success ? 'Test succeeded' : 'Test failed' }}
        </p>
        <button class="text-xs text-surface-500" @click="store.clearTestResult()">Dismiss</button>
      </div>
      <div class="flex gap-4 mt-1 text-xs" :class="store.testResult.success ? 'text-emerald-400' : 'text-red-400'">
        <span v-if="store.testResult.status_code">HTTP {{ store.testResult.status_code }}</span>
        <span v-if="store.testResult.duration_ms">{{ store.testResult.duration_ms }}ms</span>
        <span v-if="store.testResult.error">{{ store.testResult.error }}</span>
      </div>
    </div>

    <!-- Endpoint config -->
    <div class="bg-surface-900 rounded-lg border border-surface-800 p-5">
      <h2 class="text-sm font-medium text-surface-300 mb-4">Webhook Endpoint</h2>
      <div class="space-y-4">
        <div>
          <label class="text-sm text-surface-400 block mb-1">URL</label>
          <input
            v-model="webhookUrl"
            type="url"
            class="input-field w-full"
            placeholder="https://your-server.com/webhooks/settla"
          />
        </div>
        <div class="flex items-center gap-3">
          <button
            class="btn-primary text-sm"
            :disabled="store.loading || !webhookUrl.trim()"
            @click="handleSave"
          >
            {{ store.loading ? 'Saving...' : 'Save' }}
          </button>
          <span v-if="saved" class="text-xs text-emerald-400">Saved</span>
        </div>
      </div>
    </div>

    <!-- Secret display (shown after save) -->
    <div v-if="store.webhookSecret" class="bg-emerald-900/20 border border-emerald-700 rounded-lg p-4">
      <p class="text-sm font-medium text-emerald-300 mb-2">Webhook Secret</p>
      <p class="text-xs text-emerald-400 mb-2">Use this secret to verify webhook signatures (HMAC-SHA256). It will not be shown again.</p>
      <div class="flex items-center gap-2">
        <code class="flex-1 text-xs font-mono bg-surface-950 text-surface-200 px-3 py-2 rounded border border-surface-700 break-all">
          {{ store.webhookSecret }}
        </code>
        <button class="btn-primary text-xs shrink-0" @click="copySecret">Copy</button>
      </div>
      <button class="text-xs text-surface-500 hover:text-surface-300 mt-2" @click="store.clearSecret()">Dismiss</button>
    </div>

    <!-- Event type subscriptions -->
    <div class="bg-surface-900 rounded-lg border border-surface-800 p-5">
      <div class="flex items-center justify-between mb-4">
        <div>
          <h2 class="text-sm font-medium text-surface-300">Event Subscriptions</h2>
          <p class="text-xs text-surface-500 mt-0.5">Select which events trigger webhook deliveries. Empty = all events.</p>
        </div>
        <button
          class="btn-primary text-xs"
          :disabled="store.subscriptionsLoading"
          @click="handleSaveSubscriptions"
        >
          {{ store.subscriptionsLoading ? 'Saving...' : 'Save' }}
        </button>
      </div>

      <div class="grid grid-cols-1 sm:grid-cols-2 gap-2">
        <label
          v-for="et in store.availableEventTypes" :key="et"
          class="flex items-center gap-2 px-3 py-2 rounded-lg cursor-pointer hover:bg-surface-800 transition-colors"
        >
          <input
            v-model="selectedEvents"
            type="checkbox"
            :value="et"
            class="rounded border-surface-600 text-violet-500 focus:ring-violet-500 bg-surface-800"
          />
          <span class="text-sm text-surface-300 font-mono">{{ et }}</span>
        </label>
      </div>
    </div>

    <!-- Delivery stats -->
    <div class="bg-surface-900 rounded-lg border border-surface-800 p-5">
      <div class="flex items-center justify-between mb-4">
        <h2 class="text-sm font-medium text-surface-300">Delivery Statistics</h2>
        <div class="flex gap-1">
          <button
            v-for="p in statsPeriods" :key="p"
            class="px-2 py-1 text-xs rounded"
            :class="store.statsPeriod === p ? 'bg-violet-600 text-white' : 'text-surface-400 hover:bg-surface-800'"
            @click="store.fetchStats(p)"
          >
            {{ p }}
          </button>
        </div>
      </div>

      <LoadingSpinner v-if="store.statsLoading" size="sm" />
      <div v-else-if="store.stats" class="grid grid-cols-2 sm:grid-cols-4 gap-4">
        <div>
          <p class="text-xs text-surface-500">Total</p>
          <p class="text-lg font-semibold text-surface-100">{{ store.stats.total_deliveries }}</p>
        </div>
        <div>
          <p class="text-xs text-surface-500">Successful</p>
          <p class="text-lg font-semibold text-emerald-400">{{ store.stats.successful }}</p>
        </div>
        <div>
          <p class="text-xs text-surface-500">Failed</p>
          <p class="text-lg font-semibold" :class="store.stats.failed > 0 ? 'text-red-400' : 'text-surface-100'">{{ store.stats.failed }}</p>
        </div>
        <div>
          <p class="text-xs text-surface-500">Avg Latency</p>
          <p class="text-lg font-semibold text-surface-100">{{ store.stats.avg_latency_ms }}ms</p>
        </div>
      </div>
      <div v-if="store.stats" class="mt-3 flex gap-4 text-xs text-surface-500">
        <span>Success rate: <strong class="text-surface-300">{{ store.successRate }}%</strong></span>
        <span>P95 latency: <strong class="text-surface-300">{{ store.stats.p95_latency_ms }}ms</strong></span>
        <span v-if="store.stats.dead_lettered > 0" class="text-amber-400">Dead lettered: {{ store.stats.dead_lettered }}</span>
      </div>
    </div>

    <!-- Recent deliveries -->
    <div class="bg-surface-900 rounded-lg border border-surface-800 p-5">
      <div class="flex items-center justify-between mb-4">
        <h2 class="text-sm font-medium text-surface-300">Recent Deliveries</h2>
        <div class="flex gap-2">
          <select v-model="deliveryStatusFilter" class="input-field text-xs" @change="loadDeliveries">
            <option value="">All statuses</option>
            <option value="delivered">Delivered</option>
            <option value="failed">Failed</option>
            <option value="pending">Pending</option>
            <option value="dead_letter">Dead letter</option>
          </select>
        </div>
      </div>

      <LoadingSpinner v-if="store.deliveriesLoading" size="sm" />
      <template v-else>
        <div v-if="store.deliveries.length" class="space-y-2">
          <div
            v-for="d in store.deliveries" :key="d.id"
            class="flex items-center justify-between px-3 py-2 rounded-lg bg-surface-950 border border-surface-800 text-sm"
          >
            <div class="flex items-center gap-3 min-w-0">
              <span
                class="w-2 h-2 rounded-full shrink-0"
                :class="{
                  'bg-emerald-500': d.status === 'delivered',
                  'bg-red-500': d.status === 'failed',
                  'bg-amber-500': d.status === 'dead_letter',
                  'bg-violet-500': d.status === 'pending',
                }"
              />
              <span class="text-surface-300 font-mono text-xs truncate">{{ d.event_type }}</span>
              <StatusBadge :status="d.status" size="sm" />
            </div>
            <div class="flex items-center gap-3 text-xs text-surface-500 shrink-0">
              <span v-if="d.status_code">HTTP {{ d.status_code }}</span>
              <span v-if="d.duration_ms">{{ d.duration_ms }}ms</span>
              <span>{{ formatTime(d.created_at) }}</span>
              <span class="text-surface-600">{{ d.attempt }}/{{ d.max_attempts }}</span>
            </div>
          </div>
        </div>
        <EmptyState v-else title="No deliveries" description="Webhook deliveries will appear here once transfers are processed" icon="&#x26A1;" />

        <!-- Pagination -->
        <div v-if="store.deliveryTotalCount > 50" class="flex justify-center mt-4">
          <button class="btn-secondary text-xs" @click="loadMore">Load more</button>
        </div>
      </template>
    </div>

    <!-- Info -->
    <div class="bg-surface-900 rounded-lg border border-surface-800 p-5">
      <h2 class="text-sm font-medium text-surface-300 mb-3">How it works</h2>
      <ul class="space-y-2 text-sm text-surface-400">
        <li>Settla sends POST requests to your URL for each transfer state change</li>
        <li>Each request includes an <code class="text-xs bg-surface-800 px-1 py-0.5 rounded">X-Settla-Signature</code> header (HMAC-SHA256)</li>
        <li>Failed deliveries are retried with exponential backoff (up to 5 attempts)</li>
        <li>Events that exhaust all retries are moved to a dead letter queue</li>
      </ul>
    </div>
  </div>
</template>

<script setup lang="ts">
const store = useWebhooksStore()
const authStore = useAuthStore()
const { success, error: showError } = useToast()

const webhookUrl = ref('')
const saved = ref(false)
const selectedEvents = ref<string[]>([])
const deliveryStatusFilter = ref('')
const deliveryPage = ref(0)
const statsPeriods = ['24h', '7d', '30d']

async function handleSave() {
  saved.value = false
  try {
    await store.updateWebhook(webhookUrl.value.trim())
    saved.value = true
    success('Webhook configuration saved')
  } catch {
    showError('Failed to save webhook configuration')
  }
}

async function handleTest() {
  const result = await store.sendTestWebhook()
  if (result?.success) {
    success(`Test webhook delivered (${result.duration_ms}ms)`)
  } else {
    showError(result?.error || 'Test webhook failed')
  }
}

async function handleSaveSubscriptions() {
  try {
    await store.updateSubscriptions(selectedEvents.value)
    success('Event subscriptions updated')
  } catch {
    showError('Failed to update subscriptions')
  }
}

function loadDeliveries() {
  deliveryPage.value = 0
  store.fetchDeliveries({
    status: deliveryStatusFilter.value || undefined,
    page_size: 50,
    page_offset: 0,
  })
}

function loadMore() {
  deliveryPage.value += 50
  store.fetchDeliveries({
    status: deliveryStatusFilter.value || undefined,
    page_size: 50,
    page_offset: deliveryPage.value,
  })
}

function copySecret() {
  if (store.webhookSecret) {
    navigator.clipboard.writeText(store.webhookSecret)
    success('Copied to clipboard')
  }
}

function formatTime(ts: string) {
  if (!ts) return ''
  return new Date(ts).toLocaleString('en-GB', {
    day: '2-digit', month: 'short', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
  })
}

onMounted(() => {
  if (authStore.tenant?.webhook_url) {
    webhookUrl.value = authStore.tenant.webhook_url
    store.setFromProfile(authStore.tenant.webhook_url)
  }
  store.fetchStats()
  store.fetchDeliveries({ page_size: 50 })
  store.fetchSubscriptions()
})

// Sync selected events once subscriptions load
watch(() => store.subscriptions, (subs) => {
  selectedEvents.value = subs.map(s => s.event_type)
}, { immediate: true })
</script>
