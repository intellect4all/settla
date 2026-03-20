import type {
  TenantProfile, APIKeyInfo, CreateAPIKeyResponse, WebhookConfigResponse,
  DashboardMetrics, TransferStatsBucket, FeeReport,
  TransferListResponse, Transfer, TransferEvent,
  Position, WebhookDeliveryInfo, WebhookDeliveryDetail,
  WebhookDeliveryStats, WebhookSubscriptionsResponse, TestWebhookResult,
  StatusCount, CorridorMetric, LatencyPercentiles, VolumeComparison, ActivityItem,
  DepositSession, DepositSessionListResponse,
  CryptoSettings, CryptoBalance,
  PaymentLink, PaymentLinkListResponse,
  FeeBreakdownAnalytics, ProviderPerformance, DepositAnalyticsResponse,
  ReconciliationSummary, ExportJob, ExportParams,
} from '~/types'
import type { RegisterResponse } from '~/types'

export function usePortalApi() {
  async function request<T>(path: string, opts: { method?: string; body?: string; _retryCount?: number } = {}): Promise<T> {
    const controller = new AbortController()
    const timeout = setTimeout(() => controller.abort(), 30_000)
    try {
      const result = await $fetch<T>(`/api${path}`, {
        method: (opts.method ?? 'GET') as any,
        body: opts.body,
        signal: controller.signal,
        headers: {
          'Content-Type': 'application/json',
          // No Authorization header — server proxy injects from httpOnly cookie
        },
      })
      return result
    } catch (err: any) {
      const retryCount = opts._retryCount ?? 0
      const status = err?.response?.status

      // Auto-refresh on 401 and retry once
      if (status === 401 && retryCount < 1) {
        try {
          await $fetch('/api/auth/refresh', { method: 'POST' })
          return request<T>(path, { ...opts, _retryCount: retryCount + 1 })
        } catch {
          // Refresh failed — fall through to redirect
        }
        if (import.meta.client) {
          navigateTo('/auth/login')
        }
      }

      // Retry on 5xx / 429 / network errors (up to 2 retries)
      const retryable = !status || status >= 500 || status === 429
      if (retryable && retryCount < 2) {
        const delay = (retryCount + 1) * 1000 // 1s, 2s
        await new Promise(r => setTimeout(r, delay))
        return request<T>(path, { ...opts, _retryCount: retryCount + 1 })
      }

      throw err
    } finally {
      clearTimeout(timeout)
    }
  }

  async function publicRequest<T>(path: string, opts: { method?: string; body?: string } = {}): Promise<T> {
    const controller = new AbortController()
    const timeout = setTimeout(() => controller.abort(), 30_000)
    try {
      return await $fetch<T>(`/api${path}`, {
        method: (opts.method ?? 'GET') as any,
        body: opts.body,
        signal: controller.signal,
        headers: {
          'Content-Type': 'application/json',
        },
      })
    } finally {
      clearTimeout(timeout)
    }
  }

  // Tenant profile
  const getProfile = () => request<TenantProfile>('/v1/me')

  // Webhook config
  const updateWebhook = (webhookUrl: string) =>
    request<WebhookConfigResponse>('/v1/me/webhooks', {
      method: 'PUT',
      body: JSON.stringify({ webhook_url: webhookUrl }),
    })

  // API keys
  const listApiKeys = () => request<{ keys: APIKeyInfo[] }>('/v1/me/api-keys')

  const createApiKey = (environment: string, name?: string) =>
    request<CreateAPIKeyResponse>('/v1/me/api-keys', {
      method: 'POST',
      body: JSON.stringify({ environment, name }),
    })

  const revokeApiKey = (keyId: string) =>
    request<void>(`/v1/me/api-keys/${keyId}`, { method: 'DELETE' })

  const rotateApiKey = (keyId: string, name?: string) =>
    request<CreateAPIKeyResponse>(`/v1/me/api-keys/${keyId}/rotate`, {
      method: 'POST',
      body: JSON.stringify({ name }),
    })

  // Dashboard & analytics
  const getDashboard = () => request<DashboardMetrics>('/v1/me/dashboard')

  const getTransferStats = (period = '24h', granularity = 'hour') =>
    request<{ buckets: TransferStatsBucket[] }>(
      `/v1/me/transfers/stats?period=${period}&granularity=${granularity}`,
    )

  const getFeeReport = (from?: string, to?: string) => {
    const params = new URLSearchParams()
    if (from) params.set('from', from)
    if (to) params.set('to', to)
    const qs = params.toString()
    return request<FeeReport>(`/v1/me/fees/report${qs ? `?${qs}` : ''}`)
  }

  // Transfers (existing /v1/transfers endpoints, tenant-scoped by auth)
  const listTransfers = (params?: { page_size?: number; page_token?: string }) => {
    const query = new URLSearchParams()
    if (params?.page_size) query.set('page_size', String(params.page_size))
    if (params?.page_token) query.set('page_token', params.page_token)
    const qs = query.toString()
    return request<TransferListResponse>(`/v1/transfers${qs ? `?${qs}` : ''}`)
  }

  const getTransfer = (id: string) => request<Transfer>(`/v1/transfers/${id}`)

  const getTransferEvents = (id: string) =>
    request<{ events: TransferEvent[] }>(`/v1/transfers/${id}/events`)

  // Treasury
  const getPositions = () => request<{ positions: Position[] }>('/v1/treasury/positions')

  // Webhook management
  const listWebhookDeliveries = (params?: {
    event_type?: string; status?: string; page_size?: number; page_offset?: number
  }) => {
    const query = new URLSearchParams()
    if (params?.event_type) query.set('event_type', params.event_type)
    if (params?.status) query.set('status', params.status)
    if (params?.page_size) query.set('page_size', String(params.page_size))
    if (params?.page_offset) query.set('page_offset', String(params.page_offset))
    const qs = query.toString()
    return request<{ deliveries: WebhookDeliveryInfo[]; total_count: number }>(
      `/v1/me/webhooks/deliveries${qs ? `?${qs}` : ''}`,
    )
  }

  const getWebhookDelivery = (deliveryId: string) =>
    request<WebhookDeliveryDetail>(`/v1/me/webhooks/deliveries/${deliveryId}`)

  const getWebhookDeliveryStats = (period = '24h') =>
    request<WebhookDeliveryStats>(`/v1/me/webhooks/stats?period=${period}`)

  const listWebhookSubscriptions = () =>
    request<WebhookSubscriptionsResponse>('/v1/me/webhooks/subscriptions')

  const updateWebhookSubscriptions = (eventTypes: string[]) =>
    request<WebhookSubscriptionsResponse>('/v1/me/webhooks/subscriptions', {
      method: 'PUT',
      body: JSON.stringify({ event_types: eventTypes }),
    })

  const testWebhook = () =>
    request<TestWebhookResult>('/v1/me/webhooks/test', { method: 'POST' })

  // Analytics
  const getStatusDistribution = (period = '7d') =>
    request<{ statuses: StatusCount[] }>(`/v1/me/analytics/status-distribution?period=${period}`)

  const getCorridorMetrics = (period = '7d') =>
    request<{ corridors: CorridorMetric[] }>(`/v1/me/analytics/corridors?period=${period}`)

  const getLatencyPercentiles = (period = '7d') =>
    request<LatencyPercentiles>(`/v1/me/analytics/latency?period=${period}`)

  const getVolumeComparison = (period = '7d') =>
    request<VolumeComparison>(`/v1/me/analytics/comparison?period=${period}`)

  const getRecentActivity = (limit = 20) =>
    request<{ items: ActivityItem[] }>(`/v1/me/analytics/activity?limit=${limit}`)

  // Extended analytics
  const getFeeBreakdown = (period = '7d') =>
    request<{ entries: FeeBreakdownAnalytics[]; total_fees_usd: string }>(`/v1/me/analytics/fees?period=${period}`)

  const getProviderPerformance = (period = '7d') =>
    request<{ providers: ProviderPerformance[] }>(`/v1/me/analytics/providers?period=${period}`)

  const getDepositAnalytics = (period = '7d') =>
    request<DepositAnalyticsResponse>(`/v1/me/analytics/deposits?period=${period}`)

  const getReconciliationSummary = () =>
    request<ReconciliationSummary>('/v1/me/analytics/reconciliation')

  const createExportJob = (params: ExportParams) =>
    request<{ job: ExportJob }>('/v1/me/analytics/export', {
      method: 'POST',
      body: JSON.stringify(params),
    })

  const getExportJob = (jobId: string) =>
    request<{ job: ExportJob }>(`/v1/me/analytics/export/${jobId}`)

  // Deposits
  const listDeposits = (params?: { limit?: number; offset?: number }) => {
    const query = new URLSearchParams()
    if (params?.limit) query.set('limit', String(params.limit))
    if (params?.offset) query.set('offset', String(params.offset))
    const qs = query.toString()
    return request<DepositSessionListResponse>(`/v1/deposits${qs ? `?${qs}` : ''}`)
  }

  const getDeposit = (id: string) =>
    request<{ session: DepositSession }>(`/v1/deposits/${id}`)

  const createDeposit = (data: {
    chain: string
    token: string
    expected_amount: string
    currency?: string
    settlement_pref?: string
    idempotency_key?: string
    ttl_seconds?: number
  }) =>
    request<{ session: DepositSession }>('/v1/deposits', {
      method: 'POST',
      body: JSON.stringify(data),
    })

  const cancelDeposit = (id: string) =>
    request<{ session: DepositSession }>(`/v1/deposits/${id}/cancel`, { method: 'POST' })

  // Crypto settings
  const getCryptoSettings = () =>
    request<CryptoSettings>('/v1/portal/crypto-settings')

  const updateCryptoSettings = (data: Partial<CryptoSettings>) =>
    request<CryptoSettings>('/v1/portal/crypto-settings', {
      method: 'POST',
      body: JSON.stringify(data),
    })

  // Crypto balances
  const getCryptoBalances = () =>
    request<{ balances: CryptoBalance[]; total_value_usd: string }>('/v1/deposits/balance')

  const convertCryptoBalance = (data: { chain: string; token: string; amount: string }) =>
    request<{ message: string }>('/v1/deposits/convert', {
      method: 'POST',
      body: JSON.stringify(data),
    })

  // Payment Links (authenticated)
  const listPaymentLinks = (params?: { limit?: number; offset?: number }) => {
    const query = new URLSearchParams()
    if (params?.limit) query.set('limit', String(params.limit))
    if (params?.offset) query.set('offset', String(params.offset))
    const qs = query.toString()
    return request<PaymentLinkListResponse>(`/v1/payment-links${qs ? `?${qs}` : ''}`)
  }

  const createPaymentLink = (data: {
    amount: string
    currency: string
    chain: string
    token: string
    description?: string
    redirect_url?: string
    use_limit?: number
    expires_at_unix?: number
    settlement_pref?: string
    ttl_seconds?: number
  }) =>
    request<{ link: PaymentLink }>('/v1/payment-links', {
      method: 'POST',
      body: JSON.stringify(data),
    })

  const getPaymentLink = (id: string) =>
    request<{ link: PaymentLink }>(`/v1/payment-links/${id}`)

  const disablePaymentLink = (linkId: string) =>
    request<void>(`/v1/payment-links/${linkId}`, { method: 'DELETE' })

  // Payment Links (public — no auth)
  const resolvePaymentLink = (code: string) =>
    publicRequest<{ link: PaymentLink }>(`/v1/payment-links/resolve/${code}`)

  const redeemPaymentLink = (code: string) =>
    publicRequest<{ session: DepositSession; link: PaymentLink }>(`/v1/payment-links/redeem/${code}`, {
      method: 'POST',
    })

  const getDepositPublicStatus = (sessionId: string) =>
    publicRequest<{ status: string; receivedAmount?: string }>(`/v1/deposits/${sessionId}/public-status`)

  return {
    getProfile,
    updateWebhook,
    listApiKeys,
    createApiKey,
    revokeApiKey,
    rotateApiKey,
    getDashboard,
    getTransferStats,
    getFeeReport,
    listTransfers,
    getTransfer,
    getTransferEvents,
    getPositions,
    listWebhookDeliveries,
    getWebhookDelivery,
    getWebhookDeliveryStats,
    listWebhookSubscriptions,
    updateWebhookSubscriptions,
    testWebhook,
    getStatusDistribution,
    getCorridorMetrics,
    getLatencyPercentiles,
    getVolumeComparison,
    getRecentActivity,
    listDeposits,
    getDeposit,
    createDeposit,
    cancelDeposit,
    getCryptoSettings,
    updateCryptoSettings,
    getCryptoBalances,
    convertCryptoBalance,

    // Extended analytics
    getFeeBreakdown,
    getProviderPerformance,
    getDepositAnalytics,
    getReconciliationSummary,
    createExportJob,
    getExportJob,

    // Payment Links (authenticated)
    listPaymentLinks,
    getPaymentLink,
    createPaymentLink,
    disablePaymentLink,

    // Payment Links (public)
    resolvePaymentLink,
    redeemPaymentLink,
    getDepositPublicStatus,

    // Auth — routed through server proxy, cookies managed server-side
    register: (companyName: string, email: string, password: string, displayName?: string) =>
      publicRequest<RegisterResponse>('/v1/auth/register', {
        method: 'POST',
        body: JSON.stringify({ company_name: companyName, email, password, display_name: displayName }),
      }),

    login: (email: string, password: string) =>
      $fetch<any>('/api/auth/login', {
        method: 'POST',
        body: { email, password },
      }),

    refreshAccessToken: () =>
      $fetch<any>('/api/auth/refresh', { method: 'POST' }),

    verifyEmail: (token: string) =>
      publicRequest<{ message: string }>('/v1/auth/verify-email', {
        method: 'POST',
        body: JSON.stringify({ token }),
      }),

    submitKYB: (data: {
      company_registration_number: string
      country: string
      business_type: string
      contact_name: string
      contact_email: string
      contact_phone?: string
    }) =>
      request<{ message: string; kyb_status: string }>('/v1/me/kyb', {
        method: 'POST',
        body: JSON.stringify(data),
      }),
  }
}
