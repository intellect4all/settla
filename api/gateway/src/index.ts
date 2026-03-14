/**
 * Settla API Gateway — Thin BFF behind Tyk.
 *
 * Infrastructure concerns (auth key validation, rate limiting, CORS, TLS,
 * access logging, analytics) are handled by Tyk API Gateway (port 8080).
 *
 * This BFF handles only business logic:
 *   - Tenant resolution (API key → tenant from local cache → Redis → DB)
 *   - Idempotency enforcement (Redis, per-tenant scope)
 *   - Business validation (transfer limits, daily volume, KYB status)
 *   - gRPC calls to settla-server (~50 persistent connections)
 *   - Response transformation (gRPC → REST JSON, fast-json-stringify)
 *   - Inbound provider webhook ingestion (HMAC, dedup, NATS publish)
 *   - Health/metrics endpoints
 */
import Fastify from "fastify";
import fastifyCors from "@fastify/cors";
import fastifySwagger from "@fastify/swagger";
import fastifySwaggerUi from "@fastify/swagger-ui";
import IORedis from "ioredis";
import type { Redis } from "ioredis";
import { config } from "./config.js";
import { GrpcPool } from "./grpc/pool.js";
import { SettlaGrpcClient } from "./grpc/client.js";
import { TenantAuthCache } from "./auth/cache.js";
import { authPlugin } from "./auth/plugin.js";
import { healthRoutes } from "./routes/health.js";
import { quoteRoutes } from "./routes/quotes.js";
import { transferRoutes } from "./routes/transfers.js";
import { treasuryRoutes } from "./routes/treasury.js";
import { webhookRoutes } from "./routes/webhooks.js";
import { tenantPortalRoutes } from "./routes/tenant-portal.js";
import { opsRoutes } from "./routes/ops.js";
import { metricsPlugin } from "./metrics.js";
import { loadShedding } from "./middleware/load-shedding.js";
import { gracefulDrain } from "./middleware/graceful-drain.js";
import { rateLimitPlugin } from "./middleware/rate-limit.js";

export async function buildApp(deps?: {
  grpc?: SettlaGrpcClient;
  redis?: Redis | null;
  resolveTenant?: (keyHash: string) => Promise<any>;
  skipGrpcPool?: boolean;
  natsUrl?: string;
}) {
  const server = Fastify({
    bodyLimit: 1_048_576, // 1 MiB — prevents oversized payloads
    logger: {
      level: config.logLevel,
      serializers: {
        req(request) {
          return {
            method: request.method,
            url: request.url,
            hostname: request.hostname,
          };
        },
        res(reply) {
          return {
            statusCode: reply.statusCode,
          };
        },
      },
    },
    genReqId: () => crypto.randomUUID(),
    // Access logging is handled by Tyk; disable Fastify request logging in production
    disableRequestLogging: config.env === "production",
  });

  // Add request_id to all log entries
  server.addHook("onRequest", async (request) => {
    request.log = request.log.child({
      request_id: request.id,
    });
  });

  // ── Load shedding & graceful drain ─────────────────────────────────────
  await server.register(loadShedding, {
    maxConcurrent: Number(process.env.SETTLA_LOAD_SHED_MAX_CONCURRENT) || 1000,
    targetLatencyMs: Number(process.env.SETTLA_LOAD_SHED_TARGET_LATENCY_MS) || 50,
    initialLimit: Number(process.env.SETTLA_LOAD_SHED_INITIAL_LIMIT) || 200,
  });
  await server.register(gracefulDrain);

  // ── CORS ────────────────────────────────────────────────────────────────
  await server.register(fastifyCors, {
    origin: config.corsOrigin,
  });

  // ── Security headers ──────────────────────────────────────────────────
  server.addHook("onSend", async (_request, reply) => {
    reply.header("X-Content-Type-Options", "nosniff");
    reply.header("X-Frame-Options", "DENY");
    // X-XSS-Protection: 0 — disabled intentionally. Non-zero values can
    // introduce vulnerabilities in modern browsers.
    reply.header("X-XSS-Protection", "0");
    reply.header("Referrer-Policy", "strict-origin-when-cross-origin");
    if (config.env === "production") {
      reply.header(
        "Strict-Transport-Security",
        "max-age=31536000; includeSubDomains",
      );
    }
  });

  // ── OpenAPI docs ────────────────────────────────────────────────────────
  await server.register(fastifySwagger, {
    openapi: {
      info: {
        title: "Settla API",
        description: "B2B stablecoin settlement infrastructure (BFF behind Tyk)",
        version: "1.0.0",
      },
      servers: [{ url: `http://localhost:${config.port}` }],
      components: {
        securitySchemes: {
          bearerAuth: {
            type: "http",
            scheme: "bearer",
          },
        },
      },
      security: [{ bearerAuth: [] }],
    },
  });

  await server.register(fastifySwaggerUi, {
    routePrefix: "/docs",
  });

  // ── Infrastructure ──────────────────────────────────────────────────────
  let redis: Redis | null = null;
  if (deps?.redis !== undefined) {
    redis = deps.redis;
  } else {
    try {
      let redisInstance: Redis;
      if (config.redisSentinelAddrs) {
        // Production path: Sentinel-aware client for automatic master discovery
        // and transparent failover.  ioredis queries the sentinel cluster to
        // find the current master before opening the data connection.
        const sentinelAddrs = config.redisSentinelAddrs
          .split(",")
          .map((addr) => {
            const parts = addr.trim().split(":");
            return { host: parts[0], port: Number(parts[1]) || 26379 };
          });
        redisInstance = new IORedis.default({
          sentinels: sentinelAddrs,
          name: config.redisSentinelMasterName,
          maxRetriesPerRequest: 3,
          lazyConnect: true,
        });
      } else {
        // Development / standalone path: plain Redis URL.
        redisInstance = new IORedis.default(config.redisUrl, {
          maxRetriesPerRequest: 3,
          lazyConnect: true,
        });
      }
      await redisInstance.connect();
      redis = redisInstance;
    } catch {
      server.log.warn("Redis unavailable — running without L2 cache");
      redis = null;
    }
  }

  let grpcClient: SettlaGrpcClient;
  if (deps?.grpc) {
    grpcClient = deps.grpc;
  } else {
    const pool = new GrpcPool(config.grpcUrl, config.grpcPoolSize);
    pool.start();
    grpcClient = new SettlaGrpcClient(pool);

    server.addHook("onClose", async () => {
      await pool.close();
    });
  }

  // ── Auth (tenant resolution only — Tyk validates key existence) ────────
  const authCache = new TenantAuthCache(
    redis,
    config.tenantCacheTtlMs,
    config.redisCacheTtlSeconds,
  );

  const resolveTenant =
    deps?.resolveTenant ??
    (async (keyHash: string) => {
      try {
        const res = await grpcClient.validateApiKey({ keyHash });
        if (!res.valid) return null;

        let feeSchedule = {
          onRampBps: 0,
          offRampBps: 0,
          minFeeUsd: "0",
          maxFeeUsd: "0",
        };
        try {
          const raw = JSON.parse(res.feeScheduleJson);
          feeSchedule = {
            onRampBps: raw.onramp_bps ?? raw.onRampBps ?? 0,
            offRampBps: raw.offramp_bps ?? raw.offRampBps ?? 0,
            minFeeUsd: raw.min_fee_usd ?? raw.minFeeUsd ?? "0",
            maxFeeUsd: raw.max_fee_usd ?? raw.maxFeeUsd ?? "0",
          };
        } catch {
          // use defaults
        }

        return {
          tenantId: res.tenantId,
          slug: res.slug,
          status: res.status,
          feeSchedule,
          dailyLimitUsd: res.dailyLimitUsd,
          perTransferLimit: res.perTransferLimit,
        };
      } catch (err) {
        server.log.error({ err, keyHash: keyHash.slice(0, 8) + "..." }, "gRPC auth validation failed");
        return null;
      }
    });

  // SEC-2: pass redis to authPlugin for cross-instance key revocation via pub/sub
  await server.register(authPlugin, { cache: authCache, resolveTenant, redis });

  // ── Metrics ─────────────────────────────────────────────────────────────
  await server.register(metricsPlugin);

  // ── Per-tenant rate limiting (SEC-7) ────────────────────────────────────
  // Distributed rate limiter: L1 local Map + L2 Redis INCR/EXPIRE.
  // Applied after auth so tenantId is always available.
  await server.register(rateLimitPlugin, {
    limit: config.rateLimitPerTenant,
    redis,
  });

  // ── Routes ──────────────────────────────────────────────────────────────
  await server.register(healthRoutes);
  await server.register(quoteRoutes, { grpc: grpcClient });
  await server.register(transferRoutes, { grpc: grpcClient });
  await server.register(treasuryRoutes, { grpc: grpcClient });
  // SEC-2: pass invalidateAuthCache so the revoke route can flush caches
  await server.register(tenantPortalRoutes, { grpc: grpcClient });
  await server.register(webhookRoutes, { redis, natsUrl: deps?.natsUrl ?? config.natsUrl });
  await server.register(opsRoutes);

  return server;
}

// ── Start server ──────────────────────────────────────────────────────────

async function start() {
  const server = await buildApp();

  try {
    await server.listen({ port: config.port, host: config.host });
  } catch (err) {
    server.log.error(err);
    process.exit(1);
  }
}

// Only start the server when this file is the entry point (not when imported for testing)
if (!process.env.VITEST) {
  start();
}
