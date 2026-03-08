<template>
  <div class="card p-4">
    <div class="flex items-center justify-between">
      <p class="text-xs font-medium text-surface-500 uppercase tracking-wider">{{ label }}</p>
      <span v-if="trend" :class="trend > 0 ? 'text-emerald-400' : 'text-red-400'" class="text-xs font-medium">
        {{ trend > 0 ? '+' : '' }}{{ trend.toFixed(1) }}%
      </span>
    </div>
    <div class="mt-2">
      <p class="text-2xl font-semibold text-surface-100 font-mono tabular-nums">
        <slot>{{ displayValue }}</slot>
      </p>
    </div>
    <p v-if="subtitle" class="text-xs text-surface-500 mt-1">{{ subtitle }}</p>
  </div>
</template>

<script setup lang="ts">
const props = defineProps<{
  label: string
  value?: string | number
  subtitle?: string
  trend?: number
}>()

const displayValue = computed(() => props.value ?? '&#8212;')
</script>
