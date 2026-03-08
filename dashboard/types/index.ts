// API response types matching gateway schemas

export interface FeeBreakdown {
  on_ramp_fee: string
  network_fee: string
  off_ramp_fee: string
  total_fee_usd: string
}

export interface RouteInfo {
  chain: string
  stable_coin: string
  estimated_time_min: number
  on_ramp_provider: string
  off_ramp_provider: string
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

export interface PositionsResponse {
  positions: Position[]
}

export interface LiquidityReport {
  tenant_id: string
  positions: Position[]
  total_available: Record<string, string>
  alert_positions: Position[]
  generated_at: string
}

export interface Quote {
  id: string
  tenant_id: string
  source_currency: string
  source_amount: string
  dest_currency: string
  dest_amount: string
  fx_rate: string
  fees: FeeBreakdown
  route: RouteInfo
  expires_at: string
  created_at: string
}

export interface Tenant {
  id: string
  name: string
  slug: string
  status: 'ACTIVE' | 'SUSPENDED' | 'ONBOARDING'
  fee_schedule: {
    on_ramp_bps: number
    off_ramp_bps: number
    min_fee_usd: string
    max_fee_usd: string
  }
  settlement_model: 'PREFUNDED' | 'NET_SETTLEMENT'
  daily_limit_usd: string
  per_transfer_limit: string
  kyb_status: 'PENDING' | 'IN_REVIEW' | 'VERIFIED' | 'REJECTED'
  created_at: string
  updated_at: string
}

// Dashboard summary types
export interface DashboardSummary {
  total_transfers_today: number
  success_rate: number
  volume_processed_usd: string
  active_positions: number
  avg_settlement_time_ms: number
}

// Capacity monitoring types
export interface CapacityMetrics {
  current_tps: number
  peak_tps: number
  capacity_tps: number
  ledger_writes_per_sec: number
  pg_sync_lag_ms: number
  treasury_reserves_per_sec: number
  treasury_flush_lag_ms: number
  nats_partition_depths: number[]
  pgbouncer_active: number
  pgbouncer_pool_size: number
}

export interface TenantVolume {
  tenant_id: string
  tenant_name: string
  daily_volume_usd: string
  daily_limit_usd: string
  transfer_count: number
  success_rate: number
}

// Ledger types
export interface LedgerAccount {
  code: string
  name: string
  balance: string
  currency: string
  children?: LedgerAccount[]
}

export interface JournalEntry {
  id: string
  reference: string
  description: string
  entries: EntryLine[]
  created_at: string
}

export interface EntryLine {
  account_code: string
  amount: string
  currency: string
  type: 'DEBIT' | 'CREDIT'
}

// Route comparison types
export interface RouteComparison {
  chain: string
  stable_coin: string
  estimated_time_min: number
  on_ramp_fee: string
  network_fee: string
  off_ramp_fee: string
  total_fee: string
  score: number
  is_selected: boolean
  gas_price_gwei?: string
  health: 'healthy' | 'degraded' | 'down'
}

export interface ChainStatus {
  chain: string
  health: 'healthy' | 'degraded' | 'down'
  gas_price_gwei: string
  block_time_ms: number
  last_checked: string
}

// Column definition for DataTable
export interface Column<T = any> {
  key: string
  label: string
  sortable?: boolean
  width?: string
  align?: 'left' | 'center' | 'right'
  render?: (value: any, row: T) => string
}
