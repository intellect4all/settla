<template>
  <div class="fixed top-4 right-4 z-50 flex flex-col gap-2 max-w-sm">
    <TransitionGroup name="toast">
      <div
        v-for="toast in toasts" :key="toast.id"
        :class="toastClasses(toast.type)"
        class="px-4 py-3 rounded-lg shadow-lg border text-sm font-medium cursor-pointer"
        @click="dismiss(toast.id)"
      >
        {{ toast.message }}
      </div>
    </TransitionGroup>
  </div>
</template>

<script setup lang="ts">
const { toasts, dismiss } = useToast()

function toastClasses(type: string) {
  switch (type) {
    case 'success': return 'bg-emerald-900/90 border-emerald-700 text-emerald-100'
    case 'error': return 'bg-red-900/90 border-red-700 text-red-100'
    case 'warning': return 'bg-amber-900/90 border-amber-700 text-amber-100'
    default: return 'bg-blue-900/90 border-blue-700 text-blue-100'
  }
}
</script>

<style scoped>
.toast-enter-active { transition: all 0.3s ease; }
.toast-leave-active { transition: all 0.2s ease; }
.toast-enter-from { opacity: 0; transform: translateX(100%); }
.toast-leave-to { opacity: 0; transform: translateX(100%); }
</style>
