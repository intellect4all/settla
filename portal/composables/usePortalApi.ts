import type {
  TenantProfile, APIKeyInfo, CreateAPIKeyResponse, WebhookConfigResponse,
  DashboardMetrics, TransferStatsBucket, FeeReport,
  TransferListResponse, Transfer, TransferEvent,
  Position, WebhookDeliveryInfo, WebhookDeliveryDetail,
  WebhookDeliveryStats, WebhookSubscriptionsResponse, TestWebhookResult,
  StatusCount, CorridorMetric, LatencyPercentiles, VolumeComparison, ActivityItem,
} from '~/types'

export function usePortalApi() {
  const config = useRuntimeConfig()
  const baseURL = config.public.apiBase as string
  const apiKey = config.public.portalApiKey as string

  async function request<T>(path: string, opts: { method?: string; body?: string } = {}): Promise<T> {
    return $fetch<T>(`${baseURL}${path}`, {
      method: (opts.method ?? 'GET') as any,
      body: opts.body,
      headers: {
        'Content-Type': 'application/json',
        'Authorization': `Bearer ${apiKey}`,
      },
    })
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
  }
}
