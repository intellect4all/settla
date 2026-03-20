<template>
  <div role="list" class="relative">
    <div v-for="(event, index) in events" :key="event.id || index" role="listitem" class="flex gap-4 pb-6 last:pb-0">
      <div class="flex flex-col items-center">
        <div :class="dotColor(event.to_status)" class="w-3 h-3 rounded-full ring-4 timeline-dot z-10 shrink-0" />
        <div v-if="index < events.length - 1" class="w-px flex-1 timeline-line mt-1" />
      </div>
      <div class="flex-1 -mt-0.5 min-w-0">
        <div class="flex items-center gap-2 flex-wrap">
          <StatusBadge :status="event.to_status" size="sm" />
          <span v-if="event.from_status" class="text-xs text-surface-500">from {{ event.from_status }}</span>
        </div>
        <p class="text-xs text-surface-500 mt-1">
          {{ formatTimestamp(event.occurred_at) }}
          <span v-if="duration(index)" class="timeline-secondary ml-2">({{ duration(index) }})</span>
        </p>
        <p v-if="event.provider_ref" class="text-xs timeline-secondary mt-0.5 font-mono truncate">ref: {{ event.provider_ref }}</p>
      </div>
    </div>
    <div v-if="!events.length" class="text-sm text-surface-500 py-4 text-center">No events recorded</div>
  </div>
</template>

<script setup lang="ts">
import type { TransferEvent } from '~/types'

const props = defineProps<{ events: TransferEvent[] }>()

function dotColor(status: string) {
  return ({ COMPLETED: 'bg-emerald-500', FAILED: 'bg-red-500', REFUNDED: 'bg-amber-500' }[status]) || 'bg-violet-500'
}

function formatTimestamp(ts: string) {
  if (!ts) return ''
  return new Date(ts).toLocaleString('en-GB', { day: '2-digit', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false })
}

function duration(index: number) {
  if (index === 0) return null
  const prev = props.events[index - 1]
  const curr = props.events[index]
  if (!prev?.occurred_at || !curr?.occurred_at) return null
  const ms = new Date(curr.occurred_at).getTime() - new Date(prev.occurred_at).getTime()
  if (ms < 1000) return `${ms}ms`
  if (ms < 60000) return `${(ms / 1000).toFixed(1)}s`
  return `${(ms / 60000).toFixed(1)}min`
}
</script>
