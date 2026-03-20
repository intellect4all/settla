<template>
  <div>
    <div class="flex items-center justify-between mb-6">
      <div>
        <h1 class="text-2xl font-bold text-surface-100">Payment Links</h1>
        <p class="text-surface-500 text-sm mt-1">Create shareable payment links for your customers</p>
      </div>
      <AppButton @click="showCreateModal = true">
        Create Link
      </AppButton>
    </div>

    <!-- Loading -->
    <SkeletonLoader v-if="store.loading" variant="table" :lines="4" />

    <!-- Error -->
    <div v-else-if="store.error" class="text-center py-12">
      <p class="text-red-400">{{ store.error }}</p>
      <AppButton variant="ghost" size="sm" class="mt-2" @click="store.fetchLinks()">
        Retry
      </AppButton>
    </div>

    <!-- Empty -->
    <EmptyState v-else-if="store.links.length === 0" title="No payment links yet" description="Create your first payment link to start accepting payments" icon="link" />

    <!-- Links Table -->
    <div v-else class="card overflow-hidden animate-fade-in">
      <table class="w-full">
        <thead>
          <tr class="border-b border-surface-800">
            <th class="text-left text-xs font-medium text-surface-500 px-4 py-3 uppercase tracking-wide">Description</th>
            <th class="text-left text-xs font-medium text-surface-500 px-4 py-3 uppercase tracking-wide">Amount</th>
            <th class="text-left text-xs font-medium text-surface-500 px-4 py-3 uppercase tracking-wide">Status</th>
            <th class="text-left text-xs font-medium text-surface-500 px-4 py-3 uppercase tracking-wide">Uses</th>
            <th class="text-left text-xs font-medium text-surface-500 px-4 py-3 uppercase tracking-wide">Created</th>
            <th class="text-right text-xs font-medium text-surface-500 px-4 py-3 uppercase tracking-wide">Actions</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="link in store.links"
            :key="link.id"
            class="border-b border-surface-800 last:border-0 hover:bg-surface-800/50 transition-colors cursor-pointer"
            @click="navigateTo(`/payment-links/${link.id}`)"
          >
            <td class="px-4 py-3">
              <p class="text-surface-200 text-sm">{{ link.description || 'No description' }}</p>
              <p class="text-surface-600 text-xs font-mono mt-0.5">{{ link.shortCode }}</p>
            </td>
            <td class="px-4 py-3">
              <p class="text-surface-200 text-sm">
                {{ link.sessionConfig?.amount }} {{ link.sessionConfig?.token }}
              </p>
              <p class="text-surface-600 text-xs">{{ link.sessionConfig?.chain }}</p>
            </td>
            <td class="px-4 py-3">
              <span
                :class="statusClass(link.status)"
                class="inline-block px-2 py-0.5 rounded text-xs font-medium"
              >
                {{ link.status }}
              </span>
            </td>
            <td class="px-4 py-3 text-surface-300 text-sm">
              {{ link.useCount }}{{ link.hasUseLimit ? ` / ${link.useLimit}` : '' }}
            </td>
            <td class="px-4 py-3 text-surface-400 text-sm">
              {{ formatDate(link.createdAt) }}
            </td>
            <td class="px-4 py-3 text-right">
              <div class="flex items-center justify-end gap-2" @click.stop>
                <AppButton
                  variant="ghost"
                  size="sm"
                  @click="copyUrl(link)"
                >
                  {{ copiedId === link.id ? 'Copied!' : 'Copy URL' }}
                </AppButton>
                <AppButton
                  v-if="link.status === 'ACTIVE'"
                  variant="danger"
                  size="sm"
                  @click="disableLink(link.id)"
                >
                  Disable
                </AppButton>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Pagination -->
    <div v-if="store.total > store.pageSize" class="flex justify-between items-center mt-4">
      <p class="text-surface-500 text-sm">
        Showing {{ store.currentPage + 1 }}-{{ Math.min(store.currentPage + store.pageSize, store.total) }}
        of {{ store.total }}
      </p>
      <div class="flex gap-2">
        <AppButton
          variant="secondary"
          size="sm"
          :disabled="store.currentPage === 0"
          @click="store.fetchLinks(store.currentPage - store.pageSize)"
        >
          Previous
        </AppButton>
        <AppButton
          variant="secondary"
          size="sm"
          :disabled="store.currentPage + store.pageSize >= store.total"
          @click="store.fetchLinks(store.currentPage + store.pageSize)"
        >
          Next
        </AppButton>
      </div>
    </div>

    <!-- Create Modal -->
    <Modal :open="showCreateModal" title="Create Payment Link" @close="showCreateModal = false">
      <form @submit.prevent="handleCreate" class="space-y-4">
        <div>
          <label class="block text-sm text-surface-400 mb-1">Amount *</label>
          <input v-model="form.amount" type="text" placeholder="100.00"
            class="input w-full text-sm" />
        </div>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <label class="block text-sm text-surface-400 mb-1">Token *</label>
            <select v-model="form.token" class="input w-full text-sm">
              <option v-for="t in tokenOptions" :key="t" :value="t">{{ t }}</option>
            </select>
          </div>
          <div>
            <label class="block text-sm text-surface-400 mb-1">Chain *</label>
            <select v-model="form.chain" class="input w-full text-sm">
              <option v-for="c in chainOptions" :key="c" :value="c">{{ c }}</option>
            </select>
          </div>
        </div>
        <div>
          <label class="block text-sm text-surface-400 mb-1">Description</label>
          <input v-model="form.description" type="text" placeholder="Payment for invoice #123"
            class="input w-full text-sm" />
        </div>
        <div>
          <label class="block text-sm text-surface-400 mb-1">Redirect URL</label>
          <input v-model="form.redirect_url" type="text" placeholder="https://..."
            class="input w-full text-sm" />
        </div>
        <div>
          <label class="block text-sm text-surface-400 mb-1">Use Limit (blank = unlimited)</label>
          <input v-model.number="form.use_limit" type="number" min="1"
            class="input w-full text-sm" />
        </div>

        <div v-if="createError" class="text-red-400 text-sm">{{ createError }}</div>
      </form>
      <template #footer>
        <AppButton variant="ghost" @click="showCreateModal = false">Cancel</AppButton>
        <AppButton :loading="creating" @click="handleCreate">Create</AppButton>
      </template>
    </Modal>
  </div>
</template>

<script setup lang="ts">
import { usePaymentLinksStore } from '~/stores/paymentLinks'

const store = usePaymentLinksStore()
const showCreateModal = ref(false)
const creating = ref(false)
const createError = ref<string | null>(null)
const copiedId = ref<string | null>(null)

// Supported chains and tokens — defaults match the most common Settla corridor
const chainOptions = ['tron', 'ethereum', 'base'] as const
const tokenOptions = ['USDT', 'USDC'] as const

const form = reactive({
  amount: '',
  token: 'USDT' as string,
  chain: 'tron' as string,
  currency: 'USDT',
  description: '',
  redirect_url: '',
  use_limit: undefined as number | undefined,
})

onMounted(() => {
  store.fetchLinks()
})

function statusClass(status: string) {
  switch (status) {
    case 'ACTIVE': return 'bg-green-900/30 text-green-400'
    case 'DISABLED': return 'bg-red-900/30 text-red-400'
    case 'EXPIRED': return 'bg-yellow-900/30 text-yellow-400'
    default: return 'bg-surface-800 text-surface-400'
  }
}

function formatDate(dateStr: string) {
  if (!dateStr) return ''
  return new Date(dateStr).toLocaleDateString('en-US', {
    month: 'short', day: 'numeric', year: 'numeric',
  })
}

function copyUrl(link: any) {
  if (link.url) {
    navigator.clipboard.writeText(link.url)
    copiedId.value = link.id
    setTimeout(() => { copiedId.value = null }, 2000)
  }
}

async function disableLink(linkId: string) {
  if (!confirm('Are you sure you want to disable this payment link?')) return
  try {
    await store.disableLink(linkId)
  } catch (e: any) {
    alert(e?.data?.message ?? 'Failed to disable link')
  }
}

async function handleCreate() {
  createError.value = null
  if (!form.amount || !form.chain || !form.token) {
    createError.value = 'Amount, chain, and token are required'
    return
  }

  creating.value = true
  try {
    await store.createLink({
      amount: form.amount,
      currency: form.currency,
      chain: form.chain,
      token: form.token,
      description: form.description || undefined,
      redirect_url: form.redirect_url || undefined,
      use_limit: form.use_limit || undefined,
    })
    showCreateModal.value = false
    // Reset form
    form.amount = ''
    form.description = ''
    form.redirect_url = ''
    form.use_limit = undefined
  } catch (e: any) {
    createError.value = e?.data?.message ?? e?.message ?? 'Failed to create payment link'
  } finally {
    creating.value = false
  }
}
</script>
