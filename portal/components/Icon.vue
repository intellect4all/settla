<template>
  <svg
    :width="size"
    :height="size"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    stroke-width="2"
    stroke-linecap="round"
    stroke-linejoin="round"
    :class="props.class"
  >
    <path v-for="(d, i) in paths" :key="i" :d="d" />
  </svg>
</template>

<script setup lang="ts">
import { icons } from '~/utils/icons'

const props = withDefaults(defineProps<{
  name: string
  size?: number
  class?: string
}>(), {
  size: 20,
})

const paths = computed(() => {
  const raw = icons[props.name]
  if (!raw) return []
  // Split multi-path strings: each sub-path starts with M
  return raw.split(/\s(?=M)/)
})
</script>
