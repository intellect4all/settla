<template>
  <div class="flex items-center gap-1 mt-1">
    <span
      v-if="value !== 0"
      class="text-xs font-medium inline-flex items-center gap-0.5"
      :class="value > 0 ? 'text-emerald-400' : 'text-red-400'"
    >
      <Icon :name="value > 0 ? 'trending-up' : 'trending-down'" :size="12" />
      {{ Math.abs(value) }}%
    </span>
    <span v-else class="text-xs text-surface-500">&mdash;</span>
    <span v-if="label" class="text-xs text-surface-500">{{ label }}</span>
  </div>
</template>

<script setup lang="ts">
defineProps<{
  value: number
  label?: string
}>()
</script>
