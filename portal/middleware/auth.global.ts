export default defineNuxtRouteMiddleware(async (to) => {
  // Skip auth check for auth pages and public payment pages
  if (to.path.startsWith('/auth/') || to.path.startsWith('/p/')) {
    return
  }

  const authStore = useAuthStore()

  // Restore session from httpOnly cookie on first navigation / page reload
  if (!authStore.isAuthenticated) {
    await authStore.restoreSession()
  }

  // Redirect unauthenticated users to login
  if (!authStore.isAuthenticated) {
    return navigateTo('/auth/login')
  }

  // Redirect ONBOARDING tenants with PENDING KYB to KYB form
  if (
    authStore.tenantStatus === 'ONBOARDING' &&
    authStore.kybStatus === 'PENDING' &&
    to.path !== '/onboarding/kyb'
  ) {
    return navigateTo('/onboarding/kyb')
  }
})
