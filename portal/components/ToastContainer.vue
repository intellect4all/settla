<template>
  <div class="fixed top-4 right-4 z-50 flex flex-col gap-2 max-w-sm" role="status" :aria-live="toasts.some(t => t.type === 'error') ? 'assertive' : 'polite'" aria-atomic="true">
    <TransitionGroup name="toast">
      <div
        v-for="toast in toasts" :key="toast.id"
        :class="toastClasses(toast.type)"
        class="relative px-4 py-3 rounded-lg shadow-lg border text-sm font-medium overflow-hidden"
        role="alert"
      >
        <div class="flex items-start gap-3">
          <Icon :name="toastIcon(toast.type)" :size="18" class="shrink-0 mt-0.5" />
          <span class="flex-1">{{ toast.message }}</span>
          <button
            class="shrink-0 opacity-60 hover:opacity-100 transition-opacity p-0.5"
            @click="dismiss(toast.id)"
          >
            <Icon name="x" :size="14" />
          </button>
        </div>
        <!-- Progress bar -->
        <div
          class="absolute bottom-0 left-0 h-0.5 rounded-full"
          :class="progressColor(toast.type)"
          :style="{ animation: `toast-progress ${toast.duration}ms linear forwards` }"
        />
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

function toastIcon(type: string) {
  switch (type) {
    case 'success': return 'check-circle'
    case 'error': return 'x-circle'
    case 'warning': return 'alert-triangle'
    default: return 'info'
  }
}

function progressColor(type: string) {
  switch (type) {
    case 'success': return 'bg-emerald-400'
    case 'error': return 'bg-red-400'
    case 'warning': return 'bg-amber-400'
    default: return 'bg-blue-400'
  }
}
</script>

<style scoped>
.toast-enter-active { transition: all 0.3s cubic-bezier(0.16, 1, 0.3, 1); }
.toast-leave-active { transition: all 0.2s ease-in; }
.toast-enter-from { opacity: 0; transform: translateX(100%) scale(0.95); }
.toast-leave-to { opacity: 0; transform: translateX(100%) scale(0.95); }

@keyframes toast-progress {
  from { width: 100%; }
  to { width: 0%; }
}
</style>
