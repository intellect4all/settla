import { describe, it, expect, vi, beforeEach } from 'vitest'

// Mock Nuxt/Pinia auto-imports
const mockFetch = vi.fn()
const mockRuntimeConfig = {
  public: {
    apiBase: 'http://localhost:3100',
    portalApiKey: 'sk_portal_fallback',
  },
}

let mockAccessToken: string | null = 'jwt-access-token-123'

vi.stubGlobal('useRuntimeConfig', () => mockRuntimeConfig)
vi.stubGlobal('$fetch', mockFetch)
vi.stubGlobal('navigateTo', vi.fn())

// Mock useAuthStore — the portal API reads the access token from the auth store
vi.mock('../../stores/auth', () => ({
  useAuthStore: () => ({
    accessToken: mockAccessToken,
    refreshSession: vi.fn(),
    logout: vi.fn(),
  }),
}))

import { usePortalApi } from '../../composables/usePortalApi'

describe('usePortalApi', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockAccessToken = 'jwt-access-token-123'
    mockRuntimeConfig.public.apiBase = 'http://localhost:3100'
    mockRuntimeConfig.public.portalApiKey = 'sk_portal_fallback'
  })

  describe('authenticated requests', () => {
    it('should include Authorization header with access token from auth store', async () => {
      mockFetch.mockResolvedValueOnce({ id: 'tenant-1', name: 'Acme' })
      const api = usePortalApi()
      await api.getProfile()
      expect(mockFetch).toHaveBeenCalledWith(
        'http://localhost:3100/v1/me',
        expect.objectContaining({
          headers: expect.objectContaining({
            Authorization: 'Bearer jwt-access-token-123',
          }),
        }),
      )
    })

    it('should fall back to portalApiKey when auth store has no token', async () => {
      mockAccessToken = null
      mockFetch.mockResolvedValueOnce({ id: 'tenant-1' })
      const api = usePortalApi()
      await api.getProfile()
      expect(mockFetch).toHaveBeenCalledWith(
        expect.any(String),
        expect.objectContaining({
          headers: expect.objectContaining({
            Authorization: 'Bearer sk_portal_fallback',
          }),
        }),
      )
    })

    it('should use correct base URL', async () => {
      mockRuntimeConfig.public.apiBase = 'https://api.settla.io'
      mockFetch.mockResolvedValueOnce({ keys: [] })
      const api = usePortalApi()
      await api.listApiKeys()
      expect(mockFetch).toHaveBeenCalledWith(
        'https://api.settla.io/v1/me/api-keys',
        expect.anything(),
      )
    })

    it('should set Content-Type header', async () => {
      mockFetch.mockResolvedValueOnce({})
      const api = usePortalApi()
      await api.getProfile()
      expect(mockFetch).toHaveBeenCalledWith(
        expect.any(String),
        expect.objectContaining({
          headers: expect.objectContaining({
            'Content-Type': 'application/json',
          }),
        }),
      )
    })

    it('should pass AbortSignal for timeout control', async () => {
      mockFetch.mockResolvedValueOnce({})
      const api = usePortalApi()
      await api.getProfile()
      expect(mockFetch).toHaveBeenCalledWith(
        expect.any(String),
        expect.objectContaining({
          signal: expect.any(AbortSignal),
        }),
      )
    })
  })

  describe('public requests (no auth)', () => {
    it('login should NOT include Authorization header', async () => {
      mockFetch.mockResolvedValueOnce({
        access_token: 'at', refresh_token: 'rt', expires_in: 3600,
        user: { id: 'u-1', email: 'a@b.com' },
      })
      const api = usePortalApi()
      await api.login('a@b.com', 'pass')
      expect(mockFetch).toHaveBeenCalledWith(
        'http://localhost:3100/v1/auth/login',
        expect.objectContaining({
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
          },
        }),
      )
    })

    it('login should POST email and password', async () => {
      mockFetch.mockResolvedValueOnce({ access_token: 'at', refresh_token: 'rt', expires_in: 3600, user: {} })
      const api = usePortalApi()
      await api.login('alice@acme.com', 'secret123')
      expect(mockFetch).toHaveBeenCalledWith(
        expect.any(String),
        expect.objectContaining({
          body: JSON.stringify({ email: 'alice@acme.com', password: 'secret123' }),
        }),
      )
    })

    it('register should POST company details', async () => {
      mockFetch.mockResolvedValueOnce({ tenant_id: 't-1', user_id: 'u-1', email: 'a@b.com', message: 'ok' })
      const api = usePortalApi()
      await api.register('Acme Corp', 'a@b.com', 'pass', 'Alice')
      expect(mockFetch).toHaveBeenCalledWith(
        'http://localhost:3100/v1/auth/register',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify({
            company_name: 'Acme Corp',
            email: 'a@b.com',
            password: 'pass',
            display_name: 'Alice',
          }),
        }),
      )
    })

    it('verifyEmail should POST token', async () => {
      mockFetch.mockResolvedValueOnce({ message: 'Verified' })
      const api = usePortalApi()
      await api.verifyEmail('verify-token-abc')
      expect(mockFetch).toHaveBeenCalledWith(
        'http://localhost:3100/v1/auth/verify-email',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify({ token: 'verify-token-abc' }),
        }),
      )
    })

    it('refreshAccessToken should POST refresh token', async () => {
      mockFetch.mockResolvedValueOnce({ access_token: 'new-at', expires_in: 3600 })
      const api = usePortalApi()
      await api.refreshAccessToken('rt-123')
      expect(mockFetch).toHaveBeenCalledWith(
        'http://localhost:3100/v1/auth/refresh',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify({ refresh_token: 'rt-123' }),
        }),
      )
    })

    it('resolvePaymentLink should use public request (no auth header)', async () => {
      mockFetch.mockResolvedValueOnce({ link: { id: 'pl-1' } })
      const api = usePortalApi()
      await api.resolvePaymentLink('ABC123')
      expect(mockFetch).toHaveBeenCalledWith(
        'http://localhost:3100/v1/payment-links/resolve/ABC123',
        expect.objectContaining({
          headers: { 'Content-Type': 'application/json' },
        }),
      )
    })
  })

  describe('API key management', () => {
    it('createApiKey should POST environment and name', async () => {
      mockFetch.mockResolvedValueOnce({ key: { id: 'k-1' }, raw_key: 'sk_live_xxx' })
      const api = usePortalApi()
      await api.createApiKey('LIVE', 'Production Key')
      expect(mockFetch).toHaveBeenCalledWith(
        'http://localhost:3100/v1/me/api-keys',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify({ environment: 'LIVE', name: 'Production Key' }),
        }),
      )
    })

    it('revokeApiKey should DELETE by key ID', async () => {
      mockFetch.mockResolvedValueOnce(undefined)
      const api = usePortalApi()
      await api.revokeApiKey('key-123')
      expect(mockFetch).toHaveBeenCalledWith(
        'http://localhost:3100/v1/me/api-keys/key-123',
        expect.objectContaining({ method: 'DELETE' }),
      )
    })

    it('rotateApiKey should POST to rotate endpoint', async () => {
      mockFetch.mockResolvedValueOnce({ key: { id: 'k-1' }, raw_key: 'sk_live_new' })
      const api = usePortalApi()
      await api.rotateApiKey('key-123', 'Rotated Key')
      expect(mockFetch).toHaveBeenCalledWith(
        'http://localhost:3100/v1/me/api-keys/key-123/rotate',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify({ name: 'Rotated Key' }),
        }),
      )
    })
  })

  describe('transfers', () => {
    it('listTransfers should build query string', async () => {
      mockFetch.mockResolvedValueOnce({ transfers: [] })
      const api = usePortalApi()
      await api.listTransfers({ page_size: 10, page_token: 'next-page' })
      expect(mockFetch).toHaveBeenCalledWith(
        expect.stringContaining('page_size=10'),
        expect.anything(),
      )
      expect(mockFetch).toHaveBeenCalledWith(
        expect.stringContaining('page_token=next-page'),
        expect.anything(),
      )
    })

    it('listTransfers should omit query when no params', async () => {
      mockFetch.mockResolvedValueOnce({ transfers: [] })
      const api = usePortalApi()
      await api.listTransfers()
      expect(mockFetch).toHaveBeenCalledWith(
        'http://localhost:3100/v1/transfers',
        expect.anything(),
      )
    })

    it('getTransfer should request by ID', async () => {
      mockFetch.mockResolvedValueOnce({ id: 'txn-1', status: 'COMPLETED' })
      const api = usePortalApi()
      await api.getTransfer('txn-1')
      expect(mockFetch).toHaveBeenCalledWith(
        'http://localhost:3100/v1/transfers/txn-1',
        expect.anything(),
      )
    })
  })

  describe('deposits', () => {
    it('createDeposit should POST deposit data', async () => {
      mockFetch.mockResolvedValueOnce({ session: { id: 'd-1' } })
      const api = usePortalApi()
      await api.createDeposit({
        chain: 'tron',
        token: 'USDT',
        expected_amount: '1000',
      })
      expect(mockFetch).toHaveBeenCalledWith(
        'http://localhost:3100/v1/deposits',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify({
            chain: 'tron',
            token: 'USDT',
            expected_amount: '1000',
          }),
        }),
      )
    })

    it('cancelDeposit should POST to cancel endpoint', async () => {
      mockFetch.mockResolvedValueOnce({ session: { id: 'd-1', status: 'CANCELLED' } })
      const api = usePortalApi()
      await api.cancelDeposit('d-1')
      expect(mockFetch).toHaveBeenCalledWith(
        'http://localhost:3100/v1/deposits/d-1/cancel',
        expect.objectContaining({ method: 'POST' }),
      )
    })
  })

  describe('payment links', () => {
    it('createPaymentLink should POST link data', async () => {
      mockFetch.mockResolvedValueOnce({ link: { id: 'pl-1' } })
      const api = usePortalApi()
      await api.createPaymentLink({
        amount: '50',
        currency: 'USD',
        chain: 'tron',
        token: 'USDT',
        description: 'Invoice #123',
      })
      expect(mockFetch).toHaveBeenCalledWith(
        'http://localhost:3100/v1/payment-links',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify({
            amount: '50',
            currency: 'USD',
            chain: 'tron',
            token: 'USDT',
            description: 'Invoice #123',
          }),
        }),
      )
    })

    it('disablePaymentLink should send DELETE', async () => {
      mockFetch.mockResolvedValueOnce(undefined)
      const api = usePortalApi()
      await api.disablePaymentLink('pl-1')
      expect(mockFetch).toHaveBeenCalledWith(
        'http://localhost:3100/v1/payment-links/pl-1',
        expect.objectContaining({ method: 'DELETE' }),
      )
    })

    it('redeemPaymentLink should use public request', async () => {
      mockFetch.mockResolvedValueOnce({ session: {}, link: {} })
      const api = usePortalApi()
      await api.redeemPaymentLink('CODE123')
      expect(mockFetch).toHaveBeenCalledWith(
        'http://localhost:3100/v1/payment-links/redeem/CODE123',
        expect.objectContaining({
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
        }),
      )
    })
  })

  describe('analytics', () => {
    it('getTransferStats should include period and granularity', async () => {
      mockFetch.mockResolvedValueOnce({ buckets: [] })
      const api = usePortalApi()
      await api.getTransferStats('7d', 'day')
      expect(mockFetch).toHaveBeenCalledWith(
        'http://localhost:3100/v1/me/transfers/stats?period=7d&granularity=day',
        expect.anything(),
      )
    })

    it('getTransferStats should default to 24h and hour', async () => {
      mockFetch.mockResolvedValueOnce({ buckets: [] })
      const api = usePortalApi()
      await api.getTransferStats()
      expect(mockFetch).toHaveBeenCalledWith(
        'http://localhost:3100/v1/me/transfers/stats?period=24h&granularity=hour',
        expect.anything(),
      )
    })

    it('getFeeReport should include date range params', async () => {
      mockFetch.mockResolvedValueOnce({})
      const api = usePortalApi()
      await api.getFeeReport('2024-01-01', '2024-01-31')
      expect(mockFetch).toHaveBeenCalledWith(
        expect.stringContaining('from=2024-01-01'),
        expect.anything(),
      )
      expect(mockFetch).toHaveBeenCalledWith(
        expect.stringContaining('to=2024-01-31'),
        expect.anything(),
      )
    })

    it('getFeeReport should work without date params', async () => {
      mockFetch.mockResolvedValueOnce({})
      const api = usePortalApi()
      await api.getFeeReport()
      expect(mockFetch).toHaveBeenCalledWith(
        'http://localhost:3100/v1/me/fees/report',
        expect.anything(),
      )
    })
  })

  describe('webhook management', () => {
    it('updateWebhook should PUT webhook URL', async () => {
      mockFetch.mockResolvedValueOnce({ webhook_url: 'https://example.com/hook', webhook_secret: 's-123' })
      const api = usePortalApi()
      await api.updateWebhook('https://example.com/hook')
      expect(mockFetch).toHaveBeenCalledWith(
        'http://localhost:3100/v1/me/webhooks',
        expect.objectContaining({
          method: 'PUT',
          body: JSON.stringify({ webhook_url: 'https://example.com/hook' }),
        }),
      )
    })

    it('testWebhook should POST without body', async () => {
      mockFetch.mockResolvedValueOnce({ success: true })
      const api = usePortalApi()
      await api.testWebhook()
      expect(mockFetch).toHaveBeenCalledWith(
        'http://localhost:3100/v1/me/webhooks/test',
        expect.objectContaining({ method: 'POST' }),
      )
    })

    it('listWebhookDeliveries should build query from params', async () => {
      mockFetch.mockResolvedValueOnce({ deliveries: [], total_count: 0 })
      const api = usePortalApi()
      await api.listWebhookDeliveries({ event_type: 'transfer.completed', status: 'DELIVERED', page_size: 50 })
      const url = mockFetch.mock.calls[0][0] as string
      expect(url).toContain('event_type=transfer.completed')
      expect(url).toContain('status=DELIVERED')
      expect(url).toContain('page_size=50')
    })
  })

  describe('KYB submission', () => {
    it('submitKYB should POST company verification data', async () => {
      mockFetch.mockResolvedValueOnce({ message: 'KYB submitted', kyb_status: 'IN_REVIEW' })
      const api = usePortalApi()
      const kybData = {
        company_registration_number: 'REG-123',
        country: 'GB',
        business_type: 'FINTECH',
        contact_name: 'Alice',
        contact_email: 'alice@acme.com',
      }
      await api.submitKYB(kybData)
      expect(mockFetch).toHaveBeenCalledWith(
        'http://localhost:3100/v1/me/kyb',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify(kybData),
        }),
      )
    })
  })

  describe('error handling', () => {
    it('should propagate network errors', async () => {
      mockFetch.mockRejectedValueOnce(new Error('Connection refused'))
      const api = usePortalApi()
      await expect(api.getProfile()).rejects.toThrow('Connection refused')
    })
  })

  describe('return value shape', () => {
    it('should expose all expected API methods', () => {
      const api = usePortalApi()
      const expectedMethods = [
        'getProfile', 'updateWebhook',
        'listApiKeys', 'createApiKey', 'revokeApiKey', 'rotateApiKey',
        'getDashboard', 'getTransferStats', 'getFeeReport',
        'listTransfers', 'getTransfer', 'getTransferEvents',
        'getPositions',
        'listWebhookDeliveries', 'getWebhookDelivery', 'getWebhookDeliveryStats',
        'listWebhookSubscriptions', 'updateWebhookSubscriptions', 'testWebhook',
        'getStatusDistribution', 'getCorridorMetrics', 'getLatencyPercentiles',
        'getVolumeComparison', 'getRecentActivity',
        'listDeposits', 'getDeposit', 'createDeposit', 'cancelDeposit',
        'getCryptoSettings', 'updateCryptoSettings',
        'getCryptoBalances', 'convertCryptoBalance',
        'getFeeBreakdown', 'getProviderPerformance', 'getDepositAnalytics',
        'getReconciliationSummary', 'createExportJob', 'getExportJob',
        'listPaymentLinks', 'createPaymentLink', 'disablePaymentLink',
        'resolvePaymentLink', 'redeemPaymentLink', 'getDepositPublicStatus',
        'register', 'login', 'verifyEmail', 'refreshAccessToken', 'submitKYB',
      ]
      for (const method of expectedMethods) {
        expect(typeof (api as any)[method]).toBe('function')
      }
    })
  })
})
