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
    class="fixed lg:static z-40 w-56 bg-surface-900 border-r border-surface-800 flex flex-col h-full shrink-0 transition-transform duration-200"
  >
    <!-- Logo -->
    <div class="px-4 py-5 border-b border-surface-800">
      <div class="flex items-center gap-2.5">
        <div class="w-8 h-8 bg-violet-600 rounded-lg flex items-center justify-center">
          <span class="text-white font-bold text-sm">S</span>
        </div>
        <div>
          <h1 class="text-sm font-bold text-surface-100 tracking-tight">Settla</h1>
          <p class="text-xs text-surface-500 truncate max-w-[120px]">{{ tenantName }}</p>
        </div>
      </div>
    </div>

    <!-- Navigation -->
    <nav class="flex-1 px-3 py-4 overflow-y-auto">
      <!-- Main group -->
      <p class="px-3 mb-2 text-[10px] font-semibold uppercase tracking-widest text-surface-600">Main</p>
      <div class="space-y-0.5 mb-4">
        <NuxtLink
          v-for="item in mainItems"
          :key="item.path"
          :to="item.path"
          :class="[
            isActive(item.path)
              ? 'bg-violet-600/10 text-violet-400 border-l-2 border-violet-500'
              : 'text-surface-400 hover:text-surface-200 hover:bg-surface-800/70 border-l-2 border-transparent',
          ]"
          class="flex items-center gap-3 px-3 py-2 rounded-r-lg text-sm font-medium transition-all duration-150"
          :aria-current="isActive(item.path) ? 'page' : undefined"
          @click="mobileOpen = false"
        >
          <Icon :name="item.icon" :size="18" />
          {{ item.label }}
        </NuxtLink>
      </div>

      <!-- Settings group -->
      <p class="px-3 mb-2 text-[10px] font-semibold uppercase tracking-widest text-surface-600">Settings</p>
      <div class="space-y-0.5">
        <NuxtLink
          v-for="item in settingsItems"
          :key="item.path"
          :to="item.path"
          :class="[
            isActive(item.path)
              ? 'bg-violet-600/10 text-violet-400 border-l-2 border-violet-500'
              : 'text-surface-400 hover:text-surface-200 hover:bg-surface-800/70 border-l-2 border-transparent',
          ]"
          class="flex items-center gap-3 px-3 py-2 rounded-r-lg text-sm font-medium transition-all duration-150"
          :aria-current="isActive(item.path) ? 'page' : undefined"
          @click="mobileOpen = false"
        >
          <Icon :name="item.icon" :size="18" />
          {{ item.label }}
        </NuxtLink>
      </div>
    </nav>

    <!-- Footer -->
    <div class="px-3 py-3 border-t border-surface-800 space-y-1">
      <DarkModeToggle />
      <p class="text-xs text-surface-600 px-3 py-1">Portal v0.1.0</p>
    </div>
  </aside>
</template>

<script setup lang="ts">
const route = useRoute()
const authStore = useAuthStore()

const mobileOpen = ref(false)
const tenantName = computed(() => authStore.tenantName || 'Tenant Portal')

const mainItems = [
  { path: '/', label: 'Dashboard', icon: 'home' },
  { path: '/transfers', label: 'Transfers', icon: 'arrows-right-left' },
  { path: '/deposits', label: 'Deposits', icon: 'inbox' },
  { path: '/payment-links', label: 'Payment Links', icon: 'link' },
  { path: '/treasury', label: 'Treasury', icon: 'wallet' },
  { path: '/analytics', label: 'Analytics', icon: 'bar-chart' },
  { path: '/analytics/export', label: 'Export Data', icon: 'download' },
  { path: '/billing', label: 'Fees & Billing', icon: 'receipt' },
]

const settingsItems = [
  { path: '/settings/api-keys', label: 'API Keys', icon: 'key' },
  { path: '/settings/webhooks', label: 'Webhooks', icon: 'zap' },
  { path: '/settings/account', label: 'Account', icon: 'settings' },
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
