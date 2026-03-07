import Fastify from "fastify";
import fastifySwagger from "@fastify/swagger";
import fastifySwaggerUi from "@fastify/swagger-ui";
import IORedis from "ioredis";
import type { Redis } from "ioredis";
import { config } from "./config.js";
import { GrpcPool } from "./grpc/pool.js";
import { SettlaGrpcClient } from "./grpc/client.js";
import { TenantAuthCache } from "./auth/cache.js";
import { authPlugin } from "./auth/plugin.js";
import { RateLimiter, rateLimitPlugin } from "./rate-limit/limiter.js";
import { healthRoutes } from "./routes/health.js";
import { quoteRoutes } from "./routes/quotes.js";
import { transferRoutes } from "./routes/transfers.js";
import { treasuryRoutes } from "./routes/treasury.js";
import { metricsPlugin } from "./metrics.js";

export async function buildApp(deps?: {
  grpc?: SettlaGrpcClient;
  redis?: Redis | null;
  resolveTenant?: (keyHash: string) => Promise<any>;
  skipGrpcPool?: boolean;
}) {
  const server = Fastify({
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
        // Don't serialize response body (volume at 5K TPS)
        res(reply) {
          return {
            statusCode: reply.statusCode,
          };
        },
      },
    },
    // Generate request IDs for tracing
    genReqId: () => crypto.randomUUID(),
    // Disable body logging in production
    disableRequestLogging: config.env === "production",
  });

  // Add request_id and tenant_id to all log entries
  server.addHook("onRequest", async (request) => {
    request.log = request.log.child({
      request_id: request.id,
    });
  });

  server.addHook("onResponse", async (request, reply) => {
    request.log.info(
      {
        tenant_id: request.tenantAuth?.tenantId,
        method: request.method,
        path: request.url,
        status: reply.statusCode,
        duration_ms: Math.round(reply.elapsedTime),
      },
      "request completed",
    );
  });

  // ── OpenAPI docs ────────────────────────────────────────────────────────
  await server.register(fastifySwagger, {
    openapi: {
      info: {
        title: "Settla API",
        description: "B2B stablecoin settlement infrastructure",
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
      const redisInstance = new IORedis.default(config.redisUrl, {
        maxRetriesPerRequest: 3,
        lazyConnect: true,
      });
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

  // ── Auth ────────────────────────────────────────────────────────────────
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

  await server.register(authPlugin, { cache: authCache, resolveTenant });

  // ── Rate Limiting ───────────────────────────────────────────────────────
  const limiter = new RateLimiter(
    redis,
    config.rateLimitWindow,
    config.rateLimitMax,
    config.rateLimitSyncIntervalMs,
  );
  limiter.start();
  server.addHook("onClose", async () => limiter.stop());

  await server.register(rateLimitPlugin, { limiter });

  // ── Metrics ─────────────────────────────────────────────────────────────
  await server.register(metricsPlugin);

  // ── Routes ──────────────────────────────────────────────────────────────
  await server.register(healthRoutes);
  await server.register(quoteRoutes, { grpc: grpcClient });
  await server.register(transferRoutes, { grpc: grpcClient });
  await server.register(treasuryRoutes, { grpc: grpcClient });

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

  const shutdown = async () => {
    server.log.info("gateway shutting down...");
    await server.close();
    process.exit(0);
  };

  process.on("SIGINT", shutdown);
  process.on("SIGTERM", shutdown);
}

start();
