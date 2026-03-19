<template>
  <!-- Mobile overlay -->
  <Transition name="sidebar-overlay">
    <div
      v-if="mobileOpen"
      class="fixed inset-0 bg-black/50 z-40 lg:hidden"
      @click="mobileOpen = false"
    />
  </Transition>

  <!-- Mobile hamburger -->
  <button
    class="fixed top-3 left-3 z-50 lg:hidden p-2 rounded-lg bg-surface-900 border border-surface-800 text-surface-400 hover:text-surface-200"
    @click="mobileOpen = !mobileOpen"
  >
    <Icon :name="mobileOpen ? 'x' : 'grid'" :size="20" />
  </button>

  <aside
    :class="mobileOpen ? 'translate-x-0' : '-translate-x-full lg:translate-x-0'"
    class="fixed lg:static z-40 w-60 h-screen flex flex-col bg-surface-900 border-r border-surface-800 shrink-0 transition-transform duration-200"
  >
    <!-- Logo -->
    <div class="px-5 py-5 border-b border-surface-800">
      <div class="flex items-center gap-2">
        <div class="w-8 h-8 bg-violet-600 rounded-lg flex items-center justify-center">
          <span class="text-white font-bold text-sm">S</span>
        </div>
        <div>
          <h1 class="text-base font-semibold text-surface-100">Settla</h1>
          <p class="text-xs text-surface-500">Operations</p>
        </div>
      </div>
    </div>

    <!-- Navigation -->
    <nav class="flex-1 px-3 py-4 overflow-y-auto">
      <p class="px-3 mb-2 text-[10px] font-semibold uppercase tracking-widest text-surface-600">Overview</p>
      <div class="space-y-0.5">
        <NuxtLink
          v-for="item in navItems"
          :key="item.path"
          :to="item.path"
          class="flex items-center gap-3 px-3 py-2 rounded-r-lg text-sm font-medium transition-all duration-150"
          :class="isActive(item.path)
            ? 'bg-violet-600/10 text-violet-400 border-l-2 border-violet-500'
            : 'text-surface-400 hover:text-surface-200 hover:bg-surface-800/70 border-l-2 border-transparent'"
          :aria-current="isActive(item.path) ? 'page' : undefined"
          @click="mobileOpen = false"
        >
          <Icon :name="item.icon" :size="18" />
          <span>{{ item.label }}</span>
        </NuxtLink>
      </div>
    </nav>

    <!-- External Links -->
    <div class="px-3 py-2 border-t border-surface-800">
      <a
        :href="grafanaUrl"
        target="_blank"
        rel="noopener"
        class="flex items-center gap-3 px-3 py-2 rounded-lg text-sm font-medium text-surface-400 hover:text-surface-200 hover:bg-surface-800 transition-colors duration-150"
      >
        <Icon name="activity" :size="18" />
        <span>Grafana</span>
        <Icon name="external-link" :size="12" class="ml-auto text-surface-600" />
      </a>
    </div>

    <!-- Footer -->
    <div class="px-3 py-4 border-t border-surface-800">
      <DarkModeToggle />
      <div class="mt-3 px-3">
        <p class="text-xs text-surface-600">Settla v0.6.0</p>
      </div>
    </div>
  </aside>
</template>

<script setup lang="ts">
const config = useRuntimeConfig()
const route = useRoute()
const mobileOpen = ref(false)
const grafanaUrl = (config.public.prometheusBase as string).replace(/:\d+$/, ':3002')

const navItems = [
  { path: '/', label: 'Dashboard', icon: 'home' },
  { path: '/tenants', label: 'Tenants', icon: 'users' },
  { path: '/transfers', label: 'Transfers', icon: 'arrows-right-left' },
  { path: '/treasury', label: 'Treasury', icon: 'wallet' },
  { path: '/ledger', label: 'Ledger', icon: 'layers' },
  { path: '/routes', label: 'Routes', icon: 'shield' },
  { path: '/capacity', label: 'Capacity', icon: 'activity' },
  { path: '/manual-reviews', label: 'Manual Reviews', icon: 'inbox' },
  { path: '/reconciliation', label: 'Reconciliation', icon: 'check-circle' },
  { path: '/settlements', label: 'Settlements', icon: 'credit-card' },
]

function isActive(path: string) {
  if (path === '/') return route.path === '/'
  return route.path.startsWith(path)
}
</script>

<style scoped>
.sidebar-overlay-enter-active,
.sidebar-overlay-leave-active {
  transition: opacity 0.2s ease;
}
.sidebar-overlay-enter-from,
.sidebar-overlay-leave-to {
  opacity: 0;
}
</style>
