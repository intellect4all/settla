<template>
  <div :class="containerClass">
    <!-- Text variant -->
    <template v-if="variant === 'text'">
      <div v-for="i in lines" :key="i" class="skeleton h-4 mb-2 last:mb-0" :style="{ width: i === lines ? '60%' : width || '100%' }" />
    </template>

    <!-- Card variant -->
    <template v-else-if="variant === 'card'">
      <div class="skeleton rounded-xl" :style="{ width: width || '100%', height: height || '120px' }" />
    </template>

    <!-- Table variant -->
    <template v-else-if="variant === 'table'">
      <div class="space-y-3">
        <div class="skeleton h-8 rounded" />
        <div v-for="i in lines" :key="i" class="skeleton h-12 rounded" />
      </div>
    </template>

    <!-- Chart variant -->
    <template v-else-if="variant === 'chart'">
      <div class="skeleton rounded-xl" :style="{ width: width || '100%', height: height || '240px' }" />
    </template>

    <!-- Circle variant -->
    <template v-else-if="variant === 'circle'">
      <div class="skeleton rounded-full" :style="{ width: width || '40px', height: width || '40px' }" />
    </template>
  </div>
</template>

<script setup lang="ts">
const props = withDefaults(defineProps<{
  variant?: 'text' | 'card' | 'table' | 'chart' | 'circle'
  lines?: number
  width?: string
  height?: string
}>(), {
  variant: 'text',
  lines: 3,
})

const containerClass = computed(() => {
  if (props.variant === 'text') return 'space-y-0'
  return ''
})
</script>
