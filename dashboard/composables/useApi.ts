import type { TransferListResponse, Transfer, Position, PositionsResponse, LiquidityReport, Quote, DashboardSummary, CapacityMetrics, TenantVolume, Tenant, TransferEvent, LedgerAccount, JournalEntry, RouteComparison, ChainStatus } from '~/types'

interface ApiError {
  error: string
  message: string
}

interface UseApiOptions {
  tenantApiKey?: string
}

export function useApi(options: UseApiOptions = {}) {
  const config = useRuntimeConfig()
  const baseURL = config.public.apiBase as string

  // For the internal dashboard, we use an admin API key
  // In production this would come from a session/auth
  const apiKey = options.tenantApiKey || 'sk_live_settla_admin_dashboard'

  async function request<T>(path: string, opts: { method?: string; body?: string; headers?: Record<string, string> } = {}): Promise<T> {
    const url = `${baseURL}${path}`
    const res = await $fetch<T>(url, {
      method: (opts.method ?? 'GET') as any,
      body: opts.body,
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${apiKey}`,
        ...(opts.headers || {}),
      },
    })
    return res
  }

  // Transfers
  async function listTransfers(params?: { page_size?: number; page_token?: string; status?: string }) {
    const query = new URLSearchParams()
    if (params?.page_size) query.set('page_size', String(params.page_size))
    if (params?.page_token) query.set('page_token', params.page_token)
    if (params?.status) query.set('status', params.status)
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

  // Dashboard (aggregated endpoints - may need to be added to gateway)
  async function getDashboardSummary() {
    return request<DashboardSummary>('/v1/dashboard/summary')
  }

  // Capacity
  async function getCapacityMetrics() {
    return request<CapacityMetrics>('/v1/dashboard/capacity')
  }

  async function getTenantVolumes() {
    return request<{ tenants: TenantVolume[] }>('/v1/dashboard/tenants/volumes')
  }

  // Tenants
  async function listTenants() {
    return request<{ tenants: Tenant[] }>('/v1/tenants')
  }

  async function getTenant(id: string) {
    return request<Tenant>(`/v1/tenants/${id}`)
  }

  // Ledger
  async function getLedgerAccounts() {
    return request<{ accounts: LedgerAccount[] }>('/v1/ledger/accounts')
  }

  async function getAccountEntries(code: string) {
    return request<{ entries: JournalEntry[] }>(`/v1/ledger/accounts/${encodeURIComponent(code)}/entries`)
  }

  async function searchJournalEntries(reference: string) {
    return request<{ entries: JournalEntry[] }>(`/v1/ledger/entries?reference=${encodeURIComponent(reference)}`)
  }

  // Routes
  async function getRouteComparisons(amount: string, sourceCurrency: string, destCurrency: string) {
    return request<{ routes: RouteComparison[] }>(`/v1/dashboard/routes?amount=${amount}&source=${sourceCurrency}&dest=${destCurrency}`)
  }

  async function getChainStatuses() {
    return request<{ chains: ChainStatus[] }>('/v1/dashboard/chains')
  }

  // Health
  async function checkHealth() {
    return request<{ status: string }>('/health')
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
    getLedgerAccounts,
    getAccountEntries,
    searchJournalEntries,
    getRouteComparisons,
    getChainStatuses,
    checkHealth,
  }
}
