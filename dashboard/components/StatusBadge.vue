<template>
  <span
    :class="[badgeClasses, sizeClass]"
    class="inline-flex items-center font-medium rounded-full"
  >
    <span v-if="showDot" :class="dotClass" class="w-1.5 h-1.5 rounded-full mr-1.5" />
    {{ label }}
  </span>
</template>

<script setup lang="ts">
import type { TransferStatus } from '~/types'

const props = withDefaults(defineProps<{
  status: TransferStatus | string
  size?: 'sm' | 'base'
  showDot?: boolean
}>(), {
  size: 'sm',
  showDot: true,
})

const statusConfig: Record<string, { label: string; bg: string; text: string; dot: string }> = {
  CREATED: { label: 'Created', bg: 'bg-surface-700/50', text: 'text-surface-300', dot: 'bg-surface-400' },
  FUNDED: { label: 'Funded', bg: 'bg-blue-500/10', text: 'text-blue-400', dot: 'bg-blue-400' },
  ON_RAMPING: { label: 'On-ramping', bg: 'bg-violet-500/10', text: 'text-violet-400', dot: 'bg-violet-400 animate-pulse' },
  SETTLING: { label: 'Settling', bg: 'bg-amber-500/10', text: 'text-amber-400', dot: 'bg-amber-400 animate-pulse' },
  OFF_RAMPING: { label: 'Off-ramping', bg: 'bg-violet-500/10', text: 'text-violet-400', dot: 'bg-violet-400 animate-pulse' },
  COMPLETING: { label: 'Completing', bg: 'bg-emerald-500/10', text: 'text-emerald-400', dot: 'bg-emerald-400 animate-pulse' },
  COMPLETED: { label: 'Completed', bg: 'bg-emerald-500/10', text: 'text-emerald-400', dot: 'bg-emerald-400' },
  FAILED: { label: 'Failed', bg: 'bg-red-500/10', text: 'text-red-400', dot: 'bg-red-400' },
  REFUNDING: { label: 'Refunding', bg: 'bg-amber-500/10', text: 'text-amber-400', dot: 'bg-amber-400 animate-pulse' },
  REFUNDED: { label: 'Refunded', bg: 'bg-amber-500/10', text: 'text-amber-400', dot: 'bg-amber-400' },
  // Generic statuses
  ACTIVE: { label: 'Active', bg: 'bg-emerald-500/10', text: 'text-emerald-400', dot: 'bg-emerald-400' },
  SUSPENDED: { label: 'Suspended', bg: 'bg-red-500/10', text: 'text-red-400', dot: 'bg-red-400' },
  ONBOARDING: { label: 'Onboarding', bg: 'bg-blue-500/10', text: 'text-blue-400', dot: 'bg-blue-400' },
  VERIFIED: { label: 'Verified', bg: 'bg-emerald-500/10', text: 'text-emerald-400', dot: 'bg-emerald-400' },
  PENDING: { label: 'Pending', bg: 'bg-amber-500/10', text: 'text-amber-400', dot: 'bg-amber-400' },
}

const cfg = computed(() => statusConfig[props.status] || { label: props.status, bg: 'bg-surface-700/50', text: 'text-surface-400', dot: 'bg-surface-500' })
const label = computed(() => cfg.value.label)
const badgeClasses = computed(() => `${cfg.value.bg} ${cfg.value.text}`)
const dotClass = computed(() => cfg.value.dot)
const sizeClass = computed(() => props.size === 'sm' ? 'px-2.5 py-0.5 text-xs' : 'px-3 py-1 text-sm')
</script>
