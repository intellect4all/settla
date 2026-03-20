<template>
  <button
    class="flex items-center gap-2 w-full px-3 py-2 rounded-lg text-sm text-surface-400 hover:text-surface-200 hover:bg-surface-800 transition-colors focus-ring"
    aria-label="Toggle dark mode"
    @click="toggle"
  >
    <Icon :name="$colorMode.value === 'dark' ? 'sun' : 'moon'" :size="18" />
    <span>{{ $colorMode.value === 'dark' ? 'Light mode' : 'Dark mode' }}</span>
  </button>
</template>

<script setup lang="ts">
const colorMode = useColorMode()
function toggle() { colorMode.preference = colorMode.value === 'dark' ? 'light' : 'dark' }
</script>
