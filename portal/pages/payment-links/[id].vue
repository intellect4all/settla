<template>
  <div class="space-y-6">
    <div class="flex items-center gap-3">
      <NuxtLink to="/payment-links" class="text-surface-400 hover:text-surface-200 transition-colors">&larr; Back</NuxtLink>
      <h1 class="text-xl font-semibold text-surface-100">Payment Link</h1>
    </div>

    <div v-if="loading" class="space-y-4">
      <SkeletonLoader variant="card" height="160px" />
      <SkeletonLoader variant="card" height="80px" />
      <SkeletonLoader variant="card" height="120px" />
    </div>

    <template v-else-if="link">
      <!-- Header card -->
      <div class="card p-6 animate-fade-in">
        <div class="flex items-center justify-between mb-4">
          <div>
            <span
              class="text-xs font-medium px-2 py-0.5 rounded"
              :class="statusClass(link.status)"
            >{{ link.status }}</span>
            <p class="text-sm text-surface-200 mt-2">{{ link.description || 'No description' }}</p>
            <p class="text-xs text-surface-500 mt-1 font-mono">{{ link.id }}</p>
          </div>
          <AppButton
            v-if="link.status === 'ACTIVE'"
            variant="danger"
            :loading="disabling"
            @click="handleDisable"
          >Disable Link</AppButton>
        </div>

        <div class="grid grid-cols-2 lg:grid-cols-4 gap-4">
          <div>
            <dt class="text-xs text-surface-500">Amount</dt>
            <dd class="text-sm text-surface-200 mt-1 font-mono">{{ link.sessionConfig?.amount }} {{ link.sessionConfig?.currency }}</dd>
          </div>
          <div>
            <dt class="text-xs text-surface-500">Chain / Token</dt>
            <dd class="text-sm text-surface-200 mt-1">{{ link.sessionConfig?.chain }} / {{ link.sessionConfig?.token }}</dd>
          </div>
          <div>
            <dt class="text-xs text-surface-500">Redemptions</dt>
            <dd class="text-sm text-surface-200 mt-1">{{ link.useCount }}{{ link.hasUseLimit ? ` / ${link.useLimit}` : '' }}</dd>
          </div>
          <div>
            <dt class="text-xs text-surface-500">Short Code</dt>
            <dd class="text-sm text-surface-200 mt-1 font-mono">{{ link.shortCode }}</dd>
          </div>
        </div>
      </div>

      <!-- Payment URL -->
      <div class="card p-6 animate-slide-up">
        <h2 class="text-sm font-medium text-surface-300 mb-3">Payment URL</h2>
        <div class="flex items-center gap-3">
          <code class="text-sm text-surface-100 bg-surface-800 px-3 py-2 rounded font-mono flex-1 break-all">
            {{ link.url }}
          </code>
          <button
            class="text-xs text-surface-400 hover:text-surface-200 bg-surface-800 px-3 py-2 rounded"
            @click="copyUrl"
          >{{ copied ? 'Copied' : 'Copy' }}</button>
        </div>
        <!-- QR Code placeholder -->
        <div class="mt-4 flex items-center gap-3 p-3 bg-surface-800 rounded">
          <div class="w-[120px] h-[120px] bg-surface-700 rounded flex items-center justify-center text-surface-500 text-xs text-center leading-tight">
            QR Code<br />Coming Soon
          </div>
          <p class="text-xs text-surface-500">
            Share this URL or QR code with your customers to accept payments.
          </p>
        </div>
      </div>

      <!-- Session Configuration -->
      <div class="card p-6 animate-slide-up">
        <h2 class="text-sm font-medium text-surface-300 mb-3">Session Configuration</h2>
        <dl class="grid grid-cols-2 gap-x-6 gap-y-3 text-sm">
          <div>
            <dt class="text-surface-500">Settlement Preference</dt>
            <dd class="text-surface-300">{{ link.sessionConfig?.settlement_pref || 'Default' }}</dd>
          </div>
          <div>
            <dt class="text-surface-500">Session TTL</dt>
            <dd class="text-surface-300">{{ link.sessionConfig?.ttl_seconds ? `${link.sessionConfig.ttl_seconds}s` : 'Default' }}</dd>
          </div>
          <div>
            <dt class="text-surface-500">Redirect URL</dt>
            <dd class="text-surface-400 font-mono text-xs break-all">{{ link.redirectUrl || 'None' }}</dd>
          </div>
          <div>
            <dt class="text-surface-500">Use Limit</dt>
            <dd class="text-surface-300">{{ link.hasUseLimit ? link.useLimit : 'Unlimited' }}</dd>
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
            <span class="text-surface-300">{{ formatDate(link.createdAt) }}</span>
          </div>
          <div v-if="link.updatedAt && link.updatedAt !== link.createdAt" class="flex items-center gap-3 text-sm">
            <span class="w-2 h-2 rounded-full bg-blue-500"></span>
            <span class="text-surface-400 w-32">Updated</span>
            <span class="text-surface-300">{{ formatDate(link.updatedAt) }}</span>
          </div>
          <div v-if="link.expiresAt" class="flex items-center gap-3 text-sm">
            <span class="w-2 h-2 rounded-full" :class="isExpired ? 'bg-surface-400' : 'bg-yellow-500'"></span>
            <span class="text-surface-400 w-32">{{ isExpired ? 'Expired' : 'Expires' }}</span>
            <span class="text-surface-300">{{ formatDate(link.expiresAt) }}</span>
          </div>
          <div v-if="link.status === 'DISABLED'" class="flex items-center gap-3 text-sm">
            <span class="w-2 h-2 rounded-full bg-red-500"></span>
            <span class="text-surface-400 w-32">Disabled</span>
            <span class="text-surface-300">{{ formatDate(link.updatedAt) }}</span>
          </div>
        </div>
      </div>
    </template>

    <EmptyState v-else title="Payment link not found" icon="search" />
  </div>
</template>

<script setup lang="ts">
import type { PaymentLink } from '~/types'

const route = useRoute()
const api = usePortalApi()

const link = ref<PaymentLink | null>(null)
const loading = ref(true)
const disabling = ref(false)
const copied = ref(false)

const isExpired = computed(() => {
  if (!link.value?.expiresAt) return false
  return new Date(link.value.expiresAt) < new Date()
})

function statusClass(status: string) {
  switch (status) {
    case 'ACTIVE': return 'bg-green-900/30 text-green-400'
    case 'DISABLED': return 'bg-red-900/30 text-red-400'
    case 'EXPIRED': return 'bg-yellow-900/30 text-yellow-400'
    default: return 'bg-surface-800 text-surface-400'
  }
}

function formatDate(iso: string) {
  if (!iso) return ''
  return new Date(iso).toLocaleString('en-GB', {
    day: '2-digit', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit',
  })
}

async function copyUrl() {
  if (!link.value?.url) return
  await navigator.clipboard.writeText(link.value.url)
  copied.value = true
  setTimeout(() => { copied.value = false }, 2000)
}

async function handleDisable() {
  if (!link.value) return
  if (!confirm('Are you sure you want to disable this payment link?')) return
  disabling.value = true
  try {
    await api.disablePaymentLink(link.value.id)
    // Re-fetch to get updated status
    const res = await api.getPaymentLink(link.value.id)
    link.value = res.link
  } catch (e: any) {
    alert(e?.data?.message ?? e?.message ?? 'Failed to disable link')
  } finally {
    disabling.value = false
  }
}

onMounted(async () => {
  try {
    const res = await api.getPaymentLink(route.params.id as string)
    link.value = res.link
  } catch {
    link.value = null
  } finally {
    loading.value = false
  }
})
</script>
