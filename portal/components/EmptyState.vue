<template>
  <div class="flex flex-col items-center justify-center py-12 text-center">
    <div class="w-12 h-12 rounded-full bg-surface-800 flex items-center justify-center mb-4">
      <span class="text-2xl text-surface-500">{{ icon }}</span>
    </div>
    <h3 class="text-sm font-medium text-surface-300 mb-1">{{ title }}</h3>
    <p v-if="description" class="text-xs text-surface-500 max-w-sm">{{ description }}</p>
  </div>
</template>

<script setup lang="ts">
withDefaults(defineProps<{ title: string; description?: string; icon?: string }>(), { icon: '\u2205' })
</script>
