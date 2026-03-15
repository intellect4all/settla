<template>
  <aside class="w-60 h-screen flex flex-col bg-surface-900 border-r border-surface-800 shrink-0">
    <div class="light:bg-white light:border-surface-200"></div>
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
    <nav class="flex-1 px-3 py-4 space-y-1 overflow-y-auto">
      <NuxtLink
        v-for="item in navItems"
        :key="item.path"
        :to="item.path"
        class="flex items-center gap-3 px-3 py-2 rounded-lg text-sm font-medium transition-colors duration-150"
        :class="isActive(item.path)
          ? 'bg-violet-600/10 text-violet-400 border border-violet-500/20'
          : 'text-surface-400 hover:text-surface-200 hover:bg-surface-800 border border-transparent'"
      >
        <span class="text-lg" v-html="item.icon" />
        <span>{{ item.label }}</span>
      </NuxtLink>
    </nav>

    <!-- External Links -->
    <div class="px-3 py-2 border-t border-surface-800">
      <a
        :href="grafanaUrl"
        target="_blank"
        rel="noopener"
        class="flex items-center gap-3 px-3 py-2 rounded-lg text-sm font-medium text-surface-400 hover:text-surface-200 hover:bg-surface-800 border border-transparent transition-colors duration-150"
      >
        <span class="text-lg">&#9851;</span>
        <span>Grafana</span>
        <span class="text-xs text-surface-600 ml-auto">&#8599;</span>
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
const grafanaUrl = (config.public.prometheusBase as string).replace(/:\d+$/, ':3002')

const navItems = [
  { path: '/', label: 'Dashboard', icon: '&#9632;' },
  { path: '/transfers', label: 'Transfers', icon: '&#8644;' },
  { path: '/treasury', label: 'Treasury', icon: '&#9733;' },
  { path: '/ledger', label: 'Ledger', icon: '&#9776;' },
  { path: '/routes', label: 'Routes', icon: '&#9741;' },
  { path: '/capacity', label: 'Capacity', icon: '&#9881;' },
  { path: '/manual-reviews', label: 'Manual Reviews', icon: '&#9998;' },
  { path: '/reconciliation', label: 'Reconciliation', icon: '&#9878;' },
  { path: '/settlements', label: 'Settlements', icon: '&#9884;' },
]

function isActive(path: string) {
  if (path === '/') return route.path === '/'
  return route.path.startsWith(path)
}
</script>
