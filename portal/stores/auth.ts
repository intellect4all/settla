import { defineStore } from 'pinia'
import type { PortalUser, TenantProfile } from '~/types'

interface AuthState {
  user: PortalUser | null
  tenant: TenantProfile | null
  isAuthenticated: boolean
  loading: boolean
  error: string | null
}

export const useAuthStore = defineStore('auth', {
  state: (): AuthState => ({
    user: null,
    tenant: null,
    isAuthenticated: false,
    loading: false,
    error: null,
  }),

  getters: {
    tenantName: (state) => state.user?.tenant_name ?? '',
    tenantSlug: (state) => state.user?.tenant_slug ?? '',
    tenantStatus: (state) => state.user?.tenant_status ?? 'ONBOARDING',
    kybStatus: (state) => state.user?.kyb_status ?? 'PENDING',
    isActive: (state) => state.user?.tenant_status === 'ACTIVE' && state.user?.kyb_status === 'VERIFIED',
    userRole: (state) => state.user?.role ?? 'MEMBER',
    userEmail: (state) => state.user?.email ?? '',
    displayName: (state) => state.user?.display_name ?? '',
  },

  actions: {
    async login(email: string, password: string) {
      this.loading = true
      this.error = null
      try {
        const api = usePortalApi()
        const res = await api.login(email, password)
        this.user = res.user
        this.tenant = res.tenant ?? null
        this.isAuthenticated = true
      } catch (e: any) {
        this.error = e?.data?.message || e?.message || 'Login failed'
        throw e
      } finally {
        this.loading = false
      }
    },

    async register(companyName: string, email: string, password: string, displayName?: string) {
      this.loading = true
      this.error = null
      try {
        const api = usePortalApi()
        return await api.register(companyName, email, password, displayName)
      } catch (e: any) {
        this.error = e?.data?.message || e?.message || 'Registration failed'
        throw e
      } finally {
        this.loading = false
      }
    },

    async fetchProfile() {
      this.loading = true
      try {
        const api = usePortalApi()
        this.tenant = await api.getProfile()
      } catch (e: any) {
        this.error = e?.data?.message || e?.message || 'Failed to load profile'
      } finally {
        this.loading = false
      }
    },

    async logout() {
      try {
        await $fetch('/api/auth/logout', { method: 'POST' })
      } catch {
        // Best-effort — clear local state regardless
      }
      this.user = null
      this.tenant = null
      this.isAuthenticated = false
      this.error = null
    },

    async refreshSession() {
      try {
        const res = await $fetch<any>('/api/auth/refresh', { method: 'POST' })
        if (res.user) {
          this.user = res.user
          this.isAuthenticated = true
        }
      } catch {
        this.user = null
        this.tenant = null
        this.isAuthenticated = false
      }
    },

    /** Restore session on app mount / page reload.
     *  Calls the server-side session endpoint which reads the httpOnly cookie
     *  and returns user data without exposing tokens to client JS. */
    async restoreSession() {
      try {
        const res = await $fetch<{ authenticated: boolean; user: any }>('/api/auth/session')
        if (res.authenticated && res.user) {
          this.user = res.user
          this.isAuthenticated = true
        } else {
          this.isAuthenticated = false
        }
      } catch {
        this.isAuthenticated = false
      }
    },
  },
})
