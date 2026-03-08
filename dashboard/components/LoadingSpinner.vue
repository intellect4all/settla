<template>
  <div class="flex items-center justify-center" :class="containerClass">
    <div
      :class="[sizeClass, 'border-violet-500']"
      class="animate-spin rounded-full border-2 border-t-transparent"
    />
  </div>
</template>

<script setup lang="ts">
const props = withDefaults(defineProps<{
  size?: 'sm' | 'base' | 'lg'
  fullPage?: boolean
}>(), {
  size: 'base',
})

const sizeClass = computed(() => {
  const map: Record<string, string> = {
    sm: 'w-4 h-4',
    base: 'w-6 h-6',
    lg: 'w-10 h-10',
  }
  return map[props.size]
})

const containerClass = computed(() => props.fullPage ? 'h-64' : '')
</script>
