/**
 * JSON Schema definitions for Fastify's schema-based serialization.
 * Fastify compiles these at startup using fast-json-stringify, producing
 * serializers that are 2-3x faster than JSON.stringify().
 */

// ── Supported enums ────────────────────────────────────────────────────────

export const SUPPORTED_CURRENCIES = ["GBP", "NGN", "USD", "EUR", "USDT", "USDC"] as const;
export const SUPPORTED_COUNTRIES = ["GB", "NG", "US", "DE", "FR", "GH", "KE"] as const;

// ── Shared schema components ────────────────────────────────────────────────

const feeBreakdownSchema = {
  type: "object" as const,
  properties: {
    on_ramp_fee: { type: "string" as const },
    network_fee: { type: "string" as const },
    off_ramp_fee: { type: "string" as const },
    total_fee_usd: { type: "string" as const },
  },
};

const routeInfoSchema = {
  type: "object" as const,
  properties: {
    chain: { type: "string" as const },
    stable_coin: { type: "string" as const },
    estimated_time_min: { type: "integer" as const },
    on_ramp_provider: { type: "string" as const },
    off_ramp_provider: { type: "string" as const },
    explorer_url: { type: "string" as const },
  },
};

const senderSchema = {
  type: "object" as const,
  properties: {
    id: { type: "string" as const },
    name: { type: "string" as const },
    email: { type: "string" as const },
    country: { type: "string" as const, enum: [...SUPPORTED_COUNTRIES] },
  },
};

const recipientSchema = {
  type: "object" as const,
  properties: {
    name: { type: "string" as const },
    account_number: { type: "string" as const },
    sort_code: { type: "string" as const },
    bank_name: { type: "string" as const },
    country: { type: "string" as const, enum: [...SUPPORTED_COUNTRIES] },
    iban: { type: "string" as const },
  },
};

// ── Quote schemas ───────────────────────────────────────────────────────────

export const quoteResponseSchema = {
  type: "object" as const,
  properties: {
    id: { type: "string" as const },
    tenant_id: { type: "string" as const },
    source_currency: { type: "string" as const },
    source_amount: { type: "string" as const },
    dest_currency: { type: "string" as const },
    dest_amount: { type: "string" as const },
    fx_rate: { type: "string" as const },
    fees: feeBreakdownSchema,
    route: routeInfoSchema,
    expires_at: { type: "string" as const },
    created_at: { type: "string" as const },
  },
};

export const createQuoteBodySchema = {
  type: "object" as const,
  required: ["source_currency", "source_amount", "dest_currency"],
  properties: {
    source_currency: { type: "string" as const, enum: [...SUPPORTED_CURRENCIES] },
    source_amount: { type: "string" as const, pattern: "^(?:0|[1-9]\\d*)(?:\\.\\d+)?$" },
    dest_currency: { type: "string" as const, enum: [...SUPPORTED_CURRENCIES] },
    dest_country: { type: "string" as const, enum: [...SUPPORTED_COUNTRIES] },
  },
  additionalProperties: false,
};

// ── Transfer schemas ────────────────────────────────────────────────────────

const blockchainTxSchema = {
  type: "object" as const,
  properties: {
    chain: { type: "string" as const },
    type: { type: "string" as const },
    tx_hash: { type: "string" as const },
    explorer_url: { type: "string" as const },
    status: { type: "string" as const },
  },
};

export const transferResponseSchema = {
  type: "object" as const,
  properties: {
    id: { type: "string" as const },
    tenant_id: { type: "string" as const },
    external_ref: { type: "string" as const },
    idempotency_key: { type: "string" as const },
    status: { type: "string" as const },
    version: { type: "integer" as const },
    source_currency: { type: "string" as const },
    source_amount: { type: "string" as const },
    dest_currency: { type: "string" as const },
    dest_amount: { type: "string" as const },
    stable_coin: { type: "string" as const },
    stable_amount: { type: "string" as const },
    chain: { type: "string" as const },
    fx_rate: { type: "string" as const },
    fees: feeBreakdownSchema,
    sender: senderSchema,
    recipient: recipientSchema,
    quote_id: { type: "string" as const },
    blockchain_transactions: {
      type: "array" as const,
      items: blockchainTxSchema,
    },
    created_at: { type: "string" as const },
    updated_at: { type: "string" as const },
    funded_at: { type: "string" as const },
    completed_at: { type: "string" as const },
    failed_at: { type: "string" as const },
    failure_reason: { type: "string" as const },
    failure_code: { type: "string" as const },
  },
};

export const createTransferBodySchema = {
  type: "object" as const,
  required: [
    "idempotency_key",
    "source_currency",
    "source_amount",
    "dest_currency",
    "recipient",
  ],
  properties: {
    idempotency_key: { type: "string" as const, minLength: 1, maxLength: 255 },
    external_ref: { type: "string" as const, maxLength: 255 },
    source_currency: { type: "string" as const, enum: [...SUPPORTED_CURRENCIES] },
    source_amount: { type: "string" as const, pattern: "^(?:0|[1-9]\\d*)(?:\\.\\d+)?$" },
    dest_currency: { type: "string" as const, enum: [...SUPPORTED_CURRENCIES] },
    sender: senderSchema,
    recipient: {
      ...recipientSchema,
      required: ["name", "country"],
    },
    quote_id: { type: "string" as const, format: "uuid" },
  },
  additionalProperties: false,
};

export const listTransfersQuerySchema = {
  type: "object" as const,
  properties: {
    page_size: { type: "integer" as const, minimum: 1, maximum: 1000, default: 50 },
    page_token: { type: "string" as const },
  },
};

export const listTransfersResponseSchema = {
  type: "object" as const,
  properties: {
    transfers: {
      type: "array" as const,
      items: transferResponseSchema,
    },
    next_page_token: { type: "string" as const },
    total_count: { type: "integer" as const },
  },
};

// ── Treasury schemas ────────────────────────────────────────────────────────

export const positionResponseSchema = {
  type: "object" as const,
  properties: {
    id: { type: "string" as const },
    tenant_id: { type: "string" as const },
    currency: { type: "string" as const },
    location: { type: "string" as const },
    balance: { type: "string" as const },
    locked: { type: "string" as const },
    available: { type: "string" as const },
    min_balance: { type: "string" as const },
    target_balance: { type: "string" as const },
    updated_at: { type: "string" as const },
  },
};

export const positionsResponseSchema = {
  type: "object" as const,
  properties: {
    positions: {
      type: "array" as const,
      items: positionResponseSchema,
    },
  },
};

// ── Tenant portal schemas ──────────────────────────────────────────────────

const tenantFeeScheduleSchema = {
  type: "object" as const,
  properties: {
    on_ramp_bps: { type: "integer" as const },
    off_ramp_bps: { type: "integer" as const },
    min_fee_usd: { type: "string" as const },
    max_fee_usd: { type: "string" as const },
  },
};

export const tenantProfileResponseSchema = {
  type: "object" as const,
  properties: {
    id: { type: "string" as const },
    name: { type: "string" as const },
    slug: { type: "string" as const },
    status: { type: "string" as const },
    settlement_model: { type: "string" as const },
    kyb_status: { type: "string" as const },
    kyb_verified_at: { type: "string" as const },
    fee_schedule: tenantFeeScheduleSchema,
    daily_limit_usd: { type: "string" as const },
    per_transfer_limit: { type: "string" as const },
    webhook_url: { type: "string" as const },
    created_at: { type: "string" as const },
    updated_at: { type: "string" as const },
  },
};

export const webhookConfigBodySchema = {
  type: "object" as const,
  required: ["webhook_url"],
  properties: {
    webhook_url: { type: "string" as const, format: "uri", maxLength: 2048 },
  },
  additionalProperties: false,
};

export const webhookConfigResponseSchema = {
  type: "object" as const,
  properties: {
    webhook_url: { type: "string" as const },
    webhook_secret: { type: "string" as const },
  },
};

export const apiKeyInfoSchema = {
  type: "object" as const,
  properties: {
    id: { type: "string" as const },
    key_prefix: { type: "string" as const },
    environment: { type: "string" as const },
    name: { type: "string" as const },
    is_active: { type: "boolean" as const },
    last_used_at: { type: "string" as const },
    expires_at: { type: "string" as const },
    created_at: { type: "string" as const },
  },
};

export const apiKeyListResponseSchema = {
  type: "object" as const,
  properties: {
    keys: {
      type: "array" as const,
      items: apiKeyInfoSchema,
    },
  },
};

export const createApiKeyBodySchema = {
  type: "object" as const,
  required: ["environment"],
  properties: {
    environment: { type: "string" as const, enum: ["LIVE", "TEST"] },
    name: { type: "string" as const, maxLength: 255 },
  },
  additionalProperties: false,
};

export const createApiKeyResponseSchema = {
  type: "object" as const,
  properties: {
    key: apiKeyInfoSchema,
    raw_key: { type: "string" as const },
  },
};

export const dashboardMetricsResponseSchema = {
  type: "object" as const,
  properties: {
    transfers_today: { type: "integer" as const },
    volume_today_usd: { type: "string" as const },
    completed_today: { type: "integer" as const },
    failed_today: { type: "integer" as const },
    transfers_7d: { type: "integer" as const },
    volume_7d_usd: { type: "string" as const },
    fees_7d_usd: { type: "string" as const },
    transfers_30d: { type: "integer" as const },
    volume_30d_usd: { type: "string" as const },
    fees_30d_usd: { type: "string" as const },
    success_rate_30d: { type: "string" as const },
    daily_limit_usd: { type: "string" as const },
    daily_usage_usd: { type: "string" as const },
  },
};

const transferStatsBucketSchema = {
  type: "object" as const,
  properties: {
    timestamp: { type: "string" as const },
    total: { type: "integer" as const },
    completed: { type: "integer" as const },
    failed: { type: "integer" as const },
    volume_usd: { type: "string" as const },
    fees_usd: { type: "string" as const },
  },
};

export const transferStatsQuerySchema = {
  type: "object" as const,
  properties: {
    period: { type: "string" as const, enum: ["24h", "7d", "30d"], default: "24h" },
    granularity: { type: "string" as const, enum: ["hour", "day"], default: "hour" },
  },
};

export const transferStatsResponseSchema = {
  type: "object" as const,
  properties: {
    buckets: {
      type: "array" as const,
      items: transferStatsBucketSchema,
    },
  },
};

const feeReportEntrySchema = {
  type: "object" as const,
  properties: {
    source_currency: { type: "string" as const },
    dest_currency: { type: "string" as const },
    transfer_count: { type: "integer" as const },
    total_volume_usd: { type: "string" as const },
    on_ramp_fees_usd: { type: "string" as const },
    off_ramp_fees_usd: { type: "string" as const },
    network_fees_usd: { type: "string" as const },
    total_fees_usd: { type: "string" as const },
  },
};

export const feeReportQuerySchema = {
  type: "object" as const,
  properties: {
    from: { type: "string" as const, format: "date-time" },
    to: { type: "string" as const, format: "date-time" },
  },
};

export const feeReportResponseSchema = {
  type: "object" as const,
  properties: {
    entries: {
      type: "array" as const,
      items: feeReportEntrySchema,
    },
    total_fees_usd: { type: "string" as const },
  },
};

// ── Analytics schemas ──────────────────────────────────────────────────────

export const analyticsPeriodQuerySchema = {
  type: "object" as const,
  properties: {
    period: { type: "string" as const, enum: ["24h", "7d", "30d"], default: "7d" },
  },
};

const statusCountSchema = {
  type: "object" as const,
  properties: {
    status: { type: "string" as const },
    count: { type: "integer" as const },
  },
};

export const statusDistributionResponseSchema = {
  type: "object" as const,
  properties: {
    statuses: {
      type: "array" as const,
      items: statusCountSchema,
    },
  },
};

const corridorMetricSchema = {
  type: "object" as const,
  properties: {
    source_currency: { type: "string" as const },
    dest_currency: { type: "string" as const },
    transfer_count: { type: "integer" as const },
    volume_usd: { type: "string" as const },
    fees_usd: { type: "string" as const },
    completed: { type: "integer" as const },
    failed: { type: "integer" as const },
    success_rate: { type: "string" as const },
    avg_latency_ms: { type: "integer" as const },
  },
};

export const corridorMetricsResponseSchema = {
  type: "object" as const,
  properties: {
    corridors: {
      type: "array" as const,
      items: corridorMetricSchema,
    },
  },
};

export const latencyPercentilesResponseSchema = {
  type: "object" as const,
  properties: {
    sample_count: { type: "integer" as const },
    p50_ms: { type: "integer" as const },
    p90_ms: { type: "integer" as const },
    p95_ms: { type: "integer" as const },
    p99_ms: { type: "integer" as const },
  },
};

export const volumeComparisonQuerySchema = {
  type: "object" as const,
  properties: {
    period: { type: "string" as const, enum: ["7d", "30d"], default: "7d" },
  },
};

export const volumeComparisonResponseSchema = {
  type: "object" as const,
  properties: {
    current_count: { type: "integer" as const },
    current_volume_usd: { type: "string" as const },
    current_fees_usd: { type: "string" as const },
    previous_count: { type: "integer" as const },
    previous_volume_usd: { type: "string" as const },
    previous_fees_usd: { type: "string" as const },
  },
};

const activityItemSchema = {
  type: "object" as const,
  properties: {
    transfer_id: { type: "string" as const },
    external_ref: { type: "string" as const },
    status: { type: "string" as const },
    source_currency: { type: "string" as const },
    source_amount: { type: "string" as const },
    dest_currency: { type: "string" as const },
    dest_amount: { type: "string" as const },
    updated_at: { type: "string" as const },
    failure_reason: { type: "string" as const },
  },
};

export const recentActivityQuerySchema = {
  type: "object" as const,
  properties: {
    limit: { type: "integer" as const, minimum: 1, maximum: 50, default: 20 },
  },
};

export const recentActivityResponseSchema = {
  type: "object" as const,
  properties: {
    items: {
      type: "array" as const,
      items: activityItemSchema,
    },
  },
};

// ── Webhook management schemas ─────────────────────────────────────────────

const webhookDeliveryInfoSchema = {
  type: "object" as const,
  properties: {
    id: { type: "string" as const },
    tenant_id: { type: "string" as const },
    event_type: { type: "string" as const },
    transfer_id: { type: "string" as const },
    delivery_id: { type: "string" as const },
    webhook_url: { type: "string" as const },
    status: { type: "string" as const },
    status_code: { type: "integer" as const },
    attempt: { type: "integer" as const },
    max_attempts: { type: "integer" as const },
    error_message: { type: "string" as const },
    duration_ms: { type: "integer" as const },
    created_at: { type: "string" as const },
    delivered_at: { type: "string" as const },
  },
};

export const webhookDeliveriesQuerySchema = {
  type: "object" as const,
  properties: {
    event_type: { type: "string" as const },
    status: { type: "string" as const, enum: ["pending", "delivered", "failed", "dead_letter"] },
    page_size: { type: "integer" as const, minimum: 1, maximum: 100, default: 50 },
    page_offset: { type: "integer" as const, minimum: 0, default: 0 },
  },
};

export const webhookDeliveriesResponseSchema = {
  type: "object" as const,
  properties: {
    deliveries: {
      type: "array" as const,
      items: webhookDeliveryInfoSchema,
    },
    total_count: { type: "integer" as const },
  },
};

export const webhookDeliveryDetailResponseSchema = {
  type: "object" as const,
  properties: {
    delivery: webhookDeliveryInfoSchema,
    request_body: { type: "object" as const },
  },
};

export const webhookDeliveryStatsQuerySchema = {
  type: "object" as const,
  properties: {
    period: { type: "string" as const, enum: ["24h", "7d", "30d"], default: "24h" },
  },
};

export const webhookDeliveryStatsResponseSchema = {
  type: "object" as const,
  properties: {
    total_deliveries: { type: "integer" as const },
    successful: { type: "integer" as const },
    failed: { type: "integer" as const },
    dead_lettered: { type: "integer" as const },
    pending: { type: "integer" as const },
    avg_latency_ms: { type: "integer" as const },
    p95_latency_ms: { type: "integer" as const },
  },
};

const webhookEventSubscriptionSchema = {
  type: "object" as const,
  properties: {
    id: { type: "string" as const },
    event_type: { type: "string" as const },
    created_at: { type: "string" as const },
  },
};

export const webhookEventSubscriptionsResponseSchema = {
  type: "object" as const,
  properties: {
    subscriptions: {
      type: "array" as const,
      items: webhookEventSubscriptionSchema,
    },
    available_event_types: {
      type: "array" as const,
      items: { type: "string" as const },
    },
  },
};

export const updateWebhookEventSubscriptionsBodySchema = {
  type: "object" as const,
  required: ["event_types"],
  properties: {
    event_types: {
      type: "array" as const,
      items: { type: "string" as const },
    },
  },
  additionalProperties: false,
};

export const testWebhookResponseSchema = {
  type: "object" as const,
  properties: {
    success: { type: "boolean" as const },
    status_code: { type: "integer" as const },
    duration_ms: { type: "integer" as const },
    error: { type: "string" as const },
  },
};

// ── Error schemas ───────────────────────────────────────────────────────────

export const errorResponseSchema = {
  type: "object" as const,
  properties: {
    error: { type: "string" as const },
    message: { type: "string" as const },
    request_id: { type: "string" as const },
  },
};
