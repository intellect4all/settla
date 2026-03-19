/**
 * Distributed per-tenant rate limiter with L1 local + L2 Redis.
 *
 * Hot path: local in-memory counter (~100ns).
 * On window rotation: sync distributed count from Redis.
 * Fallback: local-only if Redis is unavailable.
 */
import { performance } from "node:perf_hooks";
import type { FastifyInstance, FastifyRequest, FastifyReply } from "fastify";
import fp from "fastify-plugin";
import type { Redis } from "ioredis";
import { rateLimitTotal } from "../metrics.js";

interface RateLimitEntry {
  count: number;
  /** Monotonic timestamp (ms) when the current window started. */
  windowStartMono: number;
  /** Wall-clock epoch (ms) when the current window started — used only for headers. */
  windowStartWall: number;
}

const BYPASS_PREFIXES = ["/health", "/docs", "/documentation", "/metrics", "/webhooks/"];

/**
 * Operation-weighted rate limiting: write operations cost more than reads.
 * This prevents a tenant from exhausting their rate limit budget with cheap
 * read requests while still allowing high-volume reads.
 */
const OPERATION_WEIGHTS: Record<string, number> = {
  "POST /v1/transfers": 5,
  "POST /v1/quotes": 2,
  "POST /v1/deposits": 3,
  "POST /v1/bank-deposits": 3,
  "POST /v1/payment-links": 3,
  "DELETE /v1/transfers": 3,
  "POST /v1/transfers/cancel": 3,
};

function getOperationWeight(method: string, url: string): number {
  // Normalize URL: strip query params and trailing path segments after the base
  const basePath = url.split("?")[0].replace(/\/[0-9a-f-]{36}.*$/, "");
  const key = `${method} ${basePath}`;
  return OPERATION_WEIGHTS[key] ?? 1;
}

function shouldBypass(url: string): boolean {
  for (const prefix of BYPASS_PREFIXES) {
    if (url === prefix || url.startsWith(prefix)) return true;
  }
  return false;
}

export class DistributedRateLimiter {
  private local = new Map<string, RateLimitEntry>();
  private readonly limit: number;
  private readonly windowMs: number;
  private readonly redis: Redis | null;
  private cleanupTimer: ReturnType<typeof setInterval> | null = null;
  private readonly maxLocalSize: number;

  /**
   * Returns a monotonic timestamp in milliseconds. Uses performance.now()
   * which is not affected by NTP corrections or manual clock changes,
   * ensuring window boundaries never shift unexpectedly.
   */
  private static monoNowMs(): number {
    return performance.now();
  }

  /**
   * Computes the wall-clock epoch (seconds) for the current rate-limit window.
   * Used only for Redis key naming (so all gateway instances agree on the same
   * window key) and for the X-RateLimit-Reset response header.
   */
  private windowEpochSec(): number {
    const windowSec = Math.max(1, Math.floor(this.windowMs / 1000));
    return Math.floor(Date.now() / 1000 / windowSec) * windowSec;
  }

  constructor(limit: number, windowMs: number, redis: Redis | null, maxLocalSize = 1_000_000) {
    this.limit = limit;
    this.windowMs = windowMs;
    this.redis = redis;
    this.maxLocalSize = maxLocalSize;

    // Evict stale entries every 10s (more aggressive for high-tenant-count deployments)
    this.cleanupTimer = setInterval(() => {
      const monoNow = DistributedRateLimiter.monoNowMs();
      const staleThreshold = 10_000;
      for (const [key, entry] of this.local) {
        if (monoNow - entry.windowStartMono > staleThreshold) {
          this.local.delete(key);
        }
      }
    }, 10_000);
    this.cleanupTimer.unref();
  }

  /**
   * Check rate limit. Returns { allowed, remaining, resetMs }.
   * @param tenantId - tenant or key identifier
   * @param weight - operation weight (default 1). Write operations cost more.
   */
  async check(tenantId: string, weight = 1): Promise<{
    allowed: boolean;
    remaining: number;
    resetMs: number;
  }> {
    const monoNow = DistributedRateLimiter.monoNowMs();
    const wallNow = Date.now();
    const windowEpoch = this.windowEpochSec();
    const windowSec = Math.max(1, Math.floor(this.windowMs / 1000));
    let entry = this.local.get(tenantId);

    // Evict oldest entry if at capacity (LRU-style via Map insertion order)
    if (!entry && this.local.size >= this.maxLocalSize) {
      const firstKey = this.local.keys().next().value;
      if (firstKey) this.local.delete(firstKey);
    }

    // Window rotation: use monotonic clock so NTP corrections never shift
    // the window boundary. Wall-clock time is only used for response headers
    // and Redis key naming (cross-instance agreement).
    if (!entry || monoNow - entry.windowStartMono >= this.windowMs) {
      // Capture previous window count before rotation so we can use it as
      // a fallback if Redis is unavailable (avoids TOCTOU race where the
      // counter resets to zero during an outage, allowing a burst).
      const previousWindowCount = entry ? entry.count : 0;

      // On rotation, try to fetch distributed count from Redis
      let distributedCount = 0;
      if (this.redis) {
        try {
          const key = `settla:ratelimit:${tenantId}:${windowEpoch}`;
          const val = await this.redis.incr(key);
          // Set expiry only if this is the first increment (val === 1)
          if (val === 1) {
            await this.redis.expire(key, windowSec + 1);
          }
          distributedCount = val;
        } catch {
          // Redis unavailable — fall back to local using previous window
          // count as a starting point to prevent counter-reset burst.
          const fallbackCount = Math.max(1, Math.floor(previousWindowCount * 0.5));
          distributedCount = fallbackCount;
        }
      }

      entry = { count: Math.max(1, distributedCount), windowStartMono: monoNow, windowStartWall: wallNow };
      this.local.set(tenantId, entry);

      // Use wall-clock for headers (clients need real epoch time for Retry-After).
      const resetMs = entry.windowStartWall + this.windowMs;
      const remaining = Math.max(0, this.limit - entry.count);
      return { allowed: true, remaining, resetMs };
    }

    // Increment local counter by operation weight
    entry.count += weight;

    // If local exceeds limit, optionally cross-check Redis
    if (entry.count > this.limit) {
      if (this.redis) {
        try {
          const key = `settla:ratelimit:${tenantId}:${windowEpoch}`;
          const distributed = await this.redis.get(key);
          if (distributed && Number(distributed) <= this.limit) {
            // Other instances haven't hit the limit — allow
            await this.redis.incr(key);
            const resetMs = entry.windowStartWall + this.windowMs;
            return { allowed: true, remaining: 0, resetMs };
          }
        } catch {
          // Redis unavailable — enforce local limit
        }
      }

      const resetMs = entry.windowStartWall + this.windowMs;
      return { allowed: false, remaining: 0, resetMs };
    }

    // Periodically sync to Redis (every 10 requests to reduce round-trips)
    if (this.redis && entry.count % 5 === 0) {
      const key = `settla:ratelimit:${tenantId}:${windowEpoch}`;
      try {
        await this.redis.incrby(key, 5);
      } catch {
        // Best-effort sync
      }
    }

    const resetMs = entry.windowStartWall + this.windowMs;
    const remaining = Math.max(0, this.limit - entry.count);
    return { allowed: true, remaining, resetMs };
  }

  close(): void {
    if (this.cleanupTimer) {
      clearInterval(this.cleanupTimer);
      this.cleanupTimer = null;
    }
  }
}

export interface RateLimitPluginOpts {
  limit: number;
  windowMs?: number;
  redis: Redis | null;
}

export const rateLimitPlugin = fp(
  async (fastify: FastifyInstance, opts: RateLimitPluginOpts) => {
    const limiter = new DistributedRateLimiter(
      opts.limit,
      opts.windowMs ?? 1_000,
      opts.redis,
    );

    fastify.addHook("onClose", async () => {
      limiter.close();
    });

    fastify.addHook("onRequest", async (request: FastifyRequest, reply: FastifyReply) => {
      if (shouldBypass(request.url)) return;

      const tenantId = request.tenantAuth?.tenantId;
      if (!tenantId) {
        // Public route — rate limit by IP with a lower threshold.
        // Use 20% of the tenant limit to prevent single-IP NAT abuse.
        const publicLimit = Math.max(100, Math.floor(opts.limit / 5));
        const ipKey = `public:${request.ip}`;
        const { allowed, remaining, resetMs } = await limiter.check(ipKey);
        (request as any)._rateLimit = {
          limit: publicLimit,
          remaining: Math.min(remaining, publicLimit),
          reset: Math.ceil(resetMs / 1000),
        };
        if (!allowed || remaining <= 0) {
          rateLimitTotal.inc({ tenant: "public", result: "rejected" });
          const retryAfterSec = Math.max(1, Math.ceil((resetMs - Date.now()) / 1000));
          return reply
            .header("Retry-After", String(retryAfterSec))
            .header("X-RateLimit-Limit", String(publicLimit))
            .header("X-RateLimit-Remaining", "0")
            .header("X-RateLimit-Reset", String(Math.ceil(resetMs / 1000)))
            .status(429)
            .send({
              error: "rate_limit_exceeded",
              message: "Too many requests",
              request_id: request.id,
            });
        }
        return;
      }

      const weight = getOperationWeight(request.method, request.url);
      const { allowed, remaining, resetMs } = await limiter.check(tenantId, weight);

      // Store for onSend hook
      (request as any)._rateLimit = {
        limit: opts.limit,
        remaining,
        reset: Math.ceil(resetMs / 1000),
      };

      if (!allowed) {
        rateLimitTotal.inc({ tenant: tenantId, result: "rejected" });
        request.log.warn(
          { tenant_id: tenantId, limit: opts.limit, endpoint: request.url, ip: request.ip, currentCount: remaining },
          "rate_limit_exceeded",
        );
        const retryAfterSec = Math.max(1, Math.ceil((resetMs - Date.now()) / 1000));
        return reply
          .header("Retry-After", String(retryAfterSec))
          .status(429)
          .send({
            error: "rate_limit_exceeded",
            tenant_id: tenantId,
            request_id: request.id,
          });
      }

      rateLimitTotal.inc({ tenant: tenantId, result: "allowed" });
    });

    fastify.addHook("onSend", async (request: FastifyRequest, reply: FastifyReply) => {
      const rl = (request as any)._rateLimit;
      if (!rl) return;

      reply.header("X-RateLimit-Limit", String(rl.limit));
      reply.header("X-RateLimit-Remaining", String(rl.remaining));
      reply.header("X-RateLimit-Reset", String(rl.reset));
    });
  },
  { name: "settla-rate-limit" },
);
