<template>
  <Teleport to="body">
    <Transition name="modal">
      <div
        v-if="open"
        class="fixed inset-0 z-50 flex items-center justify-center p-4"
        role="dialog"
        aria-modal="true"
        :aria-labelledby="titleId"
        @keydown.esc="closeOnEsc && $emit('close')"
      >
        <!-- Overlay -->
        <div
          class="absolute inset-0 bg-black/60 backdrop-blur-sm"
          @click="closeOnOverlay && $emit('close')"
        />

        <!-- Panel -->
        <div
          ref="panelRef"
          :class="sizeClass"
          class="relative bg-surface-900 border border-surface-700 rounded-xl shadow-2xl w-full max-h-[90vh] flex flex-col"
          @keydown.tab="handleTab"
        >
          <!-- Header -->
          <div class="flex items-center justify-between px-6 py-4 border-b border-surface-800">
            <h2 :id="titleId" class="text-lg font-semibold text-surface-100">{{ title }}</h2>
            <button
              class="p-1 rounded-lg text-surface-400 hover:text-surface-200 hover:bg-surface-800 transition-colors focus-ring"
              @click="$emit('close')"
            >
              <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M18 6L6 18M6 6l12 12" /></svg>
            </button>
          </div>

          <!-- Body -->
          <div class="px-6 py-4 overflow-y-auto flex-1">
            <slot />
          </div>

          <!-- Footer -->
          <div v-if="$slots.footer" class="px-6 py-4 border-t border-surface-800 flex justify-end gap-3">
            <slot name="footer" />
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<script setup lang="ts">
const props = withDefaults(defineProps<{
  open: boolean
  title: string
  size?: 'sm' | 'md' | 'lg'
  closeOnEsc?: boolean
  closeOnOverlay?: boolean
}>(), {
  size: 'md',
  closeOnEsc: true,
  closeOnOverlay: true,
})

const emit = defineEmits<{ close: [] }>()

const panelRef = ref<HTMLElement>()
const titleId = `modal-title-${Math.random().toString(36).slice(2, 8)}`

const sizeClass = computed(() => {
  switch (props.size) {
    case 'sm': return 'max-w-sm'
    case 'lg': return 'max-w-2xl'
    default: return 'max-w-lg'
  }
})

const FOCUSABLE_SELECTOR = 'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])'

// Focus trap: cycle Tab/Shift+Tab within modal
function handleTab(e: KeyboardEvent) {
  if (!panelRef.value) return
  const focusable = panelRef.value.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR)
  if (focusable.length === 0) return
  const first = focusable[0]
  const last = focusable[focusable.length - 1]
  if (e.shiftKey && document.activeElement === first) {
    e.preventDefault()
    last.focus()
  } else if (!e.shiftKey && document.activeElement === last) {
    e.preventDefault()
    first.focus()
  }
}

// Focus first element on open, restore focus on close, lock body scroll
const previousFocus = ref<HTMLElement>()

watch(() => props.open, (isOpen) => {
  if (isOpen) {
    previousFocus.value = document.activeElement as HTMLElement
    document.body.style.overflow = 'hidden'
    nextTick(() => {
      const first = panelRef.value?.querySelector<HTMLElement>(FOCUSABLE_SELECTOR)
      first?.focus()
    })
  } else {
    document.body.style.overflow = ''
    nextTick(() => {
      previousFocus.value?.focus()
    })
  }
})

// Restore state if the component is unmounted while the modal is still open
onUnmounted(() => {
  if (props.open) {
    document.body.style.overflow = ''
    previousFocus.value?.focus()
  }
})
</script>

<style scoped>
.modal-enter-active,
.modal-leave-active {
  transition: opacity 0.2s ease;
}
.modal-enter-active .relative,
.modal-leave-active .relative {
  transition: opacity 0.2s ease, transform 0.2s ease;
}
.modal-enter-from {
  opacity: 0;
}
.modal-enter-from .relative {
  opacity: 0;
  transform: scale(0.95);
}
.modal-leave-to {
  opacity: 0;
}
.modal-leave-to .relative {
  opacity: 0;
  transform: scale(0.95);
}

/* Light mode */
:global(.light) .bg-surface-900 {
  @apply bg-white;
}
:global(.light) .border-surface-700 {
  @apply border-surface-200;
}
:global(.light) .border-surface-800 {
  @apply border-surface-200;
}
</style>
