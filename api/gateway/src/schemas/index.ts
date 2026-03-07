/**
 * JSON Schema definitions for Fastify's schema-based serialization.
 * Fastify compiles these at startup using fast-json-stringify, producing
 * serializers that are 2-3x faster than JSON.stringify().
 */

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
  },
};

const senderSchema = {
  type: "object" as const,
  properties: {
    id: { type: "string" as const },
    name: { type: "string" as const },
    email: { type: "string" as const },
    country: { type: "string" as const },
  },
};

const recipientSchema = {
  type: "object" as const,
  properties: {
    name: { type: "string" as const },
    account_number: { type: "string" as const },
    sort_code: { type: "string" as const },
    bank_name: { type: "string" as const },
    country: { type: "string" as const },
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
    source_currency: { type: "string" as const, minLength: 3, maxLength: 5 },
    source_amount: { type: "string" as const, pattern: "^\\d+(\\.\\d+)?$" },
    dest_currency: { type: "string" as const, minLength: 3, maxLength: 5 },
    dest_country: { type: "string" as const, minLength: 2, maxLength: 2 },
  },
  additionalProperties: false,
};

// ── Transfer schemas ────────────────────────────────────────────────────────

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
    source_currency: { type: "string" as const, minLength: 3, maxLength: 5 },
    source_amount: { type: "string" as const, pattern: "^\\d+(\\.\\d+)?$" },
    dest_currency: { type: "string" as const, minLength: 3, maxLength: 5 },
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

// ── Error schemas ───────────────────────────────────────────────────────────

export const errorResponseSchema = {
  type: "object" as const,
  properties: {
    error: { type: "string" as const },
    message: { type: "string" as const },
  },
};
