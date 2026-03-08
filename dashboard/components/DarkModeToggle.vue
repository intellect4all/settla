<template>
  <button
    class="flex items-center gap-2 w-full px-3 py-2 rounded-lg text-sm text-surface-400 hover:text-surface-200 hover:bg-surface-800 transition-colors"
    @click="toggle"
  >
    <span class="text-lg">{{ $colorMode.value === 'dark' ? '&#9788;' : '&#9790;' }}</span>
    <span>{{ $colorMode.value === 'dark' ? 'Light mode' : 'Dark mode' }}</span>
  </button>
</template>

<script setup lang="ts">
const colorMode = useColorMode()

function toggle() {
  colorMode.preference = colorMode.value === 'dark' ? 'light' : 'dark'
}
</script>
