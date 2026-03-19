import type { TransferListResponse, Transfer, Position, PositionsResponse, LiquidityReport, Quote, DashboardSummary, CapacityMetrics, TenantVolume, Tenant, TransferEvent, LedgerAccount, JournalEntry, RouteComparison, ChainStatus, ManualReview, ReconciliationReport, SettlementReport } from '~/types'

export function useApi() {
  // All requests go through the Nuxt server proxy at /api/... which injects
  // API keys server-side. No secrets are sent to or stored in the browser.
  const baseURL = '/api'

  async function request<T>(path: string, opts: { method?: string; body?: string; headers?: Record<string, string> } = {}, retries = 2): Promise<T> {
    const url = `${baseURL}${path}`
    const controller = new AbortController()
    const timeout = setTimeout(() => controller.abort(), 30_000)
    try {
      const res = await $fetch<T>(url, {
        method: (opts.method ?? 'GET') as any,
        body: opts.body,
        signal: controller.signal,
        headers: {
          'Content-Type': 'application/json',
          ...(opts.headers || {}),
        },
      })
      return res
    } catch (err) {
      const status = (err as any)?.response?.status;
      const retryable = !status || status >= 500 || status === 429;
      if (retryable && retries > 0) {
        const delay = (3 - retries) * 1000; // 1s, 2s
        await new Promise(r => setTimeout(r, delay));
        return request<T>(path, opts, retries - 1);
      }
      throw err;
    } finally {
      clearTimeout(timeout)
    }
  }

  /** Ops request — routed through the same server proxy. The proxy detects
   *  /v1/ops/* paths and adds the ops API key header automatically. */
  async function opsRequest<T>(path: string, opts: { method?: string; body?: string } = {}): Promise<T> {
    return request<T>(path, opts)
  }

  // Transfers
  async function listTransfers(params?: { page_size?: number; page_token?: string; status?: string; search?: string }) {
    const query = new URLSearchParams()
    if (params?.page_size) query.set('page_size', String(params.page_size))
    if (params?.page_token) query.set('page_token', params.page_token)
    if (params?.status) query.set('status', params.status)
    if (params?.search) query.set('search', params.search)
    const qs = query.toString()
    return request<TransferListResponse>(`/v1/transfers${qs ? `?${qs}` : ''}`)
  }

  async function getTransfer(id: string) {
    return request<Transfer>(`/v1/transfers/${id}`)
  }

  async function getTransferEvents(id: string) {
    return request<{ events: TransferEvent[] }>(`/v1/transfers/${id}/events`)
  }

  // Treasury
  async function getPositions() {
    return request<PositionsResponse>('/v1/treasury/positions')
  }

  async function getPosition(currency: string, location: string) {
    return request<Position>(`/v1/treasury/positions/${currency}/${location}`)
  }

  async function getLiquidity() {
    return request<LiquidityReport>('/v1/treasury/liquidity')
  }

  // Quotes
  async function createQuote(body: { source_currency: string; source_amount: string; dest_currency: string; dest_country?: string }) {
    return request<Quote>('/v1/quotes', { method: 'POST', body: JSON.stringify(body) })
  }

  async function getQuote(id: string) {
    return request<Quote>(`/v1/quotes/${id}`)
  }

  // Dashboard summary — uses the /v1/me/dashboard endpoint (tenant-scoped)
  async function getDashboardSummary() {
    return request<DashboardSummary>('/v1/me/dashboard')
  }

  // Capacity — fetched from Prometheus first, gateway fallback
  async function getCapacityMetrics() {
    return request<CapacityMetrics>('/v1/me/dashboard')
  }

  async function getTenantVolumes() {
    return opsRequest<{ tenants: TenantVolume[] }>('/v1/ops/settlements/report')
  }

  // Tenants — ops-level: proxied through settla-server internal API
  async function listTenants(params?: { limit?: number; offset?: number }) {
    const query = new URLSearchParams()
    if (params?.limit) query.set('limit', String(params.limit))
    if (params?.offset) query.set('offset', String(params.offset))
    const qs = query.toString()
    return opsRequest<{ tenants: Tenant[] }>(`/v1/ops/tenants${qs ? `?${qs}` : ''}`)
  }

  async function getTenant(id: string) {
    return opsRequest<Tenant>(`/v1/ops/tenants/${id}`)
  }

  async function updateTenantStatus(id: string, status: string) {
    return opsRequest<{ ok: boolean }>(`/v1/ops/tenants/${id}/status`, {
      method: 'POST',
      body: JSON.stringify({ status }),
    })
  }

  async function updateTenantKYB(id: string, kybStatus: string) {
    return opsRequest<{ ok: boolean }>(`/v1/ops/tenants/${id}/kyb`, {
      method: 'POST',
      body: JSON.stringify({ kyb_status: kybStatus }),
    })
  }

  async function updateTenantFees(id: string, fees: { on_ramp_bps: number; off_ramp_bps: number; min_fee_usd: string; max_fee_usd: string }) {
    return opsRequest<{ ok: boolean }>(`/v1/ops/tenants/${id}/fees`, {
      method: 'POST',
      body: JSON.stringify(fees),
    })
  }

  async function updateTenantLimits(id: string, limits: { daily_limit_usd: string; per_transfer_limit: string }) {
    return opsRequest<{ ok: boolean }>(`/v1/ops/tenants/${id}/limits`, {
      method: 'POST',
      body: JSON.stringify(limits),
    })
  }

  // Ledger — maps to /v1/accounts gateway endpoints
  async function getLedgerAccounts() {
    return request<{ accounts: LedgerAccount[] }>('/v1/accounts')
  }

  async function getAccountEntries(code: string) {
    return request<{ entries: JournalEntry[] }>(`/v1/accounts/${encodeURIComponent(code)}/transactions`)
  }

  async function searchJournalEntries(reference: string) {
    return request<{ entries: JournalEntry[] }>(`/v1/accounts?reference=${encodeURIComponent(reference)}`)
  }

  // Routes — maps to /v1/routes gateway endpoint
  async function getRouteComparisons(amount: string, sourceCurrency: string, destCurrency: string) {
    return request<{ routes: RouteComparison[] }>(`/v1/routes`, {
      method: 'POST',
      body: JSON.stringify({ from_currency: sourceCurrency, to_currency: destCurrency, amount }),
    })
  }

  async function getChainStatuses() {
    return request<{ chains: ChainStatus[] }>('/v1/routes')
  }

  // Health
  async function checkHealth() {
    return request<{ status: string }>('/health')
  }

  // Manual Reviews (ops-authenticated)
  async function listManualReviews(status?: string) {
    const qs = status ? `?status=${status}` : ''
    return opsRequest<{ reviews: ManualReview[] }>(`/v1/ops/manual-reviews${qs}`)
  }

  async function approveReview(id: string, notes?: string) {
    return opsRequest<{ ok: boolean }>(`/v1/ops/manual-reviews/${id}/approve`, {
      method: 'POST',
      body: JSON.stringify({ notes: notes ?? '' }),
    })
  }

  async function rejectReview(id: string, notes?: string) {
    return opsRequest<{ ok: boolean }>(`/v1/ops/manual-reviews/${id}/reject`, {
      method: 'POST',
      body: JSON.stringify({ notes: notes ?? '' }),
    })
  }

  // Reconciliation (ops-authenticated)
  async function getReconciliationReport() {
    return opsRequest<ReconciliationReport>('/v1/ops/reconciliation/latest')
  }

  async function runReconciliation() {
    return opsRequest<ReconciliationReport>('/v1/ops/reconciliation/run', { method: 'POST' })
  }

  // Net Settlement (ops-authenticated)
  async function getSettlementReport(period?: string) {
    const qs = period ? `?period=${period}` : ''
    return opsRequest<SettlementReport>(`/v1/ops/settlements/report${qs}`)
  }

  async function markSettlementPaid(tenantId: string, paymentRef: string) {
    return opsRequest<{ ok: boolean }>(`/v1/ops/settlements/${tenantId}/mark-paid`, {
      method: 'POST',
      body: JSON.stringify({ payment_ref: paymentRef }),
    })
  }

  return {
    listTransfers,
    getTransfer,
    getTransferEvents,
    getPositions,
    getPosition,
    getLiquidity,
    createQuote,
    getQuote,
    getDashboardSummary,
    getCapacityMetrics,
    getTenantVolumes,
    listTenants,
    getTenant,
    updateTenantStatus,
    updateTenantKYB,
    updateTenantFees,
    updateTenantLimits,
    getLedgerAccounts,
    getAccountEntries,
    searchJournalEntries,
    getRouteComparisons,
    getChainStatuses,
    checkHealth,
    listManualReviews,
    approveReview,
    rejectReview,
    getReconciliationReport,
    runReconciliation,
    getSettlementReport,
    markSettlementPaid,
  }
}
