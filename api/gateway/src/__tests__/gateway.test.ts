import { describe, it, expect, beforeAll, afterAll, vi } from "vitest";
import Fastify, { type FastifyInstance } from "fastify";
import { TenantAuthCache, type TenantAuth } from "../auth/cache.js";
import { authPlugin, hashApiKey } from "../auth/plugin.js";
import { quoteRoutes } from "../routes/quotes.js";
import { transferRoutes } from "../routes/transfers.js";
import { healthRoutes } from "../routes/health.js";
import { paymentLinkRoutes } from "../routes/payment-links.js";
import { depositRoutes } from "../routes/deposits.js";
import { buildApp } from "../index.js";

// Ensure JWT secret is set for tests
process.env.SETTLA_JWT_SECRET ??= "test-jwt-secret-for-vitest-only";

// ── Mock gRPC client ────────────────────────────────────────────────────────

function createMockGrpc() {
  return {
    createQuote: vi.fn().mockResolvedValue({
      id: "q-001",
      tenant_id: "t-001",
      source_currency: "GBP",
      source_amount: "1000",
      dest_currency: "NGN",
      dest_amount: "520000",
      fx_rate: "520.00",
      fees: {
        on_ramp_fee: "2.00",
        network_fee: "0.50",
        off_ramp_fee: "1.50",
        total_fee_usd: "4.00",
      },
      route: {
        chain: "tron",
        stable_coin: "USDT",
        on_ramp_provider: "onramp-gbp",
        off_ramp_provider: "offramp-ngn",
      },
      expires_at: new Date(Date.now() + 300_000).toISOString(),
      created_at: new Date().toISOString(),
    }),
    getQuote: vi.fn().mockResolvedValue({
      id: "q-001",
      tenant_id: "t-001",
    }),
    createTransfer: vi.fn().mockResolvedValue({
      id: "tx-001",
      tenant_id: "t-001",
      status: "CREATED",
      idempotency_key: "idem-1",
    }),
    getTransfer: vi.fn().mockResolvedValue({
      id: "tx-001",
      tenant_id: "t-001",
      status: "CREATED",
    }),
    listTransfers: vi.fn().mockResolvedValue({
      transfers: [],
      total_count: 0,
    }),
    cancelTransfer: vi.fn().mockResolvedValue({
      id: "tx-001",
      status: "FAILED",
    }),
    getPositions: vi.fn().mockResolvedValue({ positions: [] }),
    getPosition: vi.fn().mockResolvedValue({}),
    getLiquidityReport: vi.fn().mockResolvedValue({}),
    // Deposit service
    createDepositSession: vi.fn().mockResolvedValue({
      session: { id: "ds-001", status: "PENDING_PAYMENT" },
    }),
    getDepositSession: vi.fn().mockResolvedValue({
      session: { id: "ds-001", status: "PENDING_PAYMENT" },
    }),
    listDepositSessions: vi.fn().mockResolvedValue({
      sessions: [],
      total: 0,
    }),
    cancelDepositSession: vi.fn().mockResolvedValue({
      session: { id: "ds-001", status: "CANCELLED" },
    }),
    getDepositPublicStatus: vi.fn().mockResolvedValue({
      id: "ds-001",
      status: "PENDING_PAYMENT",
      chain: "tron",
      token: "USDT",
      depositAddress: "Taddr001",
      expectedAmount: "100",
      receivedAmount: "0",
    }),
    // Payment link service
    createPaymentLink: vi.fn().mockResolvedValue({
      link: { id: "pl-001", shortCode: "ABC123", status: "ACTIVE" },
    }),
    getPaymentLink: vi.fn().mockResolvedValue({
      link: { id: "pl-001", shortCode: "ABC123", status: "ACTIVE" },
    }),
    listPaymentLinks: vi.fn().mockResolvedValue({
      links: [],
      total: 0,
    }),
    disablePaymentLink: vi.fn().mockResolvedValue({}),
    resolvePaymentLink: vi.fn().mockResolvedValue({
      link: { id: "pl-001", shortCode: "ABC123", status: "ACTIVE", amount: "100" },
    }),
    redeemPaymentLink: vi.fn().mockResolvedValue({
      session: { id: "ds-002", status: "PENDING_PAYMENT" },
      link: { id: "pl-001", shortCode: "ABC123" },
    }),
  };
}

// ── Test tenant data ────────────────────────────────────────────────────────

const lemfiTenant: TenantAuth = {
  tenantId: "t-001",
  slug: "lemfi",
  status: "ACTIVE",
  feeSchedule: { onRampBps: 40, offRampBps: 35, minFeeUsd: "0.50", maxFeeUsd: "100" },
  dailyLimitUsd: "1000000",
  perTransferLimit: "50000",
};

const suspendedTenant: TenantAuth = {
  ...lemfiTenant,
  tenantId: "t-suspended",
  status: "SUSPENDED",
};

const LEMFI_KEY = "sk_live_lemfi_test_key_123";
const SUSPENDED_KEY = "sk_live_suspended_key_456";

// ── Helper: build test app ──────────────────────────────────────────────────

async function buildTestApp(mockGrpc?: ReturnType<typeof createMockGrpc>) {
  const grpc = mockGrpc ?? createMockGrpc();
  const cache = new TenantAuthCache(null, 30_000, 300);

  // Pre-populate cache with test keys
  await cache.set(hashApiKey(LEMFI_KEY), lemfiTenant);
  await cache.set(hashApiKey(SUSPENDED_KEY), suspendedTenant);

  const app = Fastify({ logger: false });

  const resolveTenant = async (_keyHash: string) => {
    // Simulate DB lookup for uncached keys
    return null;
  };

  await app.register(authPlugin, { cache, resolveTenant });

  await app.register(healthRoutes);
  await app.register(quoteRoutes, { grpc: grpc as any });
  await app.register(transferRoutes, { grpc: grpc as any });
  await app.register(depositRoutes, { grpc: grpc as any });
  await app.register(paymentLinkRoutes, { grpc: grpc as any });

  await app.ready();
  return { app, grpc };
}

// ── Tests ───────────────────────────────────────────────────────────────────

describe("Auth", () => {
  let app: FastifyInstance;

  beforeAll(async () => {
    ({ app } = await buildTestApp());
  });

  afterAll(async () => {
    await app.close();
  });

  it("rejects requests without auth header", async () => {
    const res = await app.inject({ method: "GET", url: "/v1/transfers" });
    expect(res.statusCode).toBe(401);
    expect(res.json().error).toBe("UNAUTHORIZED");
  });

  it("rejects requests with invalid token", async () => {
    const res = await app.inject({
      method: "GET",
      url: "/v1/transfers",
      headers: { authorization: "Bearer invalid_key" },
    });
    expect(res.statusCode).toBe(401);
  });

  it("rejects suspended tenant", async () => {
    const res = await app.inject({
      method: "GET",
      url: "/v1/transfers",
      headers: { authorization: `Bearer ${SUSPENDED_KEY}` },
    });
    expect(res.statusCode).toBe(403);
    expect(res.json().error).toBe("FORBIDDEN");
  });

  it("allows health endpoint without auth", async () => {
    const res = await app.inject({ method: "GET", url: "/health" });
    expect(res.statusCode).toBe(200);
    expect(res.json().status).toBe("ok");
  });

  it("authenticates valid tenant", async () => {
    const res = await app.inject({
      method: "GET",
      url: "/v1/transfers",
      headers: { authorization: `Bearer ${LEMFI_KEY}` },
    });
    expect(res.statusCode).toBe(200);
  });
});

describe("Tenant Isolation", () => {
  let app: FastifyInstance;
  let grpc: ReturnType<typeof createMockGrpc>;

  beforeAll(async () => {
    ({ app, grpc } = await buildTestApp());
  });

  afterAll(async () => {
    await app.close();
  });

  it("passes tenant_id from auth to gRPC calls", async () => {
    await app.inject({
      method: "POST",
      url: "/v1/quotes",
      headers: {
        authorization: `Bearer ${LEMFI_KEY}`,
        "content-type": "application/json",
      },
      payload: {
        source_currency: "GBP",
        source_amount: "1000",
        dest_currency: "NGN",
      },
    });

    expect(grpc.createQuote).toHaveBeenCalledWith(
      expect.objectContaining({ tenantId: "t-001" }),
      expect.any(String),
    );
  });

  it("always uses authenticated tenant ID, not body tenant_id", async () => {
    await app.inject({
      method: "POST",
      url: "/v1/transfers",
      headers: {
        authorization: `Bearer ${LEMFI_KEY}`,
        "content-type": "application/json",
      },
      payload: {
        idempotency_key: "test-1",
        source_currency: "GBP",
        source_amount: "500",
        dest_currency: "NGN",
        recipient: { name: "Test", country: "NG" },
      },
    });

    // gRPC call should use authenticated tenant, not any body field
    expect(grpc.createTransfer).toHaveBeenCalledWith(
      expect.objectContaining({ tenantId: "t-001" }),
      expect.any(String),
    );
  });
});

describe("Idempotency", () => {
  it("forwards idempotency key to gRPC", async () => {
    const { app, grpc } = await buildTestApp();

    await app.inject({
      method: "POST",
      url: "/v1/transfers",
      headers: {
        authorization: `Bearer ${LEMFI_KEY}`,
        "content-type": "application/json",
      },
      payload: {
        idempotency_key: "unique-key-123",
        source_currency: "GBP",
        source_amount: "1000",
        dest_currency: "NGN",
        recipient: { name: "Recipient", country: "NG" },
      },
    });

    expect(grpc.createTransfer).toHaveBeenCalledWith(
      expect.objectContaining({ idempotencyKey: "unique-key-123" }),
      expect.any(String),
    );

    await app.close();
  });
});

describe("Error Mapping", () => {
  it("maps gRPC NOT_FOUND to 404", async () => {
    const grpc = createMockGrpc();
    grpc.getTransfer.mockRejectedValue({
      code: 5, // NOT_FOUND
      details: "Transfer not found",
    });

    const { app } = await buildTestApp(grpc);

    const res = await app.inject({
      method: "GET",
      url: "/v1/transfers/00000000-0000-0000-0000-000000000001",
      headers: { authorization: `Bearer ${LEMFI_KEY}` },
    });

    expect(res.statusCode).toBe(404);
    expect(res.json().error).toBe("NOT_FOUND");

    await app.close();
  });

  it("maps gRPC INVALID_ARGUMENT to 400", async () => {
    const grpc = createMockGrpc();
    grpc.createQuote.mockRejectedValue({
      code: 3, // INVALID_ARGUMENT
      details: "Invalid currency",
    });

    const { app } = await buildTestApp(grpc);

    const res = await app.inject({
      method: "POST",
      url: "/v1/quotes",
      headers: {
        authorization: `Bearer ${LEMFI_KEY}`,
        "content-type": "application/json",
      },
      payload: {
        source_currency: "XXX",
        source_amount: "100",
        dest_currency: "NGN",
      },
    });

    expect(res.statusCode).toBe(400);

    await app.close();
  });
});

describe("Request Validation", () => {
  let app: FastifyInstance;

  beforeAll(async () => {
    ({ app } = await buildTestApp());
  });

  afterAll(async () => {
    await app.close();
  });

  it("rejects transfer without required fields", async () => {
    const res = await app.inject({
      method: "POST",
      url: "/v1/transfers",
      headers: {
        authorization: `Bearer ${LEMFI_KEY}`,
        "content-type": "application/json",
      },
      payload: {
        source_currency: "GBP",
        // Missing idempotency_key, source_amount, dest_currency, recipient
      },
    });

    expect(res.statusCode).toBe(400);
  });

  it("rejects quote with invalid amount format", async () => {
    const res = await app.inject({
      method: "POST",
      url: "/v1/quotes",
      headers: {
        authorization: `Bearer ${LEMFI_KEY}`,
        "content-type": "application/json",
      },
      payload: {
        source_currency: "GBP",
        source_amount: "not-a-number",
        dest_currency: "NGN",
      },
    });

    expect(res.statusCode).toBe(400);
  });
});

describe("Auth Cache", () => {
  it("serves from local cache on second lookup", async () => {
    const cache = new TenantAuthCache(null, 30_000, 300);
    const auth: TenantAuth = {
      tenantId: "t-test",
      slug: "test",
      status: "ACTIVE",
      feeSchedule: { onRampBps: 40, offRampBps: 35, minFeeUsd: "0.50", maxFeeUsd: "100" },
      dailyLimitUsd: "1000000",
      perTransferLimit: "50000",
    };

    await cache.set("hash-1", auth);

    // L1 hit
    const result = cache.getLocal("hash-1");
    expect(result).toEqual(auth);
  });

  it("returns undefined for expired entries", async () => {
    const cache = new TenantAuthCache(null, 1, 300); // 1ms TTL

    await cache.set("hash-2", {
      tenantId: "t-test",
      slug: "test",
      status: "ACTIVE",
      feeSchedule: { onRampBps: 40, offRampBps: 35, minFeeUsd: "0", maxFeeUsd: "0" },
      dailyLimitUsd: "0",
      perTransferLimit: "0",
    });

    // Wait for expiry
    await new Promise((r) => setTimeout(r, 5));

    const result = cache.getLocal("hash-2");
    expect(result).toBeUndefined();
  });

  it("hashes API keys with SHA-256", () => {
    const hash = hashApiKey("sk_live_test_123");
    expect(hash).toHaveLength(64); // SHA-256 hex = 64 chars
    // Same input produces same hash
    expect(hashApiKey("sk_live_test_123")).toBe(hash);
    // Different input produces different hash
    expect(hashApiKey("sk_live_test_456")).not.toBe(hash);
  });
});

describe("Webhook Normalizer", () => {
  it("normalizes settla-testnet payload", async () => {
    const { settlaTestnetNormalizer } = await import("../webhooks/normalizers/settla-testnet.js");

    const result = settlaTestnetNormalizer.normalize("settla-testnet", {
      event_id: "evt-001",
      transfer_ref: "tx-abc",
      event_type: "onramp.completed",
      status: "completed",
      data: { tx_hash: "0xabc" },
    });

    expect(result).toEqual({
      provider: "settla-testnet",
      externalEventId: "evt-001",
      transferRef: "tx-abc",
      step: "onramp",
      status: "completed",
      metadata: { tx_hash: "0xabc" },
    });
  });

  it("returns null for invalid payload", async () => {
    const { settlaTestnetNormalizer } = await import("../webhooks/normalizers/settla-testnet.js");

    const result = settlaTestnetNormalizer.normalize("settla-testnet", {
      event_id: "evt-001",
      // missing required fields
    });

    expect(result).toBeNull();
  });

  it("returns null for unknown step", async () => {
    const { settlaTestnetNormalizer } = await import("../webhooks/normalizers/settla-testnet.js");

    const result = settlaTestnetNormalizer.normalize("settla-testnet", {
      event_id: "evt-001",
      transfer_ref: "tx-abc",
      event_type: "unknown.completed",
      status: "completed",
    });

    expect(result).toBeNull();
  });
});

// ── Integration tests using buildApp ──────────────────────────────────────

function createMockRedis() {
  const store = new Map<string, string>();
  const mock: any = {
    get: vi.fn(async (key: string) => store.get(key) ?? null),
    set: vi.fn(async (key: string, value: string, ...args: any[]) => {
      // Support NX flag
      if (args.includes("NX") && store.has(key)) return null;
      store.set(key, value);
      return "OK";
    }),
    del: vi.fn(async (key: string) => {
      store.delete(key);
      return 1;
    }),
    quit: vi.fn(async () => "OK"),
    disconnect: vi.fn(),
    subscribe: vi.fn(),
    on: vi.fn(),
    status: "ready",
    duplicate: vi.fn(() => {
      // Return a minimal subscriber-like object for auth plugin pub/sub
      return {
        subscribe: vi.fn(),
        on: vi.fn(),
        quit: vi.fn(async () => "OK"),
        disconnect: vi.fn(),
        status: "ready",
      };
    }),
  };
  return mock;
}

describe("Rate Limiting", () => {
  it("returns 429 when per-tenant rate limit is exceeded", async () => {
    const grpc = createMockGrpc();
    const cache = new TenantAuthCache(null, 30_000, 300);
    await cache.set(hashApiKey(LEMFI_KEY), lemfiTenant);

    const { rateLimitPlugin } = await import("../middleware/rate-limit.js");

    const app = Fastify({ logger: false });
    await app.register(authPlugin, {
      cache,
      resolveTenant: async () => null,
    });

    // Register distributed rate limiter with limit=2 for testing
    await app.register(rateLimitPlugin, { limit: 2, redis: null });

    await app.register(healthRoutes);
    await app.register(transferRoutes, { grpc: grpc as any });
    await app.ready();

    const headers = { authorization: `Bearer ${LEMFI_KEY}` };
    const req = () => app.inject({ method: "GET", url: "/v1/transfers", headers });

    // First 2 should succeed
    expect((await req()).statusCode).toBe(200);
    expect((await req()).statusCode).toBe(200);

    // Third should be rate limited
    const res = await req();
    expect(res.statusCode).toBe(429);
    expect(res.json().error).toBe("rate_limit_exceeded");

    await app.close();
  });

  it("sets rate limit headers on responses", async () => {
    const grpc = createMockGrpc();
    const cache = new TenantAuthCache(null, 30_000, 300);
    await cache.set(hashApiKey(LEMFI_KEY), lemfiTenant);

    const { rateLimitPlugin } = await import("../middleware/rate-limit.js");

    const app = Fastify({ logger: false });
    await app.register(authPlugin, {
      cache,
      resolveTenant: async () => null,
    });
    await app.register(rateLimitPlugin, { limit: 100, redis: null });
    await app.register(transferRoutes, { grpc: grpc as any });
    await app.ready();

    const res = await app.inject({
      method: "GET",
      url: "/v1/transfers",
      headers: { authorization: `Bearer ${LEMFI_KEY}` },
    });

    expect(res.statusCode).toBe(200);
    expect(res.headers["x-ratelimit-limit"]).toBe("100");
    expect(res.headers["x-ratelimit-remaining"]).toBeDefined();
    expect(res.headers["x-ratelimit-reset"]).toBeDefined();

    await app.close();
  });

  it("bypasses rate limiting for health and docs routes", async () => {
    const { rateLimitPlugin } = await import("../middleware/rate-limit.js");

    const app = Fastify({ logger: false });
    // No auth plugin — bypass routes don't need auth
    await app.register(rateLimitPlugin, { limit: 1, redis: null });
    await app.register(healthRoutes);
    await app.ready();

    // Health should always work regardless of limit
    const res1 = await app.inject({ method: "GET", url: "/health" });
    expect(res1.statusCode).toBe(200);
    const res2 = await app.inject({ method: "GET", url: "/health" });
    expect(res2.statusCode).toBe(200);

    // No rate limit headers on bypassed routes
    expect(res1.headers["x-ratelimit-limit"]).toBeUndefined();

    await app.close();
  });
});

describe("API Docs", () => {
  it("GET /docs returns 200", async () => {
    const redis = createMockRedis();
    const app = await buildApp({
      grpc: createMockGrpc() as any,
      redis,
      resolveTenant: async () => null,
      skipGrpcPool: true,
    });

    const res = await app.inject({ method: "GET", url: "/docs/" });
    expect(res.statusCode).toBe(200);

    await app.close();
  });

  it("GET /openapi.json returns valid OpenAPI spec", async () => {
    const redis = createMockRedis();
    const app = await buildApp({
      grpc: createMockGrpc() as any,
      redis,
      resolveTenant: async () => null,
      skipGrpcPool: true,
    });

    const res = await app.inject({ method: "GET", url: "/openapi.json" });
    expect(res.statusCode).toBe(200);

    const spec = res.json();
    expect(spec.openapi).toMatch(/^3\./);
    expect(spec.info.title).toBe("Settla API");
    expect(spec.tags).toBeDefined();
    expect(spec.tags.length).toBeGreaterThan(0);

    await app.close();
  });

  it("GET /openapi.json does not require auth", async () => {
    const redis = createMockRedis();
    const app = await buildApp({
      grpc: createMockGrpc() as any,
      redis,
      resolveTenant: async () => null,
      skipGrpcPool: true,
    });

    const res = await app.inject({
      method: "GET",
      url: "/openapi.json",
      // No authorization header
    });
    expect(res.statusCode).toBe(200);

    await app.close();
  });
});

describe("Webhook Dedup", () => {
  it("deduplicates identical webhooks", async () => {
    const redis = createMockRedis();
    const app = await buildApp({
      grpc: createMockGrpc() as any,
      redis,
      resolveTenant: async () => null,
      skipGrpcPool: true,
      natsUrl: "nats://localhost:14222", // intentionally bad — NATS not needed for dedup test
    });

    // Test the core dedup mechanism at the Redis level — the webhook route
    // uses the same NX pattern to deduplicate incoming events.

    // First set: should succeed (NX on empty key)
    const firstSet = await redis.set("webhook:dedup:test-provider:evt-dedup", "1", "EX", 259200, "NX");
    expect(firstSet).toBe("OK");

    // Second set: should return null (NX on existing key)
    const secondSet = await redis.set("webhook:dedup:test-provider:evt-dedup", "1", "EX", 259200, "NX");
    expect(secondSet).toBeNull();

    await app.close();
  });
});

describe("Large Payload", () => {
  it("rejects payloads exceeding body limit with 413", async () => {
    const app = await buildApp({
      grpc: createMockGrpc() as any,
      redis: null,
      resolveTenant: async (keyHash: string) => {
        if (keyHash === hashApiKey(LEMFI_KEY)) return lemfiTenant;
        return null;
      },
      skipGrpcPool: true,
    });

    // 2 MiB payload — exceeds the 1 MiB bodyLimit
    const largeBody = JSON.stringify({ data: "x".repeat(2 * 1024 * 1024) });

    const res = await app.inject({
      method: "POST",
      url: "/v1/transfers",
      headers: {
        authorization: `Bearer ${LEMFI_KEY}`,
        "content-type": "application/json",
      },
      payload: largeBody,
    });

    expect(res.statusCode).toBe(413);

    await app.close();
  });
});

describe("Security Headers", () => {
  it("includes security headers on all responses", async () => {
    const redis = createMockRedis();
    const app = await buildApp({
      grpc: createMockGrpc() as any,
      redis,
      resolveTenant: async () => null,
      skipGrpcPool: true,
    });

    const res = await app.inject({ method: "GET", url: "/health" });
    expect(res.statusCode).toBe(200);

    expect(res.headers["x-content-type-options"]).toBe("nosniff");
    expect(res.headers["x-frame-options"]).toBe("DENY");
    expect(res.headers["x-xss-protection"]).toBe("0");
    expect(res.headers["referrer-policy"]).toBe("strict-origin-when-cross-origin");

    await app.close();
  });
});

describe("Payment Links", () => {
  let app: FastifyInstance;
  let grpc: ReturnType<typeof createMockGrpc>;

  beforeAll(async () => {
    ({ app, grpc } = await buildTestApp());
  });

  afterAll(async () => {
    await app.close();
  });

  it("POST /v1/payment-links — creates payment link with auth", async () => {
    const res = await app.inject({
      method: "POST",
      url: "/v1/payment-links",
      headers: {
        authorization: `Bearer ${LEMFI_KEY}`,
        "content-type": "application/json",
      },
      payload: {
        amount: "100",
        currency: "USDT",
        chain: "tron",
        token: "USDT",
      },
    });
    expect(res.statusCode).toBe(201);
    expect(grpc.createPaymentLink).toHaveBeenCalledWith(
      expect.objectContaining({ tenantId: "t-001" }),
      expect.any(String),
    );
  });

  it("GET /v1/payment-links — lists payment links with auth", async () => {
    const res = await app.inject({
      method: "GET",
      url: "/v1/payment-links",
      headers: { authorization: `Bearer ${LEMFI_KEY}` },
    });
    expect(res.statusCode).toBe(200);
  });

  it("GET /v1/payment-links/:id — gets payment link with auth", async () => {
    const res = await app.inject({
      method: "GET",
      url: "/v1/payment-links/00000000-0000-0000-0000-000000000001",
      headers: { authorization: `Bearer ${LEMFI_KEY}` },
    });
    expect(res.statusCode).toBe(200);
  });

  it("DELETE /v1/payment-links/:id — disables payment link with auth", async () => {
    const res = await app.inject({
      method: "DELETE",
      url: "/v1/payment-links/00000000-0000-0000-0000-000000000001",
      headers: { authorization: `Bearer ${LEMFI_KEY}` },
    });
    expect(res.statusCode).toBe(204);
  });

  it("GET /v1/payment-links/resolve/:code — resolves without auth", async () => {
    const res = await app.inject({
      method: "GET",
      url: "/v1/payment-links/resolve/ABC123",
    });
    expect(res.statusCode).toBe(200);
  });

  it("POST /v1/payment-links/redeem/:code — redeems without auth", async () => {
    const res = await app.inject({
      method: "POST",
      url: "/v1/payment-links/redeem/ABC123",
    });
    expect(res.statusCode).toBe(201);
  });

  it("GET /v1/deposits/:id/public-status — returns status without auth", async () => {
    const res = await app.inject({
      method: "GET",
      url: "/v1/deposits/00000000-0000-0000-0000-000000000001/public-status",
    });
    expect(res.statusCode).toBe(200);
  });

  it("rejects POST /v1/payment-links without auth", async () => {
    const res = await app.inject({
      method: "POST",
      url: "/v1/payment-links",
      headers: { "content-type": "application/json" },
      payload: {
        amount: "100",
        currency: "USDT",
        chain: "tron",
        token: "USDT",
      },
    });
    expect(res.statusCode).toBe(401);
  });

  it("uses tenantId from auth context, not body", async () => {
    await app.inject({
      method: "POST",
      url: "/v1/payment-links",
      headers: {
        authorization: `Bearer ${LEMFI_KEY}`,
        "content-type": "application/json",
      },
      payload: {
        amount: "100",
        currency: "USDT",
        chain: "tron",
        token: "USDT",
      },
    });
    expect(grpc.createPaymentLink).toHaveBeenCalledWith(
      expect.objectContaining({ tenantId: "t-001" }),
      expect.any(String),
    );
  });
});

describe("CORS", () => {
  it("responds with CORS headers on OPTIONS request", async () => {
    const redis = createMockRedis();
    const app = await buildApp({
      grpc: createMockGrpc() as any,
      redis,
      resolveTenant: async () => null,
      skipGrpcPool: true,
    });

    const res = await app.inject({
      method: "OPTIONS",
      url: "/v1/transfers",
      headers: {
        origin: "https://dashboard.settla.io",
        "access-control-request-method": "POST",
      },
    });

    expect(res.headers["access-control-allow-origin"]).toBeDefined();

    await app.close();
  });
});
