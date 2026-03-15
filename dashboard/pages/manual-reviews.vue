<template>
  <div class="space-y-6">
    <!-- Header -->
    <div class="flex items-center justify-between">
      <div>
        <h1 class="text-xl font-semibold text-surface-100">Manual Reviews</h1>
        <p class="text-sm text-surface-400 mt-0.5">Stuck transfers escalated for human review</p>
      </div>
      <button class="btn-secondary text-sm flex items-center gap-2" @click="refresh">
        <span>&#8635;</span> Refresh
      </button>
    </div>

    <!-- API key missing -->
    <AlertBanner
      v-if="api.apiKeyMissing"
      type="warning"
      title="API key not configured"
      description="Set NUXT_PUBLIC_DASHBOARD_API_KEY to connect to the live backend. Showing sample data."
      :dismissible="false"
    />

    <!-- Auth / fetch error -->
    <AlertBanner
      v-else-if="fetchError"
      type="error"
      :title="fetchError"
      description="Check your API key and gateway connectivity."
    />

    <!-- Sample data notice -->
    <AlertBanner
      v-else-if="usingSampleData"
      type="info"
      title="Showing sample data"
      description="The /v1/ops/manual-reviews endpoint is not yet available on the gateway. Live data will appear automatically once the endpoint is deployed."
    />

    <!-- Summary cards -->
    <div class="grid grid-cols-2 md:grid-cols-4 gap-4">
      <div class="card p-4">
        <p class="text-xs text-surface-500 uppercase tracking-wider">Pending</p>
        <p class="text-2xl font-semibold text-amber-400 mt-1">{{ counts.PENDING }}</p>
      </div>
      <div class="card p-4">
        <p class="text-xs text-surface-500 uppercase tracking-wider">Escalated</p>
        <p class="text-2xl font-semibold text-red-400 mt-1">{{ counts.ESCALATED }}</p>
      </div>
      <div class="card p-4">
        <p class="text-xs text-surface-500 uppercase tracking-wider">Approved today</p>
        <p class="text-2xl font-semibold text-emerald-400 mt-1">{{ counts.APPROVED }}</p>
      </div>
      <div class="card p-4">
        <p class="text-xs text-surface-500 uppercase tracking-wider">Rejected today</p>
        <p class="text-2xl font-semibold text-surface-400 mt-1">{{ counts.REJECTED }}</p>
      </div>
    </div>

    <!-- Filters -->
    <div class="flex items-center gap-3">
      <select v-model="statusFilter" class="input text-sm">
        <option value="">All statuses</option>
        <option value="PENDING">Pending</option>
        <option value="ESCALATED">Escalated</option>
        <option value="APPROVED">Approved</option>
        <option value="REJECTED">Rejected</option>
      </select>
      <input
        v-model="searchQuery"
        class="input text-sm w-64"
        placeholder="Search transfer ID or reason…"
      />
    </div>

    <!-- Loading -->
    <div v-if="loading" class="flex items-center justify-center py-16">
      <LoadingSpinner />
    </div>

    <!-- Empty state -->
    <EmptyState
      v-else-if="filtered.length === 0"
      icon="&#9749;"
      title="No reviews found"
      description="No transfers match the current filters."
    />

    <!-- Review list -->
    <div v-else class="space-y-3">
      <div
        v-for="review in filtered"
        :key="review.id"
        class="card p-5"
      >
        <div class="flex items-start justify-between gap-4">
          <!-- Left: transfer info -->
          <div class="flex-1 min-w-0 space-y-2">
            <div class="flex items-center gap-3 flex-wrap">
              <span
                class="text-xs font-semibold px-2 py-0.5 rounded-full"
                :class="reviewStatusClass(review.status)"
              >{{ review.status }}</span>
              <span class="font-mono text-xs text-surface-400">{{ review.transfer_id }}</span>
              <span class="text-xs text-surface-500">{{ review.tenant_name }}</span>
            </div>

            <div class="flex items-center gap-6 text-sm">
              <div>
                <span class="text-surface-500">Amount: </span>
                <span class="font-mono text-surface-200">
                  {{ format(review.source_amount, review.source_currency) }}
                  <span class="text-surface-500">→ {{ review.dest_currency }}</span>
                </span>
              </div>
              <div>
                <span class="text-surface-500">Escalated: </span>
                <span class="text-surface-300">{{ relativeTime(review.escalated_at) }}</span>
              </div>
              <div v-if="review.failure_code">
                <span class="text-surface-500">Code: </span>
                <span class="font-mono text-amber-400 text-xs">{{ review.failure_code }}</span>
              </div>
            </div>

            <p class="text-sm text-surface-400 leading-relaxed">{{ review.reason }}</p>

            <div v-if="review.notes" class="text-xs text-surface-500 italic">
              Note: {{ review.notes }}
            </div>
          </div>

          <!-- Right: actions (only for pending/escalated) -->
          <div v-if="review.status === 'PENDING' || review.status === 'ESCALATED'" class="flex flex-col gap-2 shrink-0">
            <button
              class="btn-primary text-xs px-3 py-1.5"
              :disabled="actionLoading === review.id"
              @click="openAction(review, 'approve')"
            >
              &#10003; Approve
            </button>
            <button
              class="btn-secondary text-xs px-3 py-1.5 text-red-400 hover:text-red-300"
              :disabled="actionLoading === review.id"
              @click="openAction(review, 'reject')"
            >
              &#10005; Reject
            </button>
            <NuxtLink
              :to="`/transfers/${review.transfer_id}`"
              class="btn-secondary text-xs px-3 py-1.5 text-center"
            >
              &#8599; View
            </NuxtLink>
          </div>

          <!-- Resolved badge -->
          <div v-else class="text-xs text-surface-500 shrink-0 text-right space-y-1">
            <div>{{ review.reviewed_by ?? 'ops' }}</div>
            <div>{{ review.reviewed_at ? relativeTime(review.reviewed_at) : '' }}</div>
          </div>
        </div>
      </div>
    </div>

    <!-- Action modal -->
    <div
      v-if="actionModal.open"
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/60"
      @click.self="closeModal"
    >
      <div class="card w-full max-w-md p-6 space-y-4">
        <h2 class="text-base font-semibold text-surface-100">
          {{ actionModal.type === 'approve' ? '&#10003; Approve' : '&#10005; Reject' }} review
        </h2>
        <p class="text-sm text-surface-400">
          Transfer <span class="font-mono text-surface-300">{{ actionModal.review?.transfer_id }}</span>
        </p>
        <div>
          <label class="block text-xs text-surface-500 mb-1">Notes (optional)</label>
          <textarea
            v-model="actionModal.notes"
            class="input w-full text-sm resize-none"
            rows="3"
            placeholder="Add context for the audit trail…"
          />
        </div>
        <div class="flex gap-3 justify-end">
          <button class="btn-secondary text-sm" @click="closeModal">Cancel</button>
          <button
            class="btn-primary text-sm"
            :class="actionModal.type === 'reject' ? 'bg-red-600 hover:bg-red-500' : ''"
            :disabled="actionLoading !== null"
            @click="submitAction"
          >
            <span v-if="actionLoading">Processing…</span>
            <span v-else>{{ actionModal.type === 'approve' ? 'Approve' : 'Reject' }}</span>
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import type { ManualReview, ReviewStatus } from '~/types'

const api = useApi()
const { format } = useMoney()
const toast = useToast()

const reviews = ref<ManualReview[]>([])
const loading = ref(true)
const fetchError = ref<string | null>(null)
const usingSampleData = ref(false)
const statusFilter = ref('')
const searchQuery = ref('')
const actionLoading = ref<string | null>(null)

const actionModal = ref<{
  open: boolean
  type: 'approve' | 'reject'
  review: ManualReview | null
  notes: string
}>({ open: false, type: 'approve', review: null, notes: '' })

// ── Data fetching ──────────────────────────────────────────────────────────

async function loadData() {
  loading.value = true
  fetchError.value = null
  usingSampleData.value = false
  try {
    const result = await api.listManualReviews(statusFilter.value || undefined)
    reviews.value = result.reviews
  } catch (err: any) {
    const status = err?.response?.status ?? err?.statusCode
    if (status === 404) {
      // Endpoint not yet implemented in gateway — use sample data silently
      reviews.value = generateSampleReviews()
      usingSampleData.value = true
    } else if (status === 401 || status === 403) {
      fetchError.value = 'Authentication failed — check your API key.'
      reviews.value = []
    } else if (err?.message?.includes('API key not configured')) {
      // Already shown via apiKeyMissing banner — fall back to sample data
      reviews.value = generateSampleReviews()
      usingSampleData.value = true
    } else {
      // Network error or 5xx — show sample data with a warning
      reviews.value = generateSampleReviews()
      usingSampleData.value = true
    }
  } finally {
    loading.value = false
  }
}

function refresh() {
  loadData()
}

// ── Computed ───────────────────────────────────────────────────────────────

const filtered = computed(() => {
  let rows = reviews.value
  if (statusFilter.value) {
    rows = rows.filter(r => r.status === statusFilter.value)
  }
  if (searchQuery.value) {
    const q = searchQuery.value.toLowerCase()
    rows = rows.filter(r =>
      r.transfer_id.toLowerCase().includes(q) ||
      r.reason.toLowerCase().includes(q) ||
      (r.failure_code ?? '').toLowerCase().includes(q),
    )
  }
  return rows
})

const counts = computed(() => {
  const all = reviews.value
  return {
    PENDING: all.filter(r => r.status === 'PENDING').length,
    ESCALATED: all.filter(r => r.status === 'ESCALATED').length,
    APPROVED: all.filter(r => r.status === 'APPROVED').length,
    REJECTED: all.filter(r => r.status === 'REJECTED').length,
  }
})

// ── Actions ────────────────────────────────────────────────────────────────

function openAction(review: ManualReview, type: 'approve' | 'reject') {
  actionModal.value = { open: true, type, review, notes: '' }
}

function closeModal() {
  actionModal.value = { open: false, type: 'approve', review: null, notes: '' }
}

async function submitAction() {
  const { review, type, notes } = actionModal.value
  if (!review) return
  actionLoading.value = review.id
  try {
    if (type === 'approve') {
      await api.approveReview(review.id, notes)
      toast.success(`Review approved`)
    } else {
      await api.rejectReview(review.id, notes)
      toast.success(`Review rejected`)
    }
    closeModal()
    await loadData()
  } catch (err: any) {
    const status = err?.response?.status ?? err?.statusCode
    if (status === 404) {
      // Endpoint not yet implemented — optimistically update local state
      const r = reviews.value.find(r => r.id === review.id)
      if (r) r.status = type === 'approve' ? 'APPROVED' : 'REJECTED'
      toast.success(`Review ${type}d (local only — ops endpoint pending)`)
      closeModal()
    } else if (status === 401 || status === 403) {
      toast.error('Authentication failed — check your API key.')
    } else {
      toast.error(`Failed to ${type} review`)
    }
  } finally {
    actionLoading.value = null
  }
}

// ── Helpers ────────────────────────────────────────────────────────────────

function reviewStatusClass(status: ReviewStatus) {
  switch (status) {
    case 'PENDING': return 'bg-amber-500/10 text-amber-400'
    case 'ESCALATED': return 'bg-red-500/10 text-red-400'
    case 'APPROVED': return 'bg-emerald-500/10 text-emerald-400'
    case 'REJECTED': return 'bg-surface-700 text-surface-400'
  }
}

function relativeTime(iso: string) {
  const diff = Date.now() - new Date(iso).getTime()
  const mins = Math.floor(diff / 60_000)
  if (mins < 1) return 'just now'
  if (mins < 60) return `${mins}m ago`
  const hrs = Math.floor(mins / 60)
  if (hrs < 24) return `${hrs}h ago`
  return `${Math.floor(hrs / 24)}d ago`
}

// ── Sample data ────────────────────────────────────────────────────────────

function generateSampleReviews(): ManualReview[] {
  return [
    {
      id: 'rev_001',
      transfer_id: 'a1b2c3d4-0001-0000-0000-000000000001',
      tenant_id: 'a0000000-0000-0000-0000-000000000001',
      tenant_name: 'Lemfi',
      status: 'ESCALATED',
      reason: 'Transfer stuck in ON_RAMPING for 47 minutes. Provider returned HTTP 504 on all 5 retry attempts. Stuck detector escalated after 30-minute threshold.',
      failure_code: 'PROVIDER_TIMEOUT',
      source_amount: '5000.00',
      source_currency: 'GBP',
      dest_currency: 'NGN',
      escalated_at: new Date(Date.now() - 47 * 60_000).toISOString(),
    },
    {
      id: 'rev_002',
      transfer_id: 'b1b2c3d4-0002-0000-0000-000000000002',
      tenant_id: 'b0000000-0000-0000-0000-000000000002',
      tenant_name: 'Fincra',
      status: 'PENDING',
      reason: 'Transfer reached FAILED state but compensation flow aborted: treasury release succeeded but ledger reversal returned conflict error. Requires manual verification of ledger balance.',
      failure_code: 'COMPENSATION_PARTIAL',
      source_amount: '800000.00',
      source_currency: 'NGN',
      dest_currency: 'GBP',
      escalated_at: new Date(Date.now() - 2 * 3600_000).toISOString(),
    },
    {
      id: 'rev_003',
      transfer_id: 'c1b2c3d4-0003-0000-0000-000000000003',
      tenant_id: 'a0000000-0000-0000-0000-000000000001',
      tenant_name: 'Lemfi',
      status: 'PENDING',
      reason: 'Blockchain worker received ambiguous status from Tron RPC: transaction hash found but confirmations count oscillating between 0 and 18. Possible chain re-org.',
      failure_code: 'CHAIN_REORG_SUSPECTED',
      source_amount: '12500.00',
      source_currency: 'GBP',
      dest_currency: 'NGN',
      escalated_at: new Date(Date.now() - 25 * 60_000).toISOString(),
    },
    {
      id: 'rev_004',
      transfer_id: 'd1b2c3d4-0004-0000-0000-000000000004',
      tenant_id: 'b0000000-0000-0000-0000-000000000002',
      tenant_name: 'Fincra',
      status: 'APPROVED',
      reason: 'Provider webhook received with mismatched amount (sent 415.00 USDT, confirmed 414.85 USDT). Delta within 0.05% tolerance — approved as rounding from provider.',
      failure_code: 'AMOUNT_MISMATCH_MINOR',
      source_amount: '640000.00',
      source_currency: 'NGN',
      dest_currency: 'GBP',
      escalated_at: new Date(Date.now() - 5 * 3600_000).toISOString(),
      reviewed_at: new Date(Date.now() - 4 * 3600_000).toISOString(),
      reviewed_by: 'ops@settla.io',
      notes: 'Delta is 0.036% — within acceptable rounding tolerance. Approved.',
    },
    {
      id: 'rev_005',
      transfer_id: 'e1b2c3d4-0005-0000-0000-000000000005',
      tenant_id: 'a0000000-0000-0000-0000-000000000001',
      tenant_name: 'Lemfi',
      status: 'REJECTED',
      reason: 'Transfer flagged by compliance: recipient bank account appears on AML watchlist (OFAC match confidence 78%). Manual review required per policy.',
      failure_code: 'AML_WATCHLIST_HIT',
      source_amount: '95000.00',
      source_currency: 'GBP',
      dest_currency: 'NGN',
      escalated_at: new Date(Date.now() - 8 * 3600_000).toISOString(),
      reviewed_at: new Date(Date.now() - 7 * 3600_000).toISOString(),
      reviewed_by: 'compliance@settla.io',
      notes: 'OFAC match confirmed. Transfer rejected and flagged for SAR filing.',
    },
  ]
}

// ── Init ───────────────────────────────────────────────────────────────────

onMounted(loadData)
watch(statusFilter, loadData)
</script>
