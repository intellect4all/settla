import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'

// Mock usePortalApi — returns a mock API object
const mockLogin = vi.fn()
const mockRegister = vi.fn()
const mockGetProfile = vi.fn()
const mockRefreshAccessToken = vi.fn()

vi.stubGlobal('usePortalApi', () => ({
  login: mockLogin,
  register: mockRegister,
  getProfile: mockGetProfile,
  refreshAccessToken: mockRefreshAccessToken,
}))

// Mock import.meta.client — in tests we run in Node, so simulate client
vi.stubGlobal('sessionStorage', {
  _store: {} as Record<string, string>,
  getItem(key: string) { return this._store[key] ?? null },
  setItem(key: string, value: string) { this._store[key] = value },
  removeItem(key: string) { delete this._store[key] },
  clear() { this._store = {} },
})

// Mock useRuntimeConfig for usePortalApi (not directly used by auth store, but needed)
vi.stubGlobal('useRuntimeConfig', () => ({
  public: {
    apiBase: 'http://localhost:3100',
    portalApiKey: '',
  },
}))

// Mock navigateTo (Nuxt auto-import)
vi.stubGlobal('navigateTo', vi.fn())

import { useAuthStore } from '../../stores/auth'

const mockUser = {
  id: 'user-1',
  email: 'alice@acme.com',
  display_name: 'Alice',
  role: 'OWNER' as const,
  tenant_id: 'tenant-1',
  tenant_name: 'Acme Fintech',
  tenant_slug: 'acme',
  tenant_status: 'ACTIVE' as const,
  kyb_status: 'VERIFIED' as const,
}

describe('auth store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    ;(globalThis as any).sessionStorage.clear()
  })

  describe('initial state', () => {
    it('should start unauthenticated with null tokens', () => {
      const store = useAuthStore()
      expect(store.isAuthenticated).toBe(false)
      expect(store.accessToken).toBeNull()
      expect(store.refreshToken).toBeNull()
      expect(store.user).toBeNull()
      expect(store.tenant).toBeNull()
      expect(store.loading).toBe(false)
      expect(store.error).toBeNull()
    })
  })

  describe('getters', () => {
    it('should return empty defaults when user is null', () => {
      const store = useAuthStore()
      expect(store.tenantName).toBe('')
      expect(store.tenantSlug).toBe('')
      expect(store.tenantStatus).toBe('ONBOARDING')
      expect(store.kybStatus).toBe('PENDING')
      expect(store.isActive).toBe(false)
      expect(store.userRole).toBe('MEMBER')
      expect(store.userEmail).toBe('')
      expect(store.displayName).toBe('')
    })

    it('should derive values from user when set', () => {
      const store = useAuthStore()
      store.user = mockUser
      expect(store.tenantName).toBe('Acme Fintech')
      expect(store.tenantSlug).toBe('acme')
      expect(store.tenantStatus).toBe('ACTIVE')
      expect(store.kybStatus).toBe('VERIFIED')
      expect(store.userRole).toBe('OWNER')
      expect(store.userEmail).toBe('alice@acme.com')
      expect(store.displayName).toBe('Alice')
    })

    it('isActive should be true only when tenant is ACTIVE and KYB is VERIFIED', () => {
      const store = useAuthStore()
      store.user = { ...mockUser, tenant_status: 'ACTIVE', kyb_status: 'VERIFIED' }
      expect(store.isActive).toBe(true)
    })

    it('isActive should be false when tenant is ACTIVE but KYB is PENDING', () => {
      const store = useAuthStore()
      store.user = { ...mockUser, tenant_status: 'ACTIVE', kyb_status: 'PENDING' }
      expect(store.isActive).toBe(false)
    })

    it('isActive should be false when KYB is VERIFIED but tenant is SUSPENDED', () => {
      const store = useAuthStore()
      store.user = { ...mockUser, tenant_status: 'SUSPENDED', kyb_status: 'VERIFIED' }
      expect(store.isActive).toBe(false)
    })
  })

  describe('login', () => {
    it('should store tokens and user on successful login', async () => {
      mockLogin.mockResolvedValueOnce({
        access_token: 'at-123',
        refresh_token: 'rt-456',
        expires_in: 3600,
        user: mockUser,
      })
      const store = useAuthStore()
      await store.login('alice@acme.com', 'password123')

      expect(store.accessToken).toBe('at-123')
      expect(store.refreshToken).toBe('rt-456')
      expect(store.user).toEqual(mockUser)
      expect(store.isAuthenticated).toBe(true)
      expect(store.loading).toBe(false)
      expect(store.error).toBeNull()
    })

    it('should call login API with email and password', async () => {
      mockLogin.mockResolvedValueOnce({
        access_token: 'at', refresh_token: 'rt', expires_in: 3600, user: mockUser,
      })
      const store = useAuthStore()
      await store.login('bob@acme.com', 'secret')
      expect(mockLogin).toHaveBeenCalledWith('bob@acme.com', 'secret')
    })

    it('should set loading=true during login', async () => {
      let loadingDuringCall = false
      mockLogin.mockImplementationOnce(async () => {
        const store = useAuthStore()
        loadingDuringCall = store.loading
        return { access_token: 'at', refresh_token: 'rt', expires_in: 3600, user: mockUser }
      })
      const store = useAuthStore()
      await store.login('a@b.com', 'pass')
      expect(loadingDuringCall).toBe(true)
      expect(store.loading).toBe(false)
    })

    it('should set error and rethrow on login failure', async () => {
      const err = new Error('Invalid credentials')
      mockLogin.mockRejectedValueOnce(err)
      const store = useAuthStore()

      await expect(store.login('a@b.com', 'wrong')).rejects.toThrow('Invalid credentials')
      expect(store.error).toBe('Invalid credentials')
      expect(store.isAuthenticated).toBe(false)
      expect(store.loading).toBe(false)
    })

    it('should extract message from error.data.message if available', async () => {
      const err = { data: { message: 'Account locked' }, message: 'HTTP 403' }
      mockLogin.mockRejectedValueOnce(err)
      const store = useAuthStore()

      await expect(store.login('a@b.com', 'x')).rejects.toBe(err)
      expect(store.error).toBe('Account locked')
    })

    it('should persist session to sessionStorage after login', async () => {
      mockLogin.mockResolvedValueOnce({
        access_token: 'at-123', refresh_token: 'rt-456', expires_in: 3600, user: mockUser,
      })
      const store = useAuthStore()
      await store.login('a@b.com', 'pass')

      const raw = sessionStorage.getItem('settla_session')
      expect(raw).toBeTruthy()
      const session = JSON.parse(raw!)
      expect(session.accessToken).toBe('at-123')
      expect(session.refreshToken).toBe('rt-456')
      expect(session.user).toEqual(mockUser)
    })
  })

  describe('register', () => {
    it('should call register API and return response', async () => {
      const regResponse = { tenant_id: 't-1', user_id: 'u-1', email: 'a@b.com', message: 'Check email' }
      mockRegister.mockResolvedValueOnce(regResponse)
      const store = useAuthStore()
      const result = await store.register('Acme', 'a@b.com', 'pass', 'Alice')

      expect(mockRegister).toHaveBeenCalledWith('Acme', 'a@b.com', 'pass', 'Alice')
      expect(result).toEqual(regResponse)
      // Register does NOT authenticate
      expect(store.isAuthenticated).toBe(false)
    })

    it('should set error on registration failure', async () => {
      mockRegister.mockRejectedValueOnce(new Error('Email taken'))
      const store = useAuthStore()
      await expect(store.register('Acme', 'a@b.com', 'pass')).rejects.toThrow('Email taken')
      expect(store.error).toBe('Email taken')
      expect(store.loading).toBe(false)
    })
  })

  describe('logout', () => {
    it('should clear all auth state', async () => {
      // First login
      mockLogin.mockResolvedValueOnce({
        access_token: 'at', refresh_token: 'rt', expires_in: 3600, user: mockUser,
      })
      const store = useAuthStore()
      await store.login('a@b.com', 'pass')
      expect(store.isAuthenticated).toBe(true)

      // Then logout
      store.logout()
      expect(store.accessToken).toBeNull()
      expect(store.refreshToken).toBeNull()
      expect(store.user).toBeNull()
      expect(store.tenant).toBeNull()
      expect(store.isAuthenticated).toBe(false)
      expect(store.error).toBeNull()
    })

    it('should remove session from sessionStorage', async () => {
      mockLogin.mockResolvedValueOnce({
        access_token: 'at', refresh_token: 'rt', expires_in: 3600, user: mockUser,
      })
      const store = useAuthStore()
      await store.login('a@b.com', 'pass')
      expect(sessionStorage.getItem('settla_session')).toBeTruthy()

      store.logout()
      expect(sessionStorage.getItem('settla_session')).toBeNull()
      expect(sessionStorage.getItem('settla_user')).toBeNull()
    })
  })

  describe('fetchProfile', () => {
    it('should store tenant profile on success', async () => {
      const profile = {
        id: 'tenant-1', name: 'Acme', slug: 'acme', status: 'ACTIVE',
        settlement_model: 'PREFUNDED', kyb_status: 'VERIFIED',
        fee_schedule: { on_ramp_bps: 40, off_ramp_bps: 35, min_fee_usd: '0.5', max_fee_usd: '100' },
        daily_limit_usd: '50000', per_transfer_limit: '10000',
        created_at: '2024-01-01', updated_at: '2024-01-01',
      }
      mockGetProfile.mockResolvedValueOnce(profile)
      const store = useAuthStore()
      await store.fetchProfile()
      expect(store.tenant).toEqual(profile)
      expect(store.loading).toBe(false)
    })

    it('should set error on profile fetch failure', async () => {
      mockGetProfile.mockRejectedValueOnce(new Error('Forbidden'))
      const store = useAuthStore()
      await store.fetchProfile()
      expect(store.error).toBe('Forbidden')
      expect(store.loading).toBe(false)
    })
  })

  describe('refreshSession', () => {
    it('should update access token on successful refresh', async () => {
      const store = useAuthStore()
      store.refreshToken = 'rt-old'
      store.accessToken = 'at-old'
      store.user = mockUser

      mockRefreshAccessToken.mockResolvedValueOnce({
        access_token: 'at-new',
        expires_in: 3600,
      })

      await store.refreshSession()
      expect(store.accessToken).toBe('at-new')
      expect(mockRefreshAccessToken).toHaveBeenCalledWith('rt-old')
    })

    it('should logout when no refresh token exists', async () => {
      const store = useAuthStore()
      store.accessToken = 'at-stale'
      store.refreshToken = null

      await store.refreshSession()
      expect(store.accessToken).toBeNull()
      expect(store.isAuthenticated).toBe(false)
    })

    it('should logout when refresh API call fails', async () => {
      const store = useAuthStore()
      store.refreshToken = 'rt-expired'
      store.accessToken = 'at-old'
      store.user = mockUser
      store.isAuthenticated = true

      mockRefreshAccessToken.mockRejectedValueOnce(new Error('Token expired'))

      await store.refreshSession()
      expect(store.accessToken).toBeNull()
      expect(store.isAuthenticated).toBe(false)
    })
  })

  describe('token storage (memory-only, no sessionStorage)', () => {
    it('should NOT persist tokens to sessionStorage', () => {
      const store = useAuthStore()
      store.accessToken = 'at-1'
      store.refreshToken = 'rt-1'
      store.user = mockUser

      // Tokens are memory-only — sessionStorage should be empty
      expect(sessionStorage.getItem('settla_session')).toBeNull()
      expect(sessionStorage.getItem('settla_user')).toBeNull()
    })

    it('should keep tokens in reactive state (memory)', () => {
      const store = useAuthStore()
      store.accessToken = 'at-mem'
      store.refreshToken = 'rt-mem'
      expect(store.accessToken).toBe('at-mem')
      expect(store.refreshToken).toBe('rt-mem')
      expect(store.isAuthenticated).toBe(true)
    })
  })

  describe('restoreSession', () => {
    it('should be a no-op stub (tokens are memory-only)', async () => {
      const store = useAuthStore()
      await store.restoreSession()
      // restoreSession is now a no-op — tokens don't survive page reload
      expect(store.isAuthenticated).toBe(false)
      expect(store.accessToken).toBeNull()
    })
  })
})
