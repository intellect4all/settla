<template>
  <div class="space-y-6">
    <div class="flex items-center gap-3">
      <NuxtLink to="/deposits" class="text-surface-400 hover:text-surface-200 transition-colors">&larr; Back</NuxtLink>
      <h1 class="text-xl font-semibold text-surface-100">Deposit Session</h1>
    </div>

    <div v-if="loading" class="space-y-4">
      <SkeletonLoader variant="card" height="160px" />
      <SkeletonLoader variant="card" height="80px" />
      <SkeletonLoader variant="card" height="120px" />
    </div>

    <template v-else-if="session">
      <!-- Header card -->
      <div class="card p-6 animate-fade-in">
        <div class="flex items-center justify-between mb-4">
          <div>
            <span
              class="text-xs font-medium px-2 py-0.5 rounded"
              :class="statusClass(session.status)"
            >{{ session.status }}</span>
            <p class="text-xs text-surface-500 mt-2 font-mono">{{ session.id }}</p>
          </div>
          <AppButton
            v-if="session.status === 'PENDING_PAYMENT'"
            variant="danger"
            :loading="cancelling"
            @click="handleCancel"
          >Cancel Session</AppButton>
        </div>

        <div class="grid grid-cols-2 lg:grid-cols-4 gap-4">
          <div>
            <dt class="text-xs text-surface-500">Chain / Token</dt>
            <dd class="text-sm text-surface-200 mt-1">{{ session.chain }} / {{ session.token }}</dd>
          </div>
          <div>
            <dt class="text-xs text-surface-500">Expected Amount</dt>
            <dd class="text-sm text-surface-200 mt-1 font-mono">{{ money.format(session.expectedAmount, session.currency) }}</dd>
          </div>
          <div>
            <dt class="text-xs text-surface-500">Received Amount</dt>
            <dd class="text-sm font-mono mt-1" :class="session.receivedAmount !== '0' ? 'text-emerald-400' : 'text-surface-400'">
              {{ money.format(session.receivedAmount, session.currency) }}
            </dd>
          </div>
          <div>
            <dt class="text-xs text-surface-500">Net Amount</dt>
            <dd class="text-sm text-surface-200 mt-1 font-mono">{{ money.format(session.netAmount, session.currency) }}</dd>
          </div>
        </div>
      </div>

      <!-- Deposit address -->
      <div class="card p-6 animate-slide-up">
        <h2 class="text-sm font-medium text-surface-300 mb-3">Deposit Address</h2>
        <div class="flex items-center gap-3">
          <code class="text-sm text-surface-100 bg-surface-800 px-3 py-2 rounded font-mono flex-1 break-all">
            {{ session.depositAddress }}
          </code>
          <button
            class="text-xs text-surface-400 hover:text-surface-200 bg-surface-800 px-3 py-2 rounded"
            @click="copyAddr"
          >{{ copied ? 'Copied' : 'Copy' }}</button>
        </div>
      </div>

      <!-- Details -->
      <div class="card p-6 animate-slide-up">
        <h2 class="text-sm font-medium text-surface-300 mb-3">Details</h2>
        <dl class="grid grid-cols-2 gap-x-6 gap-y-3 text-sm">
          <div>
            <dt class="text-surface-500">Fee ({{ session.collectionFeeBps }} bps)</dt>
            <dd class="text-surface-300 font-mono">{{ money.format(session.feeAmount, session.currency) }}</dd>
          </div>
          <div>
            <dt class="text-surface-500">Settlement Pref</dt>
            <dd class="text-surface-300">{{ session.settlementPref || 'Default' }}</dd>
          </div>
          <div>
            <dt class="text-surface-500">Idempotency Key</dt>
            <dd class="text-surface-400 font-mono text-xs">{{ session.idempotencyKey }}</dd>
          </div>
          <div>
            <dt class="text-surface-500">Expires At</dt>
            <dd class="text-surface-300">{{ formatDate(session.expiresAt) }}</dd>
          </div>
        </dl>
      </div>

      <!-- Timeline -->
      <div class="card p-6 animate-slide-up">
        <h2 class="text-sm font-medium text-surface-300 mb-3">Timeline</h2>
        <div class="space-y-3">
          <div class="flex items-center gap-3 text-sm">
            <span class="w-2 h-2 rounded-full bg-surface-500"></span>
            <span class="text-surface-400 w-32">Created</span>
            <span class="text-surface-300">{{ formatDate(session.createdAt) }}</span>
          </div>
          <div v-if="session.detectedAt" class="flex items-center gap-3 text-sm">
            <span class="w-2 h-2 rounded-full bg-blue-500"></span>
            <span class="text-surface-400 w-32">Detected</span>
            <span class="text-surface-300">{{ formatDate(session.detectedAt) }}</span>
          </div>
          <div v-if="session.confirmedAt" class="flex items-center gap-3 text-sm">
            <span class="w-2 h-2 rounded-full bg-blue-400"></span>
            <span class="text-surface-400 w-32">Confirmed</span>
            <span class="text-surface-300">{{ formatDate(session.confirmedAt) }}</span>
          </div>
          <div v-if="session.creditedAt" class="flex items-center gap-3 text-sm">
            <span class="w-2 h-2 rounded-full bg-emerald-500"></span>
            <span class="text-surface-400 w-32">Credited</span>
            <span class="text-surface-300">{{ formatDate(session.creditedAt) }}</span>
          </div>
          <div v-if="session.settledAt" class="flex items-center gap-3 text-sm">
            <span class="w-2 h-2 rounded-full bg-emerald-400"></span>
            <span class="text-surface-400 w-32">Settled</span>
            <span class="text-surface-300">{{ formatDate(session.settledAt) }}</span>
          </div>
          <div v-if="session.expiredAt" class="flex items-center gap-3 text-sm">
            <span class="w-2 h-2 rounded-full bg-surface-400"></span>
            <span class="text-surface-400 w-32">Expired</span>
            <span class="text-surface-300">{{ formatDate(session.expiredAt) }}</span>
          </div>
          <div v-if="session.failedAt" class="flex items-center gap-3 text-sm">
            <span class="w-2 h-2 rounded-full bg-red-500"></span>
            <span class="text-surface-400 w-32">Failed</span>
            <span class="text-surface-300">{{ formatDate(session.failedAt) }}</span>
            <span v-if="session.failureReason" class="text-red-400 text-xs">{{ session.failureReason }}</span>
          </div>
        </div>
      </div>
    </template>

    <EmptyState v-else title="Session not found" icon="search" />
  </div>
</template>

<script setup lang="ts">
import type { DepositSession, DepositSessionStatus } from '~/types'

const route = useRoute()
const api = usePortalApi()
const money = useMoney()

const session = ref<DepositSession | null>(null)
const loading = ref(true)
const cancelling = ref(false)
const copied = ref(false)

function statusClass(status: DepositSessionStatus) {
  const map: Record<string, string> = {
    PENDING_PAYMENT: 'bg-yellow-900/40 text-yellow-400',
    DETECTED: 'bg-blue-900/40 text-blue-400',
    CONFIRMED: 'bg-blue-900/40 text-blue-300',
    CREDITED: 'bg-emerald-900/40 text-emerald-400',
    SETTLED: 'bg-emerald-900/40 text-emerald-300',
    HELD: 'bg-purple-900/40 text-purple-400',
    EXPIRED: 'bg-surface-800 text-surface-400',
    FAILED: 'bg-red-900/40 text-red-400',
    CANCELLED: 'bg-surface-800 text-surface-500',
  }
  return map[status] ?? 'bg-surface-800 text-surface-400'
}

function formatDate(iso: string) {
  return new Date(iso).toLocaleString('en-GB', {
    day: '2-digit', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit',
  })
}

async function copyAddr() {
  if (!session.value?.depositAddress) return
  await navigator.clipboard.writeText(session.value.depositAddress)
  copied.value = true
  setTimeout(() => { copied.value = false }, 2000)
}

async function handleCancel() {
  if (!session.value) return
  cancelling.value = true
  try {
    const res = await api.cancelDeposit(session.value.id)
    session.value = res.session
  } catch (e: any) {
    alert(e.data?.message ?? e.message ?? 'Failed to cancel')
  } finally {
    cancelling.value = false
  }
}

onMounted(async () => {
  try {
    const res = await api.getDeposit(route.params.id as string)
    session.value = res.session
  } catch {
    session.value = null
  } finally {
    loading.value = false
  }
})
</script>
