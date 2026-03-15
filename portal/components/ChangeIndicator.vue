<template>
  <div class="flex items-center gap-1 mt-1">
    <span
      v-if="value !== 0"
      class="text-xs font-medium"
      :class="value > 0 ? 'text-green-400' : 'text-red-400'"
    >
      {{ value > 0 ? '↑' : '↓' }} {{ Math.abs(value) }}%
    </span>
    <span v-else class="text-xs text-surface-500">—</span>
    <span v-if="label" class="text-xs text-surface-500">{{ label }}</span>
  </div>
</template>

<script setup lang="ts">
defineProps<{
  value: number
  label?: string
}>()
</script>
