<template>
  <div class="bg-surface-950 border border-surface-800 rounded-lg px-4 py-3 flex items-center justify-between gap-4">
    <div class="min-w-0">
      <div class="flex items-center gap-2">
        <code class="text-sm font-mono text-surface-200">{{ apiKey.key_prefix }}...</code>
        <StatusBadge :status="apiKey.environment" size="sm" />
      </div>
      <p v-if="apiKey.name" class="text-xs text-surface-500 mt-0.5">{{ apiKey.name }}</p>
      <p class="text-[10px] text-surface-600 mt-0.5">
        Created {{ formatDate(apiKey.created_at) }}
        <span v-if="apiKey.last_used_at" class="ml-2">Last used {{ formatDate(apiKey.last_used_at) }}</span>
      </p>
    </div>
    <div v-if="!disabled && apiKey.is_active" class="flex items-center gap-1 shrink-0">
      <button class="text-xs text-surface-400 hover:text-surface-200 px-2 py-1 rounded hover:bg-surface-800" @click="$emit('rotate', apiKey)">
        Rotate
      </button>
      <button class="text-xs text-red-400 hover:text-red-300 px-2 py-1 rounded hover:bg-surface-800" @click="$emit('revoke', apiKey)">
        Revoke
      </button>
    </div>
    <span v-else-if="!apiKey.is_active" class="text-xs text-surface-600">Revoked</span>
  </div>
</template>

<script setup lang="ts">
import type { APIKeyInfo } from '~/types'

defineProps<{ apiKey: APIKeyInfo; disabled?: boolean }>()
defineEmits<{ revoke: [key: APIKeyInfo]; rotate: [key: APIKeyInfo] }>()

function formatDate(ts: string) {
  if (!ts) return ''
  return new Date(ts).toLocaleDateString('en-GB', { day: '2-digit', month: 'short', year: 'numeric' })
}
</script>
