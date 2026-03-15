import { defineStore } from 'pinia'
import type { TenantProfile } from '~/types'

export const useAuthStore = defineStore('auth', {
  state: () => ({
    tenant: null as TenantProfile | null,
    isAuthenticated: false,
    loading: false,
    error: null as string | null,
  }),

  getters: {
    tenantName: (state) => state.tenant?.name ?? '',
    tenantSlug: (state) => state.tenant?.slug ?? '',
    tenantStatus: (state) => state.tenant?.status ?? 'ONBOARDING',
    isActive: (state) => state.tenant?.status === 'ACTIVE' && state.tenant?.kyb_status === 'VERIFIED',
  },

  actions: {
    async fetchProfile() {
      this.loading = true
      this.error = null
      try {
        const api = usePortalApi()
        this.tenant = await api.getProfile()
        this.isAuthenticated = true
      } catch (e) {
        this.error = e instanceof Error ? e.message : 'Failed to load profile'
        this.isAuthenticated = false
      } finally {
        this.loading = false
      }
    },
  },
})
