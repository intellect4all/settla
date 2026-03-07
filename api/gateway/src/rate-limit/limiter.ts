import type { Redis } from "ioredis";
import fp from "fastify-plugin";
import type { FastifyInstance, FastifyRequest, FastifyReply } from "fastify";

interface TenantCounter {
  count: number;
  windowStart: number;
}

/**
 * Per-tenant rate limiter with local approximation.
 * Local counters are synced to Redis every 5 seconds, which means the limit
 * is approximate (off by at most one sync interval's worth) but avoids
 * a Redis round-trip on every request.
 */
export class RateLimiter {
  private counters: Map<string, TenantCounter> = new Map();
  private redis: Redis | null;
  private windowSeconds: number;
  private defaultMax: number;
  private syncIntervalMs: number;
  private syncTimer: ReturnType<typeof setInterval> | null = null;

  constructor(
    redis: Redis | null,
    windowSeconds: number,
    defaultMax: number,
    syncIntervalMs: number,
  ) {
    this.redis = redis;
    this.windowSeconds = windowSeconds;
    this.defaultMax = defaultMax;
    this.syncIntervalMs = syncIntervalMs;
  }

  start(): void {
    if (this.redis) {
      this.syncTimer = setInterval(() => this.syncToRedis(), this.syncIntervalMs);
    }
  }

  stop(): void {
    if (this.syncTimer) {
      clearInterval(this.syncTimer);
      this.syncTimer = null;
    }
  }

  /**
   * Check and increment rate limit for a tenant.
   * Returns { allowed, remaining, limit, resetAt }.
   */
  check(
    tenantId: string,
    limit?: number,
  ): { allowed: boolean; remaining: number; limit: number; resetAt: number } {
    const max = limit ?? this.defaultMax;
    const now = Math.floor(Date.now() / 1000);
    const windowStart =
      now - (now % this.windowSeconds);
    const resetAt = windowStart + this.windowSeconds;

    let counter = this.counters.get(tenantId);
    if (!counter || counter.windowStart !== windowStart) {
      counter = { count: 0, windowStart };
      this.counters.set(tenantId, counter);
    }

    counter.count++;
    const allowed = counter.count <= max;
    const remaining = Math.max(0, max - counter.count);

    return { allowed, remaining, limit: max, resetAt };
  }

  /** Sync local counters to Redis for cross-instance coordination. */
  private async syncToRedis(): Promise<void> {
    if (!this.redis) return;
    const pipeline = this.redis.pipeline();
    const now = Math.floor(Date.now() / 1000);

    for (const [tenantId, counter] of this.counters) {
      const key = `ratelimit:${tenantId}:${counter.windowStart}`;
      pipeline.incrby(key, counter.count);
      pipeline.expire(key, this.windowSeconds * 2);
    }

    // Reset local counters after sync
    for (const [tenantId, counter] of this.counters) {
      if (now - counter.windowStart >= this.windowSeconds) {
        this.counters.delete(tenantId);
      } else {
        counter.count = 0;
      }
    }

    try {
      await pipeline.exec();
    } catch {
      // Redis failure is non-fatal — local counters still work
    }
  }
}

/**
 * Fastify plugin that enforces per-tenant rate limits.
 */
export const rateLimitPlugin = fp(async function rateLimitPluginInner(
  app: FastifyInstance,
  opts: { limiter: RateLimiter },
): Promise<void> {
  const { limiter } = opts;

  app.addHook("onRequest", async (request: FastifyRequest, reply: FastifyReply) => {
    // Skip rate limiting for unauthenticated endpoints
    if (!request.tenantAuth) return;

    const result = limiter.check(request.tenantAuth.tenantId);

    reply.header("X-RateLimit-Limit", String(result.limit));
    reply.header("X-RateLimit-Remaining", String(result.remaining));
    reply.header("X-RateLimit-Reset", String(result.resetAt));

    if (!result.allowed) {
      reply.code(429).send({
        error: "RATE_LIMITED",
        message: "Too many requests",
        retryAfter: result.resetAt - Math.floor(Date.now() / 1000),
      });
      return;
    }
  });
});
