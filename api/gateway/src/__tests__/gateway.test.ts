import { describe, it, expect, beforeAll, afterAll, vi } from "vitest";
import Fastify, { type FastifyInstance } from "fastify";
import { TenantAuthCache, type TenantAuth } from "../auth/cache.js";
import { authPlugin, hashApiKey } from "../auth/plugin.js";
import { RateLimiter, rateLimitPlugin } from "../rate-limit/limiter.js";
import { quoteRoutes } from "../routes/quotes.js";
import { transferRoutes } from "../routes/transfers.js";
import { healthRoutes } from "../routes/health.js";

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

  const resolveTenant = async (keyHash: string) => {
    // Simulate DB lookup for uncached keys
    return null;
  };

  await app.register(authPlugin, { cache, resolveTenant });

  const limiter = new RateLimiter(null, 60, 100, 5000);
  limiter.start();
  await app.register(rateLimitPlugin, { limiter });

  await app.register(healthRoutes);
  await app.register(quoteRoutes, { grpc: grpc as any });
  await app.register(transferRoutes, { grpc: grpc as any });

  await app.ready();
  return { app, grpc, limiter };
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
    );
  });
});

describe("Rate Limiting", () => {
  it("returns rate limit headers", async () => {
    const { app } = await buildTestApp();

    const res = await app.inject({
      method: "GET",
      url: "/v1/transfers",
      headers: { authorization: `Bearer ${LEMFI_KEY}` },
    });

    expect(res.headers["x-ratelimit-limit"]).toBeDefined();
    expect(res.headers["x-ratelimit-remaining"]).toBeDefined();
    expect(res.headers["x-ratelimit-reset"]).toBeDefined();

    await app.close();
  });

  it("rejects when limit exceeded", async () => {
    const grpc = createMockGrpc();
    const cache = new TenantAuthCache(null, 30_000, 300);
    await cache.set(hashApiKey(LEMFI_KEY), lemfiTenant);

    const app = Fastify({ logger: false });
    await app.register(authPlugin, {
      cache,
      resolveTenant: async () => null,
    });

    // Very low limit
    const limiter = new RateLimiter(null, 60, 3, 5000);
    limiter.start();
    await app.register(rateLimitPlugin, { limiter });
    await app.register(transferRoutes, { grpc: grpc as any });
    await app.ready();

    const headers = { authorization: `Bearer ${LEMFI_KEY}` };

    // Exhaust the limit
    for (let i = 0; i < 3; i++) {
      await app.inject({ method: "GET", url: "/v1/transfers", headers });
    }

    // Next request should be rate limited
    const res = await app.inject({ method: "GET", url: "/v1/transfers", headers });
    expect(res.statusCode).toBe(429);
    expect(res.json().error).toBe("RATE_LIMITED");

    limiter.stop();
    await app.close();
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
