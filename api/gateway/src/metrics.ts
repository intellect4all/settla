import client from "prom-client";
import type { FastifyInstance } from "fastify";
import fp from "fastify-plugin";

// Collect default Node.js metrics (event loop lag, heap, GC, etc.)
client.collectDefaultMetrics({ prefix: "settla_gateway_" });

// ── HTTP request metrics ────────────────────────────────────────────────
export const httpRequestsTotal = new client.Counter({
  name: "settla_gateway_requests_total",
  help: "Total HTTP requests by tenant, method, path, and status.",
  labelNames: ["tenant", "method", "path", "status"] as const,
});

export const httpRequestDuration = new client.Histogram({
  name: "settla_gateway_request_duration_seconds",
  help: "HTTP request duration by method and path.",
  labelNames: ["method", "path"] as const,
  buckets: [0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5],
});

// ── gRPC pool metrics ───────────────────────────────────────────────────
export const grpcRequestsTotal = new client.Counter({
  name: "settla_gateway_grpc_requests_total",
  help: "gRPC requests from gateway by service, method, and status.",
  labelNames: ["service", "method", "status"] as const,
});

export const grpcPoolActive = new client.Gauge({
  name: "settla_gateway_grpc_pool_active",
  help: "Number of active gRPC connections in the pool.",
});

// ── Auth cache metrics ──────────────────────────────────────────────────
export const authCacheHits = new client.Counter({
  name: "settla_gateway_auth_cache_hits_total",
  help: "Auth cache hit count.",
});

export const authCacheMisses = new client.Counter({
  name: "settla_gateway_auth_cache_misses_total",
  help: "Auth cache miss count.",
});

// ── Rate limit metrics ──────────────────────────────────────────────────
export const rateLimitHits = new client.Counter({
  name: "settla_gateway_rate_limit_hits_total",
  help: "Rate limit rejections by tenant.",
  labelNames: ["tenant"] as const,
});

// ── Metrics endpoint plugin ─────────────────────────────────────────────
export const metricsPlugin = fp(
  async (fastify: FastifyInstance) => {
    fastify.get("/metrics", async (_request, reply) => {
      const metrics = await client.register.metrics();
      reply.header("Content-Type", client.register.contentType);
      return metrics;
    });

    // Hook to record request metrics on every response.
    fastify.addHook("onResponse", async (request, reply) => {
      const path = normalizePath(request.url);
      const method = request.method;
      const status = String(reply.statusCode);
      const tenant = request.tenantAuth?.tenantId ?? "anonymous";

      httpRequestsTotal.inc({ tenant, method, path, status });
      httpRequestDuration.observe(
        { method, path },
        reply.elapsedTime / 1000,
      );
    });
  },
  { name: "settla-metrics" },
);

/**
 * Normalize request paths to reduce label cardinality.
 * /v1/transfers/abc-123 → /v1/transfers/:id
 */
function normalizePath(url: string): string {
  const path = url.split("?")[0];
  return path.replace(
    /\/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/gi,
    "/:id",
  );
}
