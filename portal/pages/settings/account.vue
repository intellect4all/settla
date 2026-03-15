<template>
  <div class="space-y-6">
    <div>
      <h1 class="text-xl font-semibold text-surface-100">Account</h1>
      <p class="text-sm text-surface-500 mt-1">Your tenant profile and limits</p>
    </div>

    <LoadingSpinner v-if="authStore.loading" size="lg" full-page />

    <template v-else-if="tenant">
      <!-- Profile -->
      <div class="bg-surface-900 rounded-lg border border-surface-800 p-5">
        <h2 class="text-sm font-medium text-surface-400 mb-4">Tenant Profile</h2>
        <dl class="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <div>
            <dt class="text-xs text-surface-500">Name</dt>
            <dd class="text-sm text-surface-200 mt-0.5">{{ tenant.name }}</dd>
          </div>
          <div>
            <dt class="text-xs text-surface-500">Slug</dt>
            <dd class="text-sm text-surface-300 font-mono mt-0.5">{{ tenant.slug }}</dd>
          </div>
          <div>
            <dt class="text-xs text-surface-500">Status</dt>
            <dd class="mt-0.5"><StatusBadge :status="tenant.status" size="sm" /></dd>
          </div>
          <div>
            <dt class="text-xs text-surface-500">Settlement Model</dt>
            <dd class="text-sm text-surface-300 mt-0.5">{{ tenant.settlement_model }}</dd>
          </div>
          <div>
            <dt class="text-xs text-surface-500">Tenant ID</dt>
            <dd class="text-xs text-surface-400 font-mono mt-0.5">{{ tenant.id }}</dd>
          </div>
          <div>
            <dt class="text-xs text-surface-500">Joined</dt>
            <dd class="text-sm text-surface-300 mt-0.5">{{ formatDate(tenant.created_at) }}</dd>
          </div>
        </dl>
      </div>

      <!-- KYB Status -->
      <div class="bg-surface-900 rounded-lg border border-surface-800 p-5">
        <h2 class="text-sm font-medium text-surface-400 mb-4">KYB Verification</h2>
        <div class="flex items-center gap-3">
          <StatusBadge :status="tenant.kyb_status" size="sm" />
          <span v-if="tenant.kyb_verified_at" class="text-xs text-surface-500">
            Verified {{ formatDate(tenant.kyb_verified_at) }}
          </span>
        </div>
        <p v-if="tenant.kyb_status === 'PENDING'" class="text-sm text-surface-400 mt-3">
          Your KYB verification is pending. Contact support if you need to expedite.
        </p>
        <p v-else-if="tenant.kyb_status === 'IN_REVIEW'" class="text-sm text-surface-400 mt-3">
          Your documents are being reviewed. This typically takes 1-3 business days.
        </p>
        <p v-else-if="tenant.kyb_status === 'REJECTED'" class="text-sm text-red-400 mt-3">
          Your KYB verification was rejected. Please contact support for next steps.
        </p>
      </div>

      <!-- Limits -->
      <div class="bg-surface-900 rounded-lg border border-surface-800 p-5">
        <h2 class="text-sm font-medium text-surface-400 mb-4">Limits</h2>
        <dl class="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <div>
            <dt class="text-xs text-surface-500">Daily Limit</dt>
            <dd class="text-sm text-surface-200 mt-0.5">
              <MoneyDisplay :amount="tenant.daily_limit_usd" currency="USD" />
            </dd>
          </div>
          <div>
            <dt class="text-xs text-surface-500">Per-Transfer Limit</dt>
            <dd class="text-sm text-surface-200 mt-0.5">
              <MoneyDisplay :amount="tenant.per_transfer_limit" currency="USD" />
            </dd>
          </div>
        </dl>
      </div>

      <!-- Fee Schedule -->
      <div class="bg-surface-900 rounded-lg border border-surface-800 p-5">
        <h2 class="text-sm font-medium text-surface-400 mb-4">Fee Schedule</h2>
        <dl class="grid grid-cols-1 sm:grid-cols-2 gap-4">
          <div>
            <dt class="text-xs text-surface-500">On-ramp Fee</dt>
            <dd class="text-sm text-surface-200 mt-0.5">{{ money.bpsToPercent(tenant.fee_schedule.on_ramp_bps) }}</dd>
          </div>
          <div>
            <dt class="text-xs text-surface-500">Off-ramp Fee</dt>
            <dd class="text-sm text-surface-200 mt-0.5">{{ money.bpsToPercent(tenant.fee_schedule.off_ramp_bps) }}</dd>
          </div>
          <div>
            <dt class="text-xs text-surface-500">Minimum Fee</dt>
            <dd class="text-sm text-surface-200 mt-0.5">
              <MoneyDisplay :amount="tenant.fee_schedule.min_fee_usd" currency="USD" />
            </dd>
          </div>
          <div>
            <dt class="text-xs text-surface-500">Maximum Fee</dt>
            <dd class="text-sm text-surface-200 mt-0.5">
              <MoneyDisplay :amount="tenant.fee_schedule.max_fee_usd" currency="USD" />
            </dd>
          </div>
        </dl>
      </div>
    </template>

    <EmptyState v-else title="Profile not loaded" icon="&#x2699;" />
  </div>
</template>

<script setup lang="ts">
const authStore = useAuthStore()
const money = useMoney()

const tenant = computed(() => authStore.tenant)

function formatDate(ts: string) {
  if (!ts) return ''
  return new Date(ts).toLocaleDateString('en-GB', { day: '2-digit', month: 'short', year: 'numeric' })
}

onMounted(() => {
  if (!authStore.tenant) {
    authStore.fetchProfile()
  }
})
</script>
