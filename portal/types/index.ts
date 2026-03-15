// Portal API response types matching /v1/me/* gateway schemas

export interface TenantFeeSchedule {
  on_ramp_bps: number
  off_ramp_bps: number
  min_fee_usd: string
  max_fee_usd: string
}

export interface TenantProfile {
  id: string
  name: string
  slug: string
  status: 'ACTIVE' | 'SUSPENDED' | 'ONBOARDING'
  settlement_model: 'PREFUNDED' | 'NET_SETTLEMENT'
  kyb_status: 'PENDING' | 'IN_REVIEW' | 'VERIFIED' | 'REJECTED'
  kyb_verified_at?: string
  fee_schedule: TenantFeeSchedule
  daily_limit_usd: string
  per_transfer_limit: string
  webhook_url?: string
  created_at: string
  updated_at: string
}

export interface APIKeyInfo {
  id: string
  key_prefix: string
  environment: 'LIVE' | 'TEST'
  name?: string
  is_active: boolean
  last_used_at?: string
  expires_at?: string
  created_at: string
}

export interface CreateAPIKeyResponse {
  key: APIKeyInfo
  raw_key: string
}

export interface WebhookConfigResponse {
  webhook_url: string
  webhook_secret: string
}

export interface DashboardMetrics {
  transfers_today: number
  volume_today_usd: string
  completed_today: number
  failed_today: number
  transfers_7d: number
  volume_7d_usd: string
  fees_7d_usd: string
  transfers_30d: number
  volume_30d_usd: string
  fees_30d_usd: string
  success_rate_30d: string
  daily_limit_usd: string
  daily_usage_usd: string
}

export interface TransferStatsBucket {
  timestamp: string
  total: number
  completed: number
  failed: number
  volume_usd: string
  fees_usd: string
}

export interface FeeReportEntry {
  source_currency: string
  dest_currency: string
  transfer_count: number
  total_volume_usd: string
  on_ramp_fees_usd: string
  off_ramp_fees_usd: string
  network_fees_usd: string
  total_fees_usd: string
}

export interface FeeReport {
  entries: FeeReportEntry[]
  total_fees_usd: string
}

// Shared types (same as ops dashboard)

export interface FeeBreakdown {
  on_ramp_fee: string
  network_fee: string
  off_ramp_fee: string
  total_fee_usd: string
}

export interface Sender {
  id?: string
  name?: string
  email?: string
  country?: string
}

export interface Recipient {
  name: string
  account_number?: string
  sort_code?: string
  bank_name?: string
  country: string
  iban?: string
}

export type TransferStatus =
  | 'CREATED'
  | 'FUNDED'
  | 'ON_RAMPING'
  | 'SETTLING'
  | 'OFF_RAMPING'
  | 'COMPLETING'
  | 'COMPLETED'
  | 'FAILED'
  | 'REFUNDING'
  | 'REFUNDED'

export interface Transfer {
  id: string
  tenant_id: string
  external_ref: string
  idempotency_key: string
  status: TransferStatus
  version: number
  source_currency: string
  source_amount: string
  dest_currency: string
  dest_amount: string
  stable_coin: string
  stable_amount: string
  chain: string
  fx_rate: string
  fees: FeeBreakdown
  sender: Sender
  recipient: Recipient
  quote_id?: string
  created_at: string
  updated_at: string
  funded_at?: string
  completed_at?: string
  failed_at?: string
  failure_reason?: string
  failure_code?: string
}

export interface TransferListResponse {
  transfers: Transfer[]
  next_page_token?: string
  total_count: number
}

export interface TransferEvent {
  id: string
  transfer_id: string
  tenant_id: string
  from_status: TransferStatus
  to_status: TransferStatus
  occurred_at: string
  metadata?: Record<string, string>
  provider_ref?: string
}

export interface Position {
  id: string
  tenant_id: string
  currency: string
  location: string
  balance: string
  locked: string
  available: string
  min_balance: string
  target_balance: string
  updated_at: string
}

export interface Column<T = any> {
  key: string
  label: string
  sortable?: boolean
  width?: string
  align?: 'left' | 'center' | 'right'
  render?: (value: any, row: T) => string
}

// Webhook management types

export interface WebhookDeliveryInfo {
  id: string
  tenant_id: string
  event_type: string
  transfer_id?: string
  delivery_id: string
  webhook_url: string
  status: 'pending' | 'delivered' | 'failed' | 'dead_letter'
  status_code?: number
  attempt: number
  max_attempts: number
  error_message?: string
  duration_ms?: number
  created_at: string
  delivered_at?: string
}

export interface WebhookDeliveryDetail {
  delivery: WebhookDeliveryInfo
  request_body?: Record<string, unknown>
}

export interface WebhookDeliveryStats {
  total_deliveries: number
  successful: number
  failed: number
  dead_lettered: number
  pending: number
  avg_latency_ms: number
  p95_latency_ms: number
}

export interface WebhookEventSubscription {
  id: string
  event_type: string
  created_at: string
}

export interface WebhookSubscriptionsResponse {
  subscriptions: WebhookEventSubscription[]
  available_event_types: string[]
}

export interface TestWebhookResult {
  success: boolean
  status_code?: number
  duration_ms?: number
  error?: string
}

// Analytics types

export interface StatusCount {
  status: string
  count: number
}

export interface CorridorMetric {
  source_currency: string
  dest_currency: string
  transfer_count: number
  volume_usd: string
  fees_usd: string
  completed: number
  failed: number
  success_rate: string
  avg_latency_ms: number
}

export interface LatencyPercentiles {
  sample_count: number
  p50_ms: number
  p90_ms: number
  p95_ms: number
  p99_ms: number
}

export interface VolumeComparison {
  current_count: number
  current_volume_usd: string
  current_fees_usd: string
  previous_count: number
  previous_volume_usd: string
  previous_fees_usd: string
}

export interface ActivityItem {
  transfer_id: string
  external_ref: string
  status: string
  source_currency: string
  source_amount: string
  dest_currency: string
  dest_amount: string
  updated_at: string
  failure_reason?: string
}
