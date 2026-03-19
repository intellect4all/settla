import { describe, it, expect, vi, beforeEach } from 'vitest'

// Mock Nuxt auto-imports that useApi depends on
const mockFetch = vi.fn()

vi.stubGlobal('useRuntimeConfig', () => ({}))
vi.stubGlobal('$fetch', mockFetch)

// Import after mocking globals
import { useApi } from '../../composables/useApi'

describe('useApi', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  describe('request (proxied via /api)', () => {
    it('should NOT include Authorization header (keys are server-side)', async () => {
      mockFetch.mockResolvedValueOnce({ status: 'ok' })
      const api = useApi()
      await api.checkHealth()
      const callHeaders = mockFetch.mock.calls[0][1].headers
      expect(callHeaders).not.toHaveProperty('Authorization')
    })

    it('should use /api as the base URL', async () => {
      mockFetch.mockResolvedValueOnce({ positions: [] })
      const api = useApi()
      await api.getPositions()
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/v1/treasury/positions',
        expect.anything(),
      )
    })

    it('should set Content-Type to application/json', async () => {
      mockFetch.mockResolvedValueOnce({ status: 'ok' })
      const api = useApi()
      await api.checkHealth()
      expect(mockFetch).toHaveBeenCalledWith(
        expect.any(String),
        expect.objectContaining({
          headers: expect.objectContaining({
            'Content-Type': 'application/json',
          }),
        }),
      )
    })

    it('should use GET method by default', async () => {
      mockFetch.mockResolvedValueOnce({ positions: [] })
      const api = useApi()
      await api.getPositions()
      expect(mockFetch).toHaveBeenCalledWith(
        expect.any(String),
        expect.objectContaining({ method: 'GET' }),
      )
    })

    it('should use POST method for mutations', async () => {
      mockFetch.mockResolvedValueOnce({ id: 'q1', rate: '1.25' })
      const api = useApi()
      await api.createQuote({
        source_currency: 'GBP',
        source_amount: '1000',
        dest_currency: 'NGN',
      })
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/v1/quotes',
        expect.objectContaining({ method: 'POST' }),
      )
    })

    it('should JSON-stringify the body for POST requests', async () => {
      mockFetch.mockResolvedValueOnce({ id: 'q1' })
      const api = useApi()
      const body = {
        source_currency: 'GBP',
        source_amount: '500',
        dest_currency: 'NGN',
      }
      await api.createQuote(body)
      expect(mockFetch).toHaveBeenCalledWith(
        expect.any(String),
        expect.objectContaining({ body: JSON.stringify(body) }),
      )
    })

    it('should pass an AbortSignal for timeout', async () => {
      mockFetch.mockResolvedValueOnce({ status: 'ok' })
      const api = useApi()
      await api.checkHealth()
      expect(mockFetch).toHaveBeenCalledWith(
        expect.any(String),
        expect.objectContaining({
          signal: expect.any(AbortSignal),
        }),
      )
    })

    it('should propagate network errors', async () => {
      mockFetch.mockRejectedValueOnce(new Error('Network error'))
      const api = useApi()
      await expect(api.checkHealth()).rejects.toThrow('Network error')
    })

    it('should propagate HTTP errors', async () => {
      const httpError = new Error('Internal Server Error')
      ;(httpError as any).response = { status: 500 }
      mockFetch.mockRejectedValueOnce(httpError)
      const api = useApi()
      await expect(api.getPositions()).rejects.toThrow('Internal Server Error')
    })
  })

  describe('opsRequest (proxied via /api — server adds ops key)', () => {
    it('should NOT include X-Ops-Api-Key header (keys are server-side)', async () => {
      mockFetch.mockResolvedValueOnce({ tenants: [] })
      const api = useApi()
      await api.listTenants()
      const callHeaders = mockFetch.mock.calls[0][1].headers
      expect(callHeaders).not.toHaveProperty('X-Ops-Api-Key')
    })

    it('should route ops requests through /api proxy', async () => {
      mockFetch.mockResolvedValueOnce({ tenants: [] })
      const api = useApi()
      await api.listTenants()
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/v1/ops/tenants',
        expect.anything(),
      )
    })
  })

  describe('listTransfers', () => {
    it('should build query string from params', async () => {
      mockFetch.mockResolvedValueOnce({ transfers: [], next_page_token: '' })
      const api = useApi()
      await api.listTransfers({ page_size: 25, status: 'COMPLETED' })
      expect(mockFetch).toHaveBeenCalledWith(
        expect.stringContaining('page_size=25'),
        expect.anything(),
      )
      expect(mockFetch).toHaveBeenCalledWith(
        expect.stringContaining('status=COMPLETED'),
        expect.anything(),
      )
    })

    it('should omit query string when no params given', async () => {
      mockFetch.mockResolvedValueOnce({ transfers: [] })
      const api = useApi()
      await api.listTransfers()
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/v1/transfers',
        expect.anything(),
      )
    })

    it('should pass page_token for pagination', async () => {
      mockFetch.mockResolvedValueOnce({ transfers: [] })
      const api = useApi()
      await api.listTransfers({ page_token: 'abc123' })
      expect(mockFetch).toHaveBeenCalledWith(
        expect.stringContaining('page_token=abc123'),
        expect.anything(),
      )
    })
  })

  describe('getTransfer', () => {
    it('should call the correct endpoint with transfer ID', async () => {
      mockFetch.mockResolvedValueOnce({ id: 'txn-1' })
      const api = useApi()
      await api.getTransfer('txn-1')
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/v1/transfers/txn-1',
        expect.anything(),
      )
    })
  })

  describe('treasury endpoints', () => {
    it('getPosition should include currency and location in path', async () => {
      mockFetch.mockResolvedValueOnce({ currency: 'USDT', available: '10000' })
      const api = useApi()
      await api.getPosition('USDT', 'tron')
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/v1/treasury/positions/USDT/tron',
        expect.anything(),
      )
    })
  })

  describe('tenant management (ops)', () => {
    it('updateTenantStatus should POST status', async () => {
      mockFetch.mockResolvedValueOnce({ ok: true })
      const api = useApi()
      await api.updateTenantStatus('t-1', 'SUSPENDED')
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/v1/ops/tenants/t-1/status',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify({ status: 'SUSPENDED' }),
        }),
      )
    })

    it('updateTenantFees should POST fee schedule', async () => {
      mockFetch.mockResolvedValueOnce({ ok: true })
      const api = useApi()
      const fees = { on_ramp_bps: 40, off_ramp_bps: 35, min_fee_usd: '0.50', max_fee_usd: '100.00' }
      await api.updateTenantFees('t-1', fees)
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/v1/ops/tenants/t-1/fees',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify(fees),
        }),
      )
    })

    it('updateTenantLimits should POST limits', async () => {
      mockFetch.mockResolvedValueOnce({ ok: true })
      const api = useApi()
      const limits = { daily_limit_usd: '50000', per_transfer_limit: '10000' }
      await api.updateTenantLimits('t-1', limits)
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/v1/ops/tenants/t-1/limits',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify(limits),
        }),
      )
    })
  })

  describe('manual reviews (ops)', () => {
    it('listManualReviews should include status filter', async () => {
      mockFetch.mockResolvedValueOnce({ reviews: [] })
      const api = useApi()
      await api.listManualReviews('PENDING')
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/v1/ops/manual-reviews?status=PENDING',
        expect.anything(),
      )
    })

    it('approveReview should POST to correct endpoint', async () => {
      mockFetch.mockResolvedValueOnce({ ok: true })
      const api = useApi()
      await api.approveReview('rev-1', 'Looks good')
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/v1/ops/manual-reviews/rev-1/approve',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify({ notes: 'Looks good' }),
        }),
      )
    })

    it('rejectReview should POST to correct endpoint', async () => {
      mockFetch.mockResolvedValueOnce({ ok: true })
      const api = useApi()
      await api.rejectReview('rev-1', 'Suspicious')
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/v1/ops/manual-reviews/rev-1/reject',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify({ notes: 'Suspicious' }),
        }),
      )
    })

    it('approveReview should default notes to empty string', async () => {
      mockFetch.mockResolvedValueOnce({ ok: true })
      const api = useApi()
      await api.approveReview('rev-1')
      expect(mockFetch).toHaveBeenCalledWith(
        expect.any(String),
        expect.objectContaining({
          body: JSON.stringify({ notes: '' }),
        }),
      )
    })
  })

  describe('ledger endpoints', () => {
    it('getAccountEntries should encode account code in URL', async () => {
      mockFetch.mockResolvedValueOnce({ entries: [] })
      const api = useApi()
      await api.getAccountEntries('tenant:acme:assets:bank:gbp:clearing')
      expect(mockFetch).toHaveBeenCalledWith(
        expect.stringContaining(
          encodeURIComponent('tenant:acme:assets:bank:gbp:clearing'),
        ),
        expect.anything(),
      )
    })
  })

  describe('settlement endpoints (ops)', () => {
    it('getSettlementReport should include period param', async () => {
      mockFetch.mockResolvedValueOnce({ positions: [] })
      const api = useApi()
      await api.getSettlementReport('2024-01-01')
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/v1/ops/settlements/report?period=2024-01-01',
        expect.anything(),
      )
    })

    it('markSettlementPaid should POST payment ref', async () => {
      mockFetch.mockResolvedValueOnce({ ok: true })
      const api = useApi()
      await api.markSettlementPaid('t-1', 'PAY-REF-123')
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/v1/ops/settlements/t-1/mark-paid',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify({ payment_ref: 'PAY-REF-123' }),
        }),
      )
    })
  })

  describe('route comparison', () => {
    it('should POST route comparison request', async () => {
      mockFetch.mockResolvedValueOnce({ routes: [] })
      const api = useApi()
      await api.getRouteComparisons('1000', 'GBP', 'NGN')
      expect(mockFetch).toHaveBeenCalledWith(
        '/api/v1/routes',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify({
            from_currency: 'GBP',
            to_currency: 'NGN',
            amount: '1000',
          }),
        }),
      )
    })
  })

  describe('return value shape', () => {
    it('should return all expected API methods', () => {
      const api = useApi()
      const expectedMethods = [
        'listTransfers', 'getTransfer', 'getTransferEvents',
        'getPositions', 'getPosition', 'getLiquidity',
        'createQuote', 'getQuote',
        'getDashboardSummary', 'getCapacityMetrics', 'getTenantVolumes',
        'listTenants', 'getTenant', 'updateTenantStatus', 'updateTenantKYB',
        'updateTenantFees', 'updateTenantLimits',
        'getLedgerAccounts', 'getAccountEntries', 'searchJournalEntries',
        'getRouteComparisons', 'getChainStatuses',
        'checkHealth',
        'listManualReviews', 'approveReview', 'rejectReview',
        'getReconciliationReport', 'runReconciliation',
        'getSettlementReport', 'markSettlementPaid',
      ]
      for (const method of expectedMethods) {
        expect(typeof (api as any)[method]).toBe('function')
      }
    })
  })
})
