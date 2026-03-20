<template>
  <div class="flex h-screen overflow-hidden">
    <PortalSidebar />
    <main class="flex-1 overflow-y-auto">
      <!-- KYB Pending Banner -->
      <div
        v-if="authStore.isAuthenticated && authStore.tenantStatus === 'ONBOARDING'"
        class="bg-yellow-900/50 border-b border-yellow-800 px-6 py-2 text-sm text-yellow-300"
      >
        <template v-if="authStore.kybStatus === 'PENDING'">
          Your account is pending KYB verification.
          <NuxtLink to="/onboarding/kyb" class="underline hover:text-yellow-200">Complete KYB</NuxtLink>
        </template>
        <template v-else-if="authStore.kybStatus === 'IN_REVIEW'">
          Your KYB submission is under review. We'll notify you once approved.
        </template>
      </div>

      <!-- Header -->
      <div v-if="authStore.isAuthenticated" class="flex items-center justify-end px-6 py-3 border-b border-surface-800">
        <div class="flex items-center gap-3">
          <div class="w-8 h-8 rounded-full bg-violet-600/20 text-violet-400 flex items-center justify-center text-xs font-semibold">
            {{ (authStore.displayName || authStore.userEmail || '?').charAt(0).toUpperCase() }}
          </div>
          <span class="text-sm text-surface-400">{{ authStore.displayName || authStore.userEmail }}</span>
          <button
            @click="handleLogout"
            class="text-sm text-surface-500 hover:text-surface-300 transition-colors"
          >
            Logout
          </button>
        </div>
      </div>

      <div class="p-6 max-w-[1600px] mx-auto">
        <slot />
      </div>
    </main>
  </div>
</template>

<script setup lang="ts">
const authStore = useAuthStore()
const router = useRouter()

function handleLogout() {
  authStore.logout()
  router.push('/auth/login')
}
</script>
