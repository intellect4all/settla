<template>
  <div class="space-y-6">
    <div class="flex items-center justify-between">
      <div>
        <h1 class="text-xl font-semibold text-surface-100">Deposits</h1>
        <p class="text-sm text-surface-500 mt-1">Crypto deposit sessions for your tenant</p>
      </div>
      <AppButton @click="showCreate = true">
        New Deposit
      </AppButton>
    </div>

    <SkeletonLoader v-if="store.loading" variant="table" :lines="5" />

    <template v-else-if="store.sessions.length">
      <div class="card overflow-hidden animate-fade-in">
        <table class="w-full text-sm">
          <thead>
            <tr class="border-b border-surface-800 text-surface-500 text-left">
              <th class="px-4 py-3 font-medium">Status</th>
              <th class="px-4 py-3 font-medium">Chain / Token</th>
              <th class="px-4 py-3 font-medium text-right">Expected</th>
              <th class="px-4 py-3 font-medium text-right">Received</th>
              <th class="px-4 py-3 font-medium">Address</th>
              <th class="px-4 py-3 font-medium">Created</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="s in store.sessions" :key="s.id"
              class="border-b border-surface-800/50 hover:bg-surface-800/30 cursor-pointer transition-colors"
              @click="navigateTo(`/deposits/${s.id}`)"
            >
              <td class="px-4 py-3">
                <StatusBadge :status="s.status" size="sm" />
              </td>
              <td class="px-4 py-3 text-surface-200">{{ s.chain }} / {{ s.token }}</td>
              <td class="px-4 py-3 text-right font-mono text-surface-200">{{ money.format(s.expectedAmount, s.currency) }}</td>
              <td class="px-4 py-3 text-right font-mono text-surface-300">{{ money.format(s.receivedAmount, s.currency) }}</td>
              <td class="px-4 py-3 font-mono text-xs text-surface-400">{{ truncAddr(s.depositAddress) }}</td>
              <td class="px-4 py-3 text-surface-400 text-xs">{{ formatDate(s.createdAt) }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- Pagination -->
      <div class="flex items-center justify-between text-sm text-surface-400">
        <span>Page {{ store.currentPage }} of {{ store.totalPages }} ({{ store.total }} total)</span>
        <div class="flex gap-2">
          <AppButton
            variant="secondary"
            size="sm"
            :disabled="!store.hasPrev"
            @click="store.prevPage()"
          >Prev</AppButton>
          <AppButton
            variant="secondary"
            size="sm"
            :disabled="!store.hasNext"
            @click="store.nextPage()"
          >Next</AppButton>
        </div>
      </div>
    </template>

    <EmptyState v-else title="No deposits" description="Create your first crypto deposit session" icon="link" />

    <!-- Create modal -->
    <Modal :open="showCreate" title="New Deposit Session" @close="showCreate = false">
      <div class="space-y-3">
        <div>
          <label class="text-xs text-surface-400 block mb-1">Chain</label>
          <select v-model="form.chain" class="input w-full text-sm">
            <option v-for="c in chainOptions" :key="c" :value="c">{{ c }}</option>
          </select>
        </div>
        <div>
          <label class="text-xs text-surface-400 block mb-1">Token</label>
          <select v-model="form.token" class="input w-full text-sm">
            <option v-for="t in tokenOptions" :key="t" :value="t">{{ t }}</option>
          </select>
        </div>
        <div>
          <label class="text-xs text-surface-400 block mb-1">Expected Amount</label>
          <input v-model="form.expected_amount" placeholder="100.00" class="input w-full text-sm" />
        </div>
        <div>
          <label class="text-xs text-surface-400 block mb-1">Settlement Preference</label>
          <select v-model="form.settlement_pref" class="input w-full text-sm">
            <option value="">Tenant default</option>
            <option value="AUTO_CONVERT">Auto Convert</option>
            <option value="HOLD">Hold</option>
          </select>
        </div>
      </div>
      <p v-if="createError" class="text-xs text-red-400 mt-3">{{ createError }}</p>
      <template #footer>
        <AppButton variant="ghost" @click="showCreate = false">Cancel</AppButton>
        <AppButton :loading="creating" @click="handleCreate">Create</AppButton>
      </template>
    </Modal>
  </div>
</template>

<script setup lang="ts">
const store = useDepositsStore()
const money = useMoney()
const api = usePortalApi()

const showCreate = ref(false)
const creating = ref(false)
const createError = ref('')
// Supported chains and tokens — defaults match the most common Settla corridor
const chainOptions = ['tron', 'ethereum', 'base'] as const
const tokenOptions = ['USDT', 'USDC'] as const

const form = reactive({
  chain: 'tron' as string,
  token: 'USDT' as string,
  expected_amount: '',
  settlement_pref: '',
})

function truncAddr(addr: string) {
  if (!addr || addr.length < 12) return addr ?? ''
  return addr.slice(0, 6) + '...' + addr.slice(-4)
}

function formatDate(iso: string) {
  return new Date(iso).toLocaleString('en-GB', {
    day: '2-digit', month: 'short', hour: '2-digit', minute: '2-digit',
  })
}

async function handleCreate() {
  creating.value = true
  createError.value = ''
  try {
    await api.createDeposit({
      chain: form.chain,
      token: form.token,
      expected_amount: form.expected_amount,
      settlement_pref: form.settlement_pref || undefined,
      idempotency_key: crypto.randomUUID(),
    })
    showCreate.value = false
    form.expected_amount = ''
    store.fetchSessions(0)
  } catch (e: any) {
    createError.value = e.data?.message ?? e.message ?? 'Failed to create deposit'
  } finally {
    creating.value = false
  }
}

onMounted(() => {
  store.fetchSessions()
})
</script>
