<template>
  <button
    :type="type"
    :disabled="disabled || loading"
    :class="[variantClass, sizeClass]"
    class="inline-flex items-center justify-center gap-2 font-medium rounded-lg transition-all duration-150 focus-ring"
  >
    <!-- Loading spinner -->
    <svg
      v-if="loading"
      class="animate-spin"
      :width="size === 'sm' ? 14 : 16"
      :height="size === 'sm' ? 14 : 16"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      stroke-width="2.5"
    >
      <path d="M21 12a9 9 0 1 1-6.219-8.56" />
    </svg>

    <!-- Icon -->
    <Icon v-else-if="icon" :name="icon" :size="size === 'sm' ? 14 : 16" />

    <slot />
  </button>
</template>

<script setup lang="ts">
const props = withDefaults(defineProps<{
  variant?: 'primary' | 'secondary' | 'danger' | 'ghost'
  size?: 'sm' | 'base'
  loading?: boolean
  disabled?: boolean
  icon?: string
  type?: 'button' | 'submit' | 'reset'
}>(), {
  variant: 'primary',
  size: 'base',
  type: 'button',
})

const variantClass = computed(() => {
  const base = props.disabled || props.loading ? 'opacity-50 cursor-not-allowed' : ''
  switch (props.variant) {
    case 'primary':
      return `bg-violet-600 hover:bg-violet-500 text-white ${base}`
    case 'secondary':
      return `bg-surface-800 hover:bg-surface-700 text-surface-200 ${base}`
    case 'danger':
      return `bg-red-600 hover:bg-red-500 text-white ${base}`
    case 'ghost':
      return `bg-transparent hover:bg-surface-800 text-surface-300 ${base}`
    default:
      return base
  }
})

const sizeClass = computed(() => {
  switch (props.size) {
    case 'sm': return 'px-3 py-1.5 text-xs'
    default: return 'px-4 py-2 text-sm'
  }
})
</script>
