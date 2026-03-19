<template>
  <div class="space-y-6">
    <!-- Header -->
    <div class="flex items-center justify-between animate-fade-in">
      <div>
        <h1 class="text-xl font-semibold text-surface-100">Manual Reviews</h1>
        <p class="text-sm text-surface-400 mt-0.5">Stuck transfers escalated for human review</p>
      </div>
      <AppButton variant="secondary" size="sm" icon="refresh-cw" @click="refresh">
        Refresh
      </AppButton>
    </div>

    <!-- Auth / fetch error -->
    <AlertBanner
      v-if="fetchError"
      type="error"
      :title="fetchError"
      description="Check your API key and gateway connectivity."
    />

    <!-- Summary cards -->
    <div class="grid grid-cols-2 md:grid-cols-4 gap-4 animate-fade-in">
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
    <template v-if="loading">
      <div class="space-y-3">
        <SkeletonLoader v-for="i in 3" :key="i" variant="card" height="140px" />
      </div>
    </template>

    <!-- Empty state -->
    <EmptyState
      v-else-if="filtered.length === 0"
      icon="inbox"
      title="No reviews found"
      description="No transfers match the current filters."
    />

    <!-- Review list -->
    <div v-else class="space-y-3 animate-fade-in">
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
                  <Icon name="chevron-right" :size="12" class="inline-block text-surface-500 mx-0.5" /><span class="text-surface-500">{{ review.dest_currency }}</span>
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
            <AppButton
              size="sm"
              icon="check"
              :disabled="actionLoading === review.id"
              @click="openAction(review, 'approve')"
            >
              Approve
            </AppButton>
            <AppButton
              variant="danger"
              size="sm"
              icon="x"
              :disabled="actionLoading === review.id"
              @click="openAction(review, 'reject')"
            >
              Reject
            </AppButton>
            <NuxtLink
              :to="`/transfers/${review.transfer_id}`"
              class="btn-secondary text-xs px-3 py-1.5 text-center focus-ring"
            >
              <Icon name="external-link" :size="12" class="inline-block mr-1" /> View
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
    <Modal
      :open="actionModal.open"
      :title="actionModal.type === 'approve' ? 'Approve review' : 'Reject review'"
      size="sm"
      @close="closeModal"
    >
      <p class="text-sm text-surface-400">
        Transfer <span class="font-mono text-surface-300">{{ actionModal.review?.transfer_id }}</span>
      </p>
      <div class="mt-4">
        <label class="block text-xs text-surface-500 mb-1">Notes (optional)</label>
        <textarea
          v-model="actionModal.notes"
          class="input w-full text-sm resize-none"
          rows="3"
          placeholder="Add context for the audit trail..."
        />
      </div>
      <template #footer>
        <AppButton variant="secondary" size="sm" @click="closeModal">Cancel</AppButton>
        <AppButton
          :variant="actionModal.type === 'reject' ? 'danger' : 'primary'"
          size="sm"
          :loading="actionLoading !== null"
          @click="submitAction"
        >
          {{ actionModal.type === 'approve' ? 'Approve' : 'Reject' }}
        </AppButton>
      </template>
    </Modal>
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
  try {
    const result = await api.listManualReviews(statusFilter.value || undefined)
    reviews.value = result.reviews
  } catch (err: any) {
    const status = err?.response?.status ?? err?.statusCode
    if (status === 401 || status === 403) {
      fetchError.value = 'Authentication failed — check your API key.'
    } else {
      fetchError.value = `Failed to load manual reviews: ${err?.message || 'unknown error'}`
    }
    reviews.value = []
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

// ── Init ───────────────────────────────────────────────────────────────────

onMounted(loadData)
watch(statusFilter, loadData)
</script>
