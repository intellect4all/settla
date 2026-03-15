<template>
  <aside class="w-56 bg-surface-900 border-r border-surface-800 flex flex-col h-full shrink-0">
    <!-- Logo -->
    <div class="px-4 py-5 border-b border-surface-800">
      <h1 class="text-lg font-bold text-surface-100 tracking-tight">Settla</h1>
      <p class="text-xs text-surface-500 mt-0.5">{{ tenantName }}</p>
    </div>

    <!-- Navigation -->
    <nav class="flex-1 px-3 py-4 space-y-1 overflow-y-auto">
      <NuxtLink
        v-for="item in navItems"
        :key="item.path"
        :to="item.path"
        :class="[
          isActive(item.path)
            ? 'bg-violet-600/10 text-violet-400 border-violet-500/30'
            : 'text-surface-400 hover:text-surface-200 hover:bg-surface-800 border-transparent',
        ]"
        class="flex items-center gap-3 px-3 py-2 rounded-lg text-sm font-medium border transition-colors"
      >
        <span class="text-lg w-5 text-center">{{ item.icon }}</span>
        {{ item.label }}
      </NuxtLink>
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

const tenantName = computed(() => authStore.tenantName || 'Tenant Portal')

const navItems = [
  { path: '/', label: 'Dashboard', icon: '\u25A6' },
  { path: '/transfers', label: 'Transfers', icon: '\u21C4' },
  { path: '/treasury', label: 'Treasury', icon: '\u2B21' },
  { path: '/analytics', label: 'Analytics', icon: '\u25D4' },
  { path: '/billing', label: 'Fees & Billing', icon: '\u2637' },
  { path: '/settings/api-keys', label: 'API Keys', icon: '\u26BF' },
  { path: '/settings/webhooks', label: 'Webhooks', icon: '\u26A1' },
  { path: '/settings/account', label: 'Account', icon: '\u2699' },
]

function isActive(path: string) {
  if (path === '/') return route.path === '/'
  return route.path.startsWith(path)
}
</script>
