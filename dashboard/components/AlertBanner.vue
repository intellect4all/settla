<template>
  <div
    v-if="visible"
    :class="bannerClasses"
    class="flex items-center gap-3 px-4 py-3 rounded-lg text-sm border"
  >
    <Icon :name="iconName" :size="18" class="shrink-0" />
    <div class="flex-1 min-w-0">
      <p class="font-medium">{{ title }}</p>
      <p v-if="description" class="text-xs opacity-80 mt-0.5">{{ description }}</p>
    </div>
    <button
      v-if="dismissible"
      class="text-current opacity-50 hover:opacity-100 transition-opacity shrink-0"
      @click="visible = false"
    >
      <Icon name="x" :size="16" />
    </button>
  </div>
</template>

<script setup lang="ts">
const props = withDefaults(defineProps<{
  type?: 'warning' | 'error' | 'info' | 'success'
  title: string
  description?: string
  dismissible?: boolean
}>(), {
  type: 'warning',
  dismissible: true,
})

const visible = ref(true)

const bannerClasses = computed(() => {
  switch (props.type) {
    case 'error': return 'bg-red-500/10 border-red-500/20 text-red-400'
    case 'success': return 'bg-emerald-500/10 border-emerald-500/20 text-emerald-400'
    case 'info': return 'bg-blue-500/10 border-blue-500/20 text-blue-400'
    default: return 'bg-amber-500/10 border-amber-500/20 text-amber-400'
  }
})

const iconName = computed(() => {
  switch (props.type) {
    case 'error': return 'alert-triangle'
    case 'success': return 'check-circle'
    case 'info': return 'info'
    default: return 'alert-triangle'
  }
})
</script>
